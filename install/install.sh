#!/usr/bin/env bash
# install.sh — resolve and install a released `clawctl` binary.
#
# Detects the running OS and architecture, downloads the matching artifact
# from a GitHub release (with SHA256SUMS verification), and installs it to
# /usr/local/bin/clawctl.
#
# Idempotent: re-running upgrades the binary in place. Refuses to overwrite
# anything at the target path that does not respond to `clawctl version`.
#
# Knobs (env):
#   CLAWCTL_REPO           GitHub repo slug (default: tomstagl/clawctl)
#   CLAWCTL_VERSION        Tag to install ("latest" or "vX.Y.Z"; default: latest)
#   CLAWCTL_INSTALL_DIR    Install directory (default: /usr/local/bin)
#   CLAWCTL_DOWNLOAD_BASE  Override release base URL (used by tests; defaults
#                          to https://github.com/<repo>/releases)

set -euo pipefail

REPO="${CLAWCTL_REPO:-tomstagl/clawctl}"
VERSION="${CLAWCTL_VERSION:-latest}"
INSTALL_DIR="${CLAWCTL_INSTALL_DIR:-/usr/local/bin}"
DOWNLOAD_BASE="${CLAWCTL_DOWNLOAD_BASE:-https://github.com/$REPO/releases}"
TARGET="$INSTALL_DIR/clawctl"

#───────────────────────────────────────────────────────────────────────────────
# 1. Detect platform.
#───────────────────────────────────────────────────────────────────────────────

detect_artifact() {
  local kernel machine os arch
  kernel="$(uname -s)"
  machine="$(uname -m)"

  case "$kernel" in
    Darwin) os="darwin" ;;
    Linux)  os="linux"  ;;
    *)
      echo "install: unsupported OS '$kernel' (only darwin and linux have release binaries)" >&2
      exit 2
      ;;
  esac

  case "$machine" in
    arm64|aarch64) arch="arm64" ;;
    x86_64|amd64)  arch="amd64" ;;
    *)
      echo "install: unsupported architecture '$machine' (expected arm64/aarch64 or x86_64/amd64)" >&2
      exit 2
      ;;
  esac

  printf 'clawctl-%s-%s' "$os" "$arch"
}

ARTIFACT="$(detect_artifact)"

#───────────────────────────────────────────────────────────────────────────────
# 2. Build download URLs. GitHub's `/releases/latest/download/<asset>` and
#    `/releases/download/<tag>/<asset>` patterns return the same shape, so the
#    resolver does not need to call the API for the tag — fewer auth surprises.
#───────────────────────────────────────────────────────────────────────────────

if [[ "$VERSION" == "latest" ]]; then
  ASSET_URL="$DOWNLOAD_BASE/latest/download/$ARTIFACT"
  SUMS_URL="$DOWNLOAD_BASE/latest/download/SHA256SUMS"
else
  ASSET_URL="$DOWNLOAD_BASE/download/$VERSION/$ARTIFACT"
  SUMS_URL="$DOWNLOAD_BASE/download/$VERSION/SHA256SUMS"
fi

#───────────────────────────────────────────────────────────────────────────────
# 3. Download artifact + checksum file into a tempdir; verify before install.
#───────────────────────────────────────────────────────────────────────────────

TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

echo "→ Downloading $ARTIFACT ($VERSION)…"
if ! curl -fsSL --retry 2 --output "$TMP/$ARTIFACT" "$ASSET_URL"; then
  echo "install: failed to download $ASSET_URL" >&2
  exit 1
fi

echo "→ Downloading SHA256SUMS…"
if ! curl -fsSL --retry 2 --output "$TMP/SHA256SUMS" "$SUMS_URL"; then
  echo "install: failed to download $SUMS_URL" >&2
  exit 1
fi

# Pick the shasum binary. macOS ships `shasum`; Linux usually has `sha256sum`.
# Either works against the SHA256SUMS format produced by .github/workflows/release.yml.
if command -v sha256sum >/dev/null 2>&1; then
  SHA_CMD=(sha256sum --check --ignore-missing)
elif command -v shasum >/dev/null 2>&1; then
  SHA_CMD=(shasum -a 256 --check --ignore-missing)
else
  echo "install: neither sha256sum nor shasum is available; cannot verify checksum" >&2
  exit 1
fi

echo "→ Verifying checksum…"
if ! ( cd "$TMP" && "${SHA_CMD[@]}" SHA256SUMS >/dev/null ) ; then
  echo "install: checksum mismatch for $ARTIFACT" >&2
  echo "         (downloaded asset does not match the published SHA256SUMS)" >&2
  exit 1
fi

#───────────────────────────────────────────────────────────────────────────────
# 4. Refuse to overwrite an existing binary that is not a clawctl.
#    Heuristic from the user story: `clawctl version` exits 0 and contains
#    'clawctl' in stdout. Anything else is treated as a foreign binary
#    (kubectl alias, OpenShift client, custom script, etc.) and we bail.
#───────────────────────────────────────────────────────────────────────────────

if [[ -e "$TARGET" ]]; then
  if ver_out="$("$TARGET" version 2>/dev/null)" && [[ "$ver_out" == *clawctl* ]]; then
    echo "→ Existing clawctl found at $TARGET ($ver_out); upgrading."
  else
    echo "install: refusing to overwrite $TARGET — it does not look like a clawctl binary" >&2
    echo "         (\`$TARGET version\` must exit 0 and mention 'clawctl')" >&2
    echo "         If this is an unrelated tool, set CLAWCTL_INSTALL_DIR to a different prefix" >&2
    echo "         or remove/rename the existing file first." >&2
    exit 1
  fi
fi

#───────────────────────────────────────────────────────────────────────────────
# 5. Install. /usr/local/bin is root-owned on most systems; if we can't write,
#    re-run the move under sudo so a single curl|bash works on a stock host.
#───────────────────────────────────────────────────────────────────────────────

if [[ ! -d "$INSTALL_DIR" ]]; then
  if ! mkdir -p "$INSTALL_DIR" 2>/dev/null; then
    echo "→ $INSTALL_DIR does not exist; creating with sudo."
    sudo mkdir -p "$INSTALL_DIR"
  fi
fi

chmod +x "$TMP/$ARTIFACT"

if [[ -w "$INSTALL_DIR" ]] || { [[ -e "$TARGET" ]] && [[ -w "$TARGET" ]]; }; then
  mv "$TMP/$ARTIFACT" "$TARGET"
else
  echo "→ $INSTALL_DIR not writable; using sudo."
  sudo mv "$TMP/$ARTIFACT" "$TARGET"
fi

echo "✓ installed clawctl to $TARGET"

# Print the version we just installed so the user has a receipt.
if installed_ver="$("$TARGET" version 2>/dev/null)"; then
  echo "  $installed_ver"
fi

#───────────────────────────────────────────────────────────────────────────────
# 6. PATH + setup hints.
#───────────────────────────────────────────────────────────────────────────────

if ! command -v clawctl >/dev/null 2>&1 || [[ "$(command -v clawctl)" != "$TARGET" ]]; then
  cat <<EOF

NOTE: $INSTALL_DIR is not on your PATH (or another 'clawctl' shadows it).
Add it to your shell profile, e.g.:

  export PATH="$INSTALL_DIR:\$PATH"

EOF
fi

cat <<EOF

Next steps:
  1. export CLAWCTL_HOST=http://your-openclaw-host:18789
  2. export CLAWCTL_SSH_HOST=user@your-openclaw-host    # only needed for 'clawctl cli'
  3. security add-generic-password -s openclaw-gateway-token -a "\$USER" -w '<token>'
  4. clawctl health
EOF
