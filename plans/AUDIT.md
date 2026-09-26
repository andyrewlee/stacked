> Archived audit from 2026-09-26. Its selection status and preliminary ordering are historical; [the implementation index](README.md) now supersedes them.

# Deep audit — stacked

Audited commit: `cb31f06` (2026-09-26). Status: **audit complete; implementation-plan selection pending**.

This is an audit index, not an executable implementation plan. No source files were changed. Findings below were independently checked against the cited source after parallel review. Confidence describes the evidence, not whether a new reproducer was executed; verification distinctions are recorded explicitly.

## Scope and repository intent

- One Go 1.26 module, standard library only, approximately 32,800 Go lines including tests. Packages: `cmd`, `cmd/st`, `internal/git`, `internal/stack`, and `e2e`.
- Product: a login-free stacked-diff CLI over system Git. No forge API, local common-Git-directory metadata, JSON command results, linked worktrees, conservative absorb attribution, and reversible journaled mutations.
- Architecture: command adapters parse/render; `stack.Env` supplies a Git port and checkpoint callback; production Git implementation shells out with argument arrays; state and journal use atomic writes.
- Read README, CONTRIBUTING, CLAUDE, the agent API guide, historical QA documentation, root/release/CI configuration, production packages, relevant tests, and recent history/churn. No separate ADR, PRODUCT, DESIGN, or CONTEXT document was present. The historical QA CSV is explicitly frozen.
- Existing tests include fake-Git engine/model tests, real-Git integration tests, subprocess e2e, fuzz seeds, race checks, total coverage and a per-function coverage floor. This repository already has a verification baseline; creating one is not a recommended project.
- Distribution targets are Linux/macOS amd64/arm64. CI additionally executes Windows tests and cross-vets Windows/Plan 9. Supporting Git 2.17 while recommending 2.31 is a documented compatibility constraint.

## Verification

Environment: Go `1.26.5`, Git `2.54.0`, macOS arm64.

| Command/check | Result |
| --- | --- |
| `make check-deps check-lint-version check-go-version check-goreleaser-version vet vet-cross` | PASS; includes native, Windows amd64 and Plan 9 amd64 vet |
| `./scripts/cover.sh` | PASS; race unit/integration + black-box e2e, 87.0% merged coverage, 75% total threshold and 50% function floor satisfied (4 allowlisted functions) |
| `sh -n install.sh` | PASS |
| `bash -n scripts/check-install-assets.sh scripts/cover.sh` | PASS |
| `git diff --check` | PASS; no tracked source changes |
| `gofmt -l cmd internal e2e` | PASS; no files reported |
| Deliberately contaminated single-test invocation below | Expected FAIL, confirming finding 9 |

The contamination check ran `go test ./internal/git -run '^TestCurrentBranchAndExists$' -count=1` with command-scoped `GIT_CONFIG_COUNT=2`, `commit.gpgSign=true`, and `gpg.program=/usr/bin/false`. It failed in fixture creation with “gpg failed to sign the data”; it did not change user configuration. This failure is separate from the normal suite result.

The exact full contributor gate is `make ci`. It includes `check-deps`, lint/Go/GoReleaser pin consistency, `fmt-check`, native/cross vet, build, lint, and coverage. **Full `make ci` was not run**: `golangci-lint` is absent, and installing tooling is outside this read-only audit. Plain gofmt is not a substitute for the configured gofumpt gate. The coverage script builds the actual CLI in a temporary directory and writes only the ignored `cover.out` artifact in the checkout.

Not exercised here: Windows/Plan 9 runtime behavior, older supported Git versions, actual release publishing/signing, native installer execution, extended fuzz campaigns, large-history benchmarks, or exhaustive secret/history scanning. All production packages were reviewed, but not every test assertion was individually audited. No third-party Go dependency advisory scan applies to the empty dependency graph; this report does not certify the toolchain or external tools free of vulnerabilities.

## Vetted findings, ordered by leverage

S = hours; M = roughly a day including meaningful regressions; L = multiple days. Risk is the risk of the proposed fix. All numbered findings have HIGH code confidence; platform races and history-rewrite scenarios that were not executed are identified in the details.

