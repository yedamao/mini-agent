// Package llm is a small client for the Anthropic Messages API (and compatible
// APIs such as DeepSeek's), with per-call timeouts and retries.
package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"strconv"
	"time"
)

type Message struct {
	Role    string `json:"role"`
	Content any    `json:"content"` // a string or []Block
}

type Block struct {
	Type      string          `json:"type"`
	Text      string          `json:"text,omitempty"`
	ID        string          `json:"id,omitempty"`
	Name      string          `json:"name,omitempty"`
	Input     json.RawMessage `json:"input,omitempty"`
	ToolUseID string          `json:"tool_use_id,omitempty"`
	Content   string          `json:"content,omitempty"`
}

type Usage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
	// Prompt-cache hits are reported separately and NOT included in
	// input_tokens, so the real prompt size is the sum (see PromptTokens).
	CacheRead     int `json:"cache_read_input_tokens"`
	CacheCreation int `json:"cache_creation_input_tokens"`
}

// PromptTokens is the full size of the prompt that was sent, cached or not.
func (u Usage) PromptTokens() int { return u.InputTokens + u.CacheRead + u.CacheCreation }

type Response struct {
	Content    []Block `json:"content"`
	StopReason string  `json:"stop_reason"`
	Usage      Usage   `json:"usage"`
}

// Request is one model call.
type Request struct {
	System    string
	Messages  []Message
	Tools     []map[string]any // nil for no tools
	MaxTokens int              // default 4096
}

// Client calls the API. The zero value is not usable; fill in at least
// BaseURL, Model and APIKey. Other fields have sensible defaults.
type Client struct {
	BaseURL, Model, APIKey string

	HTTP        *http.Client  // default http.DefaultClient
	CallTimeout time.Duration // per attempt; default 120s
	MaxAttempts int           // total tries including the first; default 5
	BaseDelay   time.Duration // first backoff; default 1s
	MaxDelay    time.Duration // cap per wait, also for Retry-After; default 60s

	// Logf, if set, is told about each retry.
	Logf func(format string, args ...any)
	// sleep is replaced in tests so they don't actually wait.
	sleep func(ctx context.Context, d time.Duration) error
}

// APIError is a non-200 HTTP response.
type APIError struct {
	Status     int
	Body       string        // short preview
	RetryAfter time.Duration // from the Retry-After header, 0 if absent
}

func (e *APIError) Error() string { return fmt.Sprintf("api %d: %s", e.Status, e.Body) }

// Retryable reports whether trying again might help: rate limits (429),
// server errors (5xx, including Anthropic's 529 "overloaded").
// Other 4xx (bad request, auth, not found) will fail the same way again.
func (e *APIError) Retryable() bool { return e.Status == http.StatusTooManyRequests || e.Status >= 500 }

// permanentError marks failures that retrying can't fix (bad JSON, etc.).
type permanentError struct{ error }

func (e permanentError) Unwrap() error { return e.error }

// IsRetryable reports whether err from a single attempt is worth retrying.
// Network errors and per-attempt timeouts are; permanent and 4xx errors are not.
func IsRetryable(err error) bool {
	var apiErr *APIError
	var perm permanentError
	switch {
	case errors.As(err, &apiErr):
		return apiErr.Retryable()
	case errors.As(err, &perm), errors.Is(err, context.Canceled):
		return false
	}
	return true // network error, connection reset, per-call timeout...
}

// Call sends the request, retrying transient failures with exponential
// backoff and jitter. It stops early if ctx is cancelled (e.g. Ctrl-C).
func (c *Client) Call(ctx context.Context, req Request) (*Response, error) {
	body, err := c.encode(req)
	if err != nil {
		return nil, err
	}
	attempts := orDefault(c.MaxAttempts, 5)
	var lastErr error
	for attempt := 1; attempt <= attempts; attempt++ {
		resp, err := c.attempt(ctx, body)
		if err == nil {
			return resp, nil
		}
		lastErr = err
		if ctx.Err() != nil {
			return nil, ctx.Err() // cancelled by the caller: stop now
		}
		if !IsRetryable(err) || attempt == attempts {
			break
		}
		wait := c.backoff(attempt, err)
		if c.Logf != nil {
			c.Logf("[retry] attempt %d/%d failed: %v; retrying in %s", attempt, attempts, err, wait.Round(time.Millisecond))
		}
		if err := c.doSleep(ctx, wait); err != nil {
			return nil, err
		}
	}
	return nil, lastErr
}

