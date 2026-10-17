package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// connect runs the server and an SDK client over in-memory transports,
// so the tests exercise the real protocol without a subprocess.
func connect(t *testing.T) *mcp.ClientSession {
	t.Helper()
	ctx := context.Background()
	ct, st := mcp.NewInMemoryTransports()
	ss, err := newServer().Connect(ctx, st, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ss.Close() })
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "0"}, nil).Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cs.Close() })
	return cs
}

func call(t *testing.T, cs *mcp.ClientSession, name string, args map[string]any) (string, bool) {
	t.Helper()
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	var parts []string
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			parts = append(parts, tc.Text)
		}
	}
	return strings.Join(parts, "\n"), res.IsError
}

func TestListTools(t *testing.T) {
	res, err := connect(t).ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, tl := range res.Tools {
		names = append(names, tl.Name)
		if tl.InputSchema == nil {
			t.Errorf("%s has no input schema", tl.Name)
		}
	}
	if got := strings.Join(names, ","); got != "get_time,go_test,word_count" { // SDK lists tools sorted
		t.Errorf("tools = %s", got)
	}
}

func TestWordCount(t *testing.T) {
	got, isErr := call(t, connect(t), "word_count", map[string]any{"text": "hello big world\nsecond line\n"})
	if isErr || got != "words: 5, lines: 2, characters: 28" {
		t.Errorf("got %q (isError=%v)", got, isErr)
	}
}

func TestWordCountValidatesInput(t *testing.T) {
	// The SDK checks arguments against the generated schema: text must be a string.
	cs := connect(t)
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "word_count", Arguments: map[string]any{"text": 42}})
	if err == nil && !res.IsError {
		t.Error("want an error for a non-string text")
	}
}

func TestGetTime(t *testing.T) {
	if got, isErr := call(t, connect(t), "get_time", map[string]any{}); isErr || got == "" {
		t.Errorf("got %q (isError=%v)", got, isErr)
	}
}

func TestGoTestReportsFailure(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module tmp\n\ngo 1.21\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "x_test.go"), []byte("package tmp\nimport \"testing\"\nfunc TestFail(t *testing.T) { t.Fatal(\"boom\") }\n"), 0o644)
	got, isErr := call(t, connect(t), "go_test", map[string]any{"dir": dir})
	if !isErr || !strings.Contains(got, "boom") {
		t.Errorf("want failure as isError, got %q (isError=%v)", got, isErr)
	}
}

// bearerClient sends a fixed Authorization header.
type bearerClient struct{ token string }

func (b bearerClient) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("Authorization", "Bearer "+b.token)
	return http.DefaultTransport.RoundTrip(r)
}

func TestStreamableHTTPWithToken(t *testing.T) {
	srv := httptest.NewServer(httpHandler(newServer(), "s3cret"))
	defer srv.Close()
	ctx := context.Background()
	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "0"}, nil)

	// Without the token the handshake is rejected.
	if _, err := client.Connect(ctx, &mcp.StreamableClientTransport{Endpoint: srv.URL + "/mcp"}, nil); err == nil {
		t.Fatal("want unauthorized without a token")
	}
	cs, err := client.Connect(ctx, &mcp.StreamableClientTransport{
		Endpoint:   srv.URL + "/mcp",
		HTTPClient: &http.Client{Transport: bearerClient{"s3cret"}},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()
	got, isErr := call(t, cs, "word_count", map[string]any{"text": "one two three"})
	if isErr || got != "words: 3, lines: 1, characters: 13" {
		t.Errorf("got %q (isError=%v)", got, isErr)
	}
}
