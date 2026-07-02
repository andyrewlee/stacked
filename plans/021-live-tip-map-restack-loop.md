# Plan 021: Maintain a live tip map through the restack loop so up-to-date branches cost zero git spawns

> **Executor instructions**: Follow this plan step by step. Run every
> verification command and confirm the expected result before moving to the
> next step. If anything in the "STOP conditions" section occurs, stop and
> report — do not improvise. When done, update the status row for this plan
> in `plans/README.md` — unless a reviewer dispatched you and told you they
> maintain the index.
>
> **Drift check (run first)**: `git diff --stat 0a76742..HEAD -- internal/stack/restack.go internal/stack/engine.go`
> Plan 020 also edits `restack.go` (preview paths) — that drift is EXPECTED if
> 020 ran first; re-read the live `restack.go` and proceed if the
> `RestackBranch`/`RestackUpstack` bodies still match the excerpts below.
> Any other mismatch is a STOP.

## Status

- **Priority**: P2
- **Effort**: M
- **Risk**: MED (mutation path; conflict/cross-worktree semantics must not move)
- **Depends on**: best after plans/020 (same file; run sequentially)
- **Category**: perf
- **Planned at**: commit `0a76742`, 2026-07-01

## Why this matters

`RestackBranch` spawns `git rev-parse` for the parent tip of **every** branch
it visits — *before* discovering the branch is already up to date. Since
`RestackUpstack` visits every descendant, and it underlies `st restack`,
`st sync` (via `RestackAll`), `st continue`, `st delete`, and the
modify/fold/squash/onto epilogue, an all-up-to-date `st restack` from trunk
over N branches burns N subprocess spawns (~10-50ms each) to conclude
"everything up to date". One `Tips()` read (a single `git for-each-ref`) can
answer the up-to-date question for the whole forest; only branches that
actually rebase then need any further git traffic. `st sync` on a clean
20-branch stack drops ~20 spawns.

This is deliberately the *risky* half of the batching work (the read-only
previews were plan 020): the current per-step live reads exist because a
rebase moves tips mid-loop. The fix must keep the map live — seed it once,
then refresh the entry for each branch actually rebased — not snapshot and
trust.

## Current state

- `internal/stack/restack.go:57-101` — the probe happens before the
  up-to-date check:

```go
func (s *State) RestackBranch(env Env, name string) (bool, error) {
	b, err := s.tracked(name)
	if err != nil {
		return false, err
	}
	parentTip, err := env.Git.RevParse(branchTipRef(b.Parent))   // <- 1 spawn, always
	if err != nil {
		return false, fmt.Errorf("resolve parent %q: %w", b.Parent, err)
	}
	if parentTip == b.ParentSHA {
		return false, nil                                        // <- ...even when up to date
	}

	// Owner-driven cross-worktree restack: if name is checked out in ANOTHER
	// worktree, ... the rebase must run in that worktree.
	if owner, elsewhere, err := s.ownerElsewhere(env.Git, name); err != nil {
		return false, err
	} else if elsewhere {
		return s.restackInWorktree(env, name, b, parentTip, owner)
	}

	start, startErr := env.Git.CurrentBranch()
	if rebaseErr := env.Git.RebaseOnto(parentTip, b.ParentSHA, name); rebaseErr != nil {
		paused, outErr := rebaseFailure(env.Git, rebaseErr, "rebasing", name, b.Parent)
		if paused {
			return false, &ConflictError{Action: "rebasing", Branch: name, Onto: b.Parent}
		}
		if startErr == nil {
			if restoreErr := restoreHEAD(env, start, s.Trunk); restoreErr != nil {
				return false, AlsoFailed(outErr, fmt.Sprintf("restore %q", start), restoreErr)
			}
		}
		return false, outErr
	}
	b.ParentSHA = parentTip
	if err := env.save(); err != nil {
		return false, fmt.Errorf("save state after restacking %q: %w", name, err)
	}
	return true, nil
}
```

- `internal/stack/restack.go:184-196`:

```go
func (s *State) RestackUpstack(env Env, name string) ([]string, error) {
	var rebased []string
	for _, child := range s.Descendants(name) {
		did, err := s.RestackBranch(env, child)
		if err != nil {
			return rebased, err
		}
		if did {
			rebased = append(rebased, child)
		}
	}
	return rebased, nil
}
```

- The design note this plan supersedes (restack.go:103-109, on
  `DriftAgainst`): "Mutation paths keep reading live tips per step —
  correctness during restacks depends on it — this is for the read-only
  consumers (log, validate)." The correctness requirement is real; the
  conclusion (per-step *reads*) is what changes: a maintained live map
  satisfies the same requirement. This comment MUST be updated as part of the
  plan.
- Callers of `RestackUpstack`: `Restack` (engine.go, restacks current branch
  then upstack), `RestackAll` (engine.go:868, = `RestackUpstack(trunk)`,
  used by Sync/Continue), `Delete`'s re-parent loop, `finishUpstack`
  (engine.go:139-150, the fold/squash/onto/modify tail). Callers of
  `RestackBranch` directly: run
  `grep -rn "RestackBranch(" internal/ cmd/` and treat every direct caller as
  keep-behavior-identical.