func (c *Client) encode(req Request) ([]byte, error) {
	m := map[string]any{
		"model": c.Model, "max_tokens": orDefault(req.MaxTokens, 4096),
		"system": req.System, "messages": req.Messages,
	}
	if req.Tools != nil {
		m["tools"] = req.Tools
	}
	body, err := json.Marshal(m)
	if err != nil {
		return nil, permanentError{fmt.Errorf("encode request: %w", err)}
	}
	return body, nil
}

// attempt makes one HTTP call with its own timeout.
func (c *Client) attempt(ctx context.Context, body []byte) (*Response, error) {
	ctx, cancel := context.WithTimeout(ctx, orDefault(c.CallTimeout, 120*time.Second))
	defer cancel()
	hreq, err := http.NewRequestWithContext(ctx, "POST", c.BaseURL+"/v1/messages", bytes.NewReader(body))
	if err != nil {
		return nil, permanentError{fmt.Errorf("build request: %w", err)}
	}
	hreq.Header.Set("x-api-key", c.APIKey)
	hreq.Header.Set("anthropic-version", "2023-06-01")
	hreq.Header.Set("content-type", "application/json")
	httpc := c.HTTP
	if httpc == nil {
		httpc = http.DefaultClient
	}
	res, err := httpc.Do(hreq)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	raw, err := io.ReadAll(res.Body)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}
	if res.StatusCode != http.StatusOK {
		// Error bodies may be HTML or huge; keep a short preview only.
		return nil, &APIError{Status: res.StatusCode, Body: preview(string(raw), 300), RetryAfter: parseRetryAfter(res.Header.Get("Retry-After"))}
	}
	var r Response
	if err := json.Unmarshal(raw, &r); err != nil {
		return nil, permanentError{fmt.Errorf("decode response: %w (body: %s)", err, preview(string(raw), 200))}
	}
	if len(r.Content) == 0 {
		return nil, permanentError{fmt.Errorf("empty response (stop_reason %q)", r.StopReason)}
	}
	return &r, nil
}

// backoff picks the wait before the next attempt: the server's Retry-After
// if it sent one, else exponential backoff (1s, 2s, 4s, ...) with jitter so
// many clients don't retry in lockstep. Both are capped at MaxDelay.
func (c *Client) backoff(attempt int, err error) time.Duration {
	maxDelay := orDefault(c.MaxDelay, 60*time.Second)
	var apiErr *APIError
	if errors.As(err, &apiErr) && apiErr.RetryAfter > 0 {
		return min(apiErr.RetryAfter, maxDelay)
	}
	d := min(orDefault(c.BaseDelay, time.Second)<<(attempt-1), maxDelay)
	// "Equal jitter": half fixed, half random.
	return d/2 + time.Duration(rand.Int64N(int64(d/2)+1))
}

// parseRetryAfter accepts seconds ("3") or an HTTP date.
func parseRetryAfter(v string) time.Duration {
	if v == "" {
		return 0
	}
	if s, err := strconv.Atoi(v); err == nil && s >= 0 {
		return time.Duration(s) * time.Second
	}
	if t, err := http.ParseTime(v); err == nil {
		if d := time.Until(t); d > 0 {
			return d
		}
	}
	return 0
}

func (c *Client) doSleep(ctx context.Context, d time.Duration) error {
	if c.sleep != nil {
		return c.sleep(ctx, d)
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func orDefault[T int | time.Duration](v, def T) T {
	if v > 0 {
		return v
	}
	return def
}

func preview(s string, n int) string {
	r := []rune(s)
	if len(r) > n {
		return string(r[:n]) + "..."
	}
	return s
}
