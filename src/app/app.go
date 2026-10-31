// Package app ties the app together: the main window and its page, chat
// commands and channel point rewards, and playing sounds, videos and GIFs
// one at a time.
package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"twitch_easy_media_alerts/src/appinfo"
	"twitch_easy_media_alerts/src/audio"
	"twitch_easy_media_alerts/src/config"
	"twitch_easy_media_alerts/src/i18n"
	"twitch_easy_media_alerts/src/overlay"
	"twitch_easy_media_alerts/src/twitch"
	"twitch_easy_media_alerts/src/ui"
	"twitch_easy_media_alerts/src/win"

	webview2 "github.com/jchv/go-webview2"
)

const maxLogEntries = 300

type LogEntry struct {
	Time string `json:"time"`
	Kind string `json:"kind"` // play, video, skip, info, warn, error
	Text string `json:"text"`
}

type App struct {
	mu       sync.Mutex
	cfg      config.Config
	cfgPath  string // guarded by cfgFileMu once the app runs
	status   twitch.ChatStatus
	logs     []LogEntry
	lastPlay map[string]time.Time
	lastAny  time.Time

	// cfgFileMu keeps writing the settings file and moving it apart. Take it
	// before mu, never while holding mu.
	cfgFileMu sync.Mutex

	// checkFiles asks fileChecker to look for missing files again.
	checkFiles chan struct{}

	chatMu sync.Mutex // serialises connect/disconnect; never held with mu
	chat   *twitch.ChatClient

	// Channel points. pointsMu serialises starting/stopping; never held with mu.
	pointsMu    sync.Mutex
	points      *twitch.PointsListener
	loginCancel context.CancelFunc
	pointsSt    pointsStatus // guarded by mu

	// Rewards and single-play chat commands waiting for the current sound or
	// video to end. Take redeemMu before mediaMu.
	redeemMu    sync.Mutex
	redeemQueue []pendingPlay

	// mediaMu keeps to one sound or video at a time. Take it before mu.
	mediaMu   sync.Mutex
	player    *audio.Player
	video     *videoPlayback
	multiGifs map[int]*videoPlayback // multi-play GIFs showing, by play number
	videoSeq  int

	overlay          *overlay.Server
	obsPages         atomic.Int32      // connected OBS browser sources
	videoWin         *win.OverlayShell // UI thread only
	videoWindowShown atomic.Bool

	w     webview2.WebView
	shell *win.Shell
}

// New makes the app; Run starts it.
func New() *App {
	a := &App{
		status:     twitch.ChatStatus{State: twitch.StateDisconnected},
		lastPlay:   map[string]time.Time{},
		player:     audio.NewPlayer(),
		multiGifs:  map[int]*videoPlayback{},
		checkFiles: make(chan struct{}, 1),
	}
	a.player.OnChange = func(id string) {
		a.emit(map[string]any{"type": "playing", "id": id})
		if id == "" {
			go a.drainRedemptions()
		}
	}
	return a
}

func webviewDataPath() string {
	return filepath.Join(config.AppDataDir(), "WebView2")
}