- Key invariant: after `RestackBranch` rebases `child`, the *child's own tip*
  has moved — and the child's children read *that* tip as their parent tip.
  Today they get it via their own `RevParse`. With a map, the entry for the
  just-rebased branch must be refreshed (one `RevParse` per branch that
  actually rebased — same cost as today for moved branches, zero for clean
  ones).
- Why the refresh needs a read: `RebaseOnto` returns no new tip, and the
  cross-worktree path (`restackInWorktree`) also moves the tip out of view.
  One `RevParse(branchTipRef(child))` after a successful rebase is the
  simplest correct refresh for both paths.
- The `Git` port (internal/stack/git.go) already has
  `Tips() (map[string]string, error)`.
- Model/invariant safety net: `internal/stack/model_test.go` applies
  thousands of random op sequences (including injected conflicts and
  PruneMerged/Track/Untrack) and asserts acyclicity, base-containment,
  restack-reconciles, and idempotence after every step. If this refactor
  breaks tip bookkeeping, the model test is designed to catch it.
  `cascade_test.go` covers the cross-worktree variants; the conflict recovery
  suites cover paused-rebase behavior.

## Commands you will need

| Purpose | Command | Expected on success |
|---|---|---|
| Inner loop | `make test-fast` | ok, ~1s |
| Model test only | `go test ./internal/stack -run Model -count=1` | ok |
| Race suite | `go test ./cmd/... ./internal/... -race -count=1` | ok |
| e2e | `make e2e` | ok |
| Full gate | `make ci` | exit 0 |

## Scope

**In scope** (the only files you should modify):
- `internal/stack/restack.go` (`RestackBranch`, `RestackUpstack`, the
  `DriftAgainst` doc comment, new unexported helper)
