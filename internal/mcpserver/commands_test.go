package mcpserver

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/tomstagl/clawctl/internal/transport/api"
)

// stubSrc returns a TokenSource that yields the given token without touching
// the Keychain. Used across command server tests that exercise authenticated
// paths (clawctl_models).
func stubSrc(token string) api.TokenSource {
	return func() (string, error) { return token, nil }
}

func TestBuildCommandServer_ToolsList(t *testing.T) {
	srv, err := BuildCommandServer(nil, nil, "http://localhost:19999", "")
	if err != nil {
		t.Fatalf("BuildCommandServer: %v", err)
	}
	tools := listTools(t, srv)
	if len(tools) != 5 {
		t.Fatalf("len(tools) = %d, want 5", len(tools))
	}
	byName := map[string]bool{}
	for _, tool := range tools {
		byName[tool.Name] = true
	}
	for _, want := range []string{"clawctl_health", "clawctl_models", "clawctl_verify", "clawctl_trace", "clawctl_msg"} {
		if !byName[want] {
			t.Errorf("missing tool %q; got %v", want, byName)
		}
	}
	for name := range byName {
		if !strings.HasPrefix(name, "clawctl_") {
			t.Errorf("unexpected tool %q (all tools should have clawctl_ prefix)", name)
		}
	}
}

// --------- clawctl_health ---------

func TestCommandServer_Health_HappyPath(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/health" {
			t.Errorf("unexpected path %q, want /health", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"status":"ok","version":"1.2.3"}`))
	}))
	defer backend.Close()

	srv, err := BuildCommandServer(nil, nil, backend.URL, "")
	if err != nil {
		t.Fatalf("BuildCommandServer: %v", err)
	}
	cs := connect(t, srv)
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "clawctl_health",
		Arguments: map[string]any{},
	})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if res.IsError {
		t.Fatalf("IsError = true, want false; content: %v", res.Content)
	}
	tc := requireTextContent(t, res)
	if !strings.Contains(tc.Text, `"status"`) {
		t.Errorf("health result = %q, want status field", tc.Text)
	}
}

func TestCommandServer_Health_Error(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 400 triggers no retries — fast failure
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"bad request"}`))
	}))
	defer backend.Close()

	srv, err := BuildCommandServer(nil, nil, backend.URL, "")
	if err != nil {
		t.Fatalf("BuildCommandServer: %v", err)
	}
	cs := connect(t, srv)
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "clawctl_health",
		Arguments: map[string]any{},
	})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if !res.IsError {
		t.Errorf("IsError = false, want true for 400 response")
	}
	tc := requireTextContent(t, res)
	if !strings.Contains(tc.Text, "clawctl_health") {
		t.Errorf("error result = %q, want tool name prefix", tc.Text)
	}
}

// --------- clawctl_models ---------

func TestCommandServer_Models_HappyPath(t *testing.T) {
	const wantToken = "test-tok-123"
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			t.Errorf("unexpected path %q, want /v1/models", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer "+wantToken {
			t.Errorf("Authorization = %q, want Bearer %s", got, wantToken)
		}
		_, _ = w.Write([]byte(`{"object":"list","data":[{"id":"openclaw/concierge"}]}`))
	}))
	defer backend.Close()

	srv, err := BuildCommandServer(nil, stubSrc(wantToken), backend.URL, "")
	if err != nil {
		t.Fatalf("BuildCommandServer: %v", err)
	}
	cs := connect(t, srv)
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "clawctl_models",
		Arguments: map[string]any{},
	})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if res.IsError {
		t.Fatalf("IsError = true, want false; content: %v", res.Content)
	}
	tc := requireTextContent(t, res)
	if !strings.Contains(tc.Text, "openclaw/concierge") {
		t.Errorf("models result = %q, want agent slug", tc.Text)
	}
}

