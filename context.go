package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
)

// Context management (Day 6-7). Two techniques, cheapest first:
//  1. trimOldResults: replace old tool_result contents with short stubs.
//  2. compact: when the prompt (including cached tokens) gets too big, ask the model to summarize the
//     middle of the conversation, keeping the first prompt, the current
//     request and the last few turns.
// A record of files already read is kept on the side so neither step makes
// the model forget what it has seen.

// contextBudget is the input-token size that triggers compaction.
var contextBudget = envInt("AGENT_CONTEXT_BUDGET", 50000)

const (
	stubAfter = 6 // tool results older than this many messages become stubs
	keepTail  = 4 // messages kept verbatim at the end when compacting
	// minGap is the fewest steps between two compactions. If the budget is
	// smaller than the working set (e.g. one big file), compacting every step
	// would just throw away what the model read a moment ago.
	minGap = 3
)

const summaryPrompt = `Summarize this agent conversation so you can continue the task without
re-reading anything. Write:
- Goal: the user's requests.
- Per file read: 3-6 bullets with the concrete facts (types, functions, constants
  and their values, what each part does). Keep facts from the earlier summary.
- Changes made and commands run, with results.
- Next: what is left to do.
Only state facts present in the transcript. Under 500 words.`

func envInt(k string, def int) int {
	if n, err := strconv.Atoi(os.Getenv(k)); err == nil && n > 0 {
		return n
	}
	return def
}

// Side records, filled in as tools run (see remember).
var (
	callInfo  = map[string]string{} // tool_use id -> short description, e.g. "read_file main.go"
	fullOut   = map[string]string{} // tool_use id -> untrimmed output, for the summarizer
	filesRead = map[string]string{} // path -> short digest
)

// remember records a tool call so stubs and summaries can describe it later.
func remember(b Block, out string) {
	var in toolInput
	json.Unmarshal(b.Input, &in) // best effort; bad input already failed in runTool
	desc := b.Name
	switch b.Name {
	case "read_file":
		desc += " " + in.Path
		if !strings.HasPrefix(out, "error:") {
			filesRead[in.Path] = digest(out)
		}
	case "write_file":
		desc += " " + in.Path
	case "run_shell":
		desc += " " + preview(in.Command, 60)
	}
	callInfo[b.ID] = desc
	fullOut[b.ID] = out
}

// digest summarizes file contents in one line: size, line count, first line.
func digest(s string) string {
	first, _, _ := strings.Cut(strings.TrimSpace(s), "\n")
	return fmt.Sprintf("%d lines, %d bytes, starts %q", strings.Count(s, "\n")+1, len(s), preview(first, 50))
}

// filesReadList renders filesRead as a sorted bullet list.
func filesReadList() string {
	paths := make([]string, 0, len(filesRead))
	for p := range filesRead {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	var sb strings.Builder
	for _, p := range paths {
		fmt.Fprintf(&sb, "- %s (%s)\n", p, filesRead[p])
	}
	return sb.String()
}

const stubPrefix = "[old result trimmed"

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
			if b.Type != "tool_result" || strings.HasPrefix(b.Content, stubPrefix) {
				continue
			}
			desc := callInfo[b.ToolUseID]
			if desc == "" {
				desc = "tool call"
			}
			b.Content = fmt.Sprintf("%s: %s, %d bytes, starting %q. You already saw this; re-read only if you need exact contents.]",
				stubPrefix, desc, len(b.Content), preview(b.Content, 60))
			trimmed++
		}
	}
	return trimmed
}

// cutIndex picks where the kept tail starts. It is always an assistant message,
// so a tool_use (assistant) and its tool_result (the next user message) stay
// together. It returns -1 if the history is too short to compact.
func cutIndex(msgs []Message) int {
	for i := len(msgs) - keepTail; i > 1; i-- {
		if msgs[i].Role == "assistant" {
			return i
		}
	}
	return -1
}

// compact replaces messages[1:cut] with a model-written summary.
func compact(ctx context.Context, msgs []Message, request string) ([]Message, error) {
	cut := cutIndex(msgs)
	if cut < 0 {
		return msgs, nil
	}

	// The first message holds the first prompt, plus the previous summary if we
	// already compacted once. Carry that summary forward so nothing is forgotten.
	first := blocksOf(msgs[0])
	task, prevSummary := "", ""
	for _, b := range first {
		switch {
		case b.Type != "text":
		case strings.HasPrefix(b.Text, summaryHeader):
			prevSummary = strings.TrimPrefix(b.Text, summaryHeader)
		case strings.HasPrefix(b.Text, requestHeader):
		default:
			if task == "" {
				task = b.Text
			}
		}
	}
	transcript := "user (task): " + task + "\n"
	if request != "" && request != task {
		transcript += "user (current request): " + request + "\n"
	}
	transcript += render(msgs[1:cut])
	if prevSummary != "" {
		transcript = "Earlier summary:\n" + prevSummary + "\n\nThen:\n" + transcript
	}
	// Wrap the transcript so the model summarizes it instead of continuing it.
	ask := "<transcript>\n" + transcript + "\n</transcript>\n\nWrite the summary of the transcript above now. " +
		"Do not continue the conversation, do not call tools, do not add anything not in the transcript."
	r, err := callModel(ctx, summaryPrompt, []Message{{Role: "user", Content: []Block{{Type: "text", Text: ask}}}}, false)
	if err != nil {
		return msgs, err
	}
	logUsage("summary", r.Usage)
	summary := textOf(r.Content)
	if !quiet {
		fmt.Fprintf(os.Stderr, "[context] summary: %s\n", preview(summary, 150))
	}
	if list := filesReadList(); list != "" {
		summary += "\n\nFiles already read (re-read only if you need exact contents):\n" + list
	}
	summary += loadedSkillsNote()

	// New history: first prompt + summary (+ the current request, if different),
	// then the recent tail.
	head := []Block{{Type: "text", Text: task}, {Type: "text", Text: summaryHeader + summary}}
	if request != "" && request != task {
		head = append(head, Block{Type: "text", Text: requestHeader + request})
	}
	return append([]Message{{Role: "user", Content: head}}, msgs[cut:]...), nil
}

const (
	summaryHeader = "[Summary of earlier conversation]\n"
	requestHeader = "[Current request]\n"
)

// render turns messages into readable text for the summarizer, using the
// untrimmed tool output where we still have it.
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
				out := b.Content
				if full, ok := fullOut[b.ToolUseID]; ok {
					out = full
				}
				fmt.Fprintf(&sb, "tool result: %s\n", truncateN(out, 3000))
			}
		}
	}
	return sb.String()
}

// blocksOf returns a message's content as blocks, whatever form it is stored in.
func blocksOf(m Message) []Block {
	switch c := m.Content.(type) {
	case []Block:
		return c
	case string:
		return []Block{{Type: "text", Text: c}}
	}
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

// Running token totals for the whole session.
var totalIn, totalOut int

func logUsage(label string, u Usage) {
	totalIn += u.PromptTokens()
	totalOut += u.OutputTokens
	if quiet {
		return
	}
	fmt.Fprintf(os.Stderr, "[%s] tokens prompt=%d (cached %d) out=%d | total in=%d out=%d\n",
		label, u.PromptTokens(), u.CacheRead, u.OutputTokens, totalIn, totalOut)
}
