# stacked

`stacked` is a sharp, zero-dependency CLI for managing **stacked diffs** on top of
plain `git`, with native GitHub stacked-PR support via `gh`. The CLI command is `st`.

A *stack* is a chain of small branches where each one is based on the branch below
it instead of all on `main`. This keeps each change small and reviewable while you
keep building on top. `stacked` automates rebasing your stack as lower branches
change, pruning merged branches, and keeping GitHub PR bases and stack links in sync.

`stacked` is a thin, ergonomic layer over `git` (and optionally `gh`):

- **Native GitHub stacked PRs via `gh`.** When `gh` is on your `PATH` and your
  remote is GitHub, `st submit` retargets open PR bases *before* pushing, pushes
  your stack with `--force-with-lease`, creates missing PRs bottom-up, and
  maintains an idempotent stack-navigation block in multi-PR stacks. `st sync -s`
  fetches, prunes merged PRs (including squash-merged PRs via GitHub + 3-way
  merge-tree), restacks survivors, retargets PRs, and force-pushes in one command.
  Pass `--no-pr` (or omit `gh`) for pure git push-only mode.
- **No third-party dependencies.** Written in pure Go using only the standard
  library, shelling out to your system `git` and `gh`.
- **Untracked-file safe.** Untracked files (agent plans, notes, scratch files)
  never block stack operations (`restack`, `sync`, `modify`, `delete`, `onto`,
  `squash`) and are never swept into commits (`st modify` defaults to staged
  changes; `-a` stages modified *tracked* files only, matching `git commit -a`).
