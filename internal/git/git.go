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
	cmd := exec.Command("git", args...)
	cmd.Env = gitEnv()
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
	cmd := exec.Command("git", args...)
	cmd.Env = gitEnv()
	return cmd.Run() == nil
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

// Tips returns the tip SHA of every local branch, keyed by branch name, in a
// single git invocation — so callers walking a whole forest (log, validate) do
// one spawn instead of two per branch. Full refnames are requested and the
// refs/heads/ prefix stripped, since short refnames can be ambiguous when a
// tag shares a branch's name.
func Tips() (map[string]string, error) {
	out, err := Run("for-each-ref", "--format=%(refname) %(objectname)", "refs/heads")
	if err != nil {
		return nil, err
	}
	tips := map[string]string{}
	if out == "" {
		return tips, nil
	}
	for _, line := range strings.Split(out, "\n") {
		ref, sha, ok := strings.Cut(line, " ")
		if !ok {
			continue
		}
		tips[strings.TrimPrefix(ref, "refs/heads/")] = sha
	}
	return tips, nil
}

// MergedInto returns the local branch names whose tips are ancestors of ref in
// one git invocation, matching `merge-base --is-ancestor <branch> <ref>` for
// every local branch.
func MergedInto(ref string) (map[string]bool, error) {
	if err := validRefArg("ref", ref); err != nil {
		return nil, err
	}
	out, err := Run("for-each-ref", "--format=%(refname)", "--merged", localBranchRef(ref), "refs/heads")
	if err != nil {
		return nil, err
	}
	merged := map[string]bool{}
	if out == "" {
		return merged, nil
	}
	for _, line := range strings.Split(out, "\n") {
		if line == "" {
			continue
		}
		merged[strings.TrimPrefix(line, "refs/heads/")] = true
	}
	return merged, nil
}

// TipSubjects returns the subject line of every local branch's tip commit,
// keyed by branch name, in a single git invocation — so a caller rendering a
// whole forest (log) does one spawn instead of one `git log` per branch. A NUL
// byte separates the refname from the subject so subjects containing spaces are
// parsed unambiguously.
func TipSubjects() (map[string]string, error) {
	out, err := Run("for-each-ref", "--format=%(refname)%00%(contents:subject)", "refs/heads")
	if err != nil {
		return nil, err
	}
	subjects := map[string]string{}
	if out == "" {
		return subjects, nil
	}
	for _, line := range strings.Split(out, "\n") {
		ref, subject, ok := strings.Cut(line, "\x00")
		if !ok {
			continue
		}
		subjects[strings.TrimPrefix(ref, "refs/heads/")] = subject
	}
	return subjects, nil
}

// Worktree describes a single git worktree linked to the repository, as
// reported by `git worktree list --porcelain`. The main worktree is included.
// Branch is the short branch name checked out there (empty when detached or
// bare); Head is the checked-out commit SHA.
type Worktree struct {
	Path     string `json:"path"`
	Branch   string `json:"branch,omitempty"`
	Head     string `json:"head,omitempty"`
	Bare     bool   `json:"bare,omitempty"`
	Detached bool   `json:"detached,omitempty"`
	Locked   bool   `json:"locked,omitempty"`
}

// Worktrees lists every worktree linked to the repository (the main worktree
// plus any added with `git worktree add`), in a single git invocation. Records
// in --porcelain output are blank-line separated; each begins with a "worktree
// <path>" line followed by attribute lines ("HEAD <sha>", "branch
// refs/heads/<name>", "detached", "bare", "locked"). The refs/heads/ prefix is
// stripped so Branch is the short name, matching Tips().
func Worktrees() ([]Worktree, error) {
	out, err := Run("worktree", "list", "--porcelain")
	if err != nil {
		return nil, err
	}
	return parseWorktrees(out), nil
}

// parseWorktrees parses the raw `git worktree list --porcelain` output into the
// Worktree slice. It is split out from the git invocation so the porcelain
// grammar (including the bare and locked arms) can be exercised against canned
// fixtures without spawning git.
func parseWorktrees(out string) []Worktree {
	if out == "" {
		return nil
	}
	var (
		list []Worktree
		cur  *Worktree
	)
	flush := func() {
		if cur != nil {
			list = append(list, *cur)
			cur = nil
		}
	}
	for _, line := range strings.Split(out, "\n") {
		if line == "" {
			flush()
			continue
		}
		key, val, _ := strings.Cut(line, " ")
		switch key {
		case "worktree":
			flush()
			cur = &Worktree{Path: val}
		case "HEAD":
			if cur != nil {
				cur.Head = val
			}
		case "branch":
			if cur != nil {
				cur.Branch = strings.TrimPrefix(val, "refs/heads/")
			}
		case "detached":
			if cur != nil {
				cur.Detached = true
			}
		case "bare":
			if cur != nil {
				cur.Bare = true
			}
		case "locked":
			if cur != nil {
				cur.Locked = true
			}
		}
	}
	flush()
	return list
}

