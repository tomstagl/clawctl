#!/usr/bin/env bash
# clawctl.bash — DEPRECATED bash entrypoint of clawctl.
#
# This script is the original bash MVP. The typed Go binary at
# ./cmd/clawctl/ is now the supported entrypoint; this file is kept for
# one release cycle as a transition aid for users who scripted against
# the bash output. It will be removed in the next release.
#
# Phases A (transport), B-1 (response redaction), C (claim verification),
# and lightweight Phase E (clawctl trace) live here.

set -euo pipefail

# Deprecation notice: emitted on `--help` / `help` / no-arg invocation
# (see the dispatcher's help branch). Kept as a function so the message
# stays in one place.
_print_deprecation_banner() {
  cat >&2 <<'EOF'
╔════════════════════════════════════════════════════════════════════════════╗
║  clawctl.bash is DEPRECATED.                                               ║
║                                                                            ║
║  The typed Go binary `clawctl` is now the supported entrypoint. Install    ║
║  it via `install/install.sh` (release binary) or `go build -o clawctl      ║
║  ./cmd/clawctl` from this repo. The bash entrypoint will be removed one    ║
║  release after this one — please migrate.                                  ║
╚════════════════════════════════════════════════════════════════════════════╝
EOF
}

CLAWCTL_HOST="${CLAWCTL_HOST:-}"
CLAWCTL_KEYCHAIN_SERVICE="${CLAWCTL_KEYCHAIN_SERVICE:-openclaw-gateway-token}"
CLAWCTL_TIMEOUT="${CLAWCTL_TIMEOUT:-60}"
CLAWCTL_CACHE_DIR="${CLAWCTL_CACHE_DIR:-$HOME/.cache/clawctl}"
CLAWCTL_MODELS_TTL="${CLAWCTL_MODELS_TTL:-60}"
CLAWCTL_NO_REDACT="${CLAWCTL_NO_REDACT:-0}"
CLAWCTL_SSH_HOST="${CLAWCTL_SSH_HOST:-}"

mkdir -p "$CLAWCTL_CACHE_DIR"

#───────────────────────────────────────────────────────────────────────────────
# helpers
#───────────────────────────────────────────────────────────────────────────────

_require_host() {
  if [ -z "$CLAWCTL_HOST" ]; then
    echo "clawctl: CLAWCTL_HOST not set. Export it (e.g. export CLAWCTL_HOST=http://your-openclaw-host:18789)." >&2
    exit 2
  fi
}

_require_ssh_host() {
  if [ -z "$CLAWCTL_SSH_HOST" ]; then
    echo "clawctl: CLAWCTL_SSH_HOST not set. Export it (e.g. export CLAWCTL_SSH_HOST=user@host)." >&2
    exit 2
  fi
}

_token() {
  security find-generic-password -s "$CLAWCTL_KEYCHAIN_SERVICE" -a "$USER" -w
}

# W3C traceparent: 00-<32-hex-trace-id>-<16-hex-span-id>-01
_traceparent() {
  printf '00-%s-%s-01' "$(openssl rand -hex 16)" "$(openssl rand -hex 8)"
}

_trace_id_of() { printf '%s' "$1" | cut -d- -f2; }

