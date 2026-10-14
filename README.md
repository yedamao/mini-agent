# mini-agent

A from-scratch coding agent in Go, built over 14 days.

Day 1: `go run . "what time is it?"`

Defaults to DeepSeek's Anthropic-compatible API using `$DEEPSEEK_API_KEY`.
Override with `AGENT_BASE_URL`, `AGENT_MODEL`, `AGENT_API_KEY`
(e.g. `AGENT_BASE_URL=https://api.anthropic.com AGENT_MODEL=claude-sonnet-4-5`).
