package main

import (
	"encoding/json"
	"fmt"
	"time"

	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"

	"github.com/yedamao/mini-agent/internal/llm"
)

// TaskQueue connects starters and workers: workers poll it for work.
const TaskQueue = "mini-agent"

// Input starts a run. It holds no secrets: everything in it is stored in
// the workflow history, which anyone with access to Temporal can read.
type Input struct {
	Task     string `json:"task"`
	Dir      string `json:"dir"`       // where tools run, on the worker's machine
	MaxSteps int    `json:"max_steps"` // default 20
}

// ToolCall is the input of the RunTool activity.
type ToolCall struct {
	Dir   string          `json:"dir"`
	Name  string          `json:"name"`
	Input json.RawMessage `json:"input"`
}

const systemPrompt = `You are a coding agent working in the current directory.
- Do only what the user asks. Change files only when the task asks for a change.
- Answer only from files and command output you actually saw; never invent names.
Finish with a short answer.`

// AgentWorkflow is the agent loop as a Temporal workflow.
//
// Workflow code must be deterministic: when a worker restarts, Temporal
// replays the history through this function to rebuild its state, so it has
// to make the same decisions again. All side effects (model calls, tools,
// the clock, randomness) happen in activities, whose results are recorded in
// the history and replayed instead of re-run.
func AgentWorkflow(ctx workflow.Context, in Input) (string, error) {
	log := workflow.GetLogger(ctx) // replay-aware: doesn't repeat logs during replay
	maxSteps := in.MaxSteps
	if maxSteps <= 0 {
		maxSteps = 20
	}

	// Model calls: slow and flaky, so a generous timeout and retries with
	// backoff. HeartbeatTimeout lets Temporal notice a dead worker in seconds
	// instead of waiting for the full StartToCloseTimeout.
	modelCtx := workflow.WithActivityOptions(ctx, workflow.ActivityOptions{
		StartToCloseTimeout: 3 * time.Minute,
		HeartbeatTimeout:    20 * time.Second,
		RetryPolicy: &temporal.RetryPolicy{
			InitialInterval:    2 * time.Second,
			BackoffCoefficient: 2,
			MaximumInterval:    time.Minute,
			MaximumAttempts:    5,
		},
	})
	// Tools: shorter timeout, few retries (write_file/run_shell aren't always
	// safe to repeat). A tool's own failure comes back as text, not an error.
	toolCtx := workflow.WithActivityOptions(ctx, workflow.ActivityOptions{
		StartToCloseTimeout: 90 * time.Second,
		HeartbeatTimeout:    20 * time.Second,
		RetryPolicy:         &temporal.RetryPolicy{InitialInterval: time.Second, MaximumAttempts: 3},
	})

	var a *Activities // nil is fine: only the method names are used here
	msgs := []llm.Message{{Role: "user", Content: in.Task}}
	for step := 1; step <= maxSteps; step++ {
		var r llm.Response
		if err := workflow.ExecuteActivity(modelCtx, a.CallModel, systemPrompt, msgs).Get(ctx, &r); err != nil {
			return "", err
		}
		msgs = append(msgs, llm.Message{Role: "assistant", Content: r.Content})

		var results []llm.Block
		var answer string
		for _, b := range r.Content {
			switch b.Type {
			case "text":
				answer += b.Text
			case "tool_use":
				var out string
				call := ToolCall{Dir: in.Dir, Name: b.Name, Input: b.Input}
				if err := workflow.ExecuteActivity(toolCtx, a.RunTool, call).Get(ctx, &out); err != nil {
					out = "error: " + err.Error() // let the model see it and adapt
				}
				log.Info("tool", "step", step, "name", b.Name, "input", preview(string(b.Input), 80), "output", preview(out, 80))
				results = append(results, llm.Block{Type: "tool_result", ToolUseID: b.ID, Content: out})
			}
		}
		if r.StopReason != "tool_use" || len(results) == 0 {
			return answer, nil
		}
		msgs = append(msgs, llm.Message{Role: "user", Content: results})
	}
	return "", temporal.NewNonRetryableApplicationError(
		fmt.Sprintf("hit the %d-step limit without a final answer", maxSteps), "StepLimit", nil)
}

func preview(s string, n int) string {
	r := []rune(s)
	if len(r) > n {
		return string(r[:n]) + "..."
	}
	return s
}
