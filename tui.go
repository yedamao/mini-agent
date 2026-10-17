package main

// Full-screen interactive UI (bubbletea): scrollable transcript, live-streamed
// assistant bubbles, slash-command completion, status bar.

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/yedamao/mini-agent/internal/mcpconf"
	"golang.org/x/term"
)

// runTUI launches the full-screen bubbletea UI. Falls back to plainRepl if the
// terminal is not a TTY or bubbletea fails to start.
func runTUI(parent context.Context, servers map[string]mcpconf.Server) {
	if !term.IsTerminal(int(os.Stdout.Fd())) || !term.IsTerminal(int(os.Stdin.Fd())) {
		fmt.Fprintln(os.Stderr, color("dim", "non-TTY; using plain prompt"))
		startMCPBackground(parent, servers, nil)
		plainRepl(parent)
		return
	}
	isTUI = true
	defer func() { isTUI = false }()
	interactiveMode = true
	defer func() { interactiveMode = false }()

	m := newTUIModel(parent, servers)
	p := tea.NewProgram(m,
		tea.WithAltScreen(),
		tea.WithMouseCellMotion(),
	)
	m.prog = p
	if _, err := p.Run(); err != nil {
		fmt.Fprintln(os.Stderr, "tui:", err)
		plainRepl(parent)
	}
}

type chatRole string

const (
	roleUser      chatRole = "user"
	roleAssistant chatRole = "assistant"
	roleTool      chatRole = "tool"
	roleSystem    chatRole = "system"
)

type chatLine struct {
	Role chatRole
	Text string
}

type tuiModel struct {
	parent      context.Context
	st          *replState
	prog        *tea.Program
	servers     map[string]mcpconf.Server // connected in Init (background)
	benchFramed bool

	vp   viewport.Model
	ta   textarea.Model
	spin spinner.Model

	lines   []chatLine
	width   int
	height  int
	ready   bool
	busy    bool
	waiting bool // waiting for first token
	status  string

	history     []string
	histIdx     int // -1 = editing new line
	draft       string
	completions []string
	compIdx     int

	turnCancel context.CancelFunc
	mu         sync.Mutex

	quitOnce sync.Once
}

func newTUIModel(parent context.Context, servers map[string]mcpconf.Server) *tuiModel {
	ta := textarea.New()
	ta.Placeholder = "Ask anything…  /help  Enter send  Esc cancel  Ctrl-D quit"
	ta.Prompt = "❯ "
	ta.CharLimit = 0
	ta.SetHeight(3)
	ta.ShowLineNumbers = false
	ta.KeyMap.InsertNewline.SetEnabled(false) // Enter submits; Ctrl-J for newline
	ta.Focus()

	sp := spinner.New()
	sp.Spinner = spinner.Dot
	sp.Style = lipgloss.NewStyle().Foreground(lipgloss.Color("205"))

	status := "ready"
	if len(servers) > 0 {
		status = "mcp connecting…"
		setMCPStatus("connecting")
	} else {
		setMCPStatus("none")
	}
	m := &tuiModel{
		parent:  parent,
		st:      &replState{},
		servers: servers,
		ta:      ta,
		spin:    sp,
		histIdx: -1,
		status:  status,
		lines: []chatLine{
			{Role: roleSystem, Text: "mini-agent — /help for commands · Enter send · Esc cancel turn · Ctrl-C cancel / quit · Ctrl-D quit"},
		},
	}
	if client != nil && client.Model != "" {
		m.lines = append(m.lines, chatLine{Role: roleSystem, Text: fmt.Sprintf("model %s · budget %d · %d skill(s)",
			client.Model, contextBudget, len(availableSkills))})
	}
	return m
}

func (m *tuiModel) Init() tea.Cmd {
	return tea.Batch(textarea.Blink, m.spin.Tick, m.connectMCPCmd())
}

type mcpDoneMsg struct{ Text string }

func (m *tuiModel) connectMCPCmd() tea.Cmd {
	if len(m.servers) == 0 {
		setMCPStatus("none")
		return nil
	}
	servers := m.servers
	m.servers = nil
	parent := m.parent
	return func() tea.Msg {
		// Kick off MCP without blocking this Cmd (or the UI). Completion
		// arrives via Program.Send → mcpDoneMsg.
		prog := m.prog
		startMCPBackground(parent, servers, func(status string) {
			if prog != nil {
				prog.Send(mcpDoneMsg{Text: status})
			}
		})
		return mcpDoneMsg{Text: "mcp connecting…"}
	}
}

