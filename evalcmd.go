package main

// Day 12–13: `mini-agent eval` — run deterministic agent eval cases.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/yedamao/mini-agent/internal/eval"
	"github.com/yedamao/mini-agent/internal/tools"
)

const evalUsage = `Run eval cases (Day 12–13). Needs an API key.

Usage:
  mini-agent eval [path]          Default path: evals/cases
  mini-agent eval -h

Each case is YAML/JSON with id, prompt, optional fixture/cwd/mcp_server/skill_from,
and a grader (contains | not_contains | json_path | shell).

Writes evals/last-run.json under the repo (or ./last-run.json if evals/ is missing).
Quiet, non-interactive: no TUI. Uses a disposable MCP config unless a case sets mcp_server.
`

func evalCommand(ctx context.Context, args []string, out io.Writer) error {
	if len(args) > 0 && (args[0] == "-h" || args[0] == "--help") {
		fmt.Fprint(out, evalUsage)
		return errHelp
	}
	suitePath := "evals/cases"
	if len(args) > 0 {
		suitePath = args[0]
	}
	cases, err := eval.LoadSuite(suitePath)
	if err != nil {
		return err
	}

	apiKey = env("AGENT_API_KEY", os.Getenv("DEEPSEEK_API_KEY"))
	client.APIKey = apiKey
	if apiKey == "" {
		return errExit{Msg: "no API key: set AGENT_API_KEY or DEEPSEEK_API_KEY"}
	}

	repoRoot, err := findRepoRoot()
	if err != nil {
		repoRoot, _ = os.Getwd()
	}

	// Isolate from the user's ~/.mini-agent/mcp.json (e.g. slow remote deepwiki).
	emptyMCP := filepath.Join(os.TempDir(), "mini-agent-eval-mcp.json")
	_ = os.WriteFile(emptyMCP, []byte(`{"mcpServers":{}}`), 0o600)
	prevMCPConfig := os.Getenv("AGENT_MCP_CONFIG")
	prevMCP := os.Getenv("AGENT_MCP")
	_ = os.Setenv("AGENT_MCP_CONFIG", emptyMCP)
	_ = os.Unsetenv("AGENT_MCP")
	defer func() {
		_ = os.Setenv("AGENT_MCP_CONFIG", prevMCPConfig)
		if prevMCP == "" {
			_ = os.Unsetenv("AGENT_MCP")
		} else {
			_ = os.Setenv("AGENT_MCP", prevMCP)
		}
	}()

	quiet = true
	discardAnswers = true
	defer func() { discardAnswers = false }()
	report := eval.RunReport{Started: time.Now()}
	origWD, _ := os.Getwd()

	fmt.Fprintf(out, "eval: %d case(s) from %s\n", len(cases), suitePath)
	for _, c := range cases {
		res := runEvalCase(ctx, c, repoRoot, out)
		report.Results = append(report.Results, res)
		switch {
		case res.Skipped:
			report.Skipped++
			fmt.Fprintf(out, "SKIP  %s  %s\n", c.ID, res.Reason)
		case res.Pass:
			report.Passed++
			fmt.Fprintf(out, "PASS  %s  steps=%d in=%d out=%d %s\n",
				c.ID, res.Steps, res.TokensIn, res.TokensOut, res.Duration.Round(time.Millisecond))
		default:
			report.Failed++
			fmt.Fprintf(out, "FAIL  %s  steps=%d in=%d out=%d %s\n",
				c.ID, res.Steps, res.TokensIn, res.TokensOut, res.Duration.Round(time.Millisecond))
			if res.Error != "" {
				fmt.Fprintf(out, "      error: %s\n", res.Error)
			}
			if res.Reason != "" {
				fmt.Fprintf(out, "      grade: %s\n", res.Reason)
			}
		}
		_ = os.Chdir(origWD)
	}
	report.Finished = time.Now()

	outPath := filepath.Join(repoRoot, "evals", "last-run.json")
	if err := os.MkdirAll(filepath.Dir(outPath), 0o755); err != nil {
		outPath = "last-run.json"
	}
	raw, _ := json.MarshalIndent(report, "", "  ")
	if err := os.WriteFile(outPath, raw, 0o644); err != nil {
		fmt.Fprintf(out, "warning: could not write %s: %v\n", outPath, err)
	} else {
		fmt.Fprintf(out, "wrote %s\n", outPath)
	}
	fmt.Fprintf(out, "summary: %d passed, %d failed, %d skipped\n",
		report.Passed, report.Failed, report.Skipped)
	if report.Failed > 0 {
		return errExit{Msg: fmt.Sprintf("%d eval case(s) failed", report.Failed), Code: 1}
	}
	return nil
}

