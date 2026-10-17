package llm

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const sampleSSE = `event: message_start
data: {"type":"message_start","message":{"id":"m","type":"message","role":"assistant","content":[],"usage":{"input_tokens":10,"output_tokens":0}}}

event: content_block_start
data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Hello"}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":" world"}}

event: content_block_stop
data: {"type":"content_block_stop","index":0}

event: content_block_start
data: {"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"t1","name":"get_time","input":{}}}

event: content_block_delta
data: {"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{}"}}

event: content_block_stop
data: {"type":"content_block_stop","index":1}

event: message_delta
data: {"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":5}}

event: message_stop
data: {"type":"message_stop"}

`

func TestReadSSETextAndTool(t *testing.T) {
	var texts []string
	var tools []string
	resp, err := readSSE(strings.NewReader(sampleSSE), func(ev StreamEvent) error {
		switch ev.Kind {
		case "text":
			texts = append(texts, ev.Text)
		case "tool_ready":
			tools = append(tools, ev.ToolName)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(texts, "") != "Hello world" {
		t.Errorf("texts = %v", texts)
	}
	if resp.StopReason != "tool_use" || len(tools) != 1 || tools[0] != "get_time" {
		t.Errorf("resp=%+v tools=%v", resp, tools)
	}
	if len(resp.Content) != 2 || resp.Content[0].Text != "Hello world" || resp.Content[1].Name != "get_time" {
		t.Errorf("content = %+v", resp.Content)
	}
	if resp.Usage.InputTokens != 10 || resp.Usage.OutputTokens != 5 {
		t.Errorf("usage = %+v", resp.Usage)
	}
}

func TestCallStreamAgainstServer(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		if body["stream"] != true {
			t.Errorf("stream not set: %v", body["stream"])
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.Write([]byte(sampleSSE))
	}))
	defer srv.Close()
	c := &Client{BaseURL: srv.URL, Model: "m", APIKey: "k", MaxAttempts: 1}
	var got string
	resp, err := c.CallStream(context.Background(), Request{}, func(ev StreamEvent) error {
		if ev.Kind == "text" {
			got += ev.Text
		}
		return nil
	})
	if err != nil || got != "Hello world" || resp.Content[1].Name != "get_time" {
		t.Fatalf("got %q err=%v resp=%+v", got, err, resp)
	}
}

func TestReadSSEHandlerCancel(t *testing.T) {
	_, err := readSSE(strings.NewReader(sampleSSE), func(ev StreamEvent) error {
		if ev.Kind == "text" && ev.Text == " world" {
			return context.Canceled
		}
		return nil
	})
	if err == nil || !streamStarted(err) {
		t.Fatalf("want started cancel, got %v", err)
	}
}
