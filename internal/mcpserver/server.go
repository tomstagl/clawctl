// Package mcpserver builds the clawctl MCP stdio server: it consumes the
// gateway's /v1/models response, registers one MCP tool per agent slug, and
// exposes the resulting *mcp.Server so the cmd/clawctl entrypoint can run
// it on stdin/stdout.
//
// US-025 only requires tools/list to surface registered tools. The
// per-tool handler is a stub that returns a typed "not yet implemented"
// MCP error envelope; US-026 replaces it with the chat-completions call.
// Splitting the package this way lets the tools/call wiring land later
// without re-shaping the server-bootstrap path.
package mcpserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// AgentPrefix is the gateway's slug prefix that groups openclaw-published
// models. Mirrors the bash _known_agents helper which strips
// `^openclaw/` from every cached model id.
const AgentPrefix = "openclaw/"

// Agent is the per-tool metadata extracted from a /v1/models entry. Only
// id is required — the gateway's response is OpenAI-compatible and may
// not include description/owner, so those stay optional.
type Agent struct {
	ID          string // full slug, e.g. "openclaw/concierge"
	Description string // human-readable description, may be empty
	OwnedBy     string // gateway-reported owner, may be empty
}

// Slug returns the bare slug after stripping AgentPrefix. The MCP tool
// name uses this form so callers don't have to escape "/" in tool names
// (some clients reject it). Pass-through if the prefix is absent.
func (a Agent) Slug() string {
	return strings.TrimPrefix(a.ID, AgentPrefix)
}

