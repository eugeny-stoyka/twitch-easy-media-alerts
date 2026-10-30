// Package twitch talks to Twitch: the channel chat (anonymous IRC over TLS),
// matching chat messages to commands, and channel points (device code login,
// the Helix API and an EventSub WebSocket session).
package twitch

import (
	"bufio"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net"
	"regexp"
	"strings"
	"sync"
	"time"
	"twitch_easy_media_alerts/src/i18n"
)

const twitchIRCAddr = "irc.chat.twitch.tv:6697"

type ChatState string

const (
	StateDisconnected ChatState = "disconnected"
	StateConnecting   ChatState = "connecting"
	StateConnected    ChatState = "connected"
	StateReconnecting ChatState = "reconnecting"
)

type ChatStatus struct {
	State   ChatState `json:"state"`
	Channel string    `json:"channel"`
	Message string    `json:"message"`
}

type ChatCallbacks struct {
	OnStatus  func(ChatStatus)
	OnMessage func(user, text string)
	OnNotice  func(text string)
}

// ChatClient reads a channel's chat anonymously (justinfan login, read-only)
// and reconnects until stopped.
type ChatClient struct {
	channel string
	cb      ChatCallbacks
	cancel  context.CancelFunc
	done    chan struct{}
}

func StartChat(channel string, cb ChatCallbacks) *ChatClient {
	ctx, cancel := context.WithCancel(context.Background())
	c := &ChatClient{channel: channel, cb: cb, cancel: cancel, done: make(chan struct{})}
	go c.run(ctx)
	return c
}

func (c *ChatClient) Stop() {
	c.cancel()
	<-c.done
}

func (c *ChatClient) status(state ChatState, msg string) {
	c.cb.OnStatus(ChatStatus{State: state, Channel: c.channel, Message: msg})
}

func (c *ChatClient) run(ctx context.Context) {
	defer close(c.done)
	backoff := time.Second
	for {
		c.status(StateConnecting, "")
		started := time.Now()
		err := c.session(ctx)
		if ctx.Err() != nil {
			c.status(StateDisconnected, "")
			return
		}
		if time.Since(started) > time.Minute {
			backoff = time.Second
		}
		msg := fmt.Sprintf(i18n.L("%v — retrying in %d s", "%v — повтор через %d сек."), err, int(backoff.Seconds()))
		c.status(StateReconnecting, msg)
		select {
		case <-ctx.Done():
			c.status(StateDisconnected, "")
			return
		case <-time.After(backoff):
		}
		backoff = min(backoff*2, 30*time.Second)
	}
}

func (c *ChatClient) session(ctx context.Context) error {
	dialer := &tls.Dialer{
		NetDialer: &net.Dialer{Timeout: 10 * time.Second},
		Config:    &tls.Config{ServerName: "irc.chat.twitch.tv"},
	}
	conn, err := dialer.DialContext(ctx, "tcp", twitchIRCAddr)
	if err != nil {
		return fmt.Errorf(i18n.L("couldn't connect to Twitch: %w", "не удалось подключиться к Twitch: %w"), err)
	}
	defer conn.Close()
	stopClose := context.AfterFunc(ctx, func() { conn.Close() })
	defer stopClose()

	var wmu sync.Mutex
	send := func(line string) error {
		wmu.Lock()
		defer wmu.Unlock()
		_ = conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
		_, err := io.WriteString(conn, line+"\r\n")
		return err
	}

	nick := fmt.Sprintf("justinfan%d", 10000+rand.IntN(89999))
	for _, line := range []string{
		"CAP REQ :twitch.tv/tags twitch.tv/commands",
		"PASS SCHMOOPIIE",
		"NICK " + nick,
		"JOIN #" + c.channel,
	} {
		if err := send(line); err != nil {
			return fmt.Errorf(i18n.L("sending failed: %w", "ошибка отправки: %w"), err)
		}
	}

	// Our own pings detect half-dead connections faster than Twitch's 5-minute ones.
	sessionDone := make(chan struct{})
	defer close(sessionDone)
	go func() {
		t := time.NewTicker(60 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-sessionDone:
				return
			case <-t.C:
				_ = send("PING :tmi.twitch.tv")
			}
		}
	}()

	r := bufio.NewReaderSize(conn, 64*1024)
	for {
		_ = conn.SetReadDeadline(time.Now().Add(3 * time.Minute))
		line, err := r.ReadString('\n')
		if err != nil {
			if errors.Is(err, io.EOF) {
				return errors.New(i18n.L("the server closed the connection", "соединение закрыто сервером"))
			}
			return fmt.Errorf(i18n.L("connection lost: %w", "соединение потеряно: %w"), err)
		}
		msg := parseIRC(strings.TrimRight(line, "\r\n"))
		switch msg.Command {
		case "PING":
			_ = send("PONG :" + msg.trailing())
		case "JOIN":
			if strings.EqualFold(msg.nick(), nick) {
				c.status(StateConnected, "")
			}
		case "PRIVMSG":
			user := msg.Tags["display-name"]
			if user == "" {
				user = msg.nick()
			}
			c.cb.OnMessage(user, stripAction(msg.trailing()))
		case "NOTICE":
			c.cb.OnNotice(msg.trailing())
		case "RECONNECT":
			return errors.New(i18n.L("Twitch asked to reconnect", "Twitch попросил переподключиться"))
		}
	}
}

