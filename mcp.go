package main

// MCP client support, built on the official SDK (github.com/modelcontextprotocol/go-sdk).
//
// At startup the agent connects to every server in the MCP config (see
// internal/mcpconf and `mini-agent mcp add`), lists their tools and offers them
// to the model as mcp__<server>__<tool>. Tool calls are routed back to the
// session of the server that owns the tool.
//
// Transports: stdio (a local command), Streamable HTTP (remote, the current
// spec) and SSE (the older remote transport).

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/yedamao/mini-agent/internal/mcpconf"
)

const mcpPrefix = "mcp__"

// mcpTool remembers which session owns a tool offered to the model.
type mcpTool struct {
	session *mcp.ClientSession
	server  string
	name    string // the tool's name on its server
}

// mcpHub holds the open sessions and the tool routing table.
type mcpHub struct {
	sessions map[string]*mcp.ClientSession
	tools    map[string]mcpTool // model-facing name -> owner
}

var hub *mcpHub // nil when no MCP servers are configured

// mcpServers returns the servers to connect to: the config file, plus
// AGENT_MCP=<command> as a shortcut for one stdio server.
func mcpServers() (map[string]mcpconf.Server, error) {
	cfg, err := mcpconf.Load(mcpconf.DefaultPath())
	if err != nil {
		return nil, err
	}
	servers := cfg.MCPServers
	if path := os.Getenv("AGENT_MCP"); path != "" {
		name := sanitize(strings.TrimSuffix(filepath.Base(path), filepath.Ext(path)))
		servers[name] = mcpconf.Server{Command: path}
	}
	return servers, nil
}

// startMCP connects to all servers and adds their tools to toolDefs.
// A server that fails is reported and skipped; the others still work.
func startMCP(ctx context.Context, servers map[string]mcpconf.Server) {
	if len(servers) == 0 {
		return
	}
	hub = &mcpHub{sessions: map[string]*mcp.ClientSession{}, tools: map[string]mcpTool{}}
	for _, name := range sortedKeys(servers) {
		session, tools, err := connectServer(ctx, name, servers[name], 30*time.Second)
		if err != nil {
			fmt.Fprintf(os.Stderr, "[mcp] %s: %v (skipped)\n", name, err)
			continue
		}
		hub.sessions[name] = session
		var names []string
		for _, t := range tools {
			fullName := toolName(name, t.Name)
			hub.tools[fullName] = mcpTool{session: session, server: name, name: t.Name}
			toolDefs = append(toolDefs, map[string]any{
				"name":         fullName,
				"description":  "[MCP " + name + "] " + t.Description,
				"input_schema": schemaOf(t),
			})
			names = append(names, fullName)
		}
		fmt.Fprintf(os.Stderr, "[mcp] %s (%s): %s\n", name, servers[name].Transport(), strings.Join(names, ", "))
	}
}

// connectServer opens a session and lists the server's tools, giving up
// after timeout.
//
// ctx must outlive the session: some transports (SSE) keep their stream
// tied to the context given to Connect. So the timeout is applied by
// waiting, not by cancelling ctx.
func connectServer(ctx context.Context, name string, s mcpconf.Server, timeout time.Duration) (*mcp.ClientSession, []*mcp.Tool, error) {
	transport, err := newTransport(s)
	if err != nil {
		return nil, nil, err
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "mini-agent", Version: "0.3.0"}, nil)
	type result struct {
		session *mcp.ClientSession
		err     error
	}
	done := make(chan result, 1)
	go func() {
		session, err := client.Connect(ctx, transport, nil)
		done <- result{session, err}
	}()
	var session *mcp.ClientSession
	select {
	case r := <-done:
		if r.err != nil {
			return nil, nil, fmt.Errorf("connect: %w", r.err)
		}
		session = r.session
	case <-time.After(timeout):
		go func() { // close it if it connects later
			if r := <-done; r.session != nil {
				r.session.Close()
			}
		}()
		return nil, nil, fmt.Errorf("connect: timed out after %s", timeout)
	}
	lctx, cancel := context.WithTimeout(ctx, timeout) // plain requests can use a timeout ctx
	defer cancel()
	var tools []*mcp.Tool
	for t, err := range session.Tools(lctx, nil) { // pages through tools/list
		if err != nil {
			session.Close()
			return nil, nil, fmt.Errorf("list tools: %w", err)
		}
		tools = append(tools, t)
	}
	return session, tools, nil
}

