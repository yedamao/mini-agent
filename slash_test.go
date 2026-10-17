package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

func TestParseSlash(t *testing.T) {
	cases := []struct {
		in         string
		name, args string
		ok         bool
	}{
		{"hello", "", "", false},
		{"/help", "help", "", true},
		{"  /Model  gpt-x  ", "model", "gpt-x", true},
		{"/budget 3000", "budget", "3000", true},
		{"/", "", "", true},
		{"/exit now", "exit", "now", true},
	}
	for _, c := range cases {
		name, args, ok := parseSlash(c.in)
		if name != c.name || args != c.args || ok != c.ok {
			t.Errorf("parseSlash(%q) = (%q,%q,%v), want (%q,%q,%v)", c.in, name, args, ok, c.name, c.args, c.ok)
		}
	}
}

func TestDispatchSlashTable(t *testing.T) {
	st := &replState{Msgs: []Message{{Role: "user", Content: "x"}}, Turns: 2}
	client.Model = "deepseek-chat"
	contextBudget = 50000

	cases := []struct {
		line                                          string
		wantQuit, wantClear, wantCompact, wantHandled bool
		contains                                      string
	}{
		{"hello", false, false, false, false, ""},
		{"/help", false, false, false, true, "/status"},
		{"/exit", true, false, false, true, ""},
		{"/quit", true, false, false, true, ""},
		{"/q", true, false, false, true, ""},
		{"/clear", false, true, false, true, "cleared"},
		{"/model", false, false, false, true, "deepseek-chat"},
		{"/model other-model", false, false, false, true, "other-model"},
		{"/budget", false, false, false, true, "50000"},
		{"/budget 1234", false, false, false, true, "1234"},
		{"/budget nope", false, false, false, true, "usage"},
		{"/status", false, false, false, true, "model:"},
		{"/skills", false, false, false, true, "No skills"},
		{"/mcp", false, false, false, true, "No MCP"},
		{"/doctor", false, false, false, true, "API key"},
		{"/nope", false, false, false, true, "Unknown"},
		{"/comp", false, false, false, true, "Did you mean"},
	}
	for _, c := range cases {
		var out bytes.Buffer
		// Restore budget/model between cases that mutate.
		res := dispatchSlash(context.Background(), c.line, st, &out)
		if res.Quit != c.wantQuit || res.Clear != c.wantClear || res.Compact != c.wantCompact || res.Handled != c.wantHandled {
			t.Errorf("%q: got %+v", c.line, res)
		}
		if c.contains != "" && !strings.Contains(out.String(), c.contains) {
			t.Errorf("%q: output %q, want substring %q", c.line, out.String(), c.contains)
		}
	}
	if client.Model != "other-model" || contextBudget != 1234 {
		t.Errorf("session mutations: model=%s budget=%d", client.Model, contextBudget)
	}
	if len(st.Msgs) != 0 || st.Turns != 0 {
		t.Errorf("/clear should have reset state: %+v", st)
	}
}

func TestSlashCompletions(t *testing.T) {
	got := slashCompletions("/ex")
	if len(got) != 1 || got[0] != "/exit" {
		t.Errorf("got %v", got)
	}
	if got := slashCompletions("nope"); len(got) != 0 {
		t.Errorf("nope: %v", got)
	}
	if got := slashCompletions("/model "); len(got) != 0 {
		t.Errorf("/model with args: %v", got)
	}
	all := slashCompletions("/")
	if len(all) < 8 {
		t.Errorf("expected many completions, got %v", all)
	}
}

func TestFindSlashAliases(t *testing.T) {
	if findSlash("quit").Name != "exit" || findSlash("?").Name != "help" {
		t.Error("aliases")
	}
	if findSlash("missing") != nil {
		t.Error("missing")
	}
}

func TestCompactSlashRequiresHistory(t *testing.T) {
	var out bytes.Buffer
	st := &replState{}
	res := dispatchSlash(context.Background(), "/compact", st, &out)
	if !res.Handled || res.Compact || !strings.Contains(out.String(), "Nothing to compact") {
		t.Errorf("got %+v %q", res, out.String())
	}
	st.Msgs = []Message{{}, {}, {}, {}, {}}
	out.Reset()
	res = dispatchSlash(context.Background(), "/compact", st, &out)
	if !res.Compact {
		t.Errorf("want Compact, got %+v", res)
	}
}
