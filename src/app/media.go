package app

import (
	_ "embed"
	"net/url"
	"slices"
	"strings"
	"time"
	"twitch_easy_media_alerts/src/appinfo"
	"twitch_easy_media_alerts/src/audio"
	"twitch_easy_media_alerts/src/config"
	"twitch_easy_media_alerts/src/i18n"
	"twitch_easy_media_alerts/src/overlay"
	"twitch_easy_media_alerts/src/win"

	webview2 "github.com/jchv/go-webview2"
)

const (
	videoStartTimeout = 10 * time.Second // play sent, the page hasn't started it
	videoEndGrace     = 5 * time.Second  // after the reported duration
	maxVideoLength    = 30 * time.Minute
)

var (
	errBusyVideo   = i18n.WrapError("a video is already playing", "уже играет видео", audio.ErrBusy)
	errBusyGif     = i18n.WrapError("a GIF is already showing", "уже показывается GIF", audio.ErrBusy)
	errNoVideoPage = i18n.NewError("there's nowhere to show videos: add a browser source in OBS or open the window on the OBS tab",
		"видео некуда показать — добавьте в OBS источник «Браузер» или откройте окно на вкладке «OBS»")
)

// videoWindowTitle is the title of the window OBS captures.
func videoWindowTitle() string {
	return appinfo.Title + i18n.L(" — video for OBS", " — видео для OBS")
}

// pageProblem turns what the page reports about a failed video or GIF into
// a message: "sound:" marks the GIF's sound, the rest is a code.
func pageProblem(code string) string {
	prefix := ""
	if c, ok := strings.CutPrefix(code, "sound:"); ok {
		prefix, code = i18n.L("sound: ", "звук: "), c
	}
	texts := map[string][2]string{
		"aborted":     {"loading was interrupted", "загрузка прервана"},
		"read":        {"couldn't read the file", "ошибка чтения файла"},
		"decode":      {"couldn't decode it", "ошибка декодирования"},
		"unsupported": {"the format or codec isn't supported", "формат или кодек не поддерживается"},
		"open":        {"couldn't open the file", "не удалось открыть файл"},
		"novideo":     {"the file has no video track", "в файле нет видеодорожки"},
		"image":       {"couldn't open the picture", "не удалось открыть картинку"},
	}
	if t, ok := texts[code]; ok {
		code = i18n.L(t[0], t[1])
	}
	return prefix + code
}

// videoPlayback is what is showing on the page: a video or a GIF with sound.
type videoPlayback struct {
	seq     int
	id      string
	trigger string
	busy    error // what a command that arrives meanwhile is told
	started bool
	timer   *time.Timer
}

// mediaFile returns a file for the media handler: a video command's video
// (part ""), or a GIF command's "image" or "sound".
func (a *App) mediaFile(id, part string) (string, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	file := ""
	for _, v := range slices.Concat(a.cfg.Videos, a.cfg.Rewards.Videos) {
		if v.ID == id && part == "" {
			file = v.File
		}
	}
	for _, g := range slices.Concat(a.cfg.Gifs, a.cfg.Rewards.Gifs) {
		if g.ID == id && part == "image" {
			file = g.Image
		}
		if g.ID == id && part == "sound" {
			file = g.Sound
		}
	}
	return file, file != ""
}

// mediaURL is the page-relative address of a file served by mediaFile; the
// file path makes the browser reload it when the file is changed.
func mediaURL(id, part, file string) string {
	u := "media/" + id
	if part != "" {
		u += "/" + part
	}
	return u + "?v=" + url.QueryEscape(file)
}

// lookEvent holds the parts of a play event that place the picture.
func lookEvent(l config.Look) map[string]any {
	return map[string]any{"scale": l.Scale, "x": l.X, "y": l.Y, "random": l.Random, "key": l.KeyParams()}
}

// videoTarget picks where videos go: a connected OBS browser source if there
// is one (so sound isn't doubled by the window), else the open window.
func (a *App) videoTarget() (string, bool) {
	if a.overlay.Count(overlay.PageOBS) > 0 {
		return overlay.PageOBS, true
	}
	return overlay.PageWindow, a.videoWindowShown.Load()
}

