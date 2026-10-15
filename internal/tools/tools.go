// Package tools implements the agent's built-in tools: get_time, read_file,
// write_file and run_shell. Tool errors are returned as text so the model
// can see them and recover.
package tools

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

// MaxOutput caps tool output so one huge result can't flood the model's context.
const MaxOutput = 10 * 1024

// Defs describes each tool to the model: a name, what it does, and a JSON schema for its input.
var Defs = []map[string]any{
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

// Input holds every possible argument; each tool uses the fields it needs.
type Input struct {
	Path    string `json:"path"`
	Content string `json:"content"`
	Command string `json:"command"`
}

// Run executes one tool call and returns its result as text. Relative paths
// and shell commands are resolved in dir ("" means the current directory).
// Cancelling ctx stops a running shell command.
func Run(ctx context.Context, dir, name string, raw json.RawMessage) string {
	var in Input
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &in); err != nil {
			return "error: bad input: " + err.Error()
		}
	}
	path := in.Path
	if dir != "" && path != "" && !filepath.IsAbs(path) {
		path = filepath.Join(dir, path)
	}
	switch name {
	case "get_time":
		return time.Now().Format(time.RFC1123)
	case "read_file":
		data, err := os.ReadFile(path)
		if err != nil {
			return "error: " + err.Error()
		}
		return Truncate(string(data))
	case "write_file":
		if in.Path == "" {
			return "error: path is required"
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return "error: " + err.Error()
		}
		if err := os.WriteFile(path, []byte(in.Content), 0o644); err != nil {
			return "error: " + err.Error()
		}
		return "wrote " + in.Path
	case "run_shell":
		return runShell(ctx, dir, in.Command)
	}
	return "unknown tool: " + name
}

// runShell runs a command with a 30-second timeout and returns its combined output.
func runShell(ctx context.Context, dir, command string) string {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if strings.TrimSpace(command) == "" {
		return "error: command is required"
	}
	cmd := exec.CommandContext(ctx, "sh", "-c", command)
	// Without WaitDelay, a background child holding the output pipe open
	// would make CombinedOutput hang past the timeout.
	cmd.Dir = dir
	cmd.WaitDelay = 2 * time.Second
	out, err := cmd.CombinedOutput()
	result := string(out)
	if ctx.Err() == context.DeadlineExceeded {
		result += "\n[timed out after 30s]"
	} else if ctx.Err() != nil {
		result += "\n[cancelled]"
	} else if err != nil {
		result += "\n[" + err.Error() + "]"
	}
	if result == "" {
		result = "(no output)"
	}
	return Truncate(result)
}

// Truncate keeps output under MaxOutput bytes.
func Truncate(s string) string { return TruncateN(s, MaxOutput) }

// TruncateN cuts s to at most n bytes without splitting a UTF-8 character.
func TruncateN(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n] + "\n[truncated]"
}

// Preview shortens text to one line of at most n characters, for logs.
func Preview(s string, n int) string {
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
