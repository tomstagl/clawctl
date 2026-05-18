#!/usr/bin/env bash
# smoke-parity.sh — diff clawctl.bash vs the Go binary against a real
# openclaw gateway.  Canonicalises non-deterministic fields (trace-ids,
# ISO timestamps, durations) before comparing so the diff highlights only
# real behavioural divergence, not per-call entropy.
#
# Commands exercised: health, models, raw GET /health, verify commit HEAD,
# verify file README.md.
#
# Auth: the bash binary reads its token from the macOS Keychain (security(1)).
# The Go binary uses CLAWCTL_TOKEN_CMD if set, otherwise the Keychain.
# On Linux (no security(1)), auth-gated commands (health, models, raw) are
# skipped; verify runs unconditionally because it needs no gateway token.
#
# Usage:
#   CLAWCTL_HOST=http://…  BIN=<go-binary>  bash test/smoke-parity.sh
#
# BIN_BASH defaults to <repo-root>/clawctl.bash.
# BIN_GO   defaults to the BIN env var (set by nightly.yml), then builds
#          freshly from source if neither is set.
#
# Exit 0 on success, 1 if any parity check fails.

set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
BASH_BIN="${BIN_BASH:-$ROOT/clawctl.bash}"
TMP="$(mktemp -d)"
# BIN is set by nightly.yml to the already-built Go binary; fall back to
# building from source when running locally without BIN.
GO_BIN="${BIN_GO:-${BIN:-$TMP/clawctl-go}}"

cleanup() { rm -rf "$TMP"; }
trap cleanup EXIT

# ─── Pre-flight ──────────────────────────────────────────────────────────────

if [[ -z "${CLAWCTL_HOST:-}" ]]; then
  echo "skipping live parity (CLAWCTL_HOST not set)" >&2
  exit 0
fi

if [[ ! -x "$BASH_BIN" ]]; then
  echo "FAIL: bash binary not executable: $BASH_BIN" >&2
  exit 1
fi

# Build the Go binary only when we weren't handed one via BIN / BIN_GO.
if [[ "$GO_BIN" == "$TMP/clawctl-go" ]]; then
  if ! command -v go >/dev/null 2>&1; then
    echo "FAIL: go toolchain not on PATH and no BIN / BIN_GO set" >&2
    exit 1
  fi
  echo "building Go binary → $GO_BIN"
  ( cd "$ROOT" && CGO_ENABLED=0 go build -o "$GO_BIN" ./cmd/clawctl )
fi

if [[ ! -x "$GO_BIN" ]]; then
  echo "FAIL: Go binary not executable: $GO_BIN" >&2
  exit 1
fi

# Detect whether the bash binary can authenticate (macOS Keychain only).
HAS_SECURITY=0
command -v security >/dev/null 2>&1 && HAS_SECURITY=1

# ─── Helpers ─────────────────────────────────────────────────────────────────

fail=0; pass=0
ok()   { echo "  ok    $*"; pass=$((pass + 1)); }
nope() { echo "  FAIL  $*" >&2; fail=$((fail + 1)); }

# canon: replace non-deterministic fields so comparisons are stable.
#  • 32-hex trace-ids and 16-hex span-ids in traceparent headers
#  • ISO-8601 timestamps (with or without sub-second precision)
#  • duration values like 123ms, 1.45s, 456µs, 7us
canon() {
  sed -E \
    -e 's/[0-9a-f]{32}/<TRACE_ID>/g' \
    -e 's/[0-9a-f]{16}/<SPAN_ID>/g' \
    -e 's/[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}(\.[0-9]+)?Z/<TIMESTAMP>/g' \
    -e 's/[0-9]+(\.[0-9]+)?(ms|[µu]s|s\b)/<DURATION>/g'
}

run_one() {
  # run_one <label> <bin> [args…]
  # Runs <bin> with the remaining args, capturing stdout/stderr/exit.
  # On return: exit_code, out, err are set.
  local _label="$1" _bin="$2"
  local _out="$TMP/$_label.out" _err="$TMP/$_label.err"
  set +e
  ( cd "$ROOT" && "$_bin" "${@:3}" >"$_out" 2>"$_err" )
  exit_code=$?
  set -e
  out="$(cat "$_out")"
  err="$(cat "$_err")"
}

# parity_check <label> [clawctl-args…]
# Runs both binaries, canonicalises stdout/stderr, diffs them, reports.
parity_check() {
  local _label="$1"; shift

  run_one "bash-${_label}" "$BASH_BIN" "$@" || true
  local b_out b_err b_exit
  b_out="$(canon <<<"$out")"
  b_err="$(canon <<<"$err")"
  b_exit="$exit_code"

  run_one "go-${_label}" "$GO_BIN" "$@" || true
  local g_out g_err g_exit
  g_out="$(canon <<<"$out")"
  g_err="$(canon <<<"$err")"
  g_exit="$exit_code"

  local any_fail=0

  if [[ "$b_exit" != "$g_exit" ]]; then
    nope "${_label}: exit diverges (bash=${b_exit}, go=${g_exit})"
    any_fail=1
  fi

  if [[ "$b_out" != "$g_out" ]]; then
    nope "${_label}: stdout diverges"
    diff <(printf '%s\n' "$b_out") <(printf '%s\n' "$g_out") \
      | sed 's/^-/  -want: /; s/^+/  +got:  /' || true
    any_fail=1
  fi

  if [[ "$b_err" != "$g_err" ]]; then
    nope "${_label}: stderr diverges"
    diff <(printf '%s\n' "$b_err") <(printf '%s\n' "$g_err") \
      | sed 's/^-/  -want: /; s/^+/  +got:  /' || true
    any_fail=1
  fi

  if [[ "$any_fail" -eq 0 ]]; then
    ok "${_label}: stdout, stderr, exit all match"
  fi
}

# ─── Auth-gated commands ──────────────────────────────────────────────────────

if [[ "$HAS_SECURITY" -eq 0 ]]; then
  echo "SKIP: no macOS security(1) — auth-gated parity (health, models, raw) skipped" >&2
else
  echo "→ health parity"
  parity_check "health" health

  echo "→ models parity"
  parity_check "models" models

  echo "→ raw GET /health parity"
  parity_check "raw-health" raw GET /health
fi

# ─── verify (no gateway token required) ──────────────────────────────────────

echo "→ verify commit HEAD parity"
HEAD_COMMIT="$(git -C "$ROOT" rev-parse HEAD 2>/dev/null || true)"
if [[ -n "$HEAD_COMMIT" ]]; then
  parity_check "verify-commit" verify commit "$HEAD_COMMIT"
else
  echo "  SKIP: not in a git repo (no HEAD)" >&2
fi

echo "→ verify file README.md parity"
if [[ -f "$ROOT/README.md" ]]; then
  parity_check "verify-file" verify file README.md
else
  echo "  SKIP: README.md not present in repo root" >&2
fi

# ─── Summary ─────────────────────────────────────────────────────────────────

echo
echo "passed: $pass    failed: $fail"
[[ "$fail" -eq 0 ]] || exit 1
