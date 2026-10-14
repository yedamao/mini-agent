package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
)

// Context management (Day 6-7). Two techniques, cheapest first:
//  1. trimOldResults: replace old tool_result contents with short stubs.
//  2. compact: when the prompt gets too big, ask the model to summarize the
//     middle of the conversation, keeping the first task and the last few turns.

// contextBudget is the input-token size that triggers compaction.
var contextBudget = envInt("AGENT_CONTEXT_BUDGET", 50000)

const (
	stubAfter = 6 // tool results older than this many messages become stubs
	keepTail  = 4 // messages kept verbatim at the end when compacting
)

const summaryPrompt = `Summarize this agent conversation for your own later use.
Keep: the user's goals, files read and key facts learned from them, changes made,
commands run and their results, and what is left to do.
List every file already read so it is not read again. Be concise (under 300 words).`

func envInt(k string, def int) int {
	if n, err := strconv.Atoi(os.Getenv(k)); err == nil && n > 0 {
		return n
	}
	return def
}

// trimOldResults stubs out tool_result contents in all but the last stubAfter messages.
// The tool_result blocks stay in place, so every tool_use still has its matching result.
func trimOldResults(msgs []Message) int {
	trimmed := 0
	for i := 0; i < len(msgs)-stubAfter; i++ {
		blocks, ok := msgs[i].Content.([]Block)
		if !ok {
			continue
		}
		for j := range blocks {
			b := &blocks[j]
			if b.Type == "tool_result" && !strings.HasPrefix(b.Content, "[trimmed") {
				b.Content = fmt.Sprintf("[trimmed old result: %d bytes, starting %q]", len(b.Content), preview(b.Content, 60))
				trimmed++
			}
		}
	}
	return trimmed
}

// compact replaces messages[1:cut] with a model-written summary.
// cut is always an assistant message, so a tool_use (assistant) and its
// tool_result (the following user message) are never separated.
func compact(msgs []Message) ([]Message, error) {
	cut := -1
	for i := len(msgs) - keepTail; i > 1; i-- {
		if msgs[i].Role == "assistant" {
			cut = i
			break
		}
	}
	if cut < 0 {
		return msgs, nil // too short to compact
	}

	// Summarize the middle as plain text (no tools in this call).
	// The first message holds the original task, plus the previous summary if we
	// already compacted once. Carry that summary forward so nothing is forgotten.
	firstBlocks := blocksOf(msgs[0])
	task := firstBlocks[0].Text
	transcript := render(msgs[1:cut])
	if len(firstBlocks) > 1 {
		transcript = "Earlier summary:\n" + firstBlocks[1].Text + "\n\nThen:\n" + transcript
	}
	r, err := callModel(summaryPrompt, []Message{{Role: "user", Content: []Block{{Type: "text", Text: transcript}}}}, false)
	if err != nil {
		return msgs, err
	}
	summary := textOf(r.Content)
	logUsage("summary", r.Usage)

	// New history: original task + summary (one user message), then the recent tail.
	first := []Block{
		{Type: "text", Text: task},
		{Type: "text", Text: "[Summary of earlier conversation]\n" + summary},
	}
	out := append([]Message{{Role: "user", Content: first}}, msgs[cut:]...)
	return out, nil
}

// render turns messages into readable text for the summarizer.
func render(msgs []Message) string {
	var sb strings.Builder
	for _, m := range msgs {
		for _, b := range blocksOf(m) {
			switch b.Type {
			case "text":
				fmt.Fprintf(&sb, "%s: %s\n", m.Role, b.Text)
			case "tool_use":
				fmt.Fprintf(&sb, "tool call %s %s\n", b.Name, string(b.Input))
			case "tool_result":
				fmt.Fprintf(&sb, "tool result: %s\n", truncateN(b.Content, 2000))
			}
		}
	}
	return sb.String()
}

func blocksOf(m Message) []Block {
	switch c := m.Content.(type) {
	case []Block:
		return c
	case string:
		return []Block{{Type: "text", Text: c}}
	}
	// Fallback: round-trip through JSON.
	raw, _ := json.Marshal(m.Content)
	var bs []Block
	json.Unmarshal(raw, &bs)
	return bs
}

func textOf(bs []Block) string {
	var parts []string
	for _, b := range bs {
		if b.Type == "text" {
			parts = append(parts, b.Text)
		}
	}
	return strings.Join(parts, "\n")
}

func truncateN(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "...[cut]"
}

// Running token totals for the whole session.
var totalIn, totalOut int

func logUsage(label string, u Usage) {
	totalIn += u.InputTokens
	totalOut += u.OutputTokens
	fmt.Fprintf(os.Stderr, "[%s] tokens in=%d out=%d | total in=%d out=%d\n",
		label, u.InputTokens, u.OutputTokens, totalIn, totalOut)
}
