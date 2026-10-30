package twitch

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
)

func TestProtectTokensRoundTrip(t *testing.T) {
	in := Tokens{Access: "access-123", Refresh: "refresh-456"}
	enc, err := ProtectTokens(in)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(enc, "access-123") {
		t.Fatal("tokens stored in the clear")
	}
	out, err := UnprotectTokens(enc)
	if err != nil || out != in {
		t.Fatalf("round trip = %+v, %v", out, err)
	}
	if _, err := UnprotectTokens("bm90IGRwYXBp"); err == nil {
		t.Error("garbage decrypted without error")
	}
}

// fakeTwitchID serves the OAuth endpoints; token handles POST /token.
func fakeTwitchID(t *testing.T, token http.HandlerFunc) {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("POST /device", func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		if r.Form.Get("client_id") != "client" || r.Form.Get("scopes") != pointsScope {
			http.Error(w, `{"status":400,"message":"bad request"}`, 400)
			return
		}
		fmt.Fprint(w, `{"device_code":"dc","user_code":"ABCD-EFGH","verification_uri":"https://www.twitch.tv/activate?device-code=ABCD-EFGH","expires_in":60,"interval":0}`)
	})
	mux.HandleFunc("POST /token", token)
	mux.HandleFunc("GET /validate", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") == "OAuth expired" {
			http.Error(w, `{"status":401,"message":"invalid access token"}`, 401)
			return
		}
		fmt.Fprint(w, `{"login":"streamer","user_id":"42","scopes":["channel:read:redemptions"]}`)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	old := twitchIDURL
	twitchIDURL = srv.URL
	t.Cleanup(func() { twitchIDURL = old })
}

func TestDeviceLogin(t *testing.T) {
	calls := 0
	fakeTwitchID(t, func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		if r.Form.Get("grant_type") != "urn:ietf:params:oauth:grant-type:device_code" || r.Form.Get("device_code") != "dc" {
			http.Error(w, `{"status":400,"message":"wrong grant"}`, 400)
			return
		}
		calls++
		if calls == 1 {
			http.Error(w, `{"status":400,"message":"authorization_pending"}`, 400)
			return
		}
		fmt.Fprint(w, `{"access_token":"at","refresh_token":"rt","scope":["channel:read:redemptions"]}`)
	})
	ctx := context.Background()
	d, err := StartDeviceAuth(ctx, "client")
	if err != nil || d.UserCode != "ABCD-EFGH" {
		t.Fatalf("StartDeviceAuth = %+v, %v", d, err)
	}
	tok, err := AwaitDeviceAuth(ctx, "client", d)
	if err != nil || tok != (Tokens{"at", "rt"}) || calls != 2 {
		t.Fatalf("AwaitDeviceAuth = %+v, %v after %d calls", tok, err, calls)
	}
	info, err := ValidateToken(ctx, "at")
	if err != nil || info.Login != "streamer" || info.UserID != "42" {
		t.Fatalf("ValidateToken = %+v, %v", info, err)
	}
	if _, err := ValidateToken(ctx, "expired"); !errors.Is(err, ErrUnauthorized) {
		t.Errorf("expired token: %v, want ErrUnauthorized", err)
	}
}

