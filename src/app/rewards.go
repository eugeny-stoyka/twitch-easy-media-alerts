package app

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"time"
	"twitch_easy_media_alerts/src/audio"
	"twitch_easy_media_alerts/src/config"
	"twitch_easy_media_alerts/src/i18n"
	"twitch_easy_media_alerts/src/twitch"
	"twitch_easy_media_alerts/src/win"
)

// Channel point rewards play what the "Баллы канала" tab sets up for a
// reward with that title; chat messages can't run those. Viewers paid for
// rewards, so instead of being skipped while something plays they wait in a
// queue. Multi-play ones start at once, past the queue, and wait only while
// too many multi-play sounds or GIFs are on.

const maxRedeemQueue = 50

type pointsStatus struct {
	State   string `json:"state"`   // off, login, connecting, connected, reconnecting, error
	Message string `json:"message"` // the code to enter while logging in, or what went wrong
	Login   string `json:"login"`
}

// pendingPlay is a command a reward is waiting to run.
type pendingPlay struct {
	who, file, kind string
	multi           bool // multi-play: doesn't wait for the one-at-a-time slot
	start           func() error
}

func (a *App) onPointsStatus(state, message string) {
	a.mu.Lock()
	prev := a.pointsSt
	a.pointsSt = pointsStatus{State: state, Message: message, Login: a.cfg.Twitch.Login}
	st := a.pointsSt
	a.mu.Unlock()
	a.emit(map[string]any{"type": "points", "status": st})
	if prev.State == state && prev.Message == message {
		return
	}
	if state == "connected" {
		go a.loadRewards() // the page shows them, to set up what each one plays
	}
	switch state {
	case "connected":
		a.log("info", fmt.Sprintf(i18n.L("Channel Points %s: rewards connected", "Баллы канала %s: награды подключены"), st.Login))
	case "reconnecting", "error":
		a.log("warn", i18n.L("Channel Points: ", "Баллы канала: ")+message)
	}
}

// restartPoints (re)starts listening for rewards with the saved login.
func (a *App) restartPoints() {
	a.pointsMu.Lock()
	defer a.pointsMu.Unlock()
	a.stopPointsLocked()
	acc := a.config().Twitch
	if acc.ClientID == "" || acc.UserID == "" || acc.Tokens == "" {
		a.onPointsStatus("off", "")
		return
	}
	tok, err := twitch.UnprotectTokens(acc.Tokens)
	if err != nil {
		a.onPointsStatus("error", err.Error()+i18n.L(" — log in again", " — войдите заново"))
		return
	}
	a.points = twitch.StartPoints(acc.ClientID, acc.UserID, tok, twitch.PointsCallbacks{
		OnStatus:     a.onPointsStatus,
		OnRedemption: a.onRedemption,
		OnTokens:     a.saveTokens,
	})
}

// stopPointsLocked stops the listener and any login in progress. Caller
// holds pointsMu.
func (a *App) stopPointsLocked() {
	if a.loginCancel != nil {
		a.loginCancel()
		a.loginCancel = nil
	}
	if a.points != nil {
		a.points.Stop()
		a.points = nil
	}
}

func (a *App) saveTokens(t twitch.Tokens) {
	enc, err := twitch.ProtectTokens(t)
	if err != nil {
		a.log("error", i18n.L("Couldn't save the Twitch login: ", "Не удалось сохранить вход в Twitch: ")+err.Error())
		return
	}
	a.mu.Lock()
	a.cfg.Twitch.Tokens = enc
	a.mu.Unlock()
	if err := a.saveConfig(); err != nil {
		a.logSaveError(err)
	}
}