- **Local metadata only.** The stack topology (which branch's parent is which) is
  stored in a single JSON file inside your repo's common `.git` directory.

## How the local metadata works

`stacked` stores all of its state under `.git/stacked/` in your repo — the
stack topology in `state.json`, plus the undo journal (`undo.json`) and the
lock files (`lock`, and on non-flock platforms `lock.excl`/`lock.reclaim`)
alongside it.

Because it lives inside `.git`, it is per-repo, never committed, and never pushed.
`state.json` records the trunk branch and, for each tracked branch, its parent branch
and the parent commit SHA it was last rebased onto:

```json
{
  "version": 1,
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

**One-line install** (no Go toolchain needed):

```sh
curl -fsSL https://raw.githubusercontent.com/andyrewlee/stacked/main/install.sh | sh
```

The installer detects OS/arch (darwin/linux, amd64/arm64), verifies the
release checksum, and installs `st` to `/usr/local/bin` (override with
`INSTALL_DIR`). Until release signing is provisioned it requires
`ST_ALLOW_UNVERIFIED=1` — it refuses unverifiable downloads by default.

**Prebuilt binaries**: every release publishes
darwin/linux tarballs for amd64 and arm64 plus `checksums.txt` on the
[releases page](https://github.com/andyrewlee/stacked/releases) — download
`stacked_<version>_<os>_<arch>.tar.gz`, verify the checksum, and put the `st`
binary on your `PATH`.

Building from source requires **Go 1.26+** and **Git 2.17+** on your `PATH`; **Git 2.31+** is
recommended for native common-dir path resolution, while older supported Git
versions use a fallback. On **Git <2.26** the default rebase backend drops
commits that started empty (`git commit --allow-empty`) during a restack
cascade; Git 2.26+ preserves them. The full gate (`make ci`) additionally needs
**golangci-lint v2** — an external binary, never a module dependency:
`go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.12.2`.

```sh
# install the latest release onto your PATH (no clone needed)
go install github.com/andyrewlee/stacked/cmd/st@latest

# ...or install from a clone
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
subcommands accept `--json`, and failures report stable exit codes (`1`
usage, `2` conflict, `3` not initialized, `4` dirty tree, `5` lock held,
`70` internal). See
**[docs/AGENT.md](docs/AGENT.md)** for the full machine interface (JSON schemas,
exit codes, idempotency).

## Contributing

The repo closes its own loop — `make ci` is fmt + strict lint + race tests + e2e +
a coverage gate, and the engine you'll touch most tests in milliseconds. See
**[CONTRIBUTING.md](CONTRIBUTING.md)** for the add-a-command recipe and
**[CLAUDE.md](CLAUDE.md)** for the architecture.

## Commands

Run `st help` (or `st <command> -h`) at any time. Flags may be placed
before or after a positional branch name.

Every command below except `__complete` and `completion` (plus `help`/`version`) accepts `--json`; see docs/AGENT.md.

| Command | Aliases | Summary |
| --- | --- | --- |
| `st init [--trunk <name>]` | | Initialize stack tracking in this repo. |
| `st create <name> [-m|--message <msg>] [-a|--all] [--dry-run]` | `c` | Create a new branch stacked on the current branch (`--dry-run` previews). |
| `st log [--all] [--pr] [--json]` | `ls` | Show the current stack as a tree (`--all` for whole forest, `--pr` for GitHub PR status). |
| `st status [--pr] [--json]` | `stat` | Show the current branch, its parent/children, commits, trunk lag, and restack state (`--pr` for GitHub PR). |
| `st checkout [name]` | `co` | Check out a tracked branch, or list branches when no name is given. |
| `st up [n]` | `u` | Move up the stack to a child branch. |
| `st down [n]` | `d` | Move down the stack toward trunk. |
| `st top` | `t` | Jump to the top (leaf) of the current stack. |
| `st bottom` | `b` | Jump to the bottom branch (just above trunk). |
| `st track [name] [--parent <branch>] [--all] [--dry-run]` | | Start tracking a git branch (the current one when no name is given), or every untracked branch with `--all` (`--dry-run` previews). |
| `st untrack [name] [--dry-run]` | | Stop tracking a branch (re-parents its children; `--dry-run` previews). |
| `st modify [-m|--message <msg>] [-a|--all] [--commit] [--dry-run]` | `amend`, `m` | Amend (or add) a commit from staged changes (`-a` stages modified tracked files), then restack upstack. |
| `st restack [--all] [--dry-run]` | `r` | Rebase the current branch and everything above it onto their parents (`--all` restacks the whole forest; `--dry-run` previews). |
| `st continue` | | Resume a restack interrupted by a merge conflict. |
| `st abort` | | Abort an in-progress restack/rebase. |
| `st squash [-m|--message <msg>] [--dry-run]` | | Squash all of the current branch's commits into one (`--dry-run` previews). |
| `st onto <target> [--dry-run]` | `move` | Move the current branch (and its upstack) onto a new parent (`--dry-run` previews). |
| `st rename [old] <new> [--dry-run]` | `mv` | Rename a branch and update the stack metadata (`--dry-run` previews). |
| `st delete <name> [-f|--force] [--dry-run]` | `rm` | Delete a branch (merged or squash-merged, or `-f` to force) and re-parent its children (`--dry-run` previews). |
| `st sync [-s|--submit] [--all] [--no-delete] [--no-fetch] [--remote <name>] [--dry-run]` | `s` | Fetch trunk, fast-forward it, prune merged branches, and restack (`-s`/`--submit` pushes and updates PRs after). |
| `st submit [--draft] [--no-pr] [--title <t>] [--body <b>] [--upto] [--all] [--remote <name>] [--dry-run]` | `ss` | Push the current stack and create/retarget/update GitHub PRs (`--no-pr` for push-only; `--upto`/`--all` control scope). |
| `st undo [--force]` | | Undo the last stack-mutating command (`--force` restores refs that moved outside st). |
| `st doctor [--fix] [--all]` | `validate` | Check the stack state for drift or inconsistencies (`--fix` repairs metadata; `--all` checks drift across all stacks). |
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

#### `st create <name> [-m <msg>] [-a|--all] [--dry-run]`
Creates `<name>` off the current branch, switches to it, and tracks it. `-a`
stages modified tracked changes first (matching `git commit -a`; untracked files
must still be staged with `git add`); `-m` commits the staged changes onto the
new branch (`-a` requires `-m`). `--dry-run` previews without changing anything.

```sh
git add a.txt && st create feat-a -m "add A"
```

#### `st log [--all] [--pr]` (`ls`)
Renders the current stack as a tree with the trunk at the bottom and each branch
above its parent (on trunk, detached HEAD, or with `--all`, shows the whole
forest). The current branch is marked `◉`, others `○`, drifted branches are tagged
`(needs restack)`, trunk shows `(N behind origin/<trunk>)` from cached remote refs
with zero network calls, and each branch shows its top commit subject. With `--pr`,
open GitHub PRs are annotated with number, draft badge, CI check status
(`✓`/`✗`/`⋯`), and base mismatch warnings. In a multi-worktree repo, branches
checked out in *other* linked worktrees are annotated with `(worktree: <path>)`
(the caller's own worktree path is omitted to keep output clean).

#### `st status [--pr]` (`stat`)
Prints the current branch's role (trunk / tracked / untracked), its parent and
children, whether it needs a restack, whether local trunk is behind `origin/<trunk>`,
and whether the working tree is clean (ignoring untracked files). `--json` also
includes `commits` (the branch's `parentSHA..tip` commits) and `trunkBehind`.
With `--pr`, attaches open GitHub PR metadata.

#### `st checkout [name]` (`co`)
Checks out a tracked branch (or the trunk). With no argument, lists the trunk and
all tracked branches, marking the current one with `*`. If the branch is checked
out in another git worktree, checkout refuses cleanly naming that worktree path.

#### `st up [n]` (`u`) / `st down [n]` (`d`) / `st top` (`t`) / `st bottom` (`b`)
Navigate the stack: `up`/`down` step `n` levels; `top` jumps to the leaf; `bottom`
jumps to the lowest branch above trunk.

#### `st track [name] [--parent <branch>] [--all] [--dry-run]` / `st untrack [name] [--dry-run]`
`track` adopts an existing git branch (inferring its parent from the commit graph
or using `--parent`; `--all` adopts every untracked local branch in one pass).
`untrack` stops managing a branch and re-parents its children onto its parent
without deleting the git branch.

#### `st modify [-m <msg>] [-a|--all] [--commit] [--dry-run]` (`amend`, `m`)
Amends the current branch's tip (or, with `--commit`, adds a new commit) using
**staged changes** by default — untracked files are never swept in. Pass `-a` to
stage modified tracked files (like `git commit -a`). Automatically restacks every
descendant onto the new tip.

#### `st restack [--all] [--dry-run]` (`r`)
Rebases the current branch onto its parent's current tip, then restacks its entire
upstack in topological order. From the trunk — or with `--all` from anywhere —
restacks every tracked branch in the forest. Untracked files do not block restack;
only tracked staged/unstaged edits do. When a dependent branch lives in another
clean linked git worktree, it is rebased inside that worktree; dirty or locked
(`git worktree lock`) dependent worktrees are skipped with a note rather than
clobbered. On conflict, resolve, `git add`, and run `st continue` (or `st abort`).

#### `st delete <name> [-f|--force] [--dry-run]` (`rm`)
Deletes a tracked branch, re-parents its children onto the deleted branch's parent,
and restacks them. Without `-f`, branches already merged or squash-merged into
their stack parent are allowed; `-f` force-deletes an unmerged branch. Never
deletes another git worktree on disk (refuses if the branch is checked out in
another worktree).

#### `st sync [-s|--submit] [--all] [--no-delete] [--no-fetch] [--remote <name>] [--dry-run]` (`s`)
Fetches the remote, fast-forwards the trunk, deletes branches already merged into
the trunk (re-parenting their children), restacks surviving branches onto the
updated trunk, and restores your original branch — or switches to the single
surviving child when the current branch itself was pruned. Scopes to the current
stack when on a tracked branch (pass `--all` or run from trunk for the whole
forest).

A branch counts as merged when:
- Its commits are ancestors of trunk, OR
- Its entire diff is already contained in trunk's tree (3-way `git merge-tree --write-tree`
  on Git 2.38+, with 2-way tree diff fallback on older Git), OR
- On GitHub remotes with `gh` available, its PR is marked `MERGED` on GitHub.

Pass `-s` / `--submit` to immediately retarget surviving PRs, force-push restacked
branches, and refresh PR stack blocks right after syncing.

#### `st submit [--draft] [--no-pr] [--title <t>] [--body <b>] [--upto] [--all] [--remote <name>] [--dry-run]` (`ss`)
Pushes every branch in the current stack (`bottom..top`; `--upto` stops at current;
`--all` pushes the whole forest) in dependency order using `--force-with-lease`,
setting upstream (`-u`).

When the remote is GitHub and `gh` is on `PATH` (unless `--no-pr` is passed):
1. **Retargets open PR bases before pushing** so GitHub never auto-closes child
   PRs when a parent merges or moves.
2. **Force-pushes** every branch in the scope.
3. **Creates missing PRs bottom-up** (defaulting title/body from the branch's
   top commit; override the current branch with `--title`/`--body`, or open as
   drafts with `--draft`).
4. **Updates stack links idempotently** via `<!-- st:stack:start -->`...`<!-- st:stack:end -->`
   markers while preserving author prose outside the block (omitted on single-PR
   stacks unless already present).

#### `st squash [-m <msg>] [--dry-run]` / `st onto <target> [--dry-run]` (`move`) / `st rename [old] <new> [--dry-run]` (`mv`)
- `squash`: collapses all commits on the current branch into one, then restacks descendants.
- `onto`: re-parents the current branch onto `target` and rebases it and its descendants.
- `rename`: renames a branch (`git branch -m`) and updates stack metadata and child pointers.

#### `st undo [--force]`
Reverts the last stack-mutating command: rolls back metadata and resets recorded
branches to their prior tips without touching your working tree. Refuses if a
branch moved outside `st` since the command ran unless `--force` is passed.

#### `st doctor [--fix] [--all]` (`validate`)
Checks the recorded stack against git and reports problems (missing trunk/branches,
invalid parents, cycles, stale pending reparent/continuation) plus drift warnings
for the current stack (or all stacks with `--all`). Exits non-zero when any
problem is found. Pass `--fix` to repair metadata inconsistencies automatically.

#### `st completion <bash|zsh|fish>`
Prints a shell completion script for `st` subcommands and dynamic branch completion
via the hidden `st __complete` endpoint.

## A real stacked-diff workflow

```sh
# 0. one-time setup in your repo
st init

# 1. build the first change as its own small branch
echo 'A' > a.txt && git add a.txt
st create feat-a -m "add A"

# 2. stack a second change on top of the first
echo 'B' > b.txt && git add b.txt
st create feat-b -m "add B"

# 3. inspect the stack (with optional GitHub PR & CI status)
st log --pr

# 4. push the stack and create stacked GitHub PRs with stack navigation links
st submit

# 5. drop down to revise the first branch; feat-b auto-restacks
st down
echo 'A2' >> a.txt && git add a.txt
st modify -m "add A (revised)"
st submit

# 6. once PR #1 merges on GitHub: fetch, prune feat-a, restack feat-b onto main,
#    retarget PR #2 -> main, force-push, and refresh stack links in one shot
st sync --submit
```

## Notes & limitations

- `stacked` shells out to your system `git` (and `gh` for GitHub PR operations);
  it is a sharp layer, not a reimplementation of git or a separate login flow.
- Stack metadata is local to the repo (under the common git dir, so externally
  managed `git worktree`s share it). `st` never creates or deletes worktrees on
  disk.
- Mutating commands — plus HEAD-moving navigation (`checkout <branch>`,
  `up`/`down`/`top`/`bottom`) and `init` — take an advisory lock so concurrent
  `st` invocations don't clobber metadata or move HEAD mid-mutation.
- On a rebase conflict during a restack, resolve it, `git add` the files, and run
  `st continue` (or `st abort` to back out).
