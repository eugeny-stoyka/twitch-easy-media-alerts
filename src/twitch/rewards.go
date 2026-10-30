package twitch

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"slices"
	"twitch_easy_media_alerts/src/i18n"
)

// subscribeRedemptions asks for reward redemptions on the streamer's channel
// to be sent to an EventSub WebSocket session.
func subscribeRedemptions(ctx context.Context, clientID, access, userID, sessionID string) error {
	body, _ := json.Marshal(map[string]any{
		"type":      redemptionType,
		"version":   "1",
		"condition": map[string]string{"broadcaster_user_id": userID},
		"transport": map[string]string{"method": "websocket", "session_id": sessionID},
	})
	req, err := http.NewRequestWithContext(ctx, "POST", twitchHelixURL+"/eventsub/subscriptions", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+access)
	req.Header.Set("Client-Id", clientID)
	req.Header.Set("Content-Type", "application/json")
	err = twitchCall(req, nil)
	var apiErr *apiError
	if errors.As(err, &apiErr) && apiErr.Status == http.StatusConflict {
		return nil // already subscribed
	}
	return err
}

// Reward is a channel point reward on the streamer's channel.
type Reward struct {
	Title         string `json:"title"`
	Cost          int    `json:"cost"`
	Enabled       bool   `json:"is_enabled"`
	Paused        bool   `json:"is_paused"`
	InputRequired bool   `json:"is_user_input_required"`
}

// FetchRewards lists the custom rewards on the streamer's channel, cheapest
// first.
func FetchRewards(ctx context.Context, clientID, access, userID string) ([]Reward, error) {
	req, err := http.NewRequestWithContext(ctx, "GET",
		twitchHelixURL+"/channel_points/custom_rewards?broadcaster_id="+url.QueryEscape(userID), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+access)
	req.Header.Set("Client-Id", clientID)
	var out struct {
		Data []Reward `json:"data"`
	}
	err = twitchCall(req, &out)
	var apiErr *apiError
	if errors.As(err, &apiErr) && apiErr.Status == http.StatusForbidden {
		return nil, errors.New(i18n.L("the channel has no channel points — only Twitch Affiliates and Partners have them", "у канала нет баллов — они есть только у компаньонов и партнёров Twitch"))
	}
	if err != nil {
		return nil, err
	}
	slices.SortStableFunc(out.Data, func(a, b Reward) int { return a.Cost - b.Cost })
	return out.Data, nil
}
