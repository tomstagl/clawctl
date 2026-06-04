package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/tomstagl/clawctl/internal/config"
	"github.com/tomstagl/clawctl/internal/envelope"
	"github.com/tomstagl/clawctl/internal/keychain"
	"github.com/tomstagl/clawctl/internal/logging"
	"github.com/tomstagl/clawctl/internal/redact"
	"github.com/tomstagl/clawctl/internal/trace"
	"github.com/tomstagl/clawctl/internal/transport/api"
)

// runMsg implements `clawctl msg [-s SESSION] [--text] AGENT [TEXT]`. Unlike
// the bash entrypoint — which emits plain text by default and JSON only with
// --envelope — the typed Go binary emits a v1 ToolResponse envelope by
// default and falls back to plain text only with --text. This is the
// US-018 contract: local LLM callers register clawctl as a typed-tool
// surface, so the structured shape is the load-bearing case.
//
// Redaction is applied to the gateway response before either format is
// emitted, and (in envelope mode) the per-hit list flows into
// ToolResponse.redactions[] so callers can branch on the slice without
// parsing stderr. The stderr WARNING + audit-file append still fire so
// human users keep the legacy signal (US-008 contract).
func runMsg(ctx context.Context, cfg config.Config, args []string, stdin io.Reader, stdout, stderr io.Writer) (code int) {
	log := logging.New(cfg.Log, stderr, "msg", logging.TransportAPI)
	defer func() { code = log.Finish(code) }()
	stderr = log.Stderr()

	if cfg.Host == "" {
		fmt.Fprintln(stderr, "clawctl: CLAWCTL_HOST not set. Export it (e.g. export CLAWCTL_HOST=http://your-openclaw-host:18789).")
		if cfg.JSONOutput {
			_ = writeJSONErr(stdout, "msg", 2, "CLAWCTL_HOST not set", "")
		}
		return 2
	}

	flags, rest, code := parseMsgArgs(args, stderr)
	if code != 0 {
		if cfg.JSONOutput {
			_ = writeJSONErr(stdout, "msg", code, "flag parse error", "")
		}
		return code
	}
	if len(rest) == 0 {
		fmt.Fprintln(stderr, "usage: clawctl msg [-s <session-key>] [--text] <agent> [<text>]   (text from stdin if omitted)")
		fmt.Fprintln(stderr, "       agent = 'default' or a specific agent slug")
		if cfg.JSONOutput {
			_ = writeJSONErr(stdout, "msg", 2, "missing agent argument", "")
		}
		return 2
	}
	agent := rest[0]
	log.SetAgent("openclaw/" + agent)
	var text string
	if len(rest) > 1 {
		text = strings.Join(rest[1:], " ")
	} else {
		b, err := io.ReadAll(stdin)
		if err != nil {
			fmt.Fprintf(stderr, "clawctl: read stdin: %v\n", err)
			return 1
		}
		text = string(b)
	}

	tp, err := trace.New()
	if err != nil {
		fmt.Fprintf(stderr, "clawctl: %v\n", err)
		return 1
	}
	log.SetTraceparent(tp.String())
	fmt.Fprintf(stderr, "trace-id: %s\n", tp.TraceID)

	tokenSource := keychainTokenSource(cfg)
	client := api.New(cfg.Host, cfg.Timeout, tokenSource)
	if cfg.MaxResponseBytes > 0 {
		client.MaxResponseBytes = cfg.MaxResponseBytes
	}

	payload, err := buildChatPayload(agent, text, flags.session, false)
	if err != nil {
		fmt.Fprintf(stderr, "clawctl: %v\n", err)
		return 1
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
		if cfg.JSONOutput {
			code, msg := msgAPIErrorDetails(cfg, err)
			fmt.Fprintln(stderr, "clawctl: "+msg)
			_ = writeJSONErr(stdout, "msg", code, msg, tp.TraceID)
			return code
		}
		return reportMsgError(cfg, err, stderr)
	}

	parsed, perr := parseChatResponse(body)
	if perr != nil {
		fmt.Fprintf(stderr, "clawctl: invalid response from %s/v1/chat/completions: %v\n", cfg.Host, perr)
		if cfg.JSONOutput {
			_ = writeJSONErr(stdout, "msg", 1, "invalid response from gateway", tp.TraceID)
		}
		return 1
	}

	gw := readGwToken(cfg)
	log.SetGwToken(gw)
	r := redact.Apply(parsed.Content, redact.Options{GwToken: gw, Disable: cfg.NoRedact})
	log.AddRedactions(len(r.Hits))

	kinds := r.Kinds()
	if len(kinds) > 0 {
		fmt.Fprintln(stderr, redact.WarnLine(agent, kinds))
		if cfg.CacheDir != "" {
			_ = os.MkdirAll(cfg.CacheDir, 0o755)
			_ = redact.AppendAudit(filepath.Join(cfg.CacheDir, "last-redaction"), agent, kinds)
		}
	}

	if cfg.JSONOutput {
		d := msgJSONData{
			Agent:        "openclaw/" + agent,
			Content:      r.Text,
			FinishReason: mapFinishReason(parsed.FinishReason),
			Usage:        toEnvelopeUsage(parsed.Usage),
			Redactions:   toEnvelopeRedactions(r.Hits),
		}
		data, merr := json.Marshal(d)
		if merr != nil {
			fmt.Fprintf(stderr, "clawctl: marshal msg data: %v\n", merr)
			return 1
		}
		_ = writeJSONOK(stdout, "msg", json.RawMessage(data))
		return 0
	}

	if flags.textOnly {
		// Bash parity: plain msg output is the redacted content followed by a
		// trailing newline (printf '\n' after the pipeline).
		_, _ = io.WriteString(stdout, r.Text)
		_, _ = io.WriteString(stdout, "\n")
		return 0
	}

	resp := envelope.ToolResponse{
		EnvelopeVersion: envelope.Version,
		Kind:            envelope.KindToolResponse,
		Agent:           "openclaw/" + agent,
		SessionID:       flags.session,
		TaskID:          tp.TraceID,
		Traceparent:     tp.String(),
		Input:           envelope.Input{Role: "user", Content: text},
		Output:          r.Text,
		Redactions:      toEnvelopeRedactions(r.Hits),
		Usage:           toEnvelopeUsage(parsed.Usage),
		FinishReason:    mapFinishReason(parsed.FinishReason),
	}
	if err := envelope.Validate(resp); err != nil {
		fmt.Fprintf(stderr, "clawctl: emitted envelope failed schema validation: %v\n", err)
		return 1
	}
	enc, err := json.Marshal(resp)
	if err != nil {
		fmt.Fprintf(stderr, "clawctl: marshal envelope: %v\n", err)
		return 1
	}
	_, _ = stdout.Write(enc)
	_, _ = stdout.Write([]byte{'\n'})
	return 0
}

