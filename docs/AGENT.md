# Driving `st` from an agent

`st` is built to be driven programmatically. Local stack operations do not prompt
for input, JSON-capable commands emit machine-readable JSON on request, and all
commands report outcomes through stable exit codes.

## No prompts

Local stack operations do not read from stdin or open an interactive editor. The
rebase path runs with `GIT_EDITOR`/sequence-editor disabled; when a rebase stops
on a conflict, `st` returns control to you (exit code 2) rather than blocking —
resolve the files, `git add` them, and run `st continue` (or `st abort`).

Remote Git operations use your configured Git transport. `st sync` may fetch and
`st submit` may push, so credentials, SSH agents, and host prompts behave like
the corresponding `git fetch`/`git push` commands in your environment.

## Exit codes

Branch on the exit code; do not parse messages.

| code | meaning | typical recovery |
|---|---|---|
| 0 | success | — |
| 1 | usage error / generic failure | fix the invocation |
| 2 | rebase conflict in progress | resolve + `git add`, then `st continue` (or `st abort`) |
| 3 | repo not initialized | run `st init` |
| 4 | working tree is dirty | commit or stash, then retry |
| 5 | another `st` command holds the repo lock, **or** an abandoned `lock.reclaim` guard needs an operator | `"locked"`: retry once it finishes; `"locked_guard"`: stop — a human must remove the guard file (see CONTRIBUTING) |
| 70 | internal error (a bug in `st`) | not self-recoverable; report it |

## JSON output

Every subcommand except `completion` and `shell` accepts `--json`. The built-ins `help`,
`-h`/`--help`, `version`, and `-v`/`--version` also accept `--json`. Successful
JSON output is a single indented object on **stdout**. On failure, `--json`
writes a structured envelope to **stderr** and the process still exits with the
code above:

```json
{ "error": { "code": "conflict",
             "message": "rebasing \"feat-b\" onto \"feat-a\": rebase conflict …",
             "branch": "feat-b", "onto": "feat-a" } }
```

`error.code` is one of: `error` (1), `conflict` (2), `not_initialized` (3),
`dirty` (4), `locked` (5), `locked_guard` (5 — an abandoned `lock.reclaim`
guard; operator action required, do not retry), `internal` (70, a recovered panic). So an agent can read stdout for
the result, stderr for the error envelope, and the exit code for the category.
On a `conflict` the envelope also carries `branch` (the branch whose rebase
stopped) and `onto` (its parent), so you can re-orient without parsing the
message.

### Result shapes

- **Stack-mutating commands** (`create`, `modify`, `restack`, `continue`, `fold`,
  `squash`, `onto`, `delete`, `track`, `untrack`, `rename`) share one shape:
  ```json
  { "summary": "...", "branch": "feat-b", "restacked": ["feat-c"] }
  ```
  `branch`, `restacked`, `deleted`, `notes`, and `dryRun` are all `omitempty` —
  absent when empty or false. Preview-capable commands (`restack`, `sync`,
  `onto`, `fold`, `squash`, `delete`) return the same result shape with
  `"dryRun": true` under `--dry-run`. In a multi-worktree repo, `restack`/`sync`
  rebase a dependent branch that lives in another worktree *inside that worktree*;
  a dirty dependent worktree is skipped and named in `notes` (e.g. `"skipped
  feat-a: its worktree is dirty (…)"`) rather than clobbered. `continue` resumes an interrupted restack,
  emitting `{ "summary": "continued restack", "restacked": [...] }` plus a
  `notes` entry naming the branch whose conflict was just completed.