func (a *App) Run() {
	cfgPath, migratedFrom, unavailable, resolveErr := config.Resolve()
	a.cfgPath = cfgPath
	cfg, loadErr := config.Load(a.cfgPath)
	i18n.SetLanguage(cfg.Language) // before the first message
	if unavailable != "" {
		a.log("warn", fmt.Sprintf(i18n.L("The settings folder %s is unavailable, so the settings come from %s",
			"Папка настроек %s недоступна — настройки взяты из %s"), unavailable, filepath.Dir(cfgPath)))
	}
	if resolveErr != nil {
		a.log("error", i18n.L("Couldn't prepare the settings folder: ", "Не удалось подготовить папку настроек: ")+resolveErr.Error())
	} else if migratedFrom != "" {
		a.log("info", fmt.Sprintf(i18n.L("Settings moved from %s to %s", "Настройки перенесены из %s в %s"), migratedFrom, cfgPath))
	}
	if loadErr != nil {
		broken := a.cfgPath + ".broken"
		_ = os.Rename(a.cfgPath, broken)
		a.log("error", fmt.Sprintf(i18n.L("The settings file is damaged (%v); it was kept as %s and the default settings were loaded.",
			"Файл настроек повреждён (%v), он сохранён как %s. Загружены настройки по умолчанию."), loadErr, filepath.Base(broken)))
	}
	cfg.Normalize()
	a.cfg = cfg
	// Save right away so a freshly made browser source token is kept.
	if err := a.saveConfig(); err != nil {
		a.logSaveError(err)
	}

	var portBusy bool
	var err error
	a.overlay, portBusy, err = overlay.Start(cfg.Video.Port, cfg.Video.Token, a.mediaFile, a.onVideoReport, a.onPagesChanged)
	if err != nil {
		win.MessageBox(appinfo.Title, i18n.L("Couldn't start the local server for videos: ", "Не удалось запустить локальный сервер для видео: ")+err.Error())
		return
	}
	if portBusy {
		a.log("warn", fmt.Sprintf(i18n.L("Port %d is taken by another program, so until a restart the OBS browser source address is different: %s",
			"Порт %d занят другой программой, поэтому до перезапуска адрес для источника «Браузер» в OBS другой: %s"),
			cfg.Video.Port, a.overlay.PageURL(overlay.PageOBS)))
	}
	a.overlay.SetConfig(cfg.Video)

	scale := float64(win.SystemDPI()) / 96
	var unhook func()
	if cfg.StartMinimized {
		unhook = win.HideWindowCreation()
	}
	w := webview2.NewWithOptions(webview2.WebViewOptions{
		AutoFocus: true,
		DataPath:  webviewDataPath(),
		WindowOptions: webview2.WindowOptions{
			Title:  appinfo.Title,
			Width:  uint(1040 * scale),
			Height: uint(800 * scale),
			IconId: win.AppIconID,
			Center: true,
		},
	})
	if unhook != nil {
		unhook()
	}
	if w == nil {
		win.MessageBox(appinfo.Title, i18n.L("Couldn't open the app window.\n\n"+
			"It needs the Microsoft Edge WebView2 Runtime: download it from Microsoft "+
			"(https://go.microsoft.com/fwlink/p/?LinkId=2124703) and start the app again.",
			"Не удалось запустить окно приложения.\n\n"+
				"Нужен компонент Microsoft Edge WebView2 Runtime — скачайте его на сайте Microsoft "+
				"(https://go.microsoft.com/fwlink/p/?LinkId=2124703) и запустите программу снова."))
		return
	}
	a.w = w
	w.SetSize(int(760*scale), int(560*scale), webview2.HintMin)

	a.shell = win.NewShell(uintptr(w.Window()))
	a.shell.MinimizeToTray = func() bool { return a.config().MinimizeToTray }
	a.shell.CloseToTray = func() bool { return a.config().CloseToTray }
	a.shell.Menu = a.trayMenu
	if cfg.StartMinimized {
		a.shell.FinishHiddenStart()
	}
	a.updateTrayTip()

	go a.fileChecker()
	a.bind()
	w.SetHtml(ui.Index)

	if cfg.Video.WindowOpen {
		a.showVideoWindow(false)
		if !cfg.StartMinimized {
			a.shell.Show()
		}
	}

	go a.restartPoints()

	if cfg.AutoConnect && cfg.Channel != "" {
		if login, err := twitch.ParseChannel(cfg.Channel); err == nil {
			go a.setChannel(login)
		}
	}

	w.Run()

	a.stopAll()
	done := make(chan struct{})
	go func() {
		a.setChannel("")
		a.pointsMu.Lock()
		a.stopPointsLocked()
		a.pointsMu.Unlock()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
	}
}

// onPagesChanged runs when a video page connects or disconnects.
func (a *App) onPagesChanged(obs int) {
	prev := a.obsPages.Swap(int32(obs))
	switch {
	case prev == 0 && obs > 0:
		a.log("info", i18n.L("OBS connected: videos will show in the browser source", "OBS подключился: видео будут показываться в источнике «Браузер»"))
	case prev > 0 && obs == 0:
		a.log("info", i18n.L("OBS disconnected from the browser source", "OBS отключился от источника «Браузер»"))
	}
	a.emit(map[string]any{"type": "pages", "obs": obs})
}

