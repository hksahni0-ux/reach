// Package llm asks a chain of models on NVIDIA's API, in order, until one answers.
// Five makers on one key, mirroring the website assistant's chain, so a retirement or
// outage of any one model doesn't stop a run.
package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"
)

const defaultURL = "https://integrate.api.nvidia.com/v1/chat/completions"

// DefaultModels is the fallback order. REACH_MODELS (comma-separated) overrides it.
// NVIDIA's Mistral and Google models were retired or unresponsive when this was set
// (Oct 2026), and Qwen isn't hosted there, so the chain is three makers rather than five.
// The Super models lead because on 16 Oct 2026 the others hung until timeout on every
// call, which cost each draft minutes and ran whole runs out of time; Nemotron 3 Ultra was
// retired the same day (HTTP 404). Nemotron 3.5 Super wrote the sharpest drafts in testing.
var DefaultModels = []string{
	"nvidia/nemotron-3.5-super-vl-120b-a12b", // NVIDIA, newest
	"nvidia/nemotron-3-super-120b-a12b",      // NVIDIA, steady backup
	"deepseek-ai/deepseek-v4.1-flash",        // DeepSeek
	"meta/llama-3.2-90b-vision-instruct",     // Meta
}

// Message is one chat turn.
type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// Client talks to NVIDIA's OpenAI-compatible endpoint.
type Client struct {
	APIKey   string
	URL      string
	Models   []string
	PerModel time.Duration // give up on a model after this long and try the next
	BusyWait time.Duration // wait this long and retry once when a model says it's overloaded
	HTTP     *http.Client
}

func New(apiKey string, models []string) *Client {
	if len(models) == 0 {
		models = DefaultModels
	}
	return &Client{APIKey: apiKey, URL: defaultURL, Models: models, PerModel: 90 * time.Second, BusyWait: 10 * time.Second, HTTP: &http.Client{}}
}

// Chat returns the first non-empty answer in the chain and the model that gave it.
func (c *Client) Chat(ctx context.Context, msgs []Message) (answer, model string, err error) {
	var errs []error
	for _, m := range c.Models {
		answer, err := c.one(ctx, m, msgs)
		if busy(err) && ctx.Err() == nil {
			// NVIDIA sheds load with 503/429 for a few seconds at a time; one short wait usually
			// gets an answer from the better model instead of falling down the chain.
			select {
			case <-time.After(c.BusyWait):
				answer, err = c.one(ctx, m, msgs)
			case <-ctx.Done():
			}
		}
		if err == nil && answer != "" {
			return answer, m, nil
		}
		if err == nil {
			err = errors.New("empty answer")
		}
		errs = append(errs, fmt.Errorf("%s: %w", m, err))
		if ctx.Err() != nil {
			break
		}
	}
	return "", "", fmt.Errorf("every model failed: %w", errors.Join(errs...))
}

type chatRequest struct {
	Model       string    `json:"model"`
	Messages    []Message `json:"messages"`
	Temperature float64   `json:"temperature"`
	TopP        float64   `json:"top_p"`
	MaxTokens   int       `json:"max_tokens"`
}

type chatResponse struct {
	Choices []struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
	} `json:"choices"`
}

func (c *Client) one(ctx context.Context, model string, msgs []Message) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, c.PerModel)
	defer cancel()
	// Nemotron reasons before answering, often 2,000+ tokens for a 200-character comment;
	// at a 2,000 cap it ran out mid-thought and answered nothing, so leave plenty of room.
	body, _ := json.Marshal(chatRequest{Model: model, Messages: msgs, Temperature: 0.6, TopP: 0.9, MaxTokens: 8000})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.URL, bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+c.APIKey)
	req.Header.Set("Content-Type", "application/json")
	res, err := c.HTTP.Do(req)
	if err != nil {
		return "", err
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(res.Body, 4<<20))
	if res.StatusCode >= 300 {
		return "", fmt.Errorf("HTTP %d: %.200s", res.StatusCode, raw)
	}
	var out chatResponse
	if err := json.Unmarshal(raw, &out); err != nil {
		return "", fmt.Errorf("decoding: %w", err)
	}
	if len(out.Choices) == 0 {
		return "", nil
	}
	return StripReasoning(out.Choices[0].Message.Content), nil
}

var thinkBlock = regexp.MustCompile(`(?s)<think>.*?</think>`)

// StripReasoning drops a model's visible reasoning, keeping only the answer.
func StripReasoning(s string) string {
	s = thinkBlock.ReplaceAllString(s, "")
	if i := strings.LastIndex(s, "</think>"); i >= 0 { // reasoning without an opening tag
		s = s[i+len("</think>"):]
	}
	return strings.TrimSpace(s)
}

// busy reports NVIDIA's "overloaded" and rate-limit answers.
func busy(err error) bool {
	return err != nil && (strings.Contains(err.Error(), "HTTP 503") || strings.Contains(err.Error(), "HTTP 429"))
}
