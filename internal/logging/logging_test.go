package logging

import (
	"bytes"
	"encoding/json"
	"io"
	"strings"
	"testing"
	"time"
)

// fixedNow returns a now() function that advances by step on each call,
// starting from t0. Useful for asserting latency_ms determinism.
func fixedNow(t0 time.Time, step time.Duration) func() time.Time {
	cur := t0
	return func() time.Time {
		out := cur
		cur = cur.Add(step)
		return out
	}
}

func TestNew_HumanMode_PassesThroughStderr(t *testing.T) {
	var stderr bytes.Buffer
	log := New("", &stderr, "health", TransportAPI)
	if log.Mode() != ModeHuman {
		t.Fatalf("mode = %v, want ModeHuman", log.Mode())
	}
	if log.Stderr() != &stderr {
		t.Errorf("Stderr() did not return the underlying buffer")
	}
	_, _ = log.Stderr().Write([]byte("hello stderr\n"))
	if got := stderr.String(); got != "hello stderr\n" {
		t.Errorf("stderr write not passed through: %q", got)
	}

	// Finish in human mode emits nothing extra.
	stderr.Reset()
	if code := log.Finish(0); code != 0 {
		t.Errorf("Finish exit = %d, want 0", code)
	}
	if stderr.Len() != 0 {
		t.Errorf("Finish in human mode wrote to stderr: %q", stderr.String())
	}
}

func TestNew_JSONMode_DiscardsHumanWritesAndEmitsRecord(t *testing.T) {
	var stderr bytes.Buffer
	log := New("json", &stderr, "msg", TransportAPI)
	if log.Mode() != ModeJSON {
		t.Fatalf("mode = %v, want ModeJSON", log.Mode())
	}
	if log.Stderr() != io.Discard {
		t.Errorf("Stderr() in JSON mode should be io.Discard")
	}

	// Inject a deterministic clock so latency is testable.
	t0 := time.Date(2026, 5, 10, 12, 0, 0, 0, time.UTC)
	log.started = t0
	log.now = fixedNow(t0.Add(7*time.Millisecond), 0)

	log.SetTraceparent("00-aabbccddeeff00112233445566778899-0011223344556677-01")
	log.SetAgent("openclaw/default")
	log.AddRedactions(2)

	// Writes through Stderr() must vanish in JSON mode.
	_, _ = log.Stderr().Write([]byte("this should be discarded\n"))

	if code := log.Finish(0); code != 0 {
		t.Errorf("Finish exit = %d, want 0", code)
	}

	out := stderr.String()
	if !strings.HasSuffix(out, "\n") {
		t.Errorf("output missing trailing newline: %q", out)
	}
	// One line, no human-text leak.
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if len(lines) != 1 {
		t.Fatalf("expected exactly one line, got %d: %q", len(lines), out)
	}

	var rec map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &rec); err != nil {
		t.Fatalf("unmarshal: %v\nline=%q", err, lines[0])
	}

	// Required keys (per US-024 acceptance: ts, subcommand, transport,
	// latency_ms, exit_code, redactions_count). traceparent and agent
	// were set so they must appear too.
	required := []string{"ts", "subcommand", "transport", "traceparent", "agent", "latency_ms", "exit_code", "redactions_count"}
	for _, k := range required {
		if _, ok := rec[k]; !ok {
			t.Errorf("field %q missing from %v", k, rec)
		}
	}

	if got, want := rec["subcommand"], "msg"; got != want {
		t.Errorf("subcommand = %v, want %v", got, want)
	}
	if got, want := rec["transport"], "api"; got != want {
		t.Errorf("transport = %v, want %v", got, want)
	}
	if got, want := rec["agent"], "openclaw/default"; got != want {
		t.Errorf("agent = %v, want %v", got, want)
	}
	if got, want := rec["traceparent"], "00-aabbccddeeff00112233445566778899-0011223344556677-01"; got != want {
		t.Errorf("traceparent = %v, want %v", got, want)
	}
	if got, want := rec["exit_code"], float64(0); got != want {
		t.Errorf("exit_code = %v, want %v", got, want)
	}
	if got, want := rec["redactions_count"], float64(2); got != want {
		t.Errorf("redactions_count = %v, want %v", got, want)
	}
	// latency_ms is non-negative; with our fake clock it's exactly 7.
	if got, want := rec["latency_ms"], float64(7); got != want {
		t.Errorf("latency_ms = %v, want %v", got, want)
	}
	// ts parses as RFC3339.
	tsStr, _ := rec["ts"].(string)
	if _, err := time.Parse(time.RFC3339Nano, tsStr); err != nil {
		t.Errorf("ts not RFC3339Nano: %v (got %q)", err, tsStr)
	}
}

