# mini-agent

A from-scratch coding agent in Go, built over 14 days.

Day 1: `go run . "what time is it?"`

Day 3: tools `get_time`, `read_file`, `write_file`, `run_shell` (30s timeout,
output capped at ~10KB), up to 20 loop steps. Each step is logged to stderr.
Try: `go run . "create hello.txt containing hi, then read it back and list files with ls"`

Code: `main.go` (API call + loop), `tools.go` (tool definitions + implementations).
Note: `run_shell` runs real commands on your machine; only use it in a directory you trust.

Defaults to DeepSeek's Anthropic-compatible API using `$DEEPSEEK_API_KEY`.
Override with `AGENT_BASE_URL`, `AGENT_MODEL`, `AGENT_API_KEY`
(e.g. `AGENT_BASE_URL=https://api.anthropic.com AGENT_MODEL=claude-sonnet-4-5`).

Day 5: a coding agent. `examples/buggy/` is a separate module with a failing test
(an off-by-one bug). Let the agent fix it:

    go build -o /tmp/mini-agent . && cd examples/buggy
    /tmp/mini-agent "the tests in this directory fail; find and fix the bug, then confirm go test passes"

See `examples/buggy/RUN.md` for a sample run. Afterwards, `git checkout examples/buggy` restores the bug.

Day 6-7: context management (`context.go`).
- Every step logs token usage to stderr: `[step 3] tokens prompt=… (cached …) out=… | total in=… out=…`.
  The prompt size includes cached tokens, which the API reports separately from `input_tokens`.
- Tool results older than 6 messages are replaced with short stubs (cheap).
- When a prompt goes over `AGENT_CONTEXT_BUDGET` input tokens (default 50000), the
  middle of the history is replaced by a model-written summary. The first task and the
  last few turns stay, and a tool call is never separated from its result.
- `go run . -i` starts an interactive session, so one conversation continues across prompts.

Watch compaction happen with a tiny budget:

    AGENT_CONTEXT_BUDGET=3000 go run . "read all the .go files here and explain the architecture"

Day 6-7 fixes:
- Stubs and summaries keep a list of files already read (path + digest), so the model
  doesn't re-read them; compaction waits at least 3 steps between runs so a tiny budget
  can't thrash. The summarizer sees the untrimmed tool output and is told not to invent facts.
- The system prompt limits the agent to the task: it edits files only when asked and
  answers only from files it actually read.
- `-i` mode: Ctrl-D/`exit` quits, errors don't end the session, compaction works between prompts.
- Unit tests: `go test .` (context helpers; no API calls).
- Sample run with `AGENT_CONTEXT_BUDGET=3000`: `examples/RUN-context.md`.

Day 8: an MCP server, and the agent as an MCP client.
- `cmd/mcp-server` is the server you'd ship. It's built on the official SDK,
  `github.com/modelcontextprotocol/go-sdk/mcp`: `mcp.NewServer`, `mcp.AddTool` with typed
  input structs (the SDK generates the JSON Schema and validates arguments), and
  `server.Run(ctx, &mcp.StdioTransport{})`. Tools: `get_time`, `word_count(text)`,
  `go_test(dir)` (60s timeout). Tests use `mcp.NewInMemoryTransports` with an SDK client.
- Build it and add it to Claude Code:

      go build -o ~/.local/bin/mini-mcp ./cmd/mcp-server
      claude mcp add --scope user mini-mcp -- ~/.local/bin/mini-mcp
      claude mcp list

- The agent is an MCP client too (official SDK). See "Remote MCP servers" below.
- `examples/mcp-from-scratch` is the same server written with the standard library only.
  It's for learning the protocol (newline-delimited JSON-RPC 2.0: `initialize`,
  `notifications/initialized`, `tools/list`, `tools/call`), not for shipping. Talk to it by hand:

      printf '%s\n' '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"me","version":"0"}}}' \
        '{"jsonrpc":"2.0","method":"notifications/initialized"}' \
        '{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"word_count","arguments":{"text":"hi there"}}}' \
        | go run ./examples/mcp-from-scratch

Day 10: resilience (`internal/llm`).
- Each model call gets its own timeout: `AGENT_CALL_TIMEOUT` seconds, default 120.
- Retries: 429, 5xx (including 529 "overloaded"), network errors and per-call timeouts
  are retried up to `AGENT_MAX_ATTEMPTS` (default 5) with exponential backoff and jitter
  (about 1s, 2s, 4s...). A `Retry-After` header is honored. Other 4xx (bad key, bad
  request) fail at once, since retrying can't fix them.
- Ctrl-C cancels cleanly (`signal.NotifyContext`): the in-flight request or shell
  command is aborted and the agent exits with code 130.
- Tests: `go test ./internal/llm` uses an `httptest` server that fails and then succeeds.

Day 11: the agent loop as a Temporal workflow (`cmd/temporal-agent`, `go.temporal.io/sdk`).

Why Temporal: a plain loop loses everything if the process dies halfway through a
20-step task. In Temporal, each model call and tool call is an **activity** whose result
is saved in the workflow's **history**. If a worker crashes, another worker replays the
history (without re-running finished steps) and carries on. Each activity has its own
timeout and retry policy, and the UI at http://localhost:8233 shows every step, input,
output and retry.

    temporal server start-dev                         # local dev server + UI on :8233
    go run ./cmd/temporal-agent worker                # needs DEEPSEEK_API_KEY in its env
    go run ./cmd/temporal-agent start -wait "read main.go and summarize it"

