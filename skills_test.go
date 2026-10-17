package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// inTempHome runs a test with $HOME and the working folder in temp dirs.
func inTempHome(t *testing.T) (home, proj string) {
	home, proj = t.TempDir(), t.TempDir()
	t.Setenv("HOME", home)
	wd, _ := os.Getwd()
	os.Chdir(proj)
	saved := toolDefs
	t.Cleanup(func() {
		os.Chdir(wd)
		toolDefs, availableSkills, skillPrompt, loadedSkills = saved, nil, "", map[string]bool{}
	})
	return home, proj
}

func skillCmd(t *testing.T, args ...string) (string, error) {
	var out bytes.Buffer
	err := skillCommand(context.Background(), args, &out)
	return out.String(), err
}

func TestSkillCLIAndUseSkill(t *testing.T) {
	wd, _ := os.Getwd()
	example := filepath.Join(wd, "examples", "skills", "go-test-fixer")
	home, proj := inTempHome(t)

	if _, err := skillCmd(t, "add", example); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(home, ".mini-agent", "skills", "go-test-fixer", "SKILL.md")); err != nil {
		t.Fatal("not installed in user scope:", err)
	}
	if _, err := skillCmd(t, "new", "my-helper", "--project"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(proj, ".mini-agent", "skills", "my-helper", "SKILL.md")); err != nil {
		t.Fatal("scaffold not in project scope:", err)
	}
	out, err := skillCmd(t, "list")
	if err != nil || !strings.Contains(out, "go-test-fixer") || !strings.Contains(out, "my-helper") || !strings.Contains(out, "project") {
		t.Fatalf("list: %v\n%s", err, out)
	}
	if out, _ := skillCmd(t, "show", "go-test-fixer"); !strings.Contains(out, "Skill folder:") {
		t.Errorf("show:\n%s", out)
	}

	// Agent side: only names/descriptions in the prompt; use_skill loads the body.
	setupSkills()
	if !strings.Contains(skillPrompt, "<name>go-test-fixer</name>") || strings.Contains(skillPrompt, "## Steps") {
		t.Errorf("prompt must list names/descriptions only:\n%s", skillPrompt)
	}
	got := runTool(context.Background(), useSkillTool, json.RawMessage(`{"name":"go-test-fixer"}`))
	if !strings.Contains(got, "Skill folder: "+filepath.Join(home, ".mini-agent", "skills", "go-test-fixer")) || !strings.Contains(got, "scripts/") {
		t.Errorf("use_skill:\n%s", got)
	}
	if !strings.Contains(loadedSkillsNote(), "go-test-fixer") {
		t.Error("loaded skill must be noted for compaction")
	}
	if got := runTool(context.Background(), useSkillTool, json.RawMessage(`{"name":"nope"}`)); !strings.HasPrefix(got, "error:") {
		t.Errorf("unknown skill: %s", got)
	}

	if _, err := skillCmd(t, "remove", "my-helper", "--project"); err != nil {
		t.Fatal(err)
	}
	if _, err := skillCmd(t, "remove", "my-helper"); err == nil {
		t.Error("removing from the wrong scope must fail")
	}
}
