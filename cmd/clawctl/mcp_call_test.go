package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/tomstagl/clawctl/internal/config"
	"github.com/tomstagl/clawctl/internal/envelope"
	"github.com/tomstagl/clawctl/internal/mcpserver"
	"github.com/tomstagl/clawctl/internal/transport/api"
)

// stockChatResponse is a canned /v1/chat/completions reply mirroring what
// the gateway returns for a one-shot prompt. Kept tiny so the parity test
// can diff against it byte-for-byte if needed.
const stockChatResponse = `{
  "choices": [
    {"message":{"content":"hello back"},"finish_reason":"stop"}
  ],
  "usage": {"prompt_tokens": 3, "completion_tokens": 4, "total_tokens": 7}
}`

func TestNewMCPCallHandler_HappyPath(t *testing.T) {
	var (
		gotPath        string
		gotMethod      string
		gotTraceparent string
		gotAuth        string
		gotBody        []byte
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotMethod = r.Method
		gotTraceparent = r.Header.Get("traceparent")
		gotAuth = r.Header.Get("Authorization")
		gotBody, _ = io.ReadAll(r.Body)
		_, _ = w.Write([]byte(stockChatResponse))
	}))
	defer srv.Close()

	cfg := config.Config{
		Host:            srv.URL,
		Timeout:         2 * time.Second,
		KeychainService: "test",
	}
	client := api.New(cfg.Host, cfg.Timeout, func() (string, error) { return "tok-123", nil })
	handler := newMCPCallHandler(cfg, client, func() string { return "" })

	res, err := handler(context.Background(), mcpserver.Agent{ID: "openclaw/concierge"}, callReq(t, mcpCallArgs{Text: "hi", SessionID: "s1"}))
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	if res.IsError {
		t.Fatalf("IsError = true, want false; content=%q", textOf(res))
	}

	if gotPath != "/v1/chat/completions" {
		t.Errorf("gateway path = %q, want /v1/chat/completions", gotPath)
	}
	if gotMethod != http.MethodPost {
		t.Errorf("gateway method = %q, want POST", gotMethod)
	}
	if gotAuth != "Bearer tok-123" {
		t.Errorf("Authorization = %q, want 'Bearer tok-123'", gotAuth)
	}
	if !isTraceparent(gotTraceparent) {
		t.Errorf("traceparent header = %q, not a valid W3C value", gotTraceparent)
	}
	var body struct {
		Model    string `json:"model"`
		Stream   bool   `json:"stream"`
		User     string `json:"user"`
		Messages []struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(gotBody, &body); err != nil {
		t.Fatalf("unmarshal forwarded body: %v", err)
	}
	if body.Model != "openclaw/concierge" {
		t.Errorf("forwarded model = %q, want openclaw/concierge", body.Model)
	}
	if body.User != "s1" {
		t.Errorf("forwarded user = %q, want s1 (session_id)", body.User)
	}
	if body.Stream {
		t.Errorf("forwarded stream = true, want false (US-026 is non-streaming)")
	}
	if len(body.Messages) != 1 || body.Messages[0].Content != "hi" {
		t.Errorf("forwarded messages = %+v, want one user msg 'hi'", body.Messages)
	}

	// Result content must be a valid v1 ToolResponse JSON document.
	var resp envelope.ToolResponse
	if err := json.Unmarshal([]byte(textOf(res)), &resp); err != nil {
		t.Fatalf("decode tool result content: %v\ntext=%q", err, textOf(res))
	}
	if resp.EnvelopeVersion != "1" || resp.Kind != "tool_response" {
		t.Errorf("envelope_version=%q kind=%q, want '1'/'tool_response'", resp.EnvelopeVersion, resp.Kind)
	}
	if resp.Agent != "openclaw/concierge" {
		t.Errorf("envelope.agent = %q, want openclaw/concierge", resp.Agent)
	}
	if resp.Output != "hello back" {
		t.Errorf("envelope.output = %q, want 'hello back'", resp.Output)
	}
	if resp.SessionID != "s1" {
		t.Errorf("envelope.session_id = %q, want s1", resp.SessionID)
	}
	if resp.FinishReason != "stop" {
		t.Errorf("envelope.finish_reason = %q, want stop", resp.FinishReason)
	}
	if resp.Usage.InputTokens != 3 || resp.Usage.OutputTokens != 4 || resp.Usage.TotalTokens != 7 {
		t.Errorf("envelope.usage = %+v", resp.Usage)
	}
	if resp.Traceparent != gotTraceparent {
		t.Errorf("envelope.traceparent (%q) != gateway header (%q)", resp.Traceparent, gotTraceparent)
	}
	if err := envelope.Validate(resp); err != nil {
		t.Errorf("emitted envelope failed schema validation: %v", err)
	}

	// _meta MUST echo the same traceparent so MCP clients can correlate
	// without parsing the envelope.
	if got, _ := res.Meta[metaKeyTraceparent].(string); got != gotTraceparent {
		t.Errorf("_meta[%s] = %q, want %q", metaKeyTraceparent, got, gotTraceparent)
	}
}

