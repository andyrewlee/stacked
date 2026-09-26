# Design 017: Branch-aware shell completion

> Status: **proposed contract** — nothing here ships. The `__complete`
> endpoint, every candidate policy, and all shell snippets below are
> proposals; the fixtures in `017-completion-cases.json` are not current
> output. A future implementation plan must re-verify `Current contract`
> against then-current source.

## Current contract

Verified against this stack's HEAD (`cmd/completion.go`).

- `st completion <bash|zsh|fish>` prints a static script: command names at
  word 1 (`commandNames()` = registry + help/version), then per-command
  declared flags (via each command's `NewFlagSet`, same constructors as
  `help --json`) plus literal `subVerbs` (`worktree: ls list rm remove`,
  `shell: install`, `completion: bash zsh fish`). Aliases appear in
  case-arm patterns (`casePattern`), never as word-1 candidates.
- **No positional completion exists.** `commandCompletions` emits only
  flag tokens and sub-verbs; branch names are never offered.
- Generated-script tests (`TestCommandCompletions`,
  `TestCompletionScriptsParse`, `TestCompletionShells`) prove syntax and
  command coverage; they do not execute completions dynamically.

### Positional grammar (traced)

| command (aliases) | positional(s) | flags taking values | `--` semantics | sub-verbs | candidate source |
|---|---|---|---|---|---|
| `checkout` (`co`) | `[name]` — 0 or 1 | none | rest after `--` counts as positional | — | `s.Trunk` + tracked `s.Branches` |
| `onto` (`move`) | `<target>` — exactly 1 | none | same | — | tracked branches outside the moving subtree |
| `track` | `[name]` — 0 or 1 | `--parent <branch>` | same | — | local branches that are neither tracked nor trunk |
| `worktree` (`wt`) | `<branch>` or `rm|remove <branch>` | none on leaf forms | same | `ls`/`list`, `rm`/`remove`, `--all` flag | create: tracked branches lacking a worktree (trunk excluded — it owns the main worktree); rm: branches owning a worktree |

Go's `flag` parser stops at the FIRST non-flag word and at `--`; the
completion parser must reproduce that (a flag-looking word after a
positional is itself a positional under the real grammar). Value-taking
flags are detected from `IsBoolFlag` on the declared `flag.FlagSet` — no
hand-maintained list: `-m/--message`, `--parent`, `--remote`, `--trunk`
fall out automatically.

## Candidate policy

- `checkout`/`co`: `s.Trunk` plus every tracked branch, sorted bytewise.
- `onto`/`move`: tracked branches excluding the current branch and its
  descendants (`stack.Descendants`), and excluding trunk? No — `onto
  trunk` is legal; keep trunk. Eligibility derives from the same state
  topology `OntoPlan` consults, WITHOUT invoking the planner.
- `track`: `git for-each-ref --format=%(refname:short) refs/heads` minus
  `s.Branches` keys minus `s.Trunk`. Untracked-only — tracking an
  already-tracked name is a re-parent path st does not offer here.
- `worktree`/`wt` create: tracked branches minus `s.Trunk` minus branches
  already owning a worktree (`Worktrees()` + `LinkedOwnerOf`).
- `worktree rm|remove`: branches that DO own a worktree; after `rm`, the
  next word is a branch name, not a flag.
- Candidates are byte-exact branch names — slashes, Unicode, and legal
  ref punctuation pass through unmodified. Git refname rules already
  forbid newline and control bytes, so one-name-per-line is safe; the
  endpoint still validates with `check-ref-format` semantics defensively.

## Endpoint protocol

One hidden, read-only command — name `__complete` (double-underscore: it
never appears in `commandNames()`, registry help, or generated word-1
suggestions):

```
st __complete <command-or-alias> <word-index> -- <prior words...>
```

- `<command-or-alias>`: the word-1 token as typed (`co` behaves like
  `checkout`). Unknown → exit 0, no output.
- `<word-index>`: cursor position among words AFTER the command; the
  endpoint re-parses `<prior words>` with the command's real FlagSet to
  compute the positional index, honoring value-taking flags and `--`.
