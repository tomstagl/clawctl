#!/usr/bin/env bash
# plugin-commands.sh — assert every `clawctl <subcommand>` referenced inside
# fenced code blocks in the command docs is actually implemented in the Go
# binary (exit code 2 + "not yet implemented" = unknown subcommand).
# Usage: BIN=/path/to/clawctl-go bash test/plugin-commands.sh
# Exit 0 on success, 1 on any failure.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"

# ---------- binary --------------------------------------------------------

BIN="${BIN:-}"
if [[ -z "$BIN" ]]; then
    _tmp=$(mktemp -d)
    trap 'rm -rf "$_tmp"' EXIT
    echo "Building Go binary..."
    go build -o "$_tmp/clawctl-go" "$ROOT/cmd/clawctl"
    BIN="$_tmp/clawctl-go"
fi

PASS=0
FAIL=0

ok()   { echo "  ok: $*"; PASS=$((PASS+1)); }
fail() { echo "FAIL: $*" >&2; FAIL=$((FAIL+1)); }

# ---------- extraction ----------------------------------------------------
# Walk each command doc; track whether we are inside a fenced code block
# (``` ... ```) and extract the first argument after `clawctl` (the
# top-level subcommand) from matching lines.  Record source file + line
# number so failure messages are actionable.

seen_subs=""           # space-padded string of already-recorded subcommands
declare -a check_list  # entries: "relpath|lineno|sub"

docs=(
    "$ROOT/commands/clawctl.md"
    "$ROOT/commands/clawctl-recipes.md"
    "$ROOT/commands/clawctl-cli.md"
)

for doc in "${docs[@]}"; do
    rel="${doc#"$ROOT/"}"
    in_fence=0
    lineno=0
    while IFS= read -r line || [[ -n "$line" ]]; do
        lineno=$((lineno+1))
        # Toggle fenced-block state on opening/closing ``` markers.
        if [[ "$line" == '```'* ]]; then
            in_fence=$(( 1 - in_fence ))
            continue
        fi
        if [[ $in_fence -eq 1 ]]; then
            # Match 'clawctl <word>' where <word> starts with [a-z_].
            # This skips `command -v clawctl` (word follows -v, not clawctl)
            # and `clawctl --flag` (starts with -).
            if [[ "$line" =~ clawctl[[:space:]]+([a-z_][a-zA-Z0-9_-]*) ]]; then
                sub="${BASH_REMATCH[1]}"
                if [[ "$seen_subs" != *" $sub "* ]]; then
                    seen_subs="$seen_subs $sub "
                    check_list+=("$rel|$lineno|$sub")
                fi
            fi
        fi
    done < "$doc"
done

# ---------- report extracted set ------------------------------------------

echo "Found ${#check_list[@]} unique subcommand(s) referenced in command docs:"
for entry in "${check_list[@]+"${check_list[@]}"}"; do
    IFS='|' read -r _f _l _s <<< "$entry"
    echo "  $_f:$_l → clawctl $_s"
done
echo

# ---------- existence checks ----------------------------------------------
# Run each subcommand with a dummy CLAWCTL_HOST to bypass the "host not set"
# early-exit (code 2).  Any exit code is acceptable; the only failure signal
# is stderr containing "not yet implemented in the typed binary", which is
# the exact message the default: branch in main.go emits for unknown cmds.

for entry in "${check_list[@]+"${check_list[@]}"}"; do
    IFS='|' read -r f l sub <<< "$entry"
    output=$(CLAWCTL_HOST=http://127.0.0.1:1 "$BIN" "$sub" --help 2>&1 || true)
    if printf '%s\n' "$output" | grep -qF "not yet implemented in the typed binary"; then
        fail "[$f:$l] 'clawctl $sub' is not a known subcommand in the Go binary"
    else
        ok "[$f:$l] clawctl $sub"
    fi
done

# ---------- summary -------------------------------------------------------

echo ""
echo "plugin-commands: $PASS passed, $FAIL failed"
[[ $FAIL -eq 0 ]] || exit 1
