package main

// Interactive mode: a productive REPL with history, slash commands, a
// thinking spinner, and Ctrl-C that cancels only the current turn.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/chzyer/readline"
	"golang.org/x/term"
)

// interactiveMode is set while the REPL runs so the agent loop can show a spinner.
var interactiveMode bool
var isTUI bool // full-screen bubbletea; suppresses duplicate stdout prints

// repl runs the interactive session until /exit, Ctrl-D, or two Ctrl-C on an empty prompt.
// On a TTY it opens the full-screen bubbletea UI; otherwise it falls back to the
// readline REPL, then to a plain scanner for pipes.
func repl(parent context.Context) {
	if term.IsTerminal(int(os.Stdout.Fd())) && term.IsTerminal(int(os.Stdin.Fd())) {
		runTUI(parent)
		return
	}
	readlineRepl(parent)
}

func readlineRepl(parent context.Context) {
	interactiveMode = true
	defer func() { interactiveMode = false }()

	historyFile := ""
	if home, err := os.UserHomeDir(); err == nil {
		dir := filepath.Join(home, ".mini-agent")
		_ = os.MkdirAll(dir, 0o700)
		historyFile = filepath.Join(dir, "history")
	}

	cfg := &readline.Config{
		Prompt:              color("accent", "❯ "),
		HistoryFile:         historyFile,
		HistoryLimit:        1000,
		InterruptPrompt:     "^C",
		EOFPrompt:           "exit",
		HistorySearchFold:   true,
		AutoComplete:        &slashCompleter{},
		FuncFilterInputRune: filterInput,
	}
	rl, err := readline.NewEx(cfg)
	if err != nil {
		// Non-TTY fallback: keep the simple scanner REPL working in pipes.
		fmt.Fprintln(os.Stderr, color("dim", "readline unavailable ("+err.Error()+"); using plain input"))
		plainRepl(parent)
		return
	}
	defer rl.Close()

	fmt.Fprintln(os.Stderr, color("dim", "mini-agent interactive — /help for commands, Ctrl-C cancels a turn, /exit to quit"))
	if client.Model != "" {
		fmt.Fprintf(os.Stderr, "%s model %s · budget %d · %d skill(s)\n",
			color("dim", "·"), client.Model, contextBudget, len(availableSkills))
	}

	st := &replState{}
	var interruptStreak int
	for {
		if parent.Err() != nil {
			return
		}
		line, err := readMultiline(rl)
		if err != nil {
			if errors.Is(err, readline.ErrInterrupt) {
				interruptStreak++
				if interruptStreak >= 2 {
					fmt.Fprintln(os.Stderr, color("dim", "Bye."))
					return
				}
				fmt.Fprintln(os.Stderr, color("dim", "(Ctrl-C again or /exit to quit)"))
				continue
			}
			if errors.Is(err, io.EOF) {
				fmt.Fprintln(os.Stderr, color("dim", "Bye."))
				return
			}
			fmt.Fprintln(os.Stderr, "read:", err)
			return
		}
		interruptStreak = 0
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		res := dispatchSlash(parent, line, st, os.Stderr)
		if res.Handled {
			if res.Quit {
				fmt.Fprintln(os.Stderr, color("dim", "Bye."))
				return
			}
			if res.Compact {
				turnCtx, cancel := context.WithCancel(parent)
				fmt.Fprintln(os.Stderr, color("dim", "Compacting…"))
				before := len(st.Msgs)
				compacted, err := compact(turnCtx, st.Msgs, st.LastRequest)
				cancel()
				if err != nil {
					fmt.Fprintln(os.Stderr, color("warn", "compact failed: "+err.Error()))
				} else if len(compacted) < before {
					st.Msgs = compacted
					fmt.Fprintf(os.Stderr, "%s %d → %d messages\n", color("ok", "compacted"), before, len(st.Msgs))
				} else {
					fmt.Fprintln(os.Stderr, color("dim", "History too short to compact."))
				}
			}
			continue
		}

		// Run one agent turn with its own cancel so Ctrl-C only aborts this turn.
		turnCtx, cancel := context.WithCancel(parent)
		st.CancelTurn = cancel
		stopSig := bindInterrupt(cancel)
		st.Msgs = addUserText(st.Msgs, line)
		st.LastRequest = line
		st.Turns++
		fmt.Fprintf(os.Stderr, "%s %s\n", color("user", "you"), firstLine(line, 80))
		updated, err := runAgent(turnCtx, st.Msgs, line)
		stopSig()
		st.CancelTurn = nil
		cancel()
		if updated != nil {
			st.Msgs = updated
		}
		switch {
		case errors.Is(err, context.Canceled):
			fmt.Fprintln(os.Stderr, color("warn", "turn cancelled"))
		case errors.Is(err, errStepLimit):
			fmt.Fprintln(os.Stderr, color("warn", err.Error()))
		case err != nil:
			fmt.Fprintln(os.Stderr, color("warn", "error: "+err.Error()))
		}
	}
}

