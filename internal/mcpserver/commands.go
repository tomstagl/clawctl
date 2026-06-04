package mcpserver

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/tomstagl/clawctl/internal/envelope"
	"github.com/tomstagl/clawctl/internal/redact"
	"github.com/tomstagl/clawctl/internal/transport/api"
)

// BuildCommandServer creates an MCP server exposing clawctl's read-only
// commands as static typed tools: clawctl_health, clawctl_models,
// clawctl_verify, clawctl_trace. Unlike Build (which fetches /v1/models at
// startup to register one tool per openclaw agent), BuildCommandServer
// registers a fixed set with no startup network call.
//
// Only read-only command tools are registered here. Any future mutating tool
// (e.g. wrapping the cli/SSH surface) MUST be gated by an --unsafe-mutating
// flag on clawctl mcp; never register one here unconditionally.
func BuildCommandServer(impl *Implementation, src api.TokenSource, baseURL, jaegerURL string) (*mcp.Server, error) {
	client := api.New(baseURL, 60*time.Second, src)
	srv := mcp.NewServer(buildImpl(impl), nil)
	srv.AddTool(healthTool(), healthHandler(client))
	srv.AddTool(modelsTool(), modelsHandler(client))
	srv.AddTool(verifyTool(), verifyHandler())
	srv.AddTool(traceTool(), traceHandler(jaegerURL))
	srv.AddTool(msgTool(), msgHandler(client))
	return srv, nil
}

// --------- clawctl_health ---------

func healthTool() *mcp.Tool {
	return &mcp.Tool{
		Name:        "clawctl_health",
		Description: "Check the openclaw gateway health endpoint. Returns the raw JSON health response body.",
		InputSchema: map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"properties":          map[string]any{},
		},
	}
}

func healthHandler(client *api.Client) mcp.ToolHandler {
	return func(ctx context.Context, _ *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		body, err := client.Get(ctx, "/health", false)
		if err != nil {
			return cmdErrResult("clawctl_health", err.Error()), nil
		}
		return cmdOKResult(body), nil
	}
}

// --------- clawctl_models ---------

func modelsTool() *mcp.Tool {
	return &mcp.Tool{
		Name:        "clawctl_models",
		Description: "List available openclaw agent models from the gateway. Returns the /v1/models JSON response.",
		InputSchema: map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"properties":          map[string]any{},
		},
	}
}

func modelsHandler(client *api.Client) mcp.ToolHandler {
	return func(ctx context.Context, _ *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		body, err := client.Get(ctx, "/v1/models", true)
		if err != nil {
			return cmdErrResult("clawctl_models", err.Error()), nil
		}
		return cmdOKResult(body), nil
	}
}

// --------- clawctl_verify ---------

func verifyTool() *mcp.Tool {
	return &mcp.Tool{
		Name:        "clawctl_verify",
		Description: "Verify a git commit, GitHub PR/issue, or file path.",
		InputSchema: map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"required":             []any{"type", "ref"},
			"properties": map[string]any{
				"type": map[string]any{
					"type":        "string",
					"enum":        []any{"commit", "pr", "issue", "file"},
					"description": "Kind of object to verify.",
				},
				"ref": map[string]any{
					"type":        "string",
					"minLength":   1,
					"description": "Reference: commit hash, owner/repo#num (pr/issue), or file path (file).",
				},
				"path": map[string]any{
					"type":        "string",
					"description": "For type=file: optional git ref to check the file at (e.g. HEAD, main).",
				},
			},
		},
	}
}

type cmdVerifyArgs struct {
	Type string `json:"type"`
	Ref  string `json:"ref"`
	Path string `json:"path,omitempty"`
}

