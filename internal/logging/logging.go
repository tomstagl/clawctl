// Package logging provides per-call telemetry for clawctl subcommands.
//
// Two modes are supported, switched via CLAWCTL_LOG:
//
//   - default ("")   — pass-through. The legacy human-friendly stderr
//     lines (trace-id, gateway error summaries, redaction WARNINGs)
//     reach the terminal exactly as the bash entrypoint emits them.
//   - "json"         — structured. The same human-friendly lines are
//     suppressed and a single JSON object is written to the real stderr
//     when the call ends.
//
// In JSON mode the suppression is total: callers continue to write to
// the io.Writer returned by Logger.Stderr(), but those writes are
// silently discarded so the only line on stderr is the structured
// record. Pipelines that consume stderr (jq, jaeger ingestion) get a
// clean, single-line stream.
//
// Field set (US-024 acceptance):
//
//	ts                ISO-8601 UTC, nanosecond precision
//	subcommand        e.g. "msg", "health"
//	transport         "api" | "ssh" | "local"
//	traceparent       W3C header value (omitted when no HTTP call ran)
//	agent             agent slug (omitted when not chat-shaped)
//	latency_ms        wall-clock from logging.New to Logger.Finish
//	exit_code         the exit code returned to the caller
//	redactions_count  total per-hit redactor matches across the call
//
// All string-valued fields run through internal/redact before
// marshalling so a stray secret captured in (for example) an agent
// slug never leaks via the log line.
package logging

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"time"

	"github.com/tomstagl/clawctl/internal/redact"
)

// Mode picks the stderr emission strategy.
type Mode int

const (
	// ModeHuman keeps the legacy stderr lines.
	ModeHuman Mode = iota
	// ModeJSON suppresses legacy lines and emits one JSON object on Finish.
	ModeJSON
)

// Transport identifies the backend the subcommand used. The set is
// closed (api|ssh|local) so consumers can branch on it deterministically.
type Transport string

const (
	TransportAPI   Transport = "api"
	TransportSSH   Transport = "ssh"
	TransportLocal Transport = "local"
)

// Logger captures structured data for one subcommand invocation.
//
// A zero-value Logger is unsafe; always construct via New. Methods are
// safe to call on a nil receiver so subcommands can pass the logger
// around without nil-guarding at every call site (useful in tests that
// don't care about the JSON line).
type Logger struct {
	mode      Mode
	out       io.Writer
	humanOut  io.Writer
	started   time.Time
	now       func() time.Time // injectable for tests
	rec       record
	gwToken   string
}

// record is the on-the-wire JSON shape. Field order is the struct
// definition order so the marshalled output is stable across runs.
type record struct {
	Ts              string `json:"ts"`
	Subcommand      string `json:"subcommand"`
	Transport       string `json:"transport"`
	Traceparent     string `json:"traceparent,omitempty"`
	Agent           string `json:"agent,omitempty"`
	LatencyMs       int64  `json:"latency_ms"`
	ExitCode        int    `json:"exit_code"`
	RedactionsCount int    `json:"redactions_count"`
}

// New returns a Logger configured for the given subcommand. modeStr
// comes from CLAWCTL_LOG: anything but "json" stays in human mode (the
// bash entrypoint has the same loose contract). stderr is the real
// stderr writer; in JSON mode it receives only the final record line.
func New(modeStr string, stderr io.Writer, sub string, t Transport) *Logger {
	mode := ModeHuman
	if modeStr == "json" {
		mode = ModeJSON
	}
	humanOut := stderr
	if mode == ModeJSON {
		humanOut = io.Discard
	}
	return &Logger{
		mode:     mode,
		out:      stderr,
		humanOut: humanOut,
		started:  time.Now(),
		now:      time.Now,
		rec: record{
			Subcommand: sub,
			Transport:  string(t),
		},
	}
}

// Stderr returns the writer subcommands should pass to functions that
// emit human-readable stderr (trace-id, error summaries, WARNINGs). In
// human mode this is the real stderr; in JSON mode it is io.Discard,
// so legacy lines are suppressed without per-call-site branches.
func (l *Logger) Stderr() io.Writer {
	if l == nil {
		return io.Discard
	}
	return l.humanOut
}

// Mode reports the configured emission mode (useful for tests).
func (l *Logger) Mode() Mode {
	if l == nil {
		return ModeHuman
	}
	return l.mode
}

// SetTraceparent records the W3C traceparent for the call. Safe to
// call once a header has been generated; later calls overwrite.
func (l *Logger) SetTraceparent(tp string) {
	if l == nil {
		return
	}
	l.rec.Traceparent = tp
}

// SetAgent records the agent slug (msg/stream subcommands).
func (l *Logger) SetAgent(a string) {
	if l == nil {
		return
	}
	l.rec.Agent = a
}

// SetTransport overrides the transport tag. Useful when a subcommand
// can route over more than one backend (none today, but cli could add
// a fast-path that bypasses ssh in future).
func (l *Logger) SetTransport(t Transport) {
	if l == nil {
		return
	}
	l.rec.Transport = string(t)
}

// SetGwToken provides the gateway bearer for literal-substring
// redaction of log values. Mirrors the response-redaction wiring so a
// token captured in any logged field is masked the same way.
func (l *Logger) SetGwToken(tok string) {
	if l == nil {
		return
	}
	l.gwToken = tok
}

// AddRedactions adds n hits to the running total for this call. Pass
// len(result.Hits) after each redact.Apply to keep the count accurate.
func (l *Logger) AddRedactions(n int) {
	if l == nil || n <= 0 {
		return
	}
	l.rec.RedactionsCount += n
}

// Finish records the exit code, emits the JSON record (in JSON mode),
// and returns code unchanged. Subcommands typically write
// `return log.Finish(code)` so the call site stays a one-liner.
func (l *Logger) Finish(code int) int {
	if l == nil {
		return code
	}
	l.rec.ExitCode = code
	l.rec.Ts = l.started.UTC().Format(time.RFC3339Nano)
	l.rec.LatencyMs = l.now().Sub(l.started).Milliseconds()
	if l.rec.LatencyMs < 0 {
		l.rec.LatencyMs = 0
	}
	if l.mode != ModeJSON {
		return code
	}
	r := l.rec
	r.redact(l.gwToken)
	// Disable HTML escaping so the U+003C in `<REDACTED:…>` markers
	// stays a literal `<`, matching the response-redaction surface
	// (envelope.redactions and bash _redact stdout). The trailing
	// newline json.Encoder appends terminates the NDJSON record.
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(&r); err != nil {
		// Marshal failure on a fixed shape is implausible; fall back to
		// a one-shot human line so the call still surfaces an exit.
		fmt.Fprintf(l.out, "logging: marshal failed: %v\n", err)
		return code
	}
	_, _ = l.out.Write(buf.Bytes())
	return code
}

// redact runs every string-valued field through the response-redaction
// pipeline. Subcommand and transport are constants from a closed set
// and won't ever match, but applying uniformly is cheaper than
// branching and keeps the policy "all logged strings are redacted"
// trivially auditable.
func (r *record) redact(gw string) {
	apply := func(s string) string {
		if s == "" {
			return s
		}
		return redact.Apply(s, redact.Options{GwToken: gw}).Text
	}
	r.Subcommand = apply(r.Subcommand)
	r.Transport = apply(r.Transport)
	r.Traceparent = apply(r.Traceparent)
	r.Agent = apply(r.Agent)
}
