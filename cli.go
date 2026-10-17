package main

// CLI plumbing: root flags, help, doctor, and a small flag helpers that
// make -h/--help exit 0 and turn unknown flags into a short error + "see -h".

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"runtime"

	"github.com/yedamao/mini-agent/internal/mcpconf"
	"github.com/yedamao/mini-agent/internal/skills"
)

// version is replaced at build time with -ldflags "-X main.version=..." if wanted.
var version = "0.3.0"

// quiet suppresses per-step tool and token logs (answers still go to stdout).
var quiet bool

const rootUsage = `mini-agent is a from-scratch coding agent in Go.

Usage:
  mini-agent                         Interactive TUI (default; needs an API key)
  mini-agent [flags] <prompt...>     Run one task (needs an API key)
  mini-agent [flags] -i              Interactive TUI (same as bare mini-agent)
  mini-agent mcp <subcommand> ...    Manage MCP servers (no API key)
  mini-agent skill <subcommand> ...  Manage Agent Skills (no API key)
  mini-agent doctor                  Check the local setup (no API key)
  mini-agent eval [path]             Run eval cases (needs an API key)
  mini-agent version                 Print the version

Aliases: m = mcp, s = skill, skills = skill, e = eval

Flags (before the prompt or subcommand):
  -h, --help       Show this help and exit
  -v, --version    Print the version and exit
  -i               Interactive full-screen TUI (bubbletea); plain prompt if not a TTY
                   Default when no prompt/subcommand is given. mini-agent -i -h lists keys
  -q, --quiet      Less step noise on stderr (answers still on stdout)
  -C <dir>         Run as if started in <dir> (like git -C)
  --               End of flags; the rest is the prompt (even if it starts with -)

Environment:
  AGENT_API_KEY / DEEPSEEK_API_KEY   API key (required for runs and -i)
  AGENT_BASE_URL                     Default https://api.deepseek.com/anthropic
  AGENT_MODEL                        Default deepseek-chat
  AGENT_CALL_TIMEOUT                 Per-call timeout in seconds (default 120)
  AGENT_MAX_ATTEMPTS                 Retries for transient API errors (default 5)
  AGENT_CONTEXT_BUDGET               Input-token budget before compaction (default 50000)
  AGENT_MCP                          Extra stdio MCP server (path to a command)
  AGENT_MCP_CONFIG                   MCP config file (default ~/.mini-agent/mcp.json)
  NO_COLOR                           Disable ANSI / lipgloss colors

Examples:
  mini-agent                       # interactive (default)
  mini-agent "what time is it?"    # one-shot
  mini-agent -C examples/buggy "fix the failing tests"
  mini-agent -q -- "-n means dry-run; explain that flag"
  mini-agent -i                    # same as bare mini-agent
  mini-agent -i -h                 # slash commands + TUI keys
  mini-agent -h                    # this usage
  mini-agent mcp add deepwiki https://mcp.deepwiki.com/mcp
  mini-agent skill add examples/skills/go-test-fixer
  mini-agent doctor
  mini-agent eval                 # run evals/cases
  mini-agent eval evals/cases/01_get_time.yaml
`

const mcpUsage = `Manage MCP servers (config: %s). Needs no API key.

Usage:
  mini-agent mcp add [-t http|sse|stdio] [-H "K: V"]... [-e K=V]... <name> <url | command [args...]>
  mini-agent mcp list
  mini-agent mcp get <name>
  mini-agent mcp remove <name>

Aliases: m = mcp; ls = list; rm = remove

Flags for add:
  -t, --transport   stdio, http (default for URLs) or sse
  -H, --header      Header for remote servers; quote as 'Authorization: Bearer ${TOKEN}'
  -e, --env         Env var for stdio servers (KEY=value)
  -h, --help        Show this help

Examples:
  mini-agent mcp add local ~/.local/bin/mini-mcp
  mini-agent mcp add deepwiki https://mcp.deepwiki.com/mcp
  mini-agent mcp add -H 'Authorization: Bearer ${MY_TOKEN}' work https://example.com/mcp
  mini-agent mcp list
`

const skillUsage = `Manage Agent Skills (https://agentskills.io). Needs no API key.

Usage:
  mini-agent skill add <path|git-url> [--project] [--force]
  mini-agent skill list
  mini-agent skill show <name>
  mini-agent skill remove <name> [--project]
  mini-agent skill new <name> [--project]

Aliases: s / skills = skill; ls = list; rm = remove

Flags:
  --project   Use ./.mini-agent/skills (project) instead of ~/.mini-agent/skills (user)
  --force     Replace an installed skill when adding
  -h, --help  Show this help

Skills are also discovered from ~/.claude/skills and ./.claude/skills.
Project scope overrides user scope when two skills share a name.

Examples:
  mini-agent skill add examples/skills/go-test-fixer
  mini-agent skill add https://github.com/anthropics/skills
  mini-agent skill new my-helper --project
  mini-agent skill list
`