# Redact known secret patterns from stdin → stdout.
# Writes hit-kind summary to stderr and appends an audit entry on every match.
# When CLAWCTL_REDACT_SINK is set, also writes a JSON array of
# {kind, offset_hint, count} entries (one per match, byte offset into the
# pre-redaction input) to that path so envelope emitters can populate
# redactions[] without re-parsing stderr. Exits 0 always (redaction is not a
# transport failure).
_redact() {
  if [ "$CLAWCTL_NO_REDACT" = "1" ]; then
    # Honor sink even on bypass, so envelope emitters always see a valid JSON
    # array rather than a missing file.
    if [ -n "${CLAWCTL_REDACT_SINK:-}" ]; then
      printf '[]' > "$CLAWCTL_REDACT_SINK"
    fi
    cat
    return 0
  fi

  local agent="${1:-?}"
  local gw_token=""
  gw_token="$(_token 2>/dev/null || true)"

  AGENT="$agent" GW_TOKEN="$gw_token" AUDIT="$CLAWCTL_CACHE_DIR/last-redaction" \
  SINK="${CLAWCTL_REDACT_SINK:-}" \
  perl -e '
    my $text = do { local $/; <STDIN> };
    my $orig = $text;
    my %pat = (
      dt0c01    => qr/dt0c01\.[A-Za-z0-9_.\-]{20,}/,
      dt0s16    => qr/dt0s16\.[A-Za-z0-9_.\-]{20,}/,
      gh_token  => qr/gh[psoru]_[A-Za-z0-9]{30,}/,
      aws_akid  => qr/AKIA[0-9A-Z]{16}/,
      jwt       => qr/eyJ[A-Za-z0-9_\-]+\.[A-Za-z0-9_\-]+\.[A-Za-z0-9_\-]+/,
      brave     => qr/BSA[A-Za-z0-9_\-]{25,}/,
    );
    my %hits;
    my @events; # one entry per match: {kind, offset_hint}
    for my $kind (sort keys %pat) {
      my $re = $pat{$kind};
      # Scan the unmodified input for offsets; offsets stay stable because we
      # never mutate $orig.
      while ($orig =~ /$re/g) {
        push @events, { kind => $kind, offset_hint => $-[0] + 0 };
      }
      # Then substitute against $text. Substitution shifts later offsets in
      # $text, so we deliberately do NOT use $text positions for offset_hint.
      $hits{$kind}++ while $text =~ s/$re/"<REDACTED:$kind:".substr($&,0,11)."\xE2\x80\xA6>"/e;
    }
    # gateway-token literal (length-bounded)
    my $gw = $ENV{GW_TOKEN} // "";
    if (length($gw) >= 16) {
      my $q = quotemeta($gw);
      while ($orig =~ /$q/g) {
        push @events, { kind => "gw_token", offset_hint => $-[0] + 0 };
      }
      $hits{gw_token}++ while $text =~ s/$q/"<REDACTED:gw_token:".substr($gw,0,6)."\xE2\x80\xA6>"/e;
    }
    print STDOUT $text;

    my $sink = $ENV{SINK} // "";
    if ($sink ne "") {
      my @sorted = sort {
        $a->{offset_hint} <=> $b->{offset_hint} || $a->{kind} cmp $b->{kind}
      } @events;
      my @parts;
      for my $e (@sorted) {
        push @parts, sprintf(
          q({"kind":"%s","offset_hint":%d,"count":1}),
          $e->{kind}, $e->{offset_hint},
        );
      }
      my $json = "[" . join(",", @parts) . "]";
      if (open(my $fh, ">", $sink)) {
        print $fh $json;
        close($fh);
      }
    }

    if (%hits) {
      my $ts = `date -u +%FT%TZ`; chomp $ts;
      my $kinds = join(",", sort keys %hits);
      my $agent = $ENV{AGENT} // "?";
      print STDERR "WARNING: redacted secret pattern(s): $kinds (agent=$agent). Likely R-11 violation upstream; consider rotating the matching credential.\n";
      open(my $fh, ">>", $ENV{AUDIT}) or exit 0;
      print $fh "$ts agent=$agent kinds=$kinds\n";
      close($fh);
    }
  '
}

# Parse a gateway error body and print "<code>: <message>" to stderr if recognizable.
_explain_http_error() {
  local code="$1" body_file="$2"
  if [ -s "$body_file" ]; then
    # OpenAI-style { "error": { "code": "...", "message": "..." } }
    local err_code err_msg
    err_code=$(jq -r '.error.code // .error.type // .code // empty' "$body_file" 2>/dev/null || true)
    err_msg=$(jq -r  '.error.message // .message // empty' "$body_file" 2>/dev/null || true)
    if [ -n "$err_code" ] || [ -n "$err_msg" ]; then
      echo "gateway error: HTTP $code ${err_code:+[$err_code] }${err_msg}" >&2
      return
    fi
  fi
  echo "gateway error: HTTP $code (no structured body)" >&2
}

# Slug validation against a 60s-cached `clawctl models` response.
_models_cache() {
  local cache="$CLAWCTL_CACHE_DIR/models.json"
  local age_sec=999999
  if [ -f "$cache" ]; then
    age_sec=$(( $(date +%s) - $(stat -f %m "$cache" 2>/dev/null || echo 0) ))
  fi
  if [ "$age_sec" -gt "$CLAWCTL_MODELS_TTL" ]; then
    local curl_exit=0
    set +e
    curl --silent --show-error --fail-with-body \
        --max-time "$CLAWCTL_TIMEOUT" \
        --retry 2 --retry-connrefused --retry-delay 1 --retry-all-errors \
        -H "Authorization: Bearer $(_token)" \
        -H "Accept: application/json" \
        -o "$cache.tmp" \
        "${CLAWCTL_HOST}/v1/models" >/dev/null 2>&1
    curl_exit=$?
    set -e
    if [ "$curl_exit" -eq 0 ]; then
      mv "$cache.tmp" "$cache"
    else
      rm -f "$cache.tmp"
      # Fall back to a stale cache if one exists; otherwise propagate the
      # curl exit code so the documented contract (6/7/22/28) reaches the
      # caller instead of a generic 1.
      [ -f "$cache" ] || return "$curl_exit"
    fi
  fi
  cat "$cache"
}

