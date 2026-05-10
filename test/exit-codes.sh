#!/usr/bin/env bash
# exit-codes.sh — table-test that each subcommand × failure-mode pair exits
# with a code from the documented contract in `clawctl help`:
#
#   0    ok
#   2    usage error / missing env var / unknown subcommand
#   6    DNS resolution failed
#   7    connection refused
#   22   HTTP 4xx/5xx
#   28   timeout
#
# Subcommand-specific overrides documented in `clawctl help`:
#   verify    1 = unverified (commit/PR/issue/file not found)
#   cli       pass-through: ssh / oc-remote / openclaw exit code unchanged
#   trace     best-effort: 0 even when Jaeger is unreachable so the UI link surfaces
#
# Strategy: shadow `curl` (and `security`) on PATH with stubs whose exit code
# is controlled via FAKE_CURL_EXIT. No network or Keychain required.
#
# Exit 0 on success, 1 on any failure.

set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
BIN="$ROOT/clawctl.bash"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

if [[ ! -x "$BIN" ]]; then
  echo "FAIL: $BIN not executable" >&2
  exit 1
fi

# Fake curl: honors -o so file-writing callers (e.g. _models_cache) stay
# happy, always writes a syntactically valid JSON body so jq doesn't fail
# on parse, and exits with FAKE_CURL_EXIT.
write_fake_curl() {
  cat >"$TMP/curl" <<'EOF'
#!/usr/bin/env bash
out=""
while [ $# -gt 0 ]; do
  case "$1" in
    -o|--output) out="$2"; shift 2 ;;
    *)           shift ;;
  esac
done
body='{}'
[ -n "${FAKE_CURL_BODY:-}" ] && body="$FAKE_CURL_BODY"
if [ -n "$out" ]; then
  printf '%s' "$body" >"$out"
else
  printf '%s' "$body"
fi
exit "${FAKE_CURL_EXIT:-0}"
EOF
  chmod +x "$TMP/curl"
}

# Fake security so _token works without Keychain access.
write_fake_security() {
  cat >"$TMP/security" <<'EOF'
#!/usr/bin/env bash
echo "fake-token"
EOF
  chmod +x "$TMP/security"
}

write_fake_curl
write_fake_security

clean_cache() { rm -rf "$TMP/cache"; }

# Run clawctl in a clean env with the fake curl exit code.
run_curl_exit() {
  local code="$1"; shift
  FAKE_CURL_EXIT="$code" \
  PATH="$TMP:$PATH" \
  CLAWCTL_HOST="http://example.test" \
  CLAWCTL_CACHE_DIR="$TMP/cache" \
  CLAWCTL_TIMEOUT=5 \
    "$BIN" "$@"
}

# Run clawctl with one env var explicitly unset.
run_without() {
  local var="$1"; shift
  env -u "$var" PATH="$TMP:$PATH" CLAWCTL_CACHE_DIR="$TMP/cache" "$BIN" "$@"
}

# Run clawctl normally (CLAWCTL_HOST set, no curl exit override).
run() {
  PATH="$TMP:$PATH" \
  CLAWCTL_HOST="http://example.test" \
  CLAWCTL_CACHE_DIR="$TMP/cache" \
  CLAWCTL_TIMEOUT=5 \
    "$BIN" "$@"
}

fail=0; pass=0
ok()   { echo "  ok    $*"; pass=$((pass + 1)); }
nope() { echo "  FAIL  $*" >&2; fail=$((fail + 1)); }

# expect_exit <expected> <name> <command...>
expect_exit() {
  local expected="$1" desc="$2"; shift 2
  set +e
  "$@" >/dev/null 2>&1
  local ec=$?
  set -e
  if [ "$ec" -eq "$expected" ]; then
    ok "$desc → exit $ec"
  else
    nope "$desc → exit $ec (expected $expected)"
  fi
}

#───────────────────────────────────────────────────────────────────────────────
# 1. Missing env var → exit 2
#───────────────────────────────────────────────────────────────────────────────

