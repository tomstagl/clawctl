#!/usr/bin/env bash
# parity-verify.sh — diff `clawctl verify {commit,pr,issue,file,help}`
# between the bash entrypoint and the Go binary.
#
# Coverage:
#   commit  — found, not-found, missing-arg
#   pr      — found, inaccessible, bad spec, missing-arg
#   issue   — found, inaccessible, missing-arg
#   file    — present in working tree, absent, present at ref, absent at ref,
#             missing-arg
#   help    — empty kind and explicit `help` both print the banner with exit 2
#   error   — unknown kind
#
# Strategy: build a fixture git repo with one committed file, then run both
# binaries with the fixture as $PWD. For the `pr`/`issue` cases we shadow
# `gh` on PATH with a tiny fake whose stdout and exit code are controllable
# via env vars — same approach as test/cli-hardening.sh and the Go unit
# tests in cmd/clawctl/verify_test.go. No network, no real GitHub access.
#
# Exit 0 on success, 1 on any failure.

set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
BASH_BIN="$ROOT/clawctl"
TMP="$(mktemp -d)"
GO_BIN="$TMP/clawctl-go"
SHIMS="$TMP/shims"
mkdir -p "$SHIMS"

cleanup() { rm -rf "$TMP"; }
trap cleanup EXIT

if [[ ! -x "$BASH_BIN" ]]; then
  echo "FAIL: $BASH_BIN not executable" >&2
  exit 1
fi

if ! command -v go >/dev/null 2>&1; then
  echo "FAIL: go toolchain not on PATH" >&2
  exit 1
fi

echo "building Go binary → $GO_BIN"
( cd "$ROOT" && CGO_ENABLED=0 go build -o "$GO_BIN" ./cmd/clawctl )

# ─── Fixture git repo ────────────────────────────────────────────────────
REPO="$TMP/repo"
mkdir -p "$REPO"
(
  cd "$REPO"
  git init -q -b main
  git config user.email "parity@example.test"
  git config user.name  "Parity Test"
  echo "hello" > file.txt
  git add file.txt
  git commit -q -m "init"
)
COMMIT="$(cd "$REPO" && git rev-parse HEAD)"

# ─── Fake gh shim ────────────────────────────────────────────────────────
# Behaviour controlled per-case via $GH_OUT / $GH_EXIT exported before the
# binary runs. We mount the shim under $SHIMS and prepend $SHIMS to PATH.
cat >"$SHIMS/gh" <<'GH'
#!/usr/bin/env bash
printf '%s' "${GH_OUT:-}"
exit "${GH_EXIT:-0}"
GH
chmod +x "$SHIMS/gh"

# ─── Test plumbing ───────────────────────────────────────────────────────
fail=0; pass=0
ok()   { echo "  ok    $*"; pass=$((pass + 1)); }
nope() { echo "  FAIL  $*" >&2; fail=$((fail + 1)); }

run_one() {
  # $1 = label, $2 = bin, rest = args; sets $exit, $out, $err
  local _label="$1" _bin="$2"
  local _out_file="$TMP/$_label.out" _err_file="$TMP/$_label.err"
  set +e
  ( cd "$REPO" && PATH="$SHIMS:$PATH" "$_bin" "${@:3}" >"$_out_file" 2>"$_err_file" )
  exit_code=$?
  set -e
  out="$(cat "$_out_file")"
  err="$(cat "$_err_file")"
  exit="$exit_code"
}