// msgJSONData is the data shape for the msg command's --json envelope.
// It wraps the ToolResponse core fields without envelope metadata.
type msgJSONData struct {
	Agent        string               `json:"agent"`
	Content      string               `json:"content"`
	FinishReason string               `json:"finish_reason"`
	Usage        envelope.Usage       `json:"usage"`
	Redactions   []envelope.Redaction `json:"redactions"`
}

// msgAPIErrorDetails maps errors the msg command encounters (including keychain)
// to (exitCode, message) without writing to any writer.
func msgAPIErrorDetails(cfg config.Config, err error) (exitCode int, message string) {
	if errors.Is(err, keychain.ErrNotFound) {
		return 2, fmt.Sprintf("keychain item %q not found", cfg.KeychainService)
	}
	return apiErrorDetails(cfg, err)
}

type msgFlags struct {
	session  string
	textOnly bool
}

// parseMsgArgs walks args looking for flags until it hits a positional or
// `--`. Returns (flags, rest, exitCode); exitCode is non-zero only on a
// flag-shape error.
func parseMsgArgs(args []string, stderr io.Writer) (msgFlags, []string, int) {
	var f msgFlags
	i := 0
parseLoop:
	for i < len(args) {
		a := args[i]
		switch {
		case a == "-s" || a == "--session":
			if i+1 >= len(args) {
				fmt.Fprintf(stderr, "clawctl msg: %s requires an argument\n", a)
				return f, nil, 2
			}
			f.session = args[i+1]
			i += 2
		case strings.HasPrefix(a, "-s="):
			f.session = strings.TrimPrefix(a, "-s=")
			i++
		case strings.HasPrefix(a, "--session="):
			f.session = strings.TrimPrefix(a, "--session=")
			i++
		case a == "--text":
			f.textOnly = true
			i++
		case a == "--":
			i++
			break parseLoop
		case strings.HasPrefix(a, "-"):
			fmt.Fprintf(stderr, "clawctl msg: unknown flag %q\n", a)
			return f, nil, 2
		default:
			break parseLoop
		}
	}
	return f, args[i:], 0
}

// chatPayload mirrors the OpenAI-compatible body shape the gateway expects.
// Field order matches bash's `jq -nc` output (model, stream, [user,] messages)
// so request payloads diff cleanly when comparing transports side by side.
type chatPayload struct {
	Model    string        `json:"model"`
	Stream   bool          `json:"stream"`
	User     string        `json:"user,omitempty"`
	Messages []chatMessage `json:"messages"`
}

type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

func buildChatPayload(agent, text, session string, stream bool) ([]byte, error) {
	p := chatPayload{
		Model:    "openclaw/" + agent,
		Stream:   stream,
		User:     session,
		Messages: []chatMessage{{Role: "user", Content: text}},
	}
	return json.Marshal(p)
}

