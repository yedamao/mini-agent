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