- **`absorb --json`** — its own shape, NOT the shared one:
  ```json
  { "summary": "...", "absorbed": [{ "file", "lines", "branch", "commit" }],
    "refused": [{ "file", "lines", "reason" }], "restacked": [], "notes": [],
    "dryRun": true }
  ```
  `restacked`, `notes`, and `dryRun` are `omitempty`. `--dry-run` maps staged
  hunks to the stack commits owning their lines with zero mutation
  (`"dryRun": true`). Every staged section must classify as plain text hunks
  or be refused — metadata-only changes such as empty added/deleted files,
  mode-only changes, renames/copies, binary files, and quoted paths are all
  refused, and a raw staged-path inventory cross-check fails closed on
  anything the parser cannot account for. The staged diff is captured with
  pinned flags, so user diff configuration (prefixes, color, external diff,
  textconv) cannot reshape it. Targets are restricted to branches on the current
  stack's path (the current branch plus its ancestors): a hunk owned by a
  commit tipped only by an off-path tracked branch is refused, naming that
  branch. Attribution also requires identity coordinates from blame: a hunk
  whose lines sit at a different line number or path in the owning commit —
  shifted by a descendant's edit, or belonging to a since-renamed file — is
  refused, because the amend applies the hunk at its HEAD line numbers and
  shifted coordinates could land on the wrong occurrence of repeated text.
  Bare `absorb` applies any plan with ZERO refusals — multi-target
  plans amend each owning tip with its own hunks and run one cascade; any
  refusal (or a dirty target worktree) comes back unapplied with the summary
  prefixed `"not applied: ..."` and exit 0 (refusals are data, not errors).
  `absorbed[].commit` reports each branch's live post-cascade tip. An applied absorb reports the amended tip in `absorbed[].commit`
  and the cascaded branches in `restacked`; a conflict mid-cascade exits 2
  for `st continue`/`st abort`, and one `st undo` reverts the amend plus the
  cascade.
- **`create --worktree --json`** — creates and tracks the branch without moving
  the current worktree, materializes the new branch's linked worktree, copies
  `.worktreeinclude` entries, and returns
  `{ "branch", "parent", "worktree", "copied": [], "switched": bool, "summary" }`
  (`copied` is `omitempty`). `switched` is true only when the `st shell install`
  shim is active and the command wrote `$ST_CD_FILE`; without the shim, text mode
  prints a `cd` hint instead. `--worktree` cannot be combined with `-m`/`-a`;
  commit inside the created worktree afterward.
- **`log --json`** — a recursive tree rooted at the trunk:
  ```json
  { "name": "main", "current": false, "needsRestack": false,
    "children": [ { "name": "feat-a", "parent": "main", "parentSHA": "…",
                    "current": true, "needsRestack": false, "topCommit": "add a", "children": [] } ] }
  ```
  In a multi-worktree repo each node may also carry `worktree` (the on-disk path
  of the linked worktree the branch lives in) and `dirty` (true when that
  worktree has uncommitted changes); both are `omitempty`, so single-tree output
  is unchanged.
- **`status --json`** — `{ "branch", "trunk", "role", "children": [], "worktreeClean": bool }`; `parent` is present for tracked branches, and `needsRestack` is present only when it applies. During a paused restack it also carries `rebaseInProgress` (set true), `rebaseBranch` (the branch the rebase stopped on), and `conflictedFiles` — so an agent can re-orient after exit 2 without raw git. In a multi-worktree repo it also carries `worktree` (the path of the linked worktree the current branch lives in, `omitempty`).
- **`checkout --json`** — with a name, `{ "branch", "switched": bool }`; with no
  name, `{ "trunk", "current", "branches": [] }`. When the branch lives in another
  worktree, checkout teleports there and adds `worktree` (the path, `omitempty`).
