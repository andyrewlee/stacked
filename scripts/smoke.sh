#!/usr/bin/env bash
set -u

ST="${1:-./st}"
case "$ST" in
  /*) ;;
  *) ST="$(pwd)/$ST" ;;
esac
if [ ! -x "$ST" ]; then
  echo "smoke: st binary not executable: $ST" >&2
  echo "usage: scripts/smoke.sh [path/to/st]" >&2
  exit 1
fi
if ! command -v jq >/dev/null 2>&1; then
  echo "smoke: jq is required for JSON assertions" >&2
  exit 1
fi

ROOT="$(mktemp -d)"
export GIT_AUTHOR_NAME=test
export GIT_AUTHOR_EMAIL=test@example.com
export GIT_COMMITTER_NAME=test
export GIT_COMMITTER_EMAIL=test@example.com

pass=0
fail=0
case_no=0
last_out=""
last_err=""
last_code=0

next() {
  case_no=$((case_no + 1))
  printf '\n[%03d] %s\n' "$case_no" "$*"
}

ok() {
  pass=$((pass + 1))
  printf '  ok - %s\n' "$*"
}

bad() {
  fail=$((fail + 1))
  printf '  FAIL - %s\n' "$*"
  printf '  cwd=%s code=%s\n  stdout=%s\n  stderr=%s\n' \
    "$PWD" "$last_code" "${last_out//$'\n'/\\n}" "${last_err//$'\n'/\\n}"
}

run() {
  local out_file err_file
  out_file="$ROOT/out.$case_no"
  err_file="$ROOT/err.$case_no"
  "$@" >"$out_file" 2>"$err_file"
  last_code=$?
  last_out="$(cat "$out_file")"
  last_err="$(cat "$err_file")"
  return 0
}

st() { run "$ST" "$@"; }

expect_code() { [ "$last_code" -eq "$1" ] && ok "exit $1" || bad "expected exit $1"; }
expect_out_has() { [[ "$last_out" == *"$1"* ]] && ok "stdout contains $1" || bad "stdout missing $1"; }
expect_err_has() { [[ "$last_err" == *"$1"* ]] && ok "stderr contains $1" || bad "stderr missing $1"; }
expect_no_stdout() { [ -z "$last_out" ] && ok "stdout empty" || bad "expected empty stdout"; }
expect_json() { printf '%s' "$last_out" | jq -e "$1" >/dev/null && ok "json matches $1" || bad "json mismatch $1"; }
expect_err_json() { printf '%s' "$last_err" | jq -e "$1" >/dev/null && ok "stderr json matches $1" || bad "stderr json mismatch $1"; }

expect_branch() {
  local branch
  branch="$(git rev-parse --abbrev-ref HEAD)"
  if [ "$branch" = "$1" ]; then
    ok "HEAD is $1"
  else
    last_out="$branch"
    bad "HEAD expected $1"
  fi
}

expect_file_has() {
  if [ -f "$1" ] && grep -q "$2" "$1"; then
    ok "$1 contains $2"
  else
    bad "$1 missing $2"
  fi
}

newrepo() {
  local dir
  dir="$ROOT/$1"
  mkdir -p "$dir"
  cd "$dir" || exit 1
  git init -q -b main
  git config user.email test@example.com
  git config user.name test
  printf 'base\n' >base.txt
  git add base.txt
  git commit -q -m init
}

next "not initialized status exits 3 and JSON error stays on stderr"
newrepo r00
st status --json
expect_code 3
expect_no_stdout
expect_err_json '.error.code == "not_initialized"'

next "root help text and JSON are discoverable"
st help
expect_code 0
expect_out_has "st - manage stacked diffs"
expect_out_has "create"
st help --json
expect_code 0
expect_json '.commands[] | select(.name == "version")'
st --help --json
expect_code 0
expect_json '.commands[] | select(.name == "help")'

next "built-in help/version JSON and bad flags"
st help create --json
expect_code 0
expect_json '.name == "create" and (.usage | contains("--json"))'
st help version --json
expect_code 0
expect_json '.name == "version"'
st version --json
expect_code 0
expect_json '.version | type == "string"'
st help --jsno
expect_code 1
expect_no_stdout
expect_err_has "unknown help flag"
st version --bad --json
expect_code 1
expect_no_stdout
expect_err_json '.error.code == "error"'

next "init default, idempotent init, and validation"
st init
expect_code 0
expect_out_has "initialized stacked"
st init --json
expect_code 0
expect_json '.alreadyInitialized == true'
st validate
expect_code 0
expect_out_has "ok:"
st validate --json
expect_code 0
expect_json '.ok == true and .tracked == 0'

next "create, aliases, status, log, checkout list"
printf 'a\n' >a.txt
st create feat-a -a -m "add a"
expect_code 0
expect_branch feat-a
st status --json
expect_code 0
expect_json '.branch == "feat-a" and .parent == "main" and .role == "tracked"'
st log
expect_code 0
expect_out_has "feat-a"
st ls --json
expect_code 0
expect_json '.children[0].name == "feat-a"'
st checkout --json
expect_code 0
expect_json '.current == "feat-a" and (.branches | index("feat-a"))'
st co main --json
expect_code 0
expect_json '.branch == "main" and .switched == true'
expect_branch main
st checkout feat-a
expect_code 0

next "create flags after positional and navigation"
printf 'b\n' >b.txt
st create feat-b -m "add b" -a --json
expect_code 0
expect_json '.branch == "feat-b"'
printf 'c\n' >c.txt
st c feat-c -a -m "add c"
expect_code 0
expect_branch feat-c
st down 1 --json
expect_code 0
expect_json '.branch == "feat-b"'
expect_branch feat-b
st up 1
expect_code 0
expect_out_has "feat-c"
expect_branch feat-c
st bottom
expect_code 0
expect_branch feat-a
st checkout feat-b
st top --json
expect_code 0
expect_json '.branch == "feat-c"'
expect_branch feat-c

next "branch point up stops and checkout can pick child"
st checkout feat-a
printf 'd\n' >d.txt
st create feat-d -a -m "add d"
expect_code 0
st checkout feat-a
st up --json
expect_code 0
expect_json '.children | length == 2'
expect_branch feat-a
st checkout feat-d
expect_code 0
expect_branch feat-d

next "modify amend, commit, reword, restack dry-run and actual"
st checkout feat-b
printf 'b2\n' >>b.txt
st modify --json
expect_code 0
expect_json '.branch == "feat-b"'
st checkout feat-b
printf 'b3\n' >>b.txt
st modify --commit -m "b followup"
expect_code 0
subject="$(git log -1 --format=%s)"
[ "$subject" = "b followup" ] && ok "modify --commit subject" || bad "modify --commit subject"
st modify -m "b reworded"
expect_code 0
subject="$(git log -1 --format=%s)"
[ "$subject" = "b reworded" ] && ok "modify -m subject" || bad "modify -m subject"
st checkout feat-a
printf 'a2\n' >>a.txt
st modify -a
expect_code 0
st checkout feat-c
st validate --json
expect_code 0
expect_json '.warnings | length == 0'
st restack --dry-run --json
expect_code 0
expect_json '.dryRun == true'
st restack
expect_code 0
expect_branch feat-c

next "dirty tree guard returns exit 4"
printf 'dirty\n' >>c.txt
st restack --json
expect_code 4
expect_no_stdout
expect_err_json '.error.code == "dirty"'
git checkout -- c.txt

next "squash and fold"
st checkout feat-b
printf 'b4\n' >>b.txt
st modify --commit -m "b extra"
expect_code 0
st squash -m "squashed b" --json
expect_code 0
expect_json '.branch == "feat-b"'
count="$(git rev-list --count feat-a..feat-b)"
[ "$count" = 1 ] && ok "squash leaves one commit" || bad "squash commit count"
st checkout feat-c
st fold --json
expect_code 0
expect_json '.branch == "feat-b"'
git show-ref --verify --quiet refs/heads/feat-c
rc=$?
[ "$rc" -ne 0 ] && ok "fold deletes branch" || bad "fold deleted branch"

next "onto, rename, untrack, track, delete"
st checkout feat-d
st onto feat-b --json
expect_code 0
expect_json '.branch == "feat-d"'
st rename feat-d feat-renamed --json
expect_code 0
expect_json '.branch == "feat-renamed"'
expect_branch feat-renamed
st checkout feat-b
printf 'e\n' >e.txt
st create feat-e -a -m "add e"
expect_code 0
st untrack feat-e --json
expect_code 0
expect_json '.summary | contains("Untracked")'
st track --parent feat-b --json
expect_code 0
expect_json '.branch == "feat-e"'
st checkout feat-b
st delete feat-e --force --json
expect_code 0
expect_json '.deleted[0] == "feat-e"'
git show-ref --verify --quiet refs/heads/feat-e
rc=$?
[ "$rc" -ne 0 ] && ok "delete removes git branch" || bad "delete removes git branch"

next "undo create and working tree preservation"
st checkout feat-b
printf 'u\n' >u.txt
st create undo-me -a -m "undo me"
expect_code 0
st undo --json
expect_code 0
expect_json '.undone == true and .label == "create"'
git show-ref --verify --quiet refs/heads/undo-me
rc=$?
[ "$rc" -ne 0 ] && ok "undo create deletes branch" || bad "undo create deletes branch"
printf 'local\n' >local.txt
st undo
expect_code 0
expect_out_has "note: your working tree was not modified"
[ -f local.txt ] && ok "undo preserves uncommitted working tree" || bad "undo lost working tree file"
rm -f local.txt

next "repair missing branch and validate problem JSON"
st checkout main
printf 'x\n' >x.txt
st create broken -a -m "broken"
expect_code 0
git checkout main >/dev/null 2>&1
git branch -D broken >/dev/null 2>&1
st validate --json
expect_code 1
expect_json '.ok == false and (.problems | length > 0)'
st repair --json
expect_code 0
expect_json '.repaired == true'
st validate
expect_code 0

next "completion and guide"
st completion bash
expect_code 0
expect_out_has "complete"
st completion zsh
expect_code 0
expect_out_has "#compdef"
st completion fish
expect_code 0
expect_out_has "complete -c st"
st guide --json
expect_code 0
expect_json '.steps | length > 0'

next "aliases and JSON errors keep stdout clean"
st frobnicate --json
expect_code 1
expect_no_stdout
expect_err_json '.error.code == "error"'
st create --json
expect_code 1
expect_no_stdout
expect_err_json '.error.code == "error"'
st up 0 --json
expect_code 1
expect_no_stdout
expect_err_json '.error.code == "error"'

next "abort and continue without a rebase report errors"
st abort --json
expect_code 1
expect_no_stdout
expect_err_json '.error.code == "error"'
st continue --json
expect_code 1
expect_no_stdout
expect_err_json '.error.code == "error"'

next "remote submit dry-run and actual local bare remote"
newrepo remote
st init
printf 'ra\n' >ra.txt
st create remote-a -a -m "remote a"
printf 'rb\n' >rb.txt
st create remote-b -a -m "remote b"
git init -q --bare "$ROOT/bare.git"
git remote add origin "$ROOT/bare.git"
git push -q -u origin main
st submit --dry-run --json
expect_code 0
expect_json '.dryRun == true and (.pushed | length == 2)'
st submit --json
expect_code 0
expect_json '.dryRun == false and (.pushed | length == 2)'
git --git-dir="$ROOT/bare.git" show-ref --verify --quiet refs/heads/remote-a
rc=$?
[ "$rc" -eq 0 ] && ok "remote-a pushed" || bad "remote-a not pushed"

next "sync dry-run and sync --no-delete with local remote trunk advance"
st checkout main
st submit --dry-run
expect_code 0
expect_out_has "nothing to submit"
git clone -q "$ROOT/bare.git" "$ROOT/clone"
(
  cd "$ROOT/clone" || exit 1
  git checkout -q main
  git config user.email test@example.com
  git config user.name test
  printf 'remote-main\n' >remote.txt
  git add remote.txt
  git commit -q -m "remote main"
  git push -q origin main
)
st sync --dry-run --json
expect_code 0
expect_json '.dryRun == true'
st sync --no-delete --json
expect_code 0
expect_json '.summary | contains("sync complete")'
expect_file_has remote.txt "remote-main"

next "conflict exit 2, continue resolves, abort clears rebase"
newrepo conflict
st init
printf 'A\n' >f.txt
st create feat-a -a -m "a"
printf 'A\nB\n' >f.txt
st create feat-b -a -m "b"
st checkout feat-a
printf 'X\n' >f.txt
st modify -a --json
expect_code 2
expect_no_stdout
expect_err_json '.error.code == "conflict"'
printf 'X\nB\n' >f.txt
git add f.txt
st continue --json
expect_code 0
expect_json '.summary | contains("continued")'
st validate
expect_code 0

newrepo abortcase
st init
printf 'A\n' >f.txt
st create feat-a -a -m "a"
printf 'A\nB\n' >f.txt
st create feat-b -a -m "b"
st checkout feat-a
printf 'Y\n' >f.txt
st modify -a
expect_code 2
st abort --json
expect_code 0
expect_json '.aborted == true'

printf '\nsmoke complete: pass=%d fail=%d root=%s\n' "$pass" "$fail" "$ROOT"
if [ "$fail" -eq 0 ]; then
  if [ -z "${KEEP_SMOKE_ROOT:-}" ]; then
    rm -rf "$ROOT"
  fi
  exit 0
fi
exit 1
