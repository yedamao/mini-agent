package main

// `mini-agent mcp ...` manages the MCP config, like `claude mcp`:
//
//	mini-agent mcp add [-t http|sse|stdio] [-H "Name: value"]... [-e KEY=value]... <name> <url | command [args...]>
//	mini-agent mcp list      # shows each server and checks that it connects
//	mini-agent mcp get <name>
//	mini-agent mcp remove <name>

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/yedamao/mini-agent/internal/mcpconf"
)

// multiFlag collects a repeatable flag such as -H.
type multiFlag []string

func (m *multiFlag) String() string     { return strings.Join(*m, ", ") }
func (m *multiFlag) Set(v string) error { *m = append(*m, v); return nil }

func mcpCommand(ctx context.Context, args []string, out io.Writer) error {
	path := mcpconf.DefaultPath()
	if len(args) == 0 {
		return errors.New("usage: mini-agent mcp add|list|get|remove ... (config: " + path + ")")
	}
	cfg, err := mcpconf.Load(path)
	if err != nil {
		return err
	}
	switch args[0] {
	case "add":
		name, s, err := parseAdd(args[1:])
		if err != nil {
			return err
		}
		if _, exists := cfg.MCPServers[name]; exists {
			return fmt.Errorf("server %q already exists; remove it first", name)
		}
		cfg.MCPServers[name] = s
		if err := cfg.Save(path); err != nil {
			return err
		}
		fmt.Fprintf(out, "Added %s MCP server %s to %s\n", s.Transport(), name, path)
		return nil
	case "remove", "rm":
		if len(args) != 2 {
			return errors.New("usage: mini-agent mcp remove <name>")
		}
		if _, ok := cfg.MCPServers[args[1]]; !ok {
			return fmt.Errorf("no server named %q", args[1])
		}
		delete(cfg.MCPServers, args[1])
		if err := cfg.Save(path); err != nil {
			return err
		}
		fmt.Fprintf(out, "Removed %s from %s\n", args[1], path)
		return nil
	case "get":
		if len(args) != 2 {
			return errors.New("usage: mini-agent mcp get <name>")
		}
		s, ok := cfg.MCPServers[args[1]]
		if !ok {
			return fmt.Errorf("no server named %q", args[1])
		}
		b, _ := json.MarshalIndent(s, "", "  ") // unexpanded: ${VARS} stay as written
		fmt.Fprintf(out, "%s:\n%s\n", args[1], b)
		return nil
	case "list", "ls":
		if len(cfg.MCPServers) == 0 {
			fmt.Fprintf(out, "No MCP servers in %s\n", path)
			return nil
		}
		fmt.Fprintf(out, "Checking MCP servers in %s\n", path)
		for _, name := range sortedKeys(cfg.MCPServers) {
			s := cfg.MCPServers[name]
			session, tools, err := connectServer(ctx, name, s, 20*time.Second)
			status := fmt.Sprintf("✔ connected, %d tools", len(tools))
			if err != nil {
				status = "✘ " + err.Error()
			} else {
				session.Close()
			}
			fmt.Fprintf(out, "%s (%s): %s - %s\n", name, s.Transport(), target(s), status)
		}
		return nil
	}
	return fmt.Errorf("unknown mcp command %q", args[0])
}

// parseAdd turns `add` arguments into a server entry. A target starting with
// http:// or https:// is remote (Streamable HTTP unless -t sse); anything else
// is a local command with optional arguments.
func parseAdd(args []string) (string, mcpconf.Server, error) {
	fs := flag.NewFlagSet("mcp add", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	transport := fs.String("t", "", "transport: stdio, http or sse (default: from the target)")
	fs.StringVar(transport, "transport", "", "same as -t")
	var headers, envs multiFlag
	fs.Var(&headers, "H", `header for remote servers, e.g. -H "Authorization: Bearer ${TOKEN}"`)
	fs.Var(&headers, "header", "same as -H")
	fs.Var(&envs, "e", "environment variable for stdio servers, KEY=value")
	fs.Var(&envs, "env", "same as -e")
	usage := errors.New(`usage: mini-agent mcp add [-t http|sse|stdio] [-H "K: V"] [-e K=V] <name> <url | command [args...]>`)
	if err := fs.Parse(args); err != nil {
		return "", mcpconf.Server{}, fmt.Errorf("%v\n%w", err, usage)
	}
	rest := fs.Args()
	if len(rest) > 0 && rest[0] == "--" {
		rest = rest[1:]
	}
	if len(rest) < 2 {
		return "", mcpconf.Server{}, usage
	}
	name, target := rest[0], rest[1]
	if !mcpconf.ValidName(name) {
		return "", mcpconf.Server{}, fmt.Errorf("bad name %q: use letters, digits, - or _ (no __), up to 32 chars", name)
	}
	var s mcpconf.Server
	if strings.HasPrefix(target, "http://") || strings.HasPrefix(target, "https://") {
		s = mcpconf.Server{Type: "http", URL: target}
		if *transport == "sse" {
			s.Type = "sse"
		}
		if len(rest) > 2 {
			return "", mcpconf.Server{}, errors.New("a remote server takes no extra arguments")
		}
	} else {
		s = mcpconf.Server{Command: target, Args: rest[2:]}
	}
	if *transport != "" && *transport != s.Transport() && !(*transport == "stdio" && s.Command != "") {
		return "", mcpconf.Server{}, fmt.Errorf("-t %s doesn't match target %q", *transport, target)
	}
	for _, h := range headers {
		k, v, ok := strings.Cut(h, ":")
		if !ok {
			return "", mcpconf.Server{}, fmt.Errorf("bad header %q, want \"Name: value\"", h)
		}
		if s.Headers == nil {
			s.Headers = map[string]string{}
		}
		s.Headers[strings.TrimSpace(k)] = strings.TrimSpace(v)
	}
	for _, e := range envs {
		k, v, ok := strings.Cut(e, "=")
		if !ok {
			return "", mcpconf.Server{}, fmt.Errorf("bad env %q, want KEY=value", e)
		}
		if s.Env == nil {
			s.Env = map[string]string{}
		}
		s.Env[k] = v
	}
	if len(s.Headers) > 0 && s.Transport() == "stdio" {
		return "", mcpconf.Server{}, errors.New("-H only applies to remote servers")
	}
	return name, s, s.Validate()
}

func target(s mcpconf.Server) string {
	if s.URL != "" {
		return s.URL
	}
	return strings.TrimSpace(s.Command + " " + strings.Join(s.Args, " "))
}
