package main

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/yedamao/mini-agent/internal/tools"
)

// The built-in tools live in internal/tools so the Temporal worker can share
// them. These thin wrappers keep the rest of this package short.

// toolDefs is what we offer the model: the built-in tools, plus any MCP tools
// added at startup (see mcp.go).
var toolDefs = append([]map[string]any{}, tools.Defs...)

type toolInput = tools.Input

// runTool runs a built-in tool, or forwards mcp__ tools to the MCP server that owns them.
func runTool(ctx context.Context, name string, raw json.RawMessage) string {
	if name == useSkillTool && availableSkills != nil {
		return useSkill(raw)
	}
	if strings.HasPrefix(name, mcpPrefix) && hub != nil {
		return callMCPTool(ctx, name, raw)
	}
	return tools.Run(ctx, "", name, raw)
}

func truncate(s string) string         { return tools.Truncate(s) }
func truncateN(s string, n int) string { return tools.TruncateN(s, n) }
func preview(s string, n int) string   { return tools.Preview(s, n) }
