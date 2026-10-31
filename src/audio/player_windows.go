package audio

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
	"twitch_easy_media_alerts/src/i18n"
	"twitch_easy_media_alerts/src/win"
	"unsafe"

	"github.com/gopxl/beep/v2"
	"github.com/gopxl/beep/v2/flac"
	"github.com/gopxl/beep/v2/mp3"
	"github.com/gopxl/beep/v2/vorbis"
	"github.com/gopxl/beep/v2/wav"
)

// decodeFile opens an audio file and returns a stereo streamer.
func decodeFile(path string) (beep.StreamSeekCloser, beep.Format, error) {
	f, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, beep.Format{}, fmt.Errorf(i18n.L("file not found: %s", "файл не найден: %s"), path)
		}
		return nil, beep.Format{}, err
	}
	var s beep.StreamSeekCloser
	var format beep.Format
	switch strings.ToLower(filepath.Ext(path)) {
	case ".mp3":
		s, format, err = mp3.Decode(f)
	case ".wav":
		s, format, err = wav.Decode(f)
	case ".ogg", ".oga":
		s, format, err = vorbis.Decode(f)
	case ".flac":
		s, format, err = flac.Decode(f)
	default:
		f.Close()
		return nil, beep.Format{}, fmt.Errorf(i18n.L("unsupported format %q (use mp3, wav, ogg or flac)", "неподдерживаемый формат %q (нужен mp3, wav, ogg или flac)"), filepath.Ext(path))
	}
	if err != nil {
		f.Close()
		return nil, beep.Format{}, fmt.Errorf(i18n.L("couldn't read %s: %w", "не удалось прочитать %s: %w"), filepath.Base(path), err)
	}
	return &fileStreamer{StreamSeekCloser: s, f: f}, format, nil
}

// fileStreamer makes sure the underlying file is closed with the decoder
// (wav and flac decoders don't own it).
type fileStreamer struct {
	beep.StreamSeekCloser
	f *os.File
}

func (s *fileStreamer) Close() error {
	err := s.StreamSeekCloser.Close()
	s.f.Close()
	return err
}

// ErrBusy is returned by Play when another sound is still playing.
var ErrBusy = i18n.NewError("another sound is already playing", "уже играет другой звук")

// MaxOverlays caps the multi-play sounds playing at once, so a chat spamming
// a command can't open dozens of audio streams.
const MaxOverlays = 10

// ErrTooMany is returned by PlayOverlay while MaxOverlays sounds play. It
// counts as ErrBusy: the sound can play once one of them ends.
var ErrTooMany = i18n.WrapError("too many sounds are playing at once", "одновременно играет слишком много звуков", ErrBusy)

// Player plays one sound at a time on a WASAPI endpoint, plus multi-play
// sounds (PlayOverlay) that sound over it and over each other.
type Player struct {
	mu       sync.Mutex
	cur      *playback
	overlays map[*playback]bool

	// OnChange gets the key of the sound that started playing, or "" when
	// playback ends. It runs under the player's lock and must not block.
	// Multi-play sounds don't report here.
	OnChange func(key string)
}

type playback struct {
	key    string
	cancel context.CancelFunc
	done   chan struct{}
}

func NewPlayer() *Player {
	return &Player{}
}

func (p *Player) changed(key string) {
	if p.OnChange != nil {
		p.OnChange(key)
	}
}

// Current returns the key of the sound being played, or "".
func (p *Player) Current() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.cur == nil {
		return ""
	}
	return p.cur.key
}

// Active reports whether any sound plays, multi-play ones included.
func (p *Player) Active() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.cur != nil || len(p.overlays) > 0
}

// Stop cuts every sound short, multi-play ones included.
func (p *Player) Stop() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.cur != nil {
		p.cur.cancel()
	}
	for pb := range p.overlays {
		pb.cancel()
	}
}

// StopAndWait cuts the current sound short and returns once it has ended
// (and OnChange has reported that). Multi-play sounds go on.
func (p *Player) StopAndWait() {
	p.mu.Lock()
	cur := p.cur
	if cur != nil {
		cur.cancel()
	}
	p.mu.Unlock()
	if cur != nil {
		<-cur.done
	}
}

// Play starts a sound in the background. If another sound is playing it
// returns ErrBusy, unless interrupt is set: then that sound is stopped first,
// which may block for a moment. done receives the playback result; warn is
// set when the chosen device was missing and the default one was used.
func (p *Player) Play(key, path, devID string, volume float64, interrupt bool, done func(warn string, err error)) error {
	p.mu.Lock()
	for p.cur != nil {
		if !interrupt {
			p.mu.Unlock()
			return ErrBusy
		}
		cur := p.cur
		cur.cancel()
		p.mu.Unlock()
		<-cur.done
		p.mu.Lock()
	}
	ctx, cancel := context.WithCancel(context.Background())
	pb := &playback{key: key, cancel: cancel, done: make(chan struct{})}
	p.cur = pb
	p.changed(key)
	p.mu.Unlock()
	go p.run(ctx, pb, path, devID, volume, done)
	return nil
}

// PlayOverlay starts a multi-play sound: it plays at once, over whatever
// plays, and neither waits for other sounds nor makes them wait. While
// MaxOverlays such sounds play it returns ErrTooMany. done is as for Play.
func (p *Player) PlayOverlay(path, devID string, volume float64, done func(warn string, err error)) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.overlays) >= MaxOverlays {
		return ErrTooMany
	}
	ctx, cancel := context.WithCancel(context.Background())
	pb := &playback{cancel: cancel, done: make(chan struct{})}
	if p.overlays == nil {
		p.overlays = map[*playback]bool{}
	}
	p.overlays[pb] = true
	go p.run(ctx, pb, path, devID, volume, done)
	return nil
}