// ParseModels decodes a /v1/models response body into a sorted slice of
// Agents. Order is alphabetical by ID so tools/list is deterministic
// across processes — Claude Code, Codex, and the e2e test all see the
// same shape regardless of the gateway's serialization order.
//
// Entries whose id does not start with AgentPrefix are skipped: the
// gateway may publish non-openclaw models (e.g. an embeddings backend)
// that aren't agents and shouldn't be exposed as tools.
func ParseModels(body []byte) ([]Agent, error) {
	var resp struct {
		Data []struct {
			ID          string `json:"id"`
			Description string `json:"description"`
			OwnedBy     string `json:"owned_by"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("mcpserver: decode /v1/models: %w", err)
	}
	out := make([]Agent, 0, len(resp.Data))
	for _, m := range resp.Data {
		if m.ID == "" || !strings.HasPrefix(m.ID, AgentPrefix) {
			continue
		}
		out = append(out, Agent{
			ID:          m.ID,
			Description: m.Description,
			OwnedBy:     m.OwnedBy,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

// CallHandler executes one tools/call against the gateway. It is the seam
// US-026 will fill: for US-025 we install a stub that returns a typed
// "not yet implemented" error so tools/list still works without forcing
// the call path to land at the same time.
type CallHandler func(ctx context.Context, agent Agent, req *mcp.CallToolRequest) (*mcp.CallToolResult, error)

// Implementation overrides the default name/version/title pinned on the
// MCP server identity. Pass nil for clawctl defaults.
type Implementation struct {
	Name    string
	Title   string
	Version string
}

// Build constructs an MCP server with one tool per agent. The returned
// server has no transport attached; callers run it via Server.Run with
// an mcp.StdioTransport (production) or mcp.NewInMemoryTransports (tests).
//
// When call is nil, every tool handler returns a typed
// "tool_call_not_implemented" MCP error so tools/list still validates
// end-to-end. The stub deliberately exits with IsError=true rather than
// returning a Go error so MCP clients see a structured failure on the
// content channel instead of a transport-level reject.
func Build(impl *Implementation, agents []Agent, call CallHandler) (*mcp.Server, error) {
	srv := mcp.NewServer(buildImpl(impl), nil)
	for _, a := range agents {
		if a.Slug() == "" {
			return nil, fmt.Errorf("mcpserver: agent %q has empty slug after prefix strip", a.ID)
		}
		tool, handler := buildTool(a, call)
		srv.AddTool(tool, handler)
	}
	return srv, nil
}

// buildTool produces (Tool, ToolHandler) for one agent. Split out so
// tests can introspect the schema without spinning up the full server.
func buildTool(a Agent, call CallHandler) (*mcp.Tool, mcp.ToolHandler) {
	desc := toolDescription(a)
	tool := &mcp.Tool{
		Name:        a.Slug(),
		Description: desc,
		InputSchema: inputSchema(),
	}
	if call == nil {
		call = stubHandler
	}
	handler := func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return call(ctx, a, req)
	}
	return tool, handler
}

// toolDescription mirrors the agent metadata path documented in the
// US-025 acceptance criteria: prefer the gateway-supplied description,
// otherwise synthesize a stable fallback from the slug. The fallback
// names the slug verbatim so callers can still cite the underlying
// agent in tool-routing prompts.
func toolDescription(a Agent) string {
	if d := strings.TrimSpace(a.Description); d != "" {
		return d
	}
	owner := strings.TrimSpace(a.OwnedBy)
	if owner == "" {
		owner = "openclaw"
	}
	return fmt.Sprintf("openclaw agent %q (owned by %s). Invoke with a text prompt; optional session_id resumes a prior conversation, optional tool_choice hints the agent about sub-tool routing.", a.ID, owner)
}

// inputSchema mirrors the v1 envelope's Input shape: a required text
// prompt plus the optional fields a caller may supply at the tool
// boundary (session_id, tool_choice, streaming). Returning a fresh map
// per call avoids accidental mutation by emitter code.
//
// `streaming` opts the call into the SSE backend. When true and the
// client supplied a progressToken on the call, each ToolStreamChunk is
// surfaced as an MCP `notifications/progress` message; the final
// ToolResponse remains the tool result. Without a progressToken the
// streaming flag is honoured but no chunk-level notifications fire — the
// MCP spec only allows them when the client opted in.
func inputSchema() map[string]any {
	return map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"required":             []any{"text"},
		"properties": map[string]any{
			"text": map[string]any{
				"type":        "string",
				"minLength":   1,
				"description": "Plain-text prompt to forward to the agent (mirrors envelope.input.content).",
			},
			"session_id": map[string]any{
				"type":        "string",
				"minLength":   1,
				"maxLength":   128,
				"description": "Optional opaque conversation handle (mirrors envelope.session_id).",
			},
			"tool_choice": map[string]any{
				"type":        "string",
				"enum":        []any{"auto", "none", "required"},
				"description": "Optional hint about whether the agent should call sub-tools (mirrors envelope.tool_choice).",
			},
			"streaming": map[string]any{
				"type":        "boolean",
				"default":     false,
				"description": "When true, the call uses the SSE backend and each ToolStreamChunk is sent as an MCP notifications/progress message (requires the client to supply a progressToken on the request).",
			},
		},
	}
}

// stubHandler is the default CallHandler when callers haven't wired the
// gateway path yet. Returns a typed MCP error result rather than a Go
// error so the protocol-level transport stays healthy and tools/list
// continues to work for clients that probe before calling.
func stubHandler(_ context.Context, a Agent, _ *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	return &mcp.CallToolResult{
		IsError: true,
		Content: []mcp.Content{&mcp.TextContent{
			Text: fmt.Sprintf("tool_call_not_implemented: clawctl mcp tools/call wiring lands in US-026; agent %q is registered but not yet callable", a.ID),
		}},
	}, nil
}

func buildImpl(in *Implementation) *mcp.Implementation {
	out := &mcp.Implementation{
		Name:    "clawctl",
		Title:   "clawctl — openclaw MCP gateway",
		Version: "0",
	}
	if in == nil {
		return out
	}
	if in.Name != "" {
		out.Name = in.Name
	}
	if in.Title != "" {
		out.Title = in.Title
	}
	if in.Version != "" {
		out.Version = in.Version
	}
	return out
}

// ErrNoAgents indicates the /v1/models response was well-formed but
// contained zero agent slugs. Callers should fail loudly because an MCP
// server with no tools is not useful and the user almost certainly
// expects a typo or auth issue.
var ErrNoAgents = errors.New("mcpserver: no openclaw agents in /v1/models response")
