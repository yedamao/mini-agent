// Package mcpconf reads and writes the MCP server config, using the same
// shape as Claude Code's .mcp.json:
//
//	{
//	  "mcpServers": {
//	    "local":  {"command": "mini-mcp", "args": [], "env": {"K": "v"}},
//	    "remote": {"type": "http", "url": "https://example.com/mcp",
//	               "headers": {"Authorization": "Bearer ${MY_TOKEN}"}}
//	  }
//	}
//
// type is "stdio" (default when command is set), "http" (Streamable HTTP,
// the default when url is set) or "sse" (the older HTTP+SSE transport).
// ${VAR} in headers, env, url, command and args is expanded from the
// environment when the server is used, so secrets can stay out of the file.
package mcpconf

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

type Config struct {
	MCPServers map[string]Server `json:"mcpServers"`
}

type Server struct {
	Type    string            `json:"type,omitempty"`
	Command string            `json:"command,omitempty"`
	Args    []string          `json:"args,omitempty"`
	Env     map[string]string `json:"env,omitempty"`
	URL     string            `json:"url,omitempty"`
	Headers map[string]string `json:"headers,omitempty"`
}

// Transport returns the effective transport: "stdio", "http" or "sse".
func (s Server) Transport() string {
	switch {
	case s.Type == "streamable-http" || s.Type == "streamableHttp":
		return "http"
	case s.Type != "":
		return s.Type
	case s.URL != "":
		return "http"
	}
	return "stdio"
}

// Validate checks that the fields needed by the transport are present.
func (s Server) Validate() error {
	switch s.Transport() {
	case "stdio":
		if s.Command == "" {
			return errors.New("stdio server needs a command")
		}
	case "http", "sse":
		if s.URL == "" {
			return errors.New(s.Transport() + " server needs a url")
		}
	default:
		return fmt.Errorf("unknown type %q (want stdio, http or sse)", s.Type)
	}
	return nil
}

// Expanded returns a copy with ${VAR} references replaced from the
// environment. It reports variables that are not set, so a missing token
// gives a clear error instead of an empty "Bearer " header.
func (s Server) Expanded() (Server, error) {
	var missing []string
	expand := func(v string) string {
		return varRef.ReplaceAllStringFunc(v, func(m string) string {
			name := varRef.FindStringSubmatch(m)[1]
			val, ok := os.LookupEnv(name)
			if !ok {
				missing = append(missing, name)
			}
			return val
		})
	}
	out := s
	out.Command, out.URL = expand(s.Command), expand(s.URL)
	out.Args = nil
	for _, a := range s.Args {
		out.Args = append(out.Args, expand(a))
	}
	out.Env = expandMap(s.Env, expand)
	out.Headers = expandMap(s.Headers, expand)
	if len(missing) > 0 {
		return out, fmt.Errorf("environment variable(s) not set: %s", strings.Join(missing, ", "))
	}
	return out, nil
}

// varRef matches ${NAME}. Bare $NAME is left alone on purpose, since "$"
// can appear in real tokens.
var varRef = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)\}`)

func expandMap(m map[string]string, f func(string) string) map[string]string {
	if m == nil {
		return nil
	}
	out := make(map[string]string, len(m))
	for k, v := range m {
		out[k] = f(v)
	}
	return out
}

// DefaultPath is $AGENT_MCP_CONFIG, else ~/.mini-agent/mcp.json.
func DefaultPath() string {
	if p := os.Getenv("AGENT_MCP_CONFIG"); p != "" {
		return p
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "mcp.json"
	}
	return filepath.Join(home, ".mini-agent", "mcp.json")
}

// Load reads the config. A missing file is an empty config, not an error.
func Load(path string) (*Config, error) {
	cfg := &Config{MCPServers: map[string]Server{}}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return cfg, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if cfg.MCPServers == nil {
		cfg.MCPServers = map[string]Server{}
	}
	return cfg, nil
}

// Save writes the config atomically (temp file + rename) with 0600
// permissions, since it may hold headers.
func (c *Config) Save(path string) error {
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(data, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// validName limits server names to what fits in a model tool name.
var validName = regexp.MustCompile(`^[A-Za-z0-9_-]{1,32}$`)

// ValidName reports whether name can be used as a server name.
func ValidName(name string) bool { return validName.MatchString(name) && !strings.Contains(name, "__") }
