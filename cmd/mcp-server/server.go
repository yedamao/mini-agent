package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"time"
	"unicode/utf8"
)

// protocolVersion is the MCP spec revision we implement. If the client asks
// for another version we answer with ours; the client decides if it can cope.
const protocolVersion = "2025-06-18"

// request is a JSON-RPC 2.0 request or notification (no ID).
type request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"` // number or string; echoed back as-is
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

// response is a JSON-RPC 2.0 response: exactly one of Result or Error is set.
type response struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// Standard JSON-RPC error codes.
const (
	errParse          = -32700
	errInvalidRequest = -32600
	errMethodNotFound = -32601
	errInvalidParams  = -32602
)

// tool is one entry in the tools/list result. InputSchema is a JSON Schema
// describing the arguments, the same shape the model's tool API expects.
type tool struct {
	Name        string                                   `json:"name"`
	Description string                                   `json:"description"`
	InputSchema map[string]any                           `json:"inputSchema"`
	run         func(args map[string]any) (string, bool) // (text, isError)
}

var tools = []tool{
	{
		Name:        "get_time",
		Description: "Returns the current local date and time.",
		InputSchema: map[string]any{"type": "object", "properties": map[string]any{}},
		run: func(map[string]any) (string, bool) {
			return time.Now().Format(time.RFC1123), false
		},
	},
	{
		Name:        "word_count",
		Description: "Counts words, lines and characters in a piece of text.",
		InputSchema: map[string]any{
			"type":       "object",
			"properties": map[string]any{"text": map[string]any{"type": "string", "description": "Text to count"}},
			"required":   []string{"text"},
		},
		run: func(args map[string]any) (string, bool) {
			text, ok := args["text"].(string)
			if !ok {
				return "text must be a string", true
			}
			lines := 0
			if text != "" {
				lines = strings.Count(text, "\n") + 1
				if strings.HasSuffix(text, "\n") {
					lines--
				}
			}
			return fmt.Sprintf("words: %d, lines: %d, characters: %d",
				len(strings.Fields(text)), lines, utf8.RuneCountInString(text)), false
		},
	},
	{
		Name:        "go_test",
		Description: "Runs `go test ./...` in a directory (60s timeout) and returns the output.",
		InputSchema: map[string]any{
			"type":       "object",
			"properties": map[string]any{"dir": map[string]any{"type": "string", "description": "Directory containing the Go module or package"}},
			"required":   []string{"dir"},
		},
		run: func(args map[string]any) (string, bool) {
			dir, ok := args["dir"].(string)
			if !ok || dir == "" {
				return "dir must be a non-empty string", true
			}
			return goTest(dir, 60*time.Second)
		},
	},
}

// goTest runs the tests and reports failure (or a timeout) as a tool error.
func goTest(dir string, timeout time.Duration) (string, bool) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "go", "test", "./...")
	cmd.Dir = dir
	cmd.WaitDelay = 2 * time.Second
	out, err := cmd.CombinedOutput()
	text := string(out)
	if len(text) > 10*1024 {
		text = text[:10*1024] + "\n[truncated]"
	}
	if ctx.Err() == context.DeadlineExceeded {
		return text + "\n[timed out after " + timeout.String() + "]", true
	}
	if err != nil {
		return text + "\n[" + err.Error() + "]", true
	}
	return text, false
}

// handle processes one incoming line and returns the response to send,
// or nil for notifications. It never panics on bad input.
func handle(line []byte) *response {
	var req request
	if err := json.Unmarshal(line, &req); err != nil {
		return &response{JSONRPC: "2.0", ID: json.RawMessage("null"), Error: &rpcError{errParse, "parse error: " + err.Error()}}
	}
	isNotification := len(req.ID) == 0
	result, rerr := dispatch(req)
	if isNotification {
		return nil // never answer notifications, even on error
	}
	resp := &response{JSONRPC: "2.0", ID: req.ID}
	if rerr != nil {
		resp.Error = rerr
	} else {
		resp.Result = result
	}
	return resp
}

// dispatch routes a request by method name.
func dispatch(req request) (any, *rpcError) {
	if req.JSONRPC != "2.0" || req.Method == "" {
		return nil, &rpcError{errInvalidRequest, "invalid request"}
	}
	switch req.Method {
	case "initialize":
		// Tell the client who we are and what we support (only tools).
		return map[string]any{
			"protocolVersion": protocolVersion,
			"capabilities":    map[string]any{"tools": map[string]any{}},
			"serverInfo":      map[string]any{"name": "mini-mcp", "version": "0.1.0"},
		}, nil
	case "notifications/initialized", "notifications/cancelled":
		return nil, nil // the client is ready / gave up on a request; nothing to do
	case "ping":
		return map[string]any{}, nil
	case "tools/list":
		return map[string]any{"tools": tools}, nil
	case "tools/call":
		return callTool(req.Params)
	}
	return nil, &rpcError{errMethodNotFound, "method not found: " + req.Method}
}

// callTool runs a tool. A tool that fails still returns a normal result with
// isError=true, so the model sees the message; JSON-RPC errors are for
// protocol problems such as an unknown tool name.
func callTool(params json.RawMessage) (any, *rpcError) {
	var p struct {
		Name      string         `json:"name"`
		Arguments map[string]any `json:"arguments"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, &rpcError{errInvalidParams, "bad params: " + err.Error()}
	}
	for _, t := range tools {
		if t.Name == p.Name {
			text, isErr := t.run(p.Arguments)
			return map[string]any{
				"content": []map[string]any{{"type": "text", "text": text}},
				"isError": isErr,
			}, nil
		}
	}
	return nil, &rpcError{errInvalidParams, "unknown tool: " + p.Name}
}
