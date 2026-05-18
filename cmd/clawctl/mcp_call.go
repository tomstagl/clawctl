package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/tomstagl/clawctl/internal/config"
	"github.com/tomstagl/clawctl/internal/envelope"
	"github.com/tomstagl/clawctl/internal/keychain"
	"github.com/tomstagl/clawctl/internal/mcpserver"
	"github.com/tomstagl/clawctl/internal/redact"
	"github.com/tomstagl/clawctl/internal/trace"
	"github.com/tomstagl/clawctl/internal/transport/api"
)

// metaKeyTraceparent is the _meta key clawctl populates on every tool
// result so MCP clients can correlate spans without parsing the
// envelope. Kept dotted (clawctl.traceparent) to avoid colliding with
// the slash-namespaced keys some clients reserve under _meta/mcp/*.
const (
	metaKeyTraceparent = "clawctl.traceparent"
	// metaKeyStreamChunk holds the v1 ToolStreamChunk envelope embedded
	// in a notifications/progress message. Marshalled as raw JSON so the
	// client can validate it against schemas/envelope.v1.json without an
	// intermediate decode/encode round-trip.
	metaKeyStreamChunk = "clawctl.stream_chunk"
	// metaKeyStreamSequence is the 0-based ordinal of the chunk within
	// the stream. Mirrors envelope.index but lives at the protocol layer
	// so consumers that only inspect _meta still see ordering even if
	// they skip the embedded envelope.
	metaKeyStreamSequence = "clawctl.stream_sequence"
)

// mcpCallArgs is the JSON shape of tools/call arguments. Field set
// mirrors internal/mcpserver.inputSchema() one-to-one so the schema and
// decoder cannot drift.
type mcpCallArgs struct {
	Text       string `json:"text"`
	SessionID  string `json:"session_id,omitempty"`
	ToolChoice string `json:"tool_choice,omitempty"`
	Streaming  bool   `json:"streaming,omitempty"`
}

// newMCPCallHandler returns the CallHandler that powers tools/call.
// Each call:
//   - decodes the tool arguments,
//   - generates a fresh W3C traceparent,
//   - posts to /v1/chat/completions through the same api.Client used by
//     `clawctl msg`,
//   - applies the boundary redactor,
//   - emits the result as a v1 ToolResponse envelope (JSON in
//     Content[0] + StructuredContent for typed clients), with the
//     traceparent echoed in CallToolResult.Meta.
//
// Failures land in-band as a ToolError envelope with IsError=true so
// the LLM can self-correct rather than seeing an opaque transport
// error. The MCP framing stays healthy in either case.
func newMCPCallHandler(cfg config.Config, client *api.Client, gwToken func() string) mcpserver.CallHandler {
	if gwToken == nil {
		gwToken = func() string { return "" }
	}
	return func(ctx context.Context, agent mcpserver.Agent, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		tp, err := trace.New()
		if err != nil {
			// trace.New only fails when the system entropy source is
			// broken — record it as an internal error so the client
			// still sees a structured envelope.
			return mcpErrorResult("", agent, "gateway.internal",
				"trace generation failed: "+err.Error(), nil, 1), nil
		}

		var args mcpCallArgs
		if len(req.Params.Arguments) > 0 {
			if err := json.Unmarshal(req.Params.Arguments, &args); err != nil {
				return mcpErrorResult(tp.String(), agent, "usage.invalid_argument",
					"tools/call: invalid arguments JSON: "+err.Error(), nil, 2), nil
			}
		}
		if args.Text == "" {
			return mcpErrorResult(tp.String(), agent, "usage.invalid_argument",
				"tools/call: 'text' is required and must be non-empty", nil, 2), nil
		}

		if args.Streaming {
			return runMCPStreamingCall(ctx, cfg, client, gwToken, agent, args, req, tp)
		}

		payload, err := buildChatPayload(agent.Slug(), args.Text, args.SessionID, false)
		if err != nil {
			return mcpErrorResult(tp.String(), agent, "envelope.invalid",
				"build chat payload: "+err.Error(), nil, 1), nil
		}
		body, err := client.Do(ctx, api.Request{
			Method:      http.MethodPost,
			Path:        "/v1/chat/completions",
			Body:        payload,
			Authed:      true,
			Traceparent: tp.String(),
			Headers:     []string{"Content-Type: application/json"},
			Retry:       false,
		})
		if err != nil {
			code, msg, status, exit := classifyMCPGatewayError(err, cfg)
			return mcpErrorResult(tp.String(), agent, code, msg, status, exit), nil
		}

		parsed, perr := parseChatResponse(body)
		if perr != nil {
			return mcpErrorResult(tp.String(), agent, "gateway.internal",
				"invalid response from /v1/chat/completions: "+perr.Error(), nil, 1), nil
		}

		gw := gwToken()
		r := redact.Apply(parsed.Content, redact.Options{GwToken: gw, Disable: cfg.NoRedact})
		if kinds := r.Kinds(); len(kinds) > 0 && cfg.CacheDir != "" {
			_ = os.MkdirAll(cfg.CacheDir, 0o755)
			_ = redact.AppendAudit(filepath.Join(cfg.CacheDir, "last-redaction"), agent.Slug(), kinds)
		}

		resp := envelope.ToolResponse{
			EnvelopeVersion: envelope.Version,
			Kind:            envelope.KindToolResponse,
			Agent:           agent.ID,
			SessionID:       args.SessionID,
			Traceparent:     tp.String(),
			Input:           envelope.Input{Role: "user", Content: args.Text},
			ToolChoice:      args.ToolChoice,
			Output:          r.Text,
			Redactions:      toEnvelopeRedactions(r.Hits),
			Usage:           toEnvelopeUsage(parsed.Usage),
			FinishReason:    mapFinishReason(parsed.FinishReason),
		}
		if err := envelope.Validate(resp); err != nil {
			return mcpErrorResult(tp.String(), agent, "envelope.invalid",
				"emitted envelope failed schema validation: "+err.Error(), nil, 1), nil
		}
		enc, err := json.Marshal(resp)
		if err != nil {
			return mcpErrorResult(tp.String(), agent, "envelope.invalid",
				"marshal envelope: "+err.Error(), nil, 1), nil
		}
		return &mcp.CallToolResult{
			Meta:              map[string]any{metaKeyTraceparent: tp.String()},
			Content:           []mcp.Content{&mcp.TextContent{Text: string(enc)}},
			StructuredContent: resp,
		}, nil
	}
}