// saveConfig writes the current settings to the file. Change a.cfg first
// (under mu), then call this without holding mu. The copy is taken under
// cfgFileMu, so an older copy can never overwrite a newer one.
func (a *App) saveConfig() error {
	a.cfgFileMu.Lock()
	defer a.cfgFileMu.Unlock()
	return config.Save(a.cfgPath, a.config())
}

// logSaveError reports settings that couldn't be written.
func (a *App) logSaveError(err error) {
	a.log("error", i18n.L("Couldn't save the settings: ", "Не удалось сохранить настройки: ")+err.Error())
}

func (a *App) configPath() string {
	a.cfgFileMu.Lock()
	defer a.cfgFileMu.Unlock()
	return a.cfgPath
}

func (a *App) config() config.Config {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.cfg
}

// emit sends an event to the page (window.onAppEvent). Safe from any goroutine.
func (a *App) emit(ev map[string]any) {
	if a.w == nil {
		return
	}
	b, err := json.Marshal(ev)
	if err != nil {
		return
	}
	a.w.Dispatch(func() { a.evalEvent(b) })
}

// evalEvent hands an event in JSON to the page. UI thread only.
func (a *App) evalEvent(b []byte) {
	a.w.Eval("window.onAppEvent && window.onAppEvent(" + string(b) + ")")
}

func (a *App) log(kind, text string) {
	e := LogEntry{Time: time.Now().Format("15:04:05"), Kind: kind, Text: text}
	a.mu.Lock()
	a.logs = append(a.logs, e)
	if len(a.logs) > maxLogEntries {
		a.logs = append([]LogEntry(nil), a.logs[len(a.logs)-maxLogEntries:]...)
	}
	a.mu.Unlock()
	a.emit(map[string]any{"type": "log", "entry": e})
}

// setChannel stops the current chat connection and, if login is not empty,
// joins that channel. Blocks until the old connection is closed.
func (a *App) setChannel(login string) {
	a.chatMu.Lock()
	defer a.chatMu.Unlock()
	if a.chat != nil {
		a.chat.Stop()
		a.chat = nil
	}
	if login == "" {
		return
	}
	a.chat = twitch.StartChat(login, twitch.ChatCallbacks{
		OnStatus:  a.onStatus,
		OnMessage: a.onMessage,
		OnNotice:  func(text string) { a.log("warn", "Twitch: "+text) },
	})
}

func (a *App) onStatus(st twitch.ChatStatus) {
	a.mu.Lock()
	a.status = st
	a.mu.Unlock()
	a.emit(map[string]any{"type": "status", "status": st})
	switch st.State {
	case twitch.StateConnected:
		a.log("info", i18n.L("Connected to chat #", "Подключено к чату #")+st.Channel)
	case twitch.StateReconnecting:
		a.log("warn", st.Message)
	case twitch.StateDisconnected:
		a.log("info", i18n.L("Disconnected from chat #", "Отключено от чата #")+st.Channel)
	}
	a.updateTrayTip()
}

func describeStatus(st twitch.ChatStatus) string {
	switch st.State {
	case twitch.StateConnected:
		return i18n.L("● In chat #", "● В чате #") + st.Channel
	case twitch.StateConnecting:
		return i18n.L("Connecting to #", "Подключение к #") + st.Channel + "…"
	case twitch.StateReconnecting:
		return i18n.L("Reconnecting to #", "Переподключение к #") + st.Channel + "…"
	}
	return i18n.L("Not connected", "Не подключено")
}

func (a *App) updateTrayTip() {
	a.mu.Lock()
	tip := appinfo.Title + "\n" + describeStatus(a.status)
	if !a.cfg.CommandsEnabled {
		tip += i18n.L("\nAll commands off", "\nВсе команды отключены")
	}
	a.mu.Unlock()
	if a.w == nil {
		return
	}
	a.w.Dispatch(func() {
		if a.shell != nil {
			a.shell.SetTip(tip)
		}
	})
}

func cooldownLeft(last time.Time, seconds int, now time.Time) int {
	if seconds <= 0 || last.IsZero() {
		return 0
	}
	left := time.Duration(seconds)*time.Second - now.Sub(last)
	if left <= 0 {
		return 0
	}
	return int(math.Ceil(left.Seconds()))
}

