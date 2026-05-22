#!/usr/bin/env bash
# cli-hardening.sh — assert that `clawctl cli`:
#   1. Calls clawctl-remote on the host when it is present (and passes argv as a
#      slice, byte-for-byte, with no shell-string concatenation).
#   2. Exits 2 with a remediation message pointing at install instructions
#      when clawctl-remote is absent on the host.
#   3. Preserves argv with spaces, single quotes, double quotes, and shell
#      metacharacters end-to-end (verifies the printf %q fallback is gone).
#
# Strategy: shadow `ssh` on PATH with a fake that distinguishes the
# `test -x /usr/local/bin/clawctl-remote` probe from the actual invocation, and
# logs argv to stdout so the test can diff what reached clawctl-remote against
# what was passed on the clawctl command line.
#
# This test does NOT require a network or a real gateway. It exercises only
# the SSH path of `clawctl cli`.
#
# Exit 0 on success, 1 on any failure.

set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
BIN="${BIN:-$ROOT/clawctl.bash}"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

if [[ ! -x "$BIN" ]]; then
  echo "FAIL: $BIN not executable" >&2
  exit 1
fi

# Fake ssh shared prelude. SSH_OCREMOTE_PRESENT controls probe behaviour.
# SSH_OCREMOTE_PRESENT controls probe behaviour (default 0 = absent).
# SSH_INSTALL_EXIT controls the install call exit code (default 0 = success).
# When the probe succeeds, we emit the version marker so the Go binary's
# version check passes and skips the auto-install path.
write_fake_ssh() {
  cat >"$TMP/ssh" <<'EOF'
#!/usr/bin/env bash
# Fake ssh used by test/cli-hardening.sh.
#
# Real ssh argv shapes we care about:
#   ssh -o BatchMode=yes -o ConnectTimeout=5  HOST 'test -x ... && head -3 ...'
#   ssh -o BatchMode=yes -o ConnectTimeout=10 HOST 'mkdir -p $(dirname ...) && install -m 0755 /dev/stdin ...'
#   ssh -o ControlMaster=auto ... HOST -- /usr/local/bin/clawctl-remote ARG ARG ...
#
# We strip option flags until we hit the host token, drop a leading `--` if
# present, then dispatch on the remaining argv shape.

host=""
while [ $# -gt 0 ]; do
  case "$1" in
    -o) shift 2 ;;
    -*) shift ;;
    *)  host="$1"; shift; break ;;
  esac
done

if [ "${1:-}" = "--" ]; then shift; fi

# Probe form: single argument beginning with "test -x".
# Emit version marker on success so the Go binary's version check passes.
if [ "$#" -eq 1 ] && [[ "$1" == "test -x"* ]]; then
  if [ "${SSH_OCREMOTE_PRESENT:-0}" = "1" ]; then
    printf '#!/usr/bin/env bash\n# clawctl-remote dev\n'
    exit 0
  else
    exit 1
  fi
fi

# Install form: single argument beginning with "mkdir -p".
if [ "$#" -eq 1 ] && [[ "$1" == "mkdir -p"* ]]; then
  exit "${SSH_INSTALL_EXIT:-0}"
fi

# Invocation form: print host + argv, one per line, so callers can diff.
printf 'HOST=%s\n' "$host"
printf 'ARGC=%d\n' "$#"
for a in "$@"; do
  printf 'ARG=<<%s>>\n' "$a"
done
EOF
  chmod +x "$TMP/ssh"
}

write_fake_ssh

run_cli() {
  PATH="$TMP:$PATH" CLAWCTL_SSH_HOST="example.test" "$BIN" cli "$@"
}

fail=0
pass=0

ok()    { echo "  ok    $*"; pass=$((pass + 1)); }
fail_() { echo "  FAIL  $*" >&2; fail=$((fail + 1)); }

#───────────────────────────────────────────────────────────────────────────────
# Test 1: clawctl-remote present, simple argv
#───────────────────────────────────────────────────────────────────────────────

echo "→ Test 1: clawctl-remote present, simple argv"
out=$(SSH_OCREMOTE_PRESENT=1 run_cli agents list 2>&1) \
  || { fail_ "exit code $? (expected 0)"; out=""; }

