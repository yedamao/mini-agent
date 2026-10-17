package skills

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// writeSkill creates root/dir/SKILL.md with the given name and description.
func writeSkill(t *testing.T, root, dir, name, desc, body string) string {
	t.Helper()
	p := filepath.Join(root, dir)
	if err := os.MkdirAll(p, 0o755); err != nil {
		t.Fatal(err)
	}
	md := "---\nname: " + name + "\ndescription: " + desc + "\n---\n" + body
	if err := os.WriteFile(filepath.Join(p, FileName), []byte(md), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestParseBytes(t *testing.T) {
	s, err := ParseBytes([]byte("---\r\nname: pdf-tools\r\ndescription: >\r\n  Works with PDFs.\r\nallowed-tools: Bash(git:*) Read\r\nmetadata:\r\n  author: me\r\n---\r\n\r\n# Body\r\nstep 1\r\n"))
	if err != nil {
		t.Fatal(err)
	}
	if s.Name != "pdf-tools" || s.Description != "Works with PDFs." || s.AllowedTools != "Bash(git:*) Read" ||
		s.Metadata["author"] != "me" || s.Body != "# Body\nstep 1\n" {
		t.Errorf("got %+v", s)
	}
	bad := map[string]string{
		"no frontmatter": "# hi",
		"unclosed":       "---\nname: a\ndescription: b\n",
		"bad yaml":       "---\nname: [a\ndescription: b\n---\n",
		"no desc":        "---\nname: a\n---\n",
		"---x close":     "---\nname: a\ndescription: b\n---x\n",
	}
	for what, md := range bad {
		if _, err := ParseBytes([]byte(md)); err == nil {
			t.Errorf("%s: want error", what)
		}
	}
}

func TestValidateName(t *testing.T) {
	for _, ok := range []string{"a", "pdf-processing", "go-test-fixer", "v2", strings.Repeat("a", 64)} {
		if err := ValidateName(ok); err != nil {
			t.Errorf("%q: %v", ok, err)
		}
	}
	for _, bad := range []string{"", "PDF-Processing", "-pdf", "pdf-", "pdf--processing", "has space", "under_score", "../x", strings.Repeat("a", 65)} {
		if ValidateName(bad) == nil {
			t.Errorf("%q should be invalid", bad)
		}
	}
}

func TestParseChecksFolderNameAndLimits(t *testing.T) {
	root := t.TempDir()
	if _, err := Parse(writeSkill(t, root, "folder", "other-name", "d", "")); err == nil {
		t.Error("name must match the folder")
	}
	if _, err := Parse(writeSkill(t, root, "long", "long", strings.Repeat("x", 1025), "")); err == nil {
		t.Error("description over 1024 chars must fail")
	}
	s, err := Parse(writeSkill(t, root, "good", "good", "a good skill", "body"))
	if err != nil || s.Dir != filepath.Join(root, "good") {
		t.Errorf("good: %v %+v", err, s)
	}
}

func TestDiscoverPrecedenceAndWarnings(t *testing.T) {
	home, proj := t.TempDir(), t.TempDir()
	writeSkill(t, filepath.Join(home, ".claude", "skills"), "shared", "shared", "from claude user", "")
	writeSkill(t, filepath.Join(home, ".mini-agent", "skills"), "shared", "shared", "from user", "")
	writeSkill(t, filepath.Join(home, ".mini-agent", "skills"), "only-user", "only-user", "user only", "")
	writeSkill(t, filepath.Join(proj, ".mini-agent", "skills"), "shared", "shared", "from project", "")
	writeSkill(t, filepath.Join(proj, ".claude", "skills"), "Broken", "Broken", "bad name", "")
	os.MkdirAll(filepath.Join(proj, ".mini-agent", "skills", "not-a-skill"), 0o755) // no SKILL.md: ignored

	got, warnings := Discover(Roots(home, proj))
	if len(got) != 2 || got[0].Name != "only-user" || got[1].Name != "shared" {
		t.Fatalf("skills = %v", got)
	}
	if got[1].Description != "from project" || got[1].Scope != "project" || got[0].Scope != "user" {
		t.Errorf("project must override user: %+v", got[1])
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0].Error(), "Broken") {
		t.Errorf("warnings = %v", warnings)
	}
}

func TestPromptSectionAndInstructions(t *testing.T) {
	if PromptSection(nil) != "" {
		t.Error("no skills: empty section")
	}
	root := t.TempDir()
	dir := writeSkill(t, root, "fixer", "fixer", "Fix <things> & stuff", "Do X then Y.\n")
	os.MkdirAll(filepath.Join(dir, "scripts"), 0o755)
	os.WriteFile(filepath.Join(dir, "scripts", "run.sh"), []byte("echo"), 0o755)
	s, _ := Parse(dir)
	p := PromptSection([]*Skill{s})
	if !strings.Contains(p, "<name>fixer</name>") || !strings.Contains(p, "Fix &lt;things&gt; &amp; stuff") || strings.Contains(p, "Do X") {
		t.Errorf("prompt section must list name+description only:\n%s", p)
	}
	in := s.Instructions()
	if !strings.Contains(in, "Skill folder: "+dir) || !strings.Contains(in, filepath.Join("scripts", "run.sh")) || !strings.Contains(in, "Do X then Y.") {
		t.Errorf("instructions:\n%s", in)
	}
}

func TestAddRemoveNew(t *testing.T) {
	ctx := context.Background()
	src, dest := t.TempDir(), t.TempDir()
	dir := writeSkill(t, src, "my-skill", "my-skill", "does things", "body")
	os.MkdirAll(filepath.Join(dir, "scripts"), 0o755)
	os.WriteFile(filepath.Join(dir, "scripts", "x.sh"), []byte("#!/bin/sh\necho hi\n"), 0o755)

	got, err := Add(ctx, dir, dest, false)
	if err != nil || len(got) != 1 || got[0].Name != "my-skill" {
		t.Fatalf("add: %v %v", got, err)
	}
	if st, err := os.Stat(filepath.Join(dest, "my-skill", "scripts", "x.sh")); err != nil || st.Mode().Perm()&0o100 == 0 {
		t.Errorf("script not copied as executable: %v", err)
	}
	if _, err := Add(ctx, dir, dest, false); err == nil {
		t.Error("second add without --force must fail")
	}
	if _, err := Add(ctx, dir, dest, true); err != nil {
		t.Errorf("--force: %v", err)
	}
	// A collection folder installs every skill inside it.
	coll := t.TempDir()
	writeSkill(t, filepath.Join(coll, "skills"), "one", "one", "first", "")
	writeSkill(t, coll, "two", "two", "second", "")
	if got, err := Add(ctx, coll, dest, false); err != nil || len(got) != 2 {
		t.Errorf("collection: %v %v", got, err)
	}
	// Invalid skills are rejected before anything is copied.
	badSrc := t.TempDir()
	writeSkill(t, badSrc, "x", "Bad_Name", "d", "")
	if _, err := Add(ctx, filepath.Join(badSrc, "x"), dest, false); err == nil {
		t.Error("invalid skill must be rejected")
	}

	if err := Remove(dest, "my-skill"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dest, "my-skill")); !os.IsNotExist(err) {
		t.Error("not removed")
	}
	if Remove(dest, "my-skill") == nil || Remove(dest, "../etc") == nil {
		t.Error("removing a missing or bad name must fail")
	}

	p, err := New(dest, "fresh-skill")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Parse(p); err != nil {
		t.Errorf("scaffold must be a valid skill: %v", err)
	}
	if _, err := New(dest, "fresh-skill"); err == nil {
		t.Error("New over an existing folder must fail")
	}
}

func TestAddFromGitRepo(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	repo := t.TempDir()
	writeSkill(t, repo, "", "ignored-folder-name", "from git", "body") // SKILL.md at repo root
	for _, args := range [][]string{{"init", "-q"}, {"add", "."}, {"-c", "user.email=t@t", "-c", "user.name=t", "commit", "-qm", "x"}} {
		if out, err := exec.Command("git", append([]string{"-C", repo}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	dest := t.TempDir()
	got, err := Add(context.Background(), "file://"+repo+"/.git", dest, false)
	if err != nil || len(got) != 1 || got[0].Name != "ignored-folder-name" {
		t.Fatalf("git add: %v %v", got, err)
	}
	if _, err := os.Stat(filepath.Join(dest, "ignored-folder-name", ".git")); !os.IsNotExist(err) {
		t.Error(".git must not be copied")
	}
}

func TestIsGitURL(t *testing.T) {
	for src, want := range map[string]bool{
		"https://github.com/anthropics/skills": true, "git@github.com:a/b.git": true,
		"file:///tmp/x.git": true, "./examples/skills/x": false, "/abs/path": false,
	} {
		if IsGitURL(src) != want {
			t.Errorf("IsGitURL(%q) = %v", src, !want)
		}
	}
}