func runEvalCase(ctx context.Context, c eval.Case, repoRoot string, out io.Writer) eval.Result {
	start := time.Now()
	res := eval.Result{ID: c.ID}

	// Reset per-case agent globals.
	resetEvalState()

	work, err := os.MkdirTemp("", "mini-agent-eval-"+c.ID+"-*")
	if err != nil {
		res.Error = err.Error()
		res.Duration = time.Since(start)
		return res
	}
	defer os.RemoveAll(work)

	if c.Fixture != "" {
		src := filepath.Join(repoRoot, "evals", "fixtures", c.Fixture)
		if err := copyDir(src, work); err != nil {
			res.Error = "fixture: " + err.Error()
			res.Duration = time.Since(start)
			return res
		}
	}
	if c.Cwd != "" && c.Cwd != "." {
		sub := filepath.Join(work, c.Cwd)
		if err := os.MkdirAll(sub, 0o755); err != nil {
			res.Error = err.Error()
			res.Duration = time.Since(start)
			return res
		}
		work = sub
	}
	if c.SkillFrom != "" {
		src := c.SkillFrom
		if !filepath.IsAbs(src) {
			src = filepath.Join(repoRoot, src)
		}
		dst := filepath.Join(work, ".mini-agent", "skills", filepath.Base(src))
		if err := copyDir(src, dst); err != nil {
			res.Error = "skill_from: " + err.Error()
			res.Duration = time.Since(start)
			return res
		}
	}

	// Optional local MCP stdio server for this case only.
	if c.MCPServer != "" {
		bin := c.MCPServer
		if !filepath.IsAbs(bin) {
			bin = filepath.Join(repoRoot, bin)
		}
		if _, err := os.Stat(bin); err != nil {
			// Try building cmd/mcp-server into bin/
			if err := ensureMCPServerBinary(repoRoot); err != nil {
				res.Skipped = true
				res.Reason = "mcp_server unavailable: " + err.Error()
				res.Duration = time.Since(start)
				return res
			}
			bin = filepath.Join(repoRoot, "bin", "mcp-server")
		}
		_ = os.Setenv("AGENT_MCP", bin)
	} else {
		_ = os.Unsetenv("AGENT_MCP")
	}

	if err := os.Chdir(work); err != nil {
		res.Error = err.Error()
		res.Duration = time.Since(start)
		return res
	}

	setupSkills()
	servers, err := mcpServers()
	if err != nil {
		res.Error = "mcp config: " + err.Error()
		res.Duration = time.Since(start)
		return res
	}
	startMCP(ctx, servers)
	defer stopMCP()

	turnCtx, cancel := context.WithTimeout(ctx, time.Duration(c.TimeoutSec)*time.Second)
	defer cancel()

	msgs, runErr := runAgent(turnCtx, []Message{{Role: "user", Content: c.Prompt}}, c.Prompt)
	res.Steps = countAssistantSteps(msgs)
	res.TokensIn = totalIn
	res.TokensOut = totalOut
	res.Answer = lastAssistantText(msgs)
	res.Duration = time.Since(start)

	if runErr != nil && !isBenignAgentErr(runErr) {
		res.Error = runErr.Error()
		// Still grade — partial answers may pass shell checks (e.g. files written).
	}

	ok, detail := eval.Grade(c.Grader, eval.GradeInput{Answer: res.Answer, Cwd: work})
	res.Pass = ok
	res.Reason = detail
	if !ok && res.Error == "" {
		// keep reason
	}
	return res
}

func resetEvalState() {
	totalIn, totalOut = 0, 0
	lastPrompt = 0
	sinceCompact = minGap
	availableSkills = nil
	skillPrompt = ""
	loadedSkills = map[string]bool{}
	toolDefs = append([]map[string]any{}, tools.Defs...)
	stopMCP()
	hub = nil
}

func countAssistantSteps(msgs []Message) int {
	n := 0
	for _, m := range msgs {
		if m.Role == "assistant" {
			n++
		}
	}
	return n
}

func lastAssistantText(msgs []Message) string {
	var parts []string
	for _, m := range msgs {
		if m.Role != "assistant" {
			continue
		}
		switch c := m.Content.(type) {
		case string:
			parts = append(parts, c)
		case []Block:
			for _, b := range c {
				if b.Type == "text" && b.Text != "" {
					parts = append(parts, b.Text)
				}
			}
		}
	}
	return strings.Join(parts, "\n")
}

func isBenignAgentErr(err error) bool {
	return err == nil || errors.Is(err, errStepLimit) || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}

func findRepoRoot() (string, error) {
	wd, err := os.Getwd()
	if err != nil {
		return "", err
	}
	dir := wd
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("go.mod not found from %s", wd)
		}
		dir = parent
	}
}

func ensureMCPServerBinary(repoRoot string) error {
	binDir := filepath.Join(repoRoot, "bin")
	_ = os.MkdirAll(binDir, 0o755)
	out := filepath.Join(binDir, "mcp-server")
	cmd := exec.Command("go", "build", "-o", out, "./cmd/mcp-server")
	cmd.Dir = repoRoot
	cmd.Env = os.Environ()
	b, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("go build mcp-server: %w (%s)", err, strings.TrimSpace(string(b)))
	}
	return nil
}

func copyDir(src, dst string) error {
	return filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if info.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		return os.WriteFile(target, data, info.Mode())
	})
}