// runMCPStreamingCall executes the SSE-backed streaming flavour of
// tools/call. It posts the chat-completions request with stream=true,
// buffers the SSE blob (so redaction stays boundary-safe — same trade-off
// the bash and Go `clawctl stream` make), then walks the parsed chunks:
// each becomes one MCP `notifications/progress` message addressed to the
// caller-supplied progressToken, in order. The final ToolResponse is
// returned as the tool result so MCP clients that don't subscribe to
// progress still see the full output.
//
// Two redaction passes guard against secret patterns crossing SSE chunk
// boundaries: per-chunk and aggregate. When the per-chunk redacted
// concatenation matches the aggregate-redacted text, we know no secret
// straddled a boundary and can emit one progress per chunk verbatim.
// Otherwise we coalesce into a single boundary-safe progress payload —
// emitting per-chunk in that case would either leak the unredacted bytes
// or split a redaction marker, both of which break consumers.
//
// When the client did not supply a progressToken on the request, we
// honour the streaming flag (still hit the SSE backend, still parse and
// validate chunks) but suppress the notifications: per the MCP spec,
// servers MUST NOT send progress for requests that did not opt in.
func runMCPStreamingCall(
	ctx context.Context,
	cfg config.Config,
	client *api.Client,
	gwToken func() string,
	agent mcpserver.Agent,
	args mcpCallArgs,
	req *mcp.CallToolRequest,
	tp trace.Traceparent,
) (*mcp.CallToolResult, error) {
	payload, err := buildChatPayload(agent.Slug(), args.Text, args.SessionID, true)
	if err != nil {
		return mcpErrorResult(tp.String(), agent, "envelope.invalid",
			"build chat payload: "+err.Error(), nil, 1), nil
	}
	body, err := client.Do(ctx, api.Request{
		Method:      http.MethodPost,
		Path:        "/v1/chat/completions",
		Body:        payload,
		Authed:      true,
		Traceparent: tp.String(),
		Headers:     []string{"Content-Type: application/json", "Accept: text/event-stream"},
		Retry:       false,
	})
	if err != nil {
		code, msg, status, exit := classifyMCPGatewayError(err, cfg)
		return mcpErrorResult(tp.String(), agent, code, msg, status, exit), nil
	}

	parsed, perr := parseSSEStream(body)
	if perr != nil {
		return mcpErrorResult(tp.String(), agent, "gateway.internal",
			"parse SSE stream: "+perr.Error(), nil, 1), nil
	}
	if parsed.Err != "" {
		return mcpErrorResult(tp.String(), agent, "gateway.internal",
			"stream error: "+parsed.Err, nil, 1), nil
	}

	gw := gwToken()
	perChunk := make([]redact.Result, 0, len(parsed.Chunks))
	var perChunkConcat strings.Builder
	for _, c := range parsed.Chunks {
		r := redact.Apply(c, redact.Options{GwToken: gw, Disable: cfg.NoRedact})
		perChunk = append(perChunk, r)
		perChunkConcat.WriteString(r.Text)
	}
	aggResult := redact.Apply(strings.Join(parsed.Chunks, ""), redact.Options{GwToken: gw, Disable: cfg.NoRedact})
	if kinds := aggResult.Kinds(); len(kinds) > 0 && cfg.CacheDir != "" {
		_ = os.MkdirAll(cfg.CacheDir, 0o755)
		_ = redact.AppendAudit(filepath.Join(cfg.CacheDir, "last-redaction"), agent.Slug(), kinds)
	}

	chunks := buildStreamingChunks(agent.ID, args.SessionID, tp.String(), perChunk, aggResult, perChunkConcat.String())

	progressToken := mcpProgressToken(req)
	if progressToken != nil && req.Session != nil {
		total := float64(len(chunks))
		for i, chunk := range chunks {
			enc, err := json.Marshal(chunk)
			if err != nil {
				return mcpErrorResult(tp.String(), agent, "envelope.invalid",
					"marshal stream chunk: "+err.Error(), nil, 1), nil
			}
			notifyErr := req.Session.NotifyProgress(ctx, &mcp.ProgressNotificationParams{
				ProgressToken: progressToken,
				Progress:      float64(i + 1),
				Total:         total,
				Meta: mcp.Meta{
					metaKeyTraceparent:    tp.String(),
					metaKeyStreamChunk:    json.RawMessage(enc),
					metaKeyStreamSequence: i,
				},
			})
			if notifyErr != nil {
				return mcpErrorResult(tp.String(), agent, "gateway.internal",
					"send progress notification: "+notifyErr.Error(), nil, 1), nil
			}
		}
	}

	resp := envelope.ToolResponse{
		EnvelopeVersion: envelope.Version,
		Kind:            envelope.KindToolResponse,
		Agent:           agent.ID,
		SessionID:       args.SessionID,
		Traceparent:     tp.String(),
		Input:           envelope.Input{Role: "user", Content: args.Text},
		ToolChoice:      args.ToolChoice,
		Output:          aggResult.Text,
		Redactions:      toEnvelopeRedactions(aggResult.Hits),
		Usage:           toEnvelopeUsage(parsed.Usage),
		FinishReason:    mapFinishReason(parsed.FinishReason),
	}
	if err := envelope.Validate(resp); err != nil {
		return mcpErrorResult(tp.String(), agent, "envelope.invalid",
			"emitted envelope failed schema validation: "+err.Error(), nil, 1), nil
	}
	enc, err := json.Marshal(resp)
	if err != nil {
		return mcpErrorResult(tp.String(), agent, "envelope.invalid",
			"marshal envelope: "+err.Error(), nil, 1), nil
	}
	return &mcp.CallToolResult{
		Meta:              map[string]any{metaKeyTraceparent: tp.String()},
		Content:           []mcp.Content{&mcp.TextContent{Text: string(enc)}},
		StructuredContent: resp,
	}, nil
}

