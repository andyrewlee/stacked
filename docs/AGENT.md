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
| 70 | internal error (a bug in `st`) | not self-recoverable; report it |

## JSON output

Every subcommand except `completion` accepts `--json`. The built-ins `help`,
`-h`/`--help`, `version`, and `-v`/`--version` also accept `--json`. Successful
JSON output is a single indented object on **stdout**. On failure, `--json`
writes a structured envelope to **stderr** and the process still exits with the
code above:

```json
{ "error": { "code": "conflict", "message": "rebasing \"feat-b\" ... : st continue" } }
```

`error.code` is one of: `error` (1), `conflict` (2), `not_initialized` (3),
`dirty` (4), `internal` (70, a recovered panic). So an agent can read stdout for
the result, stderr for the error envelope, and the exit code for the category.

### Result shapes

- **Stack-mutating commands** (`create`, `modify`, `restack`, `fold`, `squash`,
  `onto`, `delete`, `track`, `untrack`, `rename`) share one shape:
  ```json
  { "summary": "...", "branch": "feat-b", "restacked": ["feat-c"] }
  ```
  `branch`, `restacked`, `deleted`, `notes`, and `dryRun` are all `omitempty` —
  absent when empty or false. Preview-capable commands (`restack`, `sync`) add
  `"dryRun": true` under `--dry-run`.
- **`log --json`** — a recursive tree rooted at the trunk:
  ```json
  { "name": "main", "current": false, "needsRestack": false,
    "children": [ { "name": "feat-a", "parent": "main", "parentSHA": "…",
                    "current": true, "needsRestack": false, "topCommit": "add a", "children": [] } ] }
  ```
- **`status --json`** — `{ "branch", "role", "children": [], "worktreeClean": bool }`; `parent` is present for tracked branches, and `needsRestack` is present only when it applies.
- **`checkout --json`** — with a name, `{ "branch", "switched": bool }`; with no
  name, `{ "trunk", "current", "branches": [] }`
- **`validate --json`** — `{ "ok": bool, "tracked": n, "problems": [], "warnings": [] }` (exit 1 if problems)
- **Navigation** (`up`/`down`/`top`/`bottom`) — `{ "branch", "summary" }` (`up` adds `children` at a branch point)
- **`submit --json`** — one shape for every outcome:
  `{ "remote", "dryRun", "pushed": [], "repoURL", "summary" }` (`repoURL` and
  `summary` are `omitempty`; from trunk, `pushed` is empty and `summary`
  explains why).
- **operational** (`init`, `abort`, `undo`, `repair`) — small `{ ... }` objects, see `st help <cmd>`.

## Idempotency & safety

- `restack`, `sync`, `validate`, `log`, `status` are safe to re-run.
- `st restack --dry-run` previews the branches that *would* be rebased.
  `st sync --dry-run` previews prune/restack work without fetching, using the
  current local trunk or already-cached `refs/remotes/<remote>/<trunk>`. Both
  return a `{"dryRun": true, ...}` result without changing stack metadata or
  branch refs.
- `restack` requires a clean tree (exit 4 otherwise) and is idempotent once the
  stack is in sync.
- `undo` reverts the last mutating command's metadata and branch tips; it does not
  touch the working tree.
- Concurrent `st` processes in one repo are serialized by an advisory lock (a
  second one fails fast rather than corrupting state).

## Discoverability

- `st --help` lists every command; `st help <command>` prints its summary, usage,
  and aliases. The usage line documents the command's flags, and `st <command> -h`
  prints that same usage line. `st version` reports the build.
- `st help`, `st help <command>`, and `st version` accept `--json` and emit the
  same information as a machine-readable payload — the command list as
  `{ "commands": [ { "name", "summary", "usage", "aliases" }, … ] }`.
- `st guide` (or `st guide --json`) prints the recommended end-to-end workflow.
