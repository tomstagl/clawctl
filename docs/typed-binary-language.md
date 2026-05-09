# Typed binary: Go vs Rust

The bash MVP (`clawctl`) has carried us through the design-principles work, but every subcommand from US-011 onward needs a typed runtime so the envelope contract in `schemas/envelope.v1.json` is enforced at compile time, not at the next `_redact` regex tweak. This document records the language pick for that runtime and the criterion that decided it.

The decision is **Go**. The criterion that broke the tie is **MCP SDK maturity** (see below); every other criterion was either neutral or a slight Go preference, but none of them were load-bearing on their own.

## Criteria

The bar is "what does the typed binary need to do in the first six months", not "which language is theoretically better". The relevant axes are:

1. **MCP SDK maturity** — the binary's reason for existing is `clawctl mcp` (US-025..US-027). Whichever language has a maintained, spec-current MCP server SDK that we don't have to fork wins by default.
2. **Single-file static binary size** — the install path is `curl | sh` to `/usr/local/bin/clawctl` (US-030). Anything that pushes us past ~30 MB per arch starts to feel rude on slow links and bloats the four-arch release matrix (US-029).
3. **Cross-compile to darwin/linux (amd64 + arm64)** — the four release artifacts in US-029 must build from a single CI runner without per-target toolchains, sysroots, or QEMU.
4. **HTTP + SSE client ergonomics** — `clawctl msg` is a normal POST; `clawctl stream` is SSE with boundary-safe parsing (US-016). The standard library should cover both without a third dep.
5. **SSH client library quality** — `clawctl cli` (US-020/021) needs ControlMaster-aware ssh, not a reimplementation of the protocol. We almost certainly shell out to `/usr/bin/ssh` either way; the question is whether the language's stdlib makes that ergonomic.
6. **Team familiarity** — the bash MVP exists; the typed binary is a rewrite. Onboarding cost matters because we expect future contributors to read and patch this code, not just bless a black box.

## Comparison

| Criterion | Go | Rust | Edge |
| --- | --- | --- | --- |
| MCP SDK maturity | `github.com/modelcontextprotocol/go-sdk` is the official SDK, version-aligned with the spec, and is what Anthropic's own examples target. Tools/list, tools/call, and progress notifications are first-class. | `rmcp` (community) and a handful of forks; spec coverage trails the Go SDK by one or two minor versions, and progress notifications were not stable last time we checked. No "official" badge. | **Go** |
| Static binary size | `CGO_ENABLED=0 go build -ldflags '-s -w'` produces ~10–14 MB for a CLI of this shape. Acceptable for `curl \| sh`. | `cargo build --release` with `panic="abort"` and `strip` lands ~3–6 MB. Smaller, but the four-arch matrix size delta (~30 MB total saved) is below the noise floor of a single release tarball. | Rust (small) |
| Cross-compile darwin+linux × amd64+arm64 | One CI runner. `GOOS=darwin GOARCH=arm64 go build` Just Works; no toolchain install, no linker drama. | Possible but painful: darwin-from-linux needs `osxcross` or a macOS runner; cross-arch needs `cross` plus per-target sysroots. CI complexity goes up, and the fix-it loop on a broken release tag is worse. | **Go** |
| HTTP + SSE ergonomics | `net/http` for the POST; SSE is parse-by-line over `bufio.Scanner` against `resp.Body`. No deps. The boundary-split fuzz target in US-016 maps cleanly to `testing.F.Fuzz`. | `reqwest` for HTTP (one dep); `eventsource-stream` or hand-rolled for SSE. Workable. Cargo fuzz exists. | Slight Go (zero deps) |
| SSH client library | We shell out to `/usr/bin/ssh` either way (ControlMaster lives in the user's `~/.ssh/config`; reimplementing it is a US-004 anti-goal). `os/exec` with argv-as-slice is exactly what US-020 requires. `golang.org/x/crypto/ssh` exists if we ever want to drop the binary dep, which we do not. | `std::process::Command` covers the same ground; `russh` exists if we ever wanted to drop the binary dep. Same shape, no advantage. | Tie |
| Team familiarity | The maintainers and most expected contributors read Go fluently; `clawctl`'s reviewers have shipped Go services in production. The bash MVP's idioms (`_redact`, `_chat`, `_traceparent`) translate one-to-one to Go packages. | A subset of contributors read Rust; fewer have written async Rust at production volume. Onboarding cost for a future contributor is real. | **Go** |

## Decision

**Go.** The tie-breaker is MCP SDK maturity: the binary's headline feature (`clawctl mcp`) is built on top of an SDK that exists, ships, and tracks the spec in Go, and exists only as a community catch-up effort in Rust. Picking Rust means owning some of that catch-up — out of scope for a project whose whole point is to wrap an existing gateway, not to maintain a protocol implementation.

The smaller-binary advantage that Rust holds is real but not load-bearing: a 10 MB Go binary still fits the `curl | sh` path comfortably, and the four-arch release matrix (US-029) is dominated by CI minutes and SHA256 publication, not artifact bytes.

The cross-compile and team-familiarity edges reinforce the call but were not load-bearing in isolation. Either could have been engineered around (`cross` plus a macOS runner; a Rust onboarding doc) if the MCP SDK situation had been reversed.

## What this commits us to

- `go.mod` at repo root, Go 1.22+ toolchain (US-011).
- All typed-binary code under `cmd/clawctl/` and `internal/...`, matching standard Go layout.
- `github.com/modelcontextprotocol/go-sdk` as the MCP server dependency for US-025..US-027.
- `CGO_ENABLED=0` for releases so the four-arch matrix stays single-runner (US-029).
- `os/exec` (not a Go SSH client) for `clawctl cli` so ControlMaster keeps doing the connection-reuse work the user's `~/.ssh/config` already configures (US-020).

## What this does not commit us to

- Rewriting the bash MVP in one go. The transition plan (US-031) keeps `clawctl.bash` for one release cycle.
- Using Go for unrelated tooling. `ralph.sh` and the install scripts stay bash; this decision is scoped to the typed binary only.
- Forever. If the Rust MCP SDK reaches parity and the binary-size argument starts mattering (e.g. a future embedded target), the criteria above are reusable for a re-evaluation. Bring receipts.