// readMultiline reads one prompt. A trailing \ continues on the next line.
func readMultiline(rl *readline.Instance) (string, error) {
	var parts []string
	for {
		if len(parts) == 0 {
			rl.SetPrompt(color("accent", "❯ "))
		} else {
			rl.SetPrompt(color("dim", "… "))
		}
		line, err := rl.Readline()
		if err != nil {
			return "", err
		}
		if strings.HasSuffix(line, "\\") && !strings.HasSuffix(line, "\\\\") {
			parts = append(parts, strings.TrimSuffix(line, "\\"))
			continue
		}
		parts = append(parts, line)
		return strings.Join(parts, "\n"), nil
	}
}

// bindInterrupt cancels the turn on SIGINT without killing the process.
// Returns a stop func that restores default handling.
func bindInterrupt(cancel context.CancelFunc) (stop func()) {
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, os.Interrupt)
	done := make(chan struct{})
	go func() {
		select {
		case <-ch:
			cancel()
		case <-done:
		}
	}()
	return func() {
		signal.Stop(ch)
		close(done)
	}
}

// filterInput blocks some control characters from being inserted as text.
func filterInput(r rune) (rune, bool) {
	switch r {
	case readline.CharCtrlZ:
		return r, false
	}
	return r, true
}

// slashCompleter tabs /commands.
type slashCompleter struct{}

func (c *slashCompleter) Do(line []rune, pos int) (newLine [][]rune, length int) {
	prefix := string(line[:pos])
	// Only complete when the line so far is a slash command with no space.
	trimmed := strings.TrimLeft(prefix, " \t")
	if !strings.HasPrefix(trimmed, "/") || strings.Contains(strings.TrimPrefix(trimmed, "/"), " ") {
		return nil, 0
	}
	cands := slashCompletions(trimmed)
	if len(cands) == 0 {
		return nil, 0
	}
	// readline wants suffixes of the shared prefix; length is how many runes to replace.
	out := make([][]rune, 0, len(cands))
	for _, s := range cands {
		out = append(out, []rune(s[len(trimmed):])) // e.g. "/ex" + "it" -> "/exit"
	}
	return out, len([]rune(trimmed))
}

// plainRepl is used when stdin isn't a TTY (tests, pipes).
func plainRepl(parent context.Context) {
	st := &replState{}
	buf := make([]byte, 0, 4096)
	tmp := make([]byte, 4096)
	printPlainPrompt := func() { fmt.Fprint(os.Stderr, "❯ ") }
	printPlainPrompt()
	for {
		n, err := os.Stdin.Read(tmp)
		if n > 0 {
			buf = append(buf, tmp[:n]...)
		}
		for {
			i := strings.IndexByte(string(buf), '\n')
			if i < 0 {
				break
			}
			line := strings.TrimSpace(string(buf[:i]))
			buf = buf[i+1:]
			if line == "" {
				printPlainPrompt()
				continue
			}
			res := dispatchSlash(parent, line, st, os.Stderr)
			if res.Handled {
				if res.Quit {
					return
				}
				if res.Compact && len(st.Msgs) >= 4 {
					st.Msgs, _ = compact(parent, st.Msgs, st.LastRequest)
				}
				printPlainPrompt()
				continue
			}
			turnCtx, cancel := context.WithCancel(parent)
			st.Msgs = addUserText(st.Msgs, line)
			st.LastRequest = line
			st.Turns++
			updated, runErr := runAgent(turnCtx, st.Msgs, line)
			cancel()
			if updated != nil {
				st.Msgs = updated
			}
			if runErr != nil && !errors.Is(runErr, errStepLimit) && !errors.Is(runErr, context.Canceled) {
				fmt.Fprintln(os.Stderr, "error:", runErr)
			}
			printPlainPrompt()
		}
		if err != nil {
			return
		}
	}
}

// thinkingSpinner prints a spinner on stderr until stop is called.
type thinkingSpinner struct {
	stop chan struct{}
	wg   sync.WaitGroup
}

func startThinking() *thinkingSpinner {
	if !interactiveMode || quiet || os.Getenv("NO_COLOR") != "" {
		return nil
	}
	s := &thinkingSpinner{stop: make(chan struct{})}
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		frames := []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}
		i := 0
		t := time.NewTicker(80 * time.Millisecond)
		defer t.Stop()
		for {
			select {
			case <-s.stop:
				fmt.Fprint(os.Stderr, "\r\033[K") // clear the spinner line
				return
			case <-t.C:
				fmt.Fprintf(os.Stderr, "\r%s %s", color("dim", frames[i%len(frames)]), color("dim", "thinking…"))
				i++
			}
		}
	}()
	return s
}

func (s *thinkingSpinner) Stop() {
	if s == nil {
		return
	}
	close(s.stop)
	s.wg.Wait()
}