// buildStreamingChunks turns the per-chunk + aggregate redactor passes
// into the ordered slice of ToolStreamChunk envelopes the streaming
// emitter ships as MCP progress notifications. The boundary-safety
// reasoning is the same as cmd/clawctl/stream.go: when per-chunk
// redaction sums to the aggregate-redacted text we emit one envelope per
// chunk, otherwise we coalesce into a single chunk carrying the
// boundary-safe redacted aggregate.
func buildStreamingChunks(agentID, sessionID, traceparent string, perChunk []redact.Result, aggResult redact.Result, perChunkConcat string) []envelope.ToolStreamChunk {
	if perChunkConcat == aggResult.Text {
		out := make([]envelope.ToolStreamChunk, 0, len(perChunk))
		for i, c := range perChunk {
			out = append(out, envelope.ToolStreamChunk{
				EnvelopeVersion: envelope.Version,
				Kind:            envelope.KindToolStreamChunk,
				Agent:           agentID,
				SessionID:       sessionID,
				Traceparent:     traceparent,
				Index:           i,
				Delta:           envelope.Delta{Content: c.Text},
				Redactions:      toEnvelopeRedactions(c.Hits),
				FinishReason:    nil,
			})
		}
		return out
	}
	return []envelope.ToolStreamChunk{{
		EnvelopeVersion: envelope.Version,
		Kind:            envelope.KindToolStreamChunk,
		Agent:           agentID,
		SessionID:       sessionID,
		Traceparent:     traceparent,
		Index:           0,
		Delta:           envelope.Delta{Content: aggResult.Text},
		Redactions:      toEnvelopeRedactions(aggResult.Hits),
		FinishReason:    nil,
	}}
}