# Extract bare slug list (after "openclaw/" prefix) from cached models.
_known_agents() {
  _models_cache | jq -r '.data[].id' 2>/dev/null \
    | sed -n 's|^openclaw/||p' \
    | sort -u
}

_validate_agent() {
  local agent="$1"
  # Always accept "default"; otherwise require it to match a known slug.
  if [ "$agent" = "default" ]; then return 0; fi
  local known
  if ! known=$(_known_agents 2>/dev/null) || [ -z "$known" ]; then
    # Cache unavailable — fail open with a warning.
    echo "warning: clawctl models cache unavailable, skipping slug validation for '$agent'" >&2
    return 0
  fi
  if printf '%s\n' "$known" | grep -Fxq "$agent"; then return 0; fi
  echo "clawctl: unknown agent '$agent'" >&2
  echo "valid agents:" >&2
  printf '%s\n' "$known" | awk '{print "  "$0}' >&2
  return 2
}

# Call /v1/chat/completions; returns body on stdout, prints trace-id + errors on stderr.
# args: <agent> <stream:bool> <session_key_or_empty> <text> [<traceparent>]
# If traceparent is omitted, one is generated. Callers that need to bind the
# generated trace-id into a structured envelope MUST pass their own.
_chat() {
  local agent="$1" stream="$2" session="$3" text="$4" tp="${5:-}"
  local body http_code curl_exit
  if [ -z "$tp" ]; then
    tp="$(_traceparent)"
  fi
  echo "trace-id: $(_trace_id_of "$tp")" >&2

  body="$(mktemp)"
  trap 'rm -f "$body"' RETURN

  local payload
  if [ -n "$session" ]; then
    payload=$(jq -nc \
      --arg model "openclaw/${agent}" \
      --arg content "$text" \
      --arg user "$session" \
      --argjson stream "$stream" \
      '{model:$model, stream:$stream, user:$user, messages:[{role:"user", content:$content}]}')
  else
    payload=$(jq -nc \
      --arg model "openclaw/${agent}" \
      --arg content "$text" \
      --argjson stream "$stream" \
      '{model:$model, stream:$stream, messages:[{role:"user", content:$content}]}')
  fi

  set +e
  http_code=$(curl --silent --show-error --fail-with-body \
    --max-time "$CLAWCTL_TIMEOUT" \
    -H "Authorization: Bearer $(_token)" \
    -H "Content-Type: application/json" \
    -H "Accept: application/json" \
    -H "traceparent: $tp" \
    -o "$body" \
    --write-out '%{http_code}' \
    -d "$payload" \
    "${CLAWCTL_HOST}/v1/chat/completions")
  curl_exit=$?
  set -e

  if [ "$curl_exit" -ne 0 ]; then
    case "$curl_exit" in
      6)  echo "clawctl: DNS resolution failed for ${CLAWCTL_HOST}" >&2 ;;
      7)  echo "clawctl: connection refused: ${CLAWCTL_HOST}" >&2 ;;
      28) echo "clawctl: timeout (${CLAWCTL_TIMEOUT}s) calling ${CLAWCTL_HOST}" >&2 ;;
      22) _explain_http_error "$http_code" "$body" ;;
      *)  echo "clawctl: curl exit $curl_exit (HTTP $http_code)" >&2 ;;
    esac
    return "$curl_exit"
  fi

  cat "$body"
}

#───────────────────────────────────────────────────────────────────────────────
# subcommands
#───────────────────────────────────────────────────────────────────────────────

cmd="${1:-help}"; shift || true

