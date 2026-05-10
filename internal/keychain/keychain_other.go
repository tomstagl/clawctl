//go:build !darwin && !linux

package keychain

import "errors"

// platformToken is a stub for platforms other than darwin and linux.
// Use CLAWCTL_TOKEN_CMD to supply a token on unsupported platforms.
func platformToken(service, account string) (string, error) {
	return "", errors.New("keychain: platform not supported; set CLAWCTL_TOKEN_CMD to supply a token")
}
