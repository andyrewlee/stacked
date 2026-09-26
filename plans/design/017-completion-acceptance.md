# Design 017 acceptance: scenario → future test mapping

Each scenario in `017-completion-cases.json` maps to future tests.
`expected` is the sorted candidate list the endpoint emits — one name per
line. `mustNotHappen` entries are negative assertions the corresponding
test must prove.

## Endpoint contract tests (cmd/, fake or real git)

`cmd/completion_test.go` additions driving `st __complete <cmd> <idx> --
<words>` as a subprocess or via `runCompletion`-adjacent dispatch:

| scenario | future test | key assertions |
|---|---|---|
| checkout_tracked_and_trunk | `TestCompleteCheckoutCandidates` | exact ordered list `feat-a feat-b main`; one per line; exit 0 |
| checkout_alias_co | `TestCompleteAliasResolves` | `co` produces checkout's identical set |
| onto_excludes_moving_subtree | `TestCompleteOntoExcludesSubtree` | current + descendants absent; trunk present; no OntoPlan invocation (assert via fake-git call log) |
| track_untracked_only | `TestCompleteTrackUntracked` | local refs minus tracked minus trunk |
| track_parent_flag_value | `TestCompleteFlagValuePosition` | words after `--parent` complete tracked branches; IsBoolFlag-derived value detection, no hand list |
| double_dash_terminator | `TestCompleteDoubleDashPositional` | post-`--` words count as positionals; second positional → empty |
| worktree_create_candidates | `TestCompleteWorktreeCreate` | tracked minus trunk minus owners |
| worktree_rm_owners | `TestCompleteWorktreeRmOwners` | owners only after `rm`/`remove` |
| punctuation_and_unicode | `TestCompleteByteExactNames` | bytes round-trip through stdout decode; `{ } [ ]` Unicode intact |
| detached_head | `TestCompleteDetachedHead` | checkout candidates still emitted; no stderr, exit 0 |
| no_repository | `TestCompleteNoRepo` | empty stdout, empty stderr, exit 0 |
| uninitialized_state | `TestCompleteUninitialized` | empty; state file NOT created on disk (real-git assertion) |
| future_state_schema | `TestCompleteFutureStateSilent` | empty; no speculative parse |
| thousands_of_branches | `TestCompleteBoundedGitCalls` | PATH-shim git wrapper counts execs: ≤4 total, zero `rev-list`/`fetch`/`ls-remote` |

## Shell execution tests (e2e/)

`e2e/e2e_contract_test.go` or a new `e2e_completion_test.go`: source the
GENERATED scripts in real `bash`, `zsh`, `fish` subprocesses (skip shells
absent from the runner — `command -v` guard), drive the completion
function with a sentinel branch name, and assert:

- Candidate text inserted byte-exactly (compare against expected file).
- A name-shaped-like-execution sentinel (e.g. `pwn;touch` is illegal in
  refs — use the legal hairiest: `a{b},c[d]`, `'quoted`, `$(x)` is also
  illegal; the test uses every REFNAME-LEGAL byte combination that looks
  executable) never spawns a process or writes a file.
- No stderr from the endpoint during any scenario.

## Structural performance assertions

Not millisecond thresholds (flaky): a git PATH shim logs argv; the
`thousands_of_branches` case asserts ≤4 endpoint subprocesses, zero
network-capable commands, and one `st __complete` invocation per request.
A `cmd/completion_bench_test.go` `BenchmarkComplete5000Branches` reports
ns/op for documentation only — no CI gate on the number.

## Compatibility invariants

- `st completion <shell>` static output for commands/flags/sub-verbs
  unchanged (existing golden/parse tests keep passing).
- `__complete` never appears in `commandNames()`, help text, or its own
  word-1 candidates.
- Registry-driven: adding a flag with a value to `flagsets.go`
  automatically extends the positional parser — a test adds a synthetic
  flag and asserts.

## Validator

`check-completion.py` asserts: all 14 scenarios exist with `command`,
`words`, `cursor`, `setup`, `expected`, `proposed`; every `command` is a
known registry name or alias (checkout/co/onto/move/track/worktree/wt);
`expected` lists are internally sorted-unique or empty; each scenario is
cross-referenced in this acceptance doc; `mustNotHappen` is present and
nonempty where silence/exec-safety is claimed.
