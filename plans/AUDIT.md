# Deep audit — stacked

Reviewed 2026-10-02 against `159648a` (`main`). This is an audit and selection record, not an executable implementation plan. No source, configuration, refs, hooks, keys, or dependencies were changed. Only files under `plans/` were edited.

The highest-value work is correcting offline sync's inconsistent base, extending the repository lock to navigation and initialization, repairing undo's failure ordering, rejecting trunk records in tracked state, and preserving failed-absorb recovery pointers. The existing 20-plan backlog contains valid findings but also unsafe instructions; its revised status is in [README.md](README.md).

## Scope and verification

All production packages were reviewed: `cmd`, `cmd/st`, `internal/git`, and `internal/stack`. The review also covered `e2e`, scripts, local hooks/gates, installer/release configuration, contributor and machine-agent documentation, and existing plans. Three read-only reviewers covered engine correctness, Git/security boundaries, and verification/performance; the primary reviewer opened the reported evidence before accepting it.

All nine audit categories were considered: correctness, security, performance, tests, architecture, dependencies/migrations, tooling, docs, and product direction. The standard-library-only design, thin adapters, pure engine, dirty-tree refusal, login-free Git transport, and local-only CI are documented decisions. The dependency invariant holds: no `require` entries and no `go.sum`; no dependency migration is recommended.

| Verification | Observed result |
|---|---|
| `make test-fast` | Passed |
| `go test -timeout 20m ./cmd/... ./internal/... ./e2e/... -race -count=1` | Passed: cmd 382.866s, git 103.413s, stack 18.988s, e2e 91.043s |
| `make vet vet-cross` | Passed natively and for Windows/amd64 and Plan 9/amd64 |
| `make ci` | Exit 2 at missing golangci-lint, before fmt/vet/build/lint/coverage |
| Initial dependency/tool/version checks and `check-shell` | Passed available checks; seven shell scripts clean |
| `make check-install` | Passed signature/checksum decision matrix; GoReleaser/schema/asset leg skipped because binary absent |
| Repeated JSON-flag failures | Reproduced using the version command |
| Unicode conflict filename | Reproduced by a reviewer against a disposable real-git fixture and binary |
| Existing log benchmark | One cold iteration per case; results below, not a stable performance baseline |

Environment: Go 1.26.5, Git 2.54.0, Darwin/arm64. The full gate is **not green**: formatting, lint, merged coverage, GoReleaser asset checks, and strict release checks were not completed. No tools were installed. No release was signed or published, no credentials/key contents were inspected, and no network-dependent repository mutations were attempted.

Not audited dynamically: native Windows/Plan 9 behavior, every supported historical Git version, three-shell path round trips, live forge/transport behavior, release-key usability, exhaustive fuzzing, or every failure scenario described below. Tests and goldens were inspected selectively rather than every assertion being read line by line. Source-derived scenarios are identified as such; no exploit execution or stable speedup is claimed.

## Vetted findings

Ordered by practical leverage, with content/recovery failures ahead of cleanup. Effort: S = localized change, M = several seams/tests, L = substantial design. Risk describes the **fix**, not the defect. Confidence is HIGH unless marked otherwise; HIGH source confidence does not mean the trigger was reproduced dynamically.

