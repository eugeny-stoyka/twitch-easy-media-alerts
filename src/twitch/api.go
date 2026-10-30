package twitch

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
	"twitch_easy_media_alerts/src/i18n"
)

// Channel points: the streamer logs in with Twitch's device code flow, and an
// EventSub WebSocket session reports reward redemptions on their channel.

// Twitch endpoints; variables so tests can point them at fakes.
var (
	twitchIDURL      = "https://id.twitch.tv/oauth2"
	twitchHelixURL   = "https://api.twitch.tv/helix"
	twitchEventSubWS = "wss://eventsub.wss.twitch.tv/ws"
)

const (
	pointsScope    = "channel:read:redemptions"
	redemptionType = "channel.channel_points_custom_reward_redemption.add"
)

var (
	ErrUnauthorized = i18n.NewError("the Twitch login is no longer valid — log in again", "вход в Twitch больше не действует — войдите заново")
	errRevoked      = i18n.NewError("Twitch revoked the rewards subscription", "Twitch отозвал подписку на награды")
)

var twitchHTTP = &http.Client{Timeout: 15 * time.Second}

type Tokens struct {
	Access  string `json:"access"`
	Refresh string `json:"refresh"`
}

// apiError is an error response from Twitch. 401s (and failed refreshes)
// wrap ErrUnauthorized.
type apiError struct {
	Status  int
	Message string
}

func (e *apiError) Error() string {
	return fmt.Sprintf(i18n.L("Twitch answered %d: %s", "Twitch ответил %d: %s"), e.Status, e.Message)
}

func (e *apiError) Unwrap() error {
	if e.Status == http.StatusUnauthorized {
		return ErrUnauthorized
	}
	return nil
}

func readAPIError(resp *http.Response) error {
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	var e struct {
		Message string `json:"message"`
	}
	_ = json.Unmarshal(b, &e)
	if e.Message == "" {
		e.Message = strings.TrimSpace(string(b))
	}
	return &apiError{Status: resp.StatusCode, Message: e.Message}
}

// twitchCall sends req and decodes a 2xx JSON response into out (if not nil).
func twitchCall(req *http.Request, out any) error {
	resp, err := twitchHTTP.Do(req)
	if err != nil {
		return fmt.Errorf(i18n.L("can't reach Twitch: %w", "нет связи с Twitch: %w"), err)
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return readAPIError(resp)
	}
	if out == nil {
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

func postForm(ctx context.Context, endpoint string, form url.Values, out any) error {
	req, err := http.NewRequestWithContext(ctx, "POST", endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	return twitchCall(req, out)
}
