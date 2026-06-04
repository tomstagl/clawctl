# Tool envelope (v1)

Local LLM callers (Claude Code, Codex, the typed Go binary in this repo) need a stable, typed shape to register openclaw agents as tools. `schemas/envelope.v1.json` is the source of truth for that shape; this document records the versioning policy.

## Members

The schema is a single `oneOf` over four members, distinguished by the `kind` field:

| `kind` | Direction | When emitted |
| --- | --- | --- |
| `tool_request` | caller → clawctl | Caller asks clawctl to invoke an openclaw agent. |
| `tool_response` | clawctl → caller | Final, non-streaming result; also the terminal frame of a stream. |
| `tool_stream_chunk` | clawctl → caller | One streaming delta. `finish_reason` is always `null`; the stream ends with a `tool_response`. |
| `tool_error` | clawctl → caller | Terminal failure. Maps to a non-zero `clawctl` exit code (see `docs/transport-decisions.md`). |

Every member carries:

- `envelope_version: "1"` — pinned literal. Consumers MUST reject unknown values rather than guessing.
- `agent` — the gateway's published agent slug (validated against `/v1/models` when the cache is reachable).
- `traceparent` — W3C traceparent header. Required so callers can cite a trace-id (design principle 3) instead of dumping bodies.

Optional fields (`session_id`, `task_id`, `tool_choice`) are documented in the schema; refer to the `$defs` blocks for shape and constraints. `session_id` and `task_id` align with A2A's `contextId`/`taskId` — see `docs/agent-protocol.md`. `task_id` was added as an additive v1 field; when a caller omits it, clawctl derives a default from the call's trace-id.

## Versioning policy

The envelope follows a **strict additive policy within a major version**. Breaking changes bump the file name and the `envelope_version` literal in lock-step.

### What is allowed within v1 (additive)

A change is in-scope for `envelope.v1.json` if and only if a v1 consumer that ignores unknown fields keeps working unchanged after the update:

- Adding a new optional field to any member.
- Adding a new value to an enum **iff** the enum is documented as open-ended (currently: `Redaction.kind` is closed; `ErrorCode` is closed; `FinishReason` mirrors OpenAI's vocabulary and is closed). For a closed enum, the additive path is to bump.
- Adding a new `$def` that is referenced only by new optional fields.
- Tightening a description, example, or comment — semantics unchanged.
- Relaxing a constraint that was over-specified by mistake (e.g. lowering `minLength` from 2 to 1), provided no existing v1 producer was relying on the rejection.

### What forces a v2 bump (breaking)

- Removing a field, renaming a field, or making an optional field required.
- Removing a value from a closed enum, or adding one to a closed enum.
- Changing a field's type (`string` → `object`, `integer` → `string`, etc.).
- Changing the `kind` discriminator vocabulary.
- Changing the meaning of `envelope_version: "1"` retroactively (e.g. moving a field's semantics).

When a bump is required:

1. Copy `schemas/envelope.v1.json` to `schemas/envelope.v2.json` and edit there.
2. Set `envelope_version` to the const `"2"` in every member.
3. Update `$id` to `https://clawctl.dev/schemas/envelope.v2.json`.
4. Add a "v1 → v2 migration" section to this file naming each removed/renamed field and the v2 replacement.
5. Keep `envelope.v1.json` in the tree until every emitter and consumer has migrated; do not delete it in the bump PR.
6. Update `internal/envelope` (Go) and the bash `--envelope` flag to emit v2 by default; provide an explicit opt-in to v1 if any in-flight callers still need it.

### Consumer obligations

A v1 consumer MUST:

- Reject any document whose `envelope_version` is not `"1"`. Treating `"2"` as `"1"` would silently corrupt streams once v2 ships.
- Ignore unknown fields. The schema's `additionalProperties: false` is for emitter discipline; consumers should be tolerant in case a future v1 (additive) change adds a field they don't yet know about.
- Treat the `kind` discriminator as authoritative. Do not infer the member type from which fields happen to be present.

### Validating a document

The schema is JSON Schema 2020-12. Any compliant validator works; the project uses `ajv-cli` for ad-hoc checks:

```bash
npx --yes ajv-cli compile -s schemas/envelope.v1.json --spec=draft2020
npx --yes ajv-cli validate -s schemas/envelope.v1.json -d path/to/fixture.json --spec=draft2020
```

The Go binary self-validates via `internal/envelope.Validate` (US-012); no separate validator install is needed for runtime callers.

## Why a single schema with `oneOf` (not four files)

A single file with four `$defs` keeps the discriminator (`kind`) and shared types (`AgentSlug`, `Traceparent`, `Redaction`, `ErrorCode`) in one place. Splitting across four files would force every consumer to load four documents and re-derive the discriminator. The `oneOf` at the top level lets a consumer hand a stranger payload to the validator without first knowing which member it is.
