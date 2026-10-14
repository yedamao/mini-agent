package main

// Day 8: mini-agent as an MCP client.
// With AGENT_MCP=/path/to/server, the agent starts that server, lists its
// tools and offers them to the model with an "mcp__" prefix. When the model
// calls one, the agent forwards it to the server with tools/call.

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
)

const mcpPrefix = "mcp__"

// mcpClient talks JSON-RPC to one server over its stdin/stdout.
// Requests are sent one at a time, which keeps this simple.
type mcpClient struct {
	mu     sync.Mutex
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stdout *bufio.Scanner
	nextID int
}

var mcp *mcpClient // nil unless AGENT_MCP is set

// startMCP launches the server, does the handshake and adds its tools.
func startMCP(path string) error {
	cmd := exec.Command(path)
	cmd.Stderr = os.Stderr // server logs show up in ours
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	sc := bufio.NewScanner(stdout)
	sc.Buffer(make([]byte, 1024*1024), 10*1024*1024)
	c := &mcpClient{cmd: cmd, stdin: stdin, stdout: sc}

	// 1. initialize: agree on a protocol version and introduce ourselves.
	var initRes struct {
		ServerInfo struct{ Name, Version string } `json:"serverInfo"`
	}
	if err := c.call("initialize", map[string]any{
		"protocolVersion": "2025-06-18",
		"capabilities":    map[string]any{},
		"clientInfo":      map[string]any{"name": "mini-agent", "version": "0.1.0"},
	}, &initRes); err != nil {
		return fmt.Errorf("initialize: %w", err)
	}
	// 2. tell the server we're ready (a notification: no reply).
	if err := c.send(map[string]any{"jsonrpc": "2.0", "method": "notifications/initialized"}); err != nil {
		return err
	}
	// 3. list tools and convert them to the model API's tool format.
	var list struct {
		Tools []struct {
			Name        string         `json:"name"`
			Description string         `json:"description"`
			InputSchema map[string]any `json:"inputSchema"`
		} `json:"tools"`
	}
	if err := c.call("tools/list", map[string]any{}, &list); err != nil {
		return fmt.Errorf("tools/list: %w", err)
	}
	var names []string
	for _, t := range list.Tools {
		tools = append(tools, map[string]any{
			"name":         mcpPrefix + t.Name,
			"description":  "[MCP " + initRes.ServerInfo.Name + "] " + t.Description,
			"input_schema": t.InputSchema,
		})
		names = append(names, mcpPrefix+t.Name)
	}
	fmt.Fprintf(os.Stderr, "[mcp] connected to %s %s, tools: %s\n",
		initRes.ServerInfo.Name, initRes.ServerInfo.Version, strings.Join(names, ", "))
	mcp = c
	return nil
}

func (c *mcpClient) send(msg any) error {
	b, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	_, err = c.stdin.Write(append(b, '\n'))
	return err
}

// call sends a request and waits for the response with the same id,
// skipping any notifications the server sends in between.
func (c *mcpClient) call(method string, params any, result any) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.nextID++
	id := c.nextID
	if err := c.send(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params}); err != nil {
		return err
	}
	for c.stdout.Scan() {
		var resp struct {
			ID     *int            `json:"id"`
			Result json.RawMessage `json:"result"`
			Error  *struct {
				Code    int    `json:"code"`
				Message string `json:"message"`
			} `json:"error"`
		}
		if err := json.Unmarshal(c.stdout.Bytes(), &resp); err != nil || resp.ID == nil || *resp.ID != id {
			continue // not our response (or a notification)
		}
		if resp.Error != nil {
			return fmt.Errorf("mcp error %d: %s", resp.Error.Code, resp.Error.Message)
		}
		return json.Unmarshal(resp.Result, result)
	}
	if err := c.stdout.Err(); err != nil {
		return err
	}
	return errors.New("mcp server closed the connection")
}

// callMCPTool forwards a tool call; name still has the mcp__ prefix.
func callMCPTool(name string, input json.RawMessage) string {
	args := map[string]any{}
	if len(input) > 0 {
		if err := json.Unmarshal(input, &args); err != nil {
			return "error: bad input: " + err.Error()
		}
	}
	var res struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		IsError bool `json:"isError"`
	}
	if err := mcp.call("tools/call", map[string]any{"name": strings.TrimPrefix(name, mcpPrefix), "arguments": args}, &res); err != nil {
		return "error: " + err.Error()
	}
	var parts []string
	for _, c := range res.Content {
		if c.Type == "text" {
			parts = append(parts, c.Text)
		}
	}
	out := strings.Join(parts, "\n")
	if res.IsError {
		out = "error: " + out
	}
	return truncate(out)
}

// stopMCP closes the server's stdin (it exits on EOF) and waits for it.
func stopMCP() {
	if mcp != nil {
		mcp.stdin.Close()
		mcp.cmd.Wait()
	}
}
