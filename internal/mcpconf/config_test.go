package mcpconf

import (
	"path/filepath"
	"testing"
)

func TestLoadSaveRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "mcp.json")
	cfg, err := Load(path) // missing file = empty config
	if err != nil || len(cfg.MCPServers) != 0 {
		t.Fatalf("load missing: %v %v", cfg, err)
	}
	cfg.MCPServers["local"] = Server{Command: "mini-mcp", Args: []string{"-x"}}
	cfg.MCPServers["remote"] = Server{Type: "http", URL: "http://h/mcp", Headers: map[string]string{"Authorization": "Bearer ${TOK}"}}
	if err := cfg.Save(path); err != nil {
		t.Fatal(err)
	}
	got, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if got.MCPServers["remote"].Headers["Authorization"] != "Bearer ${TOK}" {
		t.Error("headers must be saved unexpanded")
	}
	if got.MCPServers["local"].Transport() != "stdio" || got.MCPServers["remote"].Transport() != "http" {
		t.Error("bad transports")
	}
}

func TestExpanded(t *testing.T) {
	t.Setenv("TOK", "s3cret")
	s, err := Server{URL: "http://h/${TOK}", Headers: map[string]string{"Authorization": "Bearer ${TOK}", "X": "$NOT_EXPANDED"}}.Expanded()
	if err != nil {
		t.Fatal(err)
	}
	if s.Headers["Authorization"] != "Bearer s3cret" || s.Headers["X"] != "$NOT_EXPANDED" || s.URL != "http://h/s3cret" {
		t.Errorf("got %+v", s)
	}
	if _, err := (Server{Headers: map[string]string{"A": "${NOPE_UNSET_VAR}"}}).Expanded(); err == nil {
		t.Error("want error for unset variable")
	}
}

func TestTransportAndValidate(t *testing.T) {
	cases := []struct {
		s    Server
		want string
		ok   bool
	}{
		{Server{URL: "http://x"}, "http", true},
		{Server{Type: "sse", URL: "http://x"}, "sse", true},
		{Server{Type: "streamable-http", URL: "http://x"}, "http", true},
		{Server{Command: "x"}, "stdio", true},
		{Server{Type: "http"}, "http", false},
		{Server{Type: "ws", URL: "x"}, "ws", false},
	}
	for _, c := range cases {
		if c.s.Transport() != c.want || (c.s.Validate() == nil) != c.ok {
			t.Errorf("%+v: transport %s, validate %v", c.s, c.s.Transport(), c.s.Validate())
		}
	}
	if ValidName("a__b") || ValidName("has space") || !ValidName("my-server_1") {
		t.Error("ValidName")
	}
}
