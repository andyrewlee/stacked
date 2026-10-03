# Plan 015: Replace git-error-substring classification with state probes in `Undo` and cross-worktree rollback

> **Executor instructions**: Follow this plan step by step. Run every
> verification command and confirm the expected result before moving to the
> next step. If anything in the "STOP conditions" section occurs, stop and
> report — do not improvise. When done, update your row in `plans/README.md`.
>
> **Drift check (run first)**: `git diff --stat 159648a..HEAD -- internal/stack/undo_op.go internal/stack/worktree.go`
> On mismatch — especially if Plan 001/003 changed `undo_op.go` — STOP.

## Status

- **Priority**: P3
- **Effort**: S–M
- **Risk**: MED (the substring checks drive real decisions; the probe replacements must preserve swallow-vs-propagate semantics exactly)
- **Depends on**: plans/001-undo-ref-guards.md, plans/003-undo-order-cache.md (same file — land after, or accept a rebase)
- **Category**: tech-debt / correctness-adjacent
- **Planned at**: commit `159648a`, 2026-10-02

## Why this matters

Two spots classify git failures by substring-matching its error text:

- `internal/stack/undo_op.go:259-268` — `checkoutBlockedByLocalChanges`/`checkoutBlockedByOtherWorktree` match "local changes", "would be overwritten", "already checked out", "used by worktree", "checked out at" (the triple-synonym list is itself evidence the wording varies across git versions). These decide whether `Undo` detaches HEAD and continues vs. hard-fails.
- `internal/stack/worktree.go:259-261` — `noRebaseInProgress` matches "no rebase in progress" inside `RebaseAbortIn` errors to decide whether rollback succeeded.

`LC_ALL=C` pins the locale but not the phrasing across git releases — a git rewording silently flips the fallback. The codebase already knows this is brittle: `internal/git/remote.go:26-27` deliberately refuses to parse git output for a fast-forward decision. This applies the principle consistently.

## Current state

- `internal/stack/undo_op.go:96-110` — `Checkout(target)` failure is classified via the two substring helpers; on "blocked" the code parks HEAD detached (`RevParse("HEAD")` + `CheckoutDetach`).
- `internal/stack/undo_op.go:258-268` — the two classifier functions (verbatim substring lists).
- `internal/stack/worktree.go:259-261` — `noRebaseInProgress` substring check inside the abort path.
- Available plumbing: `LinkedOwnerOf`/`OwnerOf` (`worktree.go:125-158`), `IsClean`/`IsCleanIn`, `RebaseInProgress`/`RebaseInProgressIn` — the state probes that answer the same questions without parsing text.
- Convention: engine pure over `Env`; the fakeGit models checkout failures via `checkoutErr` and can model the "blocked" conditions — check `fakegit_test.go` for existing knobs.

## Commands you will need

| Purpose | Command | Expected on success |
|---------|---------|---------------------|
| Fast tests | `make test-fast` | exit 0 |
| Undo tests | `go test ./internal/stack -run 'Undo|Checkout'` | all pass |
| Worktree tests | `go test ./internal/stack -run 'Worktree|Restack'` | all pass |
| Full gate | `make ci` | exit 0 |

## Scope

**In scope**: `internal/stack/undo_op.go`, `internal/stack/worktree.go`, `internal/stack/fakegit_test.go` (if the fake can't express the probeable states), the relevant `*_test.go` files.

**Out of scope**: `internal/git` (the port is fine — it's the classifiers' inputs that change), the other `hasControlOrSpace`-style validation (that's input vetting, not decision-driving), `cmd/` error rendering.

## Git workflow

- Branch: `advisor/015-error-substring-probes`; imperative commit; no push/PR unless instructed.

## Steps

### Step 1: Replace the checkout-blocked classifiers in `Undo`

Where `Checkout(target)` fails at `undo_op.go:96`, instead of substring-matching the error, probe the state: is `target` owned by another worktree (`LinkedOwnerOf(wts, target)`)? Is the current worktree dirty (`IsClean`)? Either answer substitutes for the substring check — the code then takes the detach path only when a *state* probe says the checkout was blocked for a known reason, and propagates otherwise. Fallback: if probes are inconclusive but the error occurred, keep the current substring check as a LAST resort (defense-in-depth — a git version rewording shouldn't strand undo entirely). The probe-first ordering is what removes the git-version dependency.

**Verify**: `go test ./internal/stack -run 'Undo'` → existing tests (e.g. blocked-checkout tests at `undo_op_test.go:179/208/324`) pass on the probe path, not the substring path.

### Step 2: Replace `noRebaseInProgress`'s string match with a state probe

In `restackInWorktree`'s rollback path (`worktree.go` — where `RebaseAbortIn` errors get classified): instead of matching "no rebase in progress" text, confirm success by calling `RebaseInProgressIn(owner.Path)` — if it returns false, the rollback achieved its goal regardless of the error's wording. Keep the error text for the message, not the decision.

**Verify**: `go test ./internal/stack -run 'Worktree'` → pass.

### Step 3: Fake support + edge tests

Ensure `fakeGit` can express "checkout fails AND IsClean=false" / "checkout fails AND other-worktree-owns-target" so the probe path is exercised (not just the substring fallback). Add a test asserting that a checkout failure NEITHER dirty NOR worktree-owned propagates as a hard error (the case the substring fallback used to catch implicitly).

**Verify**: `go test ./internal/stack` → all pass.

### Step 4: Comments + gate

Update the comments at both sites to document the probe-first policy ("probe state; error text is message-only, matching `internal/git`'s no-output-parsing rule"). Run `make ci`.

**Verify**: `make ci` → exit 0.

## Test plan

- Existing blocked-checkout tests at `internal/stack/undo_op_test.go:179/208/324` — must pass through the probe path.
- New: one test where the fake's `checkoutErr` fires with NEITHER dirty NOR worktree-owned state → hard error propagates (no detach).
- New: `RebaseInProgressIn`-driven rollback-confirmation test in the worktree path.
- Verification: `go test ./internal/stack ./cmd` → all pass; `make ci` → green.

## Done criteria

- [ ] `go build ./...` exits 0
- [ ] `go test ./internal/stack ./cmd ./e2e` exits 0
- [ ] `grep -n 'strings.Contains.*local changes\|strings.Contains.*would be overwritten\|strings.Contains.*checked out' internal/stack/undo_op.go` → gone or relegated to last-resort fallback with a comment
- [ ] `grep -n 'no rebase in progress' internal/stack/worktree.go` → gone or message-only
- [ ] `make ci` exits 0
- [ ] `plans/README.md` row updated

## STOP conditions

- A blocked checkout is NOT distinguishable by the available probes (e.g. a "checkout failed" class with no state signature) — keep the substring check for that class and document it; don't force a probe that lies.
- The fake can't model the probeable state without growing new fields — add minimal knobs; if that balloons, STOP.
- Semantics change: a case that used to swallow-and-detach now propagates (or vice versa) — that's a behavioral change; STOP and report rather than silently shifting.

## Maintenance notes

- The rule to write down (CLAUDE.md or a comment): "decisions are driven by probes; git's stderr is for humans, never for control flow" — this plan closes the two violations but the rule is what prevents a third.
- `internal/git/remote.go:26-27` is the existing exemplar — cite it in comments.
- If a future port method returns structured error kinds (e.g. an `ErrWorktreeOwned` sentinel), these sites simplify further — note as a possible follow-up.
