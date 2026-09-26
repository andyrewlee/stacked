# Plan 017: Specify fast read-only branch completion for supported shells

> **Executor instructions:** Read this entire file, then execute its steps in order. Run each verification command and check its stated result. Stop under the conditions below instead of widening the task. Update this plan's status row in `plans/README.md` when finished.
>
> **Drift check — run first:** `git diff --stat cb31f06..HEAD -- plans/design/017-branch-completion.md plans/design/017-completion-cases.json plans/design/017-completion-acceptance.md plans/design/check-completion.py plans/017-design-branch-aware-completion.md plans/README.md`
> Compare changed source with the excerpts below. Documented predecessor changes are expected; verify that the specific assumptions used here still hold. Stop on unexplained drift. Also inspect `git status --short`; preserve unrelated work.

## Status

- **Priority:** P3
- **Effort:** S–M
- **Risk:** LOW
- **Depends on:** 013-quote-manual-navigation-hints.md informs quoting tests; no source dependency for the design
- **Category:** direction
- **Audit item:** D3
- **Planned at:** commit `cb31f06`, 2026-09-26
- **Implementation status:** DONE

## Execution record

Executed on branch `advisor/017-design-branch-aware-completion` (stacked on `advisor/016-design-undo-impact-preview`).

- Step 1 trace: mapped `runCompletion`/`commandCompletions`/`casePattern`/`commandNames`/`subVerbs` (`cmd/completion.go`) and the positional grammars of `checkout`/`co` (`[name]`, tracked+trunk), `onto`/`move` (`<target>`, valid alternative parents), `track` (`[name]` untracked, `--parent` consumes a value), and `worktree`/`wt` (`<branch>` | `ls|list` | `rm|remove <branch>` | `--all`). Value-taking flags derive from `IsBoolFlag` on the declared FlagSet — no hand-maintained list; Go's flag parser stopping at the first non-flag word and at `--` is reproduced in the protocol.
- Step 2 chose ONE endpoint: hidden `st __complete <command-or-alias> <word-index> -- <prior words...>` — one validated branch name per line on stdout, no descriptions, exit 0 (silent empty) for no-repo/uninitialized/future-state/detached-HEAD/unknown-command/flag-value positions; no lock, no Save, no fetch, no mutation. Never in `commandNames()`/help/word-1 candidates. Scripts resolve `st` on PATH at completion time (`ST_COMPLETE_BIN` override). bash `compgen -W`, zsh `compadd` with `${(f)}` splitting, fish native `-a` substitution — candidates always argv data, never evaluated.
- Step 3 bounded performance: ≤3 git probes inside one endpoint process (`for-each-ref`, `worktree list`, `rev-parse`-family), no network, no history traversal, O(branches) output; acceptance asserts process COUNT via PATH shim rather than flaky millisecond gates.
- Artifacts: `plans/design/017-branch-completion.md` (8 required sections + positional-grammar table), `017-completion-cases.json` (14 scenarios incl. punctuation/Unicode names, `--` terminator, `--parent` value, dirty set boundaries, detached HEAD, no-repo, uninitialized, future schema, 5000-branch bound), `017-completion-acceptance.md` (endpoint tests + real-shell sentinel-execution tests + structural perf assertions + compatibility invariants), `check-completion.py` (stdlib validator: scenarios, registry commands/aliases, words/cursor shape, sorted-unique expectations, silence assertions, acceptance cross-refs).
- Verify commands: `rg -n '^## (Current contract|Candidate policy|Endpoint protocol|Shell integration|Performance and failures|Examples|Acceptance|Deferred work)$' plans/design/017-branch-completion.md` → all 8 sections; `python3 plans/design/check-completion.py` → `check-completion: OK` (exit 0); `rg -c 'argv|newline|detached|uninitialized|future|stderr|bash|zsh|fish' plans/design/017-branch-completion.md` → 22.
- Baseline: `go test ./cmd -run '^Test(CommandCompletions|CompletionScriptsParse|CompletionShells)$' -count=1` → ok.
- `git diff --check` → exit 0; created files all in Scope. No runtime scripts, shell config, or source files changed.

