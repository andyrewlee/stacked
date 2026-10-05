// index.go — index/working-tree plumbing: staged/unstaged probes,
// unmerged-file inventory, file staging, commits and amends, check-ignore.
package git

import (
	"bytes"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
)

// LsFilesZ returns the tracked paths of the worktree at dir, parsed from one
// `git -C dir ls-files -z` — NUL-separated so paths with spaces or quotes
// survive byte-exact. A failure to run git is an error; dir must be a git
// worktree.
func LsFilesZ(dir string) ([]string, error) {
	out, err := run("-C", dir, "ls-files", "-z")
	if err != nil {
		return nil, err
	}
	return splitNUL(out), nil
}

// LsTreeZ returns the paths rev's tree tracks, recursively, parsed from one
// `git ls-tree -r -z --name-only rev` — the set a fresh `git worktree add` of
// that rev would materialize (gitlinks included, by name). It answers the
// tracked set of a worktree that does not exist yet.
func LsTreeZ(rev string) ([]string, error) {
	out, err := run("ls-tree", "-r", "-z", "--name-only", rev)
	if err != nil {
		return nil, fmt.Errorf("git ls-tree %s: %w", rev, err)
	}
	return splitNUL(out), nil
}

func splitNUL(out string) []string {
	var paths []string
	for _, p := range strings.Split(out, "\x00") {
		if p != "" {
			paths = append(paths, p)
		}
	}
	return paths
}

// CheckIgnored reports which of rels (paths relative to root) git ignores,
// from one batched `git -C root check-ignore -z --stdin`. A single path git
// cannot classify (for example one beyond a symlinked directory) poisons the
// whole batch, so a non-clean batch failure falls back to per-entry probes: a
// path git cannot classify counts as not ignored.
//
// The boundary contract: rels arrive in native filepath.Rel form (backslashes
// on Windows) while git's pathspec convention is slash-separated on every
// platform, so the probe runs in slash form and the result map is keyed back
// to the native rels callers look up. (-z makes the echo verbatim, so the
// slash forms map back unambiguously.)
func CheckIgnored(root string, rels []string) (map[string]bool, error) {
	ignored := make(map[string]bool, len(rels))
	if len(rels) == 0 {
		return ignored, nil
	}
	slashed := make([]string, len(rels))
	bySlash := make(map[string]string, len(rels))
	for i, rel := range rels {
		slashed[i] = filepath.ToSlash(rel)
		bySlash[slashed[i]] = rel
	}
	cmd := exec.Command("git", "-C", root, "check-ignore", "-z", "--stdin")
	cmd.Env = gitEnv()
	cmd.Stdin = bytes.NewReader(checkIgnoreStdin(slashed))
	out, err := spawnOutput(cmd)
	if err != nil {
		var exitErr *exec.ExitError
		if !errors.As(err, &exitErr) {
			return nil, fmt.Errorf("git check-ignore: %w", err)
		}
		switch exitErr.ExitCode() {
		case 1:
			return ignored, nil // none of the paths are ignored
		default:
			for i, rel := range rels {
				if checkIgnored(root, slashed[i]) {
					ignored[rel] = true
				}
			}
			return ignored, nil
		}
	}
	for _, p := range strings.Split(string(out), "\x00") {
		if p == "" {
			continue
		}
		if rel, ok := bySlash[p]; ok {
			ignored[rel] = true
		} else {
			ignored[p] = true
		}
	}
	return ignored, nil
}

// checkIgnoreStdin builds the -z --stdin payload: the slash-formed rels,
// NUL-separated with a trailing NUL (each entry becomes one pathspec).
func checkIgnoreStdin(slashed []string) []byte {
	return []byte(strings.Join(slashed, "\x00") + "\x00")
}

// checkIgnored reports whether rel (relative to root) is ignored by git, via
// `git -C root check-ignore`. check-ignore exits 0 when the path is ignored,
// 1 when it is not, so a nil error means ignored. It is the per-entry fallback
// when the batched CheckIgnored probe hits a fatal entry.
func checkIgnored(root, rel string) bool {
	return ok("-C", root, "check-ignore", "-q", "--", rel)
}

// UnmergedFiles returns the paths with unresolved merge conflicts — the files a
// paused rebase is waiting on — or nil when there are none. Newline output (not
// -z) is intentional: Run() trims and every parser here splits on "\n", so a NUL
// stream would leave a stray empty field.
func UnmergedFiles() ([]string, error) {
	out, err := Run("diff", "--name-only", "--diff-filter=U")
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

// HasUnstagedChanges reports whether the working tree has unstaged tracked
// changes or untracked files.
func HasUnstagedChanges() (bool, error) {
	cmd := exec.Command("git", "diff", "--quiet")
	cmd.Env = gitEnv()
	err := spawnRun(cmd)
	if err == nil {
		out, err := Run("ls-files", "--others", "--exclude-standard")
		if err != nil {
			return false, err
		}
		return out != "", nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
		return true, nil
	}
	return false, fmt.Errorf("git diff --quiet: %w", err)
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

// ResetSoft moves the current branch to ref without touching the index or
// working tree ("git reset --soft").
func ResetSoft(ref string) error {
	if err := validRefArg("ref", ref); err != nil {
		return err
	}
	_, err := Run("reset", "--soft", ref)
	return err
}