case "$cmd" in

  health)
    _require_host
    curl --silent --show-error --fail-with-body \
      --max-time "$CLAWCTL_TIMEOUT" \
      --retry 2 --retry-connrefused --retry-delay 1 --retry-all-errors \
      "${CLAWCTL_HOST}/health" | jq .
    ;;

  models)
    _require_host
    _models_cache | jq .
    ;;

  msg|stream)
    _require_host
    is_stream=false
    [ "$cmd" = "stream" ] && is_stream=true

    session=""
    envelope=false
    while [ "${1:-}" != "" ]; do
      case "$1" in
        -s|--session) session="${2:-}"; shift 2 ;;
        -s=*)         session="${1#*=}"; shift ;;
        --session=*)  session="${1#*=}"; shift ;;
        --envelope)   envelope=true; shift ;;
        --)           shift; break ;;
        -*)
          echo "clawctl $cmd: unknown flag '$1'" >&2; exit 2 ;;
        *) break ;;
      esac
    done

    if [ "${1:-}" = "" ]; then
      echo "usage: clawctl $cmd [-s <session-key>] [--envelope] <agent> [<text>]   (text from stdin if omitted)" >&2
      echo "       agent = 'default' or a specific agent slug" >&2
      exit 2
    fi
    agent="$1"; shift
    _validate_agent "$agent" || exit $?

    if [ "$#" -gt 0 ]; then text="$*"; else text="$(cat)"; fi

    if [ "$envelope" = "true" ] && [ "$is_stream" = "false" ]; then
      tp="$(_traceparent)"
      body="$(mktemp)"
      red_out="$(mktemp)"
      red_sink="$(mktemp)"
      trap 'rm -f "$body" "$red_out" "$red_sink"' EXIT
      _chat "$agent" false "$session" "$text" "$tp" > "$body"

      jq -r '.choices[0].message.content // ""' "$body" \
        | CLAWCTL_REDACT_SINK="$red_sink" _redact "$agent" > "$red_out"

      redactions_json="[]"
      if [ -s "$red_sink" ]; then
        redactions_json="$(cat "$red_sink")"
      fi

      finish_raw=$(jq -r '.choices[0].finish_reason // "stop"' "$body")
      case "$finish_raw" in
        stop|length|content_filter|error) finish="$finish_raw" ;;
        tool_calls|function_call|tool_call) finish="tool_call" ;;
        *) finish="stop" ;;
      esac

      in_tok=$(jq -r '.usage.prompt_tokens // empty' "$body")
      out_tok=$(jq -r '.usage.completion_tokens // empty' "$body")
      tot_tok=$(jq -r '.usage.total_tokens // empty' "$body")

      jq -n \
        --arg agent "openclaw/$agent" \
        --arg tp "$tp" \
        --arg session "$session" \
        --arg input "$text" \
        --rawfile output "$red_out" \
        --argjson redactions "$redactions_json" \
        --arg finish "$finish" \
        --arg in_tok "$in_tok" \
        --arg out_tok "$out_tok" \
        --arg tot_tok "$tot_tok" \
        '{
           envelope_version: "1",
           kind: "tool_response",
           agent: $agent,
           traceparent: $tp,
           input: { role: "user", content: $input },
           output: $output,
           redactions: $redactions,
           usage: ({}
             | (if $in_tok  != "" then .input_tokens  = ($in_tok|tonumber)  else . end)
             | (if $out_tok != "" then .output_tokens = ($out_tok|tonumber) else . end)
             | (if $tot_tok != "" then .total_tokens  = ($tot_tok|tonumber) else . end)),
           finish_reason: $finish
         }
         + (if $session != "" then {session_id: $session} else {} end)'
      exit 0
    fi

    if [ "$envelope" = "true" ] && [ "$is_stream" = "true" ]; then
      tp="$(_traceparent)"
      raw="$(mktemp)"
      contents="$(mktemp)"
      per_chunk="$(mktemp)"
      per_chunk_red="$(mktemp)"
      meta="$(mktemp)"
      agg_sink="$(mktemp)"
      chunk_sink="$(mktemp)"
      trap 'rm -f "$raw" "$contents" "$per_chunk" "$per_chunk_red" "$meta" "$agg_sink" "$chunk_sink"' EXIT

      _chat "$agent" true "$session" "$text" "$tp" > "$raw" || exit $?

      # Parse SSE buffer: emit one JSON-encoded {"c": "..."} per non-empty
      # delta to $contents, plus a single {"finish":..., "usage":...} to $meta.
      python3 - "$raw" "$contents" "$meta" <<'PY'
