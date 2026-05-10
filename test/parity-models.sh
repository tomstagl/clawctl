#!/usr/bin/env bash
# parity-models.sh — diff `clawctl models` between the bash entrypoint and
# the Go binary against a controlled mock gateway.
#
# Coverage:
#   1. fresh fetch → both binaries pretty-print identical JSON, exit 0
#   2. cache hit → second invocation does NOT touch the server (proves the
#      Go binary honors CLAWCTL_MODELS_TTL with the bash file convention)
#   3. CLAWCTL_HOST unset → both exit 2
#
# Strategy: spin up a tiny Python HTTP server pinned to 127.0.0.1:<random>
# that serves a fixed /v1/models body and counts hits in $TMP/hits. Use a
# scratch CLAWCTL_CACHE_DIR per case so the cache state is deterministic.
# CLAWCTL_KEYCHAIN_SERVICE is pointed at a service that we add to the user's
# Keychain for the duration of the test, then delete on cleanup — this is
# the only path to provide a token to the bash binary, since principle #2
# forbids env/disk fallbacks. If `security` isn't available (non-macOS) the
# test skips the authenticated cases with a clear message instead of
# silently fabricating a token.

set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
BASH_BIN="$ROOT/clawctl"
TMP="$(mktemp -d)"
GO_BIN="$TMP/clawctl-go"
SERVER_PID=""
KEYCHAIN_SVC="clawctl-parity-models-$$"
KEYCHAIN_ADDED=0

cleanup() {
  if [[ -n "$SERVER_PID" ]] && kill -0 "$SERVER_PID" 2>/dev/null; then
    kill "$SERVER_PID" 2>/dev/null || true
    wait "$SERVER_PID" 2>/dev/null || true
  fi
  if [[ "$KEYCHAIN_ADDED" -eq 1 ]]; then
    security delete-generic-password -s "$KEYCHAIN_SVC" -a "$USER" >/dev/null 2>&1 || true
  fi
  rm -rf "$TMP"
}
trap cleanup EXIT

if [[ ! -x "$BASH_BIN" ]]; then
  echo "FAIL: $BASH_BIN not executable" >&2
  exit 1
fi

if ! command -v go >/dev/null 2>&1; then
  echo "FAIL: go toolchain not on PATH" >&2
  exit 1
fi

if ! command -v python3 >/dev/null 2>&1; then
  echo "FAIL: python3 not on PATH" >&2
  exit 1
fi

if ! command -v jq >/dev/null 2>&1; then
  echo "FAIL: jq not on PATH" >&2
  exit 1
fi

if ! command -v security >/dev/null 2>&1; then
  echo "SKIP: macOS \`security\` tool not available — bash binary cannot read a token" >&2
  exit 0
fi

echo "building Go binary → $GO_BIN"
( cd "$ROOT" && CGO_ENABLED=0 go build -o "$GO_BIN" ./cmd/clawctl )

# Stash a one-shot token in the user's Keychain. We label the service with
# the test PID so a crashed run leaves a uniquely-named entry that's easy to
# find and remove. cleanup() deletes it on exit.
if security add-generic-password -s "$KEYCHAIN_SVC" -a "$USER" -w "tok-parity-models" >/dev/null 2>&1; then
  KEYCHAIN_ADDED=1
else
  echo "FAIL: could not add temporary keychain item" >&2
  exit 1
fi

# Mock gateway. Reads $TMP/mode each request. Bumps $TMP/hits per /v1/models
# request so we can prove cache-hit behavior.
PORT_FILE="$TMP/port"
echo "ok" > "$TMP/mode"
echo 0 > "$TMP/hits"
SERVER_PY="$TMP/server.py"
cat >"$SERVER_PY" <<'PY'
import sys
import socketserver
from http.server import BaseHTTPRequestHandler, HTTPServer

port_file = sys.argv[1]
mode_file = sys.argv[2]
hits_file = sys.argv[3]

def mode():
    try:
        with open(mode_file) as f:
            return f.read().strip()
    except FileNotFoundError:
        return "ok"

