// Package envelope defines Go types for the v1 clawctl tool envelope and
// validates documents against the embedded JSON schema. The schema at
// schemas/envelope.v1.json is the source of truth; the structs here mirror
// it so emitters can build envelopes type-safely without re-stating the
// shape.
package envelope

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sync"

	"github.com/santhosh-tekuri/jsonschema/v5"
	"github.com/tomstagl/clawctl/schemas"
)

const (
	// Version is the literal envelope_version pinned by every v1 document.
	// Consumers MUST reject unknown values rather than guessing.
	Version = "1"

	KindToolRequest     = "tool_request"
	KindToolResponse    = "tool_response"
	KindToolStreamChunk = "tool_stream_chunk"
	KindToolError       = "tool_error"
)

// Input is the user-supplied prompt portion of a request/response envelope.
type Input struct {
	Role    string `json:"role,omitempty"`
	Content string `json:"content"`
}

// Usage echoes the gateway's token accounting. Zero-valued counts are
// elided so the envelope doesn't claim numbers the gateway didn't report.
type Usage struct {
	InputTokens  int `json:"input_tokens,omitempty"`
	OutputTokens int `json:"output_tokens,omitempty"`
	TotalTokens  int `json:"total_tokens,omitempty"`
}

// Redaction records one boundary-redactor hit so consumers can branch on
// envelope.redactions[] instead of parsing stderr.
type Redaction struct {
	Kind       string `json:"kind"`
	OffsetHint *int   `json:"offset_hint,omitempty"`
	Count      *int   `json:"count,omitempty"`
}

// Delta is a single streaming fragment.
type Delta struct {
	Content string `json:"content"`
}

// ToolRequest is the caller -> clawctl shape an LLM emits to invoke an
// openclaw agent as a tool.
type ToolRequest struct {
	EnvelopeVersion string `json:"envelope_version"`
	Kind            string `json:"kind"`
	Agent           string `json:"agent"`
	SessionID       string `json:"session_id,omitempty"`
	Traceparent     string `json:"traceparent"`
	Input           Input  `json:"input"`
	ToolChoice      string `json:"tool_choice,omitempty"`
}

// ToolResponse is the final, non-streaming clawctl -> caller shape.
// Redactions is required (possibly empty) so consumers can branch on it
// without nil checks.
type ToolResponse struct {
	EnvelopeVersion string      `json:"envelope_version"`
	Kind            string      `json:"kind"`
	Agent           string      `json:"agent"`
	SessionID       string      `json:"session_id,omitempty"`
	Traceparent     string      `json:"traceparent"`
	Input           Input       `json:"input"`
	ToolChoice      string      `json:"tool_choice,omitempty"`
	Output          string      `json:"output,omitempty"`
	Redactions      []Redaction `json:"redactions"`
	Usage           Usage       `json:"usage"`
	FinishReason    string      `json:"finish_reason"`
}

// ToolStreamChunk is one streaming delta. The terminal frame is delivered
// as a ToolResponse, so FinishReason is always null on chunks; we model
// that with a *string the schema rejects unless nil.
type ToolStreamChunk struct {
	EnvelopeVersion string      `json:"envelope_version"`
	Kind            string      `json:"kind"`
	Agent           string      `json:"agent"`
	SessionID       string      `json:"session_id,omitempty"`
	Traceparent     string      `json:"traceparent"`
	Index           int         `json:"index"`
	Delta           Delta       `json:"delta"`
	Redactions      []Redaction `json:"redactions,omitempty"`
	FinishReason    *string     `json:"finish_reason"`
}

// ToolError is the terminal failure shape. Maps to a non-zero clawctl
// exit code; see docs/transport-decisions.md for the per-subcommand
// contract.
type ToolError struct {
	EnvelopeVersion string         `json:"envelope_version"`
	Kind            string         `json:"kind"`
	Agent           string         `json:"agent,omitempty"`
	SessionID       string         `json:"session_id,omitempty"`
	Traceparent     string         `json:"traceparent"`
	Code            string         `json:"code"`
	Message         string         `json:"message"`
	HTTPStatus      *int           `json:"http_status,omitempty"`
	ExitCode        *int           `json:"exit_code,omitempty"`
	Details         map[string]any `json:"details,omitempty"`
}

var (
	schemaOnce sync.Once
	schema     *jsonschema.Schema
	schemaErr  error
)

func compile() (*jsonschema.Schema, error) {
	schemaOnce.Do(func() {
		c := jsonschema.NewCompiler()
		if err := c.AddResource("envelope.v1.json", bytes.NewReader(schemas.EnvelopeV1)); err != nil {
			schemaErr = fmt.Errorf("envelope: add resource: %w", err)
			return
		}
		s, err := c.Compile("envelope.v1.json")
		if err != nil {
			schemaErr = fmt.Errorf("envelope: compile: %w", err)
			return
		}
		schema = s
	})
	return schema, schemaErr
}

// Validate checks v against the embedded envelope.v1 JSON schema. v may be
// a struct from this package or any value produced by json.Unmarshal into
// interface{}. Returns nil for valid documents and a wrapped error for
// invalid ones.
func Validate(v any) error {
	s, err := compile()
	if err != nil {
		return err
	}
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("envelope: marshal: %w", err)
	}
	var doc any
	if err := json.Unmarshal(b, &doc); err != nil {
		return fmt.Errorf("envelope: unmarshal: %w", err)
	}
	return s.Validate(doc)
}