import json, sys
raw_path, contents_path, meta_path = sys.argv[1], sys.argv[2], sys.argv[3]
finish = "stop"
usage = {}
err = None
with open(contents_path, "w") as out, open(raw_path) as fh:
    for line in fh:
        line = line.rstrip("\n")
        if line.startswith("event:") and "error" in line:
            err = line
            continue
        if not line.startswith("data:"):
            continue
        payload = line[5:].lstrip()
        if payload == "[DONE]":
            break
        try:
            obj = json.loads(payload)
        except Exception:
            continue
        if isinstance(obj.get("usage"), dict):
            usage = obj["usage"]
        choices = obj.get("choices") or []
        if not choices:
            if "error" in obj:
                err = json.dumps(obj["error"])
            continue
        c0 = choices[0]
        if c0.get("finish_reason"):
            finish = c0["finish_reason"]
        delta = c0.get("delta") or c0.get("message") or {}
        content = delta.get("content") or ""
        if content:
            out.write(json.dumps({"c": content}) + "\n")
with open(meta_path, "w") as fh:
    json.dump({"finish": finish, "usage": usage, "err": err}, fh)
PY

      # Per-chunk redaction pass (boundary-detection + content for emission).
      # stderr suppressed so the aggregate pass below remains the canonical
      # WARNING source; audit-file dups are accepted. Per-chunk redactions[]
      # is captured into $per_chunk_red, one JSON-array line per chunk, in the
      # same order as $per_chunk so we can join them by line number.
      : > "$per_chunk"
      : > "$per_chunk_red"
      agg=""
      while IFS= read -r line; do
        c=$(printf '%s' "$line" | jq -r '.c')
        : > "$chunk_sink"
        rc=$(printf '%s' "$c" \
          | CLAWCTL_REDACT_SINK="$chunk_sink" _redact "$agent" 2>/dev/null)
        printf '%s' "$rc" | jq -cRs '{c: .}' >> "$per_chunk"
        if [ -s "$chunk_sink" ]; then
          cat "$chunk_sink" >> "$per_chunk_red"
        else
          printf '[]' >> "$per_chunk_red"
        fi
        printf '\n' >> "$per_chunk_red"
        agg+="$c"
      done < "$contents"

      # Aggregate redaction (canonical pass: stderr WARNING + audit log fire here).
      agg_redacted=$(printf '%s' "$agg" \
        | CLAWCTL_REDACT_SINK="$agg_sink" _redact "$agent")
      agg_redactions_json="[]"
      if [ -s "$agg_sink" ]; then
        agg_redactions_json="$(cat "$agg_sink")"
      fi

      per_chunk_concat=$(jq -rs 'map(.c) | add // ""' "$per_chunk")

      finish_raw=$(jq -r '.finish // "stop"' "$meta")
      case "$finish_raw" in
        stop|length|content_filter|error) finish="$finish_raw" ;;
        tool_calls|function_call|tool_call) finish="tool_call" ;;
        *) finish="stop" ;;
      esac
      in_tok=$(jq -r '.usage.prompt_tokens // empty' "$meta")
      out_tok=$(jq -r '.usage.completion_tokens // empty' "$meta")
      tot_tok=$(jq -r '.usage.total_tokens // empty' "$meta")

      if [ "$per_chunk_concat" = "$agg_redacted" ]; then
        # Boundary-safe: per-chunk redaction sums to the aggregate-redacted
        # text, so no secret crossed an SSE chunk boundary. Emit per-chunk.
        idx=0
        # Read the per-chunk redactions sidecar in lockstep with $per_chunk.
        # Bash 3.x has no good way to read two files in parallel without
        # subshells, so we collect the redactions array first.
        red_lines=()
        while IFS= read -r r_line; do
          red_lines+=("$r_line")
        done < "$per_chunk_red"

        while IFS= read -r line; do
          content=$(printf '%s' "$line" | jq -r '.c')
          chunk_red="${red_lines[$idx]:-[]}"
          jq -nc \
            --arg agent "openclaw/$agent" \
            --arg tp "$tp" \
            --arg session "$session" \
            --arg content "$content" \
            --argjson index "$idx" \
            --argjson redactions "$chunk_red" \
            '{envelope_version:"1", kind:"tool_stream_chunk",
              agent:$agent, traceparent:$tp, index:$index,
              delta:{content:$content}, redactions:$redactions, finish_reason:null}
             + (if $session != "" then {session_id:$session} else {} end)'
          idx=$((idx + 1))
        done < "$per_chunk"
      else
        # A secret pattern spans chunk boundaries — coalesce into a single
        # ToolStreamChunk carrying the boundary-safe redacted aggregate. The
        # aggregate redactions[] is the truthful set when boundary-coalescing.
        echo "warning: redacted secret pattern crossed SSE chunk boundary; coalesced into one chunk" >&2
        jq -nc \
          --arg agent "openclaw/$agent" \
          --arg tp "$tp" \
          --arg session "$session" \
          --arg content "$agg_redacted" \
          --argjson redactions "$agg_redactions_json" \
          '{envelope_version:"1", kind:"tool_stream_chunk",
            agent:$agent, traceparent:$tp, index:0,
            delta:{content:$content}, redactions:$redactions, finish_reason:null}
           + (if $session != "" then {session_id:$session} else {} end)'
      fi

      # Terminal ToolResponse with the aggregate-redacted output.
      jq -nc \
        --arg agent "openclaw/$agent" \
        --arg tp "$tp" \
        --arg session "$session" \
        --arg input "$text" \
        --arg output "$agg_redacted" \
        --argjson redactions "$agg_redactions_json" \
        --arg finish "$finish" \
        --arg in_tok "$in_tok" \
        --arg out_tok "$out_tok" \
        --arg tot_tok "$tot_tok" \
        '{envelope_version:"1", kind:"tool_response",
          agent:$agent, traceparent:$tp,
          input:{role:"user", content:$input},
          output:$output, redactions:$redactions,
          usage: ({}
            | (if $in_tok  != "" then .input_tokens  = ($in_tok|tonumber)  else . end)
            | (if $out_tok != "" then .output_tokens = ($out_tok|tonumber) else . end)
            | (if $tot_tok != "" then .total_tokens  = ($tot_tok|tonumber) else . end)),
          finish_reason: $finish}
         + (if $session != "" then {session_id:$session} else {} end)'
      exit 0
    fi

    if [ "$is_stream" = "true" ]; then
      raw="$(mktemp)"
      trap 'rm -f "$raw"' EXIT
      _chat "$agent" true "$session" "$text" > "$raw" || exit $?
      # SSE → plain text. Buffer fully, then redact (boundary-safe), then emit.
      python3 - "$raw" <<'PY' | _redact "$agent"