| # | Finding | Category | Impact | Effort | Fix risk | Confidence | Evidence |
|---|---|---|---|---|---|---|---|
| F01 | Offline sync prunes and restacks against different trunks | Correctness | Removes landed ancestor content from a surviving child; preview disagrees | M | MED | HIGH | internal/stack/ops_delete_sync.go:129,165,178,237; internal/stack/restack.go:288 |
| F02 | Navigation and initialization bypass the repository lock | Correctness | Wrong-branch amend/create; concurrent init can overwrite new metadata | S–M | MED | HIGH | cmd/shell.go:218; cmd/init.go:43,58; internal/stack/store.go:97; internal/stack/ops_lifecycle.go:101,137 |
| F03 | Undo saves old metadata before restoring refs | Correctness | Failed ref transaction leaves metadata and refs inconsistent | S–M | MED | HIGH | internal/stack/undo_op.go:123,129,143 |
| F04 | State decoding accepts trunk in the tracked branch map | Correctness | Unbounded log recursion; malformed state can make pruning target trunk | S | LOW | HIGH | internal/stack/store.go:160; cmd/log.go:185,205; internal/stack/ops_delete_sync.go:373 |
| F05 | Continue promotes stale pending reparent by branch name | Correctness | Records a parent/base the completed foreign rebase did not incorporate | S–M | LOW | HIGH | internal/stack/ops_onto.go:138,152,180 |
| F06 | Failed absorb loses undo's amended-commit annotations | Correctness | Undo leaves consumed edits recoverable only through unnamed reflog commits | M | MED | HIGH | internal/stack/absorb.go:330,354,373; cmd/absorb.go:44,66 |
| F07 | Conflict filenames are Git display spellings | Correctness | JSON paths cannot be used to resolve/stage Unicode and unusual filenames | S | LOW | HIGH, reproduced | internal/git/index.go:83,91; cmd/status.go:49,118 |
| F08 | Error JSON mode uses the first repeated flag | Correctness | Failure envelope disagrees with the parser's final JSON setting | S | LOW | HIGH, reproduced | cmd/root.go:244,479 |
| F09 | Undo journal restore values allow revision expressions/zero OIDs | Security | Corrupted journal can redirect or delete refs; preview prints raw values | S–M | MED | HIGH | internal/stack/undo.go:54; internal/git/refs.go:527,541; cmd/undo.go:451 |
| F10 | Rebase does not invalidate cached worktree ownership | Correctness | Later cascade can use the wrong conflict/recovery path | S | LOW | HIGH | cmd/gitenv.go:54; internal/stack/worktree.go:184,195,240 |
| F11 | Repository version text enters Makefile shell recipes | Security | Legal tag metacharacters can execute commands during build/install | S | LOW | HIGH | Makefile:2,3,38,41 |
| F12 | Release preflight does not prove key pairing/usability | Tooling/security | Can publish signed assets the installer refuses; signing may prompt | S–M | LOW–MED | HIGH | scripts/check-release-ready.sh:14,28; Makefile:306; .goreleaser.yaml:49 |
| F13 | Continue omits skipped-worktree notes | Correctness | Reports success while dirty dependents remain stale without warning | S | LOW | HIGH | internal/stack/ops_onto.go:207,223 |
| F14 | Undo preview does not model apply's evolving refs/errors | Correctness/architecture | Wrong final checkout and optimistic preview of failing recovery | M | MED | HIGH | internal/stack/undo_preview.go:103,289; internal/stack/undo_op.go:146,154 |
| F15 | Paused detached owners are absent from owner lookup | Correctness/tests | Skip/ownership contract gap; current fake masks it | S–M | MED | MED overall; investigate destructive impact | internal/git/worktree.go:231; internal/stack/worktree.go:127; internal/stack/fakegit_test.go:461 |
| F16 | Portable include copy reads special/large files wholesale | Correctness/performance | FIFO can hang while holding lock; memory scales with file size | S | LOW–MED | HIGH | cmd/worktree_copy.go:185,211; internal/stack/worktree_include.go:239 |
| F17 | Shell teleport loses newline-containing path bytes | Correctness | Navigation can fail or enter a different directory | S–M | MED | HIGH | cmd/shell.go:90,102,159,197 |
| F18 | Debug trace does not escape terminal controls | Security | Opt-in traces can render path/ref controls despite normal sanitization | S | LOW | HIGH | internal/git/debug.go:62,66; cmd/sanitize.go:84 |
| F19 | Captured Git errors have no credential scrubbing | Security | Transport/hook diagnostics can expose credential-bearing URLs in logs | S | LOW | MED reachability; HIGH propagation | internal/git/git.go:52; internal/git/refs.go:251,428; cmd/root.go:438 |
| F20 | Installer does not validate binary archive member type | Security/tooling | Malformed authenticated/waived archive can install a link/special member | S–M | MED | HIGH | install.sh:156,161,163,166 |
| F21 | Destructive checkpoints/new adapter contracts lack targeted coverage | Tests | Recovery and CLI-boundary regressions can pass the existing suite | M | LOW | HIGH | internal/stack/failure_injection_test.go:474; e2e/e2e_journey_sync_test.go:14; cmd/prune.go:43 |
| F22 | Log serializes one ancestry process per distinct pair | Performance | Routine observation latency grows with branch count | M | MED | HIGH spawn count; MED optimization | cmd/log.go:261,274; cmd/log_bench_test.go:15 |
| F23 | Immutable remote configuration is queried twice | Performance | Avoidable Git spawns in open/submit | S | LOW | HIGH duplicate; savings unmeasured | cmd/open.go:76,80; cmd/submit.go:80 |
| F24 | Formatting's lint dependency precedes compiler checks | Tooling | Missing lint stops promised compiler feedback | S | LOW | HIGH, observed | Makefile:28,35,50 |
| F25 | Extension/completion documentation names obsolete seams | Docs | Contributors/agents follow incorrect operation and completion recipes | S | LOW | HIGH | CONTRIBUTING.md:61; CLAUDE.md:95; docs/AGENT.md:375 |

