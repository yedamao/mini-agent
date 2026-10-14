package main

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"
)

// maxOutput caps tool output so one huge result can't flood the model's context.
const maxOutput = 10 * 1024

// tools describes each tool to the model: a name, what it does, and a JSON schema for its input.
var tools = []map[string]any{
	{
		"name":         "get_time",
		"description":  "Returns the current local date and time.",
		"input_schema": schema(nil),
	},
	{
		"name":         "read_file",
		"description":  "Reads a text file and returns its contents.",
		"input_schema": schema(map[string]string{"path": "File path to read"}),
	},
	{
		"name":         "write_file",
		"description":  "Writes content to a file, creating or overwriting it.",
		"input_schema": schema(map[string]string{"path": "File path to write", "content": "Full file content"}),
	},
	{
		"name":         "run_shell",
		"description":  "Runs a shell command (sh -c) with a 30s timeout and returns combined stdout/stderr.",
		"input_schema": schema(map[string]string{"command": "Shell command to run"}),
	},
}

// schema builds a JSON schema where every listed field is a required string.
func schema(fields map[string]string) map[string]any {
	props := map[string]any{}
	required := []string{}
	for name, desc := range fields {
		props[name] = map[string]any{"type": "string", "description": desc}
		required = append(required, name)
	}
	return map[string]any{"type": "object", "properties": props, "required": required}
}

// toolInput holds every possible argument; each tool uses the fields it needs.
type toolInput struct {
	Path    string `json:"path"`
	Content string `json:"content"`
	Command string `json:"command"`
}

// runTool executes one tool call and returns its result as text.
// Errors are returned as text too, so the model can see them and recover.
func runTool(name string, raw json.RawMessage) string {
	var in toolInput
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &in); err != nil {
			return "error: bad input: " + err.Error()
		}
	}
	if strings.HasPrefix(name, mcpPrefix) && mcp != nil {
		return callMCPTool(name, raw) // Day 8: tools from an MCP server
	}
	switch name {
	case "get_time":
		return time.Now().Format(time.RFC1123)
	case "read_file":
		data, err := os.ReadFile(in.Path)
		if err != nil {
			return "error: " + err.Error()
		}
		return truncate(string(data))
	case "write_file":
		if in.Path == "" {
			return "error: path is required"
		}
		if err := os.MkdirAll(filepath.Dir(in.Path), 0o755); err != nil {
			return "error: " + err.Error()
		}
		if err := os.WriteFile(in.Path, []byte(in.Content), 0o644); err != nil {
			return "error: " + err.Error()
		}
		return "wrote " + in.Path
	case "run_shell":
		return runShell(in.Command)
	}
	return "unknown tool: " + name
}

// runShell runs a command with a 30-second timeout and returns its combined output.
func runShell(command string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if strings.TrimSpace(command) == "" {
		return "error: command is required"
	}
	cmd := exec.CommandContext(ctx, "sh", "-c", command)
	// Without WaitDelay, a background child holding the output pipe open
	// would make CombinedOutput hang past the timeout.
	cmd.WaitDelay = 2 * time.Second
	out, err := cmd.CombinedOutput()
	result := string(out)
	if ctx.Err() == context.DeadlineExceeded {
		result += "\n[timed out after 30s]"
	} else if err != nil {
		result += "\n[" + err.Error() + "]"
	}
	if result == "" {
		result = "(no output)"
	}
	return truncate(result)
}

// truncate keeps output under maxOutput bytes.
func truncate(s string) string { return truncateN(s, maxOutput) }

// truncateN cuts s to at most n bytes without splitting a UTF-8 character.
func truncateN(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n] + "\n[truncated]"
}

// preview shortens text to one line of at most n characters, for logs.
func preview(s string, n int) string {
	r := []rune(s)
	for i, c := range r {
		if c == '\n' || c == '\r' {
			r[i] = ' '
		}
	}
	if len(r) > n {
		return string(r[:n]) + "..."
	}
	return string(r)
}