// run plays one sound to the end (or until ctx is cancelled) and forgets it.
func (p *Player) run(ctx context.Context, pb *playback, path, devID string, volume float64, done func(warn string, err error)) {
	var warn string
	err := withCOM(func() error {
		var err error
		warn, err = playStream(ctx, path, devID, volume)
		return err
	})
	pb.cancel()
	p.mu.Lock()
	if p.cur == pb {
		p.cur = nil
		p.changed("")
	}
	delete(p.overlays, pb)
	p.mu.Unlock()
	close(pb.done)
	if done != nil {
		done(warn, err)
	}
}

func playStream(ctx context.Context, path, devID string, volume float64) (warn string, err error) {
	stream, format, err := decodeFile(path)
	if err != nil {
		return "", err
	}
	defer stream.Close()

	enum, err := createEnumerator()
	if err != nil {
		return "", err
	}
	defer win.Release(enum)

	var dev uintptr
	if devID != "" {
		dev, err = deviceByID(enum, devID)
		if err != nil {
			warn = i18n.L("the chosen output device is unavailable, so the sound went to the default device", "выбранное устройство вывода недоступно, звук пошёл на устройство по умолчанию")
		}
	}
	if dev == 0 {
		if dev, err = defaultDevice(enum); err != nil {
			return warn, err
		}
	}
	defer win.Release(dev)

	var client uintptr
	r, _, _ := syscall.SyscallN(win.Method(dev, vtDeviceActivate), dev,
		uintptr(unsafe.Pointer(&iidIAudioClient)), clsctxAll, 0, uintptr(unsafe.Pointer(&client)))
	if err := hr(r); err != nil {
		return warn, err
	}
	defer win.Release(client)

	// Stereo float32 at the file's own rate; AUTOCONVERTPCM lets the Windows
	// audio engine resample and remap channels to the device mix format.
	rate := uint32(format.SampleRate)
	wfx := waveFormatEx{
		formatTag:      waveFormatIEEEFloat,
		channels:       2,
		samplesPerSec:  rate,
		avgBytesPerSec: rate * 8,
		blockAlign:     8,
		bitsPerSample:  32,
	}
	r, _, _ = syscall.SyscallN(win.Method(client, vtClientInitialize), client,
		audclntShareModeShared, audclntStreamFlagsAutoConvert|audclntStreamFlagsSRCDefaultQ,
		bufferDuration100ns, 0, uintptr(unsafe.Pointer(&wfx)), 0)
	if err := hr(r); err != nil {
		return warn, err
	}

	var bufferFrames uint32
	r, _, _ = syscall.SyscallN(win.Method(client, vtClientGetBufferSize), client, uintptr(unsafe.Pointer(&bufferFrames)))
	if err := hr(r); err != nil {
		return warn, err
	}

	var render uintptr
	r, _, _ = syscall.SyscallN(win.Method(client, vtClientGetService), client,
		uintptr(unsafe.Pointer(&iidIAudioRenderClient)), uintptr(unsafe.Pointer(&render)))
	if err := hr(r); err != nil {
		return warn, err
	}
	defer win.Release(render)

	padding := func() (uint32, error) {
		var n uint32
		r, _, _ := syscall.SyscallN(win.Method(client, vtClientGetCurrentPadding), client, uintptr(unsafe.Pointer(&n)))
		return n, hr(r)
	}

	samples := make([][2]float64, framesPerChunk)
	gain := float32(volume)
	drained, started := false, false
	var drainStart time.Time
	defer func() {
		if started {
			syscall.SyscallN(win.Method(client, vtClientStop), client)
		}
	}()

	for ctx.Err() == nil {
		queued, err := padding()
		if err != nil {
			return warn, err
		}
		if drained {
			if queued == 0 || time.Since(drainStart) > drainTimeout {
				break
			}
		} else if avail := bufferFrames - queued; avail > 0 {
			var data *float32
			r, _, _ := syscall.SyscallN(win.Method(render, vtRenderGetBuffer), render, uintptr(avail), uintptr(unsafe.Pointer(&data)))
			if err := hr(r); err != nil {
				return warn, err
			}
			out := unsafe.Slice(data, int(avail)*2)
			written := 0
			for written < int(avail) {
				n, ok := stream.Stream(samples[:min(framesPerChunk, int(avail)-written)])
				for i := range n {
					out[(written+i)*2] = clip(float32(samples[i][0]) * gain)
					out[(written+i)*2+1] = clip(float32(samples[i][1]) * gain)
				}
				written += n
				if !ok || n == 0 {
					drained, drainStart = true, time.Now()
					break
				}
			}
			r, _, _ = syscall.SyscallN(win.Method(render, vtRenderReleaseBuffer), render, uintptr(written), 0)
			if err := hr(r); err != nil {
				return warn, err
			}
			if !started {
				r, _, _ = syscall.SyscallN(win.Method(client, vtClientStart), client)
				if err := hr(r); err != nil {
					return warn, err
				}
				started = true
			}
		}
		select {
		case <-ctx.Done():
		case <-time.After(pollInterval):
		}
	}
	if err := stream.Err(); err != nil {
		return warn, fmt.Errorf(i18n.L("decoding error: %w", "ошибка декодирования: %w"), err)
	}
	return warn, nil
}

func clip(v float32) float32 {
	return max(-1, min(v, 1))
}
