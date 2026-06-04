package envelope_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tomstagl/clawctl/internal/envelope"
)

func repoRoot(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	dir := wd
	for i := 0; i < 8; i++ {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	t.Fatalf("could not locate repo root from %s", wd)
	return ""
}

func loadFixture(t *testing.T, name string) []byte {
	t.Helper()
	p := filepath.Join(repoRoot(t), "test", "fixtures", "envelope", name)
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("read %s: %v", p, err)
	}
	return b
}

func validateRaw(t *testing.T, raw []byte) {
	t.Helper()
	var doc any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if err := envelope.Validate(doc); err != nil {
		t.Fatalf("validate: %v", err)
	}
}

func TestValidateHappyResponseFixture(t *testing.T) {
	validateRaw(t, loadFixture(t, "happy.json"))
}

func TestValidateRedactedResponseFixture(t *testing.T) {
	validateRaw(t, loadFixture(t, "redacted.json"))
}

func TestValidateErrorFixture(t *testing.T) {
	validateRaw(t, loadFixture(t, "error.json"))
}

func TestValidateStreamingFixture(t *testing.T) {
	raw := loadFixture(t, "streaming.ndjson")
	lines := strings.Split(strings.TrimRight(string(raw), "\n"), "\n")
	if len(lines) < 4 {
		t.Fatalf("expected at least 4 NDJSON lines, got %d", len(lines))
	}
	for i, line := range lines {
		if line == "" {
			continue
		}
		var doc any
		if err := json.Unmarshal([]byte(line), &doc); err != nil {
			t.Fatalf("line %d: parse: %v", i, err)
		}
		if err := envelope.Validate(doc); err != nil {
			t.Fatalf("line %d: validate: %v", i, err)
		}
	}
}

func TestValidateStructToolResponse(t *testing.T) {
	resp := envelope.ToolResponse{
		EnvelopeVersion: envelope.Version,
		Kind:            envelope.KindToolResponse,
		Agent:           "openclaw/concierge",
		Traceparent:     "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01",
		Input:           envelope.Input{Role: "user", Content: "hello"},
		Redactions:      []envelope.Redaction{},
		Usage:           envelope.Usage{InputTokens: 1, OutputTokens: 2, TotalTokens: 3},
		FinishReason:    "stop",
	}
	if err := envelope.Validate(resp); err != nil {
		t.Fatalf("ToolResponse: %v", err)
	}
}

// TestValidateStructWithTaskID asserts the additive A2A-aligned task_id field
// validates against the v1 schema on both the response and stream-chunk shapes.
func TestValidateStructWithTaskID(t *testing.T) {
	resp := envelope.ToolResponse{
		EnvelopeVersion: envelope.Version,
		Kind:            envelope.KindToolResponse,
		Agent:           "openclaw/concierge",
		TaskID:          "4bf92f3577b34da6a3ce929d0e0e4736",
		Traceparent:     "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01",
		Input:           envelope.Input{Role: "user", Content: "hello"},
		Redactions:      []envelope.Redaction{},
		Usage:           envelope.Usage{},
		FinishReason:    "stop",
	}
	if err := envelope.Validate(resp); err != nil {
		t.Fatalf("ToolResponse with task_id: %v", err)
	}
	chunk := envelope.ToolStreamChunk{
		EnvelopeVersion: envelope.Version,
		Kind:            envelope.KindToolStreamChunk,
		Agent:           "openclaw/concierge",
		TaskID:          "4bf92f3577b34da6a3ce929d0e0e4736",
		Traceparent:     "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01",
		Index:           0,
		Delta:           envelope.Delta{Content: "hi"},
	}
	if err := envelope.Validate(chunk); err != nil {
		t.Fatalf("ToolStreamChunk with task_id: %v", err)
	}
}

func TestValidateStructToolStreamChunk(t *testing.T) {
	chunk := envelope.ToolStreamChunk{
		EnvelopeVersion: envelope.Version,
		Kind:            envelope.KindToolStreamChunk,
		Agent:           "openclaw/concierge",
		Traceparent:     "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01",
		Index:           0,
		Delta:           envelope.Delta{Content: "hi"},
	}
	if err := envelope.Validate(chunk); err != nil {
		t.Fatalf("ToolStreamChunk: %v", err)
	}
}

func TestValidateStructToolError(t *testing.T) {
	exit := 28
	httpStatus := 504
	e := envelope.ToolError{
		EnvelopeVersion: envelope.Version,
		Kind:            envelope.KindToolError,
		Traceparent:     "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01",
		Code:            "transport.timeout",
		Message:         "gateway did not respond within 60s",
		HTTPStatus:      &httpStatus,
		ExitCode:        &exit,
		Details: map[string]any{
			"endpoint": "/v1/chat/completions",
			"method":   "POST",
		},
	}
	if err := envelope.Validate(e); err != nil {
		t.Fatalf("ToolError: %v", err)
	}
}

func TestValidateStructRedactedResponse(t *testing.T) {
	off := 13
	one := 1
	resp := envelope.ToolResponse{
		EnvelopeVersion: envelope.Version,
		Kind:            envelope.KindToolResponse,
		Agent:           "openclaw/scout",
		Traceparent:     "00-cccccccccccccccccccccccccccccccc-dddddddddddddddd-01",
		Input:           envelope.Input{Role: "user", Content: "echo my token"},
		Output:          "Echoing back: dt0c01.[REDACTED]",
		Redactions: []envelope.Redaction{
			{Kind: "dt0c01", OffsetHint: &off, Count: &one},
			{Kind: "gw_token", Count: &one},
		},
		Usage:        envelope.Usage{InputTokens: 24, OutputTokens: 29, TotalTokens: 53},
		FinishReason: "stop",
	}
	if err := envelope.Validate(resp); err != nil {
		t.Fatalf("redacted ToolResponse: %v", err)
	}
}

func TestValidateRejectsUnknownVersion(t *testing.T) {
	bad := map[string]any{
		"envelope_version": "2",
		"kind":             "tool_response",
		"agent":            "openclaw/concierge",
		"traceparent":      "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01",
		"input":            map[string]any{"content": "hi"},
		"redactions":       []any{},
		"usage":            map[string]any{},
		"finish_reason":    "stop",
	}
	if err := envelope.Validate(bad); err == nil {
		t.Fatal("expected validation to fail for envelope_version=2")
	}
}

func TestValidateRejectsMissingRequiredField(t *testing.T) {
	// Missing traceparent on a ToolResponse must fail.
	bad := map[string]any{
		"envelope_version": "1",
		"kind":             "tool_response",
		"agent":            "openclaw/concierge",
		"input":            map[string]any{"content": "hi"},
		"redactions":       []any{},
		"usage":            map[string]any{},
		"finish_reason":    "stop",
	}
	if err := envelope.Validate(bad); err == nil {
		t.Fatal("expected validation to fail when traceparent is missing")
	}
}

func TestValidateRejectsBogusEnvelopeKind(t *testing.T) {
	bad := map[string]any{
		"envelope_version": "1",
		"kind":             "tool_handshake",
		"agent":            "openclaw/concierge",
		"traceparent":      "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01",
		"input":            map[string]any{"content": "hi"},
	}
	if err := envelope.Validate(bad); err == nil {
		t.Fatal("expected validation to fail for unknown envelope kind")
	}
}
