package app

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
	"twitch_easy_media_alerts/src/config"
	"twitch_easy_media_alerts/src/i18n"
	"twitch_easy_media_alerts/src/overlay"
	"twitch_easy_media_alerts/src/twitch"
)

func TestMasterSwitch(t *testing.T) {
	a := New()
	a.cfg = config.Config{Commands: []config.Command{
		{ID: "x", Enabled: true, Trigger: "!sad", File: `C:\nope\missing.mp3`, Volume: 100},
	}}
	a.onMessage("viewer", "!sad")
	if _, played := a.lastPlay["x"]; played {
		t.Fatal("command fired while all commands are off")
	}
	a.cfg.CommandsEnabled = true
	a.onMessage("viewer", "!sad")
	if _, played := a.lastPlay["x"]; !played {
		t.Fatal("command did not fire with commands on")
	}
}

func TestMoveSettings(t *testing.T) {
	root := t.TempDir()
	t.Setenv("APPDATA", filepath.Join(root, "appdata")) // where the chosen folder is remembered
	t.Setenv(config.DirEnv, "")

	a := New()
	a.cfg = config.Default()
	a.cfg.Channel = "mine"
	a.cfgPath = filepath.Join(root, "first", config.FileName)
	os.MkdirAll(filepath.Dir(a.cfgPath), 0o755)
	if err := a.saveConfig(); err != nil {
		t.Fatal(err)
	}

	second := filepath.Join(root, "second")
	path, err := a.moveSettings(second)
	if err != nil || path != filepath.Join(second, config.FileName) || a.configPath() != path {
		t.Fatalf("moveSettings = %q, %v", path, err)
	}
	if cfg, err := config.Load(path); err != nil || cfg.Channel != "mine" {
		t.Errorf("moved settings: %+v, %v", cfg.Channel, err)
	}
	if _, err := os.Stat(filepath.Join(root, "first", config.FileName)); err == nil {
		t.Error("old settings file left behind")
	}
	if got := config.ChosenDir(); got != second {
		t.Errorf("remembered folder = %q", got)
	}
	// Settings already in the target folder are kept as .bak.
	third := filepath.Join(root, "third")
	os.MkdirAll(third, 0o755)
	os.WriteFile(filepath.Join(third, config.FileName), []byte(`{"channel":"other"}`), 0o644)
	if _, err := a.moveSettings(third); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(third, config.FileName+".bak")); string(b) != `{"channel":"other"}` {
		t.Errorf("backup = %q", b)
	}

	// The environment variable wins and can't be changed from the app.
	t.Setenv(config.DirEnv, root)
	if _, err := a.moveSettings(second); err == nil {
		t.Error("moved although the environment fixes the folder")
	}
}

// Settings saved from several goroutines at once end up in the file as they
// are in memory: an older copy must never overwrite a newer one (losing
// refreshed Twitch tokens would end the login, as they work only once).
func TestConcurrentSavesKeepEveryChange(t *testing.T) {
	a := New()
	a.cfg = config.Default()
	a.cfgPath = filepath.Join(t.TempDir(), config.FileName)
	for round := range 20 {
		var wg sync.WaitGroup
		for i := range 8 {
			if i%4 == 0 {
				tok := twitch.Tokens{Access: fmt.Sprint("access", round, i), Refresh: "r"}
				wg.Go(func() { a.saveTokens(tok) })
			} else {
				wg.Go(func() { a.onVideoWindowMoved(round*100+i, i) })
			}
		}
		wg.Wait()

		file, err := config.Load(a.cfgPath)
		if err != nil {
			t.Fatal(err)
		}
		mem := a.config()
		if file.Video.WindowX != mem.Video.WindowX || file.Twitch.Tokens != mem.Twitch.Tokens {
			t.Fatalf("round %d: the file has window x %d, memory %d; tokens match: %v",
				round, file.Video.WindowX, mem.Video.WindowX, file.Twitch.Tokens == mem.Twitch.Tokens)
		}
	}
}

func TestCooldownLeft(t *testing.T) {
	now := time.Now()
	if got := cooldownLeft(time.Time{}, 10, now); got != 0 {
		t.Errorf("never played: %d", got)
	}
	if got := cooldownLeft(now.Add(-3*time.Second), 10, now); got != 7 {
		t.Errorf("3s ago with 10s cooldown: %d", got)
	}
	if got := cooldownLeft(now.Add(-2500*time.Millisecond), 10, now); got != 8 {
		t.Errorf("2.5s ago rounds up: %d", got)
	}
	if got := cooldownLeft(now.Add(-11*time.Second), 10, now); got != 0 {
		t.Errorf("expired: %d", got)
	}
	if got := cooldownLeft(now, 0, now); got != 0 {
		t.Errorf("no cooldown: %d", got)
	}
}

