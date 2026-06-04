// Package git is a thin wrapper around the git command line via os/exec.
// All functions operate on the git repository containing the current working
// directory.
package git

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// run executes "git args..." and returns the combined stdout/stderr output. On
// failure it returns an error whose message includes the git stderr so callers
// get an actionable diagnostic.
func run(args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	var stdout, stderr strings.Builder
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	if err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = strings.TrimSpace(stdout.String())
		}
		if msg != "" {
			return stdout.String(), fmt.Errorf("git %s: %s: %w", strings.Join(args, " "), msg, err)
		}
		return stdout.String(), fmt.Errorf("git %s: %w", strings.Join(args, " "), err)
	}
	return stdout.String(), nil
}

// ok reports whether "git args..." exits successfully. It surfaces no error for
// a non-zero exit, which is used by predicate helpers like IsAncestor.
func ok(args ...string) bool {
	return exec.Command("git", args...).Run() == nil
}

// Run runs "git args..." and returns the trimmed combined stdout. The returned
// error includes the git stderr on failure.
func Run(args ...string) (string, error) {
	out, err := run(args...)
	return strings.TrimSpace(out), err
}

// RunInteractive runs "git args..." with stdin, stdout and stderr inherited from
// the current process, which is required for operations (such as rebases) that
// may prompt or report conflicts interactively.
func RunInteractive(args ...string) error {
	cmd := exec.Command("git", args...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("git %s: %w", strings.Join(args, " "), err)
	}
	return nil
}

// ErrDetachedHEAD is returned by CurrentBranch when HEAD is not on a branch.
var ErrDetachedHEAD = errors.New("not on a branch (detached HEAD); check out a branch first")

// CurrentBranch returns the name of the currently checked-out branch. It returns
// ErrDetachedHEAD when HEAD is detached (for example, mid-rebase or sitting on a
// raw commit).
func CurrentBranch() (string, error) {
	out, err := Run("rev-parse", "--abbrev-ref", "HEAD")
	if err != nil {
		return "", err
	}
	if out == "HEAD" {
		return "", ErrDetachedHEAD
	}
	return out, nil
}

// BranchExists reports whether a local branch with the given name exists.
func BranchExists(name string) bool {
	return ok("show-ref", "--verify", "--quiet", "refs/heads/"+name)
}

// Checkout switches the working tree to the named branch.
func Checkout(name string) error {
	_, err := Run("checkout", name)
	return err
}

// CreateBranch creates a new branch off the current HEAD and switches to it.
func CreateBranch(name string) error {
	_, err := Run("checkout", "-b", name)
	return err
}

// DeleteBranch deletes the named local branch. When force is true the branch is
// removed even if it is not fully merged.
func DeleteBranch(name string, force bool) error {
	flag := "-d"
	if force {
		flag = "-D"
	}
	_, err := Run("branch", flag, name)
	return err
}

// RevParse returns the full commit SHA that the given ref resolves to.
func RevParse(ref string) (string, error) {
	return Run("rev-parse", ref)
}

func absPathFromGitOutput(path string) (string, error) {
	if filepath.IsAbs(path) {
		return filepath.Clean(path), nil
	}
	// Git emits relative repository paths from the process working directory,
	// not from the repository root.
	return filepath.Abs(path)
}

// IsClean reports whether the working tree has no staged or unstaged changes,
// i.e. "git status --porcelain" produces no output.
func IsClean() (bool, error) {
	out, err := Run("status", "--porcelain")
	if err != nil {
		return false, err
	}
	return out == "", nil
}

// HasStagedChanges reports whether there are staged changes in the index.
func HasStagedChanges() (bool, error) {
	cmd := exec.Command("git", "diff", "--cached", "--quiet")
	err := cmd.Run()
	if err == nil {
		return false, nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		if exitErr.ExitCode() == 1 {
			return true, nil
		}
	}
	return false, fmt.Errorf("git diff --cached --quiet: %w", err)
}

// Add stages the given paths. When no paths are provided it stages all changes
// in the repository ("git add -A").
func Add(paths ...string) error {
	if len(paths) == 0 {
		_, err := Run("add", "-A")
		return err
	}
	args := append([]string{"add", "--"}, paths...)
	_, err := Run(args...)
	return err
}

// Commit creates a commit with the given message. When all is true, modified and
// deleted tracked files are staged automatically ("git commit -a").
func Commit(message string, all bool) error {
	args := []string{"commit"}
	if all {
		args = append(args, "-a")
	}
	args = append(args, "-m", message)
	_, err := Run(args...)
	return err
}

// AmendNoEdit amends the most recent commit without changing its message. When
// all is true, modified and deleted tracked files are staged automatically.
func AmendNoEdit(all bool) error {
	args := []string{"commit"}
	if all {
		args = append(args, "-a")
	}
	args = append(args, "--amend", "--no-edit")
	_, err := Run(args...)
	return err
}

// AmendMessage amends the most recent commit, replacing its message. When all is
// true, modified and deleted tracked files are staged automatically.
func AmendMessage(message string, all bool) error {
	args := []string{"commit", "--amend", "-m", message}
	if all {
		args = append(args, "-a")
	}
	_, err := Run(args...)
	return err
}

// RebaseOnto runs "git rebase --onto newBase oldBase branch" with inherited
// stdio so conflicts can be resolved interactively.
func RebaseOnto(newBase, oldBase, branch string) error {
	return RunInteractive("rebase", "--onto", newBase, oldBase, branch)
}