// setCommandsEnabled flips the master switch for all commands.
func (a *App) setCommandsEnabled(on bool) {
	a.mu.Lock()
	changed := a.cfg.CommandsEnabled != on
	a.cfg.CommandsEnabled = on
	a.mu.Unlock()
	if !changed {
		return
	}
	if err := a.saveConfig(); err != nil {
		a.logSaveError(err)
	}
	if on {
		a.log("info", i18n.L("All commands on", "Все команды включены"))
	} else {
		a.log("warn", i18n.L("All commands off — sounds and videos from chat don't play", "Все команды отключены — звуки и видео из чата не играют"))
		a.dropQueuedChat()
	}
	a.emit(map[string]any{"type": "commandsEnabled", "value": on})
	a.updateTrayTip()
}

// onMessage handles a chat message: the first matching sound command wins,
// then video commands are checked, then GIFs. Multi-play sounds and GIFs
// start at once, over whatever plays.
func (a *App) onMessage(user, text string) {
	a.mu.Lock()
	if !a.cfg.CommandsEnabled {
		a.mu.Unlock()
		return
	}
	var (
		id, trigger, file string
		cooldown          int
		multi             bool
		start             func() error
		kind              = "play"
	)
	if i := twitch.MatchCommand(a.cfg.Commands, text); i >= 0 {
		cmd := a.cfg.Commands[i]
		id, trigger, file, cooldown, multi = cmd.ID, cmd.Trigger, cmd.File, cmd.Cooldown, cmd.MultiPlay
		start = func() error { return a.startSound(cmd, false) }
		if cmd.MultiPlay {
			start = func() error { return a.startSoundMulti(cmd) }
		}
	} else if i := twitch.MatchCommand(a.cfg.Videos, text); i >= 0 {
		v := a.cfg.Videos[i]
		id, trigger, file, cooldown = v.ID, v.Trigger, v.File, v.Cooldown
		start = func() error { return a.startVideo(v, false) }
		kind = "video"
	} else if i := twitch.MatchCommand(a.cfg.Gifs, text); i >= 0 {
		g := a.cfg.Gifs[i]
		id, trigger, file, cooldown, multi = g.ID, g.Trigger, g.Image, g.Cooldown, g.MultiPlay
		start = func() error { return a.startGif(g, false) }
		if g.MultiPlay {
			start = func() error { return a.startGifMulti(g) }
		}
		kind = "gif"
	} else {
		a.mu.Unlock()
		return
	}
	now := time.Now()
	who := user + ": " + trigger
	if file == "" {
		a.mu.Unlock()
		a.log("warn", who+i18n.L(" — the command has no file chosen", " — у команды не выбран файл"))
		return
	}
	if left := cooldownLeft(a.lastPlay[id], cooldown, now); left > 0 {
		a.mu.Unlock()
		a.log("skip", fmt.Sprintf(i18n.L("%s — command cooldown, %d s left", "%s — кулдаун команды, ещё %d сек."), who, left))
		return
	}
	if left := cooldownLeft(a.lastAny, a.cfg.GlobalCooldown, now); left > 0 {
		a.mu.Unlock()
		a.log("skip", fmt.Sprintf(i18n.L("%s — global cooldown, %d s left", "%s — общий кулдаун, ещё %d сек."), who, left))
		return
	}
	a.mu.Unlock()

	// One sound or video at a time: while one plays, a single-play command
	// that isn't on cooldown (checked above) waits for its turn. Its cooldown
	// starts now, so repeating it meanwhile is skipped as usual.
	p := pendingPlay{who: who, file: file, kind: kind, multi: multi, chat: true, start: start}
	if !a.playChat(p, !multi) {
		return
	}
	a.mu.Lock()
	a.lastPlay[id] = now
	a.lastAny = now
	a.mu.Unlock()
}

// playChat starts a chat command. While something plays, it waits in the
// queue if canWait is set and is skipped otherwise. It reports whether the
// command was taken (started or queued), so that its cooldown counts.
func (a *App) playChat(p pendingPlay, canWait bool) bool {
	a.redeemMu.Lock()
	defer a.redeemMu.Unlock()
	// A command that may wait keeps its turn behind the single-play ones
	// waiting already.
	if !canWait || !slices.ContainsFunc(a.redeemQueue, func(q pendingPlay) bool { return !q.multi }) {
		err := p.start()
		if err == nil {
			a.log(p.kind, p.who+" → "+filepath.Base(p.file))
			return true
		}
		if !canWait || !errors.Is(err, audio.ErrBusy) {
			a.logChatSkip(p.who, err)
			return false
		}
	}
	return a.enqueueLocked(p)
}