type ircMessage struct {
	Tags    map[string]string
	Prefix  string
	Command string
	Params  []string
}

func (m ircMessage) trailing() string {
	if len(m.Params) == 0 {
		return ""
	}
	return m.Params[len(m.Params)-1]
}

func (m ircMessage) nick() string {
	nick, _, _ := strings.Cut(m.Prefix, "!")
	return nick
}

var tagUnescaper = strings.NewReplacer(`\s`, " ", `\:`, ";", `\\`, `\`, `\r`, "\r", `\n`, "\n")

func parseIRC(line string) ircMessage {
	var m ircMessage
	if strings.HasPrefix(line, "@") {
		var tags string
		tags, line, _ = strings.Cut(line[1:], " ")
		m.Tags = map[string]string{}
		for kv := range strings.SplitSeq(tags, ";") {
			k, v, _ := strings.Cut(kv, "=")
			m.Tags[k] = tagUnescaper.Replace(v)
		}
	}
	if strings.HasPrefix(line, ":") {
		m.Prefix, line, _ = strings.Cut(line[1:], " ")
	}
	var trailing string
	hasTrailing := false
	if i := strings.Index(line, " :"); i >= 0 {
		trailing, line, hasTrailing = line[i+2:], line[:i], true
	}
	fields := strings.Fields(line)
	if len(fields) > 0 {
		m.Command, m.Params = fields[0], fields[1:]
	}
	if hasTrailing {
		m.Params = append(m.Params, trailing)
	}
	return m
}

// stripAction turns a "/me" message (\x01ACTION text\x01) into plain text.
func stripAction(text string) string {
	if s, ok := strings.CutPrefix(text, "\x01ACTION "); ok {
		return strings.TrimSuffix(s, "\x01")
	}
	return text
}

var channelNameRe = regexp.MustCompile(`^[a-z0-9_]{2,25}$`)

// ParseChannel accepts a channel link (twitch.tv/name, popout chat link, …)
// or a bare name and returns the lowercase login.
func ParseChannel(input string) (string, error) {
	s := strings.ToLower(strings.TrimSpace(input))
	if i := strings.Index(s, "twitch.tv/"); i >= 0 {
		s = s[i+len("twitch.tv/"):]
	}
	for _, p := range []string{"popout/", "moderator/", "embed/"} {
		s = strings.TrimPrefix(s, p)
	}
	s = strings.TrimLeft(s, "#@")
	if i := strings.IndexAny(s, "/?#&"); i >= 0 {
		s = s[:i]
	}
	if !channelNameRe.MatchString(s) {
		return "", fmt.Errorf(i18n.L("that doesn't look like a Twitch channel link: %q", "не похоже на ссылку на канал Twitch: %q"), input)
	}
	return s, nil
}
