package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

// fakeModel replies with the scripted responses in order, one per call.
func fakeModel(t *testing.T, replies ...string) (*httptest.Server, *int32) {
	t.Helper()
	var n int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		i := int(atomic.AddInt32(&n, 1)) - 1
		if i >= len(replies) {
			t.Errorf("unexpected model call %d", i+1)
			i = len(replies) - 1
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(replies[i]))
	}))
	t.Cleanup(srv.Close)
	old := *client
	client.BaseURL, client.APIKey, client.MaxAttempts = srv.URL, "k", 1
	t.Cleanup(func() { *client = old })
	return srv, &n
}

// checkPaired fails if any assistant tool_use lacks a tool_result in the next message.
func checkPaired(t *testing.T, msgs []Message) {
	t.Helper()
	for i, m := range msgs {
		if m.Role != "assistant" {
			continue
		}
		for _, b := range blocksOf(m) {
			if b.Type != "tool_use" {
				continue
			}
			found := false
			if i+1 < len(msgs) {
				for _, r := range blocksOf(msgs[i+1]) {
					if r.Type == "tool_result" && r.ToolUseID == b.ID {
						found = true
					}
				}
			}
			if !found {
				raw, _ := json.Marshal(msgs)
				t.Fatalf("tool_use %s has no matching tool_result; history: %s", b.ID, raw)
			}
		}
	}
}

func TestRunAgentToolThenAnswer(t *testing.T) {
	quiet, discardAnswers = true, true
	t.Cleanup(func() { quiet, discardAnswers = false, false })
	fakeModel(t,
		`{"content":[{"type":"tool_use","id":"t1","name":"get_time","input":{}}],"stop_reason":"tool_use","usage":{}}`,
		`{"content":[{"type":"text","text":"done"}],"stop_reason":"end_turn","usage":{}}`,
	)
	msgs, err := runAgent(context.Background(), []Message{{Role: "user", Content: "time?"}}, "time?")
	if err != nil {
		t.Fatal(err)
	}
	checkPaired(t, msgs)
}

// A max_tokens stop with a tool_use block must still leave every tool_use
// paired with a tool_result, or the next turn is rejected by the API.
func TestRunAgentMaxTokensKeepsHistoryValid(t *testing.T) {
	quiet, discardAnswers = true, true
	t.Cleanup(func() { quiet, discardAnswers = false, false })
	fakeModel(t,
		`{"content":[{"type":"text","text":"let me check"},{"type":"tool_use","id":"t1","name":"get_time","input":{}}],"stop_reason":"max_tokens","usage":{}}`,
		`{"content":[{"type":"text","text":"done"}],"stop_reason":"end_turn","usage":{}}`,
	)
	msgs, err := runAgent(context.Background(), []Message{{Role: "user", Content: "time?"}}, "time?")
	if err != nil {
		t.Fatal(err)
	}
	checkPaired(t, msgs)
	if got := blocksOf(msgs[2])[0].Content; got != truncatedToolMsg {
		t.Fatalf("truncated tool call should not run; got result %q", got)
	}
}