// twitchLogin starts the device code login: the page shows the code, the
// browser opens twitch.tv/activate, and listening starts once confirmed.
func (a *App) twitchLogin() error {
	clientID := a.config().Twitch.ClientID
	if clientID == "" {
		return errors.New(i18n.L("paste your Twitch app Client ID first", "сначала вставьте Client ID приложения Twitch"))
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	a.pointsMu.Lock()
	a.stopPointsLocked()
	a.loginCancel = cancel
	a.pointsMu.Unlock()

	go func() {
		defer cancel()
		fail := func(msg string) {
			if errors.Is(ctx.Err(), context.Canceled) {
				return // cancelled or superseded; whoever did that sets the status
			}
			a.onPointsStatus("error", msg)
		}
		d, err := twitch.StartDeviceAuth(ctx, clientID)
		if err != nil {
			fail(i18n.L("couldn't start the login: ", "не удалось начать вход: ") + err.Error())
			return
		}
		a.onPointsStatus("login", d.UserCode)
		a.emit(map[string]any{"type": "pointsLogin", "code": d.UserCode, "uri": d.VerificationURI})
		a.w.Dispatch(func() { win.OpenURL(d.VerificationURI) })
		tok, err := twitch.AwaitDeviceAuth(ctx, clientID, d)
		if err != nil {
			fail(err.Error())
			return
		}
		info, err := twitch.ValidateToken(ctx, tok.Access)
		if err != nil {
			fail(err.Error())
			return
		}
		enc, err := twitch.ProtectTokens(tok)
		if err != nil {
			fail(i18n.L("couldn't save the login: ", "не удалось сохранить вход: ") + err.Error())
			return
		}
		a.mu.Lock()
		a.cfg.Twitch.Login, a.cfg.Twitch.UserID, a.cfg.Twitch.Tokens = info.Login, info.UserID, enc
		a.mu.Unlock()
		if err := a.saveConfig(); err != nil {
			a.logSaveError(err)
		}
		a.log("info", i18n.L("Logged in to Twitch: ", "Вход в Twitch: ")+info.Login)
		a.pointsMu.Lock()
		a.loginCancel = nil
		a.pointsMu.Unlock()
		a.restartPoints()
	}()
	return nil
}

// cancelTwitchLogin abandons a login in progress and goes back to the
// previous state.
func (a *App) cancelTwitchLogin() {
	a.restartPoints()
}

// twitchLogout forgets the login (keeping the Client ID) and revokes the token.
func (a *App) twitchLogout() {
	a.pointsMu.Lock()
	a.stopPointsLocked()
	a.pointsMu.Unlock()
	a.mu.Lock()
	acc := a.cfg.Twitch
	a.cfg.Twitch = config.TwitchAccount{ClientID: acc.ClientID}
	a.mu.Unlock()
	if err := a.saveConfig(); err != nil {
		a.logSaveError(err)
	}
	if tok, err := twitch.UnprotectTokens(acc.Tokens); err == nil && acc.ClientID != "" {
		twitch.RevokeToken(acc.ClientID, tok.Access)
	}
	a.onPointsStatus("off", "")
	if acc.Login != "" {
		a.log("info", i18n.L("Logged out of the Twitch account ", "Вы вышли из аккаунта Twitch ")+acc.Login)
	}
}

// rewardCommand finds the enabled reward entry with this title: sounds
// first, then videos, then GIFs. Caller holds mu.
func (a *App) rewardCommand(title string) (pendingPlay, bool) {
	key := twitch.RewardKey(title)
	if key == "" {
		return pendingPlay{}, false
	}
	for _, c := range a.cfg.Rewards.Sounds {
		if c.Enabled && twitch.RewardKey(c.Trigger) == key {
			if c.MultiPlay {
				return pendingPlay{file: c.File, kind: "play", multi: true, start: func() error { return a.startSoundMulti(c) }}, true
			}
			return pendingPlay{file: c.File, kind: "play", start: func() error { return a.startSound(c, false) }}, true
		}
	}
	for _, v := range a.cfg.Rewards.Videos {
		if v.Enabled && twitch.RewardKey(v.Trigger) == key {
			return pendingPlay{file: v.File, kind: "video", start: func() error { return a.startVideo(v, false) }}, true
		}
	}
	for _, g := range a.cfg.Rewards.Gifs {
		if g.Enabled && twitch.RewardKey(g.Trigger) == key {
			if g.MultiPlay {
				return pendingPlay{file: g.Image, kind: "gif", multi: true, start: func() error { return a.startGifMulti(g) }}, true
			}
			return pendingPlay{file: g.Image, kind: "gif", start: func() error { return a.startGif(g, false) }}, true
		}
	}
	return pendingPlay{}, false
}

func (a *App) onRedemption(user, reward, input string) {
	a.mu.Lock()
	enabled := a.cfg.CommandsEnabled
	p, ok := a.rewardCommand(reward)
	a.mu.Unlock()
	p.who = fmt.Sprintf(i18n.L("%s (points): %s", "%s (баллы): %s"), user, reward)
	if input = strings.TrimSpace(input); input != "" {
		if r := []rune(input); len(r) > 100 {
			input = string(r[:100]) + "…"
		}
		p.who += i18n.L(" “"+input+"”", " «"+input+"»") // the viewer's text, for rewards that ask for it
	}
	switch {
	case !ok:
		a.log("info", p.who+i18n.L(" — no enabled reward with this title on the Channel Points tab", " — на вкладке «Баллы канала» нет включённой награды с таким названием"))
		return
	case !enabled:
		a.log("warn", p.who+i18n.L(" — all commands are off", " — все команды отключены"))
		return
	case p.file == "":
		a.log("warn", p.who+i18n.L(" — the reward has no file chosen", " — у награды не выбран файл"))
		return
	}

	a.redeemMu.Lock()
	defer a.redeemMu.Unlock()
	// A reward tries to start at once unless one of its kind waits already:
	// single-play rewards wait for the one-at-a-time slot, multi-play ones
	// only while too many multi-play sounds or GIFs are on.
	if !slices.ContainsFunc(a.redeemQueue, func(q pendingPlay) bool { return q.multi == p.multi }) {
		if err := p.start(); !errors.Is(err, audio.ErrBusy) {
			a.logRedeemed(p, err)
			return
		}
	}
	if len(a.redeemQueue) >= maxRedeemQueue {
		a.log("warn", p.who+i18n.L(" — the reward queue is full, skipped", " — очередь наград переполнена, пропущено"))
		return
	}
	a.redeemQueue = append(a.redeemQueue, p)
	a.log("info", fmt.Sprintf(i18n.L("%s — queued (%d)", "%s — в очереди (%d)"), p.who, len(a.redeemQueue)))
}

func (a *App) logRedeemed(p pendingPlay, err error) {
	if err != nil {
		a.log("warn", p.who+" — "+err.Error())
		return
	}
	a.log(p.kind, p.who+" → "+filepath.Base(p.file))
}

// drainRedemptions starts the queued rewards that can start now. It runs
// whenever playback ends. Each kind (single-play, multi-play) keeps its
// order, and one kind waiting doesn't hold up the other.
func (a *App) drainRedemptions() {
	a.redeemMu.Lock()
	defer a.redeemMu.Unlock()
	busy := map[bool]bool{} // by pendingPlay.multi
	kept := a.redeemQueue[:0]
	for _, p := range a.redeemQueue {
		if !busy[p.multi] {
			err := p.start()
			if !errors.Is(err, audio.ErrBusy) {
				a.logRedeemed(p, err) // started, or failed straight away
				continue
			}
			busy[p.multi] = true // try again when something ends
		}
		kept = append(kept, p)
	}
	clear(a.redeemQueue[len(kept):]) // drop the started ones' closures
	a.redeemQueue = kept
}

// clearRedemptions empties the queue (the Stop button means silence).
func (a *App) clearRedemptions() {
	a.redeemMu.Lock()
	n := len(a.redeemQueue)
	a.redeemQueue = nil
	a.redeemMu.Unlock()
	if n > 0 {
		a.log("info", fmt.Sprintf(i18n.L("Reward queue cleared (%d)", "Очередь наград очищена (%d)"), n))
	}
}

// loadRewards fetches the channel's custom rewards for the page.
func (a *App) loadRewards() {
	a.pointsMu.Lock()
	p := a.points
	a.pointsMu.Unlock()
	if p == nil {
		a.emit(map[string]any{"type": "rewards", "error": i18n.L("log in to Twitch first", "сначала войдите в Twitch")})
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	list, err := p.Rewards(ctx)
	ev := map[string]any{"type": "rewards", "rewards": list}
	if err != nil {
		ev["error"] = err.Error()
	}
	a.emit(ev)
}
