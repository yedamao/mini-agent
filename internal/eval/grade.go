package eval

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// GradeInput is what graders see after an agent run.
type GradeInput struct {
	Answer string // concatenated final assistant text (and/or all assistant text)
	Cwd    string
}

// Grade runs the case grader. ok is false on failure; detail explains why.
func Grade(g Grader, in GradeInput) (ok bool, detail string) {
	switch strings.ToLower(g.Type) {
	case "contains":
		if g.Value == "" {
			return false, "contains grader needs value"
		}
		if strings.Contains(in.Answer, g.Value) {
			return true, "answer contains " + strconv.Quote(g.Value)
		}
		return false, fmt.Sprintf("answer missing %q (got %q)", g.Value, trim(in.Answer, 200))
	case "not_contains":
		if strings.Contains(in.Answer, g.Value) {
			return false, fmt.Sprintf("answer unexpectedly contains %q", g.Value)
		}
		return true, "answer does not contain " + strconv.Quote(g.Value)
	case "json_path":
		return gradeJSONPath(g, in.Cwd)
	case "shell":
		return gradeShell(g, in.Cwd)
	default:
		return false, "unknown grader type " + g.Type
	}
}

func gradeJSONPath(g Grader, cwd string) (bool, string) {
	if g.Path == "" || g.JSONPath == "" {
		return false, "json_path grader needs path and json_path"
	}
	p := g.Path
	if !strings.HasPrefix(p, "/") {
		p = cwd + "/" + p
	}
	raw, err := os.ReadFile(p)
	if err != nil {
		return false, "read " + g.Path + ": " + err.Error()
	}
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return false, "json: " + err.Error()
	}
	got, err := lookupJSON(v, g.JSONPath)
	if err != nil {
		return false, err.Error()
	}
	if g.Value == "" {
		return true, fmt.Sprintf("%s = %v", g.JSONPath, got)
	}
	gotStr := fmt.Sprint(got)
	// Allow numeric compare without type fuss.
	if gs, err1 := strconv.ParseFloat(gotStr, 64); err1 == nil {
		if ws, err2 := strconv.ParseFloat(g.Value, 64); err2 == nil && gs == ws {
			return true, fmt.Sprintf("%s = %v", g.JSONPath, got)
		}
	}
	if gotStr == g.Value {
		return true, fmt.Sprintf("%s = %q", g.JSONPath, gotStr)
	}
	return false, fmt.Sprintf("%s = %v, want %s", g.JSONPath, got, g.Value)
}

func gradeShell(g Grader, cwd string) (bool, string) {
	if g.Value == "" {
		return false, "shell grader needs value (command)"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "bash", "-c", g.Value)
	cmd.Dir = cwd
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	err := cmd.Run()
	if err == nil {
		return true, "shell exit 0: " + g.Value
	}
	return false, fmt.Sprintf("shell failed: %v; stderr=%s", err, trim(stderr.String(), 300))
}

// lookupJSON supports dotted paths like $.words or words.count.
func lookupJSON(v any, path string) (any, error) {
	path = strings.TrimSpace(path)
	path = strings.TrimPrefix(path, "$.")
	path = strings.TrimPrefix(path, "$")
	path = strings.TrimPrefix(path, ".")
	if path == "" {
		return v, nil
	}
	cur := v
	for _, part := range strings.Split(path, ".") {
		if part == "" {
			continue
		}
		m, ok := cur.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("json_path %q: not an object at %q", path, part)
		}
		next, ok := m[part]
		if !ok {
			return nil, fmt.Errorf("json_path %q: missing key %q", path, part)
		}
		cur = next
	}
	return cur, nil
}

func trim(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
