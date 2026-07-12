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
	"sort"
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

// TipsFor returns the tip SHA of the named local branches, keyed by branch
// name, using exact full-ref matches. Missing branches are omitted.
func TipsFor(names []string) (map[string]string, error) {
	unique, refs, err := scopedBranchRefs(names)
	if err != nil {
		return nil, err
	}
	tips := map[string]string{}
	if len(refs) == 0 {
		return tips, nil
	}
	args := append([]string{"for-each-ref", "--format=%(refname) %(objectname)"}, refs...)
	out, err := Run(args...)
	if err != nil {
		return nil, err
	}
	refToName := make(map[string]string, len(refs))
	for i, ref := range refs {
		refToName[ref] = unique[i]
	}
	for _, line := range strings.Split(out, "\n") {
		ref, sha, ok := strings.Cut(line, " ")
		if !ok {
			continue
		}
		if name, ok := refToName[ref]; ok {
			tips[name] = sha
		}
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

// TipSubjectsFor returns the subject line of each named local branch's tip
// commit, keyed by branch name, in a single exact-ref cat-file invocation.
// Missing branches and non-commit objects are omitted.
func TipSubjectsFor(names []string) (map[string]string, error) {
	unique, refs, err := scopedBranchRefs(names)
	if err != nil {
		return nil, err
	}
	subjects := map[string]string{}
	if len(refs) == 0 {
		return subjects, nil
	}
	out, err := catFile(refs, "--batch")
	if err != nil {
		return nil, err
	}
	pos := 0
	for _, name := range unique {
		header, next, ok := readCatFileLine(out, pos)
		if !ok {
			return nil, fmt.Errorf("git cat-file --batch ended before ref %q", name)
		}
		pos = next
		fields := strings.Fields(header)
		if len(fields) == 2 && fields[1] == "missing" {
			continue
		}
		if len(fields) != 3 {
			return nil, fmt.Errorf("unexpected git cat-file --batch header %q", header)
		}
		size, err := strconv.Atoi(fields[2])
		if err != nil || size < 0 {
			return nil, fmt.Errorf("unexpected git cat-file object size in header %q", header)
		}
		if pos+size > len(out) {
			return nil, fmt.Errorf("git cat-file --batch object for %q ended early", name)
		}
		body := out[pos : pos+size]
		pos += size
		if pos < len(out) && out[pos] == '\n' {
			pos++
		}
		if fields[1] != "commit" {
			continue
		}
		if subject, ok := commitSubject(body); ok {
			subjects[name] = subject
		}
	}
	if pos != len(out) {
		return nil, fmt.Errorf("git cat-file --batch returned trailing data")
	}
	return subjects, nil
}

func scopedBranchRefs(names []string) (unique, refs []string, err error) {
	seen := map[string]bool{}
	unique = make([]string, 0, len(names))
	refs = make([]string, 0, len(names))
	for _, name := range names {
		if err := validRefArg("branch", name); err != nil {
			return nil, nil, err
		}
		if seen[name] {
			continue
		}
		seen[name] = true
		unique = append(unique, name)
		refs = append(refs, localBranchNameRef(name))
	}
	return unique, refs, nil
}

func catFile(stdin []string, args ...string) ([]byte, error) {
	cmd := exec.Command("git", append([]string{"cat-file"}, args...)...)
	cmd.Env = gitEnv()
	input := strings.Join(stdin, "\n")
	if input != "" {
		input += "\n"
	}
	cmd.Stdin = strings.NewReader(input)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	if err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = strings.TrimSpace(stdout.String())
		}
		if msg != "" {
			return stdout.Bytes(), fmt.Errorf("git cat-file %s: %s: %w", strings.Join(args, " "), msg, err)
		}
		return stdout.Bytes(), fmt.Errorf("git cat-file %s: %w", strings.Join(args, " "), err)
	}
	return stdout.Bytes(), nil
}

func readCatFileLine(out []byte, pos int) (line string, next int, ok bool) {
	if pos >= len(out) {
		return "", pos, false
	}
	offset := bytes.IndexByte(out[pos:], '\n')
	if offset < 0 {
		return string(out[pos:]), len(out), true
	}
	end := pos + offset
	return string(out[pos:end]), end + 1, true
}

