// Package git is a thin wrapper around the git command line via os/exec.
// All functions operate on the git repository containing the current working
// directory.
package git

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

// gitEnv returns the environment for git invocations whose output is parsed:
// the current environment with the locale pinned to C, so git's messages and
// formatting never vary with the user's LANG/LC_* settings. Interactive
// invocations (RunInteractive, RebaseContinue) keep the inherited environment —
// their output goes to the user and is never parsed.
func gitEnv() []string {
	return append(os.Environ(), "LC_ALL=C")
}

// run executes "git args..." and returns the combined stdout/stderr output. On
// failure it returns an error whose message includes the git stderr so callers
// get an actionable diagnostic.
func run(args ...string) (string, error) {
	return runWith(nil, nil, args...)
}

// runWith is run with extra environment entries and an optional stdin payload,
// for plumbing calls (temp-index apply, commit-tree) that are driven by env
// vars and byte streams rather than flags.
func runWith(extraEnv []string, stdin []byte, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Env = append(gitEnv(), extraEnv...)
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	var stdout, stderr strings.Builder
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := spawnRun(cmd)
	if err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = strings.TrimSpace(stdout.String())
		}
		if msg != "" {
			return stdout.String(), fmt.Errorf("git %s: %s: %w", strings.Join(args, " "), redactCredentials(msg), err)
		}
		return stdout.String(), fmt.Errorf("git %s: %w", strings.Join(args, " "), err)
	}
	return stdout.String(), nil
}

// ok reports whether "git args..." exits successfully. It surfaces no error for
// a non-zero exit, which is used by predicate helpers like IsAncestor.
func ok(args ...string) bool {
	cmd := exec.Command("git", args...)
	cmd.Env = gitEnv()
	return spawnRun(cmd) == nil
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
	if err := spawnRun(cmd); err != nil {
		return fmt.Errorf("git %s: %w", strings.Join(args, " "), err)
	}
	return nil
}

// ErrDetachedHEAD is returned by CurrentBranch when HEAD is not on a branch.
var ErrDetachedHEAD = errors.New("not on a branch (detached HEAD); check out a branch first")

// validRefArg guards branch/remote names that are passed to git as bare
// positional arguments. Git itself forbids ref components that begin with "-"
// (check-ref-format), so any such value here is either corrupt state or an
// attempt to smuggle a flag (e.g. a state.json branch named "--exec=...").
// Rejecting it before exec keeps git from parsing data as options.
func validRefArg(kind, name string) error {
	if name == "" {
		return fmt.Errorf("%s name is empty", kind)
	}
	if name[0] == '-' {
		return fmt.Errorf("%s name %q is not a valid git ref name", kind, name)
	}
	return nil
}

// hasControlOrSpace reports whether s carries any byte that could break a
// record framing (space, C0 control incl. NL/NUL, or DEL). Legitimate git
// refnames and hex SHAs never contain these.
func hasControlOrSpace(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] <= 0x20 || s[i] == 0x7f {
			return true
		}
	}
	return false
}

// CheckBranchName reports whether name is a usable git branch name, deferring to
// git's own check-ref-format so the rules match exactly (no spaces, no "..",
// no trailing ".lock", etc.). It returns a friendly one-line error instead of
// letting the raw multi-line "fatal: ... is not a valid branch name" + advice
// hints leak out of `git branch`/`git checkout -b` further down the line.
func CheckBranchName(name string) error {
	if name == "" {
		return fmt.Errorf("branch name is empty")
	}
	if !ok("check-ref-format", "refs/heads/"+name) {
		return fmt.Errorf("%q is not a valid branch name", name)
	}
	return nil
}

func absPathFromGitOutput(path string) (string, error) {
	if filepath.IsAbs(path) {
		return filepath.Clean(path), nil
	}
	// Git emits relative repository paths from the process working directory,
	// not from the repository root.
	return filepath.Abs(path)
}

func isSingleAbsolutePath(path string) bool {
	return filepath.IsAbs(path) && !strings.Contains(path, "\n")
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
	if err == nil && isSingleAbsolutePath(dir) {
		return dir, nil
	}
	// Fall back for git < 2.31 (no --path-format): resolve a possibly
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

// MinVersion is the minimum git version st requires: the documented floor
// (README/CONTRIBUTING promise "Git 2.17+"). Worktree `-z` porcelain (2.36)
// and common-dir path resolution (2.31) have graceful fallbacks, so they are
// NOT part of the floor — bumping this is a docs-visible contract change.
var MinVersion = [3]int{2, 17, 0}

// RequireMinVersion verifies the git on PATH meets MinVersion, so an
// unsupported git fails at startup with a clear message rather than
// mid-command on an unrecognized flag or grammar.
func RequireMinVersion() error {
	out, err := Run("version")
	if err != nil {
		return fmt.Errorf("git version: %w", err)
	}
	found, err := parseGitVersion(out)
	if err != nil {
		return err
	}
	if !versionAtLeast(found, MinVersion) {
		return fmt.Errorf("st requires git >= %d.%d.%d (found %d.%d.%d)",
			MinVersion[0], MinVersion[1], MinVersion[2], found[0], found[1], found[2])
	}
	return nil
}

// parseGitVersion extracts the dotted numeric version from `git version`
// output ("git version 2.46.0", and distro-suffixed forms like
// "git version 2.39.5 (Apple Git-154)").
func parseGitVersion(out string) ([3]int, error) {
	fields := strings.Fields(strings.TrimSpace(out))
	for _, f := range fields {
		if f[0] < '0' || f[0] > '9' {
			continue
		}
		var v [3]int
		parts := strings.SplitN(f, ".", 4)
		if len(parts) < 2 {
			continue
		}
		ok := true
		for i := 0; i < 3; i++ {
			if i >= len(parts) {
				break
			}
			n, err := strconv.Atoi(parts[i])
			if err != nil {
				ok = false
				break
			}
			v[i] = n
		}
		if ok {
			return v, nil
		}
	}
	return [3]int{}, fmt.Errorf("cannot parse git version from %q", out)
}

// versionAtLeast reports whether found >= want, comparing major, minor, patch
// in order.
func versionAtLeast(found, want [3]int) bool {
	for i := 0; i < 3; i++ {
		if found[i] != want[i] {
			return found[i] > want[i]
		}
	}
	return true
}
