# Driving `st` from an agent

`st` is built to be driven programmatically. Local stack operations do not prompt
for input, JSON-capable commands emit machine-readable JSON on request, and all
commands report outcomes through stable exit codes.

## No prompts & untracked-file safety

- Local stack operations never read from stdin or open an interactive editor.
  `git rebase --onto` never invokes an editor, and `st continue` pins
  `GIT_EDITOR=true`/`GIT_SEQUENCE_EDITOR=true`; when a rebase stops on a
  conflict, `st` returns control to you (exit code 2) rather than blocking —
  resolve the files, `git add` them, and run `st continue` (or `st abort`).
- **Untracked files never block stack operations.** Clean-tree checks (`sync`,
  `restack`, `delete`, `onto`, `squash`) ignore untracked files (`git status
  --porcelain --untracked-files=no`), and `-a`/`--all` on `create`/`modify`
  stages tracked modifications and deletions only (`git add -u`). Local plans,
  scratch scripts, and untracked notes stay untouched unless you explicitly
  `git add` them.
- `st modify` amends **staged changes by default** (`-a` is opt-in, default
  `false`), matching `git commit --amend`.
- Version note: on Git <2.26 the default rebase backend drops commits that
  started empty (`git commit --allow-empty`) during a restack cascade; Git 2.26+
  preserves them.

Remote Git operations use your configured Git transport, and native GitHub PR
operations shell out to the `gh` CLI when the remote is a GitHub repository and
`gh` is on `PATH` (pass `--no-pr` to `st submit` for push-only mode, or
`--no-fetch` to `st sync` for offline local sync).

## Exit codes

Branch on the exit code; do not parse messages.

| code | meaning | typical recovery |
|---|---|---|
| 0 | success | — |
| 1 | usage error / generic failure | fix the invocation |
| 2 | rebase conflict in progress | resolve + `git add`, then `st continue` (or `st abort`) |
| 3 | repo not initialized | run `st init` |
| 4 | working tree has tracked uncommitted changes | commit or stash, then retry |
| 5 | another `st` command holds the repo lock, **or** an abandoned `lock.reclaim` guard needs an operator | `"locked"`: retry once it finishes; `"locked_guard"`: stop — a human must remove the guard file (see CONTRIBUTING) |
| 70 | internal error (a bug in `st`) | not self-recoverable; report it |

## JSON output

Every subcommand except `__complete` and `completion` accepts `--json`
(`__complete` is the hidden completion endpoint, not a user command). The
built-ins `help`, `-h`/`--help`, `version`, and `-v`/`--version` also accept
`--json`. Successful JSON output is a single indented JSON object on **stdout**.
On failure, `--json` writes a structured envelope to **stderr** and the process
still exits with the code above:

```json
{ "error": { "code": "conflict",
             "message": "rebasing \"feat-b\" onto \"feat-a\": rebase conflict …",
             "branch": "feat-b", "onto": "feat-a" } }
```

`error.code` is one of: `error` (1), `paused` (1 — a tracked branch's rebase is
paused in ANOTHER worktree; the envelope also carries `branch` and `worktree`,
the path where `st continue`/`st abort` must run), `conflict` (2),
`not_initialized` (3), `dirty` (4), `locked` (5), `locked_guard` (5 — an
abandoned `lock.reclaim` guard; operator action required, do not retry),
`internal` (70, a recovered panic). On a `conflict` the envelope also carries
`branch` (the branch whose rebase stopped) and `onto` (its parent).

### Result shapes

- **Stack-mutating commands** (`create`, `modify`, `restack`, `continue`,
  `squash`, `onto`, `delete`, `track`, `untrack`, `rename`, `sync`) share one
  shape:
  ```json
  { "summary": "...", "branch": "feat-b", "restacked": ["feat-c"] }
  ```
  `branch`, `restacked`, `deleted`, `reparented`, `notes`, `tracked`, and
  `dryRun` are `omitempty`. `reparented` lists `[{ "branch", "oldParent",
  "newParent" }]` for every child whose stack parent changed (`delete`, `sync`,
  and their `--dry-run` previews). `track --all` adds `"tracked": {"name":
  "parent"}` and `notes` entries for skipped branches (e.g. orphans). Preview
  runs (`--dry-run`) return the same shape with `"dryRun": true` and perform no
  writes, no lock, and no undo entry.
