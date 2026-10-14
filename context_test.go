package main

import (
	"strings"
	"testing"
)

// history builds: user task, then n rounds of assistant tool_use + user tool_result.
func history(n int) []Message {
	msgs := []Message{{Role: "user", Content: "task"}}
	for i := 0; i < n; i++ {
		id := string(rune('a' + i))
		msgs = append(msgs,
			Message{Role: "assistant", Content: []Block{{Type: "tool_use", ID: id, Name: "read_file"}}},
			Message{Role: "user", Content: []Block{{Type: "tool_result", ToolUseID: id, Content: strings.Repeat("x", 100)}}},
		)
	}
	return msgs
}

func TestCutIndexKeepsToolPairs(t *testing.T) {
	for n := 0; n < 8; n++ {
		msgs := history(n)
		cut := cutIndex(msgs)
		if cut == -1 {
			continue
		}
		if msgs[cut].Role != "assistant" {
			t.Errorf("n=%d: tail starts with %s, want assistant", n, msgs[cut].Role)
		}
		if cut <= 1 || len(msgs)-cut < keepTail-1 {
			t.Errorf("n=%d: bad cut %d of %d", n, cut, len(msgs))
		}
	}
	if got := cutIndex(history(1)); got != -1 {
		t.Errorf("short history: cut = %d, want -1", got)
	}
}

func TestTrimOldResults(t *testing.T) {
	msgs := history(5) // 11 messages
	callInfo["a"] = "read_file main.go"
	n := trimOldResults(msgs)
	if n == 0 {
		t.Fatal("nothing trimmed")
	}
	first := msgs[2].Content.([]Block)[0].Content
	if !strings.Contains(first, "read_file main.go") || !strings.Contains(first, "re-read only") {
		t.Errorf("stub = %q", first)
	}
	last := msgs[len(msgs)-1].Content.([]Block)[0].Content
	if strings.HasPrefix(last, stubPrefix) {
		t.Error("recent result was trimmed")
	}
	if trimOldResults(msgs) != 0 {
		t.Error("stubs were trimmed twice")
	}
}

func TestAddUserTextMerges(t *testing.T) {
	msgs := history(1) // ends with a user tool_result
	msgs = addUserText(msgs, "next")
	if len(msgs) != 3 || len(blocksOf(msgs[2])) != 2 {
		t.Errorf("want merged into last user message, got %d msgs", len(msgs))
	}
	msgs = append(msgs, Message{Role: "assistant", Content: "ok"})
	if msgs = addUserText(msgs, "again"); len(msgs) != 5 {
		t.Errorf("want new message, got %d msgs", len(msgs))
	}
}

func TestTruncateNKeepsUTF8(t *testing.T) {
	got := truncateN("ab你好", 4) // byte 4 is inside 你
	if !strings.HasPrefix(got, "ab\n") {
		t.Errorf("got %q", got)
	}
}
