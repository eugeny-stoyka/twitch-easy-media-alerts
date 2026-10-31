// Package config holds the settings: their types and defaults, where the
// settings file lives, and loading and saving it.
package config

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"twitch_easy_media_alerts/src/appinfo"

	"golang.org/x/sys/windows"
)

const FileName = "settings.json"

// Command is one chat command that plays a sound.
type Command struct {
	ID       string `json:"id"`
	Enabled  bool   `json:"enabled"`
	Trigger  string `json:"trigger"`  // e.g. "!sad"
	File     string `json:"file"`     // path to the sound file
	Volume   int    `json:"volume"`   // 0..200 %
	Device   string `json:"device"`   // endpoint ID, "" = use the general device
	Cooldown int    `json:"cooldown"` // seconds between plays of this command
	// MultiPlay plays it at once, over whatever plays, instead of one at a
	// time; it doesn't hold up other commands either.
	MultiPlay bool `json:"multiPlay"`
}

// Look is how a video or picture appears in OBS: size, place and chroma key.
type Look struct {
	Scale  int     `json:"scale"`  // 5..100 % of the size that fits the frame
	Random bool    `json:"random"` // appear at a random place instead of X/Y
	X      float64 `json:"x"`      // place within the free space: 0 left … 1 right
	Y      float64 `json:"y"`      // 0 top … 1 bottom

	// Chroma key: make KeyColor (e.g. a green screen) transparent. The
	// values mean the same as in OBS's Chroma Key filter, 1..1000.
	Key        bool   `json:"key"`
	KeyColor   string `json:"keyColor"` // #rrggbb
	Similarity int    `json:"similarity"`
	Smoothness int    `json:"smoothness"`
	Spill      int    `json:"spill"` // key colour spill reduction
}

func (l *Look) Normalize() {
	if l.Scale == 0 {
		l.Scale = 100
	}
	l.Scale = clamp(l.Scale, 5, 100)
	l.X = max(0, min(l.X, 1))
	l.Y = max(0, min(l.Y, 1))
	if !hexColorRe.MatchString(l.KeyColor) {
		l.KeyColor = DefaultBackground
	}
	l.KeyColor = strings.ToLower(l.KeyColor)
	l.Similarity = clampOr(l.Similarity, 400, 1, 1000) // OBS's defaults
	l.Smoothness = clampOr(l.Smoothness, 80, 1, 1000)
	l.Spill = clampOr(l.Spill, 100, 1, 1000)
}

// KeyParams describes the chroma key for the page, or nil if it's off.
func (l Look) KeyParams() map[string]any {
	if !l.Key {
		return nil
	}
	return map[string]any{"color": l.KeyColor, "similarity": l.Similarity, "smoothness": l.Smoothness, "spill": l.Spill}
}

// VideoCommand is a chat command that plays a video in OBS.
type VideoCommand struct {
	ID       string `json:"id"`
	Enabled  bool   `json:"enabled"`
	Trigger  string `json:"trigger"`
	File     string `json:"file"`
	Volume   int    `json:"volume"`   // 0..200 %
	Cooldown int    `json:"cooldown"` // seconds
	Look
}

// GifCommand is a chat command that shows an animated picture with a sound
// in OBS, the same way videos are shown.
type GifCommand struct {
	ID       string `json:"id"`
	Enabled  bool   `json:"enabled"`
	Trigger  string `json:"trigger"`
	Image    string `json:"image"`    // gif, webp or apng
	Sound    string `json:"sound"`    // optional: mp3, wav, ogg, flac
	Volume   int    `json:"volume"`   // 0..200 %
	Duration int    `json:"duration"` // seconds to show the picture; 0 = while the sound plays
	Cooldown int    `json:"cooldown"`
	// MultiPlay shows it at once, over whatever is showing or playing, as
	// for Command.
	MultiPlay bool `json:"multiPlay"`
	Look
}

// DefaultGifDuration is how long a picture without a sound is shown when no
// duration is set.
const DefaultGifDuration = 5

// ShowFor is how many seconds the picture stays up; 0 = until the sound ends.
func (g GifCommand) ShowFor() int {
	if g.Duration == 0 && g.Sound == "" {
		return DefaultGifDuration
	}
	return g.Duration
}

