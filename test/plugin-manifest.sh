#!/usr/bin/env bash
# plugin-manifest.sh — validate .claude-plugin/{plugin,marketplace}.json structure
# and assert commands/*.md / skills/*/SKILL.md file conventions.
# Exit 0 on success, 1 on any failure.

set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
PLUGIN_JSON="$ROOT/.claude-plugin/plugin.json"
MARKET_JSON="$ROOT/.claude-plugin/marketplace.json"
PASS=0
FAIL=0

ok()   { echo "  ok: $*"; PASS=$((PASS+1)); }
fail() { echo "FAIL: $*" >&2; FAIL=$((FAIL+1)); }

# ── JSON parse ────────────────────────────────────────────────────────────────

if jq . "$PLUGIN_JSON" >/dev/null 2>&1; then
  ok "plugin.json parses as JSON"
else
  fail "plugin.json is not valid JSON"
fi

if jq . "$MARKET_JSON" >/dev/null 2>&1; then
  ok "marketplace.json parses as JSON"
else
  fail "marketplace.json is not valid JSON"
fi

# ── Required metadata keys in plugin.json ────────────────────────────────────

for key in name version description repository license; do
  val=$(jq -r --arg k "$key" '.[$k] // empty' "$PLUGIN_JSON")
  if [[ -n "$val" ]]; then
    ok "plugin.json has '$key'"
  else
    fail "plugin.json missing key '$key'"
  fi
done

# ── Version format (vX.Y.Z or X.Y.Z) ────────────────────────────────────────

version=$(jq -r '.version // empty' "$PLUGIN_JSON")
if [[ "$version" =~ ^v?[0-9]+\.[0-9]+\.[0-9]+$ ]]; then
  ok "plugin.json version '$version' matches vX.Y.Z / X.Y.Z"
else
  fail "plugin.json version '$version' does not match vX.Y.Z / X.Y.Z"
fi

# ── File conventions: commands/*.md and skills/*/SKILL.md ────────────────────

check_doc() {
  local f="$1"
  if [[ ! -s "$f" ]]; then
    fail "$f is empty"
    return
  fi
  first=$(head -n1 "$f")
  if [[ "$first" == "---" || "$first" == \#* ]]; then
    ok "$f starts with frontmatter or H1"
  else
    fail "$f must start with '---' or '#'; got: $first"
  fi
}

while IFS= read -r -d '' f; do
  check_doc "$f"
done < <(find "$ROOT/commands" -maxdepth 1 -name '*.md' -print0 | sort -z)

while IFS= read -r -d '' f; do
  check_doc "$f"
done < <(find "$ROOT/skills" -name 'SKILL.md' -print0 | sort -z)

# ── marketplace.json path references ─────────────────────────────────────────

while IFS= read -r path_val; do
  # Resolve relative paths against the repo root
  resolved="$ROOT/$path_val"
  # Strip trailing slash for the existence check
  resolved="${resolved%/}"
  if [[ -e "$resolved" ]]; then
    ok "marketplace.json path '$path_val' exists"
  else
    fail "marketplace.json path '$path_val' not found on disk (resolved: $resolved)"
  fi
done < <(jq -r '
  .plugins[]?
  | (.source // empty), (.skills // empty)
  | select(test("^[./]"))
' "$MARKET_JSON")

# ── Summary ───────────────────────────────────────────────────────────────────

echo ""
echo "plugin-manifest: $PASS passed, $FAIL failed"
[[ $FAIL -eq 0 ]] || exit 1
