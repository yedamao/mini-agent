# Context compaction sample run (Day 6-7)

    AGENT_CONTEXT_BUDGET=3000 go run . "read each .go file in this repo (including examples/buggy) with read_file, making only ONE tool call per turn, then explain the architecture"

Step logs go to stderr and the answer to stdout; both are shown here. `[context]` lines show stubbing and compaction.

````text
[step 1] tokens prompt=652 (cached 512) out=80 | total in=652 out=80
I'll read each `.go` file in the repo, one tool call per turn. Let me start by finding the files.
[step 1] run_shell {"command":"find . -name '*.go' -not -path './vendor/*' | sort"} -> ./context.go ./context_test.go ./examples/buggy/words.go ./examples/buggy/words_test.go ./main.go ./tools.go 
[step 2] tokens prompt=780 (cached 640) out=39 | total in=1432 out=119
[step 2] read_file {"path":"./context.go"} -> package main  import ( 	"encoding/json" 	"fmt" 	"os" 	"sort" 	"strconv" 	"strings" )  // Context management (Day 6-7). T...
[step 3] tokens prompt=3099 (cached 768) out=40 | total in=4531 out=159
[step 3] read_file {"path":"./context_test.go"} -> package main  import ( 	"strings" 	"testing" )  // history builds: user task, then n rounds of assistant tool_use + user...
[summary] tokens prompt=270 (cached 0) out=146 | total in=4801 out=305
[context] summary: Goal: read each `.go` file in the repo (including `examples/buggy`) with `read_file`, making only ONE tool call per turn, then explain the architectur...
[context] prompt 3099 > budget 3000: compacted 7 -> 5 messages
[step 4] tokens prompt=4000 (cached 640) out=44 | total in=8801 out=349
[step 4] read_file {"path":"./examples/buggy/words.go"} -> // Package buggy has a small, deliberate bug for the agent to fix (Day 5). package buggy  import "strings"  // ReverseWo...
[step 5] tokens prompt=4180 (cached 3968) out=45 | total in=12981 out=394
[step 5] read_file {"path":"./examples/buggy/words_test.go"} -> package buggy  import "testing"  func TestReverseWords(t *testing.T) { 	tests := []struct { 		in, want string 	}{ 		{"",...
[context] trimmed 1 old tool results
[step 6] tokens prompt=2167 (cached 768) out=39 | total in=15148 out=433
[step 6] read_file {"path":"./main.go"} -> // mini-agent: an agent loop with file and shell tools (tools.go) and // context management (context.go), no frameworks....
[context] trimmed 1 old tool results
[step 7] tokens prompt=3920 (cached 768) out=39 | total in=19068 out=472
[step 7] read_file {"path":"./tools.go"} -> package main  import ( 	"context" 	"encoding/json" 	"os" 	"os/exec" 	"path/filepath" 	"strings" 	"time" 	"unicode/utf8" ...
[context] trimmed 1 old tool results
[summary] tokens prompt=2309 (cached 0) out=1054 | total in=21377 out=1526
[context] summary: Goal: read each `.go` file in the repo (including `examples/buggy`) with `read_file`, making only ONE tool call per turn, then explain the architectur...
[context] prompt 3920 > budget 3000: compacted 13 -> 5 messages
[step 8] tokens prompt=5577 (cached 640) out=2309 | total in=26954 out=3835
I've now read all six `.go` files (`main.go`, `tools.go`, `context.go`, `context_test.go`, `examples/buggy/words.go`, `examples/buggy/words_test.go`). No files were changed. Here's the architecture.

## Overview

A framework-free "mini-agent" (`package main`) that talks to an Anthropic-compatible Messages API (`baseURL` defaults to DeepSeek's Anthropic endpoint, `model` to `deepseek-chat`; both overridable by env). Three layers, each in its own file:

| File | Layer |
|---|---|
| `main.go` | API client + agent loop + CLI |
| `tools.go` | Tool definitions, dispatch, output limits |
| `context.go` | Context-size management (trimming + compaction) |

## `main.go` — client, loop, entry point

**Config & types.** `baseURL`/`model`/`apiKey` come from `env(...)` with defaults (`AGENT_API_KEY` falls back to `DEEPSEEK_API_KEY`). The wire types mirror the Messages API: `Message{Role, Content any}` (content is either a plain string or `[]Block`), `Block` (a union of text / tool_use / tool_result fields), and `Usage` + `Response`. `Block` is the single struct carrying all block types, distinguished by `Type`.

**Token accounting.** `Usage.promptTokens()` sums `InputTokens + CacheRead + CacheCreation`, because cache hits are reported separately and not counted in `input_tokens`. The package-level `lastPrompt` records the most recent prompt size and drives compaction; `sinceCompact` counts steps since the last compaction and starts at `minGap` (from `context.go`) so the first one is allowed immediately.

**`callModel(system, msgs, withTools)`** marshals a request `map[string]any`, `POST`s to `baseURL + "/v1/messages"` with `x-api-key` and `anthropic-version: 2023-06-01` headers, and decodes `[]Block`. Non-200 bodies and decode failures are surfaced only as a `preview(..., 300/200)` slice, so HTML or huge error bodies don't blow up the message. An empty content list is also an error. `withTools=false` is the summarization path (no `tools` in the request), used by `compact`.

**Entry point.** `main` requires an API key, then either enters `repl()` (if `args[0] == "-i"`) or runs one shot with `strings.Join(args, " ")` as the prompt (default `"What time is it right now?"`). It exits `2` on `errStepLimit`, `1` on other errors.

**`repl()`** holds one `[]Message` across prompts, reading lines on a 1 MiB scanner buffer (long pastes), quitting on `exit`/EOF, skipping blanks. Each line goes through `addUserText`, then `runAgent`; non-step-limit errors are printed but the session continues with history intact.

**`addUserText`** enforces the API's alternating-turn rule: if the history already ends with a `user` message (e.g. dangling tool results after a step-limit stop), the new text is appended as an extra `text` block to that message instead of a new message.

**`runAgent(msgs, request)` — the loop** (capped at `maxSteps = 20`, returning `errStepLimit`):
1. Increment `sinceCompact`. If `lastPrompt > contextBudget` and `sinceCompact >= minGap`, call `compact(msgs, request)`; on success and only if it actually shortened the history, log it and reset the counter. This check is deliberately at the *top* of the loop so a fresh prompt in `-i` mode is covered before the next call.
2. `callModel(systemPrompt, msgs, true)`, record `lastPrompt` and log usage, append the assistant message.
3. Walk the returned blocks: `text` → print to stdout (the answer); `tool_use` → `runTool`, then `remember(b, out)` (context bookkeeping), then log `[step N] name input -> output` to **stderr** so stdout stays clean, and build a `tool_result` block.
4. Stop when `StopReason != "tool_use"` or no results were produced; otherwise append the results as a `user` message.
5. Then the cheap mitigation: `trimOldResults(msgs)`.
6. Falling out of the loop prints the step-limit notice and returns `errStepLimit`.

The stderr/stdout split is intentional: logs and diagnostics on stderr, only model text on stdout.

## `tools.go` — tool surface

`tools` is a `[]map[string]any` of four entries (`get_time`, `read_file`, `write_file`, `run_shell`), each with `name`, `description`, and an `input_schema` built by `schema(fields)`, which marks every listed field as a required string. This slice is what gets attached to API requests.

`toolInput` is one struct holding all possible arguments (`path`, `content`, `command`); `runTool(name, raw)` unmarshals into it and dispatches:
- `get_time` → `time.Now().Format(time.RFC1123)`.
- `read_file` → `os.ReadFile`, `truncate`d.
- `write_file` → validates `path`, `MkdirAll(filepath.Dir(path))`, `WriteFile` 0644, returns `"wrote <path>"`.
- `run_shell` → `runShell`.
- unknown name → `"unknown tool: <name>"`.

**Errors are text, never Go errors** — every failure path returns an `"error: ..."` string so the model can see and recover from it. `runShell` runs `sh -c` with `context.WithTimeout(30s)` and sets `cmd.WaitDelay = 2s` (documented: without it, a background child holding the output pipe open would hang `CombinedOutput` past the timeout). It appends `[timed out after 30s]` or `[err]`, substitutes `(no output)` for empty output, and truncates.

**Output limiting:** `maxOutput = 10 KiB`; `truncate` = `truncateN(s, maxOutput)`. `truncateN` backs off to a UTF-8 rune boundary (`utf8.RuneStart`) before cutting and appends `"\n[truncated]"`. `preview(s, n)` flattens newlines/CRs to spaces and limits to `n` runes plus `"..."`, used for stderr logs.

## `context.go` — keeping context small

Two escalating techniques, plus a memory side-channel so neither makes the model forget what it read.

**Tunables:** `contextBudget` (`AGENT_CONTEXT_BUDGET`, default 50000, via `envInt`), `stubAfter = 6` (tool results older than this many messages are stubbed), `keepTail = 4` (recent messages kept verbatim during compaction), `minGap = 3` (minimum steps between compactions).

**Technique 1 — `trimOldResults` (cheap, every step).** Replaces the content of old `tool_result` blocks with a short stub that includes the `callInfo` description and a "re-read only if needed" hint, returning how many were trimmed (0 on the second call, per its test). Newest results and the tail stay verbatim.

**Technique 2 — `compact` (expensive, near budget).** When the prompt exceeds `contextBudget`, asks the model (`summaryPrompt`, called via `callModel` with `withTools=false`) to summarize the middle of the conversation, preserving the first prompt, the current `request`, and the last `keepTail` messages. `cutIndex` picks a safe cut point that doesn't split an assistant/tool_use + user/tool_result pair (the "assistant" boundary, `cut > 1`, leaving at least `keepTail-1` messages), returning `-1` when there's nothing worth compacting.

**Side records (the important trick).** Even after stubbing/summarizing, the agent keeps:
- `callInfo` — tool_use id → short description ("read_file main.go", "run_shell <preview>").
- `fullOut` — tool_use id → the *untrimmed* output, fed to the summarizer so the summary is based on real content.
- `filesRead` — path → `digest` (line count, byte size, first-line preview), rendered by `filesReadList()` as a sorted bullet list, which is what lets the system prompt's "the summary lists files you already read" promise hold. `remember(b, out)` populates all three.

## `examples/buggy` — test fixture, not part of the agent

`words.go` is a separate `package buggy` with `ReverseWords`, which joins the words in reverse order separated by single spaces but loops `for i := len(words)-1; i > 0; i--` — an off-by-one that always drops the first word. `words_test.go`'s table expects the correct behaviour (`"hello big world"` → `"world big hello"`, whitespace collapsed), so this is a deliberate bug for the agent to find and fix. It's the same kind of fixture validated by `context_test.go`, which tests the context helpers directly: `TestCutIndexKeepsToolPairs` (never splits tool pairs), `TestTrimOldResults` (stubs only old results, idempotent), `TestAddUserTextMerges` (turn alternation), and `TestTruncateNKeepsUTF8`.

## Data flow summary

`main` → `runAgent` loop → `callModel` (HTTP) ↔ model; model returns text (stdout) and `tool_use` blocks → `runTool` executes (with `truncate`/error-as-text) → `remember` records descriptions/full outputs/files → results appended as a `user` turn → `trimOldResults` each step → when `lastPrompt > contextBudget`, `compact` (with `filesReadList` + preserved head/request/tail) shrinks the history and bookkeeping resets. The system prompt ties it together by telling the model that old results may be stubbed, history summarized, and which files were already read.
````
