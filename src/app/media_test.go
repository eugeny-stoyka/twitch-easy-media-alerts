package app

import (
	"bufio"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"twitch_easy_media_alerts/src/config"
	"twitch_easy_media_alerts/src/overlay"
)

func newVideoTestApp(t *testing.T) *App {
	t.Helper()
	a := New()
	a.cfgPath = filepath.Join(t.TempDir(), "settings.json")
	a.cfg = config.Default()
	a.cfg.Commands = []config.Command{{ID: "s", Enabled: true, Trigger: "!sad", File: `C:\nope\missing.mp3`, Volume: 100}}
	a.cfg.Videos = []config.VideoCommand{{ID: "v", Enabled: true, Trigger: "!clip", File: `C:\clip.mp4`, Volume: 100, Look: config.Look{Scale: 50}}}
	a.cfg.Rewards.Sounds = []config.Command{{ID: "rs", Enabled: true, Trigger: "Sad", File: `C:\nope\missing.mp3`, Volume: 100}}
	a.cfg.Rewards.Videos = []config.VideoCommand{{ID: "rv", Enabled: true, Trigger: "Clip", File: `C:\clip.mp4`, Volume: 100, Look: config.Look{Scale: 50}}}
	var err error
	if a.overlay, _, err = overlay.Start(0, config.NewID()+config.NewID(), a.mediaFile, a.onVideoReport, a.onPagesChanged); err != nil {
		t.Fatal(err)
	}
	a.overlay.SetConfig(a.cfg.Video) // as run does; pages get it first on connect
	return a
}

func TestVideoOneAtATime(t *testing.T) {
	a := newVideoTestApp(t)
	v, s := a.cfg.Videos[0], a.cfg.Commands[0]

	if err := a.startVideo(v, false); !errors.Is(err, errNoVideoPage) {
		t.Fatalf("with the window closed: %v", err)
	}
	a.videoWindowShown.Store(true)

	if err := a.startVideo(v, false); err != nil {
		t.Fatal(err)
	}
	seq := a.video.seq
	if err := a.startVideo(v, false); !errors.Is(err, errBusyVideo) {
		t.Errorf("second video: %v, want errBusyVideo", err)
	}
	if err := a.startSound(s, false); !errors.Is(err, errBusyVideo) {
		t.Errorf("sound during video: %v, want errBusyVideo", err)
	}

	a.onVideoReport(overlay.Report{Type: "started", Play: seq, Duration: 3})
	a.onVideoReport(overlay.Report{Type: "ended", Play: seq - 1}) // stale, ignored
	if a.video == nil {
		t.Fatal("stale report ended the video")
	}
	a.onVideoReport(overlay.Report{Type: "ended", Play: seq})
	if a.video != nil {
		t.Fatal("video still marked as playing after it ended")
	}

	// A preview interrupts what plays.
	if err := a.startVideo(v, false); err != nil {
		t.Fatal(err)
	}
	if err := a.startVideo(v, true); err != nil || a.video.seq != seq+2 {
		t.Errorf("interrupting preview: %v, seq %d", err, a.video.seq)
	}
	a.stopAll()
	if a.isPlaying() {
		t.Error("stopAll left something playing")
	}
}

func TestChatVideoCommand(t *testing.T) {
	a := newVideoTestApp(t)
	a.videoWindowShown.Store(true)

	a.onMessage("viewer", "!clip")
	if a.video == nil || a.video.id != "v" {
		t.Fatal("chat command did not start the video")
	}
	if _, ok := a.lastPlay["v"]; !ok {
		t.Error("cooldown not recorded")
	}
	// Sound commands are checked first.
	a.stopAll()
	a.onMessage("viewer", "!clip !sad")
	if a.video != nil {
		t.Error("video started although a sound command matched first")
	}
}

func TestVideoStartTimeout(t *testing.T) {
	a := newVideoTestApp(t)
	a.videoWindowShown.Store(true)
	if err := a.startVideo(a.cfg.Videos[0], false); err != nil {
		t.Fatal(err)
	}
	a.mediaMu.Lock()
	seq := a.video.seq
	a.video.timer.Reset(10 * time.Millisecond) // pretend the window never answers
	a.mediaMu.Unlock()
	time.Sleep(100 * time.Millisecond)
	if a.isPlaying() {
		t.Fatalf("video %d still blocks playback after the start timeout", seq)
	}
}

