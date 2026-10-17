package main

// Agent Skills (see internal/skills). At startup the agent discovers skills
// and lists only their names and descriptions in the system prompt. The model
// calls use_skill to load one skill's full instructions when a task needs it.

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"

	"github.com/yedamao/mini-agent/internal/skills"
)

var (
	availableSkills []*skills.Skill
	skillPrompt     string              // appended to the system prompt
	loadedSkills    = map[string]bool{} // kept across compaction (see context.go)
)

const useSkillTool = "use_skill"

// skillRoots: user scope from $HOME, project scope from the current folder.
func skillRoots() []skills.Root {
	home, _ := os.UserHomeDir()
	cwd, _ := os.Getwd()
	return skills.Roots(home, cwd)
}

// setupSkills discovers skills and, if there are any, adds the use_skill tool.
func setupSkills() {
	found, warnings := skills.Discover(skillRoots())
	for _, w := range warnings {
		fmt.Fprintf(os.Stderr, "[skills] skipped: %v\n", w)
	}
	if len(found) == 0 {
		return
	}
	availableSkills = found
	skillPrompt = skills.PromptSection(found)
	var names []string
	for _, s := range found {
		names = append(names, s.Name)
	}
	toolDefs = append(toolDefs, map[string]any{
		"name":        useSkillTool,
		"description": "Loads a skill's full instructions (and the folder holding its scripts and reference files). Call it when the task matches a skill in <available_skills>.",
		"input_schema": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"name": map[string]any{"type": "string", "enum": names, "description": "skill name"},
			},
			"required": []string{"name"},
		},
	})
	fmt.Fprintf(os.Stderr, "[skills] available: %s\n", strings.Join(names, ", "))
}

// useSkill is the use_skill tool: it returns the SKILL.md body and folder.
func useSkill(raw json.RawMessage) string {
	var in struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(raw, &in); err != nil {
		return "error: bad input: " + err.Error()
	}
	s := skills.Find(availableSkills, in.Name)
	if s == nil {
		var names []string
		for _, s := range availableSkills {
			names = append(names, s.Name)
		}
		return fmt.Sprintf("error: no skill %q; available: %s", in.Name, strings.Join(names, ", "))
	}
	loadedSkills[s.Name] = true
	fmt.Fprintf(os.Stderr, "[skills] loaded %s (%s scope, %s)\n", s.Name, s.Scope, s.Dir)
	return truncate(s.Instructions())
}

// loadedSkillsNote is added to compaction summaries. The use_skill result
// may have been summarized away, so the model is told to load it again.
func loadedSkillsNote() string {
	if len(loadedSkills) == 0 {
		return ""
	}
	names := make([]string, 0, len(loadedSkills))
	for n := range loadedSkills {
		names = append(names, n)
	}
	sort.Strings(names)
	return "\n\nSkills already in use: " + strings.Join(names, ", ") +
		". Their instructions may no longer be in context; call use_skill again if you need the exact steps."
}

// skillCommand implements `mini-agent skill ...`.
func skillCommand(ctx context.Context, args []string, out io.Writer) error {
	usage := errors.New(`usage: mini-agent skill add <path|git-url> [--project] [--force]
       mini-agent skill list
       mini-agent skill show <name>
       mini-agent skill remove <name> [--project]
       mini-agent skill new <name> [--project]`)
	if len(args) == 0 {
		return usage
	}
	fs := flag.NewFlagSet("skill", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	project := fs.Bool("project", false, "use the project scope (./.mini-agent/skills) instead of the user scope")
	force := fs.Bool("force", false, "replace an installed skill")
	// Allow flags before or after the positional arguments.
	var pos []string
	rest := args[1:]
	for len(rest) > 0 {
		if err := fs.Parse(rest); err != nil {
			return fmt.Errorf("%v\n%w", err, usage)
		}
		rest = fs.Args()
		if len(rest) > 0 {
			pos, rest = append(pos, rest[0]), rest[1:]
		}
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	cwd, _ := os.Getwd()
	root, scope := skills.UserDir(home), "user"
	if *project {
		root, scope = skills.ProjectDir(cwd), "project"
	}

	switch args[0] {
	case "add":
		if len(pos) != 1 {
			return usage
		}
		added, err := skills.Add(ctx, pos[0], root, *force)
		for _, s := range added {
			fmt.Fprintf(out, "Added skill %s (%s scope) -> %s\n", s.Name, scope, s.Dir)
		}
		return err
	case "list", "ls":
		found, warnings := skills.Discover(skillRoots())
		if len(found) == 0 {
			fmt.Fprintln(out, "No skills found. Add one with: mini-agent skill add <path|git-url>")
		}
		for _, s := range found {
			fmt.Fprintf(out, "%-24s %-8s %s\n    %s\n", s.Name, s.Scope, s.Dir, firstLine(s.Description, 100))
		}
		for _, w := range warnings {
			fmt.Fprintf(out, "skipped: %v\n", w)
		}
		return nil
	case "show":
		if len(pos) != 1 {
			return usage
		}
		found, _ := skills.Discover(skillRoots())
		s := skills.Find(found, pos[0])
		if s == nil {
			return fmt.Errorf("no skill %q", pos[0])
		}
		fmt.Fprintf(out, "name: %s\nscope: %s\ndescription: %s\n", s.Name, s.Scope, s.Description)
		for k, v := range map[string]string{"license": s.License, "compatibility": s.Compatibility, "allowed-tools": s.AllowedTools} {
			if v != "" {
				fmt.Fprintf(out, "%s: %s\n", k, v)
			}
		}
		fmt.Fprintf(out, "\n%s", s.Instructions())
		return nil
	case "remove", "rm":
		if len(pos) != 1 {
			return usage
		}
		if err := skills.Remove(root, pos[0]); err != nil {
			return err
		}
		fmt.Fprintf(out, "Removed skill %s from %s\n", pos[0], root)
		return nil
	case "new":
		if len(pos) != 1 {
			return usage
		}
		dir, err := skills.New(root, pos[0])
		if err != nil {
			return err
		}
		fmt.Fprintf(out, "Created %s/SKILL.md; edit the description and steps.\n", dir)
		return nil
	}
	return usage
}

func firstLine(s string, n int) string {
	s, _, _ = strings.Cut(s, "\n")
	return preview(s, n)
}