- Output: one validated branch name per line on stdout, no descriptions,
  no trailing prose. Empty output = no candidates. Exit 0 always for
  interactive use; malformed argv (missing command/index) is a usage
  error exit 1 — scripts treat any failure as silence.
- Never mutates: no lock, no `Save`, no `DropUndo`, no fetch, no
  worktree-changing call. Reads state file + `for-each-ref`/`worktree
  list` only.

## Shell integration

Generated scripts call the ENDPOINT at completion time, embedding branch
candidates as plain argv — never evaluated:

- **bash**: `COMPREPLY=( $(compgen -W "$(st __complete ...)" -- "$cur") )` —
  word-splitting on newlines is already how the static words are handled;
  names with spaces are impossible under refname rules (space is not
  legal in git refs), so `compgen -W` stays correct.
- **zsh**: `local -a brs; brs=( ${(f)"$(st __complete ...)"} ); compadd -- $brs`
  — `${(f)}` splits on newlines; `compadd --` keeps leading-dash names inert.
- **fish**: `st __complete ...` inside `-a`? No — fish supports command
  substitution directly: `complete -c st -n '...' -a "(st __complete ...)"`;
  fish splits substitution output on newlines natively.
- Scripts locate the binary as `st` on PATH at completion time (a stale
  embedded path would outlive `brew upgrade`); an `ST_COMPLETE_BIN`
  override env var is the documented escape hatch.
- All three shells must be proven to pass candidate text as DATA: the
  acceptance tests source the generated functions, feed a branch named
  `$(touch PWNED)`-style sentinel (refname-legal punctuation only, e.g.
  `feat;echo` isn't legal — use legal-but-hairy `a{b},c[d]`, `'quotes`,
  `dash--name`) and assert no command execution and byte-exact insertion.

## Performance and failures

- One `st __complete` process per request; inside it: ≤3 git probes
  (`for-each-ref`, `worktree list`, `rev-parse`-family as needed) plus
  state file read. No network, no `fetch`, no `rev-list` history walk —
  candidate sets are flat inventories.
- Silent empty on: outside a git repo, uninitialized st, corrupt or
  `future` state file, detached HEAD (checkout/onto candidates still
  work — they don't need a current branch; `onto` needs current only to
  exclude the subtree, so detached HEAD degenerates to all-tracked),
  flag-value position, unknown command, empty result. No stderr output
  in any of these — a completion that pollutes the tty is worse than
  none.
- Bounded: with thousands of local branches the endpoint cost is one
  `for-each-ref` — O(branches) output lines, no per-branch subprocess.
  The acceptance doc's benchmark asserts process COUNT, not milliseconds.

## Examples

`plans/design/017-completion-cases.json` — scenarios: checkout trunk +
tracked ordering, alias `co`, onto subtree exclusion, track untracked-only,
`--parent` value position, `--` terminator, `worktree rm` owner list,
branch names with slashes/Unicode/legal punctuation, detached HEAD, no
repo, uninitialized, future state, thousands of branches, sub-verb
routing. Each object: `scenario`, `words`, `cursor`, `setup`, `expected`,
`proposed`.

## Acceptance

`plans/design/017-completion-acceptance.md` maps every scenario to a
future test: cmd-level endpoint tests over fake/real git, generated-script
EXECUTION tests in real bash/zsh/fish (sentinel-inertness), and the
structural performance assertion (bounded git-process count via PATH
shim; no ms threshold).

## Deferred work

- Descriptions beside candidates (fish `-d`, zsh `compadd -X`) — v1 emits
  bare names only.
- Remote-branch completion for `track` (needs `refs/remotes` policy +
  fetch-staleness decisions).
- Persisted candidate cache — rejected already; one cheap probe is fast
  enough and never stale.
- Implementation split: (1) `internal/stack` read-only candidate helpers
  sharing enumeration with checkout's list path; (2) `cmd` hidden
  `__complete` endpoint + argv protocol; (3) script generators emitting
  dynamic calls behind the same `st completion` surface; (4) shell-exec
  e2e + benchmark; (5) AGENT.md/CHANGELOG.
