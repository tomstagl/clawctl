package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/tomstagl/clawctl/internal/config"
	"github.com/tomstagl/clawctl/internal/envelope"
	"github.com/tomstagl/clawctl/internal/logging"
	"github.com/tomstagl/clawctl/internal/redact"
	"github.com/tomstagl/clawctl/internal/sse"
	"github.com/tomstagl/clawctl/internal/trace"
	"github.com/tomstagl/clawctl/internal/transport/api"
)

// runStream implements `clawctl stream [-s SESSION] [--text] AGENT [TEXT]`.
// Like runMsg, the typed Go binary inverts the bash MVP's default: a tool
// envelope is emitted by default (NDJSON: one ToolStreamChunk per non-empty
// SSE delta followed by a terminal ToolResponse) and plain text only with
// --text. The structured shape is the load-bearing case for local LLM
// callers wiring clawctl as a typed tool surface (US-019).
//
// Streaming responses are buffered to memory before redaction so that
// secret patterns can never be split across SSE delta boundaries — same
// trade-off the bash entrypoint makes (see CLAUDE.md "Streaming path").
// We then run two redaction passes: per-chunk, and aggregate. When the
// per-chunk redactions sum to the aggregate result we emit one envelope
// per chunk; when a secret pattern crosses a boundary we coalesce into
// a single envelope carrying the boundary-safe redacted aggregate.
func runStream(ctx context.Context, cfg config.Config, args []string, stdin io.Reader, stdout, stderr io.Writer) (code int) {
	log := logging.New(cfg.Log, stderr, "stream", logging.TransportAPI)
	defer func() { code = log.Finish(code) }()
	stderr = log.Stderr()

	if cfg.Host == "" {
		fmt.Fprintln(stderr, "clawctl: CLAWCTL_HOST not set. Export it (e.g. export CLAWCTL_HOST=http://your-openclaw-host:18789).")
		return 2
	}

	flags, rest, code := parseStreamArgs(args, stderr)
	if code != 0 {
		return code
	}
	if len(rest) == 0 {
		fmt.Fprintln(stderr, "usage: clawctl stream [-s <session-key>] [--text] <agent> [<text>]   (text from stdin if omitted)")
		fmt.Fprintln(stderr, "       agent = 'default' or a specific agent slug")
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

	payload, err := buildChatPayload(agent, text, flags.session, true)
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
		return reportMsgError(cfg, err, stderr)
	}

	parsed, perr := parseSSEStream(body)
	if perr != nil {
		fmt.Fprintf(stderr, "clawctl: parse SSE stream: %v\n", perr)
		return 1
	}
	if parsed.Err != "" {
		fmt.Fprintf(stderr, "clawctl: stream error: %s\n", parsed.Err)
		return 1
	}

	gw := readGwToken(cfg)
	log.SetGwToken(gw)

	if flags.textOnly {
		// Bash parity: buffer the full content, redact once (boundary-safe),
		// emit with a trailing newline.
		agg := strings.Join(parsed.Chunks, "")
		r := redact.Apply(agg, redact.Options{GwToken: gw, Disable: cfg.NoRedact})
		log.AddRedactions(len(r.Hits))
		emitRedactionStderr(cfg, agent, r.Kinds(), stderr)
		_, _ = io.WriteString(stdout, r.Text)
		_, _ = io.WriteString(stdout, "\n")
		return 0
	}

	// Envelope mode (default).
	perChunk := make([]redact.Result, 0, len(parsed.Chunks))
	var perChunkConcat strings.Builder
	for _, c := range parsed.Chunks {
		r := redact.Apply(c, redact.Options{GwToken: gw, Disable: cfg.NoRedact})
		perChunk = append(perChunk, r)
		perChunkConcat.WriteString(r.Text)
	}
	agg := strings.Join(parsed.Chunks, "")
	aggResult := redact.Apply(agg, redact.Options{GwToken: gw, Disable: cfg.NoRedact})
	log.AddRedactions(len(aggResult.Hits))

	// Canonical pass: stderr WARNING + audit-file fire from the aggregate
	// hit set, not the per-chunk passes. This matches the bash entrypoint —
	// human users see one WARNING per call regardless of how many chunks
	// the matched pattern spanned.
	emitRedactionStderr(cfg, agent, aggResult.Kinds(), stderr)

	sessionID := flags.session
	agentSlug := "openclaw/" + agent

	if perChunkConcat.String() == aggResult.Text {
		// Boundary-safe: per-chunk redaction sums to the aggregate-redacted
		// text, so no secret crossed an SSE chunk boundary. Emit per-chunk.
		for i, c := range perChunk {
			chunk := envelope.ToolStreamChunk{
				EnvelopeVersion: envelope.Version,
				Kind:            envelope.KindToolStreamChunk,
				Agent:           agentSlug,
				SessionID:       sessionID,
				Traceparent:     tp.String(),
				Index:           i,
				Delta:           envelope.Delta{Content: c.Text},
				Redactions:      toEnvelopeRedactions(c.Hits),
				FinishReason:    nil,
			}
			if err := emitNDJSON(stdout, chunk); err != nil {
				fmt.Fprintf(stderr, "clawctl: %v\n", err)
				return 1
			}
		}
	} else {
		// A secret pattern spans SSE chunk boundaries — coalesce into a
		// single envelope carrying the boundary-safe redacted aggregate.
		// Emitting per-chunk here would either leak the unredacted bytes
		// (per-chunk redaction missed it) or split the redaction marker
		// across two chunks (consumer reassembly breaks).
		fmt.Fprintln(stderr, "warning: redacted secret pattern crossed SSE chunk boundary; coalesced into one chunk")
		chunk := envelope.ToolStreamChunk{
			EnvelopeVersion: envelope.Version,
			Kind:            envelope.KindToolStreamChunk,
			Agent:           agentSlug,
			SessionID:       sessionID,
			Traceparent:     tp.String(),
			Index:           0,
			Delta:           envelope.Delta{Content: aggResult.Text},
			Redactions:      toEnvelopeRedactions(aggResult.Hits),
			FinishReason:    nil,
		}
		if err := emitNDJSON(stdout, chunk); err != nil {
			fmt.Fprintf(stderr, "clawctl: %v\n", err)
			return 1
		}
	}

	resp := envelope.ToolResponse{
		EnvelopeVersion: envelope.Version,
		Kind:            envelope.KindToolResponse,
		Agent:           agentSlug,
		SessionID:       sessionID,
		Traceparent:     tp.String(),
		Input:           envelope.Input{Role: "user", Content: text},
		Output:          aggResult.Text,
		Redactions:      toEnvelopeRedactions(aggResult.Hits),
		Usage:           toEnvelopeUsage(parsed.Usage),
		FinishReason:    mapFinishReason(parsed.FinishReason),
	}
	if err := envelope.Validate(resp); err != nil {
		fmt.Fprintf(stderr, "clawctl: emitted envelope failed schema validation: %v\n", err)
		return 1
	}
	if err := emitNDJSON(stdout, resp); err != nil {
		fmt.Fprintf(stderr, "clawctl: %v\n", err)
		return 1
	}
	return 0
}

