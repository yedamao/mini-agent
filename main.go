// mini-agent: an agent loop with file and shell tools (tools.go) and
// context management (context.go), no frameworks.
// Run: go run . "what time is it?"   or   go run . -i   for an interactive session.
// Model calls have a per-call timeout and retries (internal/llm); Ctrl-C cancels.
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/yedamao/mini-agent/internal/llm"
)

// Works with Anthropic or any Anthropic-compatible API (e.g. DeepSeek).
var (
	baseURL = env("AGENT_BASE_URL", "https://api.deepseek.com/anthropic")
	model   = env("AGENT_MODEL", "deepseek-chat")
	apiKey  = env("AGENT_API_KEY", os.Getenv("DEEPSEEK_API_KEY"))
)

// benchLaunchStart is set when AGENT_BENCH_LAUNCH=1 to time first TUI frame.
var benchLaunchStart time.Time

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

func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

// The API types live in internal/llm (shared with the Temporal worker).
type (
	Message  = llm.Message
	Block    = llm.Block
	Usage    = llm.Usage
	Response = llm.Response
)

// client calls the model with a per-call timeout and retries (Day 10).
var client = &llm.Client{
	BaseURL:     baseURL,
	Model:       model,
	APIKey:      apiKey,
	CallTimeout: time.Duration(envInt("AGENT_CALL_TIMEOUT", 120)) * time.Second,
	MaxAttempts: envInt("AGENT_MAX_ATTEMPTS", 5),
	Logf: func(f string, a ...any) {
		if !quiet {
			fmt.Fprintf(os.Stderr, f+"\n", a...)
		}
	},
}

// lastPrompt is the prompt size of the most recent call; it decides compaction.
var lastPrompt int

// sinceCompact counts steps since the last compaction (starts high so the first can happen).
var sinceCompact = minGap

// callModel sends one request. withTools=false is used for summarization.
func callModel(ctx context.Context, system string, msgs []Message, withTools bool) (*Response, error) {
	req := llm.Request{System: system, Messages: msgs}
	if withTools {
		req.Tools = toolDefs
	}
	return client.Call(ctx, req)
}

func main() {
	// Ctrl-C (or SIGTERM) cancels ctx: the in-flight model call or shell
	// command is aborted, retries stop, and we exit cleanly.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, os.Args[1:]); err != nil {
		if errors.Is(err, errHelp) {
			os.Exit(0)
		}
		fmt.Fprintln(os.Stderr, err)
		os.Exit(exitCode(err))
	}
}