import json, sys
content = []
err = None
with open(sys.argv[1]) as fh:
    for line in fh:
        line = line.rstrip("\n")
        if line.startswith("event:") and "error" in line:
            err = line
            continue
        if not line.startswith("data:"):
            continue
        payload = line[5:].lstrip()
        if payload == "[DONE]":
            break
        try:
            obj = json.loads(payload)
        except Exception:
            continue
        choices = obj.get("choices") or []
        if not choices:
            if "error" in obj:
                err = json.dumps(obj["error"])
            continue
        delta = choices[0].get("delta") or choices[0].get("message") or {}
        chunk = delta.get("content") or ""
        if chunk:
            content.append(chunk)
sys.stdout.write("".join(content))
if err:
    sys.stderr.write(f"stream error: {err}\n")
    sys.exit(1)
PY
      stream_exit=${PIPESTATUS[0]}
      printf '\n'
      [ "$stream_exit" -eq 0 ] || exit "$stream_exit"
    else
      _chat "$agent" false "$session" "$text" \
        | jq -r '.choices[0].message.content // (.error // .) | (if type=="object" then tostring else . end)' \
        | _redact "$agent"
      printf '\n'
    fi
    ;;

  raw)
    _require_host
    method="${1:-GET}"; path="${2:-/health}"; shift 2 || true
    tp="$(_traceparent)"
    echo "trace-id: $(_trace_id_of "$tp")" >&2
    retry_args=()
    if [ "$method" = "GET" ]; then
      retry_args=(--retry 2 --retry-connrefused --retry-delay 1 --retry-all-errors)
    fi
    # Bash 3.2 (macOS) errors on `"${arr[@]}"` when arr is empty under set -u;
    # the ${arr[@]+...} guard expands to nothing if unset/empty.
    curl --silent --show-error --fail-with-body \
      --max-time "$CLAWCTL_TIMEOUT" \
      ${retry_args[@]+"${retry_args[@]}"} \
      -X "$method" "${CLAWCTL_HOST}${path}" \
      -H "Authorization: Bearer $(_token)" \
      -H "Accept: application/json" \
      -H "traceparent: $tp" \
      "$@"
    ;;

  cli)
    # Run an openclaw CLI command on the host via clawctl-remote.
    # clawctl-remote is REQUIRED: it accepts argv as a slice and invokes openclaw
    # without shell-string interpolation, so callers cannot inject host-side
    # shell metacharacters via argv. There is no fallback path.
    _require_ssh_host
    if ! ssh -o BatchMode=yes -o ConnectTimeout=5 "$CLAWCTL_SSH_HOST" \
        'test -x /usr/local/bin/clawctl-remote' 2>/dev/null; then
      cat >&2 <<EOF
