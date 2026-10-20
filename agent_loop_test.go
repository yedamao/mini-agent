package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
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

// fakeStreamModel is fakeModel for the streaming path: each reply is an SSE body.
func fakeStreamModel(t *testing.T, replies ...string) *int32 {
	t.Helper()
	var n int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		i := int(atomic.AddInt32(&n, 1)) - 1
		if i >= len(replies) {
			t.Errorf("unexpected model call %d", i+1)
			i = len(replies) - 1
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(replies[i]))
	}))
	t.Cleanup(srv.Close)
	old := *client
	client.BaseURL, client.APIKey, client.MaxAttempts = srv.URL, "k", 1
	t.Cleanup(func() { *client = old })
	return &n
}

// sse joins SSE data payloads into a stream body.
func sse(events ...string) string {
	var s string
	for _, e := range events {
		s += "data: " + e + "\n\n"
	}
	return s
}

// The streaming loop must handle a max_tokens stop the same way: the cut-off
// tool call (here with half-written JSON input) is not run, but still gets a
// tool_result so the history stays valid for the next call.
func TestRunAgentStreamMaxTokensKeepsHistoryValid(t *testing.T) {
	quiet, discardAnswers = true, true
	t.Cleanup(func() { quiet, discardAnswers = false, false })
	n := fakeStreamModel(t,
		sse(
			`{"type":"message_start","message":{"usage":{"input_tokens":5}}}`,
			`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
			`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"writing"}}`,
			`{"type":"content_block_stop","index":0}`,
			`{"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"t1","name":"write_file","input":{}}}`,
			`{"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{\"path\":\"should-not-exist.txt\",\"content\":\"hal"}}`,
			`{"type":"message_delta","delta":{"stop_reason":"max_tokens"},"usage":{"output_tokens":9}}`,
			`{"type":"message_stop"}`,
		),
		sse(
			`{"type":"message_start","message":{"usage":{"input_tokens":5}}}`,
			`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
			`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"done"}}`,
			`{"type":"content_block_stop","index":0}`,
			`{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":1}}`,
			`{"type":"message_stop"}`,
		),
	)
	t.Chdir(t.TempDir())
	var tools []string
	msgs, err := runAgentStream(context.Background(), []Message{{Role: "user", Content: "write"}}, "write",
		agentHooks{OnTool: func(_ int, name, _, out string) { tools = append(tools, name+": "+out) }})
	if err != nil {
		t.Fatal(err)
	}
	checkPaired(t, msgs)
	if got := atomic.LoadInt32(n); got != 2 {
		t.Fatalf("model calls = %d, want 2 (loop should continue after max_tokens tool call)", got)
	}
	if got := blocksOf(msgs[2])[0].Content; got != truncatedToolMsg {
		t.Fatalf("truncated tool call should not run; got result %q", got)
	}
	if _, err := os.Stat("should-not-exist.txt"); err == nil {
		t.Fatal("truncated write_file call was executed")
	}
	if len(tools) != 1 {
		t.Fatalf("OnTool calls = %v, want 1", tools)
	}
}