// run is the CLI entrypoint (testable). Subcommands that need no API key
// return before the key is checked.
func run(ctx context.Context, args []string) error {
	opts, err := parseRoot(args)
	if err != nil && !errors.Is(err, errHelp) {
		return err
	}
	// -i -h documents slash commands; plain -h shows root usage.
	if opts.Help || errors.Is(err, errHelp) {
		if opts.Interactive {
			fmt.Fprint(os.Stdout, interactiveHelp)
		} else {
			fmt.Fprint(os.Stdout, rootUsage)
		}
		return errHelp
	}
	if opts.Version {
		fmt.Printf("mini-agent %s (%s %s/%s)\n", version, runtime.Version(), runtime.GOOS, runtime.GOARCH)
		return nil
	}
	if opts.Dir != "" {
		if err := os.Chdir(opts.Dir); err != nil {
			return errExit{Msg: "-C: " + err.Error()}
		}
	}
	quiet = opts.Quiet
	args = opts.Args

	// No prompt and no subcommand → interactive mode (same as -i).
	// Explicit -h/--help / "help" still show usage above.
	if len(args) == 0 {
		opts.Interactive = true
	}

	if len(args) > 0 {
		switch args[0] {
		case "mcp", "m":
			err := mcpCommand(ctx, args[1:], os.Stdout)
			if errors.Is(err, errHelp) {
				return errHelp
			}
			return err
		case "skill", "skills", "s":
			err := skillCommand(ctx, args[1:], os.Stdout)
			if errors.Is(err, errHelp) {
				return errHelp
			}
			return err
		case "doctor":
			return doctor(os.Stdout)
		case "version":
			fmt.Printf("mini-agent %s (%s %s/%s)\n", version, runtime.Version(), runtime.GOOS, runtime.GOARCH)
			return nil
		case "help":
			fmt.Fprint(os.Stdout, rootUsage)
			return errHelp
		}
	}

	// Re-read the key at run time so tests (and a late export) take effect.
	apiKey = env("AGENT_API_KEY", os.Getenv("DEEPSEEK_API_KEY"))
	client.APIKey = apiKey
	if apiKey == "" {
		return errExit{Msg: "no API key: set AGENT_API_KEY or DEEPSEEK_API_KEY\nSee -h for usage."}
	}
	servers, err := mcpServers()
	if err != nil {
		return fmt.Errorf("mcp config: %w", err)
	}
	// Skills are local (sub-ms). Suppress the banner when we're about to take
	// over the screen so stderr doesn't flash before the TUI.
	if opts.Interactive {
		savedQuiet := quiet
		quiet = true
		setupSkills()
		quiet = savedQuiet
	} else {
		setupSkills()
	}
	defer stopMCP()

	if opts.Interactive {
		// Paint the TUI first; MCP connects in the background (see tui Init).
		// One-shot mode still waits for MCP below so tools exist before the call.
		if os.Getenv("AGENT_BENCH_LAUNCH") == "1" {
			benchLaunchStart = time.Now()
		}
		mcpCtx, cancel := context.WithCancel(context.Background())
		defer cancel() // cancel before stopMCP (deferred earlier) so connect aborts
		repl(mcpCtx, servers)
		return nil
	}
	startMCP(ctx, servers)
	prompt := strings.Join(args, " ")
	_, err = runAgent(ctx, []Message{{Role: "user", Content: prompt}}, prompt)
	if err != nil {
		code := 1
		if errors.Is(err, errStepLimit) {
			code = 2
		} else if ctx.Err() != nil {
			fmt.Fprintln(os.Stderr, "interrupted")
			code = 130
		} else {
			return err
		}
		stopMCP()
		os.Exit(code)
	}
	return nil
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
func runAgent(ctx context.Context, msgs []Message, request string) ([]Message, error) {
	for step := 1; step <= maxSteps; step++ {
		// If the last prompt was over budget, summarize the middle before calling.
		// Checking here (not after the reply) also covers new prompts in -i mode.
		sinceCompact++
		if lastPrompt > contextBudget && sinceCompact >= minGap {
			before := len(msgs)
			compacted, err := compact(ctx, msgs, request)
			if err != nil {
				if !quiet {
					fmt.Fprintln(os.Stderr, "[context] compaction failed:", err)
				}
			} else if len(compacted) < before {
				if !quiet {
					fmt.Fprintf(os.Stderr, "[context] prompt %d > budget %d: compacted %d -> %d messages\n",
						lastPrompt, contextBudget, before, len(compacted))
				}
				msgs = compacted
				sinceCompact = 0
			}
		}
		spin := startThinking()
		r, err := callModel(ctx, systemPrompt+skillPrompt, msgs, true)
		spin.Stop()
		if err != nil {
			return msgs, err
		}
		lastPrompt = r.Usage.PromptTokens()
		logUsage(fmt.Sprintf("step %d", step), r.Usage)
		msgs = append(msgs, Message{Role: "assistant", Content: r.Content})
		var results []Block
		for _, b := range r.Content {
			switch b.Type {
			case "text":
				fmt.Println(b.Text)
			case "tool_use":
				out := runTool(ctx, b.Name, b.Input)
				remember(b, out)
				// Log to stderr so stdout stays just the model's answer.
				if !quiet {
					if interactiveMode {
						fmt.Fprintf(os.Stderr, "%s [step %d] %s %s → %s\n",
							color("tool", "⚙"), step, b.Name, preview(string(b.Input), 60), preview(out, 80))
					} else {
						fmt.Fprintf(os.Stderr, "[step %d] %s %s -> %s\n",
							step, b.Name, preview(string(b.Input), 80), preview(out, 120))
					}
				}
				results = append(results, Block{Type: "tool_result", ToolUseID: b.ID, Content: out})
			}
		}
		if r.StopReason != "tool_use" || len(results) == 0 {
			return msgs, nil
		}
		msgs = append(msgs, Message{Role: "user", Content: results})

		// Cheap first: stub out old tool results.
		if n := trimOldResults(msgs); n > 0 && !quiet {
			fmt.Fprintf(os.Stderr, "[context] trimmed %d old tool results\n", n)
		}
	}
	fmt.Fprintf(os.Stderr, "stopped: hit the %d-step limit without a final answer\n", maxSteps)
	return msgs, errStepLimit
}