// VideoSettings describe where videos are shown: an OBS browser source and/or
// a window OBS captures.
type VideoSettings struct {
	Width      int    `json:"width"`
	Height     int    `json:"height"`
	Background string `json:"background"` // window background, #rrggbb, keyed out in OBS
	WindowOpen bool   `json:"windowOpen"`
	WindowX    int    `json:"windowX"`
	WindowY    int    `json:"windowY"`
	WindowPos  bool   `json:"windowPos"` // WindowX/Y hold a remembered position
	// The browser source URL is http://127.0.0.1:Port/Token/obs; both stay
	// fixed so the URL pasted into OBS keeps working.
	Port  int    `json:"port"`
	Token string `json:"token"`
}

const defaultOverlayPort = 27463

// TwitchAccount is the streamer's login used for channel points. ClientID
// comes from the app they registered on dev.twitch.tv; the rest is set by
// logging in.
type TwitchAccount struct {
	ClientID string `json:"clientId"`
	Login    string `json:"login"`
	UserID   string `json:"userId"`
	Tokens   string `json:"tokens"` // encrypted with Windows DPAPI for this user
}

type Config struct {
	Language        string         `json:"language"` // "en" or "ru"
	Channel         string         `json:"channel"`
	CommandsEnabled bool           `json:"commandsEnabled"` // master switch for all commands
	Device          string         `json:"device"`          // endpoint ID, "" = system default
	MasterVolume    int            `json:"masterVolume"`
	GlobalCooldown  int            `json:"globalCooldown"` // seconds between any two commands
	AutoConnect     bool           `json:"autoConnect"`
	MinimizeToTray  bool           `json:"minimizeToTray"`
	CloseToTray     bool           `json:"closeToTray"`
	StartMinimized  bool           `json:"startMinimized"`
	Tab             string         `json:"tab"`        // last open tab: chat, rewards or obs
	ChatTab         string         `json:"chatTab"`    // and inside "chat": sounds, videos or gifs
	RewardsTab      string         `json:"rewardsTab"` // and inside "rewards": sounds, videos or gifs
	Commands        []Command      `json:"commands"`
	Videos          []VideoCommand `json:"videos"`
	Gifs            []GifCommand   `json:"gifs"`
	Rewards         Rewards        `json:"rewards"`
	Video           VideoSettings  `json:"video"`
	Twitch          TwitchAccount  `json:"twitch"`
}

// Rewards are what channel point rewards play. Trigger holds the reward's
// title; chat messages never run these, and their cooldowns are unused.
type Rewards struct {
	Sounds []Command      `json:"sounds"`
	Videos []VideoCommand `json:"videos"`
	Gifs   []GifCommand   `json:"gifs"`
}

const DefaultBackground = "#00ff00"

func Default() Config {
	return Config{
		Language:        "en",
		CommandsEnabled: true,
		MasterVolume:    100,
		MinimizeToTray:  true,
		Commands: []Command{
			{ID: NewID(), Enabled: true, Trigger: "!sad", Volume: 100, Cooldown: 5},
			{ID: NewID(), Enabled: true, Trigger: "!angry", Volume: 100, Cooldown: 5},
		},
		Videos: []VideoCommand{},
		Gifs:   []GifCommand{},
		Video:  VideoSettings{Width: 1920, Height: 1080, Background: DefaultBackground, Port: defaultOverlayPort},
	}
}

