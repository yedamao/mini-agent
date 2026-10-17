//go:build live

package llm

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"
)

func TestLiveStreamToolInput(t *testing.T) {
	key := os.Getenv("AGENT_API_KEY")
	if key == "" {
		key = os.Getenv("DEEPSEEK_API_KEY")
	}
	if key == "" {
		t.Skip("no API key")
	}
	base := os.Getenv("AGENT_BASE_URL")
	if base == "" {
		base = "https://api.deepseek.com/anthropic"
	}
	model := os.Getenv("AGENT_MODEL")
	if model == "" {
		model = "deepseek-chat"
	}
	c := &Client{APIKey: key, BaseURL: base, Model: model, MaxAttempts: 2, CallTimeout: 60 * time.Second}
	tools := []map[string]any{{
		"name":         "run_shell",
		"description":  "run a shell command",
		"input_schema": json.RawMessage(`{"type":"object","properties":{"command":{"type":"string"}},"required":["command"]}`),
	}}
	req := Request{
		System:   "You must call run_shell with command exactly: echo smoke-ok. No prose.",
		Messages: []Message{{Role: "user", Content: "run echo smoke-ok via the tool now"}},
		Tools:    tools, MaxTokens: 256,
	}
	resp, err := c.CallStream(context.Background(), req, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, b := range resp.Content {
		if b.Type != "tool_use" {
			continue
		}
		t.Logf("tool=%s input=%s", b.Name, string(b.Input))
		var m map[string]any
		if err := json.Unmarshal(b.Input, &m); err != nil {
			t.Fatal(err)
		}
		if m["command"] == nil || m["command"] == "" {
			t.Fatalf("empty command in %s", b.Input)
		}
		return
	}
	t.Fatalf("no tool_use; stop=%s content=%+v", resp.StopReason, resp.Content)
}