func TestNewMCPCallHandler_RejectsEmptyText(t *testing.T) {
	cfg := config.Config{Host: "http://unreachable.invalid", Timeout: time.Second, KeychainService: "test"}
	client := api.New(cfg.Host, cfg.Timeout, func() (string, error) { return "tok", nil })
	handler := newMCPCallHandler(cfg, client, nil)

	res, err := handler(context.Background(), mcpserver.Agent{ID: "openclaw/x"}, callReq(t, mcpCallArgs{Text: ""}))
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	if !res.IsError {
		t.Fatalf("IsError = false, want true (empty text should fail before gateway)")
	}
	te := decodeToolError(t, res)
	if te.Code != "usage.invalid_argument" {
		t.Errorf("ToolError.code = %q, want usage.invalid_argument", te.Code)
	}
	if te.ExitCode == nil || *te.ExitCode != 2 {
		t.Errorf("ToolError.exit_code = %v, want 2", te.ExitCode)
	}
	if got, _ := res.Meta[metaKeyTraceparent].(string); !isTraceparent(got) {
		t.Errorf("_meta traceparent = %q, want a valid W3C value", got)
	}
}

func TestNewMCPCallHandler_RejectsUnparseableArgs(t *testing.T) {
	cfg := config.Config{Host: "http://unreachable.invalid", Timeout: time.Second, KeychainService: "test"}
	client := api.New(cfg.Host, cfg.Timeout, func() (string, error) { return "tok", nil })
	handler := newMCPCallHandler(cfg, client, nil)

	req := &mcp.CallToolRequest{Params: &mcp.CallToolParamsRaw{
		Name:      "x",
		Arguments: json.RawMessage("{not-json"),
	}}
	res, err := handler(context.Background(), mcpserver.Agent{ID: "openclaw/x"}, req)
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	if !res.IsError {
		t.Fatalf("IsError = false, want true on bad JSON args")
	}
	te := decodeToolError(t, res)
	if te.Code != "usage.invalid_argument" {
		t.Errorf("ToolError.code = %q, want usage.invalid_argument", te.Code)
	}
}

func TestNewMCPCallHandler_GatewayHTTPErrorMaps(t *testing.T) {
	cases := []struct {
		status   int
		wantCode string
	}{
		{400, "gateway.bad_request"},
		{401, "gateway.unauthorized"},
		{403, "gateway.forbidden"},
		{404, "gateway.not_found"},
		{429, "gateway.rate_limited"},
		{500, "gateway.internal"},
		{502, "gateway.upstream_unavailable"},
		{503, "gateway.upstream_unavailable"},
		{504, "gateway.upstream_unavailable"},
	}
	for _, tc := range cases {
		t.Run(http.StatusText(tc.status), func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(`{"error":{"code":"x","message":"boom"}}`))
			}))
			defer srv.Close()

			cfg := config.Config{Host: srv.URL, Timeout: 2 * time.Second, KeychainService: "test"}
			client := api.New(cfg.Host, cfg.Timeout, func() (string, error) { return "tok", nil })
			handler := newMCPCallHandler(cfg, client, nil)

			res, err := handler(context.Background(), mcpserver.Agent{ID: "openclaw/x"}, callReq(t, mcpCallArgs{Text: "hi"}))
			if err != nil {
				t.Fatalf("handler: %v", err)
			}
			if !res.IsError {
				t.Fatalf("IsError = false, want true on HTTP %d", tc.status)
			}
			te := decodeToolError(t, res)
			if te.Code != tc.wantCode {
				t.Errorf("ToolError.code = %q, want %q", te.Code, tc.wantCode)
			}
			if te.HTTPStatus == nil || *te.HTTPStatus != tc.status {
				t.Errorf("ToolError.http_status = %v, want %d", te.HTTPStatus, tc.status)
			}
			if te.ExitCode == nil || *te.ExitCode != 22 {
				t.Errorf("ToolError.exit_code = %v, want 22", te.ExitCode)
			}
			if !strings.Contains(te.Message, "[x] boom") && !strings.Contains(te.Message, "boom") {
				t.Errorf("ToolError.message = %q, want gateway body summary", te.Message)
			}
			if got, _ := res.Meta[metaKeyTraceparent].(string); !isTraceparent(got) {
				t.Errorf("_meta traceparent = %q, want a valid W3C value", got)
			}
		})
	}
}

