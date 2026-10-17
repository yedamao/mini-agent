package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/yedamao/mini-agent/internal/mcpconf"
)

type echoIn struct {
	Text string `json:"text"`
}

// testServer is a tiny MCP server with one tool, "echo".
func testServer(name string) *mcp.Server {
	s := mcp.NewServer(&mcp.Implementation{Name: name, Version: "0"}, nil)
	mcp.AddTool(s, &mcp.Tool{Name: "echo", Description: "echoes text"},
		func(ctx context.Context, req *mcp.CallToolRequest, in echoIn) (*mcp.CallToolResult, any, error) {
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: name + ":" + in.Text}}}, nil, nil
		})
	return s
}

// requireBearer rejects requests without the expected Authorization header.
func requireBearer(token string, h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+token {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		h.ServeHTTP(w, r)
	})
}

// withHub resets the global MCP state around a test.
func withHub(t *testing.T) {
	saved := toolDefs
	t.Cleanup(func() { stopMCP(); toolDefs = saved })
}

func TestRemoteHTTPAndSSEServersWithAuth(t *testing.T) {
	t.Setenv("TEST_MCP_TOKEN", "tok123")
	httpSrv := httptest.NewServer(requireBearer("tok123",
		mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return testServer("alpha") }, nil)))
	t.Cleanup(httpSrv.Close)
	sseSrv := httptest.NewServer(mcp.NewSSEHandler(func(*http.Request) *mcp.Server { return testServer("beta") }, nil))
	t.Cleanup(sseSrv.Close)
	// Registered last so it runs first: close client sessions before the
	// servers, or Close would wait forever on the open SSE stream.
	withHub(t)

	startMCP(context.Background(), map[string]mcpconf.Server{
		"alpha":  {Type: "http", URL: httpSrv.URL, Headers: map[string]string{"Authorization": "Bearer ${TEST_MCP_TOKEN}"}},
		"beta":   {Type: "sse", URL: sseSrv.URL},
		"noauth": {Type: "http", URL: httpSrv.URL}, // missing token: must be skipped, not fatal
	})
	if len(hub.sessions) != 2 {
		t.Fatalf("sessions = %d, want 2 (noauth should fail)", len(hub.sessions))
	}
	// Each tool is routed to its own server.
	for name, want := range map[string]string{"mcp__alpha__echo": "alpha:hi", "mcp__beta__echo": "beta:hi"} {
		if got := runTool(context.Background(), name, json.RawMessage(`{"text":"hi"}`)); got != want {
			t.Errorf("%s = %q, want %q", name, got, want)
		}
	}
	var offered []string
	for _, d := range toolDefs {
		offered = append(offered, d["name"].(string))
	}
	if s := strings.Join(offered, ","); !strings.Contains(s, "mcp__alpha__echo") || strings.Contains(s, "noauth") {
		t.Errorf("toolDefs = %s", s)
	}
}

func TestToolNameLimits(t *testing.T) {
	if got := toolName("my.server", "do thing"); got != "mcp__my_server__do_thing" {
		t.Errorf("got %q", got)
	}
	if got := toolName(strings.Repeat("s", 40), strings.Repeat("t", 40)); len(got) != 64 {
		t.Errorf("len = %d", len(got))
	}
}

func TestMCPCommandAddListRemove(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mcp.json")
	t.Setenv("AGENT_MCP_CONFIG", path)
	srv := httptest.NewServer(mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return testServer("x") }, nil))
	defer srv.Close()

	run := func(args ...string) (string, error) {
		var out bytes.Buffer
		err := mcpCommand(context.Background(), args, &out)
		return out.String(), err
	}
	if _, err := run("add", "-H", "Authorization: Bearer ${T}", "remote", srv.URL); err != nil {
		t.Fatal(err)
	}
	if _, err := run("add", "local", "some-cmd", "--flag"); err != nil {
		t.Fatal(err)
	}
	if _, err := run("add", "remote", srv.URL); err == nil {
		t.Error("duplicate add should fail")
	}
	cfg, _ := mcpconf.Load(path)
	if r := cfg.MCPServers["remote"]; r.Type != "http" || r.Headers["Authorization"] != "Bearer ${T}" {
		t.Errorf("remote = %+v", r)
	}
	if l := cfg.MCPServers["local"]; l.Command != "some-cmd" || len(l.Args) != 1 {
		t.Errorf("local = %+v", l)
	}
	t.Setenv("T", "x")
	out, err := run("list")
	if err != nil || !strings.Contains(out, "remote (http)") || !strings.Contains(out, "connected, 1 tools") || !strings.Contains(out, "local (stdio)") {
		t.Errorf("list: %v\n%s", err, out)
	}
	if _, err := run("remove", "local"); err != nil {
		t.Fatal(err)
	}
	cfg, _ = mcpconf.Load(path)
	if _, ok := cfg.MCPServers["local"]; ok || len(cfg.MCPServers) != 1 {
		t.Errorf("after remove: %+v", cfg.MCPServers)
	}
}

func TestParseAddErrors(t *testing.T) {
	bad := [][]string{
		{"onlyname"},
		{"bad name", "x"},
		{"a__b", "x"},
		{"-H", "noColon", "n", "https://x"},
		{"-H", "A: b", "n", "local-cmd"},
		{"-t", "sse", "n", "local-cmd"},
		{"n", "https://x", "extra"},
	}
	for _, args := range bad {
		if _, _, err := parseAdd(args); err == nil {
			t.Errorf("parseAdd(%q) should fail", args)
		}
	}
	if _, s, err := parseAdd([]string{"-t", "sse", "n", "https://x/sse"}); err != nil || s.Transport() != "sse" {
		t.Errorf("sse: %+v %v", s, err)
	}
}