- **`sync --submit --json`** (`-s`) — runs `sync` (fetching trunk, querying
  recently merged GitHub PRs via `gh` when on a GitHub remote so squash-merged
  PRs are pruned reliably, pruning merged branches, advancing HEAD to a single
  surviving child, and restacking) and immediately submits the surviving stack
  (retargeting child PR bases before pushing, force-pushing with lease, and
  refreshing managed stack blocks). Emits `summary`, `branch`, `restacked`,
  `deleted`, `reparented`, `notes`, `pushed`, and `prs`.
- **`submit --json`** — pushes the full current stack bottom→top (`--upto` stops
  at current branch; `--all` pushes the whole forest). On GitHub remotes with
  `gh` available (unless `--no-pr`), `submit` automatically:
  1. Retargets open PRs whose GitHub base differs from their local stack parent
     **before pushing** (preventing GitHub from auto-closing child PRs).
  2. Force-pushes branches with `--force-with-lease`.
  3. Creates missing PRs bottom-up (honoring `--draft`, `--title`, `--body`) and
     idempotently maintains a `<!-- st:stack:start -->`…`<!-- st:stack:end -->`
     stack navigation block inside multi-PR stacks while preserving author prose.

  One shape for every outcome:
  `{ "remote", "dryRun", "pushed": [], "prs": [], "repoURL", "prHints": [], "notes": [], "published", "summary", "failed" }`
  (`prs`, `repoURL`, `prHints`, `notes`, `published`, `summary`, and `failed`
  are `omitempty`). Each `prs` element has `{ "branch", "base", "number", "url",
  "draft", "action" }` (`action` is one of `created`, `retargeted`, `updated`,
  `unchanged`, `would_create`, `would_retarget`, `would_update`). Under
  `--dry-run`, `published` maps each branch to its state against the remote's
  local tracking ref (`current`, `stale`, `diverged`, `missing`, `unknown`). On
  partial push failure, confirmed `pushed` branches and the first rejected
  `failed` branch are emitted on stdout alongside the stderr error envelope.
- **`log --json`** — a recursive tree rooted at the trunk. By default when on a
  tracked branch, `log` scopes to the **current stack**; on trunk, detached
  HEAD, or with `--all`, it renders the whole forest.
  ```json
  { "name": "main", "tip": "…", "trunkBehind": 2, "current": false,
    "needsRestack": false,
    "children": [ { "name": "feat-a", "parent": "main", "parentSHA": "…",
                    "tip": "…", "current": true, "needsRestack": false,
                    "topCommit": "add a",
                    "pr": { "number": 35, "url": "…", "state": "OPEN",
                            "draft": false, "base": "main",
                            "baseMismatch": false, "checks": "passing" },
                    "children": [] } ] }
  ```
  `tip` is the live tip SHA; `trunkBehind` (`omitempty`, on root) reports how
  many commits local trunk is behind `origin/<trunk>` without hitting the
  network; `pr` (`omitempty`) is populated when `--pr` is passed; `worktree`,
  `dirty`, `paused`, `conflictedFiles`, and `locked` (`omitempty`) annotate
  branches checked out in other linked worktrees (the caller's own worktree is
  omitted unless paused mid-rebase); `unreachable` (`omitempty`, on root) lists
  corrupt branches not reachable from trunk.
