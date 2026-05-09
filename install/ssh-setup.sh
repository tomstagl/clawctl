#!/usr/bin/env bash
# ssh-setup.sh — append the clawctl ssh-config snippet to ~/.ssh/config.
#
# Idempotent: the snippet is fenced with `# BEGIN clawctl` / `# END clawctl`
# markers; re-running this script replaces the existing block in place.
# Pre-existing ~/.ssh/config content outside the markers is left untouched.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
SNIPPET="$SCRIPT_DIR/ssh-config.snippet"

if [[ ! -f "$SNIPPET" ]]; then
  echo "error: $SNIPPET not found" >&2
  exit 2
fi

SSH_DIR="$HOME/.ssh"
SSH_CONFIG="$SSH_DIR/config"
BEGIN_MARKER="# BEGIN clawctl"
END_MARKER="# END clawctl"

# Ensure ~/.ssh exists with secure permissions.
if [[ ! -d "$SSH_DIR" ]]; then
  mkdir -p "$SSH_DIR"
  chmod 700 "$SSH_DIR"
fi

# Ensure ~/.ssh/config exists; ssh treats a missing config as empty, but a
# missing file would make the grep below fail under `set -e`.
if [[ ! -f "$SSH_CONFIG" ]]; then
  touch "$SSH_CONFIG"
  chmod 600 "$SSH_CONFIG"
fi

snippet_body=$(cat "$SNIPPET")

# Two cases: marker block already present (replace in place) or absent (append).
if grep -qF "$BEGIN_MARKER" "$SSH_CONFIG" && grep -qF "$END_MARKER" "$SSH_CONFIG"; then
  # Rewrite the file: keep everything before BEGIN, drop the block, then
  # write the fresh snippet, then keep everything after END. We write to a
  # temp file in the same directory and rename atomically so a crash mid-
  # write cannot corrupt ~/.ssh/config.
  tmp=$(mktemp "$SSH_CONFIG.clawctl.XXXXXX")
  trap 'rm -f "$tmp"' EXIT

  # Skip everything inside the markers. Then strip trailing blank lines so
  # repeated runs do not accumulate whitespace between the carry-over
  # content and the snippet.
  awk -v begin="$BEGIN_MARKER" -v end="$END_MARKER" '
    $0 == begin { skip = 1; next }
    $0 == end   { skip = 0; next }
    skip != 1   { print }
  ' "$SSH_CONFIG" \
    | awk 'BEGIN { blank = 0 }
           /^$/  { blank++; next }
                 { while (blank-- > 0) print ""; blank = 0; print }' \
    > "$tmp"

  # Append snippet separated by exactly one blank line (unless the file
  # was empty after stripping the block, in which case no leading blank).
  if [[ -s "$tmp" ]]; then
    printf '\n%s\n' "$snippet_body" >> "$tmp"
  else
    printf '%s\n' "$snippet_body" >> "$tmp"
  fi

  mv "$tmp" "$SSH_CONFIG"
  trap - EXIT
  chmod 600 "$SSH_CONFIG"
  echo "✓ updated clawctl block in $SSH_CONFIG"
else
  # Append. If the file is non-empty and does not end in a newline, add one
  # before the snippet so the marker starts on its own line.
  if [[ -s "$SSH_CONFIG" ]]; then
    if [[ -n "$(tail -c1 "$SSH_CONFIG")" ]]; then
      printf '\n' >> "$SSH_CONFIG"
    fi
    printf '\n' >> "$SSH_CONFIG"
  fi
  printf '%s\n' "$snippet_body" >> "$SSH_CONFIG"
  chmod 600 "$SSH_CONFIG"
  echo "✓ appended clawctl block to $SSH_CONFIG"
fi

cat <<EOF

Next steps:
  1. export CLAWCTL_SSH_HOST=user@your-openclaw-host
     (set this to whatever you already use to \`ssh\` to the gateway)
  2. clawctl cli --help
     (the first call opens a master connection; subsequent calls reuse it
      for up to 10 minutes of idle time — see install/ssh-config.snippet)
EOF