// logChatSkip logs why a chat command didn't play.
func (a *App) logChatSkip(who string, err error) {
	switch {
	case errors.Is(err, audio.ErrTooMany), errors.Is(err, errTooManyGifs):
		a.log("skip", who+" — "+err.Error())
	case errors.Is(err, errBusyVideo):
		a.log("skip", who+i18n.L(" — a video is already playing", " — уже играет видео"))
	case errors.Is(err, errBusyGif):
		a.log("skip", who+i18n.L(" — a GIF is already showing", " — уже показывается GIF"))
	case errors.Is(err, audio.ErrBusy):
		a.log("skip", who+i18n.L(" — a sound is already playing", " — уже играет звук"))
	default:
		a.log("warn", who+" — "+err.Error())
	}
}

// output resolves the device and final gain for a command. Caller holds mu.
func (a *App) output(cmd config.Command) (device string, volume float64) {
	device = cmd.Device
	if device == "" {
		device = a.cfg.Device
	}
	return device, float64(cmd.Volume) / 100 * float64(a.cfg.MasterVolume) / 100
}

// play starts cmd's sound; see Player.Play for interrupt and ErrBusy.
func (a *App) play(cmd config.Command, device string, volume float64, interrupt bool) error {
	return a.player.Play(cmd.ID, cmd.File, device, volume, interrupt, func(warn string, err error) {
		a.soundDone(cmd, warn, err)
	})
}

// soundDone logs how cmd's sound went once it has ended.
func (a *App) soundDone(cmd config.Command, warn string, err error) {
	if warn != "" {
		a.log("warn", cmd.Trigger+": "+warn)
	}
	if err != nil {
		a.log("error", cmd.Trigger+": "+err.Error())
	}
}

func (a *App) trayMenu() []win.MenuItem {
	a.mu.Lock()
	st, channel, enabled := a.status, a.cfg.Channel, a.cfg.CommandsEnabled
	a.mu.Unlock()

	items := []win.MenuItem{
		{Label: i18n.L("Open window", "Открыть окно"), Default: true, Action: a.shell.Show},
		{Label: describeStatus(st), Disabled: true},
		{Separator: true},
	}
	if st.State == twitch.StateDisconnected {
		items = append(items, win.MenuItem{Label: i18n.L("Connect", "Подключиться"), Disabled: channel == "", Action: func() {
			if err := a.connect(channel); err != nil {
				a.log("error", err.Error())
			}
		}})
	} else {
		items = append(items, win.MenuItem{Label: i18n.L("Disconnect", "Отключиться"), Action: func() { go a.setChannel("") }})
	}
	toggleLabel := i18n.L("Turn off all commands", "Отключить все команды")
	if !enabled {
		toggleLabel = i18n.L("Turn on all commands", "Включить все команды")
	}
	return append(items,
		win.MenuItem{Label: toggleLabel, Action: func() { a.setCommandsEnabled(!enabled) }},
		win.MenuItem{Label: i18n.L("Stop sound / video", "Остановить звук / видео"), Disabled: !a.isPlaying(), Action: a.stopAll},
		win.MenuItem{Separator: true},
		win.MenuItem{Label: i18n.L("Exit", "Выход"), Action: a.shell.Quit},
	)
}

func (a *App) connect(input string) error {
	login, err := twitch.ParseChannel(input)
	if err != nil {
		return err
	}
	a.mu.Lock()
	a.cfg.Channel = input
	a.mu.Unlock()
	if err := a.saveConfig(); err != nil {
		a.logSaveError(err)
	}
	go a.setChannel(login)
	return nil
}