// startOnPage sends a play event to the page that shows videos and GIFs. Like
// Player.Play it returns ErrBusy (or errBusyVideo/errBusyGif) while something
// plays, unless interrupt is set. ev gets the master volume and adds the
// rest of the event.
func (a *App) startOnPage(id, trigger string, busy error, interrupt bool, ev func(master float64) map[string]any) error {
	a.mediaMu.Lock()
	defer a.mediaMu.Unlock()
	if a.video != nil || a.player.Current() != "" {
		if !interrupt {
			if a.video != nil {
				return a.video.busy
			}
			return audio.ErrBusy
		}
		a.stopVideoLocked()
		a.player.StopAndWait()
	}
	target, ok := a.videoTarget()
	if !ok {
		return errNoVideoPage
	}
	a.mu.Lock()
	master := float64(a.cfg.MasterVolume) / 100
	a.mu.Unlock()

	a.videoSeq++
	pb := &videoPlayback{seq: a.videoSeq, id: id, trigger: trigger, busy: busy}
	pb.timer = time.AfterFunc(videoStartTimeout, func() { a.videoNotStarted(pb.seq) })
	a.video = pb
	e := ev(master)
	e["type"], e["play"] = "play", pb.seq
	a.overlay.Send(e, target)
	a.emit(map[string]any{"type": "playing", "id": id})
	return nil
}

// startVideo shows a video in OBS.
func (a *App) startVideo(v config.VideoCommand, interrupt bool) error {
	return a.startOnPage(v.ID, v.Trigger, errBusyVideo, interrupt, func(master float64) map[string]any {
		e := lookEvent(v.Look)
		e["kind"] = "video"
		e["url"] = mediaURL(v.ID, "", v.File)
		e["volume"] = float64(v.Volume) / 100 * master
		return e
	})
}

// startGif shows an animated picture with its sound in OBS.
func (a *App) startGif(g config.GifCommand, interrupt bool) error {
	return a.startOnPage(g.ID, g.Trigger, errBusyGif, interrupt, func(master float64) map[string]any {
		e := lookEvent(g.Look)
		e["kind"] = "gif"
		e["image"] = mediaURL(g.ID, "image", g.Image)
		e["sound"] = ""
		if g.Sound != "" {
			e["sound"] = mediaURL(g.ID, "sound", g.Sound)
		}
		e["volume"] = float64(g.Volume) / 100 * master
		e["duration"] = g.ShowFor()
		return e
	})
}

// stopVideoLocked ends the current video. Caller holds mediaMu.
func (a *App) stopVideoLocked() {
	if a.video == nil {
		return
	}
	a.video.timer.Stop()
	a.video = nil
	a.overlay.Send(map[string]any{"type": "stop"}, "")
	a.emit(map[string]any{"type": "playing", "id": ""})
	go a.drainRedemptions()
}

// endVideo marks play request seq as finished, logging problem if set.
func (a *App) endVideo(seq int, problem string) {
	a.mediaMu.Lock()
	pb := a.video
	if pb == nil || pb.seq != seq {
		a.mediaMu.Unlock()
		return
	}
	pb.timer.Stop()
	a.video = nil
	a.emit(map[string]any{"type": "playing", "id": ""})
	a.mediaMu.Unlock()
	go a.drainRedemptions()
	if problem != "" {
		a.log("error", pb.trigger+": "+problem)
	}
}

func (a *App) videoNotStarted(seq int) {
	a.mediaMu.Lock()
	stale := a.video == nil || a.video.seq != seq || a.video.started
	a.mediaMu.Unlock()
	if !stale {
		a.endVideo(seq, i18n.L("the video window didn't respond — check that it's open", "окно видео не ответило — проверьте, что оно открыто"))
	}
}