// WorktreeAdd creates a new linked worktree at path checked out on the existing
// branch. The "--" separates options from the path/branch operands so a path
// beginning with a dash can never be read as an option.
func WorktreeAdd(path, branch string) error {
	if err := validRefArg("branch", branch); err != nil {
		return err
	}
	if path == "" {
		return fmt.Errorf("worktree path is empty")
	}
	_, err := Run("worktree", "add", "--", path, branch)
	return err
}

// WorktreeRemove removes the linked worktree at path. When force is true the
// worktree is removed even if it has uncommitted changes.
func WorktreeRemove(path string, force bool) error {
	if path == "" {
		return fmt.Errorf("worktree path is empty")
	}
	args := []string{"worktree", "remove"}
	if force {
		args = append(args, "--force")
	}
	args = append(args, "--", path)
	_, err := Run(args...)
	return err
}

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

// IsCleanIn reports whether the worktree at dir has no staged or unstaged
// changes, via `git -C <dir> status --porcelain`. It is the port-level twin of
// IsCleanAt.
func IsCleanIn(dir string) (bool, error) {
	return IsCleanAt(dir)
}

// Checkout switches the working tree to the named branch.
func Checkout(name string) error {
	if err := validRefArg("branch", name); err != nil {
		return err
	}
	_, err := Run("checkout", name)
	return err
}

// CheckoutDetach detaches HEAD at ref without switching to a branch.
func CheckoutDetach(ref string) error {
	if err := validRefArg("ref", ref); err != nil {
		return err
	}
	_, err := Run("checkout", "--detach", ref)
	return err
}

// CreateBranch creates a new branch off the current HEAD and switches to it.
func CreateBranch(name string) error {
	if err := validRefArg("branch", name); err != nil {
		return err
	}
	_, err := Run("checkout", "-b", name)
	return err
}

// DeleteBranch deletes the named local branch. When force is true the branch is
// removed even if it is not fully merged.
func DeleteBranch(name string, force bool) error {
	if err := validRefArg("branch", name); err != nil {
		return err
	}
	flag := "-d"
	if force {
		flag = "-D"
	}
	_, err := Run("branch", flag, name)
	return err
}

// RevParse returns the full commit SHA that the given ref resolves to. The ref
// is rejected if it begins with "-" so a corrupt or hostile state.json value
// (e.g. a branch named "--git-dir") cannot be parsed by git as an option.
func RevParse(ref string) (string, error) {
	if err := validRefArg("ref", ref); err != nil {
		return "", err
	}
	return Run("rev-parse", ref)
}

func localBranchRef(ref string) string {
	if ref == "HEAD" || strings.HasPrefix(ref, "refs/") {
		return ref
	}
	if BranchExists(ref) {
		return "refs/heads/" + ref
	}
	return ref
}

