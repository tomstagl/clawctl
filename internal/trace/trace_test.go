package trace

import (
	"regexp"
	"testing"
)

// traceparent shape per https://www.w3.org/TR/trace-context/#traceparent-header
var traceparentRE = regexp.MustCompile(`^00-[0-9a-f]{32}-[0-9a-f]{16}-01$`)

func TestNew_Shape(t *testing.T) {
	tp, err := New()
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	got := tp.String()
	if !traceparentRE.MatchString(got) {
		t.Errorf("traceparent %q does not match shape 00-<32hex>-<16hex>-01", got)
	}
	if tp.Version != "00" {
		t.Errorf("version = %q, want 00", tp.Version)
	}
	if tp.Flags != "01" {
		t.Errorf("flags = %q, want 01 (sampled)", tp.Flags)
	}
	if len(tp.TraceID) != 32 {
		t.Errorf("trace-id length = %d, want 32", len(tp.TraceID))
	}
	if len(tp.SpanID) != 16 {
		t.Errorf("span-id length = %d, want 16", len(tp.SpanID))
	}
}

func TestNew_Unique(t *testing.T) {
	// Two calls produce distinct trace-ids and span-ids. Birthday-collision
	// odds on 128 bits are vanishingly small, but we run a handful for the
	// happy-path signal value.
	seen := map[string]bool{}
	for i := 0; i < 64; i++ {
		tp, err := New()
		if err != nil {
			t.Fatalf("New: %v", err)
		}
		if seen[tp.TraceID] {
			t.Fatalf("duplicate trace-id %q after %d iterations", tp.TraceID, i)
		}
		seen[tp.TraceID] = true
	}
}

func TestTraceIDOf(t *testing.T) {
	cases := []struct {
		tp   string
		want string
	}{
		{"00-0123456789abcdef0123456789abcdef-fedcba9876543210-01", "0123456789abcdef0123456789abcdef"},
		{"00-aaaa-bbbb-01", "aaaa"},
		{"", ""},
		{"garbage", ""},
	}
	for _, tc := range cases {
		if got := TraceIDOf(tc.tp); got != tc.want {
			t.Errorf("TraceIDOf(%q) = %q, want %q", tc.tp, got, tc.want)
		}
	}
}
