package cmd

import (
	"flag"
	"fmt"
	"os"

	"stacked/internal/git"
	"stacked/internal/stack"
)

func init() {
	register(&Command{
		Name:    "shell",
		Summary: "Print the shell integration that teleports into a branch's worktree",
		Usage:   "st shell install [bash|zsh|fish]",
		Run:     runShell,
	})
}

// cdDirectiveEnv names the file the binary writes a target directory to when a
// navigation command teleports into another worktree. The shell shim installed
// by `st shell install` sets this variable and `builtin cd`s to the path the
// binary leaves there, so a child process can move its parent shell.
const cdDirectiveEnv = "ST_CD_FILE"

// runShell prints the shell integration snippet. Without a sub-argument (or with
// "install") it auto-detects the shell from $SHELL; an explicit shell name forces
// one. The user evaluates the snippet (e.g. `eval "$(st shell install)"`).
func runShell(args []string) error {
	// shell emits shell scripts, not JSON (like completion); it uses a plain flag
	// set so -h prints the registry-derived usage line and parse errors route
	// through the dispatcher.
	fs := flag.NewFlagSet("shell", flag.ContinueOnError)
	fs.Usage = func() { fmt.Fprintln(fs.Output(), usageLine("shell")) }
	if err := parseFlagSet(fs, args); err != nil {
		return err
	}

	// Tolerate a leading "install" verb so both `st shell install` and
	// `st shell zsh` work; everything after selects the shell.
	rest := fs.Args()
	if len(rest) > 0 && rest[0] == "install" {
		rest = rest[1:]
	}
	shell := ""
	if len(rest) == 1 {
		shell = rest[0]
	} else if len(rest) > 1 {
		return fmt.Errorf("shell install takes at most one shell name")
	}
	if shell == "" {
		shell = detectShell()
	}

	snippet, ok := shellSnippet(shell)
	if !ok {
		return fmt.Errorf("unsupported shell %q (supported: bash, zsh, fish)", shell)
	}
	out("%s", snippet)
	return nil
}

// detectShell guesses the user's shell from $SHELL, defaulting to bash.
func detectShell() string {
	sh := os.Getenv("SHELL")
	switch {
	case hasSuffix(sh, "zsh"):
		return "zsh"
	case hasSuffix(sh, "fish"):
		return "fish"
	default:
		return "bash"
	}
}

func hasSuffix(s, suffix string) bool {
	return len(s) >= len(suffix) && s[len(s)-len(suffix):] == suffix
}

// shellSnippet returns the integration snippet for the named shell. The shim
// wraps `st`: it points ST_CD_FILE at a temp file, runs the real binary, and —
// if the binary wrote a directory there — cd's the interactive shell into it.
func shellSnippet(shell string) (string, bool) {
	switch shell {
	case "bash", "zsh":
		return `st() {
  local _st_cd
  _st_cd="$(mktemp)"
  ST_CD_FILE="$_st_cd" command st "$@"
  local _st_rc=$?
  if [ -s "$_st_cd" ]; then
    builtin cd "$(cat "$_st_cd")" || true
  fi
  rm -f "$_st_cd"
  return $_st_rc
}
`, true
	case "fish":
		return `function st
  set -l _st_cd (mktemp)
  ST_CD_FILE=$_st_cd command st $argv
  set -l _st_rc $status
  if test -s $_st_cd
    builtin cd (cat $_st_cd)
  end
  rm -f $_st_cd
  return $_st_rc
end
`, true
	default:
		return "", false
	}
}

// shimActive reports whether the shell teleport shim is installed, i.e. whether
// ST_CD_FILE is set. When it is, the binary can move the parent shell by writing
// the target directory to that file; when it is not, the parent shell stays put
// and navigation must instead TELL the user how to move.
func shimActive() bool {
	return os.Getenv(cdDirectiveEnv) != ""
}

// navSummary builds a navigation command's human summary.
//
//   - In-place (dest == ""): "<verb> <branch>", unchanged.
//   - Teleport WITH the shim active: "<verb> <branch> (worktree: <path>)" — the
//     shim cd's the parent shell, so claiming the move is accurate.
//   - Teleport WITHOUT the shim (ST_CD_FILE unset): the parent shell did NOT
//     move, so we must not claim "switched". Instead emit an actionable cd line.
func navSummary(verb, branch, dest string) string {
	if dest == "" {
		return fmt.Sprintf("%s %s", verb, branch)
	}
	if shimActive() {
		return fmt.Sprintf("%s %s (worktree: %s)", verb, branch, dest)
	}
	return teleportHint(branch, dest)
}

// teleportHint is the human summary printed when a branch lives in another
// worktree and the shell shim is not installed: it names where the branch lives
// and the exact command to get there, instead of falsely reporting a switch.
func teleportHint(branch, dest string) string {
	return fmt.Sprintf("%s is in worktree %s\nrun: cd %s", branch, dest, dest)
}

func topSummary(branch, dest string) string {
	if dest == "" {
		return fmt.Sprintf("switched to %s (top of stack)", branch)
	}
	if shimActive() {
		return fmt.Sprintf("switched to %s (top of stack, worktree: %s)", branch, dest)
	}
	return teleportHint(branch, dest)
}

func alreadyAtSummary(prefix, branch string) string {
	return fmt.Sprintf("%s: %s", prefix, branch)
}

// writeCDDirective records dir as the place the parent shell should cd to, when
// the shell shim is installed (ST_CD_FILE set). Without the shim it is a no-op
// — the caller prints the path instead (progressive enhancement).
func writeCDDirective(dir string) {
	path := os.Getenv(cdDirectiveEnv)
	if path == "" {
		return
	}
	// Best-effort: a failed write must not break navigation; the command still
	// prints the path so the user can cd manually.
	_ = os.WriteFile(path, []byte(dir), 0o644)
}

// teleportCheckout moves to branch wherever it lives. In a multi-worktree repo,
// if branch is checked out in another worktree, it teleports there (writes the
// cd directive for the shell shim) instead of an in-place checkout — git forbids
// checking out a branch that lives in another worktree anyway. It returns the
// worktree path when it teleported (""=checked out in place). Single-tree repos
// take the plain git.Checkout path, so existing behavior is unchanged.
func teleportCheckout(branch string) (string, error) {
	wts, err := worktrees()
	if err != nil {
		return "", err
	}
	if stack.IsMultiWorktree(wts) {
		cur, _ := currentBranch()
		if wt, ok := stack.OwnerOf(wts, branch); ok && branch != cur {
			writeCDDirective(wt.Path)
			return wt.Path, nil
		}
	}
	if err := git.Checkout(branch); err != nil {
		return "", fmt.Errorf("checking out %q: %w", branch, err)
	}
	return "", nil
}
