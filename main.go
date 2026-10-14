// Day 1: one model call with one tool (get_time), no frameworks.
// Run: ANTHROPIC_API_KEY=... go run . "what time is it?"
package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

const model = "claude-sonnet-4-5"

type Message struct {
	Role    string `json:"role"`
	Content any    `json:"content"`
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

type Response struct {
	Content    []Block `json:"content"`
	StopReason string  `json:"stop_reason"`
}

var tools = []map[string]any{{
	"name":         "get_time",
	"description":  "Returns the current local date and time.",
	"input_schema": map[string]any{"type": "object", "properties": map[string]any{}},
}}

func runTool(name string, _ json.RawMessage) string {
	switch name {
	case "get_time":
		return time.Now().Format(time.RFC1123)
	}
	return "unknown tool: " + name
}

func call(msgs []Message) (*Response, error) {
	body, _ := json.Marshal(map[string]any{
		"model": model, "max_tokens": 1024, "tools": tools, "messages": msgs,
	})
	req, _ := http.NewRequest("POST", "https://api.anthropic.com/v1/messages", bytes.NewReader(body))
	req.Header.Set("x-api-key", os.Getenv("ANTHROPIC_API_KEY"))
	req.Header.Set("anthropic-version", "2023-06-01")
	req.Header.Set("content-type", "application/json")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(res.Body)
	if res.StatusCode != 200 {
		return nil, fmt.Errorf("api %d: %s", res.StatusCode, raw)
	}
	var r Response
	return &r, json.Unmarshal(raw, &r)
}

func main() {
	prompt := "What time is it right now?"
	if len(os.Args) > 1 {
		prompt = strings.Join(os.Args[1:], " ")
	}
	msgs := []Message{{Role: "user", Content: prompt}}

	// Day 3 turns this into a real loop with a step cap.
	for step := 1; step <= 5; step++ {
		r, err := call(msgs)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		msgs = append(msgs, Message{Role: "assistant", Content: r.Content})
		var results []Block
		for _, b := range r.Content {
			switch b.Type {
			case "text":
				fmt.Println(b.Text)
			case "tool_use":
				out := runTool(b.Name, b.Input)
				fmt.Printf("[step %d] tool %s -> %s\n", step, b.Name, out)
				results = append(results, Block{Type: "tool_result", ToolUseID: b.ID, Content: out})
			}
		}
		if r.StopReason != "tool_use" {
			return
		}
		msgs = append(msgs, Message{Role: "user", Content: results})
	}
}