diff_pair() {
  # Three labelled equality checks: stdout, stderr, exit.
  local _label="$1" _bash_out="$2" _bash_err="$3" _bash_exit="$4"
  local _go_out="$5" _go_err="$6" _go_exit="$7"
  if [[ "$_bash_out" == "$_go_out" ]]; then
    ok "$_label: stdout matches"
  else
    nope "$_label: stdout diverges"
    diff <(printf '%s' "$_bash_out") <(printf '%s' "$_go_out") || true
  fi
  if [[ "$_bash_err" == "$_go_err" ]]; then
    ok "$_label: stderr matches"
  else
    nope "$_label: stderr diverges"
    diff <(printf '%s' "$_bash_err") <(printf '%s' "$_go_err") || true
  fi
  if [[ "$_bash_exit" == "$_go_exit" ]]; then
    ok "$_label: exit $_bash_exit matches"
  else
    nope "$_label: exit diverges (bash=$_bash_exit, go=$_go_exit)"
  fi
}

# Run a paired bash/go invocation under the same env and diff the three
# streams. $1 = case label, rest = args.
parity_case() {
  local _label="$1"; shift
  run_one "bash-$_label" "$BASH_BIN" "$@" || true
  local b_out="$out" b_err="$err" b_exit="$exit"
  run_one "go-$_label"   "$GO_BIN"   "$@" || true
  local g_out="$out" g_err="$err" g_exit="$exit"
  diff_pair "$_label" "$b_out" "$b_err" "$b_exit" "$g_out" "$g_err" "$g_exit"
}

# ─── commit ──────────────────────────────────────────────────────────────
echo "case: verify commit (found)"
parity_case "commit-found" verify commit "$COMMIT"

echo "case: verify commit (missing — bogus hash)"
# bash uses `git rev-parse --show-toplevel || pwd` — set CWD to $REPO so
# both binaries land on the same toplevel string.
parity_case "commit-missing" verify commit deadbeefdeadbeefdeadbeefdeadbeefdeadbeef

echo "case: verify commit (missing arg)"
parity_case "commit-noargs" verify commit

# ─── pr ──────────────────────────────────────────────────────────────────
echo "case: verify pr (found, fake gh)"
export GH_OUT='{"state":"OPEN","url":"https://github.com/o/r/pull/12","title":"Add a thing"}'
export GH_EXIT=0
parity_case "pr-found" verify pr "o/r#12"

echo "case: verify pr (inaccessible, fake gh exits 1)"
export GH_OUT=""
export GH_EXIT=1
parity_case "pr-missing" verify pr "o/r#404"
unset GH_OUT GH_EXIT

echo "case: verify pr (bad spec — no #)"
parity_case "pr-badspec" verify pr "o/r"

echo "case: verify pr (missing arg)"
parity_case "pr-noargs" verify pr

# ─── issue ───────────────────────────────────────────────────────────────
echo "case: verify issue (found, fake gh)"
export GH_OUT='{"state":"CLOSED","url":"https://github.com/o/r/issues/3","title":"Bug"}'
export GH_EXIT=0
parity_case "issue-found" verify issue "o/r#3"

echo "case: verify issue (inaccessible)"
export GH_OUT=""
export GH_EXIT=1
parity_case "issue-missing" verify issue "o/r#7"
unset GH_OUT GH_EXIT

echo "case: verify issue (missing arg)"
parity_case "issue-noargs" verify issue

# ─── file ────────────────────────────────────────────────────────────────
echo "case: verify file (working tree, present)"
parity_case "file-present" verify file file.txt

echo "case: verify file (working tree, absent)"
parity_case "file-absent" verify file nope.txt

echo "case: verify file (at ref, present)"
parity_case "file-ref-present" verify file file.txt HEAD

echo "case: verify file (at ref, absent)"
parity_case "file-ref-absent" verify file nope.txt HEAD

echo "case: verify file (missing arg)"
parity_case "file-noargs" verify file

# ─── help / errors ───────────────────────────────────────────────────────
echo "case: verify (no kind → help banner)"
parity_case "no-kind" verify

echo "case: verify help"
parity_case "help" verify help

echo "case: verify (unknown kind)"
parity_case "unknown" verify frobnicate

#───────────────────────────────────────────────────────────────────────────
echo
echo "passed: $pass    failed: $fail"
[[ "$fail" -eq 0 ]] || exit 1