// RebaseInProgress reports whether a git rebase is currently in progress (for
// example, because it stopped on a merge conflict).
func RebaseInProgress() (bool, error) {
	gitDir, err := GitDir()
	if err != nil {
		return false, err
	}
	for _, name := range []string{"rebase-merge", "rebase-apply"} {
		_, err := os.Stat(filepath.Join(gitDir, name))
		if err == nil {
			return true, nil
		}
		if !errors.Is(err, os.ErrNotExist) {
			return false, fmt.Errorf("stat %s: %w", name, err)
		}
	}
	return false, nil
}

// RebaseHeadName returns the branch being rebased while a rebase is in progress,
// or an empty string if it cannot be determined.
func RebaseHeadName() (string, error) {
	gitDir, err := GitDir()
	if err != nil {
		return "", err
	}
	for _, dir := range []string{"rebase-merge", "rebase-apply"} {
		data, err := os.ReadFile(filepath.Join(gitDir, dir, "head-name"))
		if err != nil {
			continue
		}
		ref := strings.TrimSpace(string(data))
		return strings.TrimPrefix(ref, "refs/heads/"), nil
	}
	return "", nil
}

// RebaseContinue runs "git rebase --continue", reusing the existing commit
// messages without opening an editor so it never blocks on interactive input. It
// returns an error if the rebase does not run to completion (for example because
// conflicts remain to be resolved).
func RebaseContinue() error {
	cmd := exec.Command("git", "rebase", "--continue")
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Env = append(os.Environ(), "GIT_EDITOR=true", "GIT_SEQUENCE_EDITOR=true")
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("git rebase --continue: %w", err)
	}
	return nil
}

// Fetch updates remote-tracking refs for the named remote.
func Fetch(remote string) error {
	_, err := Run("fetch", remote)
	return err
}

// Push pushes the given branch to the "origin" remote and records it as the
// branch's upstream (-u) so ahead/behind tracking works after the first publish.
// When force is true it uses --force-with-lease for a safe force push.
func Push(branch string, force bool) error {
	args := []string{"push", "-u"}
	if force {
		args = append(args, "--force-with-lease")
	}
	refspec := "refs/heads/" + branch + ":refs/heads/" + branch
	args = append(args, "origin", refspec)
	_, err := Run(args...)
	return err
}

// RemoteExists reports whether a remote with the given name is configured.
func RemoteExists(name string) bool {
	return ok("remote", "get-url", name)
}

// MergeBase returns the best common ancestor commit of the two given refs.
func MergeBase(a, b string) (string, error) {
	return Run("merge-base", a, b)
}

// IsAncestor reports whether ancestor is an ancestor of descendant. A valid
// negative answer returns false, nil; invalid refs and other git failures return
// an error.
func IsAncestor(ancestor, descendant string) (bool, error) {
	cmd := exec.Command("git", "merge-base", "--is-ancestor", ancestor, descendant)
	out, err := cmd.CombinedOutput()
	if err == nil {
		return true, nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
		return false, nil
	}
	msg := strings.TrimSpace(string(out))
	if msg != "" {
		return false, fmt.Errorf("git merge-base --is-ancestor %s %s: %s: %w", ancestor, descendant, msg, err)
	}
	return false, fmt.Errorf("git merge-base --is-ancestor %s %s: %w", ancestor, descendant, err)
}

// GitDir returns the absolute path to the repository's .git directory.
func GitDir() (string, error) {
	return Run("rev-parse", "--absolute-git-dir")
}

// RepoRoot returns the absolute path to the top level of the working tree.
func RepoRoot() (string, error) {
	return Run("rev-parse", "--show-toplevel")
}

// GitCommonDir returns the absolute path to the repository's common git
// directory. For a linked worktree this is the shared git dir of the main
// worktree, so stack metadata is shared across all worktrees of a repository.
func GitCommonDir() (string, error) {
	dir, err := Run("rev-parse", "--path-format=absolute", "--git-common-dir")
	if err == nil {
		return dir, nil
	}
	// Fall back for git versions without --path-format: resolve a possibly
	// relative --git-common-dir from the current working directory.
	dir, err = Run("rev-parse", "--git-common-dir")
	if err != nil {
		return "", err
	}
	if !filepath.IsAbs(dir) {
		dir, err = absPathFromGitOutput(dir)
		if err != nil {
			return "", err
		}
	}
	return dir, nil
}

// RebaseAbort aborts an in-progress rebase, restoring the pre-rebase state.
func RebaseAbort() error {
	_, err := Run("rebase", "--abort")
	return err
}

// ResetSoft moves the current branch to ref without touching the index or
// working tree ("git reset --soft").
func ResetSoft(ref string) error {
	_, err := Run("reset", "--soft", ref)
	return err
}

// RenameBranch renames a local branch ("git branch -m old new").
func RenameBranch(oldName, newName string) error {
	_, err := Run("branch", "-m", oldName, newName)
	return err
}

// ForceBranch points the branch name at ref without checking it out
// ("git branch -f name ref"). It refuses to move the currently checked-out
// branch, matching git's own behavior.
func ForceBranch(name, ref string) error {
	_, err := Run("branch", "-f", name, ref)
	return err
}

// UpdateRef sets a ref (e.g. "refs/heads/feature") to the given commit SHA,
// creating it if it does not yet exist.
func UpdateRef(ref, sha string) error {
	_, err := Run("update-ref", ref, sha)
	return err
}

// RemoteURL returns the configured fetch URL of the named remote.
func RemoteURL(remote string) (string, error) {
	return Run("remote", "get-url", remote)
}

// CommitSubjects returns the subject lines of the commits in the local branch
// range base..branch, newest first.
func CommitSubjects(base, branch string) ([]string, error) {
	out, err := Run("log", "--format=%s", "refs/heads/"+base+".."+"refs/heads/"+branch)
	if err != nil {
		return nil, err
	}
	if out == "" {
		return nil, nil
	}
	return strings.Split(out, "\n"), nil
}