// errHelp is returned when the user asked for help; the caller exits 0.
var errHelp = errors.New("help")

// errExit is a CLI error that should exit with Code (default 1).
type errExit struct {
	Msg  string
	Code int
}

func (e errExit) Error() string { return e.Msg }

func exitCode(err error) int {
	var e errExit
	if errors.As(err, &e) {
		if e.Code != 0 {
			return e.Code
		}
		return 1
	}
	return 1
}

// newFlagSet builds a FlagSet that returns errHelp on -h/--help and a short
// errExit (pointing at -h) on unknown flags.
func newFlagSet(name string) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.Usage = func() {}
	return fs
}

// parseFlags runs fs.Parse and maps flag.ErrHelp / unknown-flag errors.
func parseFlags(fs *flag.FlagSet, args []string) error {
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return errHelp
		}
		return errExit{Msg: err.Error() + "\nSee -h for usage."}
	}
	return nil
}

// boolFlag registers -name and, when long is non-empty, --long as well.
// Go's flag package accepts -long for a flag named "long"; we register both
// short and long names so -h and --help both work.
func boolFlag(fs *flag.FlagSet, p *bool, short, long, usage string) {
	fs.BoolVar(p, short, false, usage)
	if long != "" && long != short {
		fs.BoolVar(p, long, false, usage)
	}
}

// stringFlag is like boolFlag for a string.
func stringFlag(fs *flag.FlagSet, p *string, short, long, def, usage string) {
	fs.StringVar(p, short, def, usage)
	if long != "" && long != short {
		fs.StringVar(p, long, def, usage)
	}
}

// splitDashDash returns (before, after) around the first "--". after is nil
// when there is no "--".
func splitDashDash(args []string) (before, after []string) {
	for i, a := range args {
		if a == "--" {
			return args[:i], args[i+1:]
		}
	}
	return args, nil
}

// rootOptions holds the flags parsed before the prompt or subcommand.
type rootOptions struct {
	Help, Version, Interactive, Quiet bool
	Dir                               string
	Args                              []string // remaining: subcommand or prompt
}

// parseRoot parses global flags. It stops at the first non-flag or after "--".
func parseRoot(args []string) (rootOptions, error) {
	var o rootOptions
	before, after := splitDashDash(args)
	fs := newFlagSet("mini-agent")
	boolFlag(fs, &o.Help, "h", "help", "show help")
	boolFlag(fs, &o.Version, "v", "version", "print version")
	boolFlag(fs, &o.Interactive, "i", "", "interactive REPL")
	boolFlag(fs, &o.Quiet, "q", "quiet", "less step noise")
	stringFlag(fs, &o.Dir, "C", "", "", "run as if started in this directory")
	if err := parseFlags(fs, before); err != nil {
		return o, err
	}
	o.Args = append(fs.Args(), after...)
	if o.Help {
		return o, errHelp
	}
	return o, nil
}

// doctor prints a one-line check of the local setup. It never prints secrets.
func doctor(out io.Writer) error {
	var ok, warn int
	check := func(label string, good bool, detail string) {
		mark := "✔"
		if !good {
			mark = "✘"
			warn++
		} else {
			ok++
		}
		fmt.Fprintf(out, "%s %s: %s\n", mark, label, detail)
	}

	keySrc := "not set"
	switch {
	case os.Getenv("AGENT_API_KEY") != "":
		keySrc = "AGENT_API_KEY is set"
	case os.Getenv("DEEPSEEK_API_KEY") != "":
		keySrc = "DEEPSEEK_API_KEY is set"
	}
	check("API key", keySrc != "not set", keySrc)

	cfgPath := mcpconf.DefaultPath()
	if st, err := os.Stat(cfgPath); err == nil {
		cfg, err := mcpconf.Load(cfgPath)
		if err != nil {
			check("MCP config", false, cfgPath+" ("+err.Error()+")")
		} else {
			check("MCP config", true, fmt.Sprintf("%s (%d server(s))", cfgPath, len(cfg.MCPServers)))
			_ = st
		}
	} else {
		check("MCP config", true, cfgPath+" (missing; using defaults / AGENT_MCP)")
	}

	home, _ := os.UserHomeDir()
	cwd, _ := os.Getwd()
	found, warnings := skills.Discover(skills.Roots(home, cwd))
	detail := fmt.Sprintf("%d skill(s)", len(found))
	if len(warnings) > 0 {
		detail += fmt.Sprintf(", %d invalid skipped", len(warnings))
	}
	check("Skills", true, detail)

	check("Go runtime", true, fmt.Sprintf("%s %s/%s", runtime.Version(), runtime.GOOS, runtime.GOARCH))

	fmt.Fprintf(out, "\nworking directory: %s\n", cwd)
	if warn == 0 {
		fmt.Fprintf(out, "all checks passed (%d)\n", ok)
	} else {
		fmt.Fprintf(out, "%d ok, %d need attention\n", ok, warn)
	}
	return nil
}