- `internal/stack/engine_test.go` and/or `internal/stack/stack_test.go` —
  spawn-count test (reuse the counter added by plan 020 if it landed;
  otherwise add it per that plan's Test plan section)
- `internal/stack/fakegit_test.go` — only if the counter doesn't exist yet

**Out of scope** (do NOT touch):
- `restackInWorktree` / `ownerElsewhere` internals — call them exactly as
  today.
- Conflict classification (`rebaseFailure`) and `ConflictError` semantics.
- `env.save()` checkpoint placement — the per-branch checkpoint is
  load-bearing for `st continue` (a test added in PR #92 asserts it survives
  a midway conflict).
- Preview/plan functions (plan 020's territory) and `NeedsRestack`.
- The engine call sites (`Restack`, `RestackAll`, `finishUpstack`, `Delete`)
  — the change is contained inside `RestackBranch`/`RestackUpstack`; if you
  find yourself editing engine.go beyond nothing, STOP.

## Git workflow

- Branch: `advisor/021-live-tip-map-restack`
- Commit style: conventional commits, e.g.
  `perf: restack from one Tips() read, refreshing tips only for rebased branches`
- Do NOT push or open a PR unless the operator instructed it.

## Steps

### Step 1: Split RestackBranch into probe + act

Extract the body after the probe into an unexported
`restackBranchWith(env Env, name string, b *Branch, parentTip string) (bool, error)`
containing everything from the `parentTip == b.ParentSHA` early-return
onward, unchanged. `RestackBranch` keeps its exact signature and behavior:

```go
func (s *State) RestackBranch(env Env, name string) (bool, error) {
	b, err := s.tracked(name)
	if err != nil {
		return false, err
	}
	parentTip, err := env.Git.RevParse(branchTipRef(b.Parent))
	if err != nil {
		return false, fmt.Errorf("resolve parent %q: %w", b.Parent, err)
	}
	return s.restackBranchWith(env, name, b, parentTip)
}
```

**Verify**: `make test-fast` → ok (pure refactor; identical behavior).

### Step 2: Seed and maintain the map in RestackUpstack

Rewrite `RestackUpstack` to read `Tips()` once and keep it live:

```go
func (s *State) RestackUpstack(env Env, name string) ([]string, error) {
	tips, err := env.Git.Tips()
	if err != nil {
		return nil, fmt.Errorf("read branch tips: %w", err)
	}
	var rebased []string
	for _, child := range s.Descendants(name) {
		b, err := s.tracked(child)
		if err != nil {
			return rebased, err
		}
		parentTip, ok := tips[b.Parent]
		if !ok {
			// Parent missing from the map (e.g. deleted outside st): fall back to
			// the live read so the error text matches RestackBranch's today.
			parentTip, err = env.Git.RevParse(branchTipRef(b.Parent))
			if err != nil {
				return rebased, fmt.Errorf("resolve parent %q: %w", b.Parent, err)
			}
		}
		did, err := s.restackBranchWith(env, child, b, parentTip)
		if err != nil {
			return rebased, err
		}
		if did {
			newTip, err := env.Git.RevParse(branchTipRef(child))
			if err != nil {
				return rebased, fmt.Errorf("resolve %q after restack: %w", child, err)
			}
			tips[child] = newTip
			rebased = append(rebased, child)
		}
	}
	return rebased, nil
}
```

Semantics preserved by construction: children are visited parents-first
(`Descendants` is topological), so when a child's parent was rebased earlier
in this same loop, `tips[parent]` was refreshed at that moment; when the
parent didn't rebase, its map entry (from the initial snapshot) is still its
live tip because nothing else moves refs inside this loop. Conflict and
non-conflict error paths return immediately, discarding the map (identical
to today: state was checkpointed by `restackBranchWith` exactly as before).

**Verify**: `make test-fast` → ok, then
`go test ./internal/stack -run 'Model|Cascade|Conflict' -count=1` → ok.

### Step 3: Update the superseded design comment

Rewrite the `DriftAgainst` comment's mutation-path sentence
(restack.go:103-109) to state the new contract, e.g.: "Mutation paths
maintain a *live* tip map: RestackUpstack seeds it from one Tips() read and
refreshes the entry for each branch it actually rebases — correctness during
restacks depends on that refresh, not on per-step reads."

**Verify**: `grep -n "per step" internal/stack/restack.go` → no stale claim
remains.

### Step 4: Pin the spawn counts

Add a test (in `internal/stack/engine_test.go`, modeled on the existing
fake-git engine tests via `newEnvState()`/`mkBranch`): build a 4-branch
chain, make everything up to date, run `Restack` from trunk (or
`RestackUpstack(trunk)` directly), and assert via the fake's call counter:
`Tips` == 1 and `RevParse` == 0 during the loop. Add a second case where one
middle branch is stale: assert exactly 1 rebase happened and `RevParse` == 1
(the refresh), and — the load-bearing assertion — its *child* was rebased
onto the refreshed tip (`ParentSHA` of the grandchild equals the rebased
child's new tip).

**Verify**: `go test ./internal/stack -count=1` → ok including new tests.

### Step 5: Full suite

**Verify**: `go test ./cmd/... ./internal/... -race -count=1` → ok;
`make e2e` → ok; `make ci` → exit 0.

## Test plan

- New: the two spawn-count/refresh tests of Step 4 (fake git, microseconds).
- Safety net (must pass unchanged, do not modify): `model_test.go` random
  invariants incl. injected conflicts; `cascade_test.go` cross-worktree
  cases; the PR #92 checkpoint-survives-midway-conflict test; the full
  conflict-recovery suites; e2e journeys.
- Pattern: `TestInferParentDeterministic` (engine_test.go:221) shows the
  `newEnvState()` + fake style.

## Done criteria

Machine-checkable. ALL must hold:

- [ ] `make test-fast` exits 0
- [ ] `go test ./cmd/... ./internal/... -race -count=1` exits 0
- [ ] `make ci` exits 0 (includes e2e + coverage gate)
- [ ] The up-to-date spawn-count test exists and asserts `RevParse == 0` for a clean forest
- [ ] `RestackBranch`'s exported signature and behavior are unchanged (`grep -n "func (s \*State) RestackBranch" internal/stack/restack.go`)
- [ ] The restack.go:103-109 comment no longer claims mutation paths read per step
- [ ] Only in-scope files modified (`git status`)
- [ ] `plans/README.md` status row updated

## STOP conditions

Stop and report back (do not improvise) if:

- `RestackBranch`/`RestackUpstack` don't match the excerpts (beyond plan
  020's preview-path changes).
- Any model/invariant, cascade, conflict, or e2e test fails after Step 2 and
  the failure isn't a trivial error-message-text mismatch — a red model test
  here means the live-map bookkeeping is wrong in a way this plan didn't
  anticipate. Report the failing seed/sequence; do not weaken the test.
- You find a caller that invokes `RestackBranch` in a loop *other than*
  `RestackUpstack` (grep in Step 1's context) — the map optimization must
  then be reconsidered at that site too; report instead of extending scope.
- `restackInWorktree` turns out to move refs of branches *other than* the one
  being rebased (it should not) — the refresh-one-entry model would be
  insufficient; report.
- The e2e suite reveals ordering-visible output changes (restack lists in a
  different order) — ordering must not change; report.

## Maintenance notes

- Anyone adding a ref-moving side effect inside the restack loop (e.g. a
  future auto-fold) must refresh `tips` for every ref they move — the Step 3
  comment is the contract; reviewers should enforce it.
- The `!ok` fallback branch (parent missing from `Tips()`) is expected to be
  nearly dead — `Tips()` lists all local branches — but keeps the error text
  identical for the deleted-outside-st case; don't "clean it up".
- Deferred follow-up: the same map could be threaded into `Delete`'s
  per-child `RestackUpstack` calls (N children → N `Tips()` reads today).
  Left out to keep this plan's blast radius one function.
