# Agent protocol position

This document records where `clawctl` sits relative to the emerging
agent-communication standards, and the deliberate choice to **align with A2A
incrementally** rather than adopt it wholesale. It is the anchor for protocol
decisions the way `docs/transport-decisions.md` anchors transport choices: a new
field or surface that touches agent communication should be justified here
first.

## The three surfaces

`clawctl` participates in three distinct conversations. Only one is truly
"agent-to-agent", and each maps to a different standard:

| Surface | Direction | Protocol today | Standard it maps to |
| --- | --- | --- | --- |
| Tooling | Claude Code → `clawctl mcp` | **MCP** (JSON-RPC over stdio) | MCP — already the standard; keep |
| Inference | `clawctl` → openclaw gateway | OpenAI-compatible Chat Completions (`/v1/*`) + the v1 envelope | A2A concepts, mapped below |
| Delivery | openclaw agents → repo/humans | GitHub loop-back (`skills/openclaw-loopback`) | a convention, not a wire protocol |

MCP (Anthropic) and A2A (Agent2Agent — Google, donated to the Linux Foundation
in 2025) are **complementary**: MCP is how an agent reaches tools and data; A2A
is how agents talk to each other. `clawctl` is an MCP *server* to Claude Code and
an A2A-aligned *client* to the gateway.

## Why incremental alignment, not full A2A

A native A2A surface (an `/.well-known/agent.json` card host, JSON-RPC `tasks`,
an artifact store, a push-callback server) is a large amount of surface area that
conflicts with two load-bearing constraints:

- **Read-only-by-default control plane** (`docs/design-principles.md` §1).
  `clawctl` is a kubectl/dtctl-style CLI, not an autonomous peer agent.
- **One static binary, zero runtime deps** (§5). A callback server and an
  artifact store pull the binary toward being a daemon.

So instead of adopting A2A's transport, the v1 envelope and `/v1/models`
discovery are shaped so the *concepts* line up. A future A2A bridge then becomes
a thin translation layer rather than a re-architecture.

## Concept mapping (A2A ⇄ clawctl)

| A2A concept | clawctl equivalent | Notes |
| --- | --- | --- |
| Agent Card (`/.well-known/agent.json`) | a `/v1/models` entry → `mcpserver.Agent` | id, description, owner, and `capabilities`/`skills` populate a minimal card; missing fields fail open. |
| `contextId` (groups tasks in a conversation) | `session_id` | Opaque, ≤128 chars; passed to the gateway as the `user` field. |
| `taskId` (one unit of work) | `task_id` (envelope) | Additive v1 field. When the caller omits it, `clawctl` derives a stable default from the call's trace-id, so every envelope carries one. |
| `Message` / `Artifact` | `ToolResponse` / `ToolStreamChunk` | Text-only for v1; multimodal artifacts deferred to envelope v2. |
| Streaming (SSE) | `clawctl stream` (SSE → NDJSON chunks) | Same buffering/redaction rules as today. |
| Push notifications | **not implemented** — see seam below | The one concept with no current equivalent. |

`task_id` is additive and optional, so it satisfies the envelope's additive-only
v1 policy (`docs/envelope.md`); existing consumers that ignore it are unaffected.

## Push-notification seam (design only)

A2A lets a server push task updates to a caller-supplied webhook. `clawctl`
deliberately does **not** ship an HTTP callback server: hosting one contradicts
the read-only CLI shape and adds inbound network surface.

When a push capability is genuinely needed, the intended attachment point is the
**SSH side**, not an HTTP callback:

- A future `clawctl cli <subscribe>` opens a long-lived `openclaw` subscription
  over the existing SSH transport (`docs/transport-decisions.md`, `cli` row),
  inheriting the host's auth instead of standing up a second auth plane.
- Task updates would surface as `ToolStreamChunk`/`ToolResponse` envelopes keyed
  by the same `task_id`, so consumers reuse the streaming path they already have.

This keeps the door open without committing the binary to becoming a daemon.

## Out of scope (revisit only if agents become addressable peers)

- A native A2A JSON-RPC server or client transport.
- Hosting `/.well-known/agent.json`.
- An artifact store or multimodal message parts (envelope v2 territory).