func verifyHandler() mcp.ToolHandler {
	return func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var args cmdVerifyArgs
		if req.Params != nil && len(req.Params.Arguments) > 0 {
			if err := json.Unmarshal(req.Params.Arguments, &args); err != nil {
				return cmdErrResult("clawctl_verify", "invalid arguments: "+err.Error()), nil
			}
		}
		if args.Type == "" || args.Ref == "" {
			return cmdErrResult("clawctl_verify", "type and ref are required"), nil
		}

		var out, errBuf bytes.Buffer
		var code int
		switch args.Type {
		case "commit":
			code = cmdVerifyCommit(ctx, args.Ref, &out, &errBuf)
		case "pr":
			code = cmdVerifyGH(ctx, "pr", args.Ref, &out, &errBuf)
		case "issue":
			code = cmdVerifyGH(ctx, "issue", args.Ref, &out, &errBuf)
		case "file":
			rest := []string{args.Ref}
			if args.Path != "" {
				rest = append(rest, args.Path)
			}
			code = cmdVerifyFile(ctx, rest, &out, &errBuf)
		default:
			return cmdErrResult("clawctl_verify", fmt.Sprintf("unknown type %q", args.Type)), nil
		}

		msg := strings.TrimSpace(out.String())
		if msg == "" {
			msg = strings.TrimSpace(errBuf.String())
		}
		if code != 0 {
			return cmdErrResult("clawctl_verify", msg), nil
		}
		return cmdTextResult(msg), nil
	}
}

func cmdVerifyCommit(ctx context.Context, hash string, stdout, stderr *bytes.Buffer) int {
	out, err := exec.CommandContext(ctx, "git", "cat-file", "-t", hash).Output()
	if err == nil && strings.TrimSpace(string(out)) == "commit" {
		fmt.Fprintf(stdout, "verified: commit %s", hash)
		return 0
	}
	fmt.Fprintf(stderr, "unverified: commit %s not found", hash)
	return 1
}

func cmdVerifyGH(ctx context.Context, kind, spec string, stdout, stderr *bytes.Buffer) int {
	idx := strings.Index(spec, "#")
	if idx < 0 || idx == len(spec)-1 {
		fmt.Fprintf(stderr, "usage: ref must be owner/repo#num, got %q", spec)
		return 2
	}
	repo := spec[:idx]
	num := spec[idx+1:]
	data, err := exec.CommandContext(ctx, "gh", kind, "view", num, "--repo", repo, "--json", "state,url,title").Output()
	if err != nil {
		label := "PR"
		if kind == "issue" {
			label = "issue"
		}
		fmt.Fprintf(stderr, "unverified: %s %s not accessible", label, spec)
		return 1
	}
	var v struct {
		State string `json:"state"`
		URL   string `json:"url"`
		Title string `json:"title"`
	}
	if err := json.Unmarshal(data, &v); err != nil {
		fmt.Fprintf(stderr, "unverified: %s %s not accessible", kind, spec)
		return 1
	}
	fmt.Fprintf(stdout, "verified: %s — %s — %s", v.State, v.Title, v.URL)
	return 0
}

func cmdVerifyFile(ctx context.Context, rest []string, stdout, stderr *bytes.Buffer) int {
	path := ""
	ref := ""
	if len(rest) > 0 {
		path = rest[0]
	}
	if len(rest) > 1 {
		ref = rest[1]
	}
	if path == "" {
		fmt.Fprintf(stderr, "ref (file path) is required for type=file")
		return 2
	}
	if ref != "" {
		if err := exec.CommandContext(ctx, "git", "cat-file", "-e", ref+":"+path).Run(); err == nil {
			fmt.Fprintf(stdout, "verified: %s @ %s", path, ref)
			return 0
		}
		fmt.Fprintf(stderr, "unverified: %s not present at %s", path, ref)
		return 1
	}
	if _, err := os.Lstat(path); err == nil {
		fmt.Fprintf(stdout, "verified: %s (working tree)", path)
		return 0
	}
	fmt.Fprintf(stderr, "unverified: %s not present in working tree", path)
	return 1
}

// --------- clawctl_trace ---------

func traceTool() *mcp.Tool {
	return &mcp.Tool{
		Name:        "clawctl_trace",
		Description: "Look up a W3C trace-id in Jaeger. Returns the UI link and optional span summary.",
		InputSchema: map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"required":             []any{"trace_id"},
			"properties": map[string]any{
				"trace_id": map[string]any{
					"type":        "string",
					"minLength":   1,
					"description": "32-hex W3C trace-id from a traceparent header.",
				},
			},
		},
	}
}

type cmdTraceArgs struct {
	TraceID string `json:"trace_id"`
}

