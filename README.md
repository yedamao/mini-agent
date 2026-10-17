# mini-agent

A from-scratch coding agent in Go: tools, context compaction, MCP, Agent Skills,
a streamed bubbletea TUI, and an eval harness.

**Version:** 0.1.0

## Install

```bash
# needs Go 1.22+
git clone https://github.com/yedamao/mini-agent.git
cd mini-agent
go build -o mini-agent .
# or:
go install github.com/yedamao/mini-agent@v0.1.0
```

Put `mini-agent` on your `PATH`, or run it as `./mini-agent`.

## Quickstart

```bash
export DEEPSEEK_API_KEY=sk-...   # or AGENT_API_KEY
# optional: HTTP proxy; keep localhost out of the proxy
# export https_proxy=http://127.0.0.1:8118 NO_PROXY=localhost,127.0.0.1

mini-agent doctor                # check key, mcp.json, skills
mini-agent "what time is it?"    # one-shot
mini-agent                       # interactive TUI (default)
```

Defaults use DeepSeek’s Anthropic-compatible API. Override with `AGENT_BASE_URL`,
`AGENT_MODEL`, and `AGENT_API_KEY` (e.g. Anthropic).

## CLI

```
mini-agent                         # interactive TUI
mini-agent "prompt..."             # one-shot task
mini-agent -i                      # same as bare mini-agent
mini-agent -q -C examples/buggy "fix the failing tests"
mini-agent -- "-n means dry-run"   # prompt that starts with -

mini-agent mcp|m ...               # manage MCP servers (no API key)
mini-agent skill|s ...             # manage Agent Skills (no API key)
mini-agent eval [path]             # run eval cases (needs API key)
mini-agent doctor                  # setup check
mini-agent -h / -v                 # help / version
```

Flags: `-h`/`--help`, `-v`/`--version`, `-i`, `-q`/`--quiet`, `-C <dir>`, `--`.

### Interactive TUI

On a TTY, `-i` / bare `mini-agent` opens a full-screen bubbletea UI with live
SSE streaming, slash commands, and a status bar. Non-TTY falls back to a plain
prompt. Keys: Enter send, Esc cancel turn, Ctrl-C cancel/quit, Ctrl-D quit,
Tab complete `/`, Up/Down history, PgUp/PgDn scroll. `mini-agent -i -h` lists
slash commands. Set `NO_COLOR=1` to disable colors.

## Environment

| Variable | Default / purpose |
|----------|-------------------|
| `AGENT_API_KEY` / `DEEPSEEK_API_KEY` | API key (required for runs / `-i` / `eval`) |
| `AGENT_BASE_URL` | `https://api.deepseek.com/anthropic` |
| `AGENT_MODEL` | `deepseek-chat` |
| `AGENT_CALL_TIMEOUT` | Per-call timeout seconds (default 120) |
| `AGENT_MAX_ATTEMPTS` | Retries for transient API errors (default 5) |
| `AGENT_CONTEXT_BUDGET` | Compact after this many input tokens (default 50000) |
| `AGENT_MCP` | Extra stdio MCP server command |
| `AGENT_MCP_CONFIG` | MCP config file (default `~/.mini-agent/mcp.json`) |
| `NO_COLOR` | Disable ANSI / lipgloss colors |

Retries: 429, 5xx, network errors and per-call timeouts are retried with
backoff; `Retry-After` is honored. Other 4xx fail immediately. Ctrl-C cancels
the in-flight call (exit 130).

## Tools

Built-in: `get_time`, `read_file`, `write_file`, `run_shell` (30s timeout,
~10KB output cap), up to 20 loop steps. Context: old tool results are stubbed;
prompts over the budget are compacted with a model summary.

`run_shell` runs real commands on your machine — only use it in directories you trust.

Try the Day 5 buggy fixture:

```bash
go build -o /tmp/mini-agent .
cd examples/buggy
/tmp/mini-agent "the tests fail; find and fix the bug, then confirm go test passes"
```

## MCP

Config: `~/.mini-agent/mcp.json` (Claude Code–compatible shape). Transports:
`stdio`, `http` (Streamable HTTP), `sse`. Tools appear as `mcp__<server>__<tool>`.
In interactive mode, MCP connects in the background so the TUI paints immediately.

```bash
go build -o ~/.local/bin/mini-mcp ./cmd/mcp-server
mini-agent mcp add local ~/.local/bin/mini-mcp
mini-agent mcp add deepwiki https://mcp.deepwiki.com/mcp
mini-agent mcp list
```

`${VAR}` in config fields expands from the environment at connect time. File mode
is `0600`. See `cmd/mcp-server` (SDK server) and `examples/mcp-from-scratch`
(hand-rolled JSON-RPC for learning).

## Skills

[Agent Skills](https://agentskills.io/specification) format (`SKILL.md` + optional
`scripts/`). Discovered from `~/.mini-agent/skills` and `./.mini-agent/skills`
(plus `.claude/skills` for compatibility). Only names/descriptions go in the
system prompt; the model calls `use_skill` for full instructions.

```bash
mini-agent skill add examples/skills/go-test-fixer
mini-agent skill list
mini-agent -C examples/buggy "fix the failing tests"
```

## Evals

```bash
mini-agent eval                              # evals/cases
mini-agent eval evals/cases/01_get_time.yaml
```

YAML/JSON cases with graders: `contains`, `not_contains`, `json_path`, `shell`.
Quiet, non-TUI. Writes `evals/last-run.json`. Graders are tested in `internal/eval`.

## Develop

```bash
go test ./...
go vet ./...
go build -o mini-agent .
```

License: MIT.