## Why this matters

Completion currently offers commands, flags and sub-verbs but not branch positionals. The repository already knows tracked and local branch sets. A small read-only completion endpoint could improve checkout, onto, track and worktree workflows without introducing a shell JSON parser dependency or fetching from a remote.

## Current state

`cmd/completion.go:69` builds static tokens:

```go
for _, f := range commandFlags(c) {
    add(flagToken(f.Name))
}
for _, v := range subVerbs[c.Name] {
    add(v)
}
```

casePattern already includes aliases. `cmd/checkout.go:77`, listBranches, renders trunk and sorted tracked branches, but also resolves currentBranch and emits user-facing output; it is not directly reusable as a quiet detached-HEAD completion API. Existing tests include TestCommandCompletions, TestCompletionScriptsParse and TestCompletionShells in cmd/commands_json_test.go.

Repository conventions: Go 1.26, standard library only, system Git, no forge API or new module dependencies. Commands in `cmd` parse/render; engine operations in `internal/stack` call the `Git` port and checkpoint through `Env.Save`. Keep Git subprocesses in `internal/git` (command-side adapters already have selected direct probes). The documented Git floor is 2.17; do not silently require a newer version. Existing fake-Git tests model engine behavior; real-Git and e2e tests prove filesystem/index/ref behavior. Use argument arrays, retain JSON output shapes unless this plan explicitly says otherwise, and keep errors on the existing channel.

Preserve generated command/flag discovery from the registry. Support bash, zsh and fish, with no jq or other runtime dependency. Branch candidates are argument values, never shell code. Keep completion silent outside repositories and do not acquire a mutation lock, fetch, save metadata or run worktree-changing commands.

## Commands you will need

Run from the repository root.

| Purpose | Command | Expected result |
| --- | --- | --- |
| Source baseline | `go test ./cmd -run '^Test(CommandCompletions\|CompletionScriptsParse\|CompletionShells)$' -count=1` | existing tests pass; this does not validate a future feature |
| Design artifact validation | `python3 plans/design/check-completion.py` | exit 0; required sections, examples and acceptance cases are present and consistent |
| Whitespace | `git diff --check` | exit 0 |
| Future implementation build/lint/full gate (not required for this design-only task) | `make build fmt-check lint ci` | a later implementation must pass the repository's pinned gates |

This is a design-only task. Existing shell syntax tests establish the current generation pattern; proposed dynamic completion requires future shell execution tests and is not validated by these existing tests alone.

## Scope

**Only modify:**

- `plans/design/017-branch-completion.md`
- `plans/design/017-completion-cases.json`
- `plans/design/017-completion-acceptance.md`
- `plans/design/check-completion.py`
- `plans/017-design-branch-aware-completion.md`
- `plans/README.md`


**Out of scope:** runtime completion scripts/endpoints, user shell configuration, installing shells, remote completion, a persisted candidate cache, new Go dependencies.

## Git workflow

Use a separate branch/worktree named `advisor/017-design-branch-aware-completion` if the operator's execution workflow provides one; do not change or discard unrelated work. Suggested conventional commit: `docs: specify branch-aware shell completion`. Do not commit, push, merge, or open a PR unless the execution request authorizes that action.

## Steps

### Step 1: Map positional grammar and branch candidate sets

Read cmd/completion.go, checkout.go, onto.go, track.go, worktree.go and their flag constructors in cmd/flagsets.go. Create sections `Current contract`, `Candidate policy`, `Endpoint protocol`, `Shell integration`, `Performance and failures`, `Examples`, `Acceptance`, and `Deferred work`. Build a table for canonical commands and aliases, positional index, flags that consume values, -- terminator behavior, sub-verbs, and candidate source. Recommend tracked branches plus trunk for checkout; only valid alternative parent candidates for onto; local untracked branch candidates for track where the current grammar accepts them; and branch candidates appropriate to worktree create/remove. Derive eligibility from existing validation without executing mutators; if a command accepts a different grammar, document the actual grammar rather than forcing this recommendation.

