// refs.go — branch/ref resolution and mutation: existence probes, tip
// reads, ancestry/merge-base checks, checkout, create/delete/rename, and the
// scoped-update plumbing (UpdateRef/UpdateRefs) the engine checkpoints with.
package git

import (
	"bytes"
	"errors"
	"fmt"
	"os/exec"
	"sort"
	"strconv"
	"strings"
)

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

// ChangesContainedIn reports whether upstream's tree already contains every
// content change branch makes relative to their merge base — the squash-merge
// and fully-cherry-picked case that ancestry checks (MergedInto, `git cherry`)
// cannot see: branch's tip is no ancestor of upstream, yet merging it would
// add nothing.
//
// The check is exact tree-content equality, not a patch-id heuristic: P is the
// path set of `git diff --no-renames upstream...branch` (three-dot =
// merge-base to branch), and branch is contained iff `git diff --quiet
// upstream branch -- P` finds no disagreement on exactly those paths.
// --no-renames keeps BOTH sides of a rename in P: a branch renaming A to B
// also deletes A, and a path set listing only B would overlook that deletion
// when upstream merely copied A to B while keeping A. Equivalently, the
// three-way merge of
// branch into upstream would yield upstream's exact tree: at every contested
// path ours==theirs, so no conflict is possible and upstream's other changes
// are untouched. A branch whose squash-merge landed in a trunk that later
// moved those same files forward is NOT contained — the conservative
// direction, keeping the branch rather than guessing. An empty P (a branch
// carrying no net change) is contained. Two spawns, no worktree, no index.
func ChangesContainedIn(upstream, branch string) (bool, error) {
	if err := validRefArg("ref", upstream); err != nil {
		return false, err
	}
	if err := validRefArg("ref", branch); err != nil {
		return false, err
	}
	up := localBranchRef(upstream)
	br := localBranchRef(branch)
	out, err := run("diff", "--name-only", "--no-renames", "-z", up+"..."+br)
	if err != nil {
		return false, err
	}
	var paths []string
	for _, p := range strings.Split(out, "\x00") {
		if p != "" {
			paths = append(paths, p)
		}
	}
	if len(paths) == 0 {
		return true, nil
	}
	args := append([]string{"diff", "--quiet", up, br, "--"}, paths...)
	cmd := exec.Command("git", args...)
	// Path names came from git itself; literal pathspecs keep a file named like
	// pathspec magic (":(glob)" etc.) from being reinterpreted on the way back
	// in.
	cmd.Env = append(gitEnv(), "GIT_LITERAL_PATHSPECS=1")
	err = spawnRun(cmd)
	if err == nil {
		return true, nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
		return false, nil
	}
	return false, fmt.Errorf("git diff --quiet %s %s -- <changed paths>: %w", up, br, err)
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
	err := spawnRun(cmd)
	if err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = strings.TrimSpace(stdout.String())
		}
		if msg != "" {
			return stdout.Bytes(), fmt.Errorf("git cat-file %s: %s: %w", strings.Join(args, " "), redactCredentials(msg), err)
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

// DeleteBranches deletes the named local branches in ONE `git branch -D`
// invocation — a prune of N merged branches costs one spawn instead of N. git
// processes the arguments independently: it deletes every branch it can and
// reports the rest on stderr with a nonzero exit, so a non-nil error means a
// PARTIAL delete may already have happened. Callers needing the exact set
// must re-probe.
func DeleteBranches(names []string, force bool) error {
	for _, name := range names {
		if err := validRefArg("branch", name); err != nil {
			return err
		}
	}
	flag := "-d"
	if force {
		flag = "-D"
	}
	args := append([]string{"branch", flag}, names...)
	_, err := Run(args...)
	return err
}

// RevParse returns the full commit SHA that the given ref resolves to. The ref
// is rejected if it begins with "-" so a corrupt or hostile state.json value
// (e.g. a branch named "--git-dir") cannot be parsed by git as an option.
// Bare local branch names are qualified first — like MergedInto, IsAncestor,
// MergeBase, and ChangesContainedIn — so a tag or SHA-shaped string of the
// same name cannot shadow the branch.
func RevParse(ref string) (string, error) {
	if err := validRefArg("ref", ref); err != nil {
		return "", err
	}
	return Run("rev-parse", localBranchRef(ref))
}

// localBranchRef qualifies a bare local branch name to refs/heads/<name> so
// callers resolve it unambiguously — a tag or SHA-shaped string of the same
// name cannot shadow the branch. HEAD, already-qualified refs, and SHAs pass
// through unchanged. The existence probe is BranchExists (`git show-ref
// --verify`), an exact-ref read rather than a namespace listing, so each call
// is O(1) in ref count. Do not "optimize" this to `for-each-ref
// refs/heads/<name>`: that enumerates and formats matches (strictly more
// work) and matches at slash boundaries, so it would need an exact-match
// post-filter just to preserve today's semantics.
func localBranchRef(ref string) string {
	// A 40-hex value can never be a branch name (refname rules forbid it), so
	// it skips the show-ref probe outright.
	if ref == "HEAD" || strings.HasPrefix(ref, "refs/") || IsHex40(ref) {
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
	out, err := spawnCombined(cmd)
	if err == nil {
		return true, nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
		return false, nil
	}
	msg := strings.TrimSpace(string(out))
	if msg != "" {
		return false, fmt.Errorf("git merge-base --is-ancestor %s %s: %s: %w", ancestorRef, descendantRef, redactCredentials(msg), err)
	}
	return false, fmt.Errorf("git merge-base --is-ancestor %s %s: %w", ancestorRef, descendantRef, err)
}

// CommitRange returns the set of commit SHAs reachable from include but not
// from exclude — `git rev-list include ^exclude`, i.e. exclude..include — in
// one bounded invocation. The walk stops at the excluded history, so the cost
// is O(range size), not O(repo history). The "^" prefix on the exclude ref
// also means it can never be parsed as an option.
func CommitRange(exclude, include string) (map[string]bool, error) {
	if err := validRefArg("ref", exclude); err != nil {
		return nil, err
	}
	if err := validRefArg("ref", include); err != nil {
		return nil, err
	}
	out, err := Run("rev-list", localBranchRef(include), "^"+localBranchRef(exclude))
	if err != nil {
		return nil, err
	}
	return revListSet(out), nil
}

// revListSet splits rev-list output into a SHA set.
func revListSet(out string) map[string]bool {
	set := map[string]bool{}
	if out == "" {
		return set
	}
	for _, line := range strings.Split(out, "\n") {
		if line != "" {
			set[line] = true
		}
	}
	return set
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
	return runUpdateRefsStdin(b.String())
}

// RefUpdate is one ref change in a compare-and-swap batch. New is the object
// id the ref moves to (a full nonzero 40-hex oid — updates never delete).
// Old is the value the ref must currently hold for the move to apply:
//
//	""          → no verification (undo journals recorded before post-op tips)
//	zero oid    → the ref must NOT exist (resurrecting a branch the op deleted;
//	              if someone recreated it since, the update refuses rather
//	              than clobbering the external recreation)
//	<full oid>  → the ref must currently equal it (the post-op tip recorded
//	              in the undo journal; a mismatch means the branch moved
//	              outside st between the op and the undo)
type RefUpdate struct {
	New string
	Old string
}

// UpdateRefsCas applies every update as ONE `git update-ref -z --stdin`
// transaction with per-ref compare-and-swap semantics: each ref moves only if
// it currently matches the update's Old expectation, and one mismatch fails
// the whole batch with no ref moved. New must be a full nonzero object id and
// Old must be empty or a full object id — anything else is rejected before
// git runs, so a revision expression or delete value in the old/new field can
// never reach update-ref's resolver.
func UpdateRefsCas(updates map[string]RefUpdate) error {
	if len(updates) == 0 {
		return nil
	}
	refs := make([]string, 0, len(updates))
	for ref, u := range updates {
		if err := validRefArg("ref", ref); err != nil {
			return err
		}
		if hasControlOrSpace(ref) {
			return fmt.Errorf("ref %q contains whitespace or control bytes", ref)
		}
		if !IsHex40(u.New) || u.New == "0000000000000000000000000000000000000000" {
			return fmt.Errorf("update value for %q is not a full nonzero object id: %q", ref, u.New)
		}
		if u.Old != "" && !IsHex40(u.Old) {
			return fmt.Errorf("expected-old value for %q is not a full object id: %q", ref, u.Old)
		}
		refs = append(refs, ref)
	}
	sort.Strings(refs)
	var b strings.Builder
	for _, ref := range refs {
		// update SP <ref> NUL <newvalue> NUL <oldvalue> NUL — empty oldvalue
		// means "no verification", all-zeros means "must not exist".
		fmt.Fprintf(&b, "update %s\x00%s\x00%s\x00", ref, updates[ref].New, updates[ref].Old)
	}
	return runUpdateRefsStdin(b.String())
}

// runUpdateRefsStdin feeds a pre-built -z record batch to one
// `git update-ref --stdin` transaction.
func runUpdateRefsStdin(batch string) error {
	cmd := exec.Command("git", "update-ref", "-z", "--stdin")
	cmd.Env = gitEnv()
	cmd.Stdin = strings.NewReader(batch)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := spawnRun(cmd); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = strings.TrimSpace(stdout.String())
		}
		if msg != "" {
			return fmt.Errorf("git update-ref --stdin: %s: %w", redactCredentials(msg), err)
		}
		return fmt.Errorf("git update-ref --stdin: %w", err)
	}
	return nil
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
