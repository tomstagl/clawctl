package mcpserver

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestParseModels_StripsPrefixAndSorts(t *testing.T) {
	body := []byte(`{"data":[
		{"id":"openclaw/zebra","description":"last alphabetically","owned_by":"team-z"},
		{"id":"openclaw/alpha","description":"first alphabetically","owned_by":"team-a"},
		{"id":"non-openclaw/skipme"},
		{"id":""},
		{"id":"openclaw/middle"}
	]}`)
	got, err := ParseModels(body)
	if err != nil {
		t.Fatalf("ParseModels: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("len(got)=%d, want 3 (entries without openclaw/ prefix or empty id are skipped)", len(got))
	}
	wantIDs := []string{"openclaw/alpha", "openclaw/middle", "openclaw/zebra"}
	for i, w := range wantIDs {
		if got[i].ID != w {
			t.Errorf("got[%d].ID = %q, want %q", i, got[i].ID, w)
		}
	}
	if got[0].Slug() != "alpha" {
		t.Errorf("Slug() = %q, want %q", got[0].Slug(), "alpha")
	}
	if got[0].Description != "first alphabetically" {
		t.Errorf("Description = %q", got[0].Description)
	}
	if got[0].OwnedBy != "team-a" {
		t.Errorf("OwnedBy = %q", got[0].OwnedBy)
	}
}

func TestParseModels_BadJSON(t *testing.T) {
	if _, err := ParseModels([]byte(`{`)); err == nil {
		t.Errorf("expected decode error on truncated body")
	}
}

func TestToolDescription_PrefersGatewaySupplied(t *testing.T) {
	got := toolDescription(Agent{ID: "openclaw/x", Description: "  custom desc  "})
	if got != "custom desc" {
		t.Errorf("got %q, want trimmed gateway description", got)
	}
}

func TestToolDescription_FallbackNamesAgent(t *testing.T) {
	got := toolDescription(Agent{ID: "openclaw/concierge"})
	if !strings.Contains(got, `"openclaw/concierge"`) {
		t.Errorf("fallback should cite the agent ID; got %q", got)
	}
	if !strings.Contains(got, "owned by openclaw") {
		t.Errorf("fallback should default owner to 'openclaw'; got %q", got)
	}
}

// TestToolDescription_AppendsCapabilities verifies advertised capabilities are
// surfaced in the tool description (A2A agent-card "skills"), and that blank
// entries are dropped.
func TestToolDescription_AppendsCapabilities(t *testing.T) {
	got := toolDescription(Agent{
		ID:           "openclaw/concierge",
		Description:  "routes requests",
		Capabilities: []string{"triage", "  ", "handoff"},
	})
	if !strings.Contains(got, "Capabilities: triage, handoff.") {
		t.Errorf("description should list non-blank capabilities; got %q", got)
	}
}

// TestParseModels_ParsesCapabilities verifies both the "capabilities" key and
// the "skills" fallback populate Agent.Capabilities.
func TestParseModels_ParsesCapabilities(t *testing.T) {
	body := []byte(`{"data":[
		{"id":"openclaw/a","capabilities":["x","y"]},
		{"id":"openclaw/b","skills":["z"]},
		{"id":"openclaw/c"}
	]}`)
	got, err := ParseModels(body)
	if err != nil {
		t.Fatalf("ParseModels: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("got %d agents, want 3", len(got))
	}
	// Sorted by ID: a, b, c.
	if len(got[0].Capabilities) != 2 || got[0].Capabilities[0] != "x" {
		t.Errorf("a.Capabilities = %v, want [x y]", got[0].Capabilities)
	}
	if len(got[1].Capabilities) != 1 || got[1].Capabilities[0] != "z" {
		t.Errorf("b.Capabilities (skills fallback) = %v, want [z]", got[1].Capabilities)
	}
	if len(got[2].Capabilities) != 0 {
		t.Errorf("c.Capabilities = %v, want empty", got[2].Capabilities)
	}
}

func TestInputSchema_MirrorsEnvelopeShape(t *testing.T) {
	schema := inputSchema()
	if schema["type"] != "object" {
		t.Errorf("type = %v, want object", schema["type"])
	}
	if schema["additionalProperties"] != false {
		t.Errorf("additionalProperties = %v, want false", schema["additionalProperties"])
	}
	required, ok := schema["required"].([]any)
	if !ok || len(required) != 1 || required[0] != "text" {
		t.Errorf("required = %v, want [text]", schema["required"])
	}
	props, ok := schema["properties"].(map[string]any)
	if !ok {
		t.Fatalf("properties not a map: %T", schema["properties"])
	}
	for _, k := range []string{"text", "session_id", "tool_choice", "streaming"} {
		if _, ok := props[k]; !ok {
			t.Errorf("properties missing %q", k)
		}
	}
	tc, _ := props["tool_choice"].(map[string]any)
	enum, _ := tc["enum"].([]any)
	if len(enum) != 3 {
		t.Errorf("tool_choice.enum = %v, want 3 values (auto/none/required)", enum)
	}
	streaming, _ := props["streaming"].(map[string]any)
	if streaming["type"] != "boolean" {
		t.Errorf("streaming.type = %v, want boolean", streaming["type"])
	}
}

func TestBuild_RegistersOneToolPerAgent(t *testing.T) {
	agents := []Agent{
		{ID: "openclaw/concierge", Description: "concierge desc"},
		{ID: "openclaw/dead-code-sweep"},
	}
	srv, err := Build(nil, agents, nil)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	tools := listTools(t, srv)
	if len(tools) != 2 {
		t.Fatalf("len(tools) = %d, want 2", len(tools))
	}
	byName := map[string]*mcp.Tool{}
	for _, tt := range tools {
		byName[tt.Name] = tt
	}
	if tt, ok := byName["concierge"]; !ok {
		t.Errorf("missing tool %q", "concierge")
	} else if tt.Description != "concierge desc" {
		t.Errorf("concierge.Description = %q, want gateway-supplied", tt.Description)
	}
	if tt, ok := byName["dead-code-sweep"]; !ok {
		t.Errorf("missing tool %q", "dead-code-sweep")
	} else if !strings.Contains(tt.Description, "openclaw/dead-code-sweep") {
		t.Errorf("dead-code-sweep.Description = %q, want fallback citing agent id", tt.Description)
	}
}

func TestBuild_StubHandlerReturnsTypedError(t *testing.T) {
	agents := []Agent{{ID: "openclaw/x"}}
	srv, err := Build(nil, agents, nil)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	cs := connect(t, srv)
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "x",
		Arguments: map[string]any{"text": "hi"},
	})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if !res.IsError {
		t.Errorf("IsError = false, want true (US-025 stub)")
	}
	if len(res.Content) == 0 {
		t.Fatalf("Content empty")
	}
	tc, ok := res.Content[0].(*mcp.TextContent)
	if !ok {
		t.Fatalf("Content[0] = %T, want *TextContent", res.Content[0])
	}
	if !strings.Contains(tc.Text, "tool_call_not_implemented") {
		t.Errorf("stub text = %q, want tool_call_not_implemented marker", tc.Text)
	}
	if !strings.Contains(tc.Text, "openclaw/x") {
		t.Errorf("stub text should cite agent id; got %q", tc.Text)
	}
}