echo "→ missing env var → exit 2"
clean_cache
expect_exit 2 "health (no CLAWCTL_HOST)"   run_without CLAWCTL_HOST health
expect_exit 2 "models (no CLAWCTL_HOST)"   run_without CLAWCTL_HOST models
expect_exit 2 "msg (no CLAWCTL_HOST)"      run_without CLAWCTL_HOST msg agent text
expect_exit 2 "stream (no CLAWCTL_HOST)"   run_without CLAWCTL_HOST stream agent text
expect_exit 2 "raw (no CLAWCTL_HOST)"      run_without CLAWCTL_HOST raw GET /health
expect_exit 2 "cli (no CLAWCTL_SSH_HOST)"  run_without CLAWCTL_SSH_HOST cli help
expect_exit 2 "trace (no CLAWCTL_JAEGER_UI)" \
  env -u CLAWCTL_JAEGER_UI PATH="$TMP:$PATH" \
  CLAWCTL_HOST=http://example.test "$BIN" trace deadbeefdeadbeefdeadbeefdeadbeef

#───────────────────────────────────────────────────────────────────────────────
# 2. Transport failures via fake curl exit codes (6 DNS, 7 refused,
#    22 HTTP 4xx/5xx, 28 timeout). Each clean_cache call ensures the cache is
#    stale so _models_cache actually invokes curl.
#───────────────────────────────────────────────────────────────────────────────

for ec in 6 7 22 28; do
  echo "→ curl exit $ec propagates"
  clean_cache; expect_exit "$ec" "health curl=$ec" run_curl_exit "$ec" health
  clean_cache; expect_exit "$ec" "models curl=$ec" run_curl_exit "$ec" models
  clean_cache; expect_exit "$ec" "raw curl=$ec"    run_curl_exit "$ec" raw GET /health
  clean_cache; expect_exit "$ec" "msg curl=$ec"    run_curl_exit "$ec" msg default "hi"
done

#───────────────────────────────────────────────────────────────────────────────
# 3. Usage errors → exit 2
#───────────────────────────────────────────────────────────────────────────────

echo "→ usage errors → exit 2"
clean_cache
expect_exit 2 "msg --bogus-flag"        run msg --bogus-flag
expect_exit 2 "msg (no agent)"          run msg
expect_exit 2 "stream (no agent)"       run stream
expect_exit 2 "verify (no kind)"        run verify
expect_exit 2 "verify commit (no hash)" run verify commit
expect_exit 2 "verify pr (bad spec)"    run verify pr foo
expect_exit 2 "verify issue (no spec)"  run verify issue
expect_exit 2 "verify file (no path)"   run verify file
expect_exit 2 "verify <unknown kind>"   run verify frobnicate
expect_exit 2 "trace (no id)"           run trace

#───────────────────────────────────────────────────────────────────────────────
# 4. Unknown subcommand → exit 2
#───────────────────────────────────────────────────────────────────────────────

echo "→ unknown subcommand → exit 2"
expect_exit 2 "unknown command"         run frobnicate

#───────────────────────────────────────────────────────────────────────────────
# 5. Subcommand-specific overrides
#───────────────────────────────────────────────────────────────────────────────

echo "→ subcommand-specific exit codes"

# verify: 1 for unverified.
expect_exit 1 "verify commit (bogus hash → unverified)" \
  run verify commit 0000000000000000000000000000000000000000

# verify file (working tree, missing path → unverified).
expect_exit 1 "verify file (missing path → unverified)" \
  run verify file "$TMP/does-not-exist"

# trace: best-effort 0 even when Jaeger curl fails (here: fake curl exits 7).
expect_exit 0 "trace (Jaeger unreachable, best-effort 0)" \
  env FAKE_CURL_EXIT=7 PATH="$TMP:$PATH" \
  CLAWCTL_HOST=http://example.test CLAWCTL_JAEGER_UI=http://jaeger.test \
  "$BIN" trace deadbeefdeadbeefdeadbeefdeadbeef

#───────────────────────────────────────────────────────────────────────────────

echo
if (( fail == 0 )); then
  echo "✓ $pass checks passed"
  exit 0
fi
echo "✗ $fail checks failed ($pass passed)" >&2
exit 1
