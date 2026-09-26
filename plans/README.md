# Implementation plans

Prepared with the improve skill on 2026-09-26 against commit `cb31f06`.

All **14 audit findings** now have implementation plans. The **three direction options** have separate design/spike plans, which produce specifications and acceptance fixtures before runtime implementation. No source changes have been made. The [original deep audit](AUDIT.md) is preserved with its evidence, baseline results and coverage limitations.

Read a plan in full before executing it. Each file includes current source excerpts, exclusive file scope, ordered steps, verification commands, regression cases, done criteria and STOP conditions. All plans are TODO; writing a plan does not establish that its proposed fix works.

## Execution order and status

The numbering is the recommended order. Audit IDs retain the original finding numbers, so the test-environment prerequisite appears first.

| Plan | Outcome | Audit ID | Priority | Effort | Risk | Status |
| --- | --- | --- | --- | --- | --- | --- |
| [001](001-isolate-git-test-environments.md) | Isolate real-Git fixtures from host configuration | 9 | P1 | S | LOW | DONE |
| [002](002-include-rename-sources-in-containment.md) | Keep both sides of renames in the prune containment check | 1 | P1 | S | LOW | DONE |
| [003](003-account-for-every-staged-absorb-change.md) | Account for every staged change before absorb can reset the index | 2 | P1 | M | MED | DONE |
| [004](004-refuse-shifted-absorb-mappings.md) | Refuse absorb hunks whose ancestor coordinates cannot be applied safely | 3 | P1 | M | MED | DONE |
| [005](005-enforce-undo-schema-compatibility.md) | Reject future state schemas before undo mutates anything | 4 | P1 | S | LOW | DONE |
| [006](006-preserve-actual-rebase-target-on-continue.md) | Record the rebase target actually incorporated by continue | 5 | P1 | M | LOW | DONE |
| [007](007-protect-worktree-include-destinations.md) | Refuse worktree include collisions before copying any files | 6 | P1 | M | MED | DONE |
| [008](008-report-confirmed-partial-push-outcomes.md) | Report every confirmed push outcome without retrying the batch | 7 | P1 | M | LOW | DONE |
| [009](009-refuse-abandoned-reclaim-guards.md) | Fail closed when the fallback reclaim guard is abandoned | 8 | P1 | M | HIGH | DONE |
| [010](010-bound-log-history-materialization.md) | Answer log ancestry questions without loading the entire history | 10 | P2 | M | LOW | DONE |
| [011](011-test-installer-signature-decisions.md) | Exercise installer signature acceptance and refusal in CI | 11 | P2 | M | LOW | TODO |
| [012](012-preserve-worktree-path-bytes.md) | Parse worktree paths losslessly with a guarded legacy fallback | 12 | P2 | M | MED | TODO |
| [013](013-quote-manual-navigation-hints.md) | Print safe copyable navigation commands for ordinary paths | 13 | P3 | S | LOW | TODO |
| [014](014-correct-onto-recovery-documentation.md) | Document pending Onto intent and its commit/abort behavior | 14 | P3 | S | LOW | TODO |

S = hours; M = approximately a day including meaningful regressions. These are estimates, not time limits. HIGH risk means the implementation needs particular review attention, not that the finding is uncertain.

## Direction plans

These are bounded design tasks. Completing one produces a contract, examples, acceptance criteria and implementation boundaries under `plans/design/`; it does not ship the proposed feature. Those design outputs are future deliverables, not files already produced by this planning pass.

| Plan | Outcome | Audit ID | Priority | Effort | Risk | Status |
| --- | --- | --- | --- | --- | --- | --- |
| [015](015-design-structured-recovery-status.md) | Specify structured recovery and skip results for orchestrators | D1 | P2 | M | MED | TODO |
| [016](016-design-undo-impact-preview.md) | Specify a read-only preview of the next undo's impact | D2 | P2 | M | MED | TODO |
| [017](017-design-branch-aware-completion.md) | Specify fast read-only branch completion for supported shells | D3 | P3 | S–M | LOW | TODO |

## Dependencies and sequencing