func TestRedemptionQueue(t *testing.T) {
	a := newVideoTestApp(t)
	a.videoWindowShown.Store(true)

	// Something is showing, so the reward has to wait.
	if err := a.startVideo(a.cfg.Videos[0], false); err != nil {
		t.Fatal(err)
	}
	seq := a.video.seq
	a.onRedemption("viewer", "SAD", "") // matches the "Sad" reward sound
	a.onRedemption("viewer", "no such reward", "")
	a.redeemMu.Lock()
	queued := len(a.redeemQueue)
	a.redeemMu.Unlock()
	if queued != 1 {
		t.Fatalf("queue = %d, want 1", queued)
	}

	a.onVideoReport(overlay.Report{Type: "ended", Play: seq})
	deadline := time.Now().Add(2 * time.Second)
	for {
		a.redeemMu.Lock()
		queued = len(a.redeemQueue)
		a.redeemMu.Unlock()
		if queued == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("queued reward didn't start after the video ended")
		}
		time.Sleep(10 * time.Millisecond)
	}

	logs := ""
	a.mu.Lock()
	for _, e := range a.logs {
		logs += e.Text + "\n"
	}
	a.mu.Unlock()
	for _, want := range []string{"viewer (points): SAD — queued (1)", "viewer (points): SAD → missing.mp3", "no such reward — no enabled reward with this title"} {
		if !strings.Contains(logs, want) {
			t.Errorf("log lacks %q:\n%s", want, logs)
		}
	}

	// Stop drops what is waiting.
	a.player.StopAndWait()
	if err := a.startVideo(a.cfg.Videos[0], false); err != nil {
		t.Fatal(err)
	}
	a.onRedemption("viewer", "!sad", "")
	a.stopAll()
	a.redeemMu.Lock()
	queued = len(a.redeemQueue)
	a.redeemMu.Unlock()
	if queued != 0 {
		t.Errorf("queue after Stop = %d", queued)
	}

}

// Multi-play rewards start at once, past single-play ones waiting for the
// slot, and wait only while too many multi-play GIFs show; each kind keeps
// its own order in the queue.
func TestMultiPlayRewards(t *testing.T) {
	a := newVideoTestApp(t)
	a.videoWindowShown.Store(true)
	a.cfg.Rewards.Sounds = append(a.cfg.Rewards.Sounds,
		config.Command{ID: "rc", Enabled: true, Trigger: "Clap", File: `C:\nope\clap.mp3`, Volume: 100, MultiPlay: true})
	a.cfg.Rewards.Gifs = []config.GifCommand{
		{ID: "rw", Enabled: true, Trigger: "Wow", Image: `C:\wow.gif`, Volume: 100, MultiPlay: true, Look: config.Look{Scale: 50}}}
	queue := func() []string {
		a.redeemMu.Lock()
		defer a.redeemMu.Unlock()
		var who []string
		for _, p := range a.redeemQueue {
			who = append(who, p.who)
		}
		return who
	}
	logged := func(want string) bool {
		a.mu.Lock()
		defer a.mu.Unlock()
		for _, e := range a.logs {
			if strings.Contains(e.Text, want) {
				return true
			}
		}
		return false
	}

	// A video holds the slot, so the single-play "Sad" waits...
	if err := a.startVideo(a.cfg.Videos[0], false); err != nil {
		t.Fatal(err)
	}
	a.onRedemption("viewer", "Sad", "")
	// ...but the multi-play "Clap" plays at once, over the video.
	a.onRedemption("viewer", "Clap", "")
	if q := queue(); len(q) != 1 || !strings.Contains(q[0], "Sad") {
		t.Fatalf("queue = %q, want only Sad", q)
	}
	if !logged("viewer (points): Clap → clap.mp3") {
		t.Error("multi-play reward didn't start at once")
	}

	// With the multi-play GIFs full, "Wow" waits too, behind "Sad".
	for range maxMultiGifs {
		if err := a.startGifMulti(a.cfg.Rewards.Gifs[0]); err != nil {
			t.Fatal(err)
		}
	}
	a.onRedemption("viewer", "Wow", "")
	if q := queue(); len(q) != 2 || !strings.Contains(q[1], "Wow") {
		t.Fatalf("queue = %q, want Sad, Wow", q)
	}

	// A multi-play GIF ending frees a spot: "Wow" starts although "Sad",
	// ahead of it, still waits for the slot.
	a.mediaMu.Lock()
	var seq int
	for s := range a.multiGifs {
		seq = s
		break
	}
	a.mediaMu.Unlock()
	a.onVideoReport(overlay.Report{Type: "ended", Play: seq})
	waitFor(t, func() bool { q := queue(); return len(q) == 1 && strings.Contains(q[0], "Sad") }, "Wow to start")

	// The video ending lets "Sad" start.
	a.onVideoReport(overlay.Report{Type: "ended", Play: a.video.seq})
	waitFor(t, func() bool { return len(queue()) == 0 }, "Sad to start")
	a.stopAll()
}

// waitFor polls cond for up to 2 seconds.
func waitFor(t *testing.T, cond func() bool, what string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for " + what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestRewardsSeparateFromChat(t *testing.T) {
	a := newVideoTestApp(t)
	a.videoWindowShown.Store(true)
	playing := func() string {
		a.mediaMu.Lock()
		defer a.mediaMu.Unlock()
		if a.video == nil {
			return ""
		}
		return a.video.id
	}

	a.onRedemption("viewer", "clip", "") // the "Clip" reward, not the "!clip" chat command
	if got := playing(); got != "rv" {
		t.Fatalf("reward played %q, want rv", got)
	}
	a.stopAll()

	a.onMessage("viewer", "Clip") // a chat message naming the reward runs nothing
	if got := playing(); got != "" {
		t.Fatalf("chat message ran %q", got)
	}
	a.onMessage("viewer", "!clip") // the chat command still works
	if got := playing(); got != "v" {
		t.Fatalf("chat command played %q, want v", got)
	}
}

func TestLogsFollowLanguage(t *testing.T) {
	defer i18n.SetLanguage("en")
	a := New()
	a.cfg = config.Default()
	a.onRedemption("viewer", "Hug", "please")
	i18n.SetLanguage("ru")
	a.onRedemption("viewer", "Hug", "please")
	a.mu.Lock()
	defer a.mu.Unlock()
	if len(a.logs) != 2 || !strings.HasPrefix(a.logs[0].Text, "viewer (points): Hug “please”") ||
		!strings.HasPrefix(a.logs[1].Text, "viewer (баллы): Hug «please»") {
		t.Errorf("logs = %+v", a.logs)
	}
}
