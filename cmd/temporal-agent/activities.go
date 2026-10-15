package main

import (
	"context"
	"errors"
	"time"

	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/temporal"

	"github.com/yedamao/mini-agent/internal/llm"
	"github.com/yedamao/mini-agent/internal/tools"
)

// Activities hold the side effects. The API key lives in Client, read from
// the worker's environment, so it never enters workflow inputs or history.
type Activities struct {
	Client *llm.Client
}

// CallModel makes one model call. Temporal owns retries here (the client is
// set to a single attempt), so each try shows up in the UI. We translate
// errors so Temporal retries only what's worth retrying.
func (a *Activities) CallModel(ctx context.Context, system string, msgs []llm.Message) (*llm.Response, error) {
	defer heartbeat(ctx)()
	r, err := a.Client.Call(ctx, llm.Request{System: system, Messages: msgs, Tools: tools.Defs})
	if err == nil {
		activity.GetLogger(ctx).Info("model", "attempt", activity.GetInfo(ctx).Attempt,
			"prompt_tokens", r.Usage.PromptTokens(), "out_tokens", r.Usage.OutputTokens, "stop", r.StopReason)
		return r, nil
	}
	var apiErr *llm.APIError
	switch {
	case ctx.Err() != nil:
		return nil, err // cancelled or timed out: Temporal decides
	case errors.As(err, &apiErr) && apiErr.Retryable() && apiErr.RetryAfter > 0:
		// Honor Retry-After by telling Temporal when to try next.
		return nil, temporal.NewApplicationErrorWithOptions(err.Error(), "APIError",
			temporal.ApplicationErrorOptions{NextRetryDelay: apiErr.RetryAfter})
	case !llm.IsRetryable(err):
		// 4xx (bad key, bad request) or a malformed response: retrying won't help.
		return nil, temporal.NewNonRetryableApplicationError(err.Error(), "PermanentError", err)
	}
	return nil, err // 429/5xx/network: retried by the activity's RetryPolicy
}

// RunTool runs one built-in tool in the given directory.
func (a *Activities) RunTool(ctx context.Context, call ToolCall) (string, error) {
	defer heartbeat(ctx)()
	return tools.Run(ctx, call.Dir, call.Name, call.Input), nil
}

// heartbeat reports liveness every few seconds until the returned func is
// called. If the worker dies, heartbeats stop and Temporal retries the
// activity on another worker after HeartbeatTimeout.
func heartbeat(ctx context.Context) (stop func()) {
	done := make(chan struct{})
	go func() {
		t := time.NewTicker(5 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-t.C:
				activity.RecordHeartbeat(ctx)
			case <-done:
				return
			case <-ctx.Done():
				return
			}
		}
	}()
	return func() { close(done) }
}