// missingFiles lists IDs of commands whose file doesn't exist; a GIF
// command's missing sound is listed as "<id>/sound".
func missingFiles(cfg config.Config) []string {
	ids := []string{}
	check := func(id, file string) {
		if file == "" {
			return
		}
		if _, err := os.Stat(file); err != nil {
			ids = append(ids, id)
		}
	}
	for _, c := range slices.Concat(cfg.Commands, cfg.Rewards.Sounds) {
		check(c.ID, c.File)
	}
	for _, v := range slices.Concat(cfg.Videos, cfg.Rewards.Videos) {
		check(v.ID, v.File)
	}
	for _, g := range slices.Concat(cfg.Gifs, cfg.Rewards.Gifs) {
		check(g.ID, g.Image)
		check(g.ID+"/sound", g.Sound)
	}
	return ids
}

func checkFile(file, what string) error {
	if file == "" {
		return fmt.Errorf(i18n.L("choose %s first", "сначала выберите %s"), what)
	}
	if _, err := os.Stat(file); err != nil {
		return fmt.Errorf(i18n.L("file not found: %s", "файл не найден: %s"), file)
	}
	return nil
}

type initialState struct {
	Config          config.Config     `json:"config"`
	Devices         []audio.Device    `json:"devices"`
	DeviceError     string            `json:"deviceError"`
	Missing         []string          `json:"missing"`
	Status          twitch.ChatStatus `json:"status"`
	Logs            []LogEntry        `json:"logs"`
	Playing         string            `json:"playing"` // ID of the command whose sound or video is playing
	MediaBase       string            `json:"mediaBase"`
	ObsURL          string            `json:"obsUrl"`   // for the OBS browser source
	ObsPages        int               `json:"obsPages"` // connected browser sources
	VideoWindowOpen bool              `json:"videoWindowOpen"`
	Points          pointsStatus      `json:"points"`
	ConfigPath      string            `json:"configPath"`
	SettingsLocked  bool              `json:"settingsLocked"` // the folder comes from the environment
	Version         string            `json:"version"`
}

// initialState gathers what the page needs to start. devices, devErr and
// missing come from slow checks made beforehand, off the UI thread.
func (a *App) initialState(devices []audio.Device, devErr error, missing []string) initialState {
	playing := a.player.Current()
	a.mediaMu.Lock()
	if a.video != nil {
		playing = a.video.id
	}
	a.mediaMu.Unlock()
	cfgPath := a.configPath() // cfgFileMu can't be taken under mu
	a.mu.Lock()
	defer a.mu.Unlock()
	st := initialState{
		Config:          a.cfg,
		Devices:         devices,
		Missing:         missing,
		Status:          a.status,
		Logs:            append([]LogEntry(nil), a.logs...),
		Playing:         playing,
		MediaBase:       a.overlay.MediaBase(),
		ObsURL:          a.overlay.PageURL(overlay.PageOBS),
		ObsPages:        int(a.obsPages.Load()),
		VideoWindowOpen: a.videoWindowShown.Load(),
		Points:          a.pointsSt,
		ConfigPath:      cfgPath,
		SettingsLocked:  config.DirLocked(),
		Version:         appinfo.Version,
	}
	if devErr != nil {
		st.DeviceError = devErr.Error()
	}
	return st
}

// recheckFiles asks for the commands' files to be checked again; the page
// gets a "missing" event. Requests made while a check runs fold into one
// more check, so the last event always matches the latest settings.
func (a *App) recheckFiles() {
	select {
	case a.checkFiles <- struct{}{}:
	default:
	}
}

// fileChecker checks the files off the UI thread: a sleeping network drive
// or an unplugged one can take seconds to answer.
func (a *App) fileChecker() {
	for range a.checkFiles {
		a.emit(map[string]any{"type": "missing", "ids": missingFiles(a.config())})
	}
}

