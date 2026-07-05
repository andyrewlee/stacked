# stacked

`stacked` is a minimal, **login-free** CLI for managing **stacked diffs** on top of
plain `git`. The CLI command is `st`.

A *stack* is a chain of small branches where each one is based on the branch below
it instead of all on `main`. This keeps each change small and reviewable while you
keep building on top. The hard part of stacking by hand is keeping every branch
rebased on its parent as the parent changes — `stacked` automates exactly that.

`stacked` is a thin, ergonomic layer over `git`:

- **No host API.** It never opens pull requests or calls a forge API. Remote Git
  commands still contact your configured remotes: `st sync` fetches and `st
  submit` pushes your branches; you open PRs yourself.
- **No third-party dependencies.** It is written in pure Go using only the standard
  library and shells out to your system `git`.
- **Local metadata only.** The stack topology (which branch's parent is which) is
  stored in a single JSON file inside your repo's `.git` directory.

## How the local metadata works

`stacked` stores all of its state in:

```
<your-repo>/.git/stacked/state.json
```

Because it lives inside `.git`, it is per-repo, never committed, and never pushed.
The file records the trunk branch and, for each tracked branch, its parent branch
and the parent commit SHA it was last rebased onto:

```json
{
  "trunk": "main",
  "branches": {
    "feat-a": {
      "name": "feat-a",
      "parent": "main",
      "parentSHA": "a1b2c3d4..."
    },
    "feat-b": {
      "name": "feat-b",
      "parent": "feat-a",
      "parentSHA": "e5f6a7b8..."
    }
  }
}
```

`parentSHA` is the key to restacking. A branch `B` stacked on parent `P` remembers
the `P` commit it was based on. When `P` advances (you add or amend a commit),
`B.parentSHA` no longer matches `P`'s tip, so `B` "needs restack". Restacking runs:

```
git rebase --onto <current tip of P> <B.parentSHA> <B>
```

and then updates `B.parentSHA` to `P`'s new tip. Any operation that moves a branch
tip (`modify`, `delete`, `sync`) automatically restacks that branch's *upstack*
(all of its descendants), always going parents-before-children and reading live
tips at each step. Commands that move `HEAD` around restore your original branch
when they finish.

The file is written atomically (temp file + rename) so an interrupted command
cannot corrupt your stack metadata.

## Install / build

Requires **Go 1.26+** and **Git 2.17+** on your `PATH`; **Git 2.31+** is
recommended for native common-dir path resolution, while older supported Git
versions use a fallback. The full gate (`make ci`) additionally needs
**golangci-lint v2** — an external binary, never a module dependency:
`go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.12.2`.

```sh
# install onto your PATH
go install ./cmd/st

# ...or build locally (Makefile stamps the version from git)
make build          # -> ./st
go build -o st ./cmd/st

# sanity check
st version          # version, commit, build time, go version
st --help
```

The main package lives at `./cmd/st`, so `go install` produces a binary named
`st`. The metadata lives under the repository's *common* git dir, so linked
worktrees of the same repo share one stack.

## For scripts and agents

`st` is built to be driven programmatically: it never prompts, JSON-capable
subcommands accept `--json`, and failures report stable exit codes (`2`
conflict, `3` not initialized, `4` dirty tree). See
**[docs/AGENT.md](docs/AGENT.md)** for the full machine interface (JSON schemas,
exit codes, idempotency).

## Contributing

The repo closes its own loop — `make ci` is fmt + strict lint + race tests + e2e +
a coverage gate, and the engine you'll touch most tests in milliseconds. See
**[CONTRIBUTING.md](CONTRIBUTING.md)** for the add-a-command recipe and
**[CLAUDE.md](CLAUDE.md)** for the architecture.

## Commands

Run `st help` (or `st <command> -h`) at any time. Flags may be placed
before or after the positional branch name.

Every command below except `completion` and `shell` (plus `help`/`version`) accepts `--json`; see docs/AGENT.md.

| Command | Aliases | Summary |
| --- | --- | --- |
| `st init [--trunk <name>]` | | Initialize stack tracking in this repo. |
| `st create <name> [-m|--message <msg>] [-a|--all]` | `c` | Create a new branch stacked on the current branch. |
| `st log [--json]` | `ls` | Show the stack as a tree (trunk at the bottom); `--json` for scripting. |
| `st status [--json]` | `stat` | Show the current branch, its parent/children, and restack state. |
| `st checkout [name]` | `co` | Check out a tracked branch, or list branches when no name is given. |
| `st up [n]` | `u` | Move up the stack to a child branch. |
| `st down [n]` | `d` | Move down the stack toward trunk. |
| `st top` | `t` | Jump to the top (leaf) of the current stack. |
| `st bottom` | `b` | Jump to the bottom branch (just above trunk). |
| `st track [--parent <branch>]` | | Start tracking the current git branch. |
| `st untrack [name]` | | Stop tracking a branch (re-parents its children). |
| `st modify [-m|--message <msg>] [-a|--all] [--commit]` | `amend`, `m` | Amend (or add) a commit, then restack everything above. |
| `st restack [--dry-run]` | `r` | Rebase the current branch and everything above it onto their parents (`--dry-run` previews). |
| `st continue` | | Resume a restack interrupted by a merge conflict. |
| `st abort` | | Abort an in-progress restack/rebase. |
| `st fold` | | Fold the current branch into its parent (parent absorbs its commits). |
| `st squash [-m|--message <msg>]` | | Squash all of the current branch's commits into one. |
| `st onto <target>` | `move` | Move the current branch (and its upstack) onto a new parent. |
| `st rename [old] <new>` | `mv` | Rename a branch and update the stack metadata. |
| `st delete <name> [-f|--force]` | `rm` | Delete a branch and re-parent its children. |
| `st sync [--no-delete] [--remote <name>] [--dry-run]` | `s` | Fetch trunk, fast-forward it, restack everything, prune merged branches (`--dry-run` previews). |
| `st submit [--remote <name>] [--dry-run]` | `ss` | Push the stack to the remote and print the repo URL (no PRs). |
| `st undo` | | Undo the last stack-mutating command. |
| `st validate` | `doctor` | Check the stack state for drift or inconsistencies. |
| `st repair` | | Reconcile the metadata with the repository (fix drift). |
| `st worktree <branch> \| ls \| rm <branch>` | `wt` | Materialize, list, or remove a branch's own worktree (for parallel work). |
| `st shell install [bash\|zsh\|fish]` | | Print the shell integration that teleports `cd` into a branch's worktree. |
| `st completion <bash\|zsh\|fish>` | | Print a shell completion script. |
| `st guide` | | Print the recommended workflow (handy for agents). |
| `st help` / `st version` | `-h`/`-v` | Show help / print the version. |

### Command details and examples

#### `st init [--trunk <name>]`
Creates `.git/stacked/state.json`. If `--trunk` is omitted, the trunk is detected
from `origin/HEAD`, then the current branch, then defaults to `main`.

```sh
st init                 # auto-detect trunk
st init --trunk main
```

#### `st create <name> [-m <msg>] [-a|--all]`
Creates `<name>` off the current branch, switches to it, and tracks it. `-a`
stages all changes first; `-m` commits the staged changes onto the new branch.

```sh
st create feat-a -m "add A"
```

#### `st log` (`ls`)
Renders the stack as a tree with the trunk at the bottom and each branch above its
parent. The current branch is marked `◉`, others `○`, drifted branches are tagged
`(needs restack)`, and each branch shows its top commit subject. In a
multi-worktree repo each branch that lives in a linked worktree is also tagged
with `(worktree: <path>)` (and `dirty` when that worktree has uncommitted
changes); `--json` adds matching `worktree`/`dirty` fields. Single-tree output is
unchanged.

#### `st status` (`stat`)
Prints the current branch's role (trunk / tracked / untracked), its parent and
children, whether it needs a restack, and whether the working tree is clean. In a
multi-worktree repo it also prints `worktree path:` for the current branch
(`worktree` in `--json`).

#### `st checkout [name]` (`co`)
Checks out a tracked branch (or the trunk). With no argument, lists the trunk and
all tracked branches, marking the current one with `*`. If the branch lives in its
own worktree (see `st worktree`), checkout *teleports* there instead of switching
in place — with the shell shim installed your shell `cd`s into the worktree; without
it the path is printed.

#### `st worktree <branch> | ls | rm <branch>` (`wt`)
Make a branch a "place you can be" on its own — useful for running multiple agents
on different branches of one stack in parallel. `st worktree <branch>` materializes
a git worktree for an existing tracked branch under `~/.stacked/worktrees/` using
a collision-resistant repo key and encoded branch segment (outside the repo, so
runners/linters never walk into it) and copies any
`.worktreeinclude` matches into it (gitignore syntax; only gitignored matches are
copied, via copy-on-write reflink when available). `st worktree ls` lists every
worktree; `st worktree rm <branch>` removes a branch's worktree. The stack metadata
is shared across all worktrees, so every `st` command sees the same stack.

#### `st shell install [bash|zsh|fish]`
Prints a tiny shell function that wraps `st` so navigation commands can change your
shell's directory (a CLI process cannot do that to its parent). Install it with, e.g.,
`eval "$(st shell install)"` in your shell rc. Without it, teleporting commands print
the destination path instead.

#### `st up [n]` (`u`) / `st down [n]` (`d`)
Walk `n` levels up (toward leaves) or down (toward trunk) and check out the result.
`up` stops at a branch point with multiple children and tells you to pick one.

#### `st top` (`t`) / `st bottom` (`b`)
Jump to the leaf of the current stack (`top`) or to the bottom branch just above
trunk (`bottom`).

#### `st track [--parent <branch>]` / `st untrack [name]`
`track` starts managing an existing git branch; the parent is inferred from the
commit graph or set with `--parent`. `untrack` stops managing a branch and
re-parents its children onto that branch's parent (the git branch is not deleted).

#### `st modify [-m <msg>] [-a|--all] [--commit]` (`amend`, `m`)
Amends the current branch's tip (or, with `--commit`, adds a new commit), then
restacks every descendant so the rest of the stack rebases onto the new tip. A
bare `st modify` stages all changes and amends without editing the message.

#### `st restack` (`r`)
Rebases the current branch onto its parent's current tip, then restacks its entire
upstack in topological order. From the trunk, restacks every tracked branch. Your
original branch is restored when done. Requires a clean working tree (commit or
stash first). If a rebase hits a conflict, resolve it, stage the files with
`git add`, then run `st continue`. When a dependent branch lives in its own
worktree (see `st worktree`), it is rebased *inside that worktree* — git won't
let it be rebased from here — as long as that worktree is clean; a dirty
dependent worktree is skipped with a note rather than clobbered.

#### `st continue`
Resumes a restack that stopped on a merge conflict. After you resolve the conflict
and `git add` the files, `st continue` completes the in-progress rebase, records
the branch's new base, and restacks the rest of the stack — picking up exactly
where it left off. If it hits another conflict, resolve and run `st continue`
again.

#### `st delete <name> [-f|--force]` (`rm`)
Deletes a tracked branch, re-parents its children onto the deleted branch's parent,
and restacks them. `-f` force-deletes an unmerged branch.

#### `st sync [--no-delete] [--remote <name>] [--dry-run]` (`s`)
Fetches the remote, fast-forwards the trunk, deletes branches already merged into
the trunk (re-parenting their children), restacks every remaining stack onto the
updated trunk, and restores your original branch. `--no-delete` keeps merged
branches; `--dry-run` previews the prune/restack plan without fetching or changing
anything.

#### `st submit [--remote <name>] [--dry-run]` (`ss`)
Pushes every branch on the current stack — from the bottom branch up to the
currently checked-out branch — using `--force-with-lease`, setting each branch's
upstream (`-u`). It is login-free and never opens PRs; it prints your repository's
URL so you can open pull requests on your host by hand.
`--dry-run` prints the plan without pushing. Most stack-mutating commands also accept
`--json` for machine-readable output.

#### `st abort`
Aborts an in-progress restack (`git rebase --abort`). Branches that already
restacked keep their new positions; the conflicted branch is rolled back and still
needs a restack.

#### `st fold`
Folds the current branch into its parent: the parent advances to include the
branch's commits, the branch is deleted, and its children are re-parented onto the
parent. The branch must be in sync first (`st restack` if needed).

#### `st squash [-m <msg>]`
Collapses every commit on the current branch (since its parent) into one, then
restacks its descendants. With no `-m`, the message is composed from the existing
commit subjects.

#### `st onto <target>` (`move`)
Re-parents the current branch onto `target` (the trunk or a tracked branch) and
rebases it and its descendants there. `target` may not be the branch itself or one
of its descendants. On conflict, resolve and run `st continue`.

#### `st rename [old] <new>` (`mv`)
Renames a branch (the current one by default) with `git branch -m` and updates the
stack metadata: the branch's record, the trunk name if applicable, and every
child's parent pointer.

#### `st undo`
Reverts the last stack-mutating command: the metadata is rolled back and each
recorded branch is reset to its prior tip. It does **not** touch your working
tree, so uncommitted changes are preserved (run `git status` to review). The
journal keeps the last several operations.

#### `st repair`
Fixes the drift `st validate` reports: untracks branches whose git branch was
deleted outside st, re-parents branches with an invalid parent onto the trunk, and
breaks parent cycles. Re-parented branches may then need `st restack`.

#### `st completion <bash|zsh|fish>`
Prints a shell completion script for st's subcommands, e.g.
`st completion zsh > "${fpath[1]}/_st"`.

#### `st validate` (`doctor`)
Checks the recorded stack against the actual repository and reports problems — a
missing trunk, tracked branches whose git branch was deleted outside `stacked`,
parents that are no longer the trunk or a tracked branch, and parent cycles — plus
warnings for branches that have drifted and need a restack. Exits non-zero when any
problem is found.

## A real stacked-diff workflow

```sh
# 0. one-time setup in your repo
st init

# 1. build the first change as its own small branch
echo 'A' > a.txt && git add -A
st create feat-a -m "add A"

# 2. stack a second change on top of the first
echo 'B' > b.txt && git add -A
st create feat-b -m "add B"

# 3. see the stack (trunk at the bottom, current branch marked ◉, colorized on a TTY)
st log
#     ◉ feat-b  add B
#   ○ feat-a  add A
# ○ main

# 4. drop down to revise the first branch
st down                       # now on feat-a

# 5. amend feat-a; feat-b is automatically restacked onto the new feat-a
echo 'A2' >> a.txt
st modify -m "add A (revised)"
# Amended feat-a with new message
# Restacked 1 branch(es):
#   feat-b

# 6. if anything ever drifts, fix the whole stack in one shot
st restack                    # no-op here: everything up to date

# 7. pull in trunk updates, fast-forward main, restack, prune merged branches
st sync

# 8. push the whole stack to the remote (no PRs are opened)
st submit                     # or: st submit --dry-run
```

## Notes & limitations

- `stacked` shells out to your system `git`; it is a convenience layer, not a
  reimplementation of git.
- The stack metadata is local to the repo (under the common git dir, so linked
  worktrees share it) and is not shared via the remote. A teammate cloning the
  repo starts with an empty stack until they `track` branches.
- Mutating commands take an advisory lock so two `st` invocations don't clobber
  each other's metadata: flock on unix-like platforms, an exclusive lock file
  elsewhere (e.g. Windows).
- On a rebase conflict during a restack, resolve it, `git add` the files, and run
  `st continue` (or `st abort` to back out). `stacked` records progress as each
  branch lands, then restacks the rest of the stack.
- `st undo` reverts the last mutating command's metadata and branch positions but
  does not modify your working tree.
- `stacked` deliberately opens no pull requests. After `st submit`, open the PRs
  it prints (or create them on your host yourself).
- **Not implemented:** `absorb` (auto-distributing staged hunks into the right
  ancestor commits) is intentionally left out — it is a sizable blame/fixup
  subsystem and is noted as a future addition rather than shipped half-done.
