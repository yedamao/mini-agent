package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Tool inputs are plain structs. The SDK builds each tool's JSON Schema from
// the json and jsonschema tags and validates arguments before calling us.
type (
	noInput        struct{}
	wordCountInput struct {
		Text string `json:"text" jsonschema:"text to count"`
	}
	goTestInput struct {
		Dir string `json:"dir" jsonschema:"directory containing the Go module or package"`
	}
)

// newServer creates the server and registers its tools. It is shared by
// main (stdio) and the tests (in-memory transport).
func newServer() *mcp.Server {
	s := mcp.NewServer(&mcp.Implementation{Name: "mini-mcp", Version: "0.2.0"}, nil)
	mcp.AddTool(s, &mcp.Tool{Name: "get_time", Description: "Returns the current local date and time."}, getTime)
	mcp.AddTool(s, &mcp.Tool{Name: "word_count", Description: "Counts words, lines and characters in a piece of text."}, wordCount)
	mcp.AddTool(s, &mcp.Tool{Name: "go_test", Description: "Runs `go test ./...` in a directory (60s timeout) and returns the output."}, goTest)
	return s
}

// text wraps a string as a tool result. isError marks a tool-level failure:
// the call worked, but the tool reports a problem the model should see.
func text(s string, isError bool) *mcp.CallToolResult {
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: s}}, IsError: isError}
}

// Each handler returns (result, structured output, error). We use the output
// type `any` and return nil, so no output schema is generated. A returned Go
// error is also reported to the client as a tool error.

func getTime(ctx context.Context, req *mcp.CallToolRequest, _ noInput) (*mcp.CallToolResult, any, error) {
	return text(time.Now().Format(time.RFC1123), false), nil, nil
}

func wordCount(ctx context.Context, req *mcp.CallToolRequest, in wordCountInput) (*mcp.CallToolResult, any, error) {
	lines := 0
	if in.Text != "" {
		lines = strings.Count(in.Text, "\n") + 1
		if strings.HasSuffix(in.Text, "\n") {
			lines--
		}
	}
	return text(fmt.Sprintf("words: %d, lines: %d, characters: %d",
		len(strings.Fields(in.Text)), lines, utf8.RuneCountInString(in.Text)), false), nil, nil
}

func goTest(ctx context.Context, req *mcp.CallToolRequest, in goTestInput) (*mcp.CallToolResult, any, error) {
	if in.Dir == "" {
		return text("dir must not be empty", true), nil, nil
	}
	// The request context is cancelled if the client gives up; add our own timeout too.
	const timeout = 60 * time.Second
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	goBin, err := lookPathGo()
	if err != nil {
		return text("go not found: "+err.Error(), true), nil, nil
	}
	cmd := exec.CommandContext(ctx, goBin, "test", "./...")
	cmd.Dir = in.Dir
	cmd.WaitDelay = 2 * time.Second
	out, err := cmd.CombinedOutput()
	s := string(out)
	if len(s) > 10*1024 {
		s = s[:10*1024] + "\n[truncated]"
	}
	switch {
	case ctx.Err() == context.DeadlineExceeded:
		return text(s+"\n[timed out after "+timeout.String()+"]", true), nil, nil
	case err != nil:
		return text(s+"\n["+err.Error()+"]", true), nil, nil
	}
	return text(s, false), nil, nil
}

// lookPathGo finds the go binary even when PATH is thin (Claude Code often
// launches MCP servers with a minimal env). It tries PATH first, then common
// install locations, then $GOROOT/bin.
func lookPathGo() (string, error) {
	if p, err := exec.LookPath("go"); err == nil {
		return p, nil
	}
	candidates := []string{
		"/opt/homebrew/bin/go", "/usr/local/go/bin/go", "/usr/local/bin/go",
	}
	if r := os.Getenv("GOROOT"); r != "" {
		candidates = append(candidates, filepath.Join(r, "bin", "go"))
	}
	if h, err := os.UserHomeDir(); err == nil {
		candidates = append(candidates, filepath.Join(h, "go", "bin", "go"), filepath.Join(h, "sdk", "go", "bin", "go"))
	}
	for _, c := range candidates {
		if st, err := os.Stat(c); err == nil && !st.IsDir() {
			return c, nil
		}
	}
	return "", fmt.Errorf("go not in PATH or common locations")
}