| Plan | Prerequisite | Why |
| --- | --- | --- |
| 001 | None | Isolates fixture configuration before other real-Git regression work. Recommended first; independent fixes can run in an already isolated environment. |
| 004 | 003 | Establishes complete staged-change accounting before changing blame provenance and refusal rules. |
| 015 | 006, 007, 012 before final contract | Recovery design must reflect actual rebase targets, safe include behavior and exact worktree identifiers. Recon/design drafts may start earlier. |
| 016 | 005 before final contract | Preview must share undo's current-state and snapshot schema checks. |
| 017 | 013 as a reference, not a hard blocker | Reuses the shell-safety test lessons; its design has no code dependency. |
| 002, 005–014 | No hard dependency beyond the rows above | Each has its own targeted regression or documentation check. 014 can be completed independently at any time. |

The plans overlap substantially in `internal/git/git.go`, command/engine tests, `docs/AGENT.md`, `CLAUDE.md` and `CHANGELOG.md`. Execute source changes sequentially, or integrate isolated worktrees one at a time and recheck later plans against the merged result. Do not let concurrent executors edit shared files in the same checkout. Plan 004 changes a Git port method; reconcile any adjacent port changes before running it.

Every plan is stamped at the same base commit. A change from a named predecessor is expected, but the executor still must compare the relevant excerpts and assumptions. Unexplained drift requires plan revision; it is not permission for a broad refactor.

## Decisions already made in these plans

- **004 uses conservative refusal.** It will reject shifted/historically renamed absorb mappings rather than implement coordinate relocation. Same-coordinate supported hunks remain available.
- **009 stops automatic abandoned-guard recovery.** Stale `lock.excl` can still be recovered under a newly owned guard; abandoned `lock.reclaim` requires maintenance after verifying writers have stopped.
- **007 refuses destination collisions.** It will not merge copied includes into existing destination roots or overwrite tracked target content.
- **008 reports confirmed outcomes from one push.** It preserves the existing JSON keys and reports transport uncertainty without retrying a mutating batch.
- **010 targets Go memory and Git output volume.** It measures the subprocess tradeoff; it does not claim constant-time ancestry inside Git.
- **012 retains ordinary Git 2.17 compatibility.** Modern NUL framing preserves unusual paths; a guarded legacy fallback refuses ambiguous cases rather than silently raising the version floor.
- **015–017 remain design tasks.** No automatic orchestration, undo mutation or shell installation is authorized by those specifications.

## Verification and handoff rules

The audit baseline passed race unit/integration tests, black-box e2e, native and Windows/Plan 9 cross-vet, pin/dependency checks and coverage at **87.0%**. Full `make ci` was **not run** because pinned golangci-lint was absent. Windows/Plan 9 runtime behavior, old-Git runtime compatibility, release signing and large-history performance were not established by that baseline. Each relevant plan includes its own evidence requirements.

This planning pass validates document structure, numbering, scopes, references and whitespace only. Source tests were not rerun for documentation creation. No dependencies were installed, no feature implemented, and no commits, pushes or issues created.

Execute focused regression gates as the plan directs, including explicitly expected failures before a fix. After a source change, run the contributor gate once the focused checks pass. Do not repeat the full suite without a new change or unresolved failure. For docs-only plans, run their narrower stated gates.

Status values: **TODO**, **IN PROGRESS**, **DONE**, **BLOCKED** (include the concrete reason), **REJECTED** (include the rationale). Update both the index row and the plan's implementation-status field. Record actual commands/results in the plan; never mark DONE based only on the historical baseline.

## Findings considered and rejected or deferred

- Dirty dependent worktree skips, conservative absorb refusal, login-free operation and partial earlier progress after a later cascade failure are documented behavior.
- Configured transports, proxies and hooks are not security defects. Diff parsing is different because it assumes a fixed machine grammar.
- Existing engine/persistence Git boundaries are intentional. No broad interface rewrite or file-size-driven engine split is planned.
- Verification already exists, CI actions are pinned, and release publishing waits for test jobs.
- Production signing-key provisioning is a known separate task. Plan 011 tests signature decisions without provisioning a key.
- Historical QA CSV content is frozen; no synchronization task is planned.
- No dependency/framework migration is justified for the standard-library-only module.
- Direct shell/flag injection, straightforward include path escape and supported remote-web-URL credential exposure were not substantiated. This is not a blanket security certification.
- General corrupt-metadata recovery, simultaneous submit/restack snapshot semantics, and ignored-file preservation during conventional worktree teardown need separate contracts or deterministic evidence; they were not promoted into fixes here.

The archived audit preserves the full reasoning and original recommendations. This index supersedes its earlier “selection pending” state and its preliminary absorb coordinate-rewrite suggestion.