### F01 — consistent offline-sync basis

The no-fetch branch picks cached remote trunk for merged detection, but apply calls restackAll using local trunk tips. Dry-run instead overrides the trunk tip with the cached remote.

Concrete source-derived scenario: local main is T; branch a adds A; b adds B on a; cached origin/main already contains A. Sync prunes a, reparents b to main while retaining its old a-tip base, then replays only B onto T. A is absent from b afterward. Existing no-fetch tests cover prunable leaves rather than content of surviving children.

Plan the policy before implementation: pruning, actual rebase targets, recorded bases, and preview must agree while honoring no fetch/no local trunk fast-forward. Merely rebasing onto the remote without addressing future local-parent drift is incomplete. Add real-git file-content assertions and preview/apply parity. New finding, adjacent to 008/017.

### F02 — lock every st operation that changes shared state or HEAD

Navigation loads state/current branch and reaches plain git.Checkout without acquireLock. Modify holds the lock but captures cur before later amending current HEAD. A concurrent st checkout can switch to another branch, including trunk after Modify's trunk refusal, so it amends the wrong branch and cascades from the captured old branch. Create similarly captures one parent but creates from a switched HEAD.

Init separately uses Load/Stat followed by atomic replacement Save without locking. Two initializers can both pass the existence checks; a delayed initializer can replace metadata after the other process has initialized and tracked a branch. Atomic file replacement does not serialize that protocol.

Cover state/current selection through in-place navigation and Load-through-Init with the existing shared advisory lock, avoiding nested acquisition. Add deterministic interleaving or lock-held tests. This is concurrent **st** behavior, not the documented inability to serialize arbitrary external Git. New; one focused lock-contract plan can address both holes.

### F03 — undo failure ordering

Undo assigns the old state and calls env.save before its atomic UpdateRefs batch. If that batch fails, refs remain at their current values but persisted topology/base metadata is already rolled back. The existing failed-batch test asserts atomic ref behavior without asserting that metadata remains truthful.

Revise 003 with explicit preflight and retry/error semantics. Moving ref restoration ahead of the state save fixes this arm but does not make all undo atomic: created-branch/worktree removal already precedes the batch, and a later save can still fail. Test both ref and save failures and document completed destructive work rather than promising total rollback. Keep cache invalidation F10 as a separable correction if that makes the recovery plan clearer.

### F04 — state must not track its own trunk

The shared state/undo-snapshot decoder checks nil records and key/name agreement, but allows Branches[Trunk]. A record with Parent equal to trunk puts trunk into its own ChildIndex list. Both log renderers recurse from that root without a visited guard; repair's cycle walk terminates when it sees trunk rather than diagnosing it.

Prune enumerates all map keys, including trunk, and can mark trunk merged into itself. With HEAD elsewhere, malformed state can therefore send trunk to the deletion phase. Normal operations never create this representation. Reject it at the decoding barrier before any mutation; add bounded rendering for manually constructed cyclic inputs and state/journal regression cases. New.

