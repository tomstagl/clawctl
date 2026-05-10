// Package trace generates W3C traceparent headers for outbound HTTP calls.
//
// The format is the version-00 base16 encoding from
// https://www.w3.org/TR/trace-context/#traceparent-header:
//
//	00-<32-hex-trace-id>-<16-hex-span-id>-01
//
// We always emit a version-00 header with the sampled flag (01) set so the
// gateway-side spans we want to look up later in Jaeger are guaranteed to be
// recorded. The bash entrypoint generates the same shape via `openssl rand
// -hex 16` + `openssl rand -hex 8`; this package matches that contract using
// crypto/rand directly.
package trace

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"
)

// Traceparent is a parsed W3C traceparent header.
type Traceparent struct {
	Version string // always "00"
	TraceID string // 32 lowercase hex chars
	SpanID  string // 16 lowercase hex chars
	Flags   string // always "01" (sampled)
}

// String renders the traceparent in canonical wire form.
func (t Traceparent) String() string {
	return fmt.Sprintf("%s-%s-%s-%s", t.Version, t.TraceID, t.SpanID, t.Flags)
}

// New generates a fresh sampled traceparent. Returns an error only if the
// system entropy source is unavailable — in practice this is fatal and
// callers should surface it as a transport failure.
func New() (Traceparent, error) {
	var traceBytes [16]byte
	if _, err := rand.Read(traceBytes[:]); err != nil {
		return Traceparent{}, fmt.Errorf("trace: read trace-id: %w", err)
	}
	var spanBytes [8]byte
	if _, err := rand.Read(spanBytes[:]); err != nil {
		return Traceparent{}, fmt.Errorf("trace: read span-id: %w", err)
	}
	return Traceparent{
		Version: "00",
		TraceID: hex.EncodeToString(traceBytes[:]),
		SpanID:  hex.EncodeToString(spanBytes[:]),
		Flags:   "01",
	}, nil
}

// TraceIDOf extracts the trace-id from a traceparent header value. Returns
// the empty string if the header is malformed; callers that need the
// distinction should call Parse instead.
func TraceIDOf(tp string) string {
	parts := strings.Split(tp, "-")
	if len(parts) < 2 {
		return ""
	}
	return parts[1]
}