func commitSubject(body []byte) (string, bool) {
	_, message, ok := bytes.Cut(body, []byte("\n\n"))
	if !ok {
		return "", false
	}
	if end := bytes.IndexByte(message, '\n'); end >= 0 {
		message = message[:end]
	}
	return string(message), true
}

// Worktree describes a single git worktree linked to the repository, as
// reported by `git worktree list --porcelain`. The main worktree is included.
// Branch is the short branch name checked out there (empty when detached or
// bare); Head is the checked-out commit SHA.
// Hunk is one staged change region from `git diff --cached -U0`. Old* describe
// the pre-image (HEAD) side; New* the index side. OldN==0 marks a pure addition
// (no pre-image lines).
type Hunk struct {
	File     string `json:"file"`
	OldStart int    `json:"oldStart"`
	OldN     int    `json:"oldN"`
	NewStart int    `json:"newStart"`
	NewN     int    `json:"newN"`
}

// DiffCachedHunks returns the staged change regions from `git diff --cached
// -U0`, in diff order. The current file is tracked from +++ b/<path> lines; a
// deletion (+++ /dev/null) keeps the --- a/<path> name, since a deleted file's
// pre-image lines are still attributable.
func DiffCachedHunks() ([]Hunk, error) {
	out, err := run("diff", "--cached", "-U0")
	if err != nil {
		return nil, err
	}
	var hunks []Hunk
	file := ""
	pendingOld := ""
	for _, line := range strings.Split(out, "\n") {
		switch {
		case strings.HasPrefix(line, "--- a/"):
			pendingOld = strings.TrimPrefix(line, "--- a/")
		case strings.HasPrefix(line, "+++ b/"):
			file = strings.TrimPrefix(line, "+++ b/")
		case strings.HasPrefix(line, "+++ /dev/null"):
			file = pendingOld // deletion: the pre-image name is the touched file
		case strings.HasPrefix(line, "@@ "):
			h, ok := parseHunkHeader(line)
			if !ok || file == "" {
				continue
			}
			h.File = file
			hunks = append(hunks, h)
		}
	}
	return hunks, nil
}

// parseHunkHeader parses "@@ -<oldStart>[,<oldN>] +<newStart>[,<newN>] @@ ...";
// an absent ,<n> means n==1.
func parseHunkHeader(line string) (Hunk, bool) {
	rest := strings.TrimPrefix(line, "@@ ")
	end := strings.Index(rest, " @@")
	if end < 0 {
		return Hunk{}, false
	}
	fields := strings.Fields(rest[:end])
	if len(fields) != 2 || !strings.HasPrefix(fields[0], "-") || !strings.HasPrefix(fields[1], "+") {
		return Hunk{}, false
	}
	oldStart, oldN, ok1 := parseHunkRange(strings.TrimPrefix(fields[0], "-"))
	newStart, newN, ok2 := parseHunkRange(strings.TrimPrefix(fields[1], "+"))
	if !ok1 || !ok2 {
		return Hunk{}, false
	}
	return Hunk{OldStart: oldStart, OldN: oldN, NewStart: newStart, NewN: newN}, true
}

// parseHunkRange parses "<start>[,<n>]"; an absent ,<n> means n==1.
func parseHunkRange(spec string) (start, n int, ok bool) {
	n = 1
	numPart, countPart, hasCount := strings.Cut(spec, ",")
	start, err := strconv.Atoi(numPart)
	if err != nil {
		return 0, 0, false
	}
	if hasCount {
		n, err = strconv.Atoi(countPart)
		if err != nil {
			return 0, 0, false
		}
	}
	return start, n, true
}

// DiffCachedPatch returns the full staged patch with ZERO context lines
// (`git diff --cached -U0`) — the exact bytes absorb later lands on a target
// tip with AmendTipWithPatch. Zero context is load-bearing: blame attribution
// guarantees only the changed pre-image lines exist in the target's tree;
// surrounding context is typically owned by descendant commits and would make
// the apply fail there.
func DiffCachedPatch() ([]byte, error) {
	out, err := run("diff", "--cached", "-U0")
	if err != nil {
		return nil, err
	}
	return []byte(out), nil
}