### F05 — match pending continuation to the actual paused target

Continue checks PendingReparent.ParentSHA against the real paused target only when head-name is empty. With a normal head-name matching the pending branch, it skips reading the target and promotes the record by name.

After an onto conflict is handled using raw git, pending metadata can survive. A later differently targeted rebase of that same branch can then promote the stale parent/base, and unchanged parent tips can hide the mismatch from the cascade. Read and validate actual onto before every promotion; mismatches should follow the existing foreign-rebase policy or refuse explicitly. Test the named-branch stale-record case, not only missing head-name. New.

### F06 — retain failed-absorb commit recovery pointers

Absorb amends targets, drops staged copies, then can fail during cascade, final tip reads, saving, or HEAD restoration. Those arms return nil result. The adapter returns on error before SetLastUndoAbsorbed, even though cleanup keeps the journal entry because refs moved.

Later undo restores pre-absorb refs and emits recovery SHA/cherry-pick notes only when AbsorbedCommits is populated. The edits remain recoverable through reflogs, but the durable named recovery pointer is lost precisely when it matters. Preserve actual successful amendments through partial results or persistence callbacks before destructive resets; do not record unapplied plan targets as amendments. Test hard failure after staged-copy removal, then undo and recovery notes. New; not already covered by 007.

### F07 and F08 — machine-output fixes

UnmergedFiles reads Git's line-oriented display output and does not decode C-quoted paths. The real-git reproduction on café.txt emits a quoted octal-escaped display spelling inside conflictedFiles. Use the existing raw/NUL parsing pattern of LsFilesZ; avoid TrimSpace on path bytes. Assert exact Unicode, newline, and surrounding-whitespace names rather than merely a nonempty list.

Separately, parseBuiltinArgs and Go flag parsing accept repeated JSON flags with the last value winning; jsonRequested returns the first match. Reproduced: version --json=false --json --unexpected emits text, and the reverse flag order emits JSON. Make dispatch error-format detection agree with parsing, including terminators and consumed flag values. Preserve the intentional malformed-JSON-value behavior. Both are new, small, independent changes.

### F09 — validate journal values before preparatory mutation

Decoded undo entries do not enforce full nonzero commit OIDs. UpdateRefs only rejects whitespace/control bytes and hands values to Git's object-name parser. A zero new value deletes a ref; a revision expression selects a different commit. The single-ref preparatory restore also needs the same barrier. Human preview sanitizes branch names but prints From/To directly.

Revise 001: use full nonzero OIDs appropriate to supported repository object formats; validate before worktree, HEAD, branch, or state changes; sanitize displayed values. External ref-drift/CAS policy is a separate product decision, not proof that ordinary historical undo is a vulnerability.

For the proposed NUL emitter, empty old-OID means missing/no verification; zero old-OID requires absence. Zero new-OID deletes. The current adapter comment accurately describes its unconditional update; the old plan's CAS instructions do not. These semantics are documented in [Git update-ref](https://git-scm.com/docs/git-update-ref).

### F10 — invalidate ownership on rebase

cachedPort overrides checkout, detach, rename, and worktree removal, but inherits rebase operations unchanged. In a multi-worktree cascade, in-place rebase changes which branch cwd owns while the cached list still names its original branch. A later visit can treat that old branch as owned elsewhere and route it into the cross-worktree path.

That path aborts conflicts and reports a generic error rather than leaving an in-place conflict paused for continue. Invalidate after every ownership-changing rebase operation, including failures and quiet variants. Use a production cachedPort/real-worktree scenario to prove the corrected conflict policy. Existing 003; avoid introducing broader caches before this is fixed.

### F11 — keep version data out of shell parsing

Makefile obtains VERSION from git describe and expands it into double-quoted build/install recipes. Relevant shell metacharacters are legal Git tag bytes, so tag-controlled text can become shell syntax even with unchanged checked-out source. This is a build boundary; runtime Git adapters use direct argv.