// bind exposes Go functions to the page. They run on the UI thread, so
// anything slow or modal is pushed out of the WebView2 callback.
func (a *App) bind() {
	// goGetInitial answers with an "initial" event. Listing the devices and
	// checking the files can be slow, so they run off the UI thread; the rest
	// of the state is taken on the UI thread right as it's sent, so every
	// event emitted after it reaches the page after it.
	a.w.Bind("goGetInitial", func() {
		go func() {
			devices, devErr := audio.ListOutputDevices()
			missing := missingFiles(a.config())
			a.w.Dispatch(func() {
				b, err := json.Marshal(map[string]any{"type": "initial", "state": a.initialState(devices, devErr, missing)})
				if err == nil {
					a.evalEvent(b)
				}
			})
		}()
	})

	// goSaveConfig takes the page's settings; whether their files exist comes
	// back as a "missing" event.
	a.w.Bind("goSaveConfig", func(cfg config.Config) error {
		cfg.Normalize()
		a.mu.Lock()
		old := a.cfg.Video
		// The window's state and the server address belong to Go, not to the page.
		cfg.Video.WindowOpen, cfg.Video.WindowX, cfg.Video.WindowY, cfg.Video.WindowPos =
			old.WindowOpen, old.WindowX, old.WindowY, old.WindowPos
		cfg.Video.Port, cfg.Video.Token = old.Port, old.Token
		oldTwitch := a.cfg.Twitch
		cfg.Twitch.Login, cfg.Twitch.UserID, cfg.Twitch.Tokens = oldTwitch.Login, oldTwitch.UserID, oldTwitch.Tokens
		// A login belongs to the Client ID it was made with.
		clientChanged := cfg.Twitch.ClientID != oldTwitch.ClientID && oldTwitch.Tokens != ""
		a.cfg = cfg
		a.mu.Unlock()
		a.recheckFiles()
		if i18n.Russian() != (cfg.Language == "ru") {
			i18n.SetLanguage(cfg.Language)
			a.updateTrayTip()
		}
		if cfg.Video.Width != old.Width || cfg.Video.Height != old.Height || cfg.Video.Background != old.Background {
			a.overlay.SetConfig(cfg.Video)
			if a.videoWin != nil {
				a.videoWin.SetClientSize(cfg.Video.Width, cfg.Video.Height)
			}
		}
		if err := a.saveConfig(); err != nil {
			return fmt.Errorf(i18n.L("couldn't save the settings: %w", "не удалось сохранить настройки: %w"), err)
		}
		if clientChanged {
			go a.twitchLogout()
		}
		return nil
	})

	a.w.Bind("goConnect", a.connect)

	a.w.Bind("goDisconnect", func() { go a.setChannel("") })

	// goBrowse picks a file of kind (sound, video, image) for field of command id.
	a.w.Bind("goBrowse", func(id, field, current, kind string) {
		a.w.Dispatch(func() {
			if path, ok := a.shell.OpenMediaDialog(current, kind); ok {
				a.emit(map[string]any{"type": "fileChosen", "id": id, "field": field, "path": path})
			}
		})
	})

	a.w.Bind("goTest", func(cmd config.Command) error {
		cmd.Volume = max(0, min(cmd.Volume, 200))
		if err := checkFile(cmd.File, i18n.L("a sound file", "звуковой файл")); err != nil {
			return err
		}
		// A preview replaces whatever is playing; stopping it may take a
		// moment, so don't wait on the UI thread.
		go a.startSound(cmd, true)
		return nil
	})

	// A preview on the page replaces what plays. With no browser source
	// connected and the window closed, the window is opened to show it.
	preview := func(trigger string, start func() error) {
		a.w.Dispatch(func() {
			if _, ok := a.videoTarget(); !ok {
				a.setVideoWindow(true)
			}
			go func() {
				if err := start(); err != nil {
					a.log("error", trigger+": "+err.Error())
				}
			}()
		})
	}

	a.w.Bind("goTestVideo", func(v config.VideoCommand) error {
		if err := checkFile(v.File, i18n.L("a video file", "видеофайл")); err != nil {
			return err
		}
		preview(v.Trigger, func() error { return a.startVideo(v, true) })
		return nil
	})

	a.w.Bind("goTestGif", func(g config.GifCommand) error {
		if err := checkFile(g.Image, i18n.L("a picture", "картинку")); err != nil {
			return err
		}
		if g.Sound != "" {
			if err := checkFile(g.Sound, i18n.L("a sound", "звук")); err != nil {
				return err
			}
		}
		preview(g.Trigger, func() error { return a.startGif(g, true) })
		return nil
	})

	a.w.Bind("goSetVideoWindow", func(open bool) {
		a.w.Dispatch(func() { a.setVideoWindow(open) })
	})

	a.w.Bind("goTwitchLogin", a.twitchLogin)

	a.w.Bind("goTwitchCancelLogin", func() { go a.cancelTwitchLogin() })

	a.w.Bind("goTwitchLogout", func() { go a.twitchLogout() })

	a.w.Bind("goTwitchRewards", func() { go a.loadRewards() })

	a.w.Bind("goOpenURL", win.OpenURL)

	a.w.Bind("goSetCommandsEnabled", a.setCommandsEnabled)

	a.w.Bind("goStop", a.stopAll)

	// goRefreshDevices lists the output devices off the UI thread and answers
	// with a "devices" event.
	a.w.Bind("goRefreshDevices", func() {
		go func() {
			devices, err := audio.ListOutputDevices()
			ev := map[string]any{"type": "devices", "devices": devices, "error": ""}
			if err != nil {
				ev["error"] = err.Error()
			}
			a.emit(ev)
		}()
	})

	// The settings folder: pick one and move the file there (asking before
	// replacing settings already in it), or show it in Explorer.
	a.w.Bind("goPickSettingsDir", func() {
		a.w.Dispatch(func() {
			dir, ok := a.shell.PickFolder(i18n.L("Settings folder", "Папка для настроек"), filepath.Dir(a.configPath()))
			if !ok {
				return
			}
			target := filepath.Join(dir, config.FileName)
			if _, err := os.Stat(target); err == nil && !strings.EqualFold(target, a.configPath()) &&
				!a.shell.Ask(appinfo.Title, fmt.Sprintf(i18n.L(
					"The folder \"%s\" already has a settings file.\n\nReplace it with the current settings? The old file will be kept as settings.json.bak.",
					"В папке «%s» уже есть файл настроек.\n\nЗаменить его текущими настройками? Прежний файл сохранится как settings.json.bak."), dir)) {
				return
			}
			go func() {
				path, err := a.moveSettings(dir)
				if err != nil {
					a.log("error", i18n.L("Couldn't change the settings folder: ", "Не удалось сменить папку настроек: ")+err.Error())
					return
				}
				a.log("info", i18n.L("Settings are now kept in ", "Настройки теперь хранятся в ")+path)
				a.emit(map[string]any{"type": "configPath", "path": path})
			}()
		})
	})

	a.w.Bind("goOpenSettingsDir", func() { win.OpenFolder(filepath.Dir(a.configPath())) })

	// goReloadPage loads the page anew, e.g. in another language.
	a.w.Bind("goReloadPage", func() { a.w.Dispatch(func() { a.w.SetHtml(ui.Index) }) })

	a.w.Bind("goClipboardText", win.ClipboardText)

	a.w.Bind("goCopyText", win.SetClipboardText)

	a.w.Bind("goClearLog", func() {
		a.mu.Lock()
		a.logs = nil
		a.mu.Unlock()
	})
}