// chatResponse is the subset of the OpenAI chat-completions reply we need:
// the first choice's content + finish_reason and the gateway-reported usage
// counts. Anything else is intentionally discarded so we don't pretend to
// understand fields we don't.
type chatResponse struct {
	Content      string
	FinishReason string
	Usage        chatUsage
}

type chatUsage struct {
	PromptTokens     *int
	CompletionTokens *int
	TotalTokens      *int
}

func parseChatResponse(body []byte) (chatResponse, error) {
	var raw struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
		Usage struct {
			PromptTokens     *int `json:"prompt_tokens"`
			CompletionTokens *int `json:"completion_tokens"`
			TotalTokens      *int `json:"total_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return chatResponse{}, err
	}
	r := chatResponse{
		Usage: chatUsage{
			PromptTokens:     raw.Usage.PromptTokens,
			CompletionTokens: raw.Usage.CompletionTokens,
			TotalTokens:      raw.Usage.TotalTokens,
		},
	}
	if len(raw.Choices) > 0 {
		r.Content = raw.Choices[0].Message.Content
		r.FinishReason = raw.Choices[0].FinishReason
	}
	return r, nil
}

// mapFinishReason normalises gateway-reported finish reasons onto the
// FinishReason enum in the v1 envelope. Mirrors the bash case-statement so
// envelopes from either binary share a vocabulary.
func mapFinishReason(raw string) string {
	switch raw {
	case "stop", "length", "content_filter", "error":
		return raw
	case "tool_calls", "function_call", "tool_call":
		return "tool_call"
	default:
		return "stop"
	}
}

// toEnvelopeRedactions promotes redact.Hit values to envelope.Redaction. We
// always return a non-nil slice so the JSON marshals as `[]` (the schema
// requires the field; nil would marshal as `null`).
func toEnvelopeRedactions(hits []redact.Hit) []envelope.Redaction {
	if len(hits) == 0 {
		return []envelope.Redaction{}
	}
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

func toEnvelopeUsage(u chatUsage) envelope.Usage {
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

// reportMsgError mirrors the bash entrypoint's _explain_http_error +
// _chat exit-code dispatch. Errors land on stderr; the body is *not*
// forwarded to stdout (parity with bash msg, where _chat returns
// curl_exit before `cat "$body"` runs).
func reportMsgError(cfg config.Config, err error, stderr io.Writer) int {
	var httpErr *api.HTTPError
	if errors.As(err, &httpErr) {
		fmt.Fprintf(stderr, "gateway error: HTTP %d\n", httpErr.StatusCode)
		// Surface a one-line decoded body when it's a recognised OpenAI-shape
		// error envelope, mirroring bash's `.error.code` / `.error.message`
		// extraction. Not load-bearing for parity (the bash version skips
		// this when those keys are absent), but it makes debugging cheaper.
		if msg := decodeGatewayErrorMessage(httpErr.Body); msg != "" {
			fmt.Fprintf(stderr, "  %s\n", msg)
		}
		return 22
	}
	var dnsErr *api.DNSError
	if errors.As(err, &dnsErr) {
		fmt.Fprintf(stderr, "clawctl: DNS resolution failed for %s\n", cfg.Host)
		return 6
	}
	var refErr *api.ConnRefusedError
	if errors.As(err, &refErr) {
		fmt.Fprintf(stderr, "clawctl: connection refused: %s\n", cfg.Host)
		return 7
	}
	var toErr *api.TimeoutError
	if errors.As(err, &toErr) {
		fmt.Fprintf(stderr, "clawctl: timeout (%ds) calling %s\n", int(cfg.Timeout.Seconds()), cfg.Host)
		return 28
	}
	if errors.Is(err, keychain.ErrNotFound) {
		fmt.Fprintf(stderr, "clawctl: keychain item %q not found. Add a token with: security add-generic-password -s %s -a $USER -w\n",
			cfg.KeychainService, cfg.KeychainService)
		return 2
	}
	fmt.Fprintf(stderr, "clawctl: %v\n", err)
	return api.ExitCode(err)
}

// decodeGatewayErrorMessage extracts a human-readable line from a gateway
// error body. Returns "" when the body isn't OpenAI-shaped.
func decodeGatewayErrorMessage(body []byte) string {
	var env struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		return ""
	}
	switch {
	case env.Error.Code != "" && env.Error.Message != "":
		return "[" + env.Error.Code + "] " + env.Error.Message
	case env.Error.Message != "":
		return env.Error.Message
	case env.Message != "":
		return env.Message
	}
	return ""
}

// msgCmd is the entry-point wrapper used by main(). Threads os.Stdin /
// os.Stdout / os.Stderr and exits with the documented code.
func msgCmd(cfg config.Config, args []string) {
	code := runMsg(context.Background(), cfg, args, os.Stdin, os.Stdout, os.Stderr)
	os.Exit(code)
}
