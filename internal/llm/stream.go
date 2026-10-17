package llm

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// StreamEvent is one piece of an Anthropic-compatible SSE response.
type StreamEvent struct {
	Kind string // "text" | "tool_start" | "tool_ready" | "usage" | "stop" | "error"

	Text       string
	ToolID     string
	ToolName   string
	ToolInput  json.RawMessage
	ToolIndex  int
	StopReason string
	Usage      Usage
	Err        error
}

// StreamHandler receives events as they arrive. A non-nil return cancels the stream.
type StreamHandler func(StreamEvent) error

// startedError means the SSE body had already begun when the call failed,
// so retrying would duplicate tokens the caller already saw.
type startedError struct{ error }

func (e startedError) Unwrap() error { return e.error }

func streamStarted(err error) bool {
	var s startedError
	return errors.As(err, &s)
}

// CallStream is Call with stream:true. Transient failures are retried only
// before the first event is delivered.
func (c *Client) CallStream(ctx context.Context, req Request, h StreamHandler) (*Response, error) {
	body, err := c.encodeStream(req)
	if err != nil {
		return nil, err
	}
	attempts := orDefault(c.MaxAttempts, 5)
	var lastErr error
	for attempt := 1; attempt <= attempts; attempt++ {
		resp, err := c.attemptStream(ctx, body, h)
		if err == nil {
			return resp, nil
		}
		lastErr = err
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if streamStarted(err) || !IsRetryable(err) || attempt == attempts {
			break
		}
		wait := c.backoff(attempt, err)
		if c.Logf != nil {
			c.Logf("[retry] stream attempt %d/%d failed: %v; retrying in %s",
				attempt, attempts, err, wait.Round(time.Millisecond))
		}
		if err := c.doSleep(ctx, wait); err != nil {
			return nil, err
		}
	}
	return nil, lastErr
}