| # | Finding | Category | Impact | Effort | Fix risk | Evidence |
| --- | --- | --- | --- | --- | --- | --- |
| 1 | Rename containment can prune an unmerged deletion | Correctness | High | S | Low | `internal/git/git.go:258`, `internal/stack/engine.go:1028` |
| 2 | Absorb can omit staged changes before resetting them | Correctness | High | M | Medium | `internal/git/git.go:483`, `internal/stack/absorb.go:317` |
| 3 | Absorb can edit the wrong repeated line in an ancestor | Correctness | High | M | Medium | `internal/git/git.go:665`, `internal/git/git.go:748`, `internal/git/git.go:825` |
| 4 | Undo bypasses the new state-schema compatibility guard | Correctness | High | S | Low | `cmd/undo.go:68`, `internal/stack/undo_op.go:22` |
| 5 | Continue can record a parent commit the child never incorporated | Correctness | High | M | Low | `internal/stack/engine.go:949`, `internal/stack/restack.go:138` |
| 6 | Worktree includes can overwrite files tracked by the destination branch | Correctness | Medium | M | Medium | `cmd/worktree_copy.go:61`, `cmd/worktree_copy.go:419` |
| 7 | Partial submit output can omit branches already published | Correctness/API | Medium | M | Low | `cmd/submit.go:110`, `cmd/submit.go:159`, `internal/git/git.go:1370` |
| 8 | Stale fallback-lock recovery can admit two writers | Concurrency/integrity | High, rare trigger | M | High | `internal/stack/lock_stale.go:53`, `internal/stack/lock_stale.go:148` |
| 9 | Real-Git test fixtures inherit developer configuration | Tests/DX | Medium | S | Low | `internal/git/git_test.go:212`, `internal/stack/store_test.go:18` |
| 10 | Log reads full reachable history and repeatedly traverses it | Performance | Medium at scale | M | Low | `cmd/log.go:216`, `cmd/log.go:244`, `cmd/log.go:275` |
| 11 | Installer signature decisions have no behavioral CI coverage | Security/tests | Medium | M | Low | `scripts/check-install-assets.sh:46`, `scripts/check-install-assets.sh:137` |
| 12 | Worktree parsing cannot preserve newline-containing paths | Correctness | Low | M | Medium | `internal/git/git.go:859`, `internal/git/git.go:884` |
| 13 | Printed navigation commands do not quote paths | DX | Low | S | Low | `cmd/sanitize.go:115`, `cmd/shell.go:145` |
| 14 | Architecture guide states the opposite Onto recovery invariant | Docs | Low | S | Low | `CLAUDE.md:113`, `internal/stack/engine.go:569` |

### 1. Include both sides of renames in containment

`ChangesContainedIn` enumerates `git diff --name-only -z upstream...branch`, then compares upstream and branch only on those paths (`internal/git/git.go:258`, `:271`). Rename detection collapses the source deletion into the destination name. `mergedBranches` treats the result as safe to prune (`internal/stack/engine.go:1058`), and `PruneMerged` force-deletes the branch (`:1028`), after removing a clean owning worktree when applicable.

Example: a branch renames A to B; trunk independently copies A to B but retains A. B matches, so the branch is reported contained even though its deletion of A has not landed. This is a false positive in a destructive predicate, not merely a missed squash-merge optimization.

Read-only verification of the Git premise used existing commit `b569262`: `git diff --name-status b569262^ b569262` reports the QA CSV rename, while `--name-only` includes only its destination. The full synthetic sync fixture was not created during the audit.

Fix sketch: disable rename collapsing for path collection, then test rename-versus-copy, a fully contained rename, and source/destination evolution in real Git and sync. S effort; low risk because the path set becomes complete while the conservative content comparison stays intact.

### 2. Make absorb account for every staged change

The diff parser promises classify-or-refuse, but its default flush simply appends whatever hunks were recognized (`internal/git/git.go:483`). Empty-file additions/deletions have no text hunks, and `new file mode`/`deleted file mode` are not handled by the existing `old mode`/`new mode` check (`:510`). Those sections yield neither an attributed hunk nor a refusal.

