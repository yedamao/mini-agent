package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseRootHelpAndVersion(t *testing.T) {
	o, err := parseRoot([]string{"-h"})
	if !errors.Is(err, errHelp) || !o.Help {
		t.Fatalf("help: %+v %v", o, err)
	}
	o, err = parseRoot([]string{"--version"})
	if err != nil || !o.Version {
		t.Fatalf("version: %+v %v", o, err)
	}
	o, err = parseRoot([]string{"-q", "-C", "/tmp", "--", "-n", "dry-run"})
	if err != nil || !o.Quiet || o.Dir != "/tmp" || strings.Join(o.Args, " ") != "-n dry-run" {
		t.Fatalf("productive flags: %+v %v", o, err)
	}
	_, err = parseRoot([]string{"-unknown"})
	var ee errExit
	if !errors.As(err, &ee) || !strings.Contains(ee.Msg, "-h") {
		t.Fatalf("unknown flag: %v", err)
	}
}

func TestRunHelpExitAndNoArgs(t *testing.T) {
	ctx := context.Background()
	for _, args := range [][]string{nil, {}, {"-h"}, {"--help"}, {"help"}} {
		err := run(ctx, args)
		if !errors.Is(err, errHelp) {
			t.Errorf("%v: %v", args, err)
		}
	}
}

func TestRunSubcommandsNeedNoAPIKey(t *testing.T) {
	ctx := context.Background()
	t.Setenv("AGENT_API_KEY", "")
	t.Setenv("DEEPSEEK_API_KEY", "")
	savedKey := apiKey
	apiKey = ""
	t.Cleanup(func() { apiKey = savedKey })
	t.Setenv("AGENT_MCP_CONFIG", filepath.Join(t.TempDir(), "mcp.json"))
	home := t.TempDir()
	t.Setenv("HOME", home)

	// Capture stdout for help.
	old := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w
	err := run(ctx, []string{"mcp", "-h"})
	w.Close()
	os.Stdout = old
	var buf bytes.Buffer
	buf.ReadFrom(r)
	if !errors.Is(err, errHelp) || !strings.Contains(buf.String(), "Manage MCP servers") {
		t.Fatalf("mcp -h: %v\n%s", err, buf.String())
	}

	err = run(ctx, []string{"s", "-h"})
	if !errors.Is(err, errHelp) {
		t.Fatalf("s -h: %v", err)
	}
	err = run(ctx, []string{"m", "list"})
	if err != nil {
		t.Fatalf("m list with empty config: %v", err)
	}
	err = run(ctx, []string{"doctor"})
	if err != nil {
		t.Fatalf("doctor: %v", err)
	}
	// Agent run without a key fails clearly.
	err = run(ctx, []string{"hello"})
	var ee errExit
	if !errors.As(err, &ee) || !strings.Contains(ee.Error(), "no API key") {
		t.Fatalf("run without key: %v", err)
	}
}

func TestDoctorOutput(t *testing.T) {
	t.Setenv("AGENT_API_KEY", "")
	t.Setenv("DEEPSEEK_API_KEY", "x") // presence only; doctor must not print it
	t.Setenv("HOME", t.TempDir())
	t.Setenv("AGENT_MCP_CONFIG", filepath.Join(t.TempDir(), "missing.json"))
	var out bytes.Buffer
	if err := doctor(&out); err != nil {
		t.Fatal(err)
	}
	s := out.String()
	if strings.Contains(s, "x") && strings.Contains(s, "DEEPSEEK") {
		// "DEEPSEEK_API_KEY is set" is fine; the value "x" alone as a line would be bad.
		// Ensure the raw key value isn't echoed as its own token after "set".
	}
	if !strings.Contains(s, "DEEPSEEK_API_KEY is set") || strings.Contains(s, "is set: x") || strings.Contains(s, "=x") {
		t.Errorf("doctor leaked or missed key presence:\n%s", s)
	}
	if !strings.Contains(s, "MCP config") || !strings.Contains(s, "Skills") {
		t.Errorf("doctor incomplete:\n%s", s)
	}
}

func TestRunCChangesDirectory(t *testing.T) {
	dir := t.TempDir()
	wd, _ := os.Getwd()
	t.Cleanup(func() { os.Chdir(wd) })
	t.Setenv("AGENT_API_KEY", "")
	t.Setenv("DEEPSEEK_API_KEY", "")
	// doctor after -C should report the new cwd.
	old := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w
	err := run(context.Background(), []string{"-C", dir, "doctor"})
	w.Close()
	os.Stdout = old
	var buf bytes.Buffer
	buf.ReadFrom(r)
	if err != nil {
		t.Fatal(err)
	}
	cwd, _ := filepath.EvalSymlinks(mustGetwd(t))
	want, _ := filepath.EvalSymlinks(dir)
	if cwd != want {
		t.Errorf("cwd = %s, want %s", cwd, want)
	}
	if !strings.Contains(buf.String(), dir) && !strings.Contains(buf.String(), want) {
		t.Errorf("doctor didn't show new cwd:\n%s", buf.String())
	}
}

func TestMCPAddHelp(t *testing.T) {
	var out bytes.Buffer
	err := mcpCommand(context.Background(), []string{"add", "-h"}, &out)
	if !errors.Is(err, errHelp) || !strings.Contains(out.String(), "mcp add") {
		t.Fatalf("%v\n%s", err, out.String())
	}
}

func mustGetwd(t *testing.T) string {
	t.Helper()
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	return cwd
}