func TestCommandServer_Models_Error(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 401 triggers no retries — fast failure
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"unauthorized"}`))
	}))
	defer backend.Close()

	srv, err := BuildCommandServer(nil, stubSrc("bad-token"), backend.URL, "")
	if err != nil {
		t.Fatalf("BuildCommandServer: %v", err)
	}
	cs := connect(t, srv)
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "clawctl_models",
		Arguments: map[string]any{},
	})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if !res.IsError {
		t.Errorf("IsError = false, want true for 401 response")
	}
}

// --------- clawctl_verify ---------

func TestCommandServer_Verify_CommitValid(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	// Use this repo's HEAD commit which is guaranteed to exist.
	out, err := exec.Command("git", "rev-parse", "HEAD").Output()
	if err != nil {
		t.Skip("not in a git repo")
	}
	headHash := strings.TrimSpace(string(out))

	srv, err := BuildCommandServer(nil, nil, "http://localhost:19999", "")
	if err != nil {
		t.Fatalf("BuildCommandServer: %v", err)
	}
	cs := connect(t, srv)
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "clawctl_verify",
		Arguments: map[string]any{"type": "commit", "ref": headHash},
	})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if res.IsError {
		t.Fatalf("IsError = true for valid commit; content: %v", res.Content)
	}
	tc := requireTextContent(t, res)
	if !strings.HasPrefix(tc.Text, "verified:") {
		t.Errorf("result = %q, want 'verified:' prefix", tc.Text)
	}
}

func TestCommandServer_Verify_CommitInvalid(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	fakeHash := strings.Repeat("0", 40)

	srv, err := BuildCommandServer(nil, nil, "http://localhost:19999", "")
	if err != nil {
		t.Fatalf("BuildCommandServer: %v", err)
	}
	cs := connect(t, srv)
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "clawctl_verify",
		Arguments: map[string]any{"type": "commit", "ref": fakeHash},
	})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if !res.IsError {
		t.Errorf("IsError = false for non-existent commit hash, want true")
	}
	tc := requireTextContent(t, res)
	if !strings.Contains(tc.Text, "unverified") {
		t.Errorf("result = %q, want 'unverified' marker", tc.Text)
	}
}

// --------- clawctl_trace ---------

func TestCommandServer_Trace_WithJaeger(t *testing.T) {
	const traceID = "abcdef1234567890abcdef1234567890"
	jaeger := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.Path, traceID) {
			t.Errorf("Jaeger request path %q does not contain trace ID", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"data":[{"spans":[{},{},{}]}],"errors":[]}`))
	}))
	defer jaeger.Close()

	srv, err := BuildCommandServer(nil, nil, "http://localhost:19999", jaeger.URL)
	if err != nil {
		t.Fatalf("BuildCommandServer: %v", err)
	}
	cs := connect(t, srv)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	res, err := cs.CallTool(ctx, &mcp.CallToolParams{
		Name:      "clawctl_trace",
		Arguments: map[string]any{"trace_id": traceID},
	})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if res.IsError {
		t.Fatalf("IsError = true, want false; content: %v", res.Content)
	}
	tc := requireTextContent(t, res)

	var data cmdTraceData
	if err := json.Unmarshal([]byte(tc.Text), &data); err != nil {
		t.Fatalf("unmarshal trace result: %v; text=%s", err, tc.Text)
	}
	if data.TraceID != traceID {
		t.Errorf("trace_id = %q, want %q", data.TraceID, traceID)
	}
	if !strings.Contains(data.UIURL, traceID) {
		t.Errorf("ui_url = %q, want to contain trace_id", data.UIURL)
	}
	if data.SpansCount == nil {
		t.Errorf("spans_count = nil, want 3 (from fixture)")
	} else if *data.SpansCount != 3 {
		t.Errorf("spans_count = %d, want 3", *data.SpansCount)
	}
}

func TestCommandServer_Trace_NoJaeger(t *testing.T) {
	const traceID = "deadbeef1234567890deadbeef123456"

	srv, err := BuildCommandServer(nil, nil, "http://localhost:19999", "")
	if err != nil {
		t.Fatalf("BuildCommandServer: %v", err)
	}
	cs := connect(t, srv)
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "clawctl_trace",
		Arguments: map[string]any{"trace_id": traceID},
	})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if res.IsError {
		t.Fatalf("IsError = true, want false (trace is best-effort); content: %v", res.Content)
	}
	tc := requireTextContent(t, res)

	var data cmdTraceData
	if err := json.Unmarshal([]byte(tc.Text), &data); err != nil {
		t.Fatalf("unmarshal trace result: %v; text=%s", err, tc.Text)
	}
	if data.TraceID != traceID {
		t.Errorf("trace_id = %q, want %q", data.TraceID, traceID)
	}
	if data.UIURL != "" {
		t.Errorf("ui_url = %q, want empty when jaegerURL is empty", data.UIURL)
	}
	if data.SpansCount != nil {
		t.Errorf("spans_count = %v, want nil when jaegerURL is empty", *data.SpansCount)
	}
}

// --------- clawctl_msg ---------