// AmendTipWithPatch rewrites branch's tip commit to also contain patch,
// without touching any worktree or the real index: the patch is applied to
// the tip's tree in a throwaway temporary index, a new tree and commit are
// built with plumbing (write-tree/commit-tree, preserving the tip's author,
// date, message, and parents), and the branch ref is moved with a
// compare-and-swap on the old tip. On any failure — including "patch does not
// apply to that tree" — the repository is untouched. Returns the new tip SHA.
func AmendTipWithPatch(branch string, patch []byte) (string, error) {
	if err := validRefArg("branch", branch); err != nil {
		return "", err
	}
	ref := localBranchNameRef(branch)
	tip, err := RevParse(ref)
	if err != nil {
		return "", err
	}
	// rev-list --parents preserves merge tips (commit-tree -p per parent),
	// though stack tips are single-parent in practice.
	parentsOut, err := run("rev-list", "--parents", "-n1", tip)
	if err != nil {
		return "", err
	}
	parents := strings.Fields(parentsOut)[1:]
	if len(parents) == 0 {
		return "", fmt.Errorf("branch %q's tip is a root commit; cannot amend it via absorb", branch)
	}
	metaOut, err := run("log", "-1", "--format=%an%x00%ae%x00%aD%x00%B", tip)
	if err != nil {
		return "", err
	}
	meta := strings.SplitN(metaOut, "\x00", 4)
	if len(meta) != 4 {
		return "", fmt.Errorf("unexpected commit metadata for %s", tip)
	}

	tmp, err := os.CreateTemp("", "st-absorb-index-")
	if err != nil {
		return "", fmt.Errorf("creating temporary index: %w", err)
	}
	indexFile := tmp.Name()
	_ = tmp.Close()
	defer os.Remove(indexFile)
	indexEnv := []string{"GIT_INDEX_FILE=" + indexFile}
	if _, err := runWith(indexEnv, nil, "read-tree", tip); err != nil {
		return "", err
	}
	// --unidiff-zero matches DiffCachedPatch's -U0 capture; the hunk's own
	// pre-image lines (which attribution proved live in this tree) anchor it.
	if _, err := runWith(indexEnv, patch, "apply", "--cached", "--unidiff-zero", "-"); err != nil {
		return "", fmt.Errorf("staged patch does not apply cleanly to the tip of %q: %w", branch, err)
	}
	treeOut, err := runWith(indexEnv, nil, "write-tree")
	if err != nil {
		return "", err
	}
	commitEnv := []string{
		"GIT_AUTHOR_NAME=" + meta[0],
		"GIT_AUTHOR_EMAIL=" + meta[1],
		"GIT_AUTHOR_DATE=" + meta[2],
	}
	commitArgs := []string{"commit-tree", strings.TrimSpace(treeOut)}
	for _, p := range parents {
		commitArgs = append(commitArgs, "-p", p)
	}
	newTipOut, err := runWith(commitEnv, []byte(meta[3]), commitArgs...)
	if err != nil {
		return "", err
	}
	newTip := strings.TrimSpace(newTipOut)
	// Compare-and-swap on the old tip: a concurrent move of the branch fails
	// the whole amend instead of being clobbered.
	if _, err := run("update-ref", ref, newTip, tip); err != nil {
		return "", err
	}
	return newTip, nil
}

// ResetHardIn runs `git reset --hard <ref>` inside the worktree at dir (""
// means the current worktree). Callers must ensure nothing unsaved can be
// lost: absorb only calls it after the staged content is committed in the
// target branch (to drop the now-redundant staged copy), or on a
// verified-clean worktree to sync it to its amended HEAD.
func ResetHardIn(dir, ref string) error {
	if err := validRefArg("ref", ref); err != nil {
		return err
	}
	var args []string
	if dir != "" {
		args = append(args, "-C", dir)
	}
	args = append(args, "reset", "--hard", ref)
	_, err := run(args...)
	return err
}

// BlamePorcelain maps each final line of file at rev to the 40-hex SHA that
// last touched it, via one `git blame --porcelain` spawn. In porcelain output
// EVERY line gets a header `<40-hex> <origLine> <finalLine>[ <groupSize>]`
// (content lines start with a TAB and metadata lines with a keyword, so the
// hex prefix is unambiguous); only the SHA and finalLine are consumed.
func BlamePorcelain(file, rev string) (map[int]string, error) {
	if err := validRefArg("ref", rev); err != nil {
		return nil, err
	}
	out, err := run("blame", "--porcelain", rev, "--", file)
	if err != nil {
		return nil, err
	}
	lines := map[int]string{}
	for _, line := range strings.Split(out, "\n") {
		if len(line) < 42 || line[40] != ' ' {
			continue
		}
		sha := line[:40]
		if !isHex40(sha) {
			continue
		}
		fields := strings.Fields(line[41:])
		if len(fields) < 2 {
			continue
		}
		final, err := strconv.Atoi(fields[1])
		if err != nil {
			continue
		}
		lines[final] = sha
	}
	return lines, nil
}