clawctl cli: clawctl-remote not found at /usr/local/bin/clawctl-remote on $CLAWCTL_SSH_HOST.

clawctl-remote is required so argv reaches openclaw without shell-string
interpolation. Install it on the host (see the "clawctl-remote (required for
clawctl cli)" section in README.md for the full procedure):

  ssh $CLAWCTL_SSH_HOST 'sudo install -m 0755 /dev/stdin /usr/local/bin/clawctl-remote' <<'OCREMOTE'
  #!/usr/bin/env bash
  set -euo pipefail
  export PATH="\$HOME/.npm-global/bin:\$PATH"
  exec openclaw "\$@"
  OCREMOTE
EOF
      exit 2
    fi
    ssh "$CLAWCTL_SSH_HOST" -- /usr/local/bin/clawctl-remote "$@"
    ;;

  verify)
    sub="${1:-}"; shift || true
    case "$sub" in
      commit)
        hash="${1:-}"
        [ -z "$hash" ] && { echo "usage: clawctl verify commit <hash>" >&2; exit 2; }
        if t=$(git cat-file -t "$hash" 2>/dev/null) && [ "$t" = "commit" ]; then
          echo "verified: commit $hash"
          exit 0
        fi
        echo "unverified: commit $hash not found in $(git rev-parse --show-toplevel 2>/dev/null || pwd)" >&2
        exit 1
        ;;
      pr)
        spec="${1:-}"
        [ -z "$spec" ] && { echo "usage: clawctl verify pr <repo>#<num>  (repo=owner/name)" >&2; exit 2; }
        repo="${spec%%#*}"; num="${spec##*#}"
        if [ "$repo" = "$spec" ] || [ -z "$num" ]; then
          echo "usage: clawctl verify pr <repo>#<num>" >&2; exit 2
        fi
        out=$(gh pr view "$num" --repo "$repo" --json state,url,title 2>/dev/null) || {
          echo "unverified: PR $repo#$num not accessible" >&2; exit 1
        }
        echo "verified: $(printf '%s' "$out" | jq -r '"\(.state) — \(.title) — \(.url)"')"
        exit 0
        ;;
      issue)
        spec="${1:-}"
        [ -z "$spec" ] && { echo "usage: clawctl verify issue <repo>#<num>" >&2; exit 2; }
        repo="${spec%%#*}"; num="${spec##*#}"
        if [ "$repo" = "$spec" ] || [ -z "$num" ]; then
          echo "usage: clawctl verify issue <repo>#<num>" >&2; exit 2
        fi
        out=$(gh issue view "$num" --repo "$repo" --json state,url,title 2>/dev/null) || {
          echo "unverified: issue $repo#$num not accessible" >&2; exit 1
        }
        echo "verified: $(printf '%s' "$out" | jq -r '"\(.state) — \(.title) — \(.url)"')"
        exit 0
        ;;
      file)
        path="${1:-}"; ref="${2:-}"
        [ -z "$path" ] && { echo "usage: clawctl verify file <path> [<ref>]" >&2; exit 2; }
        if [ -n "$ref" ]; then
          if git cat-file -e "${ref}:${path}" 2>/dev/null; then
            echo "verified: $path @ $ref"; exit 0
          fi
          echo "unverified: $path not present at $ref" >&2; exit 1
        else
          if [ -e "$path" ]; then
            echo "verified: $path (working tree)"; exit 0
          fi
          echo "unverified: $path not present in working tree" >&2; exit 1
        fi
        ;;
      ""|help|-h|--help)
        cat <<EOF >&2
clawctl verify <kind> <args>
  commit <hash>             — git cat-file -t == commit
  pr     <owner/name>#<n>   — gh pr view returns
  issue  <owner/name>#<n>   — gh issue view returns
  file   <path> [<ref>]     — file exists in working tree (or at ref)