// newTransport builds the SDK transport for a config entry.
func newTransport(s mcpconf.Server) (mcp.Transport, error) {
	if err := s.Validate(); err != nil {
		return nil, err
	}
	s, err := s.Expanded() // ${VAR} -> value, only in memory
	if err != nil {
		return nil, err
	}
	switch s.Transport() {
	case "stdio":
		cmd := exec.Command(s.Command, s.Args...)
		cmd.Env = os.Environ()
		for k, v := range s.Env {
			cmd.Env = append(cmd.Env, k+"="+v)
		}
		cmd.Stderr = os.Stderr // server logs show up in ours
		return &mcp.CommandTransport{Command: cmd}, nil
	case "http":
		return &mcp.StreamableClientTransport{Endpoint: s.URL, HTTPClient: httpClientWithHeaders(s.Headers)}, nil
	case "sse":
		return &mcp.SSEClientTransport{Endpoint: s.URL, HTTPClient: httpClientWithHeaders(s.Headers)}, nil
	}
	return nil, fmt.Errorf("unsupported transport %q", s.Transport())
}

// headerTransport adds fixed headers (e.g. Authorization) to every request.
type headerTransport struct {
	headers map[string]string
	base    http.RoundTripper
}

func (t headerTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context()) // RoundTrippers must not modify the caller's request
	for k, v := range t.headers {
		r.Header.Set(k, v)
	}
	return t.base.RoundTrip(r)
}

func httpClientWithHeaders(h map[string]string) *http.Client {
	if len(h) == 0 {
		return nil // SDK default
	}
	return &http.Client{Transport: headerTransport{headers: h, base: http.DefaultTransport}}
}

// callMCPTool forwards a model tool call to the server that owns the tool.
func callMCPTool(ctx context.Context, name string, input json.RawMessage) string {
	t, ok := hub.tools[name]
	if !ok {
		return "error: unknown MCP tool " + name
	}
	args := map[string]any{}
	if len(input) > 0 {
		if err := json.Unmarshal(input, &args); err != nil {
			return "error: bad input: " + err.Error()
		}
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	res, err := t.session.CallTool(ctx, &mcp.CallToolParams{Name: t.name, Arguments: args})
	if err != nil {
		return "error: " + err.Error()
	}
	var parts []string
	for _, c := range res.Content {
		switch c := c.(type) {
		case *mcp.TextContent:
			parts = append(parts, c.Text)
		default:
			b, _ := json.Marshal(c) // images etc.: show the raw JSON
			parts = append(parts, string(b))
		}
	}
	out := strings.Join(parts, "\n")
	if out == "" && res.StructuredContent != nil {
		b, _ := json.Marshal(res.StructuredContent)
		out = string(b)
	}
	if res.IsError {
		out = "error: " + out
	}
	return truncate(out)
}

// stopMCP closes every session (stdio servers are terminated).
func stopMCP() {
	if hub == nil {
		return
	}
	for _, s := range hub.sessions {
		s.Close()
	}
	hub = nil
}

// toolName builds mcp__<server>__<tool>, kept within the 64-character
// limit and the [A-Za-z0-9_-] alphabet that model tool names allow.
func toolName(server, tool string) string {
	n := mcpPrefix + sanitize(server) + "__" + sanitize(tool)
	if len(n) > 64 {
		n = n[:64]
	}
	return n
}

func sanitize(s string) string {
	return strings.Map(func(r rune) rune {
		if r == '_' || r == '-' || ('a' <= r && r <= 'z') || ('A' <= r && r <= 'Z') || ('0' <= r && r <= '9') {
			return r
		}
		return '_'
	}, s)
}

// schemaOf returns a tool's input schema as a plain JSON object.
func schemaOf(t *mcp.Tool) any {
	if t.InputSchema == nil {
		return map[string]any{"type": "object", "properties": map[string]any{}}
	}
	return t.InputSchema
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
