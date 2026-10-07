// Package composio calls Composio's REST API to run tools (LinkedIn, ScrapeCreators, Google Sheets…)
// on behalf of a connected account. It uses the standard library only.
package composio

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

const defaultBaseURL = "https://backend.composio.dev/api/v3.1"

// Executor runs one tool. Discovery and posting code depend on this interface,
// so tests can swap in a fake that returns fixture data.
type Executor interface {
	Execute(ctx context.Context, slug string, args map[string]any) (json.RawMessage, error)
}

// Client is the real Executor, talking to Composio over HTTPS.
type Client struct {
	APIKey  string
	UserID  string // the Composio user the connected accounts belong to
	BaseURL string
	HTTP    *http.Client
}

func New(apiKey, userID string) *Client {
	return &Client{APIKey: apiKey, UserID: userID, BaseURL: defaultBaseURL, HTTP: &http.Client{Timeout: 60 * time.Second}}
}

type executeRequest struct {
	UserID    string         `json:"user_id,omitempty"`
	Arguments map[string]any `json:"arguments"`
}

type executeResponse struct {
	Data       json.RawMessage `json:"data"`
	Error      *string         `json:"error"`
	Successful bool            `json:"successful"`
	LogID      string          `json:"log_id"`
}

// Execute runs tool `slug` with `args` and returns the tool's `data` payload.
func (c *Client) Execute(ctx context.Context, slug string, args map[string]any) (json.RawMessage, error) {
	body, err := json.Marshal(executeRequest{UserID: c.UserID, Arguments: args})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+"/tools/execute/"+slug, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("x-api-key", c.APIKey)
	req.Header.Set("Content-Type", "application/json")

	res, err := c.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", slug, err)
	}
	defer res.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(res.Body, 20<<20))
	if err != nil {
		return nil, fmt.Errorf("%s: reading response: %w", slug, err)
	}
	if res.StatusCode >= 300 {
		return nil, fmt.Errorf("%s: HTTP %d: %s", slug, res.StatusCode, truncate(string(raw), 300))
	}
	var out executeResponse
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("%s: decoding response: %w", slug, err)
	}
	if !out.Successful {
		msg := "unknown error"
		if out.Error != nil {
			msg = *out.Error
		}
		return nil, fmt.Errorf("%s failed (log %s): %s", slug, out.LogID, msg)
	}
	return out.Data, nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
