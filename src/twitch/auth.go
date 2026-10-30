package twitch

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
	"twitch_easy_media_alerts/src/i18n"
	"unsafe"

	"golang.org/x/sys/windows"
)

// ---------- login: device code flow ----------

type DeviceAuth struct {
	DeviceCode      string `json:"device_code"`
	UserCode        string `json:"user_code"`
	VerificationURI string `json:"verification_uri"`
	ExpiresIn       int    `json:"expires_in"`
	Interval        int    `json:"interval"`
}

// StartDeviceAuth asks Twitch for a code the streamer confirms on
// twitch.tv/activate.
func StartDeviceAuth(ctx context.Context, clientID string) (DeviceAuth, error) {
	var d DeviceAuth
	err := postForm(ctx, twitchIDURL+"/device", url.Values{"client_id": {clientID}, "scopes": {pointsScope}}, &d)
	var apiErr *apiError
	if errors.As(err, &apiErr) && strings.Contains(apiErr.Message, "invalid client") {
		return d, errors.New(i18n.L("Twitch doesn't know this Client ID — check that it's copied in full and that the app's client type is \"Public\"",
			"Twitch не узнал этот Client ID — проверьте, что он скопирован целиком, а тип клиента приложения — «Public»"))
	}
	return d, err
}

type tokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
}

// AwaitDeviceAuth polls Twitch until the streamer confirms the login.
func AwaitDeviceAuth(ctx context.Context, clientID string, d DeviceAuth) (Tokens, error) {
	interval := time.Duration(max(d.Interval, 1)) * time.Second
	deadline := time.Now().Add(time.Duration(max(d.ExpiresIn, 60)) * time.Second)
	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return Tokens{}, ctx.Err()
		case <-time.After(interval):
		}
		var t tokenResponse
		err := postForm(ctx, twitchIDURL+"/token", url.Values{
			"client_id":   {clientID},
			"scopes":      {pointsScope},
			"device_code": {d.DeviceCode},
			"grant_type":  {"urn:ietf:params:oauth:grant-type:device_code"},
		}, &t)
		var apiErr *apiError
		switch {
		case err == nil:
			return Tokens{Access: t.AccessToken, Refresh: t.RefreshToken}, nil
		case errors.As(err, &apiErr) && strings.Contains(apiErr.Message, "authorization_pending"):
			continue
		case errors.As(err, &apiErr) && strings.Contains(apiErr.Message, "slow_down"):
			interval += time.Second
			continue
		case errors.As(err, &apiErr) && strings.Contains(apiErr.Message, "denied"):
			return Tokens{}, errors.New(i18n.L("the login was declined on the Twitch site", "вход отклонён на сайте Twitch"))
		default:
			return Tokens{}, err
		}
	}
	return Tokens{}, errors.New(i18n.L("the login timed out — try again", "время на вход истекло — попробуйте ещё раз"))
}

// refreshTokens gets a new access token. Any refusal means the login is gone.
func refreshTokens(ctx context.Context, clientID, refresh string) (Tokens, error) {
	var t tokenResponse
	err := postForm(ctx, twitchIDURL+"/token", url.Values{
		"client_id":     {clientID},
		"grant_type":    {"refresh_token"},
		"refresh_token": {refresh},
	}, &t)
	var apiErr *apiError
	if errors.As(err, &apiErr) {
		return Tokens{}, fmt.Errorf("%w (%s)", ErrUnauthorized, apiErr.Message)
	}
	if err != nil {
		return Tokens{}, err
	}
	return Tokens{Access: t.AccessToken, Refresh: t.RefreshToken}, nil
}

type TokenInfo struct {
	Login  string   `json:"login"`
	UserID string   `json:"user_id"`
	Scopes []string `json:"scopes"`
}

func ValidateToken(ctx context.Context, access string) (TokenInfo, error) {
	var info TokenInfo
	req, err := http.NewRequestWithContext(ctx, "GET", twitchIDURL+"/validate", nil)
	if err != nil {
		return info, err
	}
	req.Header.Set("Authorization", "OAuth "+access)
	err = twitchCall(req, &info)
	return info, err
}

func RevokeToken(clientID, access string) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = postForm(ctx, twitchIDURL+"/revoke", url.Values{"client_id": {clientID}, "token": {access}}, nil)
}

// ProtectTokens encrypts tokens for the current Windows user (DPAPI), so the
// settings file doesn't hold them in the clear.
func ProtectTokens(t Tokens) (string, error) {
	plain, _ := json.Marshal(t)
	in := windows.DataBlob{Size: uint32(len(plain)), Data: &plain[0]}
	var out windows.DataBlob
	if err := windows.CryptProtectData(&in, nil, nil, 0, nil, windows.CRYPTPROTECT_UI_FORBIDDEN, &out); err != nil {
		return "", err
	}
	defer windows.LocalFree(windows.Handle(unsafe.Pointer(out.Data)))
	return base64.StdEncoding.EncodeToString(unsafe.Slice(out.Data, out.Size)), nil
}

func UnprotectTokens(s string) (Tokens, error) {
	var t Tokens
	enc, err := base64.StdEncoding.DecodeString(s)
	if err != nil || len(enc) == 0 {
		return t, errors.New(i18n.L("the saved login is damaged", "сохранённый вход повреждён"))
	}
	in := windows.DataBlob{Size: uint32(len(enc)), Data: &enc[0]}
	var out windows.DataBlob
	if err := windows.CryptUnprotectData(&in, nil, nil, 0, nil, windows.CRYPTPROTECT_UI_FORBIDDEN, &out); err != nil {
		return t, fmt.Errorf(i18n.L("couldn't read the saved login: %w", "не удалось прочитать сохранённый вход: %w"), err)
	}
	defer windows.LocalFree(windows.Handle(unsafe.Pointer(out.Data)))
	err = json.Unmarshal(unsafe.Slice(out.Data, out.Size), &t)
	return t, err
}
