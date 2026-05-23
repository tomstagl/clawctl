# clawctl bug report — 2026-05-23

Found during Phase-3 fleet deployment (registering 5 new openclaw agents + creating their cron jobs from macOS to a remote Linux host).

---

## Bug 1: `clawctl cli agents add --workspace` — tilde expanded locally, breaks on remote Linux

**Severity:** High (silent partial failure — config is written but workspace dir creation fails)

**Repro:**
```bash
clawctl cli agents add my-agent --workspace ~/.openclaw/workspace/my-agent --non-interactive
```

**What happens:**
1. The local macOS shell expands `~` to `/Users/tom` before clawctl receives the argument.
2. clawctl forwards `/Users/tom/.openclaw/workspace/my-agent` to the remote Linux host via SSH.
3. The remote openclaw process writes the workspace path to `~/.openclaw/openclaw.json` — **succeeds**.
4. The remote openclaw then tries to `mkdir /Users/tom/.openclaw/workspace/my-agent` — **fails** with `EACCES: permission denied, mkdir '/Users'`.
5. The agent appears in `agents list` with workspace `Workspace: /Users/tom/.openclaw/workspace/my-agent` — a path that does not exist on the Linux host.

**Effect:** Agent is registered but permanently broken — any run will fail to find its workspace files (SOUL.md, IDENTITY.md, MEMORY.md).

**Workaround:** Use the absolute remote path:
```bash
clawctl cli agents add my-agent --workspace /home/tom/.openclaw/workspace/my-agent --non-interactive
```

**Fix options (pick one):**
- **Option A:** Document clearly that `--workspace` must be an absolute remote path when using `clawctl cli`. Add an error or warning if the path starts with the local home prefix (e.g. `/Users/` when the remote user appears to be on Linux via `$OSTYPE` heuristic).
- **Option B:** In `cli.go`, detect paths that start with the local `os.UserHomeDir()` and rewrite them to use the remote user's home directory (derived from `cfg.SSHHost` username + assumption of `/home/<user>` on Linux). Risky — home dir detection is fragile.
- **Option C (recommended):** Add a validation step: after `agents add`, verify the workspace dir exists on the remote. If it doesn't, print an actionable error: `"workspace path /Users/tom/... was not created on the remote host; re-run with an absolute remote path (e.g. /home/tom/...)"`.

---

## Bug 2: `clawctl cli cron add --cron "0 4 * * 2"` — cron wildcards glob-expanded via SSH

**Severity:** High (command always fails with a confusing error)

**Repro:**
```bash
clawctl cli cron add --agent my-agent --cron "0 4 * * 2" --session isolated --no-deliver --message "test"
```

**What happens:**
SSH joins all arguments into a command string passed to the remote shell. The remote shell sees `* * 2` and glob-expands the `*` wildcards against the current working directory (which is the remote `$HOME`). The resulting extra arguments make `openclaw cron add` fail with:

```
Too many arguments for this command.
Try: openclaw cron add --help
```

This happens even when the cron expression is double-quoted in the local shell, because double-quoting prevents LOCAL expansion but the resulting string (with literal spaces and `*` chars) is sent as a single Go argument to the `ssh` binary, which then joins it with other arguments into a shell command string for the remote — losing the quoting.

**Workaround:** Bypass `clawctl cli` and run via direct SSH heredoc:
```bash
ssh tom@openclaw 'bash -s' << 'EOF'
export PATH="$HOME/.npm-global/bin:$PATH"
openclaw cron add --agent my-agent --cron "0 4 * * 2" ...
EOF
```

**Fix:** In `cli.go`, when constructing the SSH command for `cron add`, each argument must be shell-quoted before being appended to the remote command string. The `exec.CommandContext` call passes args to SSH as-is, but SSH joins them without quoting when building the remote command. 

Use `shellescape` (or equivalent) to quote each argument in `sshArgs` that will be forwarded to the remote shell:
```go
// Before appending to sshArgs, shell-quote each arg:
import "github.com/alessio/shellescape"
for _, arg := range args {
    sshArgs = append(sshArgs, shellescape.Quote(arg))
}
```

Alternatively, wrap the entire remote command in `bash -c '...'` with all arguments pre-escaped.

**Scope:** This bug affects any `clawctl cli` subcommand whose arguments contain shell metacharacters (`*`, `?`, `[`, `]`, `$`, backticks, etc.). The cron expression is just the most common case.

---

## Bug 3: `clawctl cli cron add --message <multiline>` — newlines in message argument cause remote shell word-splitting

**Severity:** Medium (reproducible; easy workaround)

**Repro:**
```bash
MSG="line one
line two"
clawctl cli cron add --agent my-agent --cron "0 4 2 5 1" --message "$MSG"
```

**What happens:** Same root cause as Bug 2. SSH joins the arguments into a shell command string; newlines in the `--message` value cause the remote shell to treat each line as a separate command or argument.

**Workaround:** Same as Bug 2 — direct SSH heredoc.

**Fix:** Same as Bug 2 — proper shell-quoting of all forwarded arguments in `cli.go`.

---

## Root cause summary

All three bugs trace to the same underlying issue in `cli.go`:

```go
sshArgs = append(sshArgs, args...)
```

The `args` slice is forwarded to `exec.CommandContext(ctx, "ssh", sshArgs...)` without shell-quoting. OpenSSH, when passed a remote command as multiple arguments, joins them with spaces and executes the joined string via the remote shell (`/bin/sh -c "<joined>"`). Arguments containing shell metacharacters (`~`, `*`, `?`, newlines, spaces) are word-split or glob-expanded by the remote shell.

**Minimal fix in `cli.go`:**
```go
import "github.com/alessio/shellescape"

// Replace:
sshArgs = append(sshArgs, args...)

// With:
for _, arg := range args {
    sshArgs = append(sshArgs, shellescape.Quote(arg))
}
```

This ensures every argument forwarded to the remote shell is quoted and arrives verbatim at the openclaw process.

**Test cases to add to `cli-hardening.sh`:**
```bash
# Cron expression with wildcards
clawctl cli cron add --agent test --cron "0 4 * * 2" --message "test" --session isolated --no-deliver
# Multiline message
clawctl cli cron add --agent test --cron "0 4 2 5 1" --message $'line1\nline2' --session isolated --no-deliver
# Workspace with tilde (should warn or error)
clawctl cli agents add test-agent --workspace ~/.openclaw/workspace/test --non-interactive
```
