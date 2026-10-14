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

- The agent can use any stdio MCP server's tools: `AGENT_MCP=~/.local/bin/mini-mcp go run . "..."`.
  They show up as `mcp__<name>`. The client in `mcp.go` is hand-written to show the
  handshake; for a real client, use the SDK's `mcp.NewClient` + `mcp.CommandTransport`.
- `examples/mcp-from-scratch` is the same server written with the standard library only.
  It's for learning the protocol (newline-delimited JSON-RPC 2.0: `initialize`,
  `notifications/initialized`, `tools/list`, `tools/call`), not for shipping. Talk to it by hand:

      printf '%s\n' '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"me","version":"0"}}}' \
        '{"jsonrpc":"2.0","method":"notifications/initialized"}' \
        '{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"word_count","arguments":{"text":"hi there"}}}' \
        | go run ./examples/mcp-from-scratch