// moveSettings moves the settings file into dir and remembers that folder.
// A settings file already there is kept as settings.json.bak.
func (a *App) moveSettings(dir string) (string, error) {
	if config.DirLocked() {
		return "", fmt.Errorf(i18n.L("the settings folder is set by the %s environment variable", "папку настроек задаёт переменная окружения %s"), config.DirEnv)
	}
	a.cfgFileMu.Lock()
	defer a.cfgFileMu.Unlock()
	old := a.cfgPath
	path := filepath.Join(filepath.Clean(dir), config.FileName)
	if strings.EqualFold(path, old) {
		return path, nil
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf(i18n.L("couldn't create the folder: %w", "не удалось создать папку: %w"), err)
	}
	if _, err := os.Stat(path); err == nil {
		if err := os.Rename(path, path+".bak"); err != nil {
			return "", fmt.Errorf(i18n.L("couldn't keep the old settings file as .bak: %w", "не удалось сохранить прежний файл настроек как .bak: %w"), err)
		}
	}
	if err := config.Save(path, a.config()); err != nil {
		return "", fmt.Errorf(i18n.L("couldn't write the settings to the new folder: %w", "не удалось записать настройки в новую папку: %w"), err)
	}
	if err := config.RememberDir(dir); err != nil {
		os.Remove(path)
		return "", fmt.Errorf(i18n.L("couldn't remember the folder: %w", "не удалось запомнить папку: %w"), err)
	}
	a.cfgPath = path
	os.Remove(old)
	return path, nil
}