type textDeltaMsg string
type toolLineMsg string
type systemLineMsg string
type thinkingMsg struct{}
type turnDoneMsg struct {
	msgs []Message
	err  error
}
type compactDoneMsg struct {
	msgs   []Message
	err    error
	before int
}

func (m *tuiModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmds []tea.Cmd

	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		if !benchLaunchStart.IsZero() && !m.benchFramed {
			m.benchFramed = true
			ms := time.Since(benchLaunchStart).Milliseconds()
			fmt.Fprintf(os.Stderr, "BENCH first_interactive_frame_ms=%d mcp_status=%s\n", ms, MCPStatus())
			return m, tea.Quit
		}
		headerH := 1
		statusH := 1
		inputH := m.ta.Height() + 1
		vpH := m.height - headerH - statusH - inputH
		if vpH < 3 {
			vpH = 3
		}
		if !m.ready {
			m.vp = viewport.New(m.width, vpH)
			m.ready = true
		} else {
			m.vp.Width = m.width
			m.vp.Height = vpH
		}
		m.ta.SetWidth(m.width - 2)
		m.refreshViewport()
		return m, nil

	case tea.KeyMsg:
		switch msg.String() {
		case "ctrl+c":
			if m.busy && m.turnCancel != nil {
				m.turnCancel()
				m.appendLine(roleSystem, "turn cancelled")
				m.busy = false
				m.waiting = false
				m.status = "cancelled"
				m.refreshViewport()
				return m, nil
			}
			return m, tea.Quit
		case "ctrl+d":
			if m.ta.Value() == "" {
				return m, tea.Quit
			}
		case "esc":
			if m.busy && m.turnCancel != nil {
				m.turnCancel()
				m.appendLine(roleSystem, "turn cancelled")
				m.busy = false
				m.waiting = false
				m.status = "cancelled"
				m.refreshViewport()
			}
			return m, nil
		case "enter":
			if m.busy {
				return m, nil
			}
			line := strings.TrimSpace(m.ta.Value())
			if line == "" {
				return m, nil
			}
			m.ta.Reset()
			m.history = append(m.history, line)
			m.histIdx = -1
			m.draft = ""
			m.completions = nil
			return m, m.submit(line)
		case "ctrl+j":
			// newline inside textarea
			m.ta.InsertString("\n")
			return m, nil
		case "up":
			if !m.busy {
				m.histUp()
				return m, nil
			}
		case "down":
			if !m.busy {
				m.histDown()
				return m, nil
			}
		case "tab":
			v := strings.TrimSpace(m.ta.Value())
			if v == "" {
				m.ta.SetValue("/")
				m.ta.CursorEnd()
				m.tabComplete()
				return m, nil
			}
			if strings.HasPrefix(v, "/") {
				m.tabComplete()
				return m, nil
			}
		case "pgup":
			m.vp.HalfViewUp()
			return m, nil
		case "pgdown":
			m.vp.HalfViewDown()
			return m, nil
		}

	case tea.MouseMsg:
		if m.ready {
			var cmd tea.Cmd
			m.vp, cmd = m.vp.Update(msg)
			cmds = append(cmds, cmd)
		}

	case spinner.TickMsg:
		if m.waiting {
			var cmd tea.Cmd
			m.spin, cmd = m.spin.Update(msg)
			cmds = append(cmds, cmd)
		}

	case thinkingMsg:
		m.waiting = true
		m.status = "thinking…"
		cmds = append(cmds, m.spin.Tick)

	case textDeltaMsg:
		m.waiting = false
		m.status = "streaming"
		m.appendAssistantDelta(string(msg))
		m.refreshViewport()

	case toolLineMsg:
		m.waiting = false
		m.appendLine(roleTool, string(msg))
		m.refreshViewport()

	case systemLineMsg:
		m.appendLine(roleSystem, string(msg))
		m.refreshViewport()

	case turnDoneMsg:
		m.busy = false
		m.waiting = false
		m.turnCancel = nil
		m.st.CancelTurn = nil
		if msg.msgs != nil {
			m.st.Msgs = msg.msgs
		}
		if msg.err != nil && !errors.Is(msg.err, context.Canceled) && !errors.Is(msg.err, errStepLimit) {
			m.appendLine(roleSystem, "error: "+msg.err.Error())
			m.status = "error"
		} else if errors.Is(msg.err, errStepLimit) {
			m.appendLine(roleSystem, fmt.Sprintf("stopped: hit the %d-step limit", maxSteps))
			m.status = "step limit"
		} else if errors.Is(msg.err, context.Canceled) {
			m.status = "cancelled"
		} else {
			m.status = "ready"
			m.st.Turns++
		}
		// Ensure assistant bubble ends cleanly
		m.refreshViewport()

	case mcpDoneMsg:
		m.status = msg.Text
		if strings.HasPrefix(msg.Text, "mcp ready") || msg.Text == "mcp=0" {
			// keep ready after a beat
		} else if msg.Text == "mcp failed" {
			m.appendLine(roleSystem, "MCP connect failed or timed out; built-in tools still work")
			m.refreshViewport()
		}
		return m, nil

	case compactDoneMsg:
		m.busy = false
		m.waiting = false
		if msg.err != nil {
			m.appendLine(roleSystem, "compact failed: "+msg.err.Error())
		} else if len(msg.msgs) < msg.before {
			m.st.Msgs = msg.msgs
			m.appendLine(roleSystem, fmt.Sprintf("compacted %d → %d messages", msg.before, len(msg.msgs)))
		} else {
			m.appendLine(roleSystem, "History too short to compact.")
		}
		m.status = "ready"
		m.refreshViewport()
	}

	var cmd tea.Cmd
	m.ta, cmd = m.ta.Update(msg)
	cmds = append(cmds, cmd)
	return m, tea.Batch(cmds...)
}

