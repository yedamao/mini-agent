package main

// Slash commands for interactive mode (/help, /exit, /status, ...).

import (
	"context"
	"fmt"
	"io"
	"os"
	"sort"
	"strconv"
	"strings"
)

// slashResult is what a slash command returns to the REPL.
type slashResult struct {
	Quit    bool   // leave the session
	Clear   bool   // reset conversation history
	Compact bool   // force context compaction
	Handled bool   // true if this was a slash command (even if it errored)
	Reprint string // optional message already printed by the command
}

// slashCommand is one /name entry.
type slashCommand struct {
	Name    string
	Aliases []string
	Args    string // shown in /help, e.g. "[name]"
	Summary string
	Run     func(ctx context.Context, args string, s *replState, out io.Writer) slashResult
}

var slashCommands []slashCommand

func init() {
	slashCommands = []slashCommand{
		{Name: "help", Aliases: []string{"?"}, Summary: "show slash commands", Run: slashHelp},
		{Name: "exit", Aliases: []string{"quit", "q"}, Summary: "leave interactive mode", Run: slashExit},
		{Name: "clear", Summary: "reset the conversation (keep MCP/skills)", Run: slashClear},
		{Name: "compact", Summary: "force context compaction now", Run: slashCompact},
		{Name: "model", Args: "[name]", Summary: "show or set the model for this session", Run: slashModel},
		{Name: "budget", Args: "[tokens]", Summary: "show or set AGENT_CONTEXT_BUDGET for this session", Run: slashBudget},
		{Name: "mcp", Summary: "list connected MCP servers and their tools", Run: slashMCP},
		{Name: "skills", Summary: "list available skills", Run: slashSkills},
		{Name: "status", Summary: "tokens, model, cwd, servers, skills", Run: slashStatus},
		{Name: "doctor", Summary: "run the same setup checks as `mini-agent doctor`", Run: slashDoctor},
	}
}

// replState is the mutable session the slash commands can read and change.
type replState struct {
	Msgs        []Message
	LastRequest string
	Turns       int
	CancelTurn  context.CancelFunc // cancels the in-flight turn, if any
}

// parseSlash splits "/cmd args" into (name, args). ok is false if s is not a slash command.
func parseSlash(s string) (name, args string, ok bool) {
	s = strings.TrimSpace(s)
	if s == "" || s[0] != '/' {
		return "", "", false
	}
	body := strings.TrimSpace(s[1:])
	if body == "" {
		return "", "", true // bare "/"
	}
	name, rest, _ := strings.Cut(body, " ")
	return strings.ToLower(name), strings.TrimSpace(rest), true
}

// findSlash looks up a command by name or alias.
func findSlash(name string) *slashCommand {
	for i := range slashCommands {
		c := &slashCommands[i]
		if c.Name == name {
			return c
		}
		for _, a := range c.Aliases {
			if a == name {
				return c
			}
		}
	}
	return nil
}

// dispatchSlash runs a slash command. If s is not a slash line, Handled is false.
func dispatchSlash(ctx context.Context, line string, st *replState, out io.Writer) slashResult {
	name, args, ok := parseSlash(line)
	if !ok {
		return slashResult{}
	}
	if name == "" {
		fmt.Fprintln(out, color("dim", "Type /help for slash commands."))
		return slashResult{Handled: true}
	}
	cmd := findSlash(name)
	if cmd == nil {
		fmt.Fprintf(out, "%s Unknown command /%s. Type /help for the list.\n", color("warn", "!"), name)
		// Suggest close matches.
		if sug := suggestSlash(name); len(sug) > 0 {
			fmt.Fprintf(out, "  Did you mean: %s\n", strings.Join(sug, ", "))
		}
		return slashResult{Handled: true}
	}
	return cmd.Run(ctx, args, st, out)
}

func suggestSlash(name string) []string {
	var out []string
	for _, c := range slashCommands {
		if strings.HasPrefix(c.Name, name) || strings.Contains(c.Name, name) {
			out = append(out, "/"+c.Name)
		}
		for _, a := range c.Aliases {
			if strings.HasPrefix(a, name) {
				out = append(out, "/"+a)
			}
		}
	}
	sort.Strings(out)
	if len(out) > 5 {
		out = out[:5]
	}
	return out
}