def bump_hits():
    try:
        with open(hits_file) as f:
            n = int(f.read().strip() or 0)
    except FileNotFoundError:
        n = 0
    with open(hits_file, "w") as f:
        f.write(str(n + 1))

BODY = b'{"data":[{"id":"openclaw/example"},{"id":"openclaw/another"}]}'

class H(BaseHTTPRequestHandler):
    def do_GET(self):
        if self.path != "/v1/models":
            self.send_response(404); self.end_headers(); return
        bump_hits()
        m = mode()
        if m == "ok":
            self.send_response(200)
            self.send_header("Content-Type", "application/json")
            self.send_header("Content-Length", str(len(BODY)))
            self.end_headers()
            self.wfile.write(BODY)
        else:
            self.send_response(500); self.end_headers()
    def log_message(self, *args, **kwargs):
        pass

class HS(HTTPServer):
    def server_bind(self):
        socketserver.TCPServer.server_bind(self)
        _, port = self.socket.getsockname()[:2]
        self.server_name = "localhost"
        self.server_port = port

srv = HS(("127.0.0.1", 0), H)
with open(port_file, "w") as f:
    f.write(str(srv.server_address[1]))
srv.serve_forever()
PY

python3 -u "$SERVER_PY" "$PORT_FILE" "$TMP/mode" "$TMP/hits" </dev/null >"$TMP/server.log" 2>&1 &
SERVER_PID=$!

for _ in $(seq 1 40); do
  if [[ -s "$PORT_FILE" ]]; then break; fi
  sleep 0.05
done
if [[ ! -s "$PORT_FILE" ]]; then
  echo "FAIL: mock server didn't start" >&2
  exit 1
fi
PORT="$(cat "$PORT_FILE")"
HOST="http://127.0.0.1:$PORT"

fail=0; pass=0
ok()   { echo "  ok    $*"; pass=$((pass + 1)); }
nope() { echo "  FAIL  $*" >&2; fail=$((fail + 1)); }

run_one() {
  # $1 = label, $2 = bin, rest = args; sets $exit_code, $out
  local _label="$1" _bin="$2"
  local _out_file="$TMP/$_label.out" _err_file="$TMP/$_label.err"
  set +e
  "$_bin" "${@:3}" >"$_out_file" 2>"$_err_file"
  exit_code=$?
  set -e
  out="$(cat "$_out_file")"
}

read_hits() { cat "$TMP/hits"; }

#───────────────────────────────────────────────────────────────────────────
echo "case 1: fresh fetch on a cold cache → byte-identical pretty-printed JSON"
BASH_CACHE="$TMP/cache-bash-cold"
GO_CACHE="$TMP/cache-go-cold"
mkdir -p "$BASH_CACHE" "$GO_CACHE"
echo 0 > "$TMP/hits"

CLAWCTL_HOST="$HOST" CLAWCTL_TIMEOUT=5 CLAWCTL_MODELS_TTL=60 \
  CLAWCTL_CACHE_DIR="$BASH_CACHE" CLAWCTL_KEYCHAIN_SERVICE="$KEYCHAIN_SVC" \
  run_one "bash-cold" "$BASH_BIN" models
bash_out="$out"; bash_exit="$exit_code"

CLAWCTL_HOST="$HOST" CLAWCTL_TIMEOUT=5 CLAWCTL_MODELS_TTL=60 \
  CLAWCTL_CACHE_DIR="$GO_CACHE" CLAWCTL_KEYCHAIN_SERVICE="$KEYCHAIN_SVC" \
  run_one "go-cold" "$GO_BIN" models
go_out="$out"; go_exit="$exit_code"

[[ "$bash_exit" -eq 0 ]] && ok "bash exit 0 on cold fetch" || nope "bash exit $bash_exit"
[[ "$go_exit"   -eq 0 ]] && ok "go   exit 0 on cold fetch" || nope "go   exit $go_exit"
if [[ "$bash_out" == "$go_out" ]]; then ok "stdout matches"
else
  nope "stdout diverges"
  diff <(printf '%s' "$bash_out") <(printf '%s' "$go_out") || true
