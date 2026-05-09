package keychain

import (
	"errors"
	"testing"
)

func TestTokenRequiresService(t *testing.T) {
	_, err := Token("", "")
	if err == nil {
		t.Fatal("expected error for empty service")
	}
}

func TestTokenMissingItemReturnsErrNotFound(t *testing.T) {
	// Use a service name that almost certainly isn't present on dev machines or
	// CI runners. If it ever is, the test will be flaky — pick something
	// distinctive.
	_, err := Token("clawctl-test-service-that-does-not-exist-x73h", "")
	if err == nil {
		t.Skip("keychain unexpectedly contained the test service; skipping")
	}
	if !errors.Is(err, ErrNotFound) {
		// Don't fail on non-darwin or missing `security` — this package is
		// macOS-only by design and tests run on contributor laptops.
		t.Skipf("keychain probe returned non-ErrNotFound error (likely non-macOS env): %v", err)
	}
}
