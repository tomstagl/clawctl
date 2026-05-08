#!/usr/bin/env bash
# install.sh — install the clawctl wrapper.
# Idempotent: re-running upgrades the binary, leaves config untouched.

set -euo pipefail

REPO_RAW="${CLAWCTL_REPO_RAW:-https://raw.githubusercontent.com/tomstagl/clawctl/main}"
INSTALL_DIR="${CLAWCTL_INSTALL_DIR:-$HOME/.local/bin}"
TARGET="$INSTALL_DIR/clawctl"

if [[ ! -d "$INSTALL_DIR" ]]; then
  mkdir -p "$INSTALL_DIR"
fi

# If we're running from a checked-out repo, copy the local file. Otherwise curl.
if [[ -f "$(dirname "$0")/../clawctl" ]]; then
  cp "$(dirname "$0")/../clawctl" "$TARGET"
else
  curl -fsSL "$REPO_RAW/clawctl" -o "$TARGET"
fi

chmod +x "$TARGET"

echo "✓ installed clawctl to $TARGET"

# PATH check
if ! command -v clawctl >/dev/null 2>&1 || [[ "$(command -v clawctl)" != "$TARGET" ]]; then
  cat <<EOF

NOTE: $INSTALL_DIR is not on your PATH (or another 'clawctl' shadows it).
Add this line to your shell profile:

  export PATH="\$HOME/.local/bin:\$PATH"

EOF
fi

# Sanity check
echo
echo "Next steps:"
echo "  1. export CLAWCTL_HOST=http://your-openclaw-host:18789"
echo "  2. export CLAWCTL_SSH_HOST=user@your-openclaw-host    # only needed for 'clawctl cli'"
echo "  3. security add-generic-password -s openclaw-gateway-token -a \"\$USER\" -w '<token>'"
echo "  4. clawctl health"