// slashCompletions returns the /names that start with prefix (including the leading /).
func slashCompletions(line string) []string {
	line = strings.TrimLeft(line, " \t")
	if !strings.HasPrefix(line, "/") {
		return nil
	}
	prefix := strings.ToLower(line[1:])
	if prefix == "" {
		// bare "/" — offer every command
	} else if strings.Contains(prefix, " ") {
		return nil // already typing args; don't complete the name
	}
	var out []string
	seen := map[string]bool{}
	for _, c := range slashCommands {
		names := append([]string{c.Name}, c.Aliases...)
		for _, n := range names {
			if strings.HasPrefix(n, prefix) && !seen[n] {
				seen[n] = true
				out = append(out, "/"+n)
			}
		}
	}
	sort.Strings(out)
	return out
}

func slashHelp(_ context.Context, _ string, _ *replState, out io.Writer) slashResult {
	fmt.Fprint(out, interactiveHelp)
	return slashResult{Handled: true}
}

func slashExit(_ context.Context, _ string, _ *replState, _ io.Writer) slashResult {
	return slashResult{Handled: true, Quit: true}
}

func slashClear(_ context.Context, _ string, st *replState, out io.Writer) slashResult {
	st.Msgs = nil
	st.LastRequest = ""
	st.Turns = 0
	loadedSkills = map[string]bool{}
	totalIn, totalOut, lastPrompt, sinceCompact = 0, 0, 0, minGap
	fmt.Fprintln(out, color("dim", "Conversation cleared."))
	return slashResult{Handled: true, Clear: true}
}

func slashCompact(_ context.Context, _ string, st *replState, out io.Writer) slashResult {
	if len(st.Msgs) < 4 {
		fmt.Fprintln(out, color("dim", "Nothing to compact yet."))
		return slashResult{Handled: true}
	}
	return slashResult{Handled: true, Compact: true}
}

func slashModel(_ context.Context, args string, _ *replState, out io.Writer) slashResult {
	if args == "" {
		fmt.Fprintf(out, "model: %s\n", client.Model)
		return slashResult{Handled: true}
	}
	client.Model = args
	model = args
	fmt.Fprintf(out, "model set to %s (this session only)\n", client.Model)
	return slashResult{Handled: true}
}

func slashBudget(_ context.Context, args string, _ *replState, out io.Writer) slashResult {
	if args == "" {
		fmt.Fprintf(out, "context budget: %d input tokens\n", contextBudget)
		return slashResult{Handled: true}
	}
	n, err := strconv.Atoi(args)
	if err != nil || n <= 0 {
		fmt.Fprintln(out, color("warn", "usage: /budget <positive integer>"))
		return slashResult{Handled: true}
	}
	contextBudget = n
	fmt.Fprintf(out, "context budget set to %d (this session only)\n", contextBudget)
	return slashResult{Handled: true}
}

func slashMCP(_ context.Context, _ string, _ *replState, out io.Writer) slashResult {
	if st := MCPStatus(); st == "connecting" {
		fmt.Fprintln(out, "MCP still connecting…")
		return slashResult{Handled: true}
	}
	if mcpSessionCount() == 0 {
		fmt.Fprintln(out, "No MCP servers connected. Add some with: mini-agent mcp add ...")
		return slashResult{Handled: true}
	}
	// Group tools by server (snapshot under lock).
	mcpMu.Lock()
	by := map[string][]string{}
	sessions := make([]string, 0, len(hub.sessions))
	for full, t := range hub.tools {
		by[t.server] = append(by[t.server], full)
	}
	for name := range hub.sessions {
		sessions = append(sessions, name)
	}
	mcpMu.Unlock()
	sort.Strings(sessions)
	for _, name := range sessions {
		tools := by[name]
		sort.Strings(tools)
		fmt.Fprintf(out, "%s (%d tools)\n", color("accent", name), len(tools))
		for _, t := range tools {
			fmt.Fprintf(out, "  %s\n", t)
		}
	}
	return slashResult{Handled: true}
}