**Verify:** `rg -n '^## (Current contract|Candidate policy|Endpoint protocol|Shell integration|Performance and failures|Examples|Acceptance|Deferred work)$' plans/design/017-branch-completion.md` → all eight sections appear and the command/alias/position table is complete

### Step 2: Choose a quiet endpoint and shell-safe transport

Specify one recommended internal read-only completion command that receives command/position context as argv and emits one raw validated branch name per line, with no descriptions in the first version. Git ref-name rules exclude newline, making this candidate framing viable; still preserve legal punctuation as literal data. Keep the endpoint hidden from ordinary help/completion suggestions and define how generated scripts locate the running st binary. Define exact behavior for detached HEAD, outside a Git repo, uninitialized state, corrupt/future state, unknown commands, flags expecting a value and empty results: silent no candidates is the interactive default, with no stderr pollution. No evaluation of candidate strings, no command substitution assembled from branch names, and no jq. Describe shell-specific array/candidate APIs and escaping tests for bash/zsh/fish. Reuse branch enumeration below listBranches rather than rendering/parsing checkout JSON with shell tools.

**Verify:** `rg -n 'argv|newline|detached|uninitialized|future|stderr|bash|zsh|fish' plans/design/017-branch-completion.md` → transport, failure handling and all three shell behaviors are explicitly specified

### Step 3: Define acceptance cases and a bounded performance budget

Write 017-completion-cases.json containing words, cursor position, repository scenario and expected candidates/order for each supported command/alias. Include branch names with slashes, Unicode and legal shell punctuation, option values, --, worktree sub-verbs, detached HEAD, no repository, uninitialized state and thousands of local branches. Specify no network calls, no full-history traversal, one endpoint process per completion request and a bounded number of local Git queries. Put a reproducible benchmark recipe and non-flaky structural performance assertions in the acceptance document; avoid a hard machine-specific millisecond CI threshold. Add check-completion.py to validate scenarios, referenced command/alias entries, candidate uniqueness/order and acceptance coverage. Describe future tests that invoke actual generated shell completion functions and prove candidates remain inert strings. Finish with exact implementation file boundaries and test commands.

**Verify:** `python3 plans/design/check-completion.py` → all examples and acceptance mappings validate; the existing completion tests also pass

## Test plan

- Artifact validator checks every supported command/alias position and failure scenario.
- Future tests must execute generated completion functions in bash/zsh/fish, not merely parse their syntax.
- Branch punctuation must never execute a sentinel command; candidates must retain exact names after shell insertion.
- Future performance tests assert no network/history traversal and bounded Git process count, with benchmark evidence for a large branch set.

## Done criteria

- [ ] Four scoped design artifacts exist and check-completion.py passes.
- [ ] One endpoint protocol and a complete positional candidate policy are specified.
- [ ] Shell safety, quiet failures, detached HEAD and performance bounds have explicit acceptance cases.
- [ ] No runtime scripts, shell configuration or source files changed.
- [ ] `git diff --check` exits 0.
- [ ] Compare `git diff --name-only` and `git ls-files --others --exclude-standard` with the initial inventory; every task-created or task-modified file is in Scope.
- [ ] Record executed commands and results in this plan; update its index row to DONE only when all required gates pass, otherwise BLOCKED with the concrete reason.

## STOP conditions

- Source assumptions no longer match, except for understood changes from the named dependencies.
- A verification fails twice after a reasonable correction, or the regression fails for an unrelated fixture/setup reason.
- The solution needs an out-of-scope file, dependency, public contract change, or a Git/toolchain floor increase.
- The design requires parsing JSON with a new external tool, evaluating branch text, or contacting a remote.
- Candidate eligibility can only be obtained by invoking a mutator; specify a future read-only extraction instead.
- One shell cannot safely represent an allowed branch name with the proposed protocol; revise the protocol before claiming portability.

## Maintenance notes

New flags and aliases must update positional parsing through the same registry/flag metadata where possible. Completion should remain a cheap read-only convenience and must never become a hidden mutation path.