Exit codes: 0 verified, 1 not found, 2 usage/ambiguous.
EOF
        exit 2
        ;;
      *)
        echo "clawctl verify: unknown kind '$sub' (try 'clawctl verify help')" >&2; exit 2 ;;
    esac
    ;;

  trace)
    tid="${1:-}"
    [ -z "$tid" ] && { echo "usage: clawctl trace <trace-id-32-hex>" >&2; exit 2; }
    JAEGER_UI="${CLAWCTL_JAEGER_UI:-}"
    if [ -z "$JAEGER_UI" ]; then
      echo "clawctl: CLAWCTL_JAEGER_UI not set. Export your Jaeger base URL (e.g. http://jaeger:16686)." >&2
      exit 2
    fi
    echo "trace-id: $tid"
    echo "UI:       $JAEGER_UI/trace/$tid"
    echo
    api="$JAEGER_UI/jaeger/api/traces/$tid"
    if curl -sS --max-time 5 "$api" 2>/dev/null | python3 -c '
import sys, json
try:
    d=json.load(sys.stdin)
except Exception:
    sys.exit(2)
errs=d.get("errors") or []
if errs:
    print("Jaeger:", errs[0].get("msg","unknown"))
    sys.exit(1)
traces=d.get("data") or []
if not traces:
    print("Jaeger: no spans for this trace")
    sys.exit(1)
t=traces[0]
spans=t.get("spans",[])
procs=t.get("processes",{})
print(f"Spans: {len(spans)}")
for s in spans[:30]:
    op=s.get("operationName","?")
    dur=s.get("duration",0)/1000
    p=procs.get(s.get("processID",""),{})
    svc=p.get("serviceName","?")
    print(f"  {svc:<24s} {op:<40s} {dur:>7.0f}ms")
sys.exit(0)
'; then :; fi
    ;;

  help|--help|-h|"")
    _print_deprecation_banner
    cat <<EOF
clawctl.bash — openclaw client, bash entrypoint (DEPRECATED; host: ${CLAWCTL_HOST:-<unset>})

  clawctl health                              gateway liveness
  clawctl models                              list registered agents (60s cache)
  clawctl msg [-s SESSION] [--envelope] AGENT [TEXT]
                                              chat with agent; stdin if no text
                                              --envelope emits a v1 ToolResponse JSON document
  clawctl stream [-s SESSION] [--envelope] AGENT [TEXT]
                                              same, SSE; output buffered + redacted
                                              --envelope emits NDJSON ToolStreamChunks + final ToolResponse
  clawctl raw METHOD PATH [curl-args]         arbitrary call with auth + traceparent
  clawctl cli SUBCOMMAND...                   run \`openclaw …\` over SSH on host
  clawctl verify KIND ARGS                    R-2 claim verification (see 'clawctl verify help')
  clawctl trace TRACE-ID                      lookup hint for a trace id

Required env:
  CLAWCTL_HOST              gateway URL (e.g. http://your-openclaw-host:18789)
  CLAWCTL_SSH_HOST          user@host for the gateway machine (only required for 'clawctl cli')
  CLAWCTL_JAEGER_UI         Jaeger base URL (only required for 'clawctl trace')

Optional env:
  CLAWCTL_KEYCHAIN_SERVICE  keychain service for the bearer token (default: openclaw-gateway-token)
  CLAWCTL_TIMEOUT           per-call timeout in seconds (default 60)
  CLAWCTL_NO_REDACT=1       disable client-side response redaction (NOT recommended)
  CLAWCTL_MODELS_TTL        seconds to cache /v1/models (default 60)

Exit codes (transport):
  0   ok
  2   usage error, missing env var, unknown subcommand
  6   DNS resolution failed
  7   connection refused
  22  HTTP 4xx/5xx (body printed; reason on stderr)
  28  timeout

Subcommand-specific exit codes (rationale):
  verify    1 = unverified (commit/PR/issue/file not found); see 'clawctl verify help'
  cli       pass-through: ssh and clawctl-remote/openclaw exit codes reach the caller unchanged
  trace     best-effort: returns 0 even when Jaeger is unreachable so the UI link still surfaces
EOF
    ;;

  _redact)
    # Hidden helper for parity testing only (intentionally absent from
    # `clawctl help`). Reads stdin, writes redacted text to stdout. The
    # optional positional arg is the agent tag carried into the stderr
    # WARNING line and the audit-file entry. CLAWCTL_REDACT_SINK,
    # CLAWCTL_NO_REDACT, and CLAWCTL_CACHE_DIR behave identically to
    # their use inside the production subcommands. Used by
    # test/parity-redact.sh to diff bash _redact vs the Go port.
    _redact "${1:-?}"
    ;;

  *)
    echo "clawctl: unknown command '$cmd' (try 'clawctl help')" >&2
    exit 2
    ;;
esac
