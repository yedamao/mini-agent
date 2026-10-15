package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func run(t *testing.T, ctx context.Context, dir, name string, in map[string]string) string {
	t.Helper()
	raw, _ := json.Marshal(in)
	return Run(ctx, dir, name, raw)
}

func TestWriteReadInDir(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()
	if got := run(t, ctx, dir, "write_file", map[string]string{"path": "a/b.txt", "content": "hi"}); !strings.HasPrefix(got, "wrote") {
		t.Fatal(got)
	}
	if _, err := os.Stat(filepath.Join(dir, "a/b.txt")); err != nil {
		t.Fatal("file not written relative to dir:", err)
	}
	if got := run(t, ctx, dir, "read_file", map[string]string{"path": "a/b.txt"}); got != "hi" {
		t.Errorf("read = %q", got)
	}
	if got := run(t, ctx, dir, "run_shell", map[string]string{"command": "cat a/b.txt"}); got != "hi" {
		t.Errorf("shell in dir = %q", got)
	}
}

func TestShellCancelled(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	start := time.Now()
	got := run(t, ctx, "", "run_shell", map[string]string{"command": "sleep 10"})
	if time.Since(start) > 5*time.Second || !strings.Contains(got, "[") {
		t.Errorf("not cancelled promptly: %q after %s", got, time.Since(start))
	}
}

func TestTruncateNKeepsUTF8(t *testing.T) {
	if got := TruncateN("ab你好", 4); !strings.HasPrefix(got, "ab\n") {
		t.Errorf("got %q", got)
	}
}
