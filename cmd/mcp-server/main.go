// mini-mcp: an MCP server built on the official Go SDK
// (github.com/modelcontextprotocol/go-sdk). The SDK handles the JSON-RPC
// protocol, the initialize handshake, input schemas generated from the Go
// structs and argument validation, so we only write the tools.
//
// To see what the SDK does for you, read ../../examples/mcp-from-scratch.
//
// Local (stdio), e.g. for Claude Code:
//
//	go build -o ~/.local/bin/mini-mcp ./cmd/mcp-server
//	claude mcp add --scope user mini-mcp -- ~/.local/bin/mini-mcp
//
// Remote (Streamable HTTP at /mcp):
//
//	MINI_MCP_TOKEN=secret mini-mcp -http 127.0.0.1:8090
//	mini-agent mcp add -H "Authorization: Bearer \${MINI_MCP_TOKEN}" mini http://127.0.0.1:8090/mcp
package main

import (
	"context"
	"crypto/subtle"
	"errors"
	"flag"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func main() {
	addr := flag.String("http", "", "serve Streamable HTTP on this address (e.g. 127.0.0.1:8090) instead of stdio")
	flag.Parse()
	// stdout carries the protocol in stdio mode, so logs must go to stderr.
	log.SetOutput(os.Stderr)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	server := newServer()
	if *addr == "" {
		// Run serves over stdin/stdout until the client disconnects.
		if err := server.Run(ctx, &mcp.StdioTransport{}); err != nil {
			log.Fatal(err)
		}
		return
	}
	if err := serveHTTP(ctx, *addr, server, os.Getenv("MINI_MCP_TOKEN")); err != nil {
		log.Fatal(err)
	}
}

// serveHTTP serves the MCP endpoint at /mcp until ctx is cancelled.
func serveHTTP(ctx context.Context, addr string, server *mcp.Server, token string) error {
	srv := &http.Server{Addr: addr, Handler: httpHandler(server, token), ReadHeaderTimeout: 10 * time.Second}
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		srv.Shutdown(shutdownCtx)
	}()
	if token == "" {
		log.Printf("WARNING: no MINI_MCP_TOKEN set; anyone who can reach %s can run go_test", addr)
	}
	log.Printf("mini-mcp serving Streamable HTTP at http://%s/mcp", addr)
	if err := srv.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// httpHandler mounts the SDK's Streamable HTTP handler at /mcp, behind an
// optional bearer-token check. One server instance serves every session.
func httpHandler(server *mcp.Server, token string) http.Handler {
	var h http.Handler = mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, nil)
	if token != "" {
		next := h
		want := []byte("Bearer " + token)
		h = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), want) != 1 {
				w.Header().Set("WWW-Authenticate", "Bearer")
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
	mux := http.NewServeMux()
	mux.Handle("/mcp", h)
	return mux
}