func (m *tuiModel) submit(line string) tea.Cmd {
	// Capture slash output into the transcript.
	var buf strings.Builder
	res := dispatchSlash(m.parent, line, m.st, &buf)
	if out := strings.TrimSpace(buf.String()); out != "" {
		m.appendLine(roleSystem, out)
	}
	if res.Handled {
		m.refreshViewport()
		if res.Quit {
			return tea.Quit
		}
		if res.Clear {
			m.lines = m.lines[:0]
			m.appendLine(roleSystem, "conversation cleared")
			m.refreshViewport()
		}
		if res.Compact {
			m.busy = true
			m.waiting = true
			m.status = "compacting…"
			before := len(m.st.Msgs)
			turnCtx, cancel := context.WithCancel(m.parent)
			m.turnCancel = cancel
			return func() tea.Msg {
				compacted, err := compact(turnCtx, m.st.Msgs, m.st.LastRequest)
				cancel()
				return compactDoneMsg{msgs: compacted, err: err, before: before}
			}
		}
		return nil
	}

	m.appendLine(roleUser, line)
	m.st.Msgs = addUserText(m.st.Msgs, line)
	m.st.LastRequest = line
	m.busy = true
	m.waiting = true
	m.status = "thinking…"
	m.refreshViewport()

	turnCtx, cancel := context.WithCancel(m.parent)
	m.turnCancel = cancel
	m.st.CancelTurn = cancel
	prog := m.prog
	msgs := m.st.Msgs
	req := line

	return tea.Batch(m.spin.Tick, func() tea.Msg {
		hooks := agentHooks{
			OnThinking: func() {
				if prog != nil {
					prog.Send(thinkingMsg{})
				}
			},
			OnText: func(t string) {
				if prog != nil {
					prog.Send(textDeltaMsg(t))
				}
			},
			OnTool: func(step int, name, in, out string) {
				if prog != nil {
					prog.Send(toolLineMsg(fmt.Sprintf("[step %d] %s %s → %s", step, name, in, out)))
				}
			},
		}
		newMsgs, err := runAgentStream(turnCtx, msgs, req, hooks)
		cancel()
		return turnDoneMsg{msgs: newMsgs, err: err}
	})
}

func (m *tuiModel) appendLine(role chatRole, text string) {
	m.lines = append(m.lines, chatLine{Role: role, Text: text})
}

func (m *tuiModel) appendAssistantDelta(delta string) {
	n := len(m.lines)
	if n > 0 && m.lines[n-1].Role == roleAssistant {
		m.lines[n-1].Text += delta
		return
	}
	m.lines = append(m.lines, chatLine{Role: roleAssistant, Text: delta})
}

func (m *tuiModel) histUp() {
	if len(m.history) == 0 {
		return
	}
	if m.histIdx < 0 {
		m.draft = m.ta.Value()
		m.histIdx = len(m.history) - 1
	} else if m.histIdx > 0 {
		m.histIdx--
	}
	m.ta.SetValue(m.history[m.histIdx])
	m.ta.CursorEnd()
}

func (m *tuiModel) histDown() {
	if m.histIdx < 0 {
		return
	}
	if m.histIdx >= len(m.history)-1 {
		m.histIdx = -1
		m.ta.SetValue(m.draft)
	} else {
		m.histIdx++
		m.ta.SetValue(m.history[m.histIdx])
	}
	m.ta.CursorEnd()
}