func isHex40(s string) bool {
	if len(s) != 40 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

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

// MergeFFOnlyIn runs "git -C <dir> merge --ff-only upstream", advancing the
// branch checked out in the worktree at dir (its working tree included) the
// way running the merge inside that worktree would.
func MergeFFOnlyIn(dir, upstream string) error {
	if dir == "" {
		return fmt.Errorf("worktree dir is empty")
	}
	if err := validRefArg("ref", upstream); err != nil {
		return err
	}
	_, err := run("-C", dir, "merge", "--ff-only", upstream)
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

// CreateBranchAt creates a new local branch at ref without checking it out.
func CreateBranchAt(name, ref string) error {
	if err := validRefArg("branch", name); err != nil {
		return err
	}
	if err := validRefArg("ref", ref); err != nil {
		return err
	}
	_, err := Run("branch", name, ref)
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

// UpdateRefs sets every ref in updates to its SHA in a single
// `git update-ref -z --stdin` invocation. git applies the batch as one
// transaction: on any failure no ref is updated. An `update` directive
// creates a missing ref, which is what resurrects pruned branches on undo.
// Empty input is a no-op.
//
// The batch is NUL-framed (-z) and both refs and values are rejected if they
// carry whitespace/control bytes: the inputs come from the undo journal,
// which the threat model treats as potentially hostile, and a newline in a
// space/LF-framed record would inject a second directive into the
// transaction (the argv-framed single-ref UpdateRef never had that problem).
func UpdateRefs(updates map[string]string) error {
	if len(updates) == 0 {
		return nil
	}
	refs := make([]string, 0, len(updates))
	for ref, val := range updates {
		if err := validRefArg("ref", ref); err != nil {
			return err
		}
		if hasControlOrSpace(ref) {
			return fmt.Errorf("ref %q contains whitespace or control bytes", ref)
		}
		if hasControlOrSpace(val) {
			return fmt.Errorf("update value for %q contains whitespace or control bytes", ref)
		}
		refs = append(refs, ref)
	}
	sort.Strings(refs) // deterministic batch for tests and debuggability
	var b strings.Builder
	for _, ref := range refs {
		// -z record grammar: update SP <ref> NUL <newvalue> NUL <oldvalue> NUL.
		// The old-oid field must be PRESENT mid-batch (omitting it makes git
		// consume the next record as the old value) but EMPTY, which means "no
		// verification" — same semantics as the old newline form, verified
		// against real git: empty old-oid updates existing refs and creates
		// missing ones.
		fmt.Fprintf(&b, "update %s\x00%s\x00\x00", ref, updates[ref])
	}
	cmd := exec.Command("git", "update-ref", "-z", "--stdin")
	cmd.Env = gitEnv()
	cmd.Stdin = strings.NewReader(b.String())
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = strings.TrimSpace(stdout.String())
		}
		if msg != "" {
			return fmt.Errorf("git update-ref --stdin: %s: %w", msg, err)
		}
		return fmt.Errorf("git update-ref --stdin: %w", err)
	}
	return nil
}

// RemoteURL returns the configured fetch URL of the named remote.
func RemoteURL(remote string) (string, error) {
	return Run("remote", "get-url", remote)
}

// CommitSubjects returns the subject lines of the commits in the local branch
// range base..branch, newest first.
func CommitSubjects(base, branch string) ([]string, error) {
	if err := validRefArg("ref", base); err != nil {
		return nil, err
	}
	if err := validRefArg("branch", branch); err != nil {
		return nil, err
	}
	out, err := Run("log", "--format=%s", localBranchRef(base)+".."+localBranchNameRef(branch))
	if err != nil {
		return nil, err
	}
	if out == "" {
		return nil, nil
	}
	return strings.Split(out, "\n"), nil
}