if [[ "$out" == *"HOST=example.test"* ]] \
   && [[ "$out" == *"ARGC=3"* ]] \
   && [[ "$out" == *"ARG=<</usr/local/bin/clawctl-remote>>"* ]] \
   && [[ "$out" == *"ARG=<<agents>>"* ]] \
   && [[ "$out" == *"ARG=<<list>>"* ]]; then
  ok "argv reached clawctl-remote intact"
else
  fail_ "unexpected argv on the wire"
  printf '%s\n' "$out" | sed 's/^/      /' >&2
fi

#───────────────────────────────────────────────────────────────────────────────
# Test 2: clawctl-remote absent → exit 2 + remediation message
#───────────────────────────────────────────────────────────────────────────────

echo "→ Test 2: clawctl-remote absent + install fails → exit 2 with remediation message"
set +e
out=$(SSH_OCREMOTE_PRESENT=0 SSH_INSTALL_EXIT=1 run_cli agents list 2>&1)
ec=$?
set -e

if [ "$ec" -eq 2 ]; then
  ok "exit code 2"
else
  fail_ "exit code $ec (expected 2)"
fi

if [[ "$out" == *"/usr/local/bin/clawctl-remote"* ]]; then
  ok "stderr names the install path"
else
  fail_ "stderr missing install path: $out"
fi

if [[ "$out" == *"install"* ]]; then
  ok "stderr points at install instructions"
else
  fail_ "stderr lacks install pointer"
fi

#───────────────────────────────────────────────────────────────────────────────
# Test 3: argv with spaces, quotes, and shell metacharacters preserved
#───────────────────────────────────────────────────────────────────────────────

echo "→ Test 3: argv with spaces, quotes, and shell metacharacters"
tricky_a='hello world'
tricky_b="it's a 'mixed' \"quote\" test"
# shellcheck disable=SC2016  # literal metacharacters are the point of this test
tricky_c='$(rm -rf /); `id`; |&;<>'

out=$(SSH_OCREMOTE_PRESENT=1 run_cli msg "$tricky_a" "$tricky_b" "$tricky_c" 2>&1) \
  || { fail_ "exit code $? (expected 0)"; out=""; }

# argc should be 4: clawctl-remote path + msg + 3 tricky args.
if [[ "$out" == *"ARGC=5"* ]]; then
  ok "argc=5"
else
  fail_ "argc mismatch"
  printf '%s\n' "$out" | sed 's/^/      /' >&2
fi

for needle in "ARG=<</usr/local/bin/clawctl-remote>>" \
              "ARG=<<msg>>" \
              "ARG=<<${tricky_a}>>" \
              "ARG=<<${tricky_b}>>" \
              "ARG=<<${tricky_c}>>"; do
  if [[ "$out" == *"$needle"* ]]; then
    ok "preserved: $needle"
  else
    fail_ "argv mutated, missing: $needle"
    printf '%s\n' "$out" | sed 's/^/      /' >&2
  fi
done

#───────────────────────────────────────────────────────────────────────────────
# Test 4: missing CLAWCTL_SSH_HOST still exits 2 (regression on _require_ssh_host)
#───────────────────────────────────────────────────────────────────────────────

echo "→ Test 4: CLAWCTL_SSH_HOST unset still exits 2"
set +e
out=$(PATH="$TMP:$PATH" CLAWCTL_SSH_HOST="" "$BIN" cli agents list 2>&1)
ec=$?
set -e

if [ "$ec" -eq 2 ]; then
  ok "exit code 2"
else
  fail_ "exit code $ec (expected 2)"
fi

if [[ "$out" == *"CLAWCTL_SSH_HOST"* ]]; then
  ok "stderr names the missing env var"
else
  fail_ "stderr missing CLAWCTL_SSH_HOST hint: $out"
fi

#───────────────────────────────────────────────────────────────────────────────

echo
if (( fail == 0 )); then
  echo "✓ $pass checks passed"
  exit 0
fi
echo "✗ $fail checks failed ($pass passed)" >&2
exit 1
