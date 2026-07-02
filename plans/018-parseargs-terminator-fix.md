# Plan 018: Make a non-leading `--` terminator behave per the documented contract in parseArgs

> **Executor instructions**: Follow this plan step by step. Run every
> verification command and confirm the expected result before moving to the
> next step. If anything in the "STOP conditions" section occurs, stop and
> report — do not improvise. When done, update the status row for this plan
> in `plans/README.md` — unless a reviewer dispatched you and told you they
> maintain the index.
>
> **Drift check (run first)**: `git diff --stat 0a76742..HEAD -- cmd/util.go cmd/util_test.go`
> If either file changed since this plan was written, compare the
> "Current state" excerpts against the live code before proceeding; on a
> mismatch, treat it as a STOP condition.

## Status

- **Priority**: P1
- **Effort**: S
- **Risk**: LOW
- **Depends on**: none
- **Category**: bug
- **Planned at**: commit `0a76742`, 2026-07-01

## Why this matters

`parseArgs` documents (cmd/util.go:337-338): "A bare `--` terminates flag
parsing: it and everything after it is treated as positional, in order." That
holds when `--` is the *first* token, but when `--` appears *after* a
positional has already been collected, the literal `--` token itself leaks
into the positional list. `st create feat --` then fails with "create requires
exactly one branch name" (it sees 2 positionals), and any command tolerant of
extra positionals receives a stray `--` argument. Agents driving `st`
programmatically are the likeliest to append a defensive `--`, so this
contradicts the machine-interface promise. The fix is a one-line change plus
table-test rows.

## Current state

- `cmd/util.go` — `parseArgs` (lines 339-372) reshuffles flags around
  positionals before calling `fs.Parse`. The bug is in the terminator branch:

```go
// cmd/util.go:341-346
for i := 0; i < len(args); i++ {
    a := args[i]
    if a == "--" {
        positional = append(positional, args[i:]...)   // <-- appends the "--" token itself
        break
    }
```

- Later (lines 366-370) a synthetic `--` is prepended so flag-like positionals
  are shielded from `fs.Parse`:

```go
// cmd/util.go:366-370
combined := append([]string(nil), flags...)
if len(positional) > 0 && positional[0] != "--" {
    combined = append(combined, "--")
}
combined = append(combined, positional...)
```

- Failure trace for `st create feat --`: `positional=["feat","--"]` →
  `positional[0]` is `"feat"`, so a synthetic `--` is prepended →
  `combined=["--","feat","--"]` → stdlib `fs.Parse` strips only the *first*
  `--` → `fs.Args() == ["feat","--"]` (len 2) → `runCreate` rejects with
  "create requires exactly one branch name".
- The leading case `st create -- feat` works today only because the
  `positional[0] != "--"` guard skips the synthetic terminator, letting
  `fs.Parse` strip the original one.
- `cmd/util_test.go` — `TestParseArgs` (lines 13-79) is the table test; its
  only terminator row is the leading case (line 46:
  `{"double dash escapes a flag-like positional", []string{"--", "-m"}, ...}`).
  No row exercises a trailing or embedded `--`.

Repo conventions: table-driven tests with `name/in/want/wantArgs/wantErr`
rows — extend `TestParseArgs`'s existing table in place; match its style
exactly. Comments state constraints, not narration.

## Commands you will need

| Purpose | Command | Expected on success |
|---|---|---|
| Focused test | `go test ./cmd -run TestParseArgs -count=1` | ok, all subtests pass |
| Package tests | `go test ./cmd -count=1` | ok (takes ~1 min) |
| Full gate | `make ci` | exit 0 (needs golangci-lint v2 on PATH; see STOP conditions) |

## Scope

