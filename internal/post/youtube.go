// Package post publishes approved comments where a platform's API allows it. YouTube does:
// commentThreads.insert posts a top-level comment as the signed-in user (scope
// youtube.force-ssl, 50 units of the project's 10,000 daily). It uses Prateek's own Google
// Cloud OAuth client, not Composio, so the quota is his alone.
package post

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Scope is the one Google permission posting needs.
const Scope = "https://www.googleapis.com/auth/youtube.force-ssl"

// YouTube posts comments as the account behind RefreshToken.
type YouTube struct {
	ClientID, ClientSecret, RefreshToken string
	TokenURL, APIBase                    string // swappable for tests
	HTTP                                 *http.Client

	access string
}

func NewYouTube(clientID, clientSecret, refreshToken string) *YouTube {
	return &YouTube{ClientID: clientID, ClientSecret: clientSecret, RefreshToken: refreshToken,
		TokenURL: "https://oauth2.googleapis.com/token", APIBase: "https://www.googleapis.com/youtube/v3",
		HTTP: &http.Client{Timeout: 30 * time.Second}}
}

// accessToken trades the long-lived refresh token for a short-lived access token, once per run.
func (y *YouTube) accessToken(ctx context.Context) (string, error) {
	if y.access != "" {
		return y.access, nil
	}
	form := url.Values{"client_id": {y.ClientID}, "client_secret": {y.ClientSecret},
		"refresh_token": {y.RefreshToken}, "grant_type": {"refresh_token"}}
	var out struct {
		AccessToken string `json:"access_token"`
		Error       string `json:"error"`
		Description string `json:"error_description"`
	}
	if err := y.postForm(ctx, y.TokenURL, form, &out); err != nil {
		return "", fmt.Errorf("Google sign-in: %w", err)
	}
	if out.AccessToken == "" {
		return "", fmt.Errorf("Google sign-in refused: %s %s (run `reach youtube-login` again)", out.Error, out.Description)
	}
	y.access = out.AccessToken
	return y.access, nil
}

func (y *YouTube) postForm(ctx context.Context, u string, form url.Values, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	res, err := y.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	return json.Unmarshal(raw, out)
}

// Comment posts text as a top-level comment on the video and returns the comment's ID.
func (y *YouTube) Comment(ctx context.Context, videoID, text string) (string, error) {
	token, err := y.accessToken(ctx)
	if err != nil {
		return "", err
	}
	body, _ := json.Marshal(map[string]any{"snippet": map[string]any{
		"videoId":         videoID,
		"topLevelComment": map[string]any{"snippet": map[string]string{"textOriginal": text}},
	}})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, y.APIBase+"/commentThreads?part=snippet", strings.NewReader(string(body)))
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	res, err := y.HTTP.Do(req)
	if err != nil {
		return "", err
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if res.StatusCode != http.StatusOK {
		return "", fmt.Errorf("YouTube HTTP %d: %.300s", res.StatusCode, raw)
	}
	var out struct {
		ID string `json:"id"`
	}
	json.Unmarshal(raw, &out)
	return out.ID, nil
}

// AuthURL is Google's consent page for this client; offline + consent returns a refresh token.
func AuthURL(clientID, redirect, state string) string {
	return "https://accounts.google.com/o/oauth2/v2/auth?" + url.Values{
		"client_id": {clientID}, "redirect_uri": {redirect}, "response_type": {"code"},
		"scope": {Scope}, "access_type": {"offline"}, "prompt": {"consent"}, "state": {state},
	}.Encode()
}

// Exchange trades the code from the consent page for a refresh token.
func Exchange(ctx context.Context, clientID, clientSecret, code, redirect string) (string, error) {
	y := NewYouTube(clientID, clientSecret, "")
	var out struct {
		RefreshToken string `json:"refresh_token"`
		Error        string `json:"error"`
		Description  string `json:"error_description"`
	}
	form := url.Values{"client_id": {clientID}, "client_secret": {clientSecret}, "code": {code},
		"redirect_uri": {redirect}, "grant_type": {"authorization_code"}}
	if err := y.postForm(ctx, y.TokenURL, form, &out); err != nil {
		return "", err
	}
	if out.RefreshToken == "" {
		return "", fmt.Errorf("no refresh token from Google: %s %s", out.Error, out.Description)
	}
	return out.RefreshToken, nil
}