Read version as data inside a recipe with an explicit accepted grammar, or use a direct-argv helper. Preserve normal release/hash/dirty spellings. Test unusual legal tag bytes in an isolated fixture without executing an attack payload. [Git ref-name rules](https://git-scm.com/docs/git-check-ref-format) support the tag-byte premise. New.

### F12 — prove signing before publication

Release preflight checks only a non-placeholder public string and a secret-key comment. A mismatched key can pass and publish signatures that install.sh rejects. Release/snapshot also do not depend on the pinned GoReleaser check, and release does not run strict installer/asset verification.

Correct 004 and the dependent 006 runbook. Require a bounded, noninteractive sign/verify rehearsal against the embedded public key, actual pinned release tooling, and strict release-adjacent checks. Do not silently skip the pairing proof on a publishing path.

The proposed grep for an encrypted comment is incorrect: upstream uses the same default comment for both key forms, and signing loads the key without using the -W generation/password-change flag. See [minisign constants](https://raw.githubusercontent.com/jedisct1/minisign/master/src/minisign.h) and [generation/signing implementation](https://raw.githubusercontent.com/jedisct1/minisign/master/src/minisign.c). The current empty installer key fails closed and blocks release; provisioning is operator work, not an automatic audit action.

### F13 and F14 — truthful recovery observation

Continue restacks the forest but does not drain skipped-worktree notes, unlike Restack, Sync, and Absorb. A dirty dependent can remain stale while the result says continued restack without the usual warning. Append the existing notes once on success and assert both JSON and text behavior. New.

UndoPreview ignores a failed RebaseInProgress probe that real apply surfaces. It also only proposes a final checkout if the branch exists in the pre-undo live set; apply recreates journal refs before that decision. A deleted/renamed current branch can therefore be recreated and checked out by apply while preview reports no checkout. Multi-step preview evolves metadata but not the full ref inventory.

Expand existing 010 with these concrete cases before unifying paths. Resolve corrected validation/ordering first; a structural refactor alone would preserve or conceal these mismatches.

### F15 — paused-owner behavior and fake conformance

Real paused rebases appear detached with no Branch in worktree porcelain. OwnerOf matches only Branch, so paused-owner gates are not reached for that worktree. The fake always reports linked branches as attached even when rebaseInWT is set; its apparently protective cascade regression test models a state Git does not report.

The lookup gap is confirmed. Native Git has independent protections, so this review does **not** claim the gap alone proves branch deletion or foreign-rebase destruction. Revise 002 around a real detached-owner fixture and observable metadata/ref effects; use that fixture as the first 016 conformance case. Characterization precedes shared-gate refactoring in 017.

### F16 and F17 — exact and bounded filesystem behavior

plainCopy handles symlinks and directories, then uses ReadFile for everything else. A selected ignored FIFO can block the fallback while worktree creation holds the repository lock; large regular files allocate their full size. Reject unsupported file types and stream regular data with bounded buffers. Test the fallback directly so platform cp availability cannot hide it. New.

The shell directive writes exact paths, but Bash/Zsh read them with command substitution and Fish uses line-splitting substitution. Trailing newlines are lost in the former; embedded newlines become multiple arguments in the latter. The manual hint explicitly sends control-byte paths to this integration. Test exact path round trips in all three shells and use a delimiter/read mechanism that preserves bytes. This is path correctness, not evaluation of the path as shell code. See [Bash substitution](https://www.gnu.org/s/bash/manual/html_node/Command-Substitution.html) and [Fish substitution](https://fishshell.com/docs/current/language.html#command-substitution). New.

### F18 and F19 — diagnostic boundaries

Debug trace redacts URL-shaped argv but writes the remaining bytes directly to stderr. Worktree paths, invalid ref data, and Unicode format/control characters can bypass normal human-output sanitization when ST_DEBUG is enabled. Escape after redaction, preserving one trace line per spawn; test synthetic path bytes. New.

Captured Git diagnostics are separately interpolated into errors without credential scrubbing and exported through JSON. Custom wrappers in refs, rebase, and worktree files duplicate the boundary. No credential exposure was observed; transport/hook reachability is conditional. Retain 019 with all captured wrappers and synthetic credential tests, preserving useful host/path diagnostics. Redaction and terminal escaping solve different output properties.

### F20 and F21 — distribution and verification hardening

Installer authenticates checksums, then extracts the entire archive and moves st without checking member type. A malformed authenticated artifact, or one accepted under the explicit unverified waiver, can supply a link/special member; chmod can affect a link target. This is not a signature bypass. Narrow 005 to regular-member validation, bounded extraction, and explicit executable mode; remove its unsupported ambient-override/JSON threat explanations.

Keep 007 focused on failures after meaningful checkpoints: Sync detach/prune/save, Continue completed-rebase/save, cross-worktree recovery, and the containment probe. Assert persisted metadata, completed mutations, final HEAD, and composed errors rather than just nonnil error.

Keep 008 for standalone prune/undo/dry-run, newest open JSON contracts, bulk track, and sync flags through the binary. The searched e2e sources contain no standalone prune/open invocation and do not exercise successful explicit sync no-fetch/no-delete/remote flag journeys. Prove no-fetch transport avoidance with a real mutation and unreachable remote; dry-run alone cannot do that. Fold F01's surviving-child content test into its fix rather than creating overlapping plans.

### F22 and F23 — performance with measured scope

Log serializes merge-base probes for each distinct nontrivial branch-parent pair. A reviewer ran the existing benchmark once per case on an Apple M1 Max while other checks may have been running:

| History commits | 0 branches | 10 branches | 50 branches |
|---|---|---|---|
| 1,000 | 116.7ms | 435.3ms | 1,781.2ms |
| 20,000 | 118.0ms | 589.8ms | 1,863.7ms |

These noisy cold samples support investigating branch-count latency, not claiming a speedup. Benchmark a small bounded worker pool over immutable observed SHA pairs; collect results/errors deterministically and preserve bounded history traversal.

Revise 020: inferParentPick's best candidate is a sequential dependency, and it sorts a shared slice in place. Naive parallelization changes semantics or races. Rebase methods return only errors, so there is no existing returned tip OID to reuse; moving RevParse into an adapter saves no process. Measure repeated runs before selecting an optimization.

For 011, immutable remote configuration reuse is a smaller independent opportunity: open checks RemoteExists and then RemoteURL, and submit has an analogous sequence. One URL lookup can classify absence and supply the value. Preserve error/output behavior. Broader tip/HEAD caching requires freshness boundaries and correct F10 invalidation; finalization needs fresh post-operation tips.

### F24 and F25 — align tooling and contributor contract

Makefile promises compiler/vet checks before lint-dependent failure, but fmt-check precedes them and itself needs golangci-lint. The observed make ci failure confirms the ordering. Put independent compiler checks earlier in the serialized gate; do not imply that this makes the full gate pass without the lint tool.

Update 013: contributor recipe should put operations in their domain ops file; completion policy lives in command registration, not the removed switch. The machine docs omit delete/untrack/rename completion policies. Keep frozen historical QA and accurate changelog history unchanged.

## Existing-plan reconciliation

Every existing plan was reviewed against the same HEAD; none was marked DONE. Source did not drift, so the issue is finding/approach quality rather than stale line numbers.

| Plan | Verdict |
|---|---|
| 001 | Revise before execution: nonzero restore values, object-format policy, NUL old-OID semantics, validation timing, and explicit external-drift policy |
| 002 | Investigate with realistic detached-owner fixture; narrow destructive-impact claims |
| 003 | Revised after selection: explicit partial-cleanup and save-failure retry semantics; snapshot-ref transaction before metadata save; five actual rebase-port cache wrappers |
| 004 | Revise: comment detector is wrong, signing rehearsal must be bounded/noninteractive/mandatory for release; enforce strict tooling |
| 005 | Revise: keep member-type/mode validation, remove unsupported override and release-JSON explanations |
| 006 | Revise key-format explanation; retains explicit operator/release dependency, not autonomous execution |
| 007 | Retain targeted checkpoint/failure coverage, avoid trivial delegate tests |
| 008 | Retain; strengthen no-fetch and surviving-child content checks without duplicating F01 |
| 009 | Retain low-priority hygiene: stale allowlist cmd path and duplicated assertions; no claim all exceptions are unnecessary without merged coverage |
| 010 | Retain after corrected 001/003; add F14's recreated-checkout, probe-error, and multi-step-ref cases |
| 011 | Retain narrowly: remote lookup reuse first; caches require fresh-snapshot and invalidation discipline |
| 012 | Investigate before implementation; no gate speedup measured; cmd cwd/env prevent blanket parallel tests |
| 013 | Retain F25; preserve frozen/history docs |
| 014 | Lower priority; reuse registry/helper seams only where actual drift or duplicate protocol warrants it |
| 015 | Lower priority; parsed locale is already pinned, so do not claim random localization failures; state probes can improve error classification |
| 016 | Retain as a focused fake/real port conformance opportunity; detached ownership is the first useful case |
| 017 | Retain after owner characterization; duplicated preview/apply predicates are a future drift risk, not a new independent failure |
| 018 | Reject demonstrated-vulnerability framing: symlinks are copied verbatim by tested design, not dereferenced |
| 019 | Retain F19, expand captured wrapper inventory, qualify credential reachability |
| 020 | Revise: immutable independent probes, measured bounded concurrency, no blind parallel inference or nonexistent returned-OID reuse |

Rejected findings/approaches are also recorded in the index so they are not re-audited: direct symlink-copy exfiltration, automatic broken-link refusal, normal Git proxy/transport behavior, runtime shell injection where direct argv is used, removal of dirty refusal, historical QA/changelog drift, absence of remote CI, and speculative checkpoint/cache optimizations.

## Direction options

These are product choices, not defects, and are separate from the ranked fixes.

1. **Local-versus-published freshness in log/status** (M, MED risk). PushBranches already records upstreams (internal/git/remote.go:137), while observation exposes only local topology/restack/worktree state (cmd/log.go:139, cmd/status.go:108). A batched offline comparison can show unpublished rewrites; cached remote refs need explicit missing/unknown/stale semantics and cannot promise current server state.
2. **Stack-relative commits/diff inspection** (S–M, LOW–MED risk). Recorded parentSHA is central to the model, and CommitRange/subject primitives exist (internal/git/refs.go:438,564). A show/commits surface would reduce hand-built ranges; decide recorded-base versus current-parent behavior when drift exists before naming the API.
3. **Repeated per-worktree checks** (M–L, MED risk). The guide and agent contract already describe per-branch worktree orchestration (cmd/guide.go:25, docs/AGENT.md:294). A bounded each-style argv runner could simplify checks, but requires explicit choices about absent worktrees, cancellation, environment/cwd, and aggregated exits.

These overlap the prior index's unplanned roadmap suggestions; no duplicate design plans were created.

## Recommended selection and ordering

The maintainer selected all five recommended recovery/correctness plans. Recommended execution order is [021: offline sync](021-offline-sync-local-basis.md), [022: navigation/init locking](022-navigation-init-locking.md), [revised 003: undo ordering and rebase cache](003-undo-order-cache.md), [023: trunk-state validation](023-trunk-state-validation.md), then [024: failed-absorb recovery](024-absorb-recovery-checkpoints.md). The plans are self-contained; execute serially to avoid shared test/doc edits. F05 is the next recovery correction; F07/F08 are small independent machine-contract fixes.

Each fix includes its own characterization/regression tests. OID validation must precede any new undo CAS/refactor; corrected undo ordering and drift policy precede 010. Real detached-owner characterization precedes 002/017 owner changes. Cache invalidation precedes broader 011 caching. Correct 004 and the 006 runbook before any release-key provisioning or publication work.

Selected plans are written against `159648a`. New numbering starts after 020; the existing undo/cache finding keeps 003, now revised rather than duplicated. Remaining findings are retained in this audit for later selection; no source implementation was performed.