**In scope** (the only files you should modify):
- `cmd/util.go` (the `a == "--"` branch of `parseArgs`, and its doc comment)
- `cmd/util_test.go` (new rows in `TestParseArgs`'s table)

**Out of scope** (do NOT touch, even though they look related):
- `parseFlagSet`, `parseCount`, `parseBuiltinArgs`, `looksNumeric`,
  `isBoolFlag` — all behave correctly; `parseBuiltinArgs` has its own
  terminator handling with a passing test (util_test.go:145).
- Any command's `run*` function — the fix is entirely inside `parseArgs`.
- `docs/AGENT.md` / `README.md` — their descriptions of `--` are already
  correct; the code is what's wrong.

## Git workflow

- Branch: `advisor/018-parseargs-terminator-fix`
- Commit style: conventional commits, e.g. `fix: keep a non-leading -- terminator out of the positional args`
  (match `git log --oneline -10` style: `fix:`/`refactor:`/`test:` prefixes)
- Do NOT push or open a PR unless the operator instructed it.

## Steps

### Step 1: Drop the terminator token from the collected positionals

In `cmd/util.go`, change the terminator branch to append everything *after*
the `--`, not from it:

```go
if a == "--" {
    positional = append(positional, args[i+1:]...)
    break
}
```

With that change `positional` can never contain `--`, so the
`positional[0] != "--"` guard at line 367 becomes dead. Simplify it to:

```go
if len(positional) > 0 {
    combined = append(combined, "--")
}
```

Update the function's doc comment sentence "it and everything after it is
treated as positional" to "everything after it is treated as positional" (the
token itself is consumed as the terminator).

**Verify**: `go test ./cmd -run TestParseArgs -count=1` → ok (all existing
rows still pass, including "double dash escapes a flag-like positional" and
"negative number is positional").

### Step 2: Pin the fixed behavior with new table rows

Add these rows to `TestParseArgs`'s table in `cmd/util_test.go`, matching the
existing row style:

```go
{"trailing terminator is consumed", []string{"feat", "--"}, result{}, []string{"feat"}, ""},
{"terminator after positional escapes later flag", []string{"feat", "--", "-m"}, result{}, []string{"feat", "-m"}, ""},
{"terminator only", []string{"--"}, result{}, nil, ""},
{"flag then terminator then flag-like positional", []string{"-a", "--", "-m"}, result{all: true}, []string{"-m"}, ""},
```

**Verify**: `go test ./cmd -run TestParseArgs -count=1 -v` → all subtests
including the 4 new names pass.

### Step 3: Run the package and full gate

**Verify**: `go test ./cmd -count=1` → ok.
**Verify**: `make ci` → exit 0. (No golden files change — this alters arg
parsing, not any command's output for previously-valid invocations.)

## Test plan

- New tests: the 4 table rows in Step 2, in `cmd/util_test.go`, covering:
  trailing `--`, embedded `--` shielding a flag-like token, bare `--`, and
  flags-before-terminator.
- Pattern: the existing `TestParseArgs` table (cmd/util_test.go:32-51).
- Verification: `go test ./cmd -run TestParseArgs -count=1` → all pass.

## Done criteria

Machine-checkable. ALL must hold:

- [ ] `go test ./cmd -run TestParseArgs -count=1` exits 0 with the 4 new rows present
- [ ] `go test ./cmd -count=1` exits 0
- [ ] `make ci` exits 0
- [ ] `grep -n 'args\[i:\]' cmd/util.go` returns no match inside the `a == "--"` branch
- [ ] Only `cmd/util.go` and `cmd/util_test.go` are modified (`git status`)
- [ ] `plans/README.md` status row updated

## STOP conditions

Stop and report back (do not improvise) if:

- The `parseArgs` code at cmd/util.go:339-372 does not match the excerpts
  above (drifted).
- Any *existing* `TestParseArgs` row fails after Step 1 — that means the
  simplification changed a behavior this plan didn't predict.
- `make ci` fails on the lint step because golangci-lint is missing or v1:
  run `PATH=/opt/homebrew/bin:$PATH make ci` (the machine's default resolves
  to v1; the repo requires v2). If it still fails, report.
- The fix appears to require touching any file outside the in-scope list.

## Maintenance notes

- `parseBuiltinArgs` (help/version) implements its own `--` handling — if the
  two ever merge, the new table rows here are the contract to preserve.
- Reviewer should scrutinize: the simplified guard (`len(positional) > 0`)
  now always prepends a synthetic `--` when positionals exist — confirm no
  command relies on receiving a literal `--` in `fs.Args()` (none does today;
  `rejectArgs`/count-checking callers would have been *more* broken by it).