func NewID() string {
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func clamp(v, lo, hi int) int {
	return max(lo, min(v, hi))
}

// clampOr is clamp with def standing in for an unset (zero) value.
func clampOr(v, def, lo, hi int) int {
	if v == 0 {
		v = def
	}
	return clamp(v, lo, hi)
}

var (
	hexColorRe = regexp.MustCompile(`^#[0-9a-fA-F]{6}$`)
	tokenRe    = regexp.MustCompile(`^[0-9a-f]{16,64}$`)
)

// normalize fixes values that came from the UI or a hand-edited file.
func (c *Config) Normalize() {
	c.Channel = strings.TrimSpace(c.Channel)
	c.Twitch.ClientID = strings.TrimSpace(c.Twitch.ClientID)
	c.MasterVolume = clamp(c.MasterVolume, 0, 100)
	c.GlobalCooldown = clamp(c.GlobalCooldown, 0, 3600)
	seen := map[string]bool{}
	ids := func(id string) string {
		if id == "" || seen[id] {
			id = NewID()
		}
		seen[id] = true
		return id
	}
	c.Commands = normalizeSounds(c.Commands, ids)
	c.Videos = normalizeVideos(c.Videos, ids)
	c.Gifs = normalizeGifs(c.Gifs, ids)
	c.Rewards.Sounds = normalizeSounds(c.Rewards.Sounds, ids)
	c.Rewards.Videos = normalizeVideos(c.Rewards.Videos, ids)
	c.Rewards.Gifs = normalizeGifs(c.Rewards.Gifs, ids)
	c.Video.Width = clamp(c.Video.Width, 320, 7680)
	c.Video.Height = clamp(c.Video.Height, 180, 4320)
	if !hexColorRe.MatchString(c.Video.Background) {
		c.Video.Background = DefaultBackground
	}
	c.Video.Background = strings.ToLower(c.Video.Background)
	if c.Video.Port < 1024 || c.Video.Port > 65535 {
		c.Video.Port = defaultOverlayPort
	}
	if !tokenRe.MatchString(c.Video.Token) {
		c.Video.Token = NewID() + NewID()
	}
	switch c.Tab {
	case "sounds", "videos", "gifs": // these were top-level tabs before "chat"
		if c.ChatTab == "" {
			c.ChatTab = c.Tab
		}
		c.Tab = "chat"
	case "chat", "rewards", "obs":
	default:
		c.Tab = "chat"
	}
	switch c.ChatTab {
	case "sounds", "videos", "gifs":
	default:
		c.ChatTab = "sounds"
	}
	switch c.RewardsTab {
	case "sounds", "videos", "gifs":
	default:
		c.RewardsTab = "sounds"
	}
	if c.Language != "ru" {
		c.Language = "en"
	}
}

// AppDataDir is the app's folder in the user's profile:
// %APPDATA%\TwitchEasyMediaAlerts.
func AppDataDir() string { return filepath.Join(appDataBase(), appinfo.ID) }

func appDataBase() string {
	base, err := os.UserConfigDir()
	if err != nil {
		base = os.Getenv("APPDATA")
	}
	return base
}

// documentsDir is the user's Documents folder; a variable for tests.
var documentsDir = func() string {
	if docs, err := windows.KnownFolderPath(windows.FOLDERID_Documents, 0); err == nil {
		return docs
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, "Documents")
}

// DirEnv names a fixed folder for the settings: a portable setup, or a test
// run that must not touch the real ones. It beats the folder chosen in the
// app, and nothing is moved into it from elsewhere.
const DirEnv = "TWITCH_EASY_MEDIA_ALERTS_DIR"

// The folder chosen in the app is remembered outside the settings (which
// live in it), in this file in %APPDATA%\TwitchEasyMediaAlerts.
const settingsDirFile = "settings-dir.txt"

// defaultSettingsDir is Documents\TwitchEasyMediaAlerts: easy to find and back up.
func defaultSettingsDir() string { return filepath.Join(documentsDir(), appinfo.ID) }

// ChosenDir is the folder picked in the app, or "". A choice made before the
// app was renamed is carried over.
func ChosenDir() string {
	b, err := os.ReadFile(filepath.Join(AppDataDir(), settingsDirFile))
	if err != nil {
		old := filepath.Join(appDataBase(), appinfo.OldID, settingsDirFile)
		if b, err = os.ReadFile(old); err != nil {
			return ""
		}
		if RememberDir(strings.TrimSpace(string(b))) == nil {
			os.Remove(old)
		}
	}
	return strings.TrimSpace(string(b))
}

// RememberDir records the folder picked in the app; the default
// folder needs no record.
func RememberDir(dir string) error {
	file := filepath.Join(AppDataDir(), settingsDirFile)
	if filepath.Clean(dir) == filepath.Clean(defaultSettingsDir()) {
		err := os.Remove(file)
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	if err := os.MkdirAll(AppDataDir(), 0o755); err != nil {
		return err
	}
	return os.WriteFile(file, []byte(dir), 0o644)
}

// DirLocked reports whether the environment fixes the folder.
func DirLocked() bool { return os.Getenv(DirEnv) != "" }

// Resolve returns the settings file: in the folder from the environment, the
// one chosen in the app, or Documents\TwitchEasyMediaAlerts. If the chosen
// folder is gone (an unplugged drive), unavailable names it and the default
// is used. Settings that older versions kept elsewhere (under the old name,
// in %APPDATA%, next to the exe) are moved in; migratedFrom names that file.
func Resolve() (path, migratedFrom, unavailable string, err error) {
	dir := os.Getenv(DirEnv)
	fixed := dir != ""
	if !fixed {
		dir = ChosenDir()
		if dir != "" && os.MkdirAll(dir, 0o755) != nil {
			unavailable, dir = dir, ""
		}
		if dir == "" {
			dir = defaultSettingsDir()
		}
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return filepath.Join(dir, FileName), "", unavailable, err
	}
	path = filepath.Join(dir, FileName)
	if fixed {
		return path, "", unavailable, nil
	}
	older := []string{
		filepath.Join(documentsDir(), appinfo.OldID, FileName),
		filepath.Join(appDataBase(), appinfo.OldID, FileName),
	}
	if exe, e := os.Executable(); e == nil {
		older = append(older, filepath.Join(filepath.Dir(exe), FileName))
	}
	for _, old := range older {
		if strings.EqualFold(old, path) {
			continue
		}
		moved, e := migrateConfig(old, path)
		if e != nil {
			return path, "", unavailable, e
		}
		if moved {
			return path, old, unavailable, nil
		}
	}
	return path, "", unavailable, nil
}

// migrateConfig moves the settings file from old to path, unless there is
// nothing to move or path already exists.
func migrateConfig(old, path string) (bool, error) {
	if _, err := os.Stat(path); err == nil {
		return false, nil
	}
	data, err := os.ReadFile(old)
	if err != nil {
		return false, nil
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return false, err
	}
	os.Remove(old)
	os.Remove(filepath.Dir(old)) // the old folder, if that left it empty
	return true, nil
}

func Load(path string) (Config, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		cfg := Default()
		return cfg, nil
	}
	if err != nil {
		return Default(), err
	}
	cfg := Default()
	cfg.Commands = nil
	if err := json.Unmarshal(data, &cfg); err != nil {
		return Default(), err
	}
	cfg.Normalize()
	return cfg, nil
}

func Save(path string, cfg Config) error {
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// cleanPath trims spaces and the quotes Explorer's "Copy as path" adds.
func cleanPath(p string) string {
	return strings.Trim(strings.TrimSpace(p), `"`)
}

// The normalize helpers get ids to give every command a unique id.

func normalizeSounds(cmds []Command, ids func(string) string) []Command {
	if cmds == nil {
		return []Command{}
	}
	for i := range cmds {
		cmd := &cmds[i]
		cmd.ID = ids(cmd.ID)
		cmd.Trigger = strings.TrimSpace(cmd.Trigger)
		cmd.File = cleanPath(cmd.File)
		cmd.Volume = clamp(cmd.Volume, 0, 200)
		cmd.Cooldown = clamp(cmd.Cooldown, 0, 3600)
	}
	return cmds
}

func normalizeVideos(videos []VideoCommand, ids func(string) string) []VideoCommand {
	if videos == nil {
		return []VideoCommand{}
	}
	for i := range videos {
		v := &videos[i]
		v.ID = ids(v.ID)
		v.Trigger = strings.TrimSpace(v.Trigger)
		v.File = cleanPath(v.File)
		v.Volume = clamp(v.Volume, 0, 200)
		v.Cooldown = clamp(v.Cooldown, 0, 3600)
		v.Look.Normalize()
	}
	return videos
}

func normalizeGifs(gifs []GifCommand, ids func(string) string) []GifCommand {
	if gifs == nil {
		return []GifCommand{}
	}
	for i := range gifs {
		g := &gifs[i]
		g.ID = ids(g.ID)
		g.Trigger = strings.TrimSpace(g.Trigger)
		g.Image = cleanPath(g.Image)
		g.Sound = cleanPath(g.Sound)
		g.Volume = clamp(g.Volume, 0, 200)
		g.Duration = clamp(g.Duration, 0, 600)
		g.Cooldown = clamp(g.Cooldown, 0, 3600)
		g.Look.Normalize()
	}
	return gifs
}

// Activation tells whether the command is on and which word triggers it.
func (c Command) Activation() (bool, string)      { return c.Enabled, c.Trigger }
func (v VideoCommand) Activation() (bool, string) { return v.Enabled, v.Trigger }
func (g GifCommand) Activation() (bool, string)   { return g.Enabled, g.Trigger }