Rules this follows:
- Workflow code is deterministic: no `time.Now`, randomness or I/O. Those all happen in
  activities, so a replay makes the same decisions.
- The API key is never in workflow input or history. Activities read it from the
  worker's environment.
- Activities send heartbeats, so a dead worker is noticed in ~20s, not after the full timeout.
- 4xx errors are marked non-retryable; Retry-After becomes Temporal's `NextRetryDelay`.

`examples/RUN-temporal.md` shows a durability demo: the worker is killed in the middle of
a run, restarted, and the workflow completes.

Remote MCP servers (Streamable HTTP) and multiple servers.

The agent's MCP client (`mcp.go`) is built on the official go-sdk (`mcp.NewClient`). It
connects to every configured server at startup and offers their tools to the model as
`mcp__<server>__<tool>`; each call goes to the server that owns the tool. A server that
fails to connect is reported and skipped. Supported transports:
- `stdio`: a local command (`mcp.CommandTransport`)
- `http`: Streamable HTTP, the current remote transport (`mcp.StreamableClientTransport`)
- `sse`: the older HTTP+SSE transport (`mcp.SSEClientTransport`)

Manage servers like `claude mcp`:

    mini-agent mcp add local ~/.local/bin/mini-mcp                  # stdio
    mini-agent mcp add deepwiki https://mcp.deepwiki.com/mcp         # remote, no auth
    mini-agent mcp add -H 'Authorization: Bearer ${MY_TOKEN}' work https://example.com/mcp
    mini-agent mcp add -t sse old https://example.com/sse            # legacy SSE
    mini-agent mcp list        # checks each server: ✔ connected, N tools
    mini-agent mcp get work
    mini-agent mcp remove work

Config lives in `~/.mini-agent/mcp.json` (or `$AGENT_MCP_CONFIG`). It uses the same shape as
Claude Code's `.mcp.json`:

```json
{
  "mcpServers": {
    "local":  { "command": "/Users/you/.local/bin/mini-mcp", "args": [], "env": {} },
    "remote": { "type": "http", "url": "http://127.0.0.1:8090/mcp",
                "headers": { "Authorization": "Bearer ${MINI_MCP_TOKEN}" } }
  }
}
```

`${VAR}` in `headers`, `env`, `url`, `command` and `args` is filled in from the environment
when connecting, so tokens stay in env vars, not in the file. Quote the `-H` value in single
quotes so your shell doesn't expand it. A missing variable is an error, not an empty header.
The file is written with 0600 permissions. `AGENT_MCP=<command>` still works as a shortcut
for one extra stdio server.

`mini-mcp` can be a remote server too:

    MINI_MCP_TOKEN=$(openssl rand -hex 16) mini-mcp -http 127.0.0.1:8090    # endpoint: /mcp
    mini-agent mcp add -H 'Authorization: Bearer ${MINI_MCP_TOKEN}' remote http://127.0.0.1:8090/mcp

With `MINI_MCP_TOKEN` set it requires that bearer token; without it, anyone who can reach the
port can call `go_test`, so bind to 127.0.0.1. If you use a proxy, set
`NO_PROXY=localhost,127.0.0.1` so local servers aren't sent through it.

Agent Skills (`internal/skills`, `skills.go`).

Skills use the same format as Claude Code ([agentskills.io](https://agentskills.io/specification)).
A skill is a folder with a `SKILL.md`: YAML frontmatter plus markdown instructions, and
optionally `scripts/`, `references/` and `assets/`.

```markdown
---
name: go-test-fixer            # 1-64 chars: a-z, 0-9, single hyphens; must match the folder name
description: Step-by-step workflow for fixing failing Go tests. Use when go test reports FAIL.
license: MIT                   # optional
compatibility: Requires Go.    # optional, max 500 chars
metadata: {author: yedamao}    # optional
allowed-tools: Bash Read       # optional, experimental: shown but not enforced
---
# Steps ...
```

- **Where skills are found:** user scope is `~/.mini-agent/skills/<name>/`, and project scope is
  `./.mini-agent/skills/<name>/`. `~/.claude/skills` and `./.claude/skills` are read too, for
  compatibility with Claude Code. Project overrides user, and invalid skills are skipped with a
  warning.
- **Progressive disclosure:** only each skill's name and description go into the system prompt,
  as an `<available_skills>` list. The model calls `use_skill(name)` to load the full
  instructions, the skill's folder path and its file list. It then reads files or runs scripts
  by full path with the normal tools. Skills already loaded are listed in compaction summaries,
  so the model can load them again if their instructions were summarized away.
- **CLI:**

      mini-agent skill add examples/skills/go-test-fixer       # a folder, user scope
      mini-agent skill add https://github.com/anthropics/skills  # a git repo: installs every skill in it
      mini-agent skill add ./my-skill --project --force         # project scope, replace if present
      mini-agent skill list
      mini-agent skill show go-test-fixer
      mini-agent skill new my-skill [--project]                 # scaffold SKILL.md + scripts/
      mini-agent skill remove my-skill [--project]

  `add` checks every SKILL.md before copying anything, skips `.git` and keeps scripts
  executable.
- **Example:** `examples/skills/go-test-fixer` is a workflow for fixing failing Go tests, with
  `scripts/failing-tests.sh`. Install it, then run `mini-agent "fix the failing tests"` in
  `examples/buggy`. The log shows `[skills] loaded go-test-fixer`.
