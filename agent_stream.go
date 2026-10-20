package main

import (
	"context"
	"fmt"
	"os"

	"github.com/yedamao/mini-agent/internal/llm"
)

// agentHooks lets the TUI (or tests) observe a streaming agent turn.
type agentHooks struct {
	OnText     func(text string)                    // model text delta
	OnTool     func(step int, name, in, out string) // after a tool finishes
	OnThinking func()                               // about to call the model
	OnStep     func(step int)                       // step number started
}

// runAgentStream is runAgent with CallStream so text can paint live.
func runAgentStream(ctx context.Context, msgs []Message, request string, h agentHooks) ([]Message, error) {
	for step := 1; step <= maxSteps; step++ {
		sinceCompact++
		if lastPrompt > contextBudget && sinceCompact >= minGap {
			before := len(msgs)
			compacted, err := compact(ctx, msgs, request)
			if err != nil {
				if !quiet {
					fmt.Fprintln(os.Stderr, "[context] compaction failed:", err)
				}
			} else if len(compacted) < before {
				if !quiet && !interactiveMode {
					fmt.Fprintf(os.Stderr, "[context] prompt %d > budget %d: compacted %d -> %d messages\n",
						lastPrompt, contextBudget, before, len(compacted))
				}
				msgs = compacted
				sinceCompact = 0
			}
		}
		if h.OnStep != nil {
			h.OnStep(step)
		}
		if h.OnThinking != nil {
			h.OnThinking()
		}
		req := llm.Request{System: systemPrompt + skillPrompt, Messages: msgs, Tools: toolDefs}
		r, err := client.CallStream(ctx, req, func(ev llm.StreamEvent) error {
			if ev.Kind == "text" && h.OnText != nil && ev.Text != "" {
				h.OnText(ev.Text)
			}
			return ctx.Err()
		})
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
				// Already streamed via OnText; still print in non-TUI interactive/plain modes.
				if !isTUI && !discardAnswers {
					fmt.Print(b.Text)
					if b.Text != "" && b.Text[len(b.Text)-1] != '\n' {
						fmt.Println()
					}
				}
			case "tool_use":
				out := truncatedToolMsg
				if r.StopReason != "max_tokens" {
					out = runTool(ctx, b.Name, b.Input)
				}
				remember(b, out)
				if h.OnTool != nil {
					h.OnTool(step, b.Name, preview(string(b.Input), 80), preview(out, 120))
				} else if !quiet {
					fmt.Fprintf(os.Stderr, "[step %d] %s %s -> %s\n",
						step, b.Name, preview(string(b.Input), 80), preview(out, 120))
				}
				results = append(results, Block{Type: "tool_result", ToolUseID: b.ID, Content: out})
			}
		}
		// Every tool_use needs a tool_result in the next message, whatever the
		// stop reason, or the next call is rejected. So any tool call continues the loop.
		if len(results) == 0 {
			return msgs, nil
		}
		msgs = append(msgs, Message{Role: "user", Content: results})
		if n := trimOldResults(msgs); n > 0 && !quiet && !isTUI {
			fmt.Fprintf(os.Stderr, "[context] trimmed %d old tool results\n", n)
		}
	}
	if !quiet && !isTUI {
		fmt.Fprintf(os.Stderr, "stopped: hit the %d-step limit without a final answer\n", maxSteps)
	}
	return msgs, errStepLimit
}
