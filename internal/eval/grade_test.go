package eval

import (
	"os"
	"path/filepath"
	"testing"
)

func TestGradeContains(t *testing.T) {
	ok, _ := Grade(Grader{Type: "contains", Value: "hello"}, GradeInput{Answer: "say hello there"})
	if !ok {
		t.Fatal("expected pass")
	}
	ok, _ = Grade(Grader{Type: "contains", Value: "nope"}, GradeInput{Answer: "hello"})
	if ok {
		t.Fatal("expected fail")
	}
}

func TestGradeNotContains(t *testing.T) {
	ok, _ := Grade(Grader{Type: "not_contains", Value: "error"}, GradeInput{Answer: "all good"})
	if !ok {
		t.Fatal("expected pass")
	}
}

func TestGradeJSONPath(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "out.json")
	if err := os.WriteFile(path, []byte(`{"words":3,"lines":1}`), 0o644); err != nil {
		t.Fatal(err)
	}
	ok, detail := Grade(Grader{Type: "json_path", Path: "out.json", JSONPath: "$.words", Value: "3"}, GradeInput{Cwd: dir})
	if !ok {
		t.Fatalf("expected pass: %s", detail)
	}
	ok, _ = Grade(Grader{Type: "json_path", Path: "out.json", JSONPath: "words", Value: "9"}, GradeInput{Cwd: dir})
	if ok {
		t.Fatal("expected fail")
	}
}

func TestGradeShell(t *testing.T) {
	dir := t.TempDir()
	ok, _ := Grade(Grader{Type: "shell", Value: "test -d ."}, GradeInput{Cwd: dir})
	if !ok {
		t.Fatal("expected pass")
	}
	ok, _ = Grade(Grader{Type: "shell", Value: "exit 1"}, GradeInput{Cwd: dir})
	if ok {
		t.Fatal("expected fail")
	}
}

func TestLoadCaseYAML(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "c.yaml")
	if err := os.WriteFile(p, []byte("id: t\nprompt: hi\ngrader:\n  type: contains\n  value: hi\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	c, err := LoadCase(p)
	if err != nil || c.ID != "t" || c.Grader.Type != "contains" {
		t.Fatalf("%+v %v", c, err)
	}
}
