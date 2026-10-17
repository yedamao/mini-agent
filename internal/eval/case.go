// Package eval loads and grades mini-agent eval cases (Day 12–13).
package eval

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"
)

// Case is one eval scenario.
type Case struct {
	ID         string `yaml:"id" json:"id"`
	Prompt     string `yaml:"prompt" json:"prompt"`
	Cwd        string `yaml:"cwd" json:"cwd"`         // relative to suite root; working dir for the agent
	Fixture    string `yaml:"fixture" json:"fixture"` // copy this fixtures/ subdir into cwd first
	TimeoutSec int    `yaml:"timeout_sec" json:"timeout_sec"`
	MCPServer  string `yaml:"mcp_server" json:"mcp_server"` // optional stdio MCP binary (repo-relative)
	SkillFrom  string `yaml:"skill_from" json:"skill_from"` // copy skill dir into cwd/.mini-agent/skills/<name>
	SkipIf     string `yaml:"skip_if" json:"skip_if"`       // "no_api" handled by runner; reserved
	Grader     Grader `yaml:"grader" json:"grader"`
}

// Grader checks the agent outcome.
//
//	type: contains | not_contains | json_path | shell
type Grader struct {
	Type     string `yaml:"type" json:"type"`
	Value    string `yaml:"value" json:"value"`         // substring, expected JSON value, or shell command
	Path     string `yaml:"path" json:"path"`           // file for json_path (relative to cwd)
	JSONPath string `yaml:"json_path" json:"json_path"` // e.g. $.words or words
}

// Result is one case outcome for reporting / last-run.json.
type Result struct {
	ID        string        `json:"id"`
	Pass      bool          `json:"pass"`
	Skipped   bool          `json:"skipped,omitempty"`
	Reason    string        `json:"reason,omitempty"`
	Steps     int           `json:"steps"`
	TokensIn  int           `json:"tokens_in"`
	TokensOut int           `json:"tokens_out"`
	Duration  time.Duration `json:"duration_ms"` // marshalled via custom below
	Answer    string        `json:"answer,omitempty"`
	Error     string        `json:"error,omitempty"`
}

// MarshalJSON writes Duration as milliseconds.
func (r Result) MarshalJSON() ([]byte, error) {
	type alias struct {
		ID         string `json:"id"`
		Pass       bool   `json:"pass"`
		Skipped    bool   `json:"skipped,omitempty"`
		Reason     string `json:"reason,omitempty"`
		Steps      int    `json:"steps"`
		TokensIn   int    `json:"tokens_in"`
		TokensOut  int    `json:"tokens_out"`
		DurationMS int64  `json:"duration_ms"`
		Answer     string `json:"answer,omitempty"`
		Error      string `json:"error,omitempty"`
	}
	return json.Marshal(alias{
		ID: r.ID, Pass: r.Pass, Skipped: r.Skipped, Reason: r.Reason,
		Steps: r.Steps, TokensIn: r.TokensIn, TokensOut: r.TokensOut,
		DurationMS: r.Duration.Milliseconds(), Answer: r.Answer, Error: r.Error,
	})
}

// RunReport is written to evals/last-run.json.
type RunReport struct {
	Started  time.Time `json:"started"`
	Finished time.Time `json:"finished"`
	Passed   int       `json:"passed"`
	Failed   int       `json:"failed"`
	Skipped  int       `json:"skipped"`
	Results  []Result  `json:"results"`
}

// LoadCase reads one YAML or JSON case file.
func LoadCase(path string) (Case, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return Case{}, err
	}
	var c Case
	switch strings.ToLower(filepath.Ext(path)) {
	case ".json":
		err = json.Unmarshal(raw, &c)
	default: // .yaml / .yml
		err = yaml.Unmarshal(raw, &c)
	}
	if err != nil {
		return Case{}, fmt.Errorf("%s: %w", path, err)
	}
	if c.ID == "" {
		c.ID = strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	}
	if c.Prompt == "" {
		return Case{}, fmt.Errorf("%s: prompt is required", path)
	}
	if c.Grader.Type == "" {
		return Case{}, fmt.Errorf("%s: grader.type is required", path)
	}
	if c.TimeoutSec <= 0 {
		c.TimeoutSec = 120
	}
	return c, nil
}

// LoadSuite loads every *.yaml / *.yml / *.json under dir (non-recursive) or a single file.
func LoadSuite(path string) ([]Case, error) {
	st, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if !st.IsDir() {
		c, err := LoadCase(path)
		if err != nil {
			return nil, err
		}
		return []Case{c}, nil
	}
	ents, err := os.ReadDir(path)
	if err != nil {
		return nil, err
	}
	var out []Case
	for _, e := range ents {
		if e.IsDir() {
			continue
		}
		ext := strings.ToLower(filepath.Ext(e.Name()))
		if ext != ".yaml" && ext != ".yml" && ext != ".json" {
			continue
		}
		c, err := LoadCase(filepath.Join(path, e.Name()))
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no case files in %s", path)
	}
	return out, nil
}