func TestFinish_JSONMode_FailedCallShape(t *testing.T) {
	var stderr bytes.Buffer
	log := New("json", &stderr, "health", TransportAPI)
	t0 := time.Date(2026, 5, 10, 12, 0, 0, 0, time.UTC)
	log.started = t0
	log.now = fixedNow(t0.Add(13*time.Millisecond), 0)

	// No traceparent/agent set — DNS-class failures fail before the
	// header is generated. Field set must reflect that.
	if code := log.Finish(7); code != 7 {
		t.Errorf("Finish exit = %d, want 7", code)
	}
	out := strings.TrimRight(stderr.String(), "\n")

	var rec map[string]any
	if err := json.Unmarshal([]byte(out), &rec); err != nil {
		t.Fatalf("unmarshal: %v\nline=%q", err, out)
	}

	for _, k := range []string{"ts", "subcommand", "transport", "latency_ms", "exit_code", "redactions_count"} {
		if _, ok := rec[k]; !ok {
			t.Errorf("required field %q missing from failed-call record: %v", k, rec)
		}
	}
	for _, k := range []string{"traceparent", "agent"} {
		if _, ok := rec[k]; ok {
			t.Errorf("optional field %q should be omitted when unset: %v", k, rec)
		}
	}
	if got, want := rec["exit_code"], float64(7); got != want {
		t.Errorf("exit_code = %v, want %v", got, want)
	}
	if got, want := rec["redactions_count"], float64(0); got != want {
		t.Errorf("redactions_count = %v, want %v", got, want)
	}
	if got, want := rec["transport"], "api"; got != want {
		t.Errorf("transport = %v, want %v", got, want)
	}
}

func TestFinish_JSONMode_RedactsLogValues(t *testing.T) {
	var stderr bytes.Buffer
	log := New("json", &stderr, "msg", TransportAPI)
	t0 := time.Date(2026, 5, 10, 12, 0, 0, 0, time.UTC)
	log.started = t0
	log.now = fixedNow(t0.Add(time.Millisecond), 0)

	// An agent slug with a syntactic GitHub token in it (contrived but
	// the redactor sees strings, not semantics) — the field set should
	// still mask the secret pattern in the JSON record.
	log.SetAgent("openclaw/ghp_abcdefghijklmnopqrstuvwxyz0123456789")
	if code := log.Finish(0); code != 0 {
		t.Errorf("Finish exit = %d, want 0", code)
	}

	line := strings.TrimRight(stderr.String(), "\n")
	if strings.Contains(line, "ghp_abcdefghijklmnopqrstuvwxyz0123456789") {
		t.Errorf("raw secret leaked into log: %s", line)
	}
	if !strings.Contains(line, "<REDACTED:gh_token:") {
		t.Errorf("expected redaction marker in log: %s", line)
	}
}

func TestFinish_JSONMode_GwTokenRedacted(t *testing.T) {
	var stderr bytes.Buffer
	log := New("json", &stderr, "msg", TransportAPI)
	t0 := time.Date(2026, 5, 10, 12, 0, 0, 0, time.UTC)
	log.started = t0
	log.now = fixedNow(t0.Add(time.Millisecond), 0)

	gw := "supersecretgatewaybearer1234567890"
	log.SetGwToken(gw)
	// Embed the literal in an otherwise-innocuous field.
	log.SetAgent("openclaw/" + gw)

	_ = log.Finish(0)
	line := strings.TrimRight(stderr.String(), "\n")
	if strings.Contains(line, gw) {
		t.Errorf("gateway bearer leaked into log: %s", line)
	}
	if !strings.Contains(line, "<REDACTED:gw_token:") {
		t.Errorf("expected gw_token redaction marker: %s", line)
	}
}

func TestNilLogger_NoPanics(t *testing.T) {
	var l *Logger
	// Every method is no-op on nil. The Stderr writer routes to
	// io.Discard so call sites can omit nil-guards.
	if l.Stderr() != io.Discard {
		t.Errorf("nil.Stderr() should return io.Discard")
	}
	if l.Mode() != ModeHuman {
		t.Errorf("nil.Mode() should return ModeHuman")
	}
	l.SetTraceparent("x")
	l.SetAgent("y")
	l.SetTransport(TransportSSH)
	l.SetGwToken("z")
	l.AddRedactions(5)
	if code := l.Finish(42); code != 42 {
		t.Errorf("nil.Finish should pass through code, got %d", code)
	}
}

func TestAddRedactions_AccumulatesAndIgnoresNonPositive(t *testing.T) {
	var stderr bytes.Buffer
	log := New("json", &stderr, "msg", TransportAPI)
	t0 := time.Date(2026, 5, 10, 12, 0, 0, 0, time.UTC)
	log.started = t0
	log.now = fixedNow(t0, 0)

	log.AddRedactions(0)
	log.AddRedactions(-1)
	log.AddRedactions(3)
	log.AddRedactions(2)
	_ = log.Finish(0)

	var rec map[string]any
	_ = json.Unmarshal([]byte(strings.TrimRight(stderr.String(), "\n")), &rec)
	if got, want := rec["redactions_count"], float64(5); got != want {
		t.Errorf("redactions_count = %v, want %v", got, want)
	}
}