// mcpProgressToken pulls the progressToken the client supplied on the
// initial tools/call. Per the MCP spec the server MUST NOT send progress
// notifications when no token is set; nil is the explicit "do not
// notify" signal that suppresses the per-chunk emission.
func mcpProgressToken(req *mcp.CallToolRequest) any {
	if req == nil || req.Params == nil {
		return nil
	}
	return req.Params.GetProgressToken()
}

// mcpErrorResult builds a CallToolResult carrying a v1 ToolError
// envelope. Pass an empty traceparent only when trace generation
// itself failed; in that path the schema's "required: traceparent"
// constraint is intentionally violated and the field is dropped from
// the envelope by sending it as a non-validated marshal — but in
// practice trace.New only fails on a broken entropy source, which we
// surface through the message.
func mcpErrorResult(tp string, agent mcpserver.Agent, code, msg string, httpStatus *int, exit int) *mcp.CallToolResult {
	exitCopy := exit
	te := envelope.ToolError{
		EnvelopeVersion: envelope.Version,
		Kind:            envelope.KindToolError,
		Agent:           agent.ID,
		Traceparent:     tp,
		Code:            code,
		Message:         msg,
		HTTPStatus:      httpStatus,
		ExitCode:        &exitCopy,
	}
	// Best-effort marshal: if it fails we still emit a TextContent
	// with the message so the client sees something useful.
	encoded, err := json.Marshal(te)
	text := string(encoded)
	if err != nil {
		text = fmt.Sprintf(`{"envelope_version":"1","kind":"tool_error","code":%q,"message":%q}`, code, msg)
	}
	return &mcp.CallToolResult{
		Meta:              map[string]any{metaKeyTraceparent: tp},
		IsError:           true,
		Content:           []mcp.Content{&mcp.TextContent{Text: text}},
		StructuredContent: te,
	}
}

// classifyMCPGatewayError maps a transport error to the typed envelope
// error code, a human one-liner, an optional HTTP status, and the
// matching clawctl exit code (kept on the envelope so callers that
// re-shell to clawctl get a deterministic exit). The mapping mirrors
// the curl-aligned contract documented in `clawctl help`.
func classifyMCPGatewayError(err error, cfg config.Config) (code, msg string, httpStatus *int, exit int) {
	var httpErr *api.HTTPError
	if errors.As(err, &httpErr) {
		status := httpErr.StatusCode
		mapped := mapHTTPStatusToErrorCode(status)
		out := fmt.Sprintf("gateway error: HTTP %d", status)
		if extra := decodeGatewayErrorMessage(httpErr.Body); extra != "" {
			out = out + " — " + extra
		}
		return mapped, out, &status, 22
	}
	var dnsErr *api.DNSError
	if errors.As(err, &dnsErr) {
		return "transport.dns", "DNS resolution failed for " + cfg.Host, nil, 6
	}
	var refErr *api.ConnRefusedError
	if errors.As(err, &refErr) {
		return "transport.connection_refused", "connection refused: " + cfg.Host, nil, 7
	}
	var toErr *api.TimeoutError
	if errors.As(err, &toErr) {
		return "transport.timeout",
			fmt.Sprintf("timeout (%ds) calling %s", int(cfg.Timeout.Seconds()), cfg.Host),
			nil, 28
	}
	if errors.Is(err, keychain.ErrNotFound) {
		return "gateway.unauthorized",
			fmt.Sprintf("keychain item %q not found; add a token with: security add-generic-password -s %s -a $USER -w",
				cfg.KeychainService, cfg.KeychainService),
			nil, 2
	}
	return "gateway.internal", err.Error(), nil, 1
}

// mapHTTPStatusToErrorCode picks the v1 envelope ErrorCode enum value
// for a gateway HTTP status. Falls back to gateway.bad_request for
// unknown 4xx values and gateway.internal for unknown 5xx values so
// the field always validates against the schema.
func mapHTTPStatusToErrorCode(status int) string {
	switch status {
	case 400:
		return "gateway.bad_request"
	case 401:
		return "gateway.unauthorized"
	case 403:
		return "gateway.forbidden"
	case 404:
		return "gateway.not_found"
	case 429:
		return "gateway.rate_limited"
	case 500:
		return "gateway.internal"
	case 502, 503, 504:
		return "gateway.upstream_unavailable"
	}
	if status >= 500 {
		return "gateway.internal"
	}
	return "gateway.bad_request"
}