- **`validate --json`** — `{ "ok": bool, "tracked": n, "problems": [], "warnings": [] }` (exit 1 if problems)
- **Navigation** (`up`/`down`/`top`/`bottom`) — `{ "branch", "summary" }` (`up` adds `children` at a branch point). When the move teleports into another worktree, the `summary` names the worktree path; with the `st shell install` shim the shell `cd`s there (the binary writes the path to `$ST_CD_FILE`).
- **`worktree --json`** (`wt`) — `st worktree <branch>` returns `{ "branch", "path", "copied": [], "summary" }` (`copied` lists `.worktreeinclude` files brought over, `omitempty`); `st worktree ls` returns an array of `{ "path", "branch", "head", … }`; `st worktree rm <branch>` returns `{ "branch", "removed" }` (`removed` is a STRING — the released path). The bulk forms return aggregates: `st worktree --all` → `{ "created": [{ "branch", "path", "copied": [], "summary" }], "skipped": [{ "branch", "reason" }], "failed": { "branch", "error" } }` and `st worktree rm --all` → `{ "removed": [{ "branch", "path" }], "skipped": [{ "branch", "reason" }], "failed": { "branch", "error" } }` — note `removed` is an ARRAY of objects in the bulk form, unlike the single-branch string. Both stop at the first hard failure (`failed`, non-zero exit) and skip dirty/main-worktree branches into `skipped`. `st shell install` emits a shell script, not JSON.
- **`submit --json`** — one shape for every outcome:
  `{ "remote", "dryRun", "pushed": [], "repoURL", "prHints": [], "summary", "failed" }`
  (`repoURL`, `prHints`, `summary`, and `failed` are `omitempty`; from trunk,
  `pushed` is empty and `summary` explains why). On successful non-trunk submits,
  `prHints` lists `{ "head", "base", "compareURL" }` objects so each stacked PR
  targets its stack parent; `compareURL` is present for known compare URL shapes
  (github.com, gitlab.com, and self-hosted hosts whose name carries a
  `github`/`gitlab` label); for unrecognized hosts the hint object still
  carries `head`/`base` but `compareURL` is simply absent. Submit reads the single
  batch push's own per-ref statuses, so a partial failure can report a
  non-prefix outcome — e.g. `pushed` `[A,C]` with `failed` `B` when the remote
  accepted A and C but rejected B in the same push: the result is the same
  shape carrying `{ "remote", "dryRun", "pushed", "failed" }`, `pushed` lists
  every branch the remote confirmed updated or already current in stack order,
  `failed` names the first confirmed rejection, and the process still exits
  non-zero with the error envelope on stderr. A ref whose outcome the push did
  not confirm is reported in neither field — the stderr message describes the
  uncertainty instead.
- **`init --json`** — one shape for both outcomes:
  `{ "trunk", "initialized": bool, "alreadyInitialized": bool }` (a fresh init
  sets `initialized`; an already-initialized repo sets `alreadyInitialized`).
- **operational** — each emits a small fixed object:
  - `abort --json` → `{ "aborted": true, "summary" }`.
  - `undo --json` → `{ "undone": true, "label", "restored": [] }` (`label` names the
    reverted command; `restored` lists branches whose tips were moved back). An
    empty journal is still a success: `{ "undone": false }` on stdout, exit 0
    (text mode prints `nothing to undo`).
  - `undo --list --json` previews the journal without reverting:
    `{ "entries": [ { "index", "label", "currentBranch", "createdBranches": [],
      "createdWorktrees": {}, "refs": {} } ] }`, newest first — `index` 1 is
    what a bare `undo` reverts. `refs` maps each recorded branch to the tip it
    would be reset to; `createdBranches`/`createdWorktrees` name what the op
    created and undo would remove (both `omitempty`, as is `currentBranch`);
    the internal state snapshot is never emitted. An empty journal emits
    `{ "entries": [] }` (exit 0; text prints `nothing to undo`).
  - `repair --json` → `{ "repaired": bool, "fixes": [] }` (`repaired` is true when
    `fixes` is non-empty; both are present on every run).

## Idempotency & safety

- `restack`, `sync`, `validate`, `log`, `status` are safe to re-run.
- `st restack --all` restacks every tracked branch (the whole forest) from any
  branch or worktree — the current branch need not be tracked; branches living
  in dirty linked worktrees are skipped into `notes`. `--all --dry-run`
  previews the same set.
- `st restack --dry-run`, `st sync --dry-run`, `st onto --dry-run`,
  `st fold --dry-run`, `st squash --dry-run`, and `st delete --dry-run` preview
  the branches that *would* be rebased, moved, folded, squashed, or deleted.
  They return a `{"dryRun": true, ...}` result without changing stack metadata or
  branch refs. `sync --dry-run` does not fetch; it uses the current local trunk or
  already-cached `refs/remotes/<remote>/<trunk>`.
- `restack` requires a clean tree (exit 4 otherwise) and is idempotent once the
  stack is in sync.
- `undo` reverts the last mutating command's metadata and branch tips; it does not
  touch the working tree.
