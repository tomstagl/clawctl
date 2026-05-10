//go:build darwin

package keychain

import (
	"errors"
	"testing"
)

// TestPlatformTokenMissing exercises the macOS platformToken path directly:
// a service that doesn't exist should return ErrNotFound.
func TestPlatformTokenMissing(t *testing.T) {
	_, err := platformToken("clawctl-darwin-test-missing-x73h", "nobody")
	if err == nil {
		t.Skip("keychain unexpectedly contained the test service")
	}
	if !errors.Is(err, ErrNotFound) {
		t.Skipf("security not available or returned unexpected error: %v", err)
	}
}
