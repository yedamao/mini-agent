// mini-agent: an agent loop with file and shell tools (tools.go) and
// context management (context.go), no frameworks.
// Run: go run . "what time is it?"   or   go run . -i   for an interactive session.
package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

// Works with Anthropic or any Anthropic-compatible API (e.g. DeepSeek).
var (
	baseURL = env("AGENT_BASE_URL", "https://api.deepseek.com/anthropic")
	model   = env("AGENT_MODEL", "deepseek-chat")
	apiKey  = env("AGENT_API_KEY", os.Getenv("DEEPSEEK_API_KEY"))
)

// systemPrompt tells the model how to behave as a coding agent.
const systemPrompt = `You are a coding agent working in the current directory.
- Do only what the user asks. Change files only when the task asks for a change;
  for questions, explanations or reviews, read and report, never edit.
- To fix code: inspect files, run the tests, make the smallest fix with write_file,
  then re-run the tests until they pass.
- Answer only from files and command output you actually saw. Never invent file
  names, functions or behaviour; if you didn't check something, say so.
- Old tool results may be stubbed and history summarized; the summary lists files
  you already read. Re-read a file only if you need its exact contents.
Finish with a short answer or summary of what you changed.`

// maxSteps caps the loop so a confused model can't run forever.
const maxSteps = 20

// errStepLimit is returned when the loop hits maxSteps without a final answer.
var errStepLimit = errors.New("step limit reached")

var httpClient = &http.Client{Timeout: 3 * time.Minute}

func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

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
	// input_tokens, so the real prompt size is the sum (see promptTokens).
	CacheRead     int `json:"cache_read_input_tokens"`
	CacheCreation int `json:"cache_creation_input_tokens"`
}

// promptTokens is the full size of the prompt we sent, cached or not.
func (u Usage) promptTokens() int { return u.InputTokens + u.CacheRead + u.CacheCreation }

// lastPrompt is the prompt size of the most recent call; it decides compaction.
var lastPrompt int

// sinceCompact counts steps since the last compaction (starts high so the first can happen).
var sinceCompact = minGap

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
	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("encode request: %w", err)
	}
	hreq, err := http.NewRequest("POST", baseURL+"/v1/messages", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	hreq.Header.Set("x-api-key", apiKey)
	hreq.Header.Set("anthropic-version", "2023-06-01")
	hreq.Header.Set("content-type", "application/json")
	res, err := httpClient.Do(hreq)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	raw, err := io.ReadAll(res.Body)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}
	if res.StatusCode != http.StatusOK {
		// Error bodies may be HTML or huge; show a short preview only.
		return nil, fmt.Errorf("api %d: %s", res.StatusCode, preview(string(raw), 300))
	}
	var r Response
	if err := json.Unmarshal(raw, &r); err != nil {
		return nil, fmt.Errorf("decode response: %w (body: %s)", err, preview(string(raw), 200))
	}
	if len(r.Content) == 0 {
		return nil, fmt.Errorf("empty response (stop_reason %q)", r.StopReason)
	}
	return &r, nil
}

func main() {
	if apiKey == "" {
		fmt.Fprintln(os.Stderr, "no API key: set AGENT_API_KEY or DEEPSEEK_API_KEY")
		os.Exit(1)
	}
	args := os.Args[1:]
	if len(args) > 0 && args[0] == "-i" {
		repl()
		return
	}
	prompt := "What time is it right now?"
	if len(args) > 0 {
		prompt = strings.Join(args, " ")
	}
	_, err := runAgent([]Message{{Role: "user", Content: prompt}}, prompt)
	if errors.Is(err, errStepLimit) {
		os.Exit(2)
	} else if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

// repl keeps one conversation going across prompts. Type "exit" or press Ctrl-D to quit.
func repl() {
	var msgs []Message
	in := bufio.NewScanner(os.Stdin)
	in.Buffer(make([]byte, 1024*1024), 1024*1024) // allow long pasted prompts
	for {
		fmt.Fprint(os.Stderr, "> ")
		if !in.Scan() {
			fmt.Fprintln(os.Stderr) // EOF: end the prompt line cleanly
			return
		}
		line := strings.TrimSpace(in.Text())
		if line == "exit" {
			return
		}
		if line == "" {
			continue
		}
		msgs = addUserText(msgs, line)
		var err error
		msgs, err = runAgent(msgs, line)
		if err != nil && !errors.Is(err, errStepLimit) {
			// Keep the session alive; the history up to the failure is kept.
			fmt.Fprintln(os.Stderr, "error:", err)
		}
	}
}

// addUserText appends a user prompt. If the history already ends with a user
// message (e.g. tool results after hitting the step limit), the text is merged
// into it, because the API expects user and assistant turns to alternate.
func addUserText(msgs []Message, text string) []Message {
	if n := len(msgs); n > 0 && msgs[n-1].Role == "user" {
		msgs[n-1].Content = append(blocksOf(msgs[n-1]), Block{Type: "text", Text: text})
		return msgs
	}
	return append(msgs, Message{Role: "user", Content: text})
}

// runAgent is the agent loop: call the model, run any tools it asks for,
// send results back, and keep the context small. request is the user's
// current prompt, which compaction always keeps. It returns the updated history.
func runAgent(msgs []Message, request string) ([]Message, error) {
	for step := 1; step <= maxSteps; step++ {
		// If the last prompt was over budget, summarize the middle before calling.
		// Checking here (not after the reply) also covers new prompts in -i mode.
		sinceCompact++
		if lastPrompt > contextBudget && sinceCompact >= minGap {
			before := len(msgs)
			compacted, err := compact(msgs, request)
			if err != nil {
				fmt.Fprintln(os.Stderr, "[context] compaction failed:", err)
			} else if len(compacted) < before {
				fmt.Fprintf(os.Stderr, "[context] prompt %d > budget %d: compacted %d -> %d messages\n",
					lastPrompt, contextBudget, before, len(compacted))
				msgs = compacted
				sinceCompact = 0
			}
		}
		r, err := callModel(systemPrompt, msgs, true)
		if err != nil {
			return msgs, err
		}
		lastPrompt = r.Usage.promptTokens()
		logUsage(fmt.Sprintf("step %d", step), r.Usage)
		msgs = append(msgs, Message{Role: "assistant", Content: r.Content})
		var results []Block
		for _, b := range r.Content {
			switch b.Type {
			case "text":
				fmt.Println(b.Text)
			case "tool_use":
				out := runTool(b.Name, b.Input)
				remember(b, out)
				// Log to stderr so stdout stays just the model's answer.
				fmt.Fprintf(os.Stderr, "[step %d] %s %s -> %s\n",
					step, b.Name, preview(string(b.Input), 80), preview(out, 120))
				results = append(results, Block{Type: "tool_result", ToolUseID: b.ID, Content: out})
			}
		}
		if r.StopReason != "tool_use" || len(results) == 0 {
			return msgs, nil
		}
		msgs = append(msgs, Message{Role: "user", Content: results})

		// Cheap first: stub out old tool results.
		if n := trimOldResults(msgs); n > 0 {
			fmt.Fprintf(os.Stderr, "[context] trimmed %d old tool results\n", n)
		}
	}
	fmt.Fprintf(os.Stderr, "stopped: hit the %d-step limit without a final answer\n", maxSteps)
	return msgs, errStepLimit
}