func TestNewMCPCallHandler_TransportConnRefused(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	addr := srv.URL
	srv.Close()

	cfg := config.Config{Host: addr, Timeout: time.Second, KeychainService: "test"}
	client := api.New(cfg.Host, cfg.Timeout, func() (string, error) { return "tok", nil })
	handler := newMCPCallHandler(cfg, client, nil)

	res, err := handler(context.Background(), mcpserver.Agent{ID: "openclaw/x"}, callReq(t, mcpCallArgs{Text: "hi"}))
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	if !res.IsError {
		t.Fatalf("IsError = false, want true on connection refused")
	}
	te := decodeToolError(t, res)
	if te.Code != "transport.connection_refused" {
		t.Errorf("ToolError.code = %q, want transport.connection_refused", te.Code)
	}
	if te.ExitCode == nil || *te.ExitCode != 7 {
		t.Errorf("ToolError.exit_code = %v, want 7", te.ExitCode)
	}
}

// callReq builds a CallToolRequest with the given args JSON-encoded.
func callReq(t *testing.T, args mcpCallArgs) *mcp.CallToolRequest {
	t.Helper()
	raw, err := json.Marshal(args)
	if err != nil {
		t.Fatalf("marshal args: %v", err)
	}
	return &mcp.CallToolRequest{Params: &mcp.CallToolParamsRaw{
		Name:      "x",
		Arguments: raw,
	}}
}

// textOf returns the first TextContent payload of res, or "" if absent.
func textOf(res *mcp.CallToolResult) string {
	if len(res.Content) == 0 {
		return ""
	}
	if tc, ok := res.Content[0].(*mcp.TextContent); ok {
		return tc.Text
	}
	return ""
}

// decodeToolError pulls the ToolError envelope out of the first text
// content. Fails the test if the JSON doesn't decode.
func decodeToolError(t *testing.T, res *mcp.CallToolResult) envelope.ToolError {
	t.Helper()
	var te envelope.ToolError
	if err := json.Unmarshal([]byte(textOf(res)), &te); err != nil {
		t.Fatalf("decode ToolError: %v\ntext=%q", err, textOf(res))
	}
	if te.EnvelopeVersion != "1" || te.Kind != "tool_error" {
		t.Errorf("decoded envelope is not a v1 tool_error: version=%q kind=%q", te.EnvelopeVersion, te.Kind)
	}
	return te
}

// isTraceparent returns true when s is a syntactically-valid W3C
// traceparent (version-00 sampled). Same regex the schema uses.
func isTraceparent(s string) bool {
	if len(s) != 55 {
		return false
	}
	parts := strings.Split(s, "-")
	if len(parts) != 4 {
		return false
	}
	if parts[0] != "00" || parts[3] != "01" {
		return false
	}
	if len(parts[1]) != 32 || len(parts[2]) != 16 {
		return false
	}
	for _, p := range []string{parts[1], parts[2]} {
		for _, c := range p {
			if !(c >= '0' && c <= '9') && !(c >= 'a' && c <= 'f') {
				return false
			}
		}
	}
	return true
}
