package config

import (
	"os"
	"path/filepath"
	"testing"
	"twitch_easy_media_alerts/src/appinfo"
)

func TestOldConfigKeepsCommandsOn(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	if err := os.WriteFile(path, []byte(`{"channel":"x","commands":[]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil || !cfg.CommandsEnabled {
		t.Fatalf("Load = %+v, %v; want commands enabled", cfg, err)
	}
}

func TestMigrateConfig(t *testing.T) {
	dir := t.TempDir()
	old, path := filepath.Join(dir, "old.json"), filepath.Join(dir, "new.json")

	if moved, err := migrateConfig(old, path); moved || err != nil {
		t.Fatalf("nothing to move: moved=%v err=%v", moved, err)
	}
	if _, err := os.Stat(path); err == nil {
		t.Fatal("created a settings file out of nothing")
	}

	os.WriteFile(old, []byte(`{"channel":"a"}`), 0o644)
	if moved, err := migrateConfig(old, path); !moved || err != nil {
		t.Fatalf("move: moved=%v err=%v", moved, err)
	}
	if b, _ := os.ReadFile(path); string(b) != `{"channel":"a"}` {
		t.Errorf("moved content = %q", b)
	}
	if _, err := os.Stat(old); err == nil {
		t.Error("old file left behind")
	}

	// Existing settings in the profile win; the old file is left alone.
	os.WriteFile(old, []byte(`{"channel":"b"}`), 0o644)
	if moved, _ := migrateConfig(old, path); moved {
		t.Error("overwrote existing settings")
	}
	if b, _ := os.ReadFile(path); string(b) != `{"channel":"a"}` {
		t.Errorf("existing settings changed: %q", b)
	}
	if _, err := os.Stat(old); err != nil {
		t.Error("old file removed although it wasn't moved")
	}
}

func TestTabMigration(t *testing.T) {
	for old, want := range map[string][2]string{
		"videos": {"chat", "videos"}, "gifs": {"chat", "gifs"}, "sounds": {"chat", "sounds"},
		"rewards": {"rewards", "sounds"}, "": {"chat", "sounds"}, "bogus": {"chat", "sounds"},
	} {
		c := Config{Tab: old}
		c.Normalize()
		if c.Tab != want[0] || c.ChatTab != want[1] {
			t.Errorf("tab %q → %q/%q, want %v", old, c.Tab, c.ChatTab, want)
		}
	}
}

// isolate points the user's folders at temporary ones, so tests never touch
// the real settings.
func isolate(t *testing.T) (appData, docs string) {
	appData, docs = t.TempDir(), t.TempDir()
	t.Setenv("APPDATA", appData)
	t.Setenv(DirEnv, "")
	prev := documentsDir
	documentsDir = func() string { return docs }
	t.Cleanup(func() { documentsDir = prev })
	return appData, docs
}

func TestSettingsMoveFromOldPlaces(t *testing.T) {
	for _, where := range []string{"documents", "appdata"} {
		t.Run(where, func(t *testing.T) {
			appData, docs := isolate(t)
			old := filepath.Join(docs, appinfo.OldID, FileName)
			if where == "appdata" {
				old = filepath.Join(appData, appinfo.OldID, FileName)
			}
			os.MkdirAll(filepath.Dir(old), 0o755)
			os.WriteFile(old, []byte(`{"channel":"mine"}`), 0o644)

			path, from, _, err := Resolve()
			if err != nil || path != filepath.Join(docs, appinfo.ID, FileName) || from != old {
				t.Fatalf("Resolve = %q, %q, %v", path, from, err)
			}
			if b, _ := os.ReadFile(path); string(b) != `{"channel":"mine"}` {
				t.Errorf("moved settings = %q", b)
			}
			if _, err := os.Stat(old); err == nil {
				t.Error("old settings left behind")
			}
			if _, from, _, _ := Resolve(); from != "" {
				t.Errorf("moved again from %q", from)
			}
		})
	}
}

func TestChosenDirCarriedOver(t *testing.T) {
	appData, docs := isolate(t)
	chosen := filepath.Join(t.TempDir(), "mine")
	old := filepath.Join(appData, appinfo.OldID, settingsDirFile)
	os.MkdirAll(filepath.Dir(old), 0o755)
	os.WriteFile(old, []byte(chosen), 0o644)

	path, _, unavailable, err := Resolve()
	if err != nil || path != filepath.Join(chosen, FileName) || unavailable != "" {
		t.Fatalf("Resolve = %q, %q, %v", path, unavailable, err)
	}
	if _, err := os.Stat(old); err == nil {
		t.Error("old record of the chosen folder left behind")
	}
	// Going back to the default forgets the choice for good.
	if err := RememberDir(filepath.Join(docs, appinfo.ID)); err != nil || ChosenDir() != "" {
		t.Errorf("after choosing the default: %v, %q", err, ChosenDir())
	}
}

func TestFixedDirMovesNothing(t *testing.T) {
	_, docs := isolate(t)
	fixed := t.TempDir()
	t.Setenv(DirEnv, fixed)
	old := filepath.Join(docs, appinfo.OldID, FileName)
	os.MkdirAll(filepath.Dir(old), 0o755)
	os.WriteFile(old, []byte(`{}`), 0o644)
	if path, from, _, err := Resolve(); err != nil || path != filepath.Join(fixed, FileName) || from != "" {
		t.Errorf("Resolve = %q, %q, %v", path, from, err)
	}
	if _, err := os.Stat(old); err != nil {
		t.Error("settings moved into the fixed folder")
	}
}

func TestConfigNormalize(t *testing.T) {
	c := Config{MasterVolume: 300, Commands: []Command{
		{ID: "a", Volume: -5, Cooldown: 99999, File: ` "C:\x.mp3" `},
		{ID: "a", Volume: 150},
	}}
	c.Normalize()
	if c.MasterVolume != 100 || c.Commands[0].Volume != 0 || c.Commands[0].Cooldown != 3600 ||
		c.Commands[0].File != `C:\x.mp3` || c.Commands[1].ID == "a" || c.Commands[1].Volume != 150 {
		t.Errorf("unexpected normalize result: %+v", c)
	}
}

func TestVideoConfigNormalize(t *testing.T) {
	c := Config{
		Videos: []VideoCommand{
			{ID: "v", Volume: 500, Look: Look{Scale: 0, X: -1, Y: 2}},
			{ID: "w", Look: Look{Scale: 300}},
		},
		Video: VideoSettings{Width: 10, Height: 99999, Background: "red"},
	}
	c.Normalize()
	v, w := c.Videos[0], c.Videos[1]
	if v.Scale != 100 || v.X != 0 || v.Y != 1 || v.Volume != 200 || w.Scale != 100 {
		t.Errorf("videos not normalized: %+v %+v", v, w)
	}
	if c.Video.Width != 320 || c.Video.Height != 4320 || c.Video.Background != DefaultBackground {
		t.Errorf("video settings not normalized: %+v", c.Video)
	}
}

func TestOldConfigGetsVideoDefaults(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	os.WriteFile(path, []byte(`{"channel":"x","commands":[]}`), 0o644)
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Video.Width != 1920 || cfg.Video.Height != 1080 || cfg.Video.Background != DefaultBackground || cfg.Videos == nil {
		t.Errorf("unexpected video defaults: %+v, videos=%v", cfg.Video, cfg.Videos)
	}
}

func TestChromaKeySettings(t *testing.T) {
	c := Config{Videos: []VideoCommand{
		{ID: "a", Look: Look{Key: true, KeyColor: "#00FF00"}},
		{ID: "b", Look: Look{Key: true, KeyColor: "green", Similarity: 5000, Smoothness: -3, Spill: 7}},
	}}
	c.Normalize()
	a, b := c.Videos[0], c.Videos[1]
	if a.KeyColor != "#00ff00" || a.Similarity != 400 || a.Smoothness != 80 || a.Spill != 100 {
		t.Errorf("defaults not applied: %+v", a)
	}
	if b.KeyColor != DefaultBackground || b.Similarity != 1000 || b.Smoothness != 1 || b.Spill != 7 {
		t.Errorf("values not clamped: %+v", b)
	}
	if p := a.KeyParams(); p["color"] != "#00ff00" || p["similarity"] != 400 {
		t.Errorf("KeyParams = %v", p)
	}
	a.Key = false
	if p := a.KeyParams(); p != nil {
		t.Errorf("KeyParams with the key off = %v, want nil", p)
	}
}

func TestGifConfigNormalize(t *testing.T) {
	c := Config{Gifs: []GifCommand{
		{ID: "g", Image: ` "C:\a.gif" `, Duration: 9999, Volume: -1},
		{ID: "g", Sound: `C:\a.mp3`},
	}}
	c.Normalize()
	g, h := c.Gifs[0], c.Gifs[1]
	if g.Image != `C:\a.gif` || g.Duration != 600 || g.Volume != 0 || g.Scale != 100 || g.Similarity != 400 {
		t.Errorf("gif not normalized: %+v", g)
	}
	if h.ID == "g" {
		t.Error("duplicate id kept")
	}
	if got := (GifCommand{}).ShowFor(); got != DefaultGifDuration {
		t.Errorf("no sound, no duration: ShowFor = %d, want %d", got, DefaultGifDuration)
	}
	if got := (GifCommand{Sound: "x.mp3"}).ShowFor(); got != 0 {
		t.Errorf("with sound: ShowFor = %d, want 0 (while the sound plays)", got)
	}
}