- Concurrent `st` processes in one repo are serialized by an advisory lock (a
  second one fails fast rather than corrupting state): flock on unix-like
  platforms, an exclusive lock file elsewhere. A contender exits 5
  (`"code": "locked"`). To have `st` itself wait instead of looping in your
  shell, set `ST_LOCK_WAIT` to a Go duration or bare seconds
  (`ST_LOCK_WAIT=10s` or `ST_LOCK_WAIT=10`) — acquisition is retried for up to
  that budget and exit 5 still means the lock never cleared. On non-flock
  platforms an *abandoned* `lock.reclaim` guard (left by a dead process) is
  also exit 5 but carries `"code": "locked_guard"` — a maintenance error
  naming the guard path; it is never removed automatically and never retried,
  even with `ST_LOCK_WAIT` set. See CONTRIBUTING's troubleshooting section for
  the operator procedure.

## Orchestrating parallel agents

One stack, N agents, one worktree per branch:

1. **Spawn.** `st create <name> --worktree --json` returns `worktree` (the
   directory to start the agent in) and `switched` (whether the calling shell
   teleported). For an existing branch: `st worktree <branch> --json` returns
   `path`. To seed every tracked branch at once: `st worktree --all --json`
   returns `{"created":[{"branch","path","copied":[],"summary"}],
   "skipped":[{"branch","reason"}],"failed":{"branch","error"}}` — the branch
   checked out in the main worktree is skipped, adds stop at the first failure
   (`failed`, non-zero exit; rerunning is safe — materialization is
   idempotent).
2. **Observe.** `st log --json` carries, per node, `worktree` (the branch is
   claimed by that worktree), `dirty` (uncommitted work there), and
   `needsRestack`. A node with no `worktree` field is unclaimed.
3. **Coordinate.** `st restack` / `st sync` rebase branches living in other
   worktrees *inside those worktrees* automatically and SKIP dirty ones, naming
   them in the result's `notes` — so an orchestrator's loop is `st log --json`
   (who needs restack) then `st restack --all` (whole forest from any worktree,
   dirty agents skipped), re-checking `notes` for skipped branches. `st sync` works from
   a linked worktree too (a dirty trunk worktree blocks it with an error naming
   the path).
4. **Clean up.** `st worktree rm <branch>` releases a branch's worktree, and
   `st worktree rm --all` releases every linked worktree in one call (dirty
   ones are skipped into `skipped`, mirroring `worktree --all`'s shape);
   `st undo` after a `create --worktree` also removes the worktree it created.

   Worktree paths are byte-exact: on git ≥ 2.36 the listing uses the
   NUL-framed `worktree list --porcelain -z` grammar, so paths containing
   newlines or other control bytes round-trip losslessly through JSON. On
   older git the line-based listing is used only when the repository's
   worktree metadata provably contains no CR/LF path bytes; a repo whose
   worktree paths need them fails with an actionable error naming git ≥ 2.36
   rather than returning a corrupted ownership map. Terminal output always
   escapes control bytes in paths.

## Discoverability

- `st --help` lists every command; `st help <command>` prints its summary, usage,
  and aliases. The usage line documents the command's flags, and `st <command> -h`
  prints that same usage line — or, with `--json`, the same machine-readable
  object as `st help <command> --json` (aliases resolve identically). `st version`
  reports the build.
- `st help`, `st help <command>`, and `st version` accept `--json` and emit the
  same information as a machine-readable payload — the command list as
  `{ "commands": [ { "name", "summary", "usage", "aliases", "flags" }, … ] }`.
  Each `flags` entry is `{ "name", "type": "bool"|"string", "default", "summary" }`
  and lists *declared* flags, so `-m`, `--message`, and `--json` each appear
  separately; positionals remain described only by the prose `usage`.
- `st version --json` emits `{ "version", "commit", "built", "go" }` —
  `version` is the release version (an ldflags stamp wins, then the module
  version a `go install` recorded, then the compiled-in default); `commit` is
  the VCS revision (≤12 chars, with a ` (dirty)` suffix when the tree was
  dirty), `built` the VCS commit time, and `go` the toolchain version — all
  three `omitempty`, absent when the build carries no VCS metadata.
- `st guide` (or `st guide --json`) prints the recommended end-to-end
  workflow; `--json` emits `{ "steps": [ "…", … ], "docs": "…" }` where `steps`
  is the same annotated command list text mode prints and `docs` points at
  `st help <command>` for per-command usage.
