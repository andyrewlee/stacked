// rebase.go — rebase lifecycle plumbing: onto-rebases (here and inside a
// linked worktree), paused-state probes, continue/abort, and the editor-free
// environment that makes conflict hand-off deterministic.
package git

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// RebaseOntoIn runs "git -C <dir> rebase --onto newBase oldBase branch" with no
// inherited stdio, so a branch checked out in the worktree at dir is rebased by
// its owner (git refuses to rebase a branch checked out in another worktree).
func RebaseOntoIn(dir, newBase, oldBase, branch string) error {
	if dir == "" {
		return fmt.Errorf("worktree dir is empty")
	}
	if err := validRefArg("ref", newBase); err != nil {
		return err
	}
	if err := validRefArg("ref", oldBase); err != nil {
		return err
	}
	if err := validRefArg("branch", branch); err != nil {
		return err
	}
	_, err := run("-C", dir, "rebase", "--quiet", "--onto", newBase, oldBase, branch)
	return err
}

// RebaseAbortIn aborts an in-progress rebase inside the worktree at dir.
func RebaseAbortIn(dir string) error {
	if dir == "" {
		return fmt.Errorf("worktree dir is empty")
	}
	_, err := run("-C", dir, "rebase", "--abort")
	return err
}

// RebaseOnto runs "git rebase --onto newBase oldBase branch" with inherited
// stdio so a conflict's details surface to the user. --quiet suppresses git's
// chatty success progress ("Rebasing (1/1)…Successfully rebased…") so that on a
// clean rebase the CLI's own one-line summary is the only output, while conflict
// messages (which --quiet does NOT silence) still reach the user.
func RebaseOnto(newBase, oldBase, branch string) error {
	if err := validRefArg("ref", newBase); err != nil {
		return err
	}
	if err := validRefArg("ref", oldBase); err != nil {
		return err
	}
	if err := validRefArg("branch", branch); err != nil {
		return err
	}
	return RunInteractive("rebase", "--quiet", "--onto", newBase, oldBase, branch)
}

// RebaseOntoQuiet runs rebase without inheriting stdout/stderr, for callers that
// need machine-readable output.
func RebaseOntoQuiet(newBase, oldBase, branch string) error {
	if err := validRefArg("ref", newBase); err != nil {
		return err
	}
	if err := validRefArg("ref", oldBase); err != nil {
		return err
	}
	if err := validRefArg("branch", branch); err != nil {
		return err
	}
	_, err := run("rebase", "--quiet", "--onto", newBase, oldBase, branch)
	return err
}

// RebaseInProgress reports whether a git rebase is currently in progress (for
// example, because it stopped on a merge conflict).
func RebaseInProgress() (bool, error) {
	gitDir, err := GitDir()
	if err != nil {
		return false, err
	}
	return rebaseInProgressAt(gitDir)
}

// RebaseInProgressIn reports whether a git rebase is in progress in the
// worktree at dir — including one this process did not start. Rebase metadata
// is per-worktree: it lives under that worktree's own git dir
// (.git/worktrees/<name>/ for a linked worktree), resolved via `git -C dir
// rev-parse --absolute-git-dir` rather than assumed from the caller's git dir.
func RebaseInProgressIn(dir string) (bool, error) {
	if dir == "" {
		return false, fmt.Errorf("worktree dir is empty")
	}
	gitDir, err := Run("-C", dir, "rev-parse", "--absolute-git-dir")
	if err != nil {
		return false, err
	}
	return rebaseInProgressAt(gitDir)
}

func rebaseInProgressAt(gitDir string) (bool, error) {
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
	return rebaseHeadNameAt(gitDir)
}

// RebaseHeadNameIn returns the branch a paused rebase in the worktree at dir
// targets, resolved through that worktree's own git dir — the metadata is
// per-worktree like RebaseInProgressIn's. A worktree mid-rebase reports
// "detached" in `git worktree list`, so head-name is the only way to learn
// which branch its --continue/--abort will eventually update-ref. Empty
// means no in-progress rebase was found or it names no branch.
func RebaseHeadNameIn(dir string) (string, error) {
	if dir == "" {
		return "", fmt.Errorf("worktree dir is empty")
	}
	gitDir, err := Run("-C", dir, "rev-parse", "--absolute-git-dir")
	if err != nil {
		return "", err
	}
	return rebaseHeadNameAt(gitDir)
}

func rebaseHeadNameAt(gitDir string) (string, error) {
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

// RebaseOntoSHA returns the commit the in-progress rebase is replaying onto,
// read from the worktree-local rebase metadata (rebase-merge/onto or
// rebase-apply/onto). The metadata lives under GitDir — not GitCommonDir — so
// a rebase paused inside a linked worktree resolves against that worktree's
// state. The recorded value must resolve to a commit; a missing/unreadable
// file and an unresolvable value are distinct actionable errors — a caller
// must never substitute the current parent ref for a target it cannot read.
func RebaseOntoSHA() (string, error) {
	gitDir, err := GitDir()
	if err != nil {
		return "", err
	}
	for _, dir := range []string{"rebase-merge", "rebase-apply"} {
		if _, err := os.Stat(filepath.Join(gitDir, dir)); err != nil {
			continue // this backend is not the one in progress
		}
		data, err := os.ReadFile(filepath.Join(gitDir, dir, "onto"))
		if err != nil {
			return "", fmt.Errorf("reading %s/onto: %w", dir, err)
		}
		target := strings.TrimSpace(string(data))
		sha, err := Run("rev-parse", "--verify", "--quiet", target+"^{commit}")
		if err != nil {
			return "", fmt.Errorf("%s/onto value %q does not resolve to a commit: %w", dir, target, err)
		}
		return sha, nil
	}
	return "", errors.New("no in-progress rebase found (rebase-merge/onto, rebase-apply/onto)")
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
	cmd.Env = append(gitEnv(), "GIT_EDITOR=true", "GIT_SEQUENCE_EDITOR=true")
	if err := spawnRun(cmd); err != nil {
		return fmt.Errorf("git rebase --continue: %w", err)
	}
	return nil
}

// RebaseContinueQuiet resumes a rebase without writing git's human output to
// stdout/stderr.
func RebaseContinueQuiet() error {
	cmd := exec.Command("git", "rebase", "--continue")
	cmd.Stdin = os.Stdin
	cmd.Env = append(gitEnv(), "GIT_EDITOR=true", "GIT_SEQUENCE_EDITOR=true")
	out, err := spawnCombined(cmd)
	if err != nil {
		msg := strings.TrimSpace(string(out))
		if msg != "" {
			return fmt.Errorf("git rebase --continue: %s: %w", redactCredentials(msg), err)
		}
		return fmt.Errorf("git rebase --continue: %w", err)
	}
	return nil
}

// RebaseAbort aborts an in-progress rebase, restoring the pre-rebase state.
func RebaseAbort() error {
	_, err := Run("rebase", "--abort")
	return err
}
