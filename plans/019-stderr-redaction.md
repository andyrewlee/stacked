# Plan 019: Redact credential-shaped strings from git stderr folded into errors

> **Executor instructions**: Follow this plan step by step. Run every
> verification command and confirm the expected result before moving to the
> next step. If anything in the "STOP conditions" section occurs, stop and
> report — do not improvise. When done, update your row in `plans/README.md`.
>
> **Drift check (run first)**: `git diff --stat 159648a..HEAD -- internal/git/git.go internal/git/refs.go internal/git/debug.go`
> On mismatch, STOP.

## Status

- **Priority**: P3
- **Effort**: S
- **Risk**: LOW (error-message content only; worst case an over-redacted message — still actionable)
- **Depends on**: none
- **Category**: security (hardening — the payload is unproven, the asymmetry is real)
- **Planned at**: commit `159648a`, 2026-10-02

## Why this matters

`internal/git/git.go:42-54` (and `refs.go:426-430,549-557`) fold captured git stderr verbatim into wrapped errors; those errors surface at `cmd/root.go` into `--json` envelopes (`{"error":{"message":…}}`) consumed by agents and logged into transcripts. Meanwhile `internal/git/debug.go:62-85` already treats credential-shaped argv as sensitive (`redactURLArg` masks `://`-URLs and `user:pass@host`) — argv gets redacted, *output* doesn't. Modern git anonymizes common transport messages, but coverage is version- and path-dependent, and remote-side `remote:` hook text is relayed unfiltered. This is defense-in-depth: don't trust the child to anonymize.

## Current state

- `internal/git/git.go:42-54` — spawn-failure path builds `fmt.Errorf("git %s: %s: %w", args, msg, err)` from raw stderr/stdout.
- `internal/git/refs.go:426-430` and `:549-557` — same pattern for ref commands and `update-ref --stdin`.
- `internal/git/debug.go:62-85` — `redactURLArg(arg)` — masks `"://"`-containing args, scp-like `user@host:path`, and `user:pass@host` userinfo. The shape to reuse.
- `cmd/root.go:436-453` — text mode sanitizes for terminal escapes (`sanitizeErrorForTerminal`); `--json` emits `err.Error()` raw (JSON escaping protects terminals, not content).
- Credential-bearing remote URLs (`https://user:token@host/repo`) are a real-world pattern; `redactURLArg`'s masks are proven.

## Commands you will need

| Purpose | Command | Expected on success |
|---------|---------|---------------------|
| Package tests | `go test ./internal/git` | all pass |
| Envelope tests | `go test ./cmd -run 'Envelope|Error|JSON'` | all pass |
| Full gate | `make ci` | exit 0 |

## Scope

**In scope**: `internal/git/git.go`, `internal/git/refs.go`, `internal/git/debug.go` (add a `redactCredentials(text)` sibling to `redactURLArg`), `internal/git/*_test.go`.

**Out of scope**: `cmd/` error envelope shape, `sanitizeForTerminal`/`sanitizeErrorForTerminal` (terminal-escape path is separate — keep it), fetch/push success-path output (`remote:` lines in normal output — consider only if trivially in scope).

## Git workflow

- Branch: `advisor/019-stderr-redaction`; imperative commit; no push/PR unless instructed.

## Steps

### Step 1: Add `redactCredentials(text) string` next to `redactURLArg`

In `internal/git/debug.go`: a text-level scrubber that finds credential userinfo inside arbitrary text (unlike `redactURLArg`, which masks a whole argv token):

- `scheme://userinfo@host` → `scheme://<redacted>@host` (mask the userinfo segment, keep host for actionability). Regex-free is fine: find `://`, then `@` before the next `/`, and replace that span.
- `user:pass@host:` scp-like prefix → `<user>@host:` → mask the `user:pass` part when it's followed by a `:` before any `/`.

Keep it small — the goal is catching `https://oauth2:TOKEN@host/…` and `git@host:…` shapes embedded in error text, not building a URL parser.

### Step 2: Apply at the three error-wrap points

In `internal/git/git.go:47-53` and `refs.go:426-430,549-557`: run the captured `msg` through `redactCredentials` before building the error:

```go
if msg != "" {
    return stdout.String(), fmt.Errorf("git %s: %s: %w", strings.Join(args, " "), redactCredentials(msg), err)
}
```

The `args` are already redacted in `ST_DEBUG` traces but NOT in error messages — consider applying `redactURLArg` per-arg in the error's `strings.Join` too if args can carry URLs (`git remote get-url` failures echo the name, not the URL — check call sites; if any spawn passes a URL as an arg, redact it there).

**Verify**: `go build ./internal/git` → exit 0.

### Step 3: Test

In `internal/git/git_test.go` (or a new `debug_test.go`): drive `runWith`/`Run` against a command that fails with a credential-bearing message — easiest: a test that calls the error-wrap path with a synthetic `msg` via a small seam, OR a real git command guaranteed to fail (`git -C /nonexistent status` won't echo a URL; better: unit-test `redactCredentials` directly with cases like `fatal: unable to access 'https://oauth2:SECRET@h/r/': …` → assert `SECRET` absent from output, `h`/`r` retained).

Test the wrap functions via a PATH-shim `git` that exits 1 with a credential-bearing stderr (the repo has a PATH-shim precedent in `cmd/open_test.go`).

**Verify**: `go test ./internal/git` → pass including the new cases.

### Step 4: Gate

**Verify**: `make ci` → exit 0.

## Test plan

- Unit: `redactCredentials` table — URL userinfo, scp-like, plain text (unchanged), multiline, edge cases (no `@`, `@` after `/`, empty userinfo).
- Integration: a PATH-shim `git` failure → wrapped error contains no token.
- Verification: `go test ./internal/git` → all pass; `make ci` → green.

## Done criteria

- [ ] `go build ./...` exits 0
- [ ] `go test ./internal/git` exits 0 with the new table/shim tests
- [ ] `grep -n 'redactCredentials' internal/git/git.go internal/git/refs.go` shows the wrap points
- [ ] A wrapped error containing `https://user:TOKEN@host` prints no `TOKEN`
- [ ] `make ci` exits 0
- [ ] `plans/README.md` row updated

## STOP conditions

- A scrub pattern false-positives on a real error message that tests pin — keep the mask minimal (only `://...@` and `user:pass@host:` shapes), don't over-match `user@host` without a colon (that's a legit ssh-style reference).
- The wrap points have more call sites than the three listed (grep `fmt.Errorf("git %s`/`git %s: %s`) — cover all or STOP and list them.
- `cmd`'s envelope path turns out to need content-level redaction beyond what the git layer provides — that's a separate decision; keep this at the git layer.

## Maintenance notes

- The asymmetry this closes: `ST_DEBUG` already redacts argv (`debug.go`) — this extends the same principle to captured output. If a future flag echoes URLs in argv to the error message, apply `redactURLArg` there too.
- `remote:` server-side lines (push/fetch output embedded in errors) get the same scrub — that's the "unvetted content channel into agent JSON" arm.
- Deferred: a broader output sanitizer if agents start parsing `remote:` progress text — not needed today.