With another valid ancestor-owned hunk, the zero-refusal gate passes. Per-target patch assembly excludes the empty-file change; `internal/stack/absorb.go:317` then resets the current index/worktree. The staged empty-file addition/deletion is discarded. If the only target is the current branch, that particular reset is skipped; the dangerous case is an ancestor or multi-target apply. The undo journal captures refs/state, not the original index.

The same completeness boundary must handle Git configuration. Diff capture at `internal/git/git.go:465` and `:610` pins quotePath but leaves prefixes, color and diff drivers configurable; parsing expects literal `--- a/`, `+++ b/`, and `@@` headers. A read-only historical-diff check confirmed that no-prefix/color settings remove recognizable headers. Whole-diff formatting changes can produce a false “nothing staged”; a per-file unrecognized section alongside ordinary hunks risks partial accounting. These are machine-parser assumptions, not objections to honoring normal user transport/hook settings. [Git diff options](https://git-scm.com/docs/git-diff) document controls for prefixes, colors and diff drivers.

Fix sketch: normalize machine-oriented diff output using options compatible with the supported Git floor; represent or refuse every section; cross-check accounted paths/records before any destructive reset. Add real-Git regressions for mixed normal and empty-file changes and configured formatting/drivers. M effort; medium risk around conservative parsing and existing refusal semantics.

### 3. Map absorb edits into ancestor coordinates

Blame retains only final line → SHA (`internal/git/git.go:821`–825), dropping original line coordinates. Patch assembly retains HEAD-relative `OldStart` (`:665`) and applies the resulting zero-context patch against the ancestor tip's tree (`:743`–748).

Concrete code-derived scenario: A owns two identical lines at offsets 2 and 4. B prepends two lines, moving them to offsets 4 and 6. Editing the first occurrence produces a hunk at HEAD line 4; applying that hunk to A can match A's second occurrence at line 4. Ownership attribution succeeds, the wrong line is amended, and the cascade can finish cleanly. The staged copy is then gone from the current index. Git's [zero-context apply behavior](https://git-scm.com/docs/git-apply) does not establish identity of the intended occurrence.

This specific fixture was not executed; the finding follows the blame → patch → apply path. The current real-Git amend test explicitly keeps positions identical (`internal/git/git_test.go:1469`), so it does not guard the shifted repeated-line case.

Fix sketch: retain original coordinates, validate every mapped range against the owning tree, and conservatively refuse ambiguous/non-contiguous mappings. Add a real-Git shifted-duplicate regression and exact final-tree assertions before broadening attribution. M effort; medium risk to multi-target patch assembly. Preserve existing refusals for historical, off-path, binary, quoted-path and ambiguous edits.

### 4. Apply schema compatibility checks to undo

`Load` rejects newer state schemas (`internal/stack/store.go:108`), but `runUndo` treats every load failure as permission to restore from the journal (`cmd/undo.go:68`–74). `Undo` unmarshals the snapshot without checking its version (`internal/stack/undo_op.go:22`) before performing recovery mutations.

An older binary can therefore run incompatible recovery semantics despite a rejected current-state schema. If the current state loads but an undo snapshot is newer, assigning the decoded snapshot and calling `Save` also drops unknown fields and stamps the older supported schema. The nil-state raw-restore path preserves snapshot bytes; it is incorrect to claim it always strips fields, but it still bypasses compatibility refusal.

Fix sketch: reject `ErrStateTooNew` explicitly, validate snapshot versions before any worktree/ref/state mutation, and retain intentional recovery for supported missing/corrupt state. Add command and engine regressions asserting state, refs and journal remain unchanged on refusal. S effort; low risk.

### 5. Preserve the actual target of a paused restack

Ordinary restack passes a resolved parent SHA into rebase but does not persist that SHA on conflict (`internal/stack/restack.go:138`–141). After `RebaseContinue`, the ordinary continuation path records the parent's then-current tip (`internal/stack/engine.go:949`–953). Unlike `PendingReparent`, this does not necessarily describe the base Git actually used.

If A advances from A1 to A2 in another worktree while B's rebase onto A1 is paused, continuing B records A2 even though B contains A1. The following cascade sees a matching recorded base and skips B (`internal/stack/restack.go:119`); future status/restack can also report no drift. This is supported by the state-machine path, not a newly executed fixture.

Fix sketch: capture the rebase's actual `onto` SHA before Git removes its rebase metadata, or persist equivalent pending-operation state. Then normal restacking can detect subsequent parent movement. Test parent advancement between conflict and continuation, repeated conflicts, and save failure. M effort; low fix risk if isolated to recording the truthful base.

### 6. Protect destination contents when copying worktree includes

`materializeWorktreeAt` checks out the destination branch before copying (`cmd/worktree.go:155`, `:163`). Include selection queries only the source index (`cmd/worktree_copy.go:61`, `:243`). Destination checks reject symlinks but permit existing regular files; copying may overwrite them (`:339`, `:419`).

A path ignored on the source branch can be tracked on the target branch. The new worktree is then created successfully with that tracked file overwritten by local source content. Existing directory destinations also have divergent semantics: native `cp -R` nests into an existing directory, while the Go fallback merges it.

Fix sketch: check the target index and destination collisions before copying, define skip/refusal behavior, and align native/fallback directory semantics. Test branches with different tracked paths and directories containing target-tracked descendants. M effort; medium risk to include-directory behavior. Do not weaken existing source/destination containment or symlink guards.

### 7. Report the initial batch push's real outcome

`PushBranches` issues a non-atomic multi-ref push and returns only an error (`internal/git/git.go:1370`–1379). On any error, submit retries individually (`cmd/submit.go:110`), then reports only the successful prefix of that second pass (`:159`–168).

If the batch publishes A and C but rejects B, the retry reports A, stops at B, and omits already-published C. Existing tests use `pre-receive`, which rejects the whole batch; they do not cover a per-ref `update` rejection (`cmd/commands_json_test.go:872`). Git explicitly distinguishes these [hook semantics](https://git-scm.com/docs/githooks#_update), and supports [per-ref machine-readable push output](https://git-scm.com/docs/git-push#Documentation/git-push.txt---porcelain).

Fix sketch: retain authoritative per-ref outcomes, or use a clearly specified atomic-first policy with careful unsupported-server handling. Avoid blindly repeating successful network mutations. Test a rejected middle ref, successful later refs, stale leases and accurate JSON/text partial results. M effort; low fix risk when the existing public result shape is retained. This finding concerns inaccurate reporting, not lost commits.

### 8. Serialize stale reclaim-guard recovery

`removeLockFileIfContentErr` reads and compares a token, closes that read, then independently unlinks the pathname (`internal/stack/lock_stale.go:53`–65). `acquireReclaimGuard` uses that operation to reclaim the guard itself (`:148`), while actual stale-lock reclamation assumes the guard has provided exclusion (`:215`).

A crashed reclaimer can leave both files stale. Valid interleaving: A checks the old guard and pauses; B replaces it and checks the old actual lock; A removes B's replacement guard, establishes its own guard and actual lock; B's delayed unlink removes A's replacement actual lock and establishes B's lock. Both acquisitions return success. This affects the fallback used on Windows and other non-flock platforms. Existing concurrent tests seed only a stale actual lock (`internal/stack/lock_stale_test.go:282`).

This race was established by source interleaving, not reproduced on Windows. Fix sketch: use an OS-backed exclusion primitive or conservatively refuse automatic recovery of an abandoned reclamation guard until exclusion can be established. First add a deterministic interleaving regression for both stale files; do not rely solely on stress loops. M effort; high fix risk because portability and crash recovery are involved.

### 9. Isolate all real-Git test fixtures

`internal/git` fixtures execute Git with inherited config/environment (`internal/git/git_test.go:212`, `:226`). `internal/stack` fixtures do likewise (`internal/stack/store_test.go:18`, `internal/stack/undo_test.go:17`). The isolation in `cmd/integration_test.go:25` and the e2e subprocess harness does not apply to these separate test binaries.

The command-scoped signing contamination check above reproduced a failure during the initial fixture commit. Developer hooks, signing and repository-routing settings can similarly influence tests, even though the worktrees are temporary.

Fix sketch: give every real-Git test harness deterministic config/identity and clear inherited routing/config-injection variables. Preserve production behavior and tests that intentionally exercise particular configurations. S effort; low risk; useful before other real-Git regression work if an executor's environment is affected.

### 10. Bound log's history work

`runLog` always creates `tipGraph`, including when trunk is the only rendered node (`cmd/log.go:53`, `:77`). `tipGraph` supplies only positive revisions to `rev-list --parents`, materializing all history reachable from rendered tips (`:216`–239). For each ordinary child ahead of its parent, `topSubject` asks whether the child is reachable backward from the parent (`:275`); the false answer traverses parent history with a fresh visited map (`:244`–260).

Cost can therefore grow with B × H, where B is the number of tracked branches and H is shared reachable history; even an empty stack exports trunk history. Reducing subprocess count has not bounded bytes, allocations or graph traversal. No large-history latency benchmark was run, so this is an algorithmic finding, not a measured performance claim.

Fix sketch: establish long-history/small-stack benchmarks and preserve existing subject semantics for equal, behind and diverged tips. Replace full history export with bounded ancestry queries or an indexed/batched strategy whose cost is demonstrated by those benchmarks. M effort; low risk if output contracts stay unchanged. Do not assume more subprocesses automatically means slower execution.

### 11. Test signature verification behavior without production keys

The installer smoke skips signing by default (`scripts/check-install-assets.sh:46`) and always opts into checksum-only fallback (`:137`). Grepping signature-related text in the config is not behavioral coverage of `install.sh:121`–129.

CI can miss broken valid-signature acceptance, missing-prerequisite refusal, or invalid-signature rejection even with `ST_ALLOW_UNVERIFIED=1`. The empty production public key is an explicitly documented provisioning step, not an undisclosed vulnerability found here.

Fix sketch: generate disposable fixture keys, use a temporary installer copy with that public key, and test valid signatures, missing key/tool/signature, tampered checksums/assets, and invalid signatures with and without the override. Production release secrets must not enter PR CI. M effort; low risk, test-only scope apart from any needed safe test seam.

### 12. Preserve worktree path boundaries

`Worktrees` requests newline-delimited porcelain and splits every newline (`internal/git/git.go:859`, `:884`), storing the fragment after `worktree` as the path (`:893`). Legal Unix paths containing newlines cannot round-trip; navigation and ownership-sensitive operations consume the wrong path.

Fix sketch: parse NUL-delimited output without trimming path bytes and add round-trip tests. Preserve the documented Git 2.17 floor through an explicit capability/fallback decision: `worktree list -z` is present in [Git 2.36 documentation](https://git-scm.com/docs/git-worktree/2.36.0), while the previous version's documentation lacks it. M effort; medium risk from compatibility. No supported-Git-floor change should be slipped into a parser fix.

### 13. Quote manual navigation hints

The suggested `run: cd …` line inserts an unquoted destination in both terminal and plain summaries (`cmd/sanitize.go:115`, `cmd/shell.go:145`). Terminal-control escaping is a different operation from shell argument quoting. Ordinary spaces break the suggested command; shell metacharacters change its interpretation.

Fix sketch: render a properly shell-quoted command argument separately from the human descriptive path and test spaces, quotes and metacharacters. Keep the installed shell shim's existing quoted behavior. S effort; low risk; not evidence that the shim itself evaluates path contents as commands.

### 14. Correct the Onto recovery description

`CLAUDE.md:113` says the new parent is stored before rebasing. In fact, conflict stores `PendingReparent` (`internal/stack/engine.go:569`), successful rebase updates the parent (`:592`), Continue promotes pending intent (`:939`), and Abort clears it (`:891`). A regression explicitly protects unchanged parent metadata during conflict (`internal/stack/engine_test.go:858`).

Fix sketch: update the contributor invariant to describe pending intent, promotion and abort behavior; retain links to the relevant regression. S effort; low risk. This deserves a small documentation correction, not an architecture rewrite.

## Direction options — separate from defect priority

These are design/spike candidates, not commitments. Effort estimates are coarse.

1. **Structured recovery and skip status for orchestrators (M; HIGH grounding, medium API risk).** The documented orchestration loop reads log, restacks, then examines prose `notes` for dirty-worktree skips (`docs/AGENT.md:181`–188; `internal/stack/engine.go:375`–387). An additive structured list with branch, worktree, reason code and next action could make that loop reliable without parsing messages. Decide compatibility and the scope of repository-wide versus current-worktree recovery before adding fields.
2. **Undo impact preview (M; HIGH grounding, medium design risk).** `undo --list` already exposes captured refs and created branches (`cmd/undo.go:153`–210), while the actual undo can delete branches/worktrees and restore all captured refs (`internal/stack/undo_op.go:26`–125). A read-only preview comparing live and recorded state could show exact affected refs and intervening commits before execution. The tradeoff is designing honest drift reporting without implying that a preview reserves state or makes a later undo atomic.
3. **Branch-aware shell completion (S–M; HIGH grounding, low risk).** Completion currently derives command flags/sub-verbs (`cmd/completion.go:69`), while checkout already provides a structured branch listing (`cmd/checkout.go:77`). Reuse a read-only branch enumeration path for checkout/onto/track/worktree positionals. Keep completion fast and silent outside initialized repositories and define shell quoting across bash, zsh and fish.

## Recommended selection and ordering

Recommended initial implementation plans: **1–5**. Each should include its own reproducing regression; these gaps do not justify a broad test rewrite.

- Findings 1, 4 and 5 are independent and have bounded verification stories.
- Land absorb completeness (2) before the coordinate rewrite (3); both touch diff/patch handling. Characterize mixed unsupported staging and shifted repeated lines before changing either path.
- If an executor's Git configuration affects tests, do 9 before relying on its regression results; it does not block work in a clean environment.
- For 8, a deterministic stale-guard race test precedes any locking redesign.
- For 10, establish the history-size benchmark and subject-output characterization before optimization.
- Direction work should follow the relevant safety fixes: undo preview after 4; richer recovery status after 5 and worktree boundary fixes. No implementation plan has been selected or written yet.

| Plan | Finding | Status |
| --- | --- | --- |
| — | Awaiting user selection; recommended 1–5 | AUDITED |

## Considered and rejected or deferred

- Dirty dependent worktrees being skipped, conservative absorb refusals, no forge API, and partial earlier progress after a later cascade failure are documented behavior.
- Conventional configured transports/proxies/hooks are not security findings. The parser in finding 2 is different: it assumes a fixed machine grammar while consuming configurable output.
- The engine's Git value-type import and persistence-layer Git-directory probe are explicitly documented boundaries, not layering defects requiring interfaces everywhere.
- No general-purpose engine split/refactor: file size alone did not establish a maintenance benefit strong enough to compete with correctness work.
- Missing verification baseline, unpinned CI actions and publishing before tests were rejected: the baseline exists, actions are pinned, and release waits for Linux/macOS and Windows jobs.
- Production signing provisioning is intentionally incomplete and fail-closed; report 11 covers missing behavioral tests, not the known provisioning task.
- Frozen QA documentation is explicitly historical; no plan to keep the CSV synchronized.
- No arbitrary dependency/framework upgrades: the module has zero third-party dependencies and no migration need was demonstrated.
- Direct shell/flag injection, straightforward include path escape and supported remote-web-URL credential exposure were not substantiated in the inspected boundaries. Existing argv, validation, containment and sanitization defenses were retained as positive evidence, not a security certification.
- Corrupt metadata with null branch records or cycles can challenge readers; deferred as lower-leverage recovery hardening rather than competing with valid-operation bugs. A future investigation should define the supported corruption/recovery contract before changing loading behavior.
- Submit during concurrent restacking lacks an explicit operation snapshot/lock; plausible concurrency concern, but not promoted without a deterministic overlapping-operation regression. The confirmed partial-push reporting defect is finding 7.
- Ignored-file removal during conventional clean-worktree teardown was not promoted without a stronger preservation contract.

Only files under `plans/` may be edited during this advisory workflow. Implementation must be performed separately after plan selection.