func TestCommandServer_Msg_HappyPath(t *testing.T) {
	const wantToken = "tok-msg-test"
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			t.Errorf("unexpected path %q, want /v1/chat/completions", r.URL.Path)
		}
		if r.Method != http.MethodPost {
			t.Errorf("method = %q, want POST", r.Method)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer "+wantToken {
			t.Errorf("Authorization = %q, want Bearer %s", got, wantToken)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"choices":[{"message":{"content":"hello from concierge"},"finish_reason":"stop"}],
			"usage":{"prompt_tokens":10,"completion_tokens":4,"total_tokens":14}
		}`))
	}))
	defer backend.Close()

	srv, err := BuildCommandServer(nil, stubSrc(wantToken), backend.URL, "")
	if err != nil {
		t.Fatalf("BuildCommandServer: %v", err)
	}
	cs := connect(t, srv)
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "clawctl_msg",
		Arguments: map[string]any{"agent": "concierge", "text": "hello"},
	})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if res.IsError {
		t.Fatalf("IsError = true, want false; content: %v", res.Content)
	}
	tc := requireTextContent(t, res)

	var envelope map[string]any
	if err := json.Unmarshal([]byte(tc.Text), &envelope); err != nil {
		t.Fatalf("unmarshal envelope: %v; text=%s", err, tc.Text)
	}
	if got := envelope["kind"]; got != "tool_response" {
		t.Errorf("kind = %v, want tool_response", got)
	}
	if got := envelope["agent"]; got != "openclaw/concierge" {
		t.Errorf("agent = %v, want openclaw/concierge", got)
	}
	if got := envelope["output"]; got != "hello from concierge" {
		t.Errorf("output = %v, want 'hello from concierge'", got)
	}
	if got := envelope["finish_reason"]; got != "stop" {
		t.Errorf("finish_reason = %v, want stop", got)
	}
	usage, ok := envelope["usage"].(map[string]any)
	if !ok {
		t.Fatalf("usage field missing or wrong type: %T", envelope["usage"])
	}
	if got := usage["input_tokens"]; got != float64(10) {
		t.Errorf("usage.input_tokens = %v, want 10", got)
	}
}

func TestCommandServer_Msg_Error(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"unauthorized"}`))
	}))
	defer backend.Close()

	srv, err := BuildCommandServer(nil, stubSrc("bad-tok"), backend.URL, "")
	if err != nil {
		t.Fatalf("BuildCommandServer: %v", err)
	}
	cs := connect(t, srv)
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "clawctl_msg",
		Arguments: map[string]any{"agent": "concierge", "text": "hello"},
	})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if !res.IsError {
		t.Errorf("IsError = false, want true for 401 response")
	}
	tc := requireTextContent(t, res)
	if !strings.Contains(tc.Text, "clawctl_msg") {
		t.Errorf("error result = %q, want tool name prefix", tc.Text)
	}
}

func TestCommandServer_Msg_Redaction(t *testing.T) {
	const secretToken = "dt0c01.SECRETSECRET.verylongsecretvalue1234567890abcdef"
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		resp := `{"choices":[{"message":{"content":"token is ` + secretToken + `"},"finish_reason":"stop"}],"usage":{}}`
		_, _ = w.Write([]byte(resp))
	}))
	defer backend.Close()

	srv, err := BuildCommandServer(nil, stubSrc("tok"), backend.URL, "")
	if err != nil {
		t.Fatalf("BuildCommandServer: %v", err)
	}
	cs := connect(t, srv)
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "clawctl_msg",
		Arguments: map[string]any{"agent": "concierge", "text": "what is the token?"},
	})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if res.IsError {
		t.Fatalf("IsError = true, want false; content: %v", res.Content)
	}
	tc := requireTextContent(t, res)
	if strings.Contains(tc.Text, secretToken) {
		t.Errorf("secret token not redacted in output: %s", tc.Text)
	}
	if !strings.Contains(tc.Text, "REDACTED") {
		t.Errorf("output should contain REDACTED marker; got: %s", tc.Text)
	}
}

// requireTextContent extracts the first TextContent from a CallToolResult,
// failing the test if it is absent or the wrong type.
func requireTextContent(t *testing.T, res *mcp.CallToolResult) *mcp.TextContent {
	t.Helper()
	if len(res.Content) == 0 {
		t.Fatal("CallToolResult.Content is empty")
	}
	tc, ok := res.Content[0].(*mcp.TextContent)
	if !ok {
		t.Fatalf("Content[0] = %T, want *mcp.TextContent", res.Content[0])
	}
	return tc
}