func (a *App) onVideoReport(r overlay.Report) {
	switch r.Type {
	case "started":
		a.mediaMu.Lock()
		defer a.mediaMu.Unlock()
		pb := a.video
		if pb == nil || pb.seq != r.Play {
			return
		}
		pb.started = true
		d := maxVideoLength
		if r.Duration > 0 && r.Duration < d.Seconds() {
			d = time.Duration(r.Duration * float64(time.Second))
		}
		// A safety net in case the page never reports the end.
		pb.timer.Stop()
		pb.timer = time.AfterFunc(d+videoEndGrace, func() { a.endVideo(r.Play, "") })
	case "ended":
		a.endVideo(r.Play, "")
	case "error":
		a.endVideo(r.Play, i18n.L("couldn't play it: ", "не удалось воспроизвести: ")+pageProblem(r.Message))
	}
}

// startSound plays a sound command, keeping to one sound or video at a time.
func (a *App) startSound(cmd config.Command, interrupt bool) error {
	a.mediaMu.Lock()
	defer a.mediaMu.Unlock()
	if a.video != nil {
		if !interrupt {
			return errBusyVideo
		}
		a.stopVideoLocked()
	}
	a.mu.Lock()
	dev, vol := a.output(cmd)
	a.mu.Unlock()
	return a.play(cmd, dev, vol, interrupt)
}

// stopAll stops whatever sound or video is playing and drops queued rewards.
func (a *App) stopAll() {
	a.clearRedemptions()
	a.mediaMu.Lock()
	defer a.mediaMu.Unlock()
	a.player.Stop()
	a.stopVideoLocked()
}

func (a *App) isPlaying() bool {
	a.mediaMu.Lock()
	defer a.mediaMu.Unlock()
	return a.video != nil || a.player.Current() != ""
}

// setVideoWindow opens or hides the OBS window and remembers that. UI thread
// only, outside of WebView2 callbacks (creating a WebView2 pumps messages).
func (a *App) setVideoWindow(open bool) {
	if open {
		a.showVideoWindow(true)
	} else {
		a.hideVideoWindow()
	}
	shown := a.videoWindowShown.Load()
	a.mu.Lock()
	a.cfg.Video.WindowOpen = shown
	cfg := a.cfg
	a.mu.Unlock()
	if err := a.save(cfg); err != nil {
		a.logSaveError(err)
	}
	a.emit(map[string]any{"type": "videoWindow", "open": shown})
}

func (a *App) showVideoWindow(activate bool) {
	if a.videoWin == nil && !a.createVideoWindow() {
		return
	}
	a.videoWin.Show(activate)
	a.videoWindowShown.Store(true)
}

func (a *App) hideVideoWindow() {
	a.mediaMu.Lock()
	a.stopVideoLocked()
	a.mediaMu.Unlock()
	if a.videoWin != nil {
		a.videoWin.Hide()
	}
	a.videoWindowShown.Store(false)
}

func (a *App) createVideoWindow() bool {
	v := a.config().Video
	if v.WindowPos && win.PointOnScreen(v.WindowX+60, v.WindowY+15) {
		defer win.PlaceWindowCreation(v.WindowX, v.WindowY)()
	}
	w := webview2.NewWithOptions(webview2.WebViewOptions{
		DataPath: webviewDataPath(),
		WindowOptions: webview2.WindowOptions{
			Title:  videoWindowTitle(),
			Width:  uint(v.Width),
			Height: uint(v.Height),
			IconId: win.AppIconID,
		},
	})
	if w == nil {
		a.log("error", i18n.L("Couldn't create the video window", "Не удалось создать окно для видео"))
		return false
	}
	// Never call w.Dispatch or w.Bind: go-webview2 runs only the main
	// webview's queue. The page talks to us over HTTP instead.
	a.videoWin = win.NewOverlayShell(uintptr(w.Window()),
		func() { a.setVideoWindow(false) },
		a.onVideoWindowMoved)
	a.videoWin.SetClientSize(v.Width, v.Height)
	w.Navigate(a.overlay.PageURL(overlay.PageWindow))
	return true
}

func (a *App) onVideoWindowMoved(x, y int) {
	a.mu.Lock()
	a.cfg.Video.WindowX, a.cfg.Video.WindowY, a.cfg.Video.WindowPos = x, y, true
	cfg := a.cfg
	a.mu.Unlock()
	if err := a.save(cfg); err != nil {
		a.logSaveError(err)
	}
}