fi
echo "$go_out" | jq -e '.data | length == 2' >/dev/null \
  && ok "stdout is the expected pretty JSON with 2 entries" \
  || nope "stdout not the expected JSON"

# One bash call + one Go call should be exactly two server hits at this point.
hits_after_cold="$(read_hits)"
[[ "$hits_after_cold" -eq 2 ]] \
  && ok "server hit twice (one per binary on cold cache)" \
  || nope "server hits = $hits_after_cold, want 2"

#───────────────────────────────────────────────────────────────────────────
echo "case 2: cache hit on a warm cache → no extra server hits"
# Re-run both binaries against the SAME cache dirs they just populated. The
# mtime of the cache file is well within TTL=60s, so neither should refresh.
CLAWCTL_HOST="$HOST" CLAWCTL_TIMEOUT=5 CLAWCTL_MODELS_TTL=60 \
  CLAWCTL_CACHE_DIR="$BASH_CACHE" CLAWCTL_KEYCHAIN_SERVICE="$KEYCHAIN_SVC" \
  run_one "bash-warm" "$BASH_BIN" models
bash_warm_out="$out"; bash_warm_exit="$exit_code"
CLAWCTL_HOST="$HOST" CLAWCTL_TIMEOUT=5 CLAWCTL_MODELS_TTL=60 \
  CLAWCTL_CACHE_DIR="$GO_CACHE" CLAWCTL_KEYCHAIN_SERVICE="$KEYCHAIN_SVC" \
  run_one "go-warm" "$GO_BIN" models
go_warm_out="$out"; go_warm_exit="$exit_code"

[[ "$bash_warm_exit" -eq 0 ]] && ok "bash warm exit 0" || nope "bash warm exit $bash_warm_exit"
[[ "$go_warm_exit"   -eq 0 ]] && ok "go   warm exit 0" || nope "go   warm exit $go_warm_exit"
if [[ "$bash_warm_out" == "$go_warm_out" && "$bash_warm_out" == "$bash_out" ]]; then
  ok "stdout identical to cold-fetch stdout (cache served the same body)"
else
  nope "stdout diverged on warm cache"
fi

hits_after_warm="$(read_hits)"
delta=$(( hits_after_warm - hits_after_cold ))
[[ "$delta" -eq 0 ]] \
  && ok "server hits unchanged on warm cache (delta=0)" \
  || nope "server hits delta = $delta, want 0 (cache should have served both)"

#───────────────────────────────────────────────────────────────────────────
echo "case 3: cache file convention is identical (both at \$CLAWCTL_CACHE_DIR/models.json)"
[[ -f "$BASH_CACHE/models.json" ]] \
  && ok "bash wrote $BASH_CACHE/models.json" \
  || nope "bash did not write models.json"
[[ -f "$GO_CACHE/models.json" ]] \
  && ok "go   wrote $GO_CACHE/models.json" \
  || nope "go did not write models.json"
if diff -q "$BASH_CACHE/models.json" "$GO_CACHE/models.json" >/dev/null; then
  ok "cache file contents byte-identical between binaries"
else
  nope "cache file contents differ"
  diff "$BASH_CACHE/models.json" "$GO_CACHE/models.json" || true
fi

#───────────────────────────────────────────────────────────────────────────
echo "case 4: CLAWCTL_HOST unset → exit 2 from both"
unset CLAWCTL_HOST
run_one "bash-nohost" "$BASH_BIN" models || true
bash_exit="$exit_code"
run_one "go-nohost"   "$GO_BIN"   models || true
go_exit="$exit_code"
[[ "$bash_exit" -eq 2 ]] && ok "bash exit 2 when CLAWCTL_HOST unset" || nope "bash exit $bash_exit (want 2)"
[[ "$go_exit"   -eq 2 ]] && ok "go   exit 2 when CLAWCTL_HOST unset" || nope "go   exit $go_exit (want 2)"

#───────────────────────────────────────────────────────────────────────────
echo
echo "passed: $pass    failed: $fail"
[[ "$fail" -eq 0 ]] || exit 1