func localBranchNameRef(name string) string {
	return "refs/heads/" + name
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

// IsClean reports whether the working tree has no staged or unstaged changes,
// i.e. "git status --porcelain" produces no output.
func IsClean() (bool, error) {
	out, err := Run("status", "--porcelain")
	if err != nil {
		return false, err
	}
	return out == "", nil
}

// IsCleanAt reports whether the working tree at dir has no staged or unstaged
// changes, by running `git -C <dir> status --porcelain`. It exists so callers
// can report the dirty state of a LINKED worktree (a different directory than
// the process cwd) — the only place git -C is needed for the worktree feature.
func IsCleanAt(dir string) (bool, error) {
	out, err := Run("-C", dir, "status", "--porcelain")
	if err != nil {
		return false, err
	}
	return out == "", nil
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

// HasUnstagedChanges reports whether the working tree has unstaged tracked
// changes or untracked files.
func HasUnstagedChanges() (bool, error) {
	cmd := exec.Command("git", "diff", "--quiet")
	cmd.Env = gitEnv()
	err := cmd.Run()
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

// RebaseContinueQuiet resumes a rebase without writing git's human output to
// stdout/stderr.
func RebaseContinueQuiet() error {
	cmd := exec.Command("git", "rebase", "--continue")
	cmd.Stdin = os.Stdin
	cmd.Env = append(gitEnv(), "GIT_EDITOR=true", "GIT_SEQUENCE_EDITOR=true")
	out, err := cmd.CombinedOutput()
	if err != nil {
		msg := strings.TrimSpace(string(out))
		if msg != "" {
			return fmt.Errorf("git rebase --continue: %s: %w", msg, err)
		}
		return fmt.Errorf("git rebase --continue: %w", err)
	}
	return nil
}

// Fetch updates remote-tracking refs for the named remote.
func Fetch(remote string) error {
	if err := validRefArg("remote", remote); err != nil {
		return err
	}
	_, err := Run("fetch", remote)
	return err
}

// PushRemote pushes the given branch to remote and records it as the branch's
// upstream (-u) so ahead/behind tracking works after the first publish. When
// force is true it uses --force-with-lease for a safe force push.
func PushRemote(remote, branch string, force bool) error {
	if err := validRefArg("remote", remote); err != nil {
		return err
	}
	args := []string{"push", "-u"}
	if force {
		args = append(args, "--force-with-lease")
	}
	refspec := "refs/heads/" + branch + ":refs/heads/" + branch
	args = append(args, remote, refspec)
	_, err := Run(args...)
	return err
}

// PushBranches pushes the given branches to remote in a single git invocation
// and records upstreams (-u) for each branch.
func PushBranches(remote string, branches []string, force bool) error {
	if err := validRefArg("remote", remote); err != nil {
		return err
	}
	for _, branch := range branches {
		if err := validRefArg("branch", branch); err != nil {
			return err
		}
	}
	if len(branches) == 0 {
		return nil
	}

	args := []string{"push", "-u"}
	if force {
		args = append(args, "--force-with-lease")
	}
	args = append(args, remote)
	for _, branch := range branches {
		refspec := "refs/heads/" + branch + ":refs/heads/" + branch
		args = append(args, refspec)
	}
	_, err := Run(args...)
	return err
}

// RemoteExists reports whether a remote with the given name is configured.
func RemoteExists(name string) bool {
	return ok("remote", "get-url", name)
}

// MergeBase returns the best common ancestor commit of the two given refs.
func MergeBase(a, b string) (string, error) {
	if err := validRefArg("ref", a); err != nil {
		return "", err
	}
	if err := validRefArg("ref", b); err != nil {
		return "", err
	}
	return Run("merge-base", localBranchRef(a), localBranchRef(b))
}

// IsAncestor reports whether ancestor is an ancestor of descendant. A valid
// negative answer returns false, nil; invalid refs and other git failures return
// an error.
func IsAncestor(ancestor, descendant string) (bool, error) {
	if err := validRefArg("ref", ancestor); err != nil {
		return false, err
	}
	if err := validRefArg("ref", descendant); err != nil {
		return false, err
	}
	ancestorRef := localBranchRef(ancestor)
	descendantRef := localBranchRef(descendant)
	cmd := exec.Command("git", "merge-base", "--is-ancestor", ancestorRef, descendantRef)
	cmd.Env = gitEnv()
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
		return false, fmt.Errorf("git merge-base --is-ancestor %s %s: %s: %w", ancestorRef, descendantRef, msg, err)
	}
	return false, fmt.Errorf("git merge-base --is-ancestor %s %s: %w", ancestorRef, descendantRef, err)
}

// AncestorSet returns the set of commit SHAs reachable from ref (ref itself and
// all its ancestors) in one git invocation, so a caller can answer many
// ancestry questions about the same ref with map lookups instead of one
// `merge-base --is-ancestor` spawn per question.
func AncestorSet(ref string) (map[string]bool, error) {
	if err := validRefArg("ref", ref); err != nil {
		return nil, err
	}
	out, err := Run("rev-list", localBranchRef(ref))
	if err != nil {
		return nil, err
	}
	set := map[string]bool{}
	if out == "" {
		return set, nil
	}
	for _, line := range strings.Split(out, "\n") {
		if line != "" {
			set[line] = true
		}
	}
	return set, nil
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

// RebaseAbort aborts an in-progress rebase, restoring the pre-rebase state.
func RebaseAbort() error {
	_, err := Run("rebase", "--abort")
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

// RenameBranch renames a local branch ("git branch -m old new").
func RenameBranch(oldName, newName string) error {
	if err := validRefArg("branch", oldName); err != nil {
		return err
	}
	if err := validRefArg("branch", newName); err != nil {
		return err
	}
	_, err := Run("branch", "-m", oldName, newName)
	return err
}

// ForceBranch points the branch name at ref without checking it out
// ("git branch -f name ref"). It refuses to move the currently checked-out
// branch, matching git's own behavior.
func ForceBranch(name, ref string) error {
	if err := validRefArg("branch", name); err != nil {
		return err
	}
	if err := validRefArg("ref", ref); err != nil {
		return err
	}
	_, err := Run("branch", "-f", name, ref)
	return err
}

// UpdateRef sets a ref (e.g. "refs/heads/feature") to the given commit SHA,
// creating it if it does not yet exist. The ref is rejected if it begins with
// "-", and "--" terminates option parsing so neither the ref nor the SHA can be
// interpreted by git as an option.
func UpdateRef(ref, sha string) error {
	if err := validRefArg("ref", ref); err != nil {
		return err
	}
	_, err := Run("update-ref", "--", ref, sha)
	return err
}

// RemoteURL returns the configured fetch URL of the named remote.
func RemoteURL(remote string) (string, error) {
	return Run("remote", "get-url", remote)
}

// CommitSubjects returns the subject lines of the commits in the local branch
// range base..branch, newest first.
func CommitSubjects(base, branch string) ([]string, error) {
	out, err := Run("log", "--format=%s", localBranchRef(base)+".."+localBranchNameRef(branch))
	if err != nil {
		return nil, err
	}
	if out == "" {
		return nil, nil
	}
	return strings.Split(out, "\n"), nil
}
