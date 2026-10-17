// Package skills implements Agent Skills (https://agentskills.io), the same
// format Claude Code uses: a skill is a folder with a SKILL.md file holding
// YAML frontmatter (name, description, ...) and markdown instructions, plus
// optional scripts/, references/ and assets/.
//
// Agents use progressive disclosure: only each skill's name and description
// go into the system prompt. The model loads a skill's full instructions when
// a task needs it, and reads its other files only as required.
package skills

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"

	"go.yaml.in/yaml/v3"
)

// FileName is the required file in every skill folder.
const FileName = "SKILL.md"

// Skill is one parsed SKILL.md.
type Skill struct {
	Name          string            `yaml:"name"`
	Description   string            `yaml:"description"`
	License       string            `yaml:"license,omitempty"`
	Compatibility string            `yaml:"compatibility,omitempty"`
	Metadata      map[string]string `yaml:"metadata,omitempty"`
	// AllowedTools is experimental in the spec. mini-agent shows it but
	// does not enforce it.
	AllowedTools string `yaml:"allowed-tools,omitempty"`

	Dir   string `yaml:"-"` // absolute path of the skill folder
	Scope string `yaml:"-"` // "user" or "project"
	Body  string `yaml:"-"` // markdown after the frontmatter
}

// namePattern: lowercase letters, digits and single hyphens, not at the ends.
var namePattern = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)

// ValidateName applies the spec's name rules.
func ValidateName(name string) error {
	switch {
	case name == "":
		return errors.New("name is required")
	case utf8.RuneCountInString(name) > 64:
		return errors.New("name must be at most 64 characters")
	case !namePattern.MatchString(name):
		return fmt.Errorf("invalid name %q: use lowercase letters, digits and hyphens; no leading, trailing or double hyphens", name)
	}
	return nil
}

// Validate checks the frontmatter against the spec. The name must also
// match the folder name.
func (s *Skill) Validate() error {
	if err := ValidateName(s.Name); err != nil {
		return err
	}
	if s.Dir != "" && filepath.Base(s.Dir) != s.Name {
		return fmt.Errorf("name %q must match its folder name %q", s.Name, filepath.Base(s.Dir))
	}
	if n := utf8.RuneCountInString(strings.TrimSpace(s.Description)); n == 0 || n > 1024 {
		return errors.New("description is required and must be at most 1024 characters")
	}
	if utf8.RuneCountInString(s.Compatibility) > 500 {
		return errors.New("compatibility must be at most 500 characters")
	}
	return nil
}

// Parse reads and validates dir/SKILL.md.
func Parse(dir string) (*Skill, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(filepath.Join(abs, FileName))
	if err != nil {
		return nil, err
	}
	s, err := ParseBytes(data)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", filepath.Join(abs, FileName), err)
	}
	s.Dir = abs
	if err := s.Validate(); err != nil {
		return nil, fmt.Errorf("%s: %w", filepath.Join(abs, FileName), err)
	}
	return s, nil
}

// ParseBytes splits the YAML frontmatter from the markdown body. It does not
// check the folder name; use Parse for that.
func ParseBytes(data []byte) (*Skill, error) {
	data = bytes.TrimPrefix(data, []byte("\ufeff")) // tolerate a BOM
	text := strings.ReplaceAll(string(data), "\r\n", "\n")
	if !strings.HasPrefix(text, "---\n") {
		return nil, errors.New("missing YAML frontmatter (the file must start with ---)")
	}
	front, body, ok := strings.Cut(text[4:], "\n---")
	if !ok {
		return nil, errors.New("frontmatter is not closed with ---")
	}
	// The closing --- must be a whole line.
	if rest, _, _ := strings.Cut(body, "\n"); strings.TrimSpace(rest) != "" {
		return nil, errors.New("frontmatter is not closed with ---")
	}
	_, body, _ = strings.Cut(body, "\n")
	var s Skill
	if err := yaml.Unmarshal([]byte(front), &s); err != nil {
		return nil, fmt.Errorf("frontmatter: %w", err)
	}
	s.Name = strings.TrimSpace(s.Name)
	s.Description = strings.TrimSpace(s.Description)
	s.Body = strings.TrimLeft(body, "\n")
	if err := s.Validate(); err != nil {
		return nil, err
	}
	return &s, nil
}

// Root is a folder that holds skill folders.
type Root struct {
	Dir   string
	Scope string // "user" or "project"
}

