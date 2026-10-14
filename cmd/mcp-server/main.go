// mini-mcp: a tiny MCP (Model Context Protocol) server, standard library only.
//
// How MCP over stdio works:
//   - The client (e.g. Claude Code) starts this program and talks to it over
//     stdin/stdout. Each message is one JSON-RPC 2.0 object on its own line.
//   - stdout is reserved for protocol messages; logs must go to stderr.
//   - A request has an "id" and expects a response with the same id.
//     A notification has no "id" and gets no response.
//
// The conversation:
//  1. client -> initialize                 (we reply with our capabilities)
//  2. client -> notifications/initialized  (no reply)
//  3. client -> tools/list                 (we reply with tool names + JSON schemas)
//  4. client -> tools/call                 (we run a tool and reply with its output)
//
// Usage: build it, then register it, e.g.
//
//	go build -o ~/.local/bin/mini-mcp ./cmd/mcp-server
//	claude mcp add --scope user mini-mcp -- ~/.local/bin/mini-mcp
package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
)

func main() {
	in := bufio.NewScanner(os.Stdin)
	in.Buffer(make([]byte, 1024*1024), 10*1024*1024) // allow big messages
	out := json.NewEncoder(os.Stdout)                // Encode adds the trailing newline

	for in.Scan() {
		resp := handle(in.Bytes())
		if resp == nil {
			continue // notification: nothing to send back
		}
		if err := out.Encode(resp); err != nil {
			fmt.Fprintln(os.Stderr, "write:", err)
			return
		}
	}
	if err := in.Err(); err != nil {
		fmt.Fprintln(os.Stderr, "read:", err)
	}
	// EOF on stdin means the client is gone: exit quietly.
}