// streamFlags reuses msg's flag shape — -s/--session and --text are the
// only flags either subcommand accepts. A separate struct keeps the
// per-subcommand parser self-documenting at the call site.
type streamFlags struct {
	session  string
	textOnly bool
}

// parseStreamArgs walks args looking for flags until it hits a positional
// or `--`. Returns (flags, rest, exitCode); exitCode is non-zero only on
// a flag-shape error.
func parseStreamArgs(args []string, stderr io.Writer) (streamFlags, []string, int) {
	var f streamFlags
	i := 0
parseLoop:
	for i < len(args) {
		a := args[i]
		switch {
		case a == "-s" || a == "--session":
			if i+1 >= len(args) {
				fmt.Fprintf(stderr, "clawctl stream: %s requires an argument\n", a)
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
			fmt.Fprintf(stderr, "clawctl stream: unknown flag %q\n", a)
			return f, nil, 2
		default:
			break parseLoop
		}
	}
	return f, args[i:], 0
}

// streamParseResult bundles the SSE-parsed view of a buffered chat
// completion stream: ordered non-empty content deltas, the
// final-frame finish_reason and usage block, and any inline error
// captured via an `event: error` SSE frame or an OpenAI-style
// `{"error":...}` payload.
type streamParseResult struct {
	Chunks       []string
	FinishReason string
	Usage        chatUsage
	Err          string
}

// parseSSEStream consumes the buffered SSE response body and extracts
// the OpenAI-shape content deltas, finish reason, and usage counts.
// Mirrors the python heredoc in the bash stream path: skip non-data
// lines, stop at the [DONE] sentinel, accumulate non-empty
// `choices[0].delta.content` strings in order, and remember the last
// `finish_reason` plus the (single, terminal) `usage` block.
//
// Errors surfaced via SSE `event: error` lines or OpenAI-shape
// `{"error":...}` payloads land in result.Err so the caller can
// distinguish a malformed stream from a transport failure.
func parseSSEStream(body []byte) (streamParseResult, error) {
	res := streamParseResult{FinishReason: "stop"}
	parser := sse.New(bytes.NewReader(body))
	for {
		ev, err := parser.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return res, err
		}
		if ev.Type == "error" {
			res.Err = ev.Data
			continue
		}
		payload := ev.Data
		if payload == "" {
			continue
		}
		if payload == "[DONE]" {
			break
		}
		var obj struct {
			Choices []struct {
				Delta struct {
					Content string `json:"content"`
				} `json:"delta"`
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
			Error json.RawMessage `json:"error"`
		}
		if err := json.Unmarshal([]byte(payload), &obj); err != nil {
			// Skip malformed payloads quietly — the bash python heredoc
			// does the same. A persistent malformation surfaces as no
			// chunks and finish_reason=stop, which the caller can
			// distinguish from a healthy empty response by inspecting
			// the SSE blob itself.
			continue
		}
		if obj.Usage.PromptTokens != nil {
			res.Usage.PromptTokens = obj.Usage.PromptTokens
		}
		if obj.Usage.CompletionTokens != nil {
			res.Usage.CompletionTokens = obj.Usage.CompletionTokens
		}
		if obj.Usage.TotalTokens != nil {
			res.Usage.TotalTokens = obj.Usage.TotalTokens
		}
		if len(obj.Choices) == 0 {
			if len(obj.Error) > 0 {
				res.Err = string(obj.Error)
			}
			continue
		}
		c0 := obj.Choices[0]
		if c0.FinishReason != "" {
			res.FinishReason = c0.FinishReason
		}
		content := c0.Delta.Content
		if content == "" {
			content = c0.Message.Content
		}
		if content != "" {
			res.Chunks = append(res.Chunks, content)
		}
	}
	return res, nil
}

// emitNDJSON writes one JSON document followed by '\n' to w. The
// stream subcommand emits NDJSON so each line is independently
// parseable by streaming clients (one chunk per line, terminal
// ToolResponse on the last line).
func emitNDJSON(w io.Writer, v any) error {
	enc, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("marshal envelope: %w", err)
	}
	if _, err := w.Write(enc); err != nil {
		return err
	}
	if _, err := w.Write([]byte{'\n'}); err != nil {
		return err
	}
	return nil
}

// emitRedactionStderr fires the human-facing WARNING line and the
// audit-file append when the redactor saw any matches. Shared between
// the text and envelope code paths so the stderr signal is consistent
// regardless of output mode (US-008 contract).
func emitRedactionStderr(cfg config.Config, agent string, kinds []string, stderr io.Writer) {
	if len(kinds) == 0 {
		return
	}
	fmt.Fprintln(stderr, redact.WarnLine(agent, kinds))
	if cfg.CacheDir != "" {
		_ = os.MkdirAll(cfg.CacheDir, 0o755)
		_ = redact.AppendAudit(filepath.Join(cfg.CacheDir, "last-redaction"), agent, kinds)
	}
}

// streamCmd is the entry-point wrapper used by main(). Threads
// os.Stdin / os.Stdout / os.Stderr and exits with the documented code.
func streamCmd(cfg config.Config, args []string) {
	code := runStream(context.Background(), cfg, args, os.Stdin, os.Stdout, os.Stderr)
	os.Exit(code)
}