// Roots lists where skills are found, lowest precedence first: user scope
// (~/.claude/skills, then ~/.mini-agent/skills), then project scope under
// projectDir (.claude/skills, then .mini-agent/skills). .claude paths are
// read for compatibility with Claude Code; later roots override earlier
// ones when two skills share a name.
func Roots(home, projectDir string) []Root {
	var roots []Root
	if home != "" {
		roots = append(roots,
			Root{filepath.Join(home, ".claude", "skills"), "user"},
			Root{filepath.Join(home, ".mini-agent", "skills"), "user"})
	}
	if projectDir != "" {
		roots = append(roots,
			Root{filepath.Join(projectDir, ".claude", "skills"), "project"},
			Root{filepath.Join(projectDir, ".mini-agent", "skills"), "project"})
	}
	return roots
}

// UserDir and ProjectDir are where `skill add` installs.
func UserDir(home string) string          { return filepath.Join(home, ".mini-agent", "skills") }
func ProjectDir(projectDir string) string { return filepath.Join(projectDir, ".mini-agent", "skills") }

// Discover loads every valid skill under roots. Invalid skills are returned
// as warnings and skipped, so one broken folder doesn't hide the rest.
func Discover(roots []Root) (skills []*Skill, warnings []error) {
	byName := map[string]*Skill{}
	for _, root := range roots {
		entries, err := os.ReadDir(root.Dir)
		if err != nil {
			continue // a missing root is normal
		}
		for _, e := range entries {
			dir := filepath.Join(root.Dir, e.Name())
			if st, err := os.Stat(dir); err != nil || !st.IsDir() { // follows symlinks
				continue
			}
			if _, err := os.Stat(filepath.Join(dir, FileName)); err != nil {
				continue
			}
			s, err := Parse(dir)
			if err != nil {
				warnings = append(warnings, err)
				continue
			}
			s.Scope = root.Scope
			byName[s.Name] = s // later roots win
		}
	}
	for _, s := range byName {
		skills = append(skills, s)
	}
	sort.Slice(skills, func(i, j int) bool { return skills[i].Name < skills[j].Name })
	return skills, warnings
}

// Find returns the skill with the given name, or nil.
func Find(skills []*Skill, name string) *Skill {
	for _, s := range skills {
		if s.Name == name {
			return s
		}
	}
	return nil
}

// PromptSection is the system-prompt text listing the available skills
// (name and description only). It is empty when there are none.
func PromptSection(skills []*Skill) string {
	if len(skills) == 0 {
		return ""
	}
	var sb strings.Builder
	sb.WriteString("\n\nSkills give you extra instructions for specific tasks. When a task matches a\n")
	sb.WriteString("skill's description, call use_skill with its name BEFORE starting, then follow it.\n")
	sb.WriteString("Paths in a skill are relative to the skill folder use_skill reports: read them as\n")
	sb.WriteString("<skill folder>/<path>, and run scripts by that full path from the task's working\n")
	sb.WriteString("directory (don't cd into the skill folder).\n")
	sb.WriteString("<available_skills>\n")
	for _, s := range skills {
		fmt.Fprintf(&sb, "<skill>\n<name>%s</name>\n<description>%s</description>\n</skill>\n", s.Name, xmlEscape(s.Description))
	}
	sb.WriteString("</available_skills>")
	return sb.String()
}

// Instructions is what use_skill returns: where the skill lives, its other
// files, and the full SKILL.md body.
func (s *Skill) Instructions() string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "Skill %q loaded.\nSkill folder: %s\n", s.Name, s.Dir)
	if files := s.Files(); len(files) > 0 {
		fmt.Fprintf(&sb, "Files in the skill folder (paths relative to it): %s\n", strings.Join(files, ", "))
	}
	if s.AllowedTools != "" {
		fmt.Fprintf(&sb, "allowed-tools (informational): %s\n", s.AllowedTools)
	}
	sb.WriteString("\n" + s.Body)
	return sb.String()
}

// Files lists the skill's files other than SKILL.md (relative paths, at
// most 50), so the model knows what it can read or run.
func (s *Skill) Files() []string {
	var files []string
	filepath.WalkDir(s.Dir, func(p string, d os.DirEntry, err error) error {
		if err != nil || len(files) >= 50 {
			return filepath.SkipDir
		}
		if d.IsDir() && d.Name() == ".git" {
			return filepath.SkipDir
		}
		if rel, _ := filepath.Rel(s.Dir, p); !d.IsDir() && rel != FileName {
			files = append(files, rel)
		}
		return nil
	})
	return files
}

func xmlEscape(s string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;").Replace(s)
}