func (m *tuiModel) tabComplete() {
	cur := m.ta.Value()
	cands := slashCompletions(cur)
	if len(cands) == 0 {
		return
	}
	if len(m.completions) == 0 || !sameStrings(m.completions, cands) {
		m.completions = cands
		m.compIdx = 0
	} else {
		m.compIdx = (m.compIdx + 1) % len(m.completions)
	}
	m.ta.SetValue(m.completions[m.compIdx] + " ")
	m.ta.CursorEnd()
}

func sameStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func (m *tuiModel) refreshViewport() {
	if !m.ready {
		return
	}
	var b strings.Builder
	for _, ln := range m.lines {
		b.WriteString(m.renderLine(ln))
		b.WriteByte('\n')
	}
	if m.waiting {
		b.WriteString(m.spin.View() + " " + styleDim("waiting for first token…") + "\n")
	}
	m.vp.SetContent(b.String())
	m.vp.GotoBottom()
}

func (m *tuiModel) renderLine(ln chatLine) string {
	w := m.width
	if w < 20 {
		w = 80
	}
	switch ln.Role {
	case roleUser:
		return styleUser("You") + "\n" + wrapText(ln.Text, w-2)
	case roleAssistant:
		return styleAssistant("Assistant") + "\n" + wrapText(ln.Text, w-2)
	case roleTool:
		return styleTool("⚙ " + ln.Text)
	default:
		return styleDim("· " + ln.Text)
	}
}

func (m *tuiModel) View() string {
	if !m.ready {
		return "…"
	}
	header := styleHeader(fmt.Sprintf(" mini-agent · %s ", shortModel()))
	status := m.statusBar()
	return lipgloss.JoinVertical(lipgloss.Left,
		header,
		m.vp.View(),
		status,
		m.ta.View(),
	)
}

func (m *tuiModel) statusBar() string {
	cwd, _ := os.Getwd()
	cwd = shortPath(cwd)
	mcpN := mcpSessionCount()
	mcpLabel := fmt.Sprintf("mcp=%d", mcpN)
	if st := MCPStatus(); st == "connecting" {
		mcpLabel = "mcp=connecting…"
	}
	tok := fmt.Sprintf("in=%d out=%d", totalIn, totalOut)
	parts := []string{
		shortModel(),
		tok,
		cwd,
		mcpLabel,
		fmt.Sprintf("skills=%d", len(availableSkills)),
		m.status,
	}
	line := " " + strings.Join(parts, " · ") + " "
	style := lipgloss.NewStyle().
		Foreground(lipgloss.Color("230")).
		Background(lipgloss.Color("236")).
		Width(m.width)
	if noColor() {
		return line
	}
	return style.Render(clipStr(line, m.width))
}

func shortModel() string {
	if client == nil || client.Model == "" {
		return "?"
	}
	return client.Model
}

func shortPath(p string) string {
	if home, err := os.UserHomeDir(); err == nil && strings.HasPrefix(p, home) {
		return "~" + p[len(home):]
	}
	if len(p) > 40 {
		return "…" + p[len(p)-39:]
	}
	return p
}

func wrapText(s string, width int) string {
	if width < 10 {
		return s
	}
	var out strings.Builder
	for _, paragraph := range strings.Split(s, "\n") {
		for len(paragraph) > width {
			out.WriteString(paragraph[:width])
			out.WriteByte('\n')
			paragraph = paragraph[width:]
		}
		out.WriteString(paragraph)
		out.WriteByte('\n')
	}
	return strings.TrimRight(out.String(), "\n")
}

func clipStr(s string, n int) string {
	if n <= 0 || len(s) <= n {
		return s
	}
	if n <= 1 {
		return s[:n]
	}
	return s[:n-1] + "…"
}

func noColor() bool {
	return os.Getenv("NO_COLOR") != "" || os.Getenv("TERM") == "dumb"
}

// Style helpers respect NO_COLOR via lipgloss when the env is set (lipgloss
// already checks NO_COLOR / TERM=dumb through termenv).
func styleUser(s string) string {
	if noColor() {
		return s
	}
	return lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("39")).Render(s)
}
func styleAssistant(s string) string {
	if noColor() {
		return s
	}
	return lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("213")).Render(s)
}
func styleTool(s string) string {
	if noColor() {
		return s
	}
	return lipgloss.NewStyle().Foreground(lipgloss.Color("178")).Render(s)
}
func styleDim(s string) string {
	if noColor() {
		return s
	}
	return lipgloss.NewStyle().Foreground(lipgloss.Color("245")).Render(s)
}
func styleHeader(s string) string {
	if noColor() {
		return s
	}
	return lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("230")).Background(lipgloss.Color("63")).Render(s)
}