- **`status --json`** — `{ "branch", "trunk", "trunkBehind", "role", "parent",
  "tip", "published", "children": [], "commits": [ { "sha", "subject" } ],
  "needsRestack", "worktreeClean": bool, "rebaseInProgress", "rebaseBranch",
  "conflictedFiles", "worktree", "pr" }` (`trunkBehind`, `parent`, `tip`,
  `published`, `commits`, `needsRestack`, `rebaseInProgress`, `rebaseBranch`,
  `conflictedFiles`, `worktree`, and `pr` are `omitempty`). `commits` lists the
  current tracked branch's commits in `parentSHA..HEAD` newest-first. `--pr`
  populates `pr` via `gh`.
- **`checkout --json`** — with a name, `{ "branch", "switched": bool }`; with no
  name, `{ "trunk", "current", "branches": [] }`. Refuses clearly if the target
  branch is checked out in another worktree.
- **`doctor --json`** (alias `validate`) — without `--fix`:
  `{ "ok": bool, "tracked": n, "problems": [], "warnings": [] }` (exit 1 if
  problems; drift warnings scope to the current stack unless `--all`). With
  `--fix`: `{ "repaired": bool, "fixes": [] }`.
- **Navigation** (`up`/`down`/`top`/`bottom`) — `{ "branch", "summary" }` (`up`
  adds `children` at a branch point).
- **`init --json`** — `{ "trunk", "initialized": bool, "alreadyInitialized": bool }`.
- **`abort --json`** — `{ "aborted": true, "summary" }`.
- **`undo --json`** — reverts the last stack-mutating command:
  `{ "undone": true, "label", "restored": [], "notes": [] }` (`notes` is
  `omitempty`). An empty journal emits `{ "undone": false }` (exit 0). Refuses
  if a recorded branch moved outside `st` or has a paused rebase unless
  `--force` is passed.

## Idempotency & safety

- `restack`, `sync`, `doctor`, `log`, `status`, and `submit` are safe to re-run.
- `st` **never creates or deletes git worktrees on disk**. It is fully
  multi-worktree aware: restacking a stack rebases clean linked-worktree
  descendants in place, skips dirty/locked/stale linked worktrees with a note,
  and refuses to delete branches currently checked out in another worktree.
- Mutating commands, HEAD-moving navigation (`checkout <branch>`, `up`, `down`,
  `top`, `bottom`), `submit`, and `init` are serialized across `st` processes by
  an advisory lock (exit 5 / `"locked"`, configurable via `ST_LOCK_WAIT`).
  Read-only commands (`status`, `log`, `doctor`, bare `st checkout`, and all
  `--dry-run` previews) bypass the lock.
- Only commands routed through `mutateState` (`create`, `modify`, `squash`,
  `onto`, `delete`, `rename`, `track`, `untrack`, `restack`, `doctor --fix`,
  and `sync`'s local mutations) record an undo entry.

## Discoverability

- `st --help` lists every command; `st help <command>` prints its summary,
  usage, and aliases. `st <command> -h --json` emits the same object as
  `st help <command> --json`.
- `st help --json` emits `{ "commands": [ { "name", "summary", "usage",
  "aliases", "flags", "subVerbs" }, … ] }`, where each `flags` entry is
  `{ "name", "type": "bool"|"string", "default", "summary" }`.
- `st version --json` emits `{ "version", "commit", "built", "go" }`.
- `st guide --json` emits `{ "steps": [ "…" ], "docs": "…" }`.
- `ST_DEBUG=1 st <cmd>` logs every git subprocess spawn to stderr.

### Internal: `st __complete`

`st __complete <command-or-alias> <index> -- <prior words...>` is the hidden,
read-only completion endpoint invoked by generated shell scripts
(`st completion <bash|zsh|fish>`). Candidate policy: `checkout`/`co` → trunk +
tracked branches; `onto`/`move` → tracked + trunk minus the current branch's
subtree; `track` → untracked local branches (or tracked + trunk after
`--parent`); `delete` and `untrack` → tracked branches; `rename` → tracked +
trunk on the first positional. `ST_COMPLETE_BIN` overrides the binary invoked by
generated hooks.
