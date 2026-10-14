// mini-mcp: an MCP server built on the official Go SDK
// (github.com/modelcontextprotocol/go-sdk). The SDK handles the JSON-RPC
// protocol, the initialize handshake, input schemas generated from the Go
// structs and argument validation, so we only write the tools.
//
// To see what the SDK does for you, read ../../examples/mcp-from-scratch.
//
//	go build -o ~/.local/bin/mini-mcp ./cmd/mcp-server
//	claude mcp add --scope user mini-mcp -- ~/.local/bin/mini-mcp
package main

import (
	"context"
	"log"
	"os"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func main() {
	// stdout carries the protocol, so logs must go to stderr.
	log.SetOutput(os.Stderr)
	// Run serves over stdin/stdout until the client disconnects.
	if err := newServer().Run(context.Background(), &mcp.StdioTransport{}); err != nil {
		log.Fatal(err)
	}
}