func TestRefreshRefusedMeansLoggedOut(t *testing.T) {
	fakeTwitchID(t, func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"status":400,"message":"Invalid refresh token"}`, 400)
	})
	if _, err := refreshTokens(context.Background(), "client", "old"); !errors.Is(err, ErrUnauthorized) {
		t.Errorf("refused refresh: %v, want ErrUnauthorized", err)
	}
}

func eventSubMsg(id, typ string, payload string) string {
	return fmt.Sprintf(`{"metadata":{"message_id":%q,"message_type":%q},"payload":%s}`, id, typ, payload)
}

func redemptionMsg(id, reward string) string {
	return eventSubMsg(id, "notification", fmt.Sprintf(
		`{"subscription":{"type":%q},"event":{"user_name":"viewer","user_input":"","reward":{"title":%q}}}`,
		redemptionType, reward))
}

func TestPointsListener(t *testing.T) {
	fakeTwitchID(t, func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		if r.Form.Get("grant_type") != "refresh_token" || r.Form.Get("refresh_token") != "rt" {
			http.Error(w, `{"status":400,"message":"bad refresh"}`, 400)
			return
		}
		fmt.Fprint(w, `{"access_token":"fresh","refresh_token":"rt2"}`)
	})

	// Helix: the first subscribe sees an expired token, which forces a refresh.
	var (
		mu       sync.Mutex
		subs     []string
		attempts int
	)
	subscribed := make(chan struct{}, 4)
	helix := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		attempts++
		if r.Header.Get("Authorization") != "Bearer fresh" || r.Header.Get("Client-Id") != "client" {
			http.Error(w, `{"status":401,"message":"Invalid OAuth token"}`, 401)
			return
		}
		var body struct {
			Type      string            `json:"type"`
			Condition map[string]string `json:"condition"`
			Transport map[string]string `json:"transport"`
		}
		b, _ := io.ReadAll(r.Body)
		json.Unmarshal(b, &body)
		if body.Type != redemptionType || body.Condition["broadcaster_user_id"] != "42" || body.Transport["method"] != "websocket" {
			http.Error(w, `{"status":400,"message":"bad body"}`, 400)
			return
		}
		subs = append(subs, body.Transport["session_id"])
		w.WriteHeader(http.StatusAccepted)
		subscribed <- struct{}{}
	}))
	defer helix.Close()
	oldHelix := twitchHelixURL
	twitchHelixURL = helix.URL
	defer func() { twitchHelixURL = oldHelix }()

	// EventSub: /ws1 welcomes, sends a reward (twice, as Twitch may) and
	// moves the session to /ws2, which sends another reward.
	var wsURL string
	ws := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer c.CloseNow()
		ctx := r.Context()
		send := func(s string) { c.Write(ctx, websocket.MessageText, []byte(s)) }
		switch r.URL.Path {
		case "/ws1":
			send(eventSubMsg("w1", "session_welcome", `{"session":{"id":"s1","keepalive_timeout_seconds":10}}`))
			select {
			case <-subscribed:
			case <-time.After(5 * time.Second):
				return
			}
			send(redemptionMsg("m1", "Sad"))
			send(redemptionMsg("m1", "Sad")) // duplicate
			send(eventSubMsg("r1", "session_reconnect", fmt.Sprintf(`{"session":{"id":"s1","reconnect_url":%q}}`, wsURL+"/ws2")))
		case "/ws2":
			send(eventSubMsg("w2", "session_welcome", `{"session":{"id":"s2","keepalive_timeout_seconds":10}}`))
			send(redemptionMsg("m2", "Angry"))
		}
		c.Read(ctx) // until the client goes away
	}))
	defer ws.Close()
	wsURL = "ws" + strings.TrimPrefix(ws.URL, "http")
	oldWS := twitchEventSubWS
	twitchEventSubWS = wsURL + "/ws1"
	defer func() { twitchEventSubWS = oldWS }()

	rewards := make(chan string, 10)
	var savedTokens Tokens
	var statusMu sync.Mutex
	var statuses []string
	p := StartPoints("client", "42", Tokens{Access: "expired", Refresh: "rt"}, PointsCallbacks{
		OnStatus: func(state, msg string) {
			statusMu.Lock()
			statuses = append(statuses, state)
			statusMu.Unlock()
		},
		OnRedemption: func(user, reward, input string) { rewards <- user + ":" + reward },
		OnTokens:     func(tok Tokens) { savedTokens = tok },
	})
	var got []string
	for len(got) < 2 {
		select {
		case r := <-rewards:
			got = append(got, r)
		case <-time.After(10 * time.Second):
			p.Stop()
			t.Fatalf("rewards so far: %v; statuses %v", got, statuses)
		}
	}
	select {
	case r := <-rewards:
		t.Errorf("duplicate message delivered: %s", r)
	case <-time.After(200 * time.Millisecond):
	}
	p.Stop()

	if strings.Join(got, ",") != "viewer:Sad,viewer:Angry" {
		t.Errorf("rewards = %v", got)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(subs) != 1 || subs[0] != "s1" {
		t.Errorf("subscriptions = %v; the moved session must not subscribe again", subs)
	}
	if savedTokens != (Tokens{"fresh", "rt2"}) {
		t.Errorf("refreshed tokens not saved: %+v", savedTokens)
	}
	statusMu.Lock()
	defer statusMu.Unlock()
	if !strings.Contains(strings.Join(statuses, ","), "connected") || statuses[len(statuses)-1] != "off" {
		t.Errorf("statuses = %v", statuses)
	}
}

func TestFetchRewards(t *testing.T) {
	helix := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/channel_points/custom_rewards" || r.URL.Query().Get("broadcaster_id") != "42" ||
			r.Header.Get("Authorization") != "Bearer at" || r.Header.Get("Client-Id") != "client" {
			http.Error(w, `{"status":400,"message":"bad request"}`, 400)
			return
		}
		fmt.Fprint(w, `{"data":[
			{"title":"Hydrate","cost":500,"is_enabled":true,"is_user_input_required":false},
			{"title":"Sad","cost":100,"is_enabled":true,"is_user_input_required":true},
			{"title":"Paused one","cost":300,"is_enabled":true,"is_paused":true}]}`)
	}))
	defer helix.Close()
	old := twitchHelixURL
	twitchHelixURL = helix.URL
	defer func() { twitchHelixURL = old }()

	list, err := FetchRewards(context.Background(), "client", "at", "42")
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, r := range list {
		got = append(got, fmt.Sprintf("%s/%d/%v/%v", r.Title, r.Cost, r.InputRequired, r.Paused))
	}
	if strings.Join(got, ",") != "Sad/100/true/false,Paused one/300/false/true,Hydrate/500/false/false" {
		t.Errorf("rewards = %v", got)
	}

	notAffiliate := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"status":403,"message":"The broadcaster must have partner or affiliate status."}`, 403)
	}))
	defer notAffiliate.Close()
	twitchHelixURL = notAffiliate.URL
	if _, err := FetchRewards(context.Background(), "client", "at", "42"); err == nil || !strings.Contains(err.Error(), "Affiliates") {
		t.Errorf("non-affiliate error = %v", err)
	}
}
