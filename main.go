// mini-agent: an agent loop with file and shell tools (tools.go) and
// context management (context.go), no frameworks.
// Run: go run . "what time is it?"   or   go run . -i   for an interactive session.
package main

import (
	"bufio"
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

type Usage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
}

type Response struct {
	Content    []Block `json:"content"`
	StopReason string  `json:"stop_reason"`
	Usage      Usage   `json:"usage"`
}

// callModel sends one request. withTools=false is used for summarization.
func callModel(system string, msgs []Message, withTools bool) (*Response, error) {
	req := map[string]any{"model": model, "max_tokens": 4096, "system": system, "messages": msgs}
	if withTools {
		req["tools"] = tools
	}
	body, _ := json.Marshal(req)
	hreq, _ := http.NewRequest("POST", baseURL+"/v1/messages", bytes.NewReader(body))
	hreq.Header.Set("x-api-key", apiKey)
	hreq.Header.Set("anthropic-version", "2023-06-01")
	hreq.Header.Set("content-type", "application/json")
	res, err := http.DefaultClient.Do(hreq)
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
	args := os.Args[1:]
	if len(args) > 0 && args[0] == "-i" {
		repl()
		return
	}
	prompt := "What time is it right now?"
	if len(args) > 0 {
		prompt = strings.Join(args, " ")
	}
	msgs := []Message{{Role: "user", Content: prompt}}
	if _, ok := runAgent(msgs); !ok {
		os.Exit(2)
	}
}

// repl keeps one conversation going across prompts (type "exit" to quit).
func repl() {
	var msgs []Message
	in := bufio.NewScanner(os.Stdin)
	for {
		fmt.Fprint(os.Stderr, "> ")
		if !in.Scan() || in.Text() == "exit" {
			return
		}
		if strings.TrimSpace(in.Text()) == "" {
			continue
		}
		msgs = append(msgs, Message{Role: "user", Content: in.Text()})
		msgs, _ = runAgent(msgs)
	}
}

// runAgent is the agent loop: call the model, run any tools it asks for,
// send results back, and keep the context small. It returns the updated
// history and false if it hit maxSteps.
func runAgent(msgs []Message) ([]Message, bool) {
	for step := 1; step <= maxSteps; step++ {
		r, err := callModel(systemPrompt, msgs, true)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		logUsage(fmt.Sprintf("step %d", step), r.Usage)
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
			return msgs, true
		}
		msgs = append(msgs, Message{Role: "user", Content: results})

		// Cheap first: stub out old tool results.
		if n := trimOldResults(msgs); n > 0 {
			fmt.Fprintf(os.Stderr, "[context] trimmed %d old tool results\n", n)
		}
		// Then, if the last prompt was over budget, summarize the middle.
		if r.Usage.InputTokens > contextBudget {
			before := len(msgs)
			if msgs, err = compact(msgs); err != nil {
				fmt.Fprintln(os.Stderr, "[context] compaction failed:", err)
			} else {
				fmt.Fprintf(os.Stderr, "[context] input %d > budget %d: compacted %d -> %d messages\n",
					r.Usage.InputTokens, contextBudget, before, len(msgs))
			}
		}
	}
	fmt.Fprintf(os.Stderr, "stopped: hit the %d-step limit without a final answer\n", maxSteps)
	return msgs, false
}
