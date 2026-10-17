package skills

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// IsGitURL reports whether src looks like a git repository URL.
func IsGitURL(src string) bool {
	return strings.HasPrefix(src, "git@") || strings.HasPrefix(src, "ssh://") ||
		strings.HasSuffix(src, ".git") ||
		((strings.HasPrefix(src, "https://") || strings.HasPrefix(src, "http://")) && !strings.HasSuffix(src, ".md"))
}

// Add installs skills from src into destRoot. src is a local folder or a
// git URL (cloned with --depth 1). If src itself holds a SKILL.md it is one
// skill; otherwise every subfolder (one level deep, or under skills/) that
// holds a valid SKILL.md is installed, like a skills collection repo.
// Existing skills are replaced only if force is set.
func Add(ctx context.Context, src, destRoot string, force bool) ([]*Skill, error) {
	dir := src
	if IsGitURL(src) {
		tmp, err := os.MkdirTemp("", "mini-agent-skill-*")
		if err != nil {
			return nil, err
		}
		defer os.RemoveAll(tmp)
		ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
		defer cancel()
		cmd := exec.CommandContext(ctx, "git", "clone", "--depth", "1", "--quiet", src, tmp)
		if out, err := cmd.CombinedOutput(); err != nil {
			return nil, fmt.Errorf("git clone %s: %v: %s", src, err, strings.TrimSpace(string(out)))
		}
		dir = tmp
	}
	found, err := findSkillDirs(dir)
	if err != nil {
		return nil, err
	}
	// Validate everything first so a bad skill doesn't leave a half-done install.
	var parsed []*Skill
	for _, d := range found {
		s, err := parseForInstall(d)
		if err != nil {
			return nil, err
		}
		if _, err := os.Stat(filepath.Join(destRoot, s.Name)); err == nil && !force {
			return nil, fmt.Errorf("skill %q is already installed in %s (use --force to replace it)", s.Name, destRoot)
		}
		parsed = append(parsed, s)
	}
	var installed []*Skill
	for _, s := range parsed {
		dest := filepath.Join(destRoot, s.Name)
		if err := replaceDir(s.Dir, dest); err != nil {
			return installed, err
		}
		ns, err := Parse(dest)
		if err != nil {
			return installed, err
		}
		installed = append(installed, ns)
	}
	return installed, nil
}

// parseForInstall parses a skill whose folder name may differ from its name
// (e.g. a clone in a temp dir): it is installed under its declared name.
func parseForInstall(dir string) (*Skill, error) {
	data, err := os.ReadFile(filepath.Join(dir, FileName))
	if err != nil {
		return nil, err
	}
	s, err := ParseBytes(data)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", filepath.Join(dir, FileName), err)
	}
	s.Dir, _ = filepath.Abs(dir)
	return s, nil
}

func findSkillDirs(dir string) ([]string, error) {
	if st, err := os.Stat(dir); err != nil || !st.IsDir() {
		return nil, fmt.Errorf("%s is not a folder", dir)
	}
	if _, err := os.Stat(filepath.Join(dir, FileName)); err == nil {
		return []string{dir}, nil
	}
	var found []string
	for _, parent := range []string{dir, filepath.Join(dir, "skills")} {
		entries, _ := os.ReadDir(parent)
		for _, e := range entries {
			if e.IsDir() {
				if _, err := os.Stat(filepath.Join(parent, e.Name(), FileName)); err == nil {
					found = append(found, filepath.Join(parent, e.Name()))
				}
			}
		}
	}
	if len(found) == 0 {
		return nil, fmt.Errorf("no %s found in %s", FileName, dir)
	}
	return found, nil
}

// replaceDir copies src to dest via a temp folder and rename, so dest is
// never left half-written.
func replaceDir(src, dest string) error {
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return err
	}
	tmp, err := os.MkdirTemp(filepath.Dir(dest), "."+filepath.Base(dest)+"-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	if err := copyDir(src, tmp); err != nil {
		return err
	}
	if err := os.RemoveAll(dest); err != nil {
		return err
	}
	return os.Rename(tmp, dest)
}

// copyDir copies regular files (keeping their mode, so scripts stay
// executable), skipping .git and symlinks.
func copyDir(src, dst string) error {
	return filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		if d.IsDir() && d.Name() == ".git" {
			return filepath.SkipDir
		}
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		if !d.Type().IsRegular() {
			return nil // skip symlinks, devices, ...
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		in, err := os.Open(p)
		if err != nil {
			return err
		}
		defer in.Close()
		out, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, info.Mode().Perm())
		if err != nil {
			return err
		}
		if _, err := io.Copy(out, in); err != nil {
			out.Close()
			return err
		}
		return out.Close()
	})
}

// Remove deletes the named skill from root.
func Remove(root, name string) error {
	if err := ValidateName(name); err != nil {
		return err // also blocks paths like ../x
	}
	dir := filepath.Join(root, name)
	if _, err := os.Stat(filepath.Join(dir, FileName)); err != nil {
		return fmt.Errorf("no skill %q in %s", name, root)
	}
	return os.RemoveAll(dir)
}

// New scaffolds root/name/SKILL.md with a template to fill in.
func New(root, name string) (string, error) {
	if err := ValidateName(name); err != nil {
		return "", err
	}
	dir := filepath.Join(root, name)
	if _, err := os.Stat(dir); err == nil {
		return "", fmt.Errorf("%s already exists", dir)
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	if err := os.MkdirAll(filepath.Join(dir, "scripts"), 0o755); err != nil {
		return "", err
	}
	tmpl := `---
name: ` + name + `
description: TODO what this skill does, and when to use it (include keywords a user might say).
---

# ` + name + `

## When to use
TODO

## Steps
1. TODO

## Files
- scripts/: helper scripts; run them with run_shell from the skill folder.
`
	if err := os.WriteFile(filepath.Join(dir, FileName), []byte(tmpl), 0o644); err != nil {
		return "", err
	}
	return dir, nil
}
