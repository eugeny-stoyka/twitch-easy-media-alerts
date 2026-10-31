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

// queuedWho lists who is waiting in the queue, in order.
func queuedWho(a *App) []string {
	a.redeemMu.Lock()
	defer a.redeemMu.Unlock()
	var who []string
	for _, p := range a.redeemQueue {
		who = append(who, p.who)
	}
	return who
}

// logHas reports whether a log entry contains want.
func logHas(a *App, want string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, e := range a.logs {
		if strings.Contains(e.Text, want) {
			return true
		}
	}
	return false
}

// While something plays, a single-play chat command that isn't on cooldown
// waits for its turn, whatever its cooldown; repeating it while it is on
// cooldown is skipped.
func TestChatCommandsQueue(t *testing.T) {
	a := newVideoTestApp(t)
	a.videoWindowShown.Store(true)
	a.cfg.Commands = []config.Command{
		{ID: "s", Enabled: true, Trigger: "!sad", File: `C:\nope\missing.mp3`, Volume: 100},
		{ID: "c", Enabled: true, Trigger: "!angry", File: `C:\nope\missing.mp3`, Volume: 100, Cooldown: 5},
	}
	if err := a.startVideo(a.cfg.Videos[0], false); err != nil { // holds the slot
		t.Fatal(err)
	}

	a.onMessage("viewer", "!angry") // has a 5 s cooldown, but isn't on it
	if q := queuedWho(a); len(q) != 1 || q[0] != "viewer: !angry" {
		t.Fatalf("command not on cooldown: queue %q", q)
	}
	a.onMessage("viewer2", "!angry") // its cooldown started when it was queued
	if q := queuedWho(a); len(q) != 1 || !logHas(a, "viewer2: !angry — command cooldown") {
		t.Fatalf("command on cooldown: queue %q", q)
	}

	for i := range maxChatQueue - 1 { // no cooldown: it may wait several times
		a.onMessage(fmt.Sprint("viewer", i), "!sad")
	}
	if q := queuedWho(a); len(q) != maxChatQueue || q[1] != "viewer0: !sad" || !logHas(a, "viewer0: !sad — queued (2)") {
		t.Fatalf("queue = %q", q)
	}
	a.onMessage("late", "!sad") // the chat's share of the queue is full
	if !logHas(a, "late: !sad — 10 chat commands are waiting already, skipped") || len(queuedWho(a)) != maxChatQueue {
		t.Error("an 11th chat command was queued")
	}
	a.onRedemption("viewer", "Sad", "") // rewards have room of their own
	if q := queuedWho(a); len(q) != maxChatQueue+1 || !strings.Contains(q[maxChatQueue], "(points)") {
		t.Fatalf("reward behind a full chat queue: %q", q)
	}

	// Once the video ends they play one after another.
	a.onVideoReport(overlay.Report{Type: "ended", Play: a.video.seq})
	waitFor(t, func() bool { return len(queuedWho(a)) == 0 }, "the queue to play out")
	for _, want := range []string{"viewer: !angry → missing.mp3", "viewer0: !sad → missing.mp3", "viewer8: !sad → missing.mp3", "viewer (points): Sad → missing.mp3"} {
		if !logHas(a, want) {
			t.Errorf("log lacks %q", want)
		}
	}
}

// A sound plays from chat; another sound command, with a cooldown of its own
// but not on it, waits and plays once the first one ends.
func TestSecondSoundWaitsForFirst(t *testing.T) {
	if testing.Short() {
		t.Skip("plays (silently) on the real audio device")
	}
	const sound = `C:\Windows\Media\Alarm01.wav` // a few seconds long
	if _, err := os.Stat(sound); err != nil {
		t.Skip("test sound not found")
	}
	a := newVideoTestApp(t)
	a.cfg.Commands = []config.Command{
		{ID: "a", Enabled: true, Trigger: "!sadmeow", File: sound, Volume: 0, Cooldown: 5},
		{ID: "b", Enabled: true, Trigger: "!wow", File: sound, Volume: 0, Cooldown: 5},
	}
	a.onMessage("viewer", "!sadmeow")
	if a.player.Current() != "a" {
		t.Fatalf("first sound isn't playing: %q", a.player.Current())
	}
	a.onMessage("viewer", "!wow")
	if q := queuedWho(a); len(q) != 1 || q[0] != "viewer: !wow" {
		t.Fatalf("second sound didn't wait: queue %q", q)
	}
	a.player.StopAndWait() // the first one ends
	waitFor(t, func() bool { return a.player.Current() == "b" }, "the second sound to start")
	if len(queuedWho(a)) != 0 {
		t.Error("the second sound is still queued")
	}
	a.stopAll()
}

// Turning all commands off drops the chat commands waiting, not the rewards.
func TestCommandsOffDropsQueuedChat(t *testing.T) {
	a := newVideoTestApp(t)
	a.videoWindowShown.Store(true)
	if err := a.startVideo(a.cfg.Videos[0], false); err != nil {
		t.Fatal(err)
	}
	a.onMessage("viewer", "!sad")
	a.onRedemption("viewer", "Sad", "")
	if q := queuedWho(a); len(q) != 2 {
		t.Fatalf("queue = %q", q)
	}
	a.setCommandsEnabled(false)
	if q := queuedWho(a); len(q) != 1 || !strings.Contains(q[0], "(points)") || !logHas(a, "Chat commands dropped from the queue (1)") {
		t.Errorf("after turning commands off: %q", q)
	}
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