func (c *Client) encodeStream(req Request) ([]byte, error) {
	m := map[string]any{
		"model": c.Model, "max_tokens": orDefault(req.MaxTokens, 4096),
		"system": req.System, "messages": req.Messages, "stream": true,
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

func (c *Client) attemptStream(ctx context.Context, body []byte, h StreamHandler) (*Response, error) {
	ctx, cancel := context.WithTimeout(ctx, orDefault(c.CallTimeout, 120*time.Second))
	defer cancel()

	hreq, err := http.NewRequestWithContext(ctx, "POST", c.BaseURL+"/v1/messages", bytes.NewReader(body))
	if err != nil {
		return nil, permanentError{fmt.Errorf("build request: %w", err)}
	}
	hreq.Header.Set("x-api-key", c.APIKey)
	hreq.Header.Set("anthropic-version", "2023-06-01")
	hreq.Header.Set("content-type", "application/json")
	hreq.Header.Set("accept", "text/event-stream")

	httpc := c.HTTP
	if httpc == nil {
		httpc = http.DefaultClient
	}
	res, err := httpc.Do(hreq)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(io.LimitReader(res.Body, 4096))
		return nil, &APIError{Status: res.StatusCode, Body: preview(string(raw), 300), RetryAfter: parseRetryAfter(res.Header.Get("Retry-After"))}
	}
	return readSSE(res.Body, h)
}

// readSSE parses Anthropic-compatible SSE and rebuilds a full Response.
func readSSE(r io.Reader, h StreamHandler) (*Response, error) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	var (
		resp      Response
		started   bool
		curIndex  = -1
		curType   string
		curID     string
		curName   string
		curInput  strings.Builder
		blocks    []Block
		emitError error
	)
	flushTool := func() {
		if curType != "tool_use" {
			return
		}
		raw := json.RawMessage(curInput.String())
		if !json.Valid(raw) {
			raw = json.RawMessage(`{}`)
		}
		blocks = append(blocks, Block{Type: "tool_use", ID: curID, Name: curName, Input: raw})
		if h != nil {
			_ = h(StreamEvent{Kind: "tool_ready", ToolID: curID, ToolName: curName, ToolInput: raw, ToolIndex: curIndex})
		}
	}

	for sc.Scan() {
		line := sc.Text()
		if line == "" || strings.HasPrefix(line, ":") {
			continue
		}
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "[DONE]" {
			break
		}
		started = true
		var ev map[string]json.RawMessage
		if err := json.Unmarshal([]byte(data), &ev); err != nil {
			continue
		}
		var typ string
		_ = json.Unmarshal(ev["type"], &typ)
		switch typ {
		case "message_start":
			// optional usage in message.usage
			var msg struct {
				Message struct {
					Usage Usage `json:"usage"`
				} `json:"message"`
			}
			_ = json.Unmarshal([]byte(data), &msg)
			if msg.Message.Usage.InputTokens > 0 {
				resp.Usage = msg.Message.Usage
				if h != nil {
					_ = h(StreamEvent{Kind: "usage", Usage: resp.Usage})
				}
			}
		case "content_block_start":
			flushTool()
			var start struct {
				Index        int `json:"index"`
				ContentBlock struct {
					Type  string          `json:"type"`
					ID    string          `json:"id"`
					Name  string          `json:"name"`
					Input json.RawMessage `json:"input"`
					Text  string          `json:"text"`
				} `json:"content_block"`
			}
			_ = json.Unmarshal([]byte(data), &start)
			curIndex, curType = start.Index, start.ContentBlock.Type
			curID, curName = start.ContentBlock.ID, start.ContentBlock.Name
			curInput.Reset()
			if curType == "text" && start.ContentBlock.Text != "" {
				blocks = appendText(blocks, start.ContentBlock.Text)
				if h != nil {
					if err := h(StreamEvent{Kind: "text", Text: start.ContentBlock.Text}); err != nil {
						emitError = err
					}
				}
			}
			if curType == "tool_use" {
				if len(start.ContentBlock.Input) > 0 && string(start.ContentBlock.Input) != "null" {
					curInput.Write(start.ContentBlock.Input)
				}
				if h != nil {
					_ = h(StreamEvent{Kind: "tool_start", ToolID: curID, ToolName: curName, ToolIndex: curIndex})
				}
			}
		case "content_block_delta":
			var delta struct {
				Index int `json:"index"`
				Delta struct {
					Type        string `json:"type"`
					Text        string `json:"text"`
					PartialJSON string `json:"partial_json"`
				} `json:"delta"`
			}
			_ = json.Unmarshal([]byte(data), &delta)
			switch delta.Delta.Type {
			case "text_delta":
				blocks = appendText(blocks, delta.Delta.Text)
				if h != nil && delta.Delta.Text != "" {
					if err := h(StreamEvent{Kind: "text", Text: delta.Delta.Text}); err != nil {
						emitError = err
					}
				}
			case "input_json_delta":
				curInput.WriteString(delta.Delta.PartialJSON)
			}
		case "content_block_stop":
			if curType == "text" {
				// text already accumulated in blocks
			} else {
				flushTool()
			}
			curType, curID, curName = "", "", ""
			curInput.Reset()
			curIndex = -1
		case "message_delta":
			var md struct {
				Delta struct {
					StopReason string `json:"stop_reason"`
				} `json:"delta"`
				Usage Usage `json:"usage"`
			}
			_ = json.Unmarshal([]byte(data), &md)
			if md.Delta.StopReason != "" {
				resp.StopReason = md.Delta.StopReason
			}
			// message_delta.usage is typically output_tokens only
			if md.Usage.OutputTokens > 0 {
				resp.Usage.OutputTokens = md.Usage.OutputTokens
			}
			if md.Usage.InputTokens > 0 {
				resp.Usage.InputTokens = md.Usage.InputTokens
			}
			if md.Usage.CacheRead > 0 {
				resp.Usage.CacheRead = md.Usage.CacheRead
			}
			if md.Usage.CacheCreation > 0 {
				resp.Usage.CacheCreation = md.Usage.CacheCreation
			}
		case "message_stop":
			// done
		case "error":
			var er struct {
				Error struct {
					Message string `json:"message"`
					Type    string `json:"type"`
				} `json:"error"`
			}
			_ = json.Unmarshal([]byte(data), &er)
			msg := er.Error.Message
			if msg == "" {
				msg = data
			}
			err := startedError{fmt.Errorf("stream error: %s", msg)}
			if h != nil {
				_ = h(StreamEvent{Kind: "error", Err: err})
			}
			return nil, err
		}
		if emitError != nil {
			return nil, startedError{emitError}
		}
	}
	if err := sc.Err(); err != nil {
		if started {
			return nil, startedError{err}
		}
		return nil, err
	}
	flushTool()
	resp.Content = blocks
	if resp.StopReason == "" {
		if hasToolUse(blocks) {
			resp.StopReason = "tool_use"
		} else {
			resp.StopReason = "end_turn"
		}
	}
	if h != nil {
		_ = h(StreamEvent{Kind: "stop", StopReason: resp.StopReason, Usage: resp.Usage})
	}
	if len(resp.Content) == 0 {
		return nil, startedError{permanentError{fmt.Errorf("empty stream response")}}
	}
	return &resp, nil
}

func appendText(blocks []Block, text string) []Block {
	if text == "" {
		return blocks
	}
	if n := len(blocks); n > 0 && blocks[n-1].Type == "text" {
		blocks[n-1].Text += text
		return blocks
	}
	return append(blocks, Block{Type: "text", Text: text})
}

func hasToolUse(blocks []Block) bool {
	for _, b := range blocks {
		if b.Type == "tool_use" {
			return true
		}
	}
	return false
}
