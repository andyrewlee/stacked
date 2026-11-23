# stacked

**Sharp, zero-dependency stacked diffs for Git and GitHub — built for humans and coding agents.**

`st` turns stacked branches into a fast, predictable loop: break large changes into small reviewable PRs, amend lower branches without hand-rebasing the chain, and let `st` keep your GitHub PR bases, stack links, and merged branches in sync.

```sh
# 1. Build a 2-PR stack
git add auth.go && st create feat-auth -m "Add token validator"
git add api.go  && st create feat-api  -m "Wire auth into /v1 endpoints"

# 2. Push & open stacked GitHub PRs (with auto-maintained stack links)
st submit
# pushed feat-auth
# pushed feat-api
#
# pull requests:
#   #41 feat-auth -> main (created)       https://github.com/org/repo/pull/41
#   #42 feat-api -> feat-auth (created)   https://github.com/org/repo/pull/42

# 3. See stack + live PR & CI status
st log --pr
#     ◉ feat-api [#42 ✓]  Wire auth into /v1 endpoints
#   ○ feat-auth [#41 ✓]  Add token validator
# ○ main

# 4. Address review on PR #41 — PR #42 restacks automatically
st down
git add auth.go && st modify && st submit

# 5. PR #41 merges on GitHub — prune, advance HEAD, restack, retarget PR #42 -> main, and push
st sync --submit
```

---

## Why `st`?

- **Native GitHub stacked PRs via `gh` — no extra account or hosted service.**
  On GitHub remotes with `gh` installed, `st submit` retargets open PR bases *before* pushing (so GitHub never auto-closes child PRs), force-pushes with `--force-with-lease`, creates missing PRs bottom-up, and maintains a clean navigation block in multi-PR stacks without touching your PR description. Pass `--no-pr` (or skip `gh`) for push-only Git mode.
- **One-command post-merge drill (`st sync --submit`).**
  Fetches trunk, detects merged and squash-merged PRs (via GitHub PR state *and* 3-way `git merge-tree`), prunes the merged branch, moves you to the next branch in the stack, restacks descendants, retargets the new bottom PR to `main`, and force-pushes — all in one shot.
- **Built for coding agents & automation.**
  Zero interactive prompts, structured `--json` output across the CLI, stable exit codes (`2` conflict, `3` uninitialized, `4` dirty tree, `5` lock held), advisory locking for concurrent runs, and `st guide` for instant self-orientation.
- **Untracked-file safe.**
  Untracked scratch files (`plans/`, notes, local logs) never block `restack`, `sync`, `modify`, `delete`, `onto`, or `squash`, and never leak into commits. `st modify` defaults to **staged changes only**; `-a` stages modified *tracked* files only (matching `git commit -a`).
- **Plain Git underneath, zero dependencies.**
  Written in pure Go using only the standard library. All stack metadata lives locally in `.git/stacked/state.json` — never committed, never pushed, and automatically shared across `git worktree`s (without `st` ever creating or deleting worktrees on disk).

---

## Install

**One-line installer** (macOS & Linux, amd64/arm64 — verifies minisign signature & SHA-256 checksums):

```sh
curl -fsSL https://raw.githubusercontent.com/andyrewlee/stacked/main/install.sh | sh
```

Installs `st` to `/usr/local/bin` (override with `INSTALL_DIR`). Releases are signed with `minisign` (public key in [stacked.pub](stacked.pub)); if `minisign` isn't installed locally, pass `ST_ALLOW_UNVERIFIED=1` to fall back to checksum-only verification.

**With Go:**

```sh
go install github.com/andyrewlee/stacked/cmd/st@latest
```

