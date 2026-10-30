package twitch

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"
	"twitch_easy_media_alerts/src/i18n"

	"github.com/coder/websocket"
)

// ---------- listening ----------

type PointsCallbacks struct {
	OnStatus     func(state, message string) // connecting, connected, reconnecting, error, off
	OnRedemption func(user, reward, input string)
	OnTokens     func(Tokens) // after a refresh, to save them
}

// PointsListener keeps an EventSub WebSocket session with a redemption
// subscription for the streamer's channel, reconnecting until stopped.
type PointsListener struct {
	clientID, userID string
	cb               PointsCallbacks

	mu  sync.Mutex
	tok Tokens

	seen   map[string]bool // recent message ids: Twitch may send one twice
	recent []string

	authLost atomic.Bool // the hourly check found the login gone; it reported that

	cancel context.CancelFunc
	done   chan struct{}
}

func StartPoints(clientID, userID string, tok Tokens, cb PointsCallbacks) *PointsListener {
	ctx, cancel := context.WithCancel(context.Background())
	p := &PointsListener{clientID: clientID, userID: userID, cb: cb, tok: tok,
		seen: map[string]bool{}, cancel: cancel, done: make(chan struct{})}
	go p.run(ctx)
	return p
}

func (p *PointsListener) Stop() {
	p.cancel()
	<-p.done
}

// withToken runs f with the access token, refreshing it once if Twitch says
// it has expired.
func (p *PointsListener) withToken(ctx context.Context, f func(access string) error) error {
	p.mu.Lock()
	tok := p.tok
	p.mu.Unlock()
	err := f(tok.Access)
	if !errors.Is(err, ErrUnauthorized) {
		return err
	}
	tok, err = refreshTokens(ctx, p.clientID, tok.Refresh)
	if err != nil {
		return err
	}
	p.mu.Lock()
	p.tok = tok
	p.mu.Unlock()
	if p.cb.OnTokens != nil {
		p.cb.OnTokens(tok)
	}
	return f(tok.Access)
}

// Rewards lists the channel's custom rewards with the listener's login.
func (p *PointsListener) Rewards(ctx context.Context) ([]Reward, error) {
	var list []Reward
	err := p.withToken(ctx, func(access string) error {
		var err error
		list, err = FetchRewards(ctx, p.clientID, access, p.userID)
		return err
	})
	return list, err
}

func (p *PointsListener) validate(ctx context.Context) error {
	return p.withToken(ctx, func(access string) error {
		_, err := ValidateToken(ctx, access)
		return err
	})
}

func (p *PointsListener) run(ctx context.Context) {
	defer close(p.done)
	// Twitch wants apps to validate their tokens at start and every hour.
	if err := p.validate(ctx); err != nil && ctx.Err() == nil && errors.Is(err, ErrUnauthorized) {
		p.cb.OnStatus("error", err.Error())
		return
	}
	go func() {
		t := time.NewTicker(time.Hour)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				if err := p.validate(ctx); errors.Is(err, ErrUnauthorized) {
					p.authLost.Store(true)
					p.cb.OnStatus("error", err.Error())
					p.cancel()
				}
			}
		}
	}()

	backoff := time.Second
	moveTo := ""
	for {
		p.cb.OnStatus("connecting", "")
		started := time.Now()
		next, err := p.session(ctx, moveTo)
		if ctx.Err() != nil {
			if !p.authLost.Load() {
				p.cb.OnStatus("off", "")
			}
			return
		}
		moveTo = next
		if next != "" {
			continue // Twitch moved the session; subscriptions carry over
		}
		if errors.Is(err, ErrUnauthorized) || errors.Is(err, errRevoked) {
			p.cb.OnStatus("error", err.Error())
			return
		}
		if time.Since(started) > time.Minute {
			backoff = time.Second
		}
		p.cb.OnStatus("reconnecting", fmt.Sprintf(i18n.L("%v — retrying in %d s", "%v — повтор через %d сек."), err, int(backoff.Seconds())))
		select {
		case <-ctx.Done():
			p.cb.OnStatus("off", "")
			return
		case <-time.After(backoff):
		}
		backoff = min(backoff*2, time.Minute)
	}
}