// readEvent returns the next "data:" payload from a server-sent events stream.
func readEvent(t *testing.T, sc *bufio.Scanner) string {
	t.Helper()
	for sc.Scan() {
		if d, ok := strings.CutPrefix(sc.Text(), "data: "); ok {
			return d
		}
	}
	t.Fatal("event stream ended")
	return ""
}

func TestVideoGoesToOBSBrowserSource(t *testing.T) {
	a := newVideoTestApp(t)
	a.videoWindowShown.Store(true)

	// The app's window page is connected too: it must not get the video.
	win, err := http.Get(a.overlay.PageURL("events?kind=window"))
	if err != nil {
		t.Fatal(err)
	}
	defer win.Body.Close()
	obs, err := http.Get(a.overlay.PageURL("events?kind=obs"))
	if err != nil {
		t.Fatal(err)
	}
	defer obs.Body.Close()
	obsEvents := bufio.NewScanner(obs.Body)
	readEvent(t, obsEvents) // config
	for a.obsPages.Load() == 0 {
		time.Sleep(10 * time.Millisecond)
	}

	a.videoWindowShown.Store(false) // OBS alone is enough
	if err := a.startVideo(a.cfg.Videos[0], false); err != nil {
		t.Fatal(err)
	}
	if ev := readEvent(t, obsEvents); !strings.Contains(ev, `"type":"play"`) {
		t.Errorf("OBS page got %s, want the play event", ev)
	}
	if n := a.overlay.Count(overlay.PageWindow); n != 1 {
		t.Errorf("window pages = %d", n)
	}

	// The window page only ever got the config.
	winEvents := bufio.NewScanner(win.Body)
	readEvent(t, winEvents)
	got := make(chan string, 1)
	go func() {
		if winEvents.Scan() {
			got <- winEvents.Text()
		}
	}()
	select {
	case line := <-got:
		if strings.Contains(line, "play") {
			t.Errorf("window page got the video too: %s", line)
		}
	case <-time.After(100 * time.Millisecond):
	}
}

func TestGifMediaAndPlayEvent(t *testing.T) {
	a := newVideoTestApp(t)
	a.cfg.Gifs = []config.GifCommand{{ID: "g", Enabled: true, Trigger: "!gif", Image: `C:\x.gif`, Sound: `C:\x.mp3`, Volume: 50,
		Look: config.Look{Scale: 30, Random: true, Key: true, KeyColor: "#00ff00", Similarity: 400, Smoothness: 80, Spill: 100}}}

	for part, want := range map[string]string{"image": `C:\x.gif`, "sound": `C:\x.mp3`} {
		if got, ok := a.mediaFile("g", part); !ok || got != want {
			t.Errorf("mediaFile(g, %s) = %q, %v", part, got, ok)
		}
	}
	if _, ok := a.mediaFile("g", ""); ok {
		t.Error("a GIF command has no video part")
	}
	if got, ok := a.mediaFile("v", ""); !ok || got != `C:\clip.mp4` {
		t.Errorf("video lookup broken: %q %v", got, ok)
	}

	obs, err := http.Get(a.overlay.PageURL("events?kind=obs"))
	if err != nil {
		t.Fatal(err)
	}
	defer obs.Body.Close()
	events := bufio.NewScanner(obs.Body)
	readEvent(t, events) // config
	for a.obsPages.Load() == 0 {
		time.Sleep(10 * time.Millisecond)
	}

	a.onMessage("viewer", "смотри !gif")
	ev := readEvent(t, events)
	for _, want := range []string{`"kind":"gif"`, `"image":"media/g/image?v=`, `"sound":"media/g/sound?v=`, `"duration":0`, `"random":true`, `"color":"#00ff00"`} {
		if !strings.Contains(ev, want) {
			t.Errorf("play event %s lacks %s", ev, want)
		}
	}
	if err := a.startVideo(a.cfg.Videos[0], false); !errors.Is(err, errBusyGif) {
		t.Errorf("video while a GIF shows: %v, want errBusyGif", err)
	}
}

func TestMissingGifFiles(t *testing.T) {
	dir := t.TempDir()
	img := filepath.Join(dir, "a.gif")
	os.WriteFile(img, []byte("GIF89a"), 0o644)
	cfg := config.Config{Gifs: []config.GifCommand{{ID: "g", Image: img, Sound: filepath.Join(dir, "nope.mp3")}}}
	if got := strings.Join(missingFiles(cfg), ","); got != "g/sound" {
		t.Errorf("missingFiles = %q, want g/sound", got)
	}
}