**Prebuilt archives:** download `stacked_<version>_<os>_<arch>.tar.gz` and `checksums.txt` from the [releases page](https://github.com/andyrewlee/stacked/releases).

**From source:** requires **Go 1.26+** and **Git 2.17+** on your `PATH` (**Git 2.31+** recommended for native common-dir path resolution; **Git 2.26+** preserves empty commits during restack). Optional native GitHub PR automation uses your existing authenticated [`gh`](https://cli.github.com/) CLI. Running the full `make ci` gate also needs `golangci-lint` v2: `go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.12.2`.

```sh
git clone https://github.com/andyrewlee/stacked.git
cd stacked
make build          # produces ./st with version stamped from git
./st version
```

---

## The Stacked-PR Workflow

### 1. Initialize once per repo

```sh
st init                 # auto-detects trunk (origin/HEAD -> current branch -> main)
st init --trunk main    # or specify explicitly
```

Already have local branches? Adopt them without recreating anything:

```sh
st track                # track current branch (infers parent from commit graph)
st track --all          # adopt every untracked local branch in one pass
```

### 2. Stack small branches as you go

```sh
# Branch 1 on main
git add schema.sql && st create db-schema -m "Add users table"

# Branch 2 stacked on db-schema
git add store.go   && st create user-store -m "Add UserStore queries"

# Branch 3 stacked on user-store
git add handler.go && st create user-api   -m "Expose /users endpoint"
```

### 3. Submit the whole stack to GitHub

```sh
st submit               # push bottom..top, create/retarget PRs, update stack links
st submit --draft       # open newly created PRs as drafts
st submit --upto        # stop at current branch (omit upstack descendants)
st submit --no-pr       # push branches only; skip GitHub PR management
```

For multi-PR stacks, `st submit` appends an idempotent stack block to each PR description while keeping all of your own prose outside the markers untouched:

```markdown
<!-- st:stack:start -->
**Stack:**
- #41 (`db-schema` → `main`)
- 👉 **#42** (`user-store` → `db-schema`) *(this PR)*
- #43 (`user-api` → `user-store`)

<sub>Managed by `st`. Merge bottom-up.</sub>
<!-- st:stack:end -->
```

Single-PR stacks stay completely clean (no stack block added unless one already exists).

### 4. Revise lower PRs without manual rebasing

```sh
st bottom               # jump to db-schema (or st down / st checkout db-schema)
# ...edit schema.sql...
git add schema.sql
st modify               # amends db-schema, then restacks user-store & user-api
st submit               # force-pushes updated stack & keeps PRs in sync
```

Need a new commit instead of amending? Use `st modify --commit -m "Handle null email"`. Want to collapse a messy branch back into one clean commit? Run `st squash -m "Add users table"`.

### 5. After the bottom PR merges

```sh
st sync --submit        # or shorthand: st sync -s
```

In one command, `st`:
1. Fetches and fast-forwards `main`.
2. Detects merged PRs — via Git ancestry, 3-way `git merge-tree` squash-merge detection, *and* GitHub PR state.
3. Deletes `db-schema`, re-parents `user-store` onto `main`, and switches `HEAD` to `user-store`.
4. Restacks `user-store` and `user-api` onto the new `main`.
5. Retargets PR #42's GitHub base from `db-schema` → `main` **before** pushing, force-pushes the updated stack, and refreshes the stack links on #42 and #43.

### 6. Check status at a glance

```sh
st log                  # current stack only (zero network, shows trunk lag)
st log --all            # all tracked stacks in the repo
st log --pr             # add PR #number, draft, CI checks (✓/✗/⋯), base mismatches
st status --pr          # current branch details, commits, trunk lag, and PR info
```

---

## Built for Agents & Scripts

`st` is designed to be driven reliably by AI coding agents and CI/automation scripts — see **[docs/AGENT.md](docs/AGENT.md)** for the full contract (JSON schemas, error envelopes, idempotency).

- **Machine-readable everywhere:** every command except `__complete` and `completion` (plus `help`/`version`) accepts `--json`. Errors emit structured JSON envelopes on `stderr` and keep `stdout` clean.
- **Stable exit codes:**

  | Exit code | JSON `error.code` | Meaning |
  | --- | --- | --- |
  | `0` | — | Success |
  | `1` | `error` / `paused` | Usage, git, or precondition error |
  | `2` | `conflict` | Rebase stopped on a merge conflict (`st continue` / `st abort`) |
  | `3` | `not_initialized` | Repo has not run `st init` |
  | `4` | `dirty` | Tracked uncommitted changes block the operation |
  | `5` | `locked` / `locked_guard` | Advisory repo lock is held by another `st` process |
  | `70` | `internal` | Unexpected internal error |

- **Current-stack scoping by default:** `st log`, `st sync`, `st submit`, `st restack`, and `st doctor` drift warnings scope to your current stack when on a tracked branch so parallel work in sibling stacks or other `git worktree`s is never disturbed. Pass `--all` whenever you want the whole forest.
- **Safe with external `git worktree`s:** `st` shares `.git/stacked/state.json` across worktrees, rebases clean dependent worktrees in place during cascades, skips dirty or `git worktree lock`ed worktrees with a note, omits your own worktree path from `st log` to keep output noise-free, and never creates or deletes worktrees on disk.
- **Dry-run & Undo:** preview mutations with `--dry-run` on `create`, `modify`, `restack`, `squash`, `onto`, `rename`, `delete`, `track --all`, `untrack`, `sync`, and `submit`. Roll back the last mutation with `st undo` (preserves your working tree).

---

## How It Works

`st` stores all metadata in `.git/stacked/state.json` (written atomically via temp file + rename, guarded by an advisory lock):

```json
{
  "version": 1,
  "trunk": "main",
  "branches": {
    "feat-auth": {
      "name": "feat-auth",
      "parent": "main",
      "parentSHA": "a1b2c3d4..."
    },
    "feat-api": {
      "name": "feat-api",
      "parent": "feat-auth",
      "parentSHA": "e5f6a7b8..."
    }
  }
}
```

Each branch records `parentSHA` — the parent commit it was last rebased onto. When `feat-auth` changes, `feat-api.parentSHA` no longer matches `feat-auth`'s tip, so `st` marks it `(needs restack)` and runs:

```sh
git rebase --onto <current tip of feat-auth> <feat-api.parentSHA> feat-api
```

Then it updates `feat-api.parentSHA` and walks upward in topological order. If a rebase hits a conflict, `st` exits `2` leaving the standard Git rebase paused — resolve the files, `git add`, and run `st continue` (or `st abort` to back out).

---

## Command Reference

Flags may appear before or after positional arguments. Run `st help <command>` (or `st <command> -h`) for full flag details.

Every command below except `__complete` and `completion` (plus `help`/`version`) accepts `--json`; see docs/AGENT.md.

### Inspect & Setup

| Command | Aliases | Summary |
| --- | --- | --- |
| `st init [--trunk <name>]` | | Initialize stack tracking in this repo. |
| `st log [--all] [--pr]` | `ls` | Show the current stack tree (`--all` for all stacks, `--pr` for GitHub PR & CI status). |
| `st status [--pr]` | `stat` | Show current branch, parent/children, commits (`--json`), trunk lag, and restack state. |
| `st doctor [--fix] [--all]` | `validate` | Check stack metadata for problems and drift (`--fix` repairs metadata, `--all` checks all stacks). |
| `st guide` | | Print the recommended stacked-diff workflow (handy for agents). |

### Navigate

| Command | Aliases | Summary |
| --- | --- | --- |
| `st checkout [name]` | `co` | Check out a tracked branch (or trunk), or list tracked branches with no arg. |
| `st up [n]` | `u` | Move `n` levels up toward leaves (stops at branch points). |
| `st down [n]` | `d` | Move `n` levels down toward trunk. |
| `st top` | `t` | Jump to the leaf of the current stack. |
| `st bottom` | `b` | Jump to the lowest branch above trunk. |

### Build & Edit Stacks

| Command | Aliases | Summary |
| --- | --- | --- |
| `st create <name> [-m\|--message <msg>] [-a\|--all] [--dry-run]` | `c` | Create and track a branch stacked on current (`-a` stages tracked edits; `-m` commits). |
| `st modify [-m\|--message <msg>] [-a\|--all] [--commit] [--dry-run]` | `amend`, `m` | Amend (or `--commit`) staged changes (`-a` stages tracked edits) and restack upstack. |
| `st restack [--all] [--dry-run]` | `r` | Rebase current branch and upstack onto their parents (`--all` for whole forest). |
| `st squash [-m\|--message <msg>] [--dry-run]` | | Collapse all commits on the current branch into one and restack upstack. |
| `st onto <target> [--dry-run]` | `move` | Re-parent the current branch (and its upstack) onto `target`. |
| `st rename [old] <new> [--dry-run]` | `mv` | Rename a branch and update stack metadata & child parent pointers. |
| `st delete <name> [-f\|--force] [--dry-run]` | `rm` | Delete a merged/squash-merged branch (`-f` forces) and re-parent + restack children. |
| `st track [name] [--parent <branch>] [--all] [--dry-run]` | | Start tracking an existing branch (or all untracked branches with `--all`). |
| `st untrack [name] [--dry-run]` | | Stop tracking a branch and re-parent its children (keeps the git branch). |

### Sync & GitHub PRs

| Command | Aliases | Summary |
| --- | --- | --- |
| `st submit [--draft] [--no-pr] [--title <t>] [--body <b>] [--upto] [--all] [--remote <name>] [--dry-run]` | `ss` | Push stack (`--force-with-lease`) and create/retarget/update GitHub PRs. |
| `st sync [-s\|--submit] [--all] [--no-delete] [--no-fetch] [--remote <name>] [--dry-run]` | `s` | Fetch & fast-forward trunk, prune merged branches, restack (`-s` pushes & updates PRs). |

### Recover & Shell

| Command | Aliases | Summary |
| --- | --- | --- |
| `st continue` | | Resume a restack/onto/sync paused on a merge conflict. |
| `st abort` | | Abort an in-progress rebase and restore stack state. |
| `st undo [--force]` | | Revert the last stack-mutating command without touching your working tree. |
| `st completion <bash\|zsh\|fish>` | | Print a shell completion script (with live branch completion via `st __complete`). |
| `st help [<command>]` / `st version` | `-h`/`-v` | Show help / print build & version info. |

### Good to Know

- **GitHub PR defaults:** `st submit` automatically activates GitHub PR management whenever the remote is GitHub (including GitHub Enterprise) and `gh` is on `PATH`. Single-commit branches default the PR title & body from that commit; multi-commit branches default the title from the oldest commit and bullet-list the subjects. Override the current branch's PR with `--title` / `--body`.
- **Offline sync:** `st sync --no-fetch` skips all network calls and prunes/restacks purely against your local trunk ref.
- **Shell completions:** install once for live branch-name tab completion:
  ```sh
  st completion zsh  > "${fpath[1]}/_st"   # zsh
  st completion bash > /etc/bash_completion.d/st   # bash
  st completion fish > ~/.config/fish/completions/st.fish   # fish
  ```
- **Debugging git spawns:** run `ST_DEBUG=1 st <cmd>` to trace every `git` invocation and duration on `stderr` (`stdout` / `--json` stays untouched).
- **Waiting on locks:** set `ST_LOCK_WAIT=10s` to retry briefly on advisory lock contention instead of failing immediately with exit `5`.

---

## Contributing

The repo closes its own loop locally — `make test-fast` runs the pure fake-git engine suite in ~1s, and `make ci` runs version-pin checks, `gofumpt`, strict `golangci-lint`, `go vet` (+ Windows/Plan 9 cross-vet), `-race` tests, black-box e2e, and a merged coverage gate (≥75% total, ≥50% per function).

See **[CONTRIBUTING.md](CONTRIBUTING.md)** for the add-a-command recipe and **[AGENTS.md](AGENTS.md)** for the architecture and constraints.