type cmdTraceData struct {
	TraceID    string `json:"trace_id"`
	UIURL      string `json:"ui_url,omitempty"`
	SpansCount *int   `json:"spans_count,omitempty"`
}

func traceHandler(jaegerURL string) mcp.ToolHandler {
	return func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var args cmdTraceArgs
		if req.Params != nil && len(req.Params.Arguments) > 0 {
			if err := json.Unmarshal(req.Params.Arguments, &args); err != nil {
				return cmdErrResult("clawctl_trace", "invalid arguments: "+err.Error()), nil
			}
		}
		if args.TraceID == "" {
			return cmdErrResult("clawctl_trace", "trace_id is required"), nil
		}

		result := cmdTraceData{TraceID: args.TraceID}
		if jaegerURL != "" {
			result.UIURL = jaegerURL + "/trace/" + args.TraceID
			apiURL := jaegerURL + "/jaeger/api/traces/" + args.TraceID
			if body, err := cmdJaegerFetch(ctx, apiURL); err == nil {
				result.SpansCount = cmdSpanCount(body)
			}
		}
		enc, _ := json.Marshal(result)
		return cmdOKResult(enc), nil
	}
}

func cmdJaegerFetch(ctx context.Context, rawURL string) ([]byte, error) {
	cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(cctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	return api.ReadLimited(resp.Body, api.DefaultMaxResponseBytes)
}

func cmdSpanCount(body []byte) *int {
	var d struct {
		Data []struct {
			Spans []struct{} `json:"spans"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &d); err != nil || len(d.Data) == 0 {
		return nil
	}
	n := len(d.Data[0].Spans)
	return &n
}

// --------- clawctl_msg ---------

func msgTool() *mcp.Tool {
	return &mcp.Tool{
		Name:        "clawctl_msg",
		Description: "Send a prompt to an openclaw agent and receive a ToolResponse envelope. Redaction is applied to the response before it is returned.",
		InputSchema: map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"required":             []any{"agent", "text"},
			"properties": map[string]any{
				"agent": map[string]any{
					"type":        "string",
					"minLength":   1,
					"description": "openclaw agent slug (e.g. 'concierge').",
				},
				"text": map[string]any{
					"type":        "string",
					"minLength":   1,
					"description": "User prompt text to send to the agent.",
				},
				"session_id": map[string]any{
					"type":        "string",
					"description": "Optional session key for conversation continuity.",
				},
				"tool_choice": map[string]any{
					"type":        "string",
					"enum":        []any{"auto", "none", "required"},
					"description": "Optional hint about whether the agent should call sub-tools.",
				},
			},
		},
	}
}

type cmdMsgArgs struct {
	Agent      string `json:"agent"`
	Text       string `json:"text"`
	SessionID  string `json:"session_id,omitempty"`
	ToolChoice string `json:"tool_choice,omitempty"`
}

type cmdMsgChatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type cmdMsgChatPayload struct {
	Model    string              `json:"model"`
	Stream   bool                `json:"stream"`
	User     string              `json:"user,omitempty"`
	Messages []cmdMsgChatMessage `json:"messages"`
}

type cmdMsgChatUsage struct {
	PromptTokens     *int `json:"prompt_tokens"`
	CompletionTokens *int `json:"completion_tokens"`
	TotalTokens      *int `json:"total_tokens"`
}

type cmdMsgChatParsed struct {
	Content      string
	FinishReason string
	Usage        cmdMsgChatUsage
}

func cmdMsgBuildPayload(agent, text, session string) ([]byte, error) {
	p := cmdMsgChatPayload{
		Model:    "openclaw/" + agent,
		Stream:   false,
		User:     session,
		Messages: []cmdMsgChatMessage{{Role: "user", Content: text}},
	}
	return json.Marshal(p)
}

func cmdMsgParseChatResponse(body []byte) (cmdMsgChatParsed, error) {
	var raw struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
		Usage cmdMsgChatUsage `json:"usage"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return cmdMsgChatParsed{}, err
	}
	r := cmdMsgChatParsed{Usage: raw.Usage}
	if len(raw.Choices) > 0 {
		r.Content = raw.Choices[0].Message.Content
		r.FinishReason = raw.Choices[0].FinishReason
	}
	return r, nil
}

func cmdMsgMapFinishReason(raw string) string {
	switch raw {
	case "stop", "length", "content_filter", "error":
		return raw
	case "tool_calls", "function_call", "tool_call":
		return "tool_call"
	default:
		return "stop"
	}
}

func cmdMsgToEnvelopeUsage(u cmdMsgChatUsage) envelope.Usage {
	out := envelope.Usage{}
	if u.PromptTokens != nil {
		out.InputTokens = *u.PromptTokens
	}
	if u.CompletionTokens != nil {
		out.OutputTokens = *u.CompletionTokens
	}
	if u.TotalTokens != nil {
		out.TotalTokens = *u.TotalTokens
	}
	return out
}

func cmdMsgToEnvelopeRedactions(hits []redact.Hit) []envelope.Redaction {
	out := make([]envelope.Redaction, 0, len(hits))
	for _, h := range hits {
		off := h.OffsetHint
		count := h.Count
		out = append(out, envelope.Redaction{
			Kind:       h.Kind,
			OffsetHint: &off,
			Count:      &count,
		})
	}
	return out
}

func msgHandler(client *api.Client) mcp.ToolHandler {
	return func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var args cmdMsgArgs
		if req.Params != nil && len(req.Params.Arguments) > 0 {
			if err := json.Unmarshal(req.Params.Arguments, &args); err != nil {
				return cmdErrResult("clawctl_msg", "invalid arguments: "+err.Error()), nil
			}
		}
		if args.Agent == "" {
			return cmdErrResult("clawctl_msg", "agent is required"), nil
		}
		if args.Text == "" {
			return cmdErrResult("clawctl_msg", "text is required"), nil
		}
		if int64(len(args.Text)) > api.DefaultMaxResponseBytes {
			return cmdErrResult("clawctl_msg", fmt.Sprintf(
				"text exceeds %d-byte limit", api.DefaultMaxResponseBytes)), nil
		}

		payload, err := cmdMsgBuildPayload(args.Agent, args.Text, args.SessionID)
		if err != nil {
			return cmdErrResult("clawctl_msg", "build payload: "+err.Error()), nil
		}

		body, err := client.Do(ctx, api.Request{
			Method:  http.MethodPost,
			Path:    "/v1/chat/completions",
			Body:    payload,
			Authed:  true,
			Retry:   false,
			Headers: []string{"Content-Type: application/json"},
		})
		if err != nil {
			return cmdErrResult("clawctl_msg", err.Error()), nil
		}

		parsed, err := cmdMsgParseChatResponse(body)
		if err != nil {
			return cmdErrResult("clawctl_msg", "parse response: "+err.Error()), nil
		}

		r := redact.Apply(parsed.Content, redact.Options{})

		resp := envelope.ToolResponse{
			EnvelopeVersion: envelope.Version,
			Kind:            envelope.KindToolResponse,
			Agent:           "openclaw/" + args.Agent,
			SessionID:       args.SessionID,
			Input:           envelope.Input{Role: "user", Content: args.Text},
			Output:          r.Text,
			Redactions:      cmdMsgToEnvelopeRedactions(r.Hits),
			Usage:           cmdMsgToEnvelopeUsage(parsed.Usage),
			FinishReason:    cmdMsgMapFinishReason(parsed.FinishReason),
		}

		enc, err := json.Marshal(resp)
		if err != nil {
			return cmdErrResult("clawctl_msg", "marshal response: "+err.Error()), nil
		}
		return cmdOKResult(enc), nil
	}
}

// --------- shared result helpers ---------

func cmdOKResult(body []byte) *mcp.CallToolResult {
	return &mcp.CallToolResult{
		Content: []mcp.Content{&mcp.TextContent{Text: string(body)}},
	}
}

func cmdTextResult(text string) *mcp.CallToolResult {
	return &mcp.CallToolResult{
		Content: []mcp.Content{&mcp.TextContent{Text: text}},
	}
}

func cmdErrResult(tool, msg string) *mcp.CallToolResult {
	return &mcp.CallToolResult{
		IsError: true,
		Content: []mcp.Content{&mcp.TextContent{
			Text: tool + ": " + msg,
		}},
	}
}