func slashSkills(_ context.Context, _ string, _ *replState, out io.Writer) slashResult {
	if len(availableSkills) == 0 {
		fmt.Fprintln(out, "No skills available. Add one with: mini-agent skill add ...")
		return slashResult{Handled: true}
	}
	for _, s := range availableSkills {
		mark := ""
		if loadedSkills[s.Name] {
			mark = color("ok", " (loaded)")
		}
		fmt.Fprintf(out, "%s%s [%s]\n    %s\n", color("accent", s.Name), mark, s.Scope, firstLine(s.Description, 100))
	}
	return slashResult{Handled: true}
}

func slashStatus(_ context.Context, _ string, st *replState, out io.Writer) slashResult {
	cwd, _ := os.Getwd()
	fmt.Fprintf(out, "model:    %s\n", client.Model)
	fmt.Fprintf(out, "base URL: %s\n", client.BaseURL)
	fmt.Fprintf(out, "cwd:      %s\n", cwd)
	fmt.Fprintf(out, "budget:   %d tokens (last prompt %d)\n", contextBudget, lastPrompt)
	fmt.Fprintf(out, "tokens:   in=%d out=%d (session totals)\n", totalIn, totalOut)
	fmt.Fprintf(out, "turns:    %d   messages: %d\n", st.Turns, len(st.Msgs))
	nMCP := mcpSessionCount()
	mcpLine := fmt.Sprintf("%d server(s)", nMCP)
	if MCPStatus() == "connecting" {
		mcpLine = "connecting…"
	}
	fmt.Fprintf(out, "mcp:      %s\n", mcpLine)
	fmt.Fprintf(out, "skills:   %d available", len(availableSkills))
	if n := len(loadedSkills); n > 0 {
		fmt.Fprintf(out, ", %d loaded", n)
	}
	fmt.Fprintln(out)
	return slashResult{Handled: true}
}

func slashDoctor(ctx context.Context, _ string, _ *replState, out io.Writer) slashResult {
	_ = doctor(out)
	return slashResult{Handled: true}
}

const interactiveHelp = `Interactive mode (full-screen TUI on a TTY; plain prompt in pipes).

Slash commands:
  /help              Show this help
  /exit  /quit  /q   Leave interactive mode
  /clear             Reset the conversation (MCP & skills stay)
  /compact           Force context compaction now
  /model [name]      Show or set the model for this session
  /budget [tokens]   Show or set the context budget for this session
  /mcp               List connected MCP servers and tools
  /skills            List available skills
  /status            Tokens, model, cwd, servers, skills
  /doctor            Same checks as ` + "`mini-agent doctor`" + `

Keys (TUI):
  Enter              Send
  Ctrl-J             Newline in the input box
  Esc                Cancel the in-flight turn
  Ctrl-C             Cancel turn; on idle, quit
  Ctrl-D             Quit (empty input)
  Up/Down            Input history
  Tab                Complete /commands
  PgUp/PgDn / mouse  Scroll the transcript

Answers stream into the assistant bubble as tokens arrive (Anthropic-compatible
SSE). Tool calls appear as discrete lines. A spinner shows until the first token.
Non-TTY / pipes use the plain prompt (no alt screen). Set NO_COLOR=1 to disable colors.
`

// color wraps s in ANSI codes unless NO_COLOR is set or stdout isn't a TTY-ish env.
func color(kind, s string) string {
	if os.Getenv("NO_COLOR") != "" || os.Getenv("TERM") == "dumb" {
		return s
	}
	var code string
	switch kind {
	case "dim":
		code = "90"
	case "warn":
		code = "33"
	case "ok":
		code = "32"
	case "accent":
		code = "36"
	case "user":
		code = "35"
	case "assistant":
		code = "37"
	case "tool":
		code = "34"
	default:
		return s
	}
	return "\033[" + code + "m" + s + "\033[0m"
}
