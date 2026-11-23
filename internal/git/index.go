// index.go — index/working-tree plumbing: staged/unstaged probes,
// unmerged-file inventory, file staging, commits and amends.
package git

import (
	"errors"
	"fmt"
	"os/exec"
	"strings"
)

// UnmergedFiles returns the paths with unresolved merge conflicts — the files a
// paused rebase is waiting on — or nil when there are none. Newline output (not
// -z) is intentional: Run() trims and every parser here splits on "\n", so a NUL
// stream would leave a stray empty field.
func UnmergedFiles() ([]string, error) {
	return unmergedFilesIn("")
}

// UnmergedFilesIn is UnmergedFiles for the worktree at dir — the per-path probe
// `st log` runs against a worktree paused mid-rebase, where the conflicted
// files live.
func UnmergedFilesIn(dir string) ([]string, error) {
	if dir == "" {
		return nil, fmt.Errorf("worktree dir is empty")
	}
	return unmergedFilesIn(dir)
}

func unmergedFilesIn(dir string) ([]string, error) {
	args := []string{"diff", "--name-only", "--diff-filter=U"}
	if dir != "" {
		args = append([]string{"-C", dir}, args...)
	}
	out, err := Run(args...)
	if err != nil {
		return nil, err
	}
	if out == "" {
		return nil, nil
	}
	var files []string
	for _, line := range strings.Split(out, "\n") {
		if line != "" {
			files = append(files, line)
		}
	}
	return files, nil
}

// HasStagedChanges reports whether there are staged changes in the index.
func HasStagedChanges() (bool, error) {
	cmd := exec.Command("git", "diff", "--cached", "--quiet")
	cmd.Env = gitEnv()
	err := spawnRun(cmd)
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

// HasUnstagedChanges reports whether the working tree has unstaged changes to
// tracked files. Untracked files are deliberately ignored so scratch/plan files
// never block stack mutations or rebases.
func HasUnstagedChanges() (bool, error) {
	cmd := exec.Command("git", "diff", "--quiet")
	cmd.Env = gitEnv()
	err := spawnRun(cmd)
	if err == nil {
		return false, nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
		return true, nil
	}
	return false, fmt.Errorf("git diff --quiet: %w", err)
}

// Add stages the given paths. When no paths are provided it stages modified and
// deleted tracked files ("git add -u") so untracked scratch/plan files are
// never swept up accidentally.
func Add(paths ...string) error {
	if len(paths) == 0 {
		_, err := Run("add", "-u")
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

// AmendMessage amends the most recent commit, replacing its message. When
// all is true, modified and deleted tracked files are staged automatically.
func AmendMessage(message string, all bool) error {
	args := []string{"commit", "--amend", "-m", message}
	if all {
		args = append(args, "-a")
	}
	_, err := Run(args...)
	return err
}

// ResetSoft moves the current branch to ref without touching the index or
// working tree ("git reset --soft").
func ResetSoft(ref string) error {
	if err := validRefArg("ref", ref); err != nil {
		return err
	}
	_, err := Run("reset", "--soft", ref)
	return err
}
