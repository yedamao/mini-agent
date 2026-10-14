// Day 3: an agent loop with file and shell tools (see tools.go), no frameworks.
// Run: go run . "what time is it?"   (uses $DEEPSEEK_API_KEY by default)
package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
)

// Works with Anthropic or any Anthropic-compatible API (e.g. DeepSeek).
var (
	baseURL = env("AGENT_BASE_URL", "https://api.deepseek.com/anthropic")
	model   = env("AGENT_MODEL", "deepseek-chat")
	apiKey  = env("AGENT_API_KEY", os.Getenv("DEEPSEEK_API_KEY"))
)

// systemPrompt tells the model how to behave as a coding agent.
const systemPrompt = `You are a coding agent working in the current directory.
Work step by step: inspect files (ls, read_file), run the tests (go test ./...),
make the smallest fix with write_file, then re-run the tests until they pass.
Never guess file contents; read them first. Finish with a short summary of what you changed.`

// maxSteps caps the loop so a confused model can't run forever.
const maxSteps = 20

func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

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

func call(msgs []Message) (*Response, error) {
	body, _ := json.Marshal(map[string]any{
		"model": model, "max_tokens": 4096, "system": systemPrompt,
		"tools": tools, "messages": msgs,
	})
	req, _ := http.NewRequest("POST", baseURL+"/v1/messages", bytes.NewReader(body))
	req.Header.Set("x-api-key", apiKey)
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

	// The agent loop: call the model, run any tools it asks for, send results back.
	// Stop when the model answers without a tool call, or after maxSteps.
	for step := 1; step <= maxSteps; step++ {
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
				// Log to stderr so stdout stays just the model's answer.
				fmt.Fprintf(os.Stderr, "[step %d] %s %s -> %s\n",
					step, b.Name, preview(string(b.Input), 80), preview(out, 120))
				results = append(results, Block{Type: "tool_result", ToolUseID: b.ID, Content: out})
			}
		}
		if r.StopReason != "tool_use" {
			return
		}
		msgs = append(msgs, Message{Role: "user", Content: results})
	}
	fmt.Fprintf(os.Stderr, "stopped: hit the %d-step limit without a final answer\n", maxSteps)
	os.Exit(2)
}
