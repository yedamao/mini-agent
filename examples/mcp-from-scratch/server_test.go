package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// roundTrip sends one JSON line to handle and decodes the reply generically.
func roundTrip(t *testing.T, line string) map[string]any {
	t.Helper()
	resp := handle([]byte(line))
	if resp == nil {
		return nil
	}
	raw, err := json.Marshal(resp)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	json.Unmarshal(raw, &m)
	return m
}

func TestInitialize(t *testing.T) {
	m := roundTrip(t, `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"test","version":"0"}}}`)
	res := m["result"].(map[string]any)
	if res["protocolVersion"] != protocolVersion || res["capabilities"].(map[string]any)["tools"] == nil {
		t.Errorf("bad initialize result: %v", res)
	}
	if m["id"] != 1.0 {
		t.Errorf("id not echoed: %v", m["id"])
	}
}

func TestNotificationGetsNoReply(t *testing.T) {
	if m := roundTrip(t, `{"jsonrpc":"2.0","method":"notifications/initialized"}`); m != nil {
		t.Errorf("got reply %v", m)
	}
}

func TestToolsList(t *testing.T) {
	m := roundTrip(t, `{"jsonrpc":"2.0","id":"a","method":"tools/list"}`)
	list := m["result"].(map[string]any)["tools"].([]any)
	var names []string
	for _, x := range list {
		tl := x.(map[string]any)
		names = append(names, tl["name"].(string))
		if tl["inputSchema"] == nil {
			t.Errorf("%v has no inputSchema", tl["name"])
		}
	}
	if strings.Join(names, ",") != "get_time,word_count,go_test" {
		t.Errorf("tools = %v", names)
	}
}

func callText(t *testing.T, name, args string) (string, bool) {
	t.Helper()
	m := roundTrip(t, `{"jsonrpc":"2.0","id":7,"method":"tools/call","params":{"name":"`+name+`","arguments":`+args+`}}`)
	res, ok := m["result"].(map[string]any)
	if !ok {
		t.Fatalf("no result: %v", m)
	}
	content := res["content"].([]any)[0].(map[string]any)
	return content["text"].(string), res["isError"].(bool)
}

func TestWordCount(t *testing.T) {
	text, isErr := callText(t, "word_count", `{"text":"hello big world\nsecond line\n"}`)
	if isErr || text != "words: 5, lines: 2, characters: 28" {
		t.Errorf("got %q (isError=%v)", text, isErr)
	}
}

func TestGoTest(t *testing.T) {
	dir := t.TempDir()
	write := func(name, body string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("go.mod", "module tmp\n\ngo 1.21\n")
	write("x_test.go", "package tmp\nimport \"testing\"\nfunc TestFail(t *testing.T) { t.Fatal(\"boom\") }\n")
	text, isErr := callText(t, "go_test", `{"dir":"`+dir+`"}`)
	if !isErr || !strings.Contains(text, "boom") {
		t.Errorf("want failing test reported as isError, got %q (isError=%v)", text, isErr)
	}
}

func TestErrors(t *testing.T) {
	cases := map[string]float64{
		`not json`: errParse,
		`{"jsonrpc":"2.0","id":2,"method":"nope"}`:                                   errMethodNotFound,
		`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"missing"}}`: errInvalidParams,
	}
	for line, code := range cases {
		m := roundTrip(t, line)
		e, ok := m["error"].(map[string]any)
		if !ok || e["code"] != code {
			t.Errorf("%s: got %v, want code %v", line, m, code)
		}
	}
}