func TestBuild_RoutesCallToHandler(t *testing.T) {
	agents := []Agent{{ID: "openclaw/concierge"}, {ID: "openclaw/main"}}
	calls := []string{}
	handler := func(_ context.Context, a Agent, _ *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		calls = append(calls, a.ID)
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "ok:" + a.ID}}}, nil
	}
	srv, err := Build(nil, agents, handler)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	cs := connect(t, srv)

	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "main", Arguments: map[string]any{"text": "hello"},
	})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if res.IsError {
		t.Errorf("IsError = true, want false")
	}
	if len(calls) != 1 || calls[0] != "openclaw/main" {
		t.Errorf("calls = %v, want [openclaw/main]", calls)
	}
}

func TestBuild_RejectsAgentWithEmptySlug(t *testing.T) {
	_, err := Build(nil, []Agent{{ID: "openclaw/"}}, nil)
	if err == nil {
		t.Errorf("expected error on empty-slug agent")
	}
}

// listTools spins up the server on an in-memory transport and returns the
// tools/list payload as a flat []*mcp.Tool. Mirrors what an MCP client
// (Claude Code, Codex) sees on connect.
func listTools(t *testing.T, srv *mcp.Server) []*mcp.Tool {
	t.Helper()
	cs := connect(t, srv)
	res, err := cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	return res.Tools
}

func connect(t *testing.T, srv *mcp.Server) *mcp.ClientSession {
	t.Helper()
	ct, st := mcp.NewInMemoryTransports()
	go func() {
		// Server.Run blocks until the transport closes; ignore the error
		// the test cleanup path triggers.
		_ = srv.Run(context.Background(), st)
	}()
	cli := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "0"}, nil)
	cs, err := cli.Connect(context.Background(), ct, nil)
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	t.Cleanup(func() { _ = cs.Close() })
	return cs
}

// TestInputSchema_MarshalsValidJSON guards against future regressions where
// someone hands InputSchema a value that breaks json.Marshal at the wire.
// (The SDK happily accepts any-typed schemas, which makes silent breakage
// possible if a contributor swaps the inner map for a struct without json
// tags.)
func TestInputSchema_MarshalsValidJSON(t *testing.T) {
	b, err := json.Marshal(inputSchema())
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var round map[string]any
	if err := json.Unmarshal(b, &round); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if round["type"] != "object" {
		t.Errorf("round-trip lost type: %v", round)
	}
}