type eventSubMessage struct {
	Metadata struct {
		MessageID   string `json:"message_id"`
		MessageType string `json:"message_type"`
	} `json:"metadata"`
	Payload struct {
		Session struct {
			ID                      string `json:"id"`
			KeepaliveTimeoutSeconds int    `json:"keepalive_timeout_seconds"`
			ReconnectURL            string `json:"reconnect_url"`
		} `json:"session"`
		Subscription struct {
			Type   string `json:"type"`
			Status string `json:"status"`
		} `json:"subscription"`
		Event struct {
			UserName  string `json:"user_name"`
			UserInput string `json:"user_input"`
			Reward    struct {
				Title string `json:"title"`
			} `json:"reward"`
		} `json:"event"`
	} `json:"payload"`
}

// duplicate reports whether a message id was seen recently.
func (p *PointsListener) duplicate(id string) bool {
	if id == "" {
		return false
	}
	if p.seen[id] {
		return true
	}
	p.seen[id] = true
	p.recent = append(p.recent, id)
	if len(p.recent) > 200 {
		delete(p.seen, p.recent[0])
		p.recent = p.recent[1:]
	}
	return false
}

// session runs one EventSub connection. moveTo is a reconnect URL from the
// previous session (whose subscriptions carry over); a reconnect URL is
// returned when Twitch asks to move again.
func (p *PointsListener) session(ctx context.Context, moveTo string) (string, error) {
	addr := twitchEventSubWS
	if moveTo != "" {
		addr = moveTo
	}
	dialCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	conn, _, err := websocket.Dial(dialCtx, addr, nil)
	cancel()
	if err != nil {
		return "", fmt.Errorf(i18n.L("can't reach Twitch EventSub: %w", "нет связи с Twitch EventSub: %w"), err)
	}
	defer conn.CloseNow()
	conn.SetReadLimit(1 << 20)

	keepalive := 30 * time.Second // until the welcome message says otherwise
	subscribed := moveTo != ""
	for {
		readCtx, cancel := context.WithTimeout(ctx, keepalive+10*time.Second)
		_, data, err := conn.Read(readCtx)
		cancel()
		if err != nil {
			if ctx.Err() != nil {
				return "", ctx.Err()
			}
			return "", fmt.Errorf(i18n.L("lost the connection to Twitch EventSub: %w", "связь с Twitch EventSub потеряна: %w"), err)
		}
		var msg eventSubMessage
		if json.Unmarshal(data, &msg) != nil || p.duplicate(msg.Metadata.MessageID) {
			continue
		}
		pl := msg.Payload
		switch msg.Metadata.MessageType {
		case "session_welcome":
			if s := pl.Session.KeepaliveTimeoutSeconds; s > 0 {
				keepalive = time.Duration(s) * time.Second
			}
			if !subscribed {
				err := p.withToken(ctx, func(access string) error {
					return subscribeRedemptions(ctx, p.clientID, access, p.userID, pl.Session.ID)
				})
				if err != nil {
					return "", fmt.Errorf(i18n.L("couldn't subscribe to rewards: %w", "не удалось подписаться на награды: %w"), err)
				}
				subscribed = true
			}
			p.cb.OnStatus("connected", "")
		case "notification":
			if pl.Subscription.Type == redemptionType {
				p.cb.OnRedemption(pl.Event.UserName, pl.Event.Reward.Title, pl.Event.UserInput)
			}
		case "session_reconnect":
			return pl.Session.ReconnectURL, nil
		case "revocation":
			return "", fmt.Errorf("%w (%s)", errRevoked, pl.Subscription.Status)
		}
	}
}
