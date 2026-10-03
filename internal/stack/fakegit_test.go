package stack

import (
	"fmt"
	"strings"
	"testing"

	"github.com/andyrewlee/stacked/internal/git"
)

// fakeCommit is a node in the in-memory commit DAG.
type fakeCommit struct {
	id      string
	parent  string // parent commit id ("" for the root)
	subject string
	// content is the fake's stand-in for the commit's tree diff: a set of
	// opaque tokens the commit introduces. The default is the commit's own id,
	// so every commit carries something unique and a branch is "contained" in
	// a ref only when a test lands those tokens on the ref's history — e.g.
	// squashInto, which models a host-side squash-merge as one commit holding
	// the union of the branch's tokens.
	content map[string]bool
}

// fakeGit is an in-memory implementation of the Git port. It models a commit DAG
// and branch refs faithfully enough to exercise the restack engine without
// spawning git. Rebases never conflict (conflict handling is covered by the
// real-git integration and e2e suites); this fake exists to prove the engine's
// topology/parentSHA bookkeeping under thousands of random operations.
type fakeGit struct {
	commits  map[string]*fakeCommit
	branches map[string]string // branch -> tip commit id
	// remoteRefs models fetched remote-tracking refs ("refs/remotes/<r>/<b>")
	// so tests can point a prune basis at them without a real remote.
	remoteRefs map[string]string
	head       string // current branch
	seq        int

	// conflict modeling: a branch in conflictNext stops mid-rebase the next time
	// it is rebased, mirroring a real merge conflict that the caller resolves with
	// RebaseContinue.
	conflictNext map[string]bool
	// conflictEvery stalls EVERY rebase of the branch (not just the next), so
	// a test can keep a branch conflicting after the paused rebase completes —
	// e.g. a catch-up cascade rebase that must also stall.
	conflictEvery   map[string]bool
	rebaseActive    bool
	rebaseRestall   bool // when set, RebaseContinue fails and leaves the rebase paused
	rebaseBranch    string
	rebaseNewBase   string
	rebaseOldBase   string
	rebaseAbortErr  error
	rebaseOntoErr   error // when set, RebaseOntoSHA fails while the rebase stays paused
	rebaseLog       []rebaseCall
	staged          bool
	clean           bool
	checkoutErr     map[string]error
	deleteErr       map[string]error
	rebaseErr       map[string]error
	commitErr       error
	isAncestorCalls int
	mergedIntoCalls int
	mergedIntoRefs  []string
	// detachedAt is the commit a CheckoutDetach left HEAD on ("" when HEAD is
	// on a branch).
	detachedAt string

	// linkedWorktrees models extra (non-main) worktrees by branch -> path. The
	// main worktree (f.head) is synthesized in Worktrees().
	linkedWorktrees map[string]string
	// stagedHunks/stagedUnsupported/blame are the canned inputs for the
	// absorb attribution: DiffCachedHunks returns stagedHunks plus
	// stagedUnsupported; BlamePorcelain returns blame[file].
	stagedHunks       []git.Hunk
	stagedUnsupported []git.UnsupportedRecord
	blame             map[string]map[int]git.BlameLine
	// stagedPatch is the canned DiffCachedPatch payload; applyErr, when set,
	// makes AmendTipWithPatch fail like a patch that does not apply to the
	// target's tree (nothing mutated). resetHardDirs records the ResetHardIn
	// calls ("" = the current worktree) so tests can pin the absorb sequence.
	stagedPatch   []byte
	applyErr      error
	resetHardDirs []string
	// dirtyWT marks linked worktrees (by branch) as having a dirty tree, so
	// IsCleanIn can model a skipped dependent in the cascade tests.
	dirtyWT map[string]bool
	// rebaseInWT marks worktree dirs (the Path Worktrees reports — "." is the
	// main worktree) with a paused rebase, whether st-armed or armed directly
	// by the test as a foreign rebase st must not touch. rebaseWT records
	// which dir the globally modeled rebaseActive state lives in, so aborts
	// and continues clear the right per-dir entry.
	rebaseInWT map[string]bool
	rebaseWT   string
	// repoRoot is what RepoRoot reports — the top level of the worktree the
	// test's cwd is meant to sit in. "" means "the main worktree" (the fake
	// does not model a path for it), so a guard comparing against a linked
	// worktree's Path sees not-inside; set it to an owner's Path to exercise
	// the you-are-inside-it refusals.
	repoRoot string

	// failErr forces method-level errors beyond the dedicated maps: the key
	// is the method name (e.g. "RebaseInProgress", "MergeBase"). failAfter
	// delays the failure until the method has been called more than N times,
	// modeling "first probe OK, second probe dies" windows. calls counts
	// every invocation of a fail-instrumented method (so tests can assert a
	// method was never reached, e.g. calls["UpdateRefs"] == 0).
	//
	// Convention: every new port method gets an `f.fail("Method")` guard at
	// its top, so future failure-path tests never need a bespoke knob.
	failErr   map[string]error
	failAfter map[string]int
	calls     map[string]int
}

func newFakeGit() *fakeGit {
	f := &fakeGit{
		commits:         map[string]*fakeCommit{},
		branches:        map[string]string{},
		remoteRefs:      map[string]string{},
		conflictNext:    map[string]bool{},
		conflictEvery:   map[string]bool{},
		checkoutErr:     map[string]error{},
		deleteErr:       map[string]error{},
		rebaseErr:       map[string]error{},
		failErr:         map[string]error{},
		failAfter:       map[string]int{},
		calls:           map[string]int{},
		clean:           true,
		linkedWorktrees: map[string]string{},
		rebaseInWT:      map[string]bool{},
	}
	id := f.newID()
	f.commits[id] = &fakeCommit{id: id, subject: "init", content: map[string]bool{id: true}}
	f.branches["main"] = id
	f.head = "main"
	return f
}

// conflictOn makes the next rebase of branch stop on a conflict.
func (f *fakeGit) conflictOn(branch string) { f.conflictNext[branch] = true }

// alwaysConflictOn makes every rebase of branch stop on a conflict.
func (f *fakeGit) alwaysConflictOn(branch string) { f.conflictEvery[branch] = true }

// rebaseCall records a RebaseOnto attempt's unresolved arguments so tests can
// pin which target each rebase replayed onto.
type rebaseCall struct{ newBase, oldBase, branch string }

// newID mints fake commit ids as full lowercase 40-hex object ids, matching
// the shape real SHAs take anywhere values are validated or compared — the
// fake must not pass journal/OID barriers on credentials real git lacks.
func (f *fakeGit) newID() string {
	f.seq++
	return fmt.Sprintf("%040x", f.seq)
}

// resolve turns a ref (branch name, commit id, or HEAD) into a commit id.
func (f *fakeGit) resolve(ref string) string {
	if ref == "HEAD" {
		if f.head == "" {
			return f.detachedAt
		}
		return f.branches[f.head]
	}
	ref = strings.TrimPrefix(ref, "refs/heads/")
	if tip, ok := f.branches[ref]; ok {
		return tip
	}
	if tip, ok := f.remoteRefs[ref]; ok {
		return tip
	}
	if _, ok := f.commits[ref]; ok {
		return ref
	}
	return ""
}

// fail returns the injected error for method once its allowed-call budget is
// exhausted, and counts every invocation under calls. A nil map entry means
// "never fail" — the guard costs nothing when the knobs are unset.
func (f *fakeGit) fail(method string) error {
	f.calls[method]++
	if n, ok := f.failAfter[method]; ok && f.calls[method] <= n {
		return nil
	}
	return f.failErr[method]
}

func (f *fakeGit) RevParse(ref string) (string, error) {
	if err := f.fail("RevParse"); err != nil {
		return "", err
	}
	if id := f.resolve(ref); id != "" {
		return id, nil
	}
	return "", fmt.Errorf("unknown revision %q", ref)
}

func (f *fakeGit) CurrentBranch() (string, error) {
	if err := f.fail("CurrentBranch"); err != nil {
		return "", err
	}
	if f.head == "" {
		return "", fmt.Errorf("detached HEAD")
	}
	return f.head, nil
}

func (f *fakeGit) BranchExists(name string) bool {
	f.calls["BranchExists"]++
	_, ok := f.branches[name]
	return ok
}

func (f *fakeGit) Tips() (map[string]string, error) {
	if err := f.fail("Tips"); err != nil {
		return nil, err
	}
	tips := make(map[string]string, len(f.branches))
	for name, tip := range f.branches {
		tips[name] = tip
	}
	return tips, nil
}

func (f *fakeGit) TipsFor(names []string) (map[string]string, error) {
	if err := f.fail("TipsFor"); err != nil {
		return nil, err
	}
	tips := map[string]string{}
	seen := map[string]bool{}
	for _, name := range names {
		if seen[name] {
			continue
		}
		seen[name] = true
		if tip, ok := f.branches[name]; ok {
			tips[name] = tip
		}
	}
	return tips, nil
}

type tipReadSpyGit struct {
	Git
	revParseCalls      int
	tipsCalls          int
	tipsForCalls       int
	currentBranchCalls int
	commitRangeCalls   int
	blameCalls         int
	worktreesCalls     int
	tipsForNames       [][]string
}

func (g *tipReadSpyGit) RevParse(ref string) (string, error) {
	g.revParseCalls++
	return g.Git.RevParse(ref)
}

func (g *tipReadSpyGit) CurrentBranch() (string, error) {
	g.currentBranchCalls++
	return g.Git.CurrentBranch()
}

func (g *tipReadSpyGit) Tips() (map[string]string, error) {
	g.tipsCalls++
	return g.Git.Tips()
}

func (g *tipReadSpyGit) TipsFor(names []string) (map[string]string, error) {
	g.tipsForCalls++
	g.tipsForNames = append(g.tipsForNames, append([]string(nil), names...))
	return g.Git.TipsFor(names)
}

func (g *tipReadSpyGit) CommitRange(exclude, include string) (map[string]bool, error) {
	g.commitRangeCalls++
	return g.Git.CommitRange(exclude, include)
}

func (g *tipReadSpyGit) BlamePorcelain(file, rev string) (map[int]git.BlameLine, error) {
	g.blameCalls++
	return g.Git.BlamePorcelain(file, rev)
}

func (g *tipReadSpyGit) Worktrees() ([]git.Worktree, error) {
	g.worktreesCalls++
	return g.Git.Worktrees()
}

func (f *fakeGit) MergedInto(ref string) (map[string]bool, error) {
	f.mergedIntoCalls++
	f.mergedIntoRefs = append(f.mergedIntoRefs, ref)
	if err := f.fail("MergedInto"); err != nil {
		return nil, err
	}
	target := f.resolve(ref)
	if target == "" {
		return nil, fmt.Errorf("unknown revision %q", ref)
	}
	merged := map[string]bool{}
	for name, tip := range f.branches {
		for cur := target; cur != ""; cur = f.commits[cur].parent {
			if cur == tip {
				merged[name] = true
				break
			}
		}
	}
	return merged, nil
}

// ChangesContainedIn models the shell's tree-content check over the token
// sets: contained when every content token on the branch's commits between
// merge-base and tip also appears in some commit reachable from upstream. The
// default token is the commit's own id, so containment is only true when a
// test explicitly lands the tokens on upstream — squashInto (a host
// squash-merge) or an ancestry merge that makes the commits reachable.
func (f *fakeGit) ChangesContainedIn(upstream, branch string) (bool, error) {
	if err := f.fail("ChangesContainedIn"); err != nil {
		return false, err
	}
	base, err := f.MergeBase(upstream, branch)
	if err != nil {
		return false, err
	}
	up, br := f.resolve(upstream), f.resolve(branch)
	if up == "" || br == "" {
		return false, fmt.Errorf("unknown revision in containment check %q..%q", upstream, branch)
	}
	upstreamTokens := map[string]bool{}
	for cur := up; cur != ""; cur = f.commits[cur].parent {
		for tok := range f.commits[cur].content {
			upstreamTokens[tok] = true
		}
	}
	for cur := br; cur != "" && cur != base; cur = f.commits[cur].parent {
		for tok := range f.commits[cur].content {
			if !upstreamTokens[tok] {
				return false, nil
			}
		}
	}
	return true, nil
}

// squashTokens returns the union of the content tokens on every commit in
// merge-base(upstream, branch)..branch — what a host-side squash-merge of
// branch would land as one commit.
func (f *fakeGit) squashTokens(t *testing.T, upstream, branch string) map[string]bool {
	t.Helper()
	base, err := f.MergeBase(upstream, branch)
	if err != nil {
		t.Fatalf("squashTokens: merge-base %q %q: %v", upstream, branch, err)
	}
	toks := map[string]bool{}
	for cur := f.resolve(branch); cur != "" && cur != base; cur = f.commits[cur].parent {
		for tok := range f.commits[cur].content {
			toks[tok] = true
		}
	}
	return toks
}

// squashInto lands branch's entire content on trunk as ONE new commit — the
// host-side squash-merge: branch's tip is no ancestor of trunk, yet every
// content token its commits carried is now reachable from trunk, so
// MergedInto misses it while ChangesContainedIn catches it.
func (f *fakeGit) squashInto(t *testing.T, trunk, branch string) {
	t.Helper()
	id := f.newID()
	f.commits[id] = &fakeCommit{id: id, parent: f.branches[trunk], subject: "squash " + branch, content: f.squashTokens(t, trunk, branch)}
	f.branches[trunk] = id
}

// DiffCachedHunks returns the canned staged hunks a test set on stagedHunks.
func (f *fakeGit) DiffCachedHunks() ([]git.Hunk, []git.UnsupportedRecord, error) {
	if err := f.fail("DiffCachedHunks"); err != nil {
		return nil, nil, err
	}
	return f.stagedHunks, f.stagedUnsupported, nil
}

// BlamePorcelain returns the canned per-file blame a test set on blame. The
// rev is ignored: engine tests only ever blame HEAD.
func (f *fakeGit) BlamePorcelain(file, _ string) (map[int]git.BlameLine, error) {
	if err := f.fail("BlamePorcelain"); err != nil {
		return nil, err
	}
	return f.blame[file], nil
}

// DiffCachedPatchesFor ignores the hunk selection (patch content is not
// modeled; real reassembly is proven by the git-level and e2e tests). Each
// target gets the same stagedPatch bytes.
func (f *fakeGit) DiffCachedPatchesFor(want map[string][]git.Hunk) (map[string][]byte, error) {
	if err := f.fail("DiffCachedPatchesFor"); err != nil {
		return nil, err
	}
	patches := make(map[string][]byte, len(want))
	for target := range want {
		patches[target] = f.stagedPatch
	}
	return patches, nil
}

// AmendTipWithPatch models the temp-index amend: the branch's tip is replaced
// by a new commit with the same parent and subject (patch content is not
// modeled — real application is proven by the git-level and e2e tests).
func (f *fakeGit) AmendTipWithPatch(branch string, _ []byte) (string, error) {
	if f.applyErr != nil {
		return "", f.applyErr
	}
	tip, ok := f.branches[branch]
	if !ok {
		return "", fmt.Errorf("no such branch %q", branch)
	}
	old := f.commits[tip]
	id := f.newID()
	f.commits[id] = &fakeCommit{id: id, parent: old.parent, subject: old.subject, content: old.content}
	f.branches[branch] = id
	return id, nil
}

// ResetHardIn records the call; for the current worktree ("") it clears the
// staged state, mirroring `git reset --hard` dropping the staged copy.
func (f *fakeGit) ResetHardIn(dir, _ string) error {
	if err := f.fail("ResetHardIn"); err != nil {
		return err
	}
	f.resetHardDirs = append(f.resetHardDirs, dir)
	if dir == "" {
		f.staged = false
		f.clean = true
		f.stagedHunks = nil
		f.stagedPatch = nil
	}
	return nil
}

// addWorktree registers a fake linked worktree for branch at path, a test seam
// for exercising multi-worktree code paths without spawning git.
func (f *fakeGit) addWorktree(path, branch string) { f.linkedWorktrees[branch] = path }

// Worktrees synthesizes the main worktree (the current f.head, when on a
// branch) plus any registered linked worktrees. It is read-only and tolerates a
// detached HEAD (it simply omits the main worktree's branch entry).
func (f *fakeGit) Worktrees() ([]git.Worktree, error) {
	if err := f.fail("Worktrees"); err != nil {
		return nil, err
	}
	var list []git.Worktree
	main := git.Worktree{Path: "."}
	if f.head != "" {
		main.Branch = f.head
		main.Head = f.branches[f.head]
	} else {
		main.Detached = true
		main.Head = f.detachedAt
	}
	list = append(list, main)
	for branch, path := range f.linkedWorktrees {
		list = append(list, git.Worktree{Path: path, Branch: branch, Head: f.branches[branch]})
	}
	return list, nil
}

// dirtyWorktrees marks linked worktrees (by branch) as dirty for IsCleanIn.
func (f *fakeGit) markWorktreeDirty(branch string) {
	if f.dirtyWT == nil {
		f.dirtyWT = map[string]bool{}
	}
	f.dirtyWT[branch] = true
}

// RebaseOntoIn models an owner-driven rebase: it replays the branch's commits
// like RebaseOnto but, crucially, does NOT move f.head — the rebase happens in
// another worktree, leaving the main worktree's HEAD untouched. A branch armed
// via conflictOn stalls just like RebaseOnto.
func (f *fakeGit) RebaseOntoIn(dir string, newBase, oldBase, branch string) error {
	if err := f.rebaseErr[branch]; err != nil {
		return err
	}
	if f.conflictNext[branch] || f.conflictEvery[branch] {
		f.rebaseActive = true
		f.rebaseBranch = branch
		f.rebaseNewBase = f.resolve(newBase)
		f.rebaseOldBase = f.resolve(oldBase)
		f.rebaseInWT[dir] = true // the paused rebase lives in dir
		f.rebaseWT = dir
		return fmt.Errorf("conflict rebasing %q", branch)
	}
	savedHead := f.head
	err := f.replay(newBase, oldBase, branch)
	f.head = savedHead // the owner worktree rebases; main HEAD does not move
	return err
}

// RebaseAbortIn aborts the rebase paused in dir specifically — a dir with no
// paused rebase reports "no rebase in progress" even if the main worktree has
// one (rebase state is per-worktree). It also clears foreign rebases a test
// armed directly in rebaseInWT.
func (f *fakeGit) RebaseAbortIn(dir string) error {
	if err := f.fail("RebaseAbortIn"); err != nil {
		return err
	}
	if f.rebaseAbortErr != nil {
		return f.rebaseAbortErr
	}
	if !f.rebaseInWT[dir] {
		return fmt.Errorf("no rebase in progress")
	}
	delete(f.rebaseInWT, dir)
	if f.rebaseWT == dir {
		f.rebaseActive, f.rebaseBranch, f.rebaseNewBase, f.rebaseOldBase, f.rebaseWT = false, "", "", "", ""
	}
	return nil
}

func (f *fakeGit) RebaseInProgressIn(dir string) (bool, error) {
	if err := f.fail("RebaseInProgressIn"); err != nil {
		return false, err
	}
	return f.rebaseInWT[dir], nil
}

func (f *fakeGit) IsCleanIn(dir string) (bool, error) {
	if err := f.fail("IsCleanIn"); err != nil {
		return false, err
	}
	// Map the worktree dir back to its branch to honor markWorktreeDirty.
	for branch, path := range f.linkedWorktrees {
		if path == dir {
			return !f.dirtyWT[branch], nil
		}
	}
	return true, nil
}

func (f *fakeGit) RepoRoot() (string, error) {
	if err := f.fail("RepoRoot"); err != nil {
		return "", err
	}
	return f.repoRoot, nil
}

// WorktreeRemove tears down the linked worktree at dir, mirroring git's refusal
// to remove a dirty worktree without --force. It deregisters the branch so a
// follow-up DeleteBranch no longer hits "checked out in another worktree".
func (f *fakeGit) WorktreeRemove(dir string, force bool) error {
	if err := f.fail("WorktreeRemove"); err != nil {
		return err
	}
	for branch, path := range f.linkedWorktrees {
		if path != dir {
			continue
		}
		if !force && f.dirtyWT[branch] {
			return fmt.Errorf("worktree %q is dirty; use --force", dir)
		}
		delete(f.linkedWorktrees, branch)
		delete(f.dirtyWT, branch)
		return nil
	}
	return fmt.Errorf("no worktree at %q", dir)
}

func (f *fakeGit) Checkout(name string) error {
	if _, ok := f.branches[name]; !ok {
		return fmt.Errorf("no such branch %q", name)
	}
	if err := f.checkoutErr[name]; err != nil {
		return err
	}
	f.head = name
	f.detachedAt = ""
	return nil
}

func (f *fakeGit) CheckoutDetach(ref string) error {
	if err := f.fail("CheckoutDetach"); err != nil {
		return err
	}
	id := f.resolve(ref)
	if id == "" {
		return fmt.Errorf("unknown revision %q", ref)
	}
	f.head = ""
	f.detachedAt = id
	return nil
}

// headBranch returns the current branch, panicking with a located message if
// HEAD is detached. The fake otherwise silently reads f.branches[""] and
// fabricates a branch at the empty SHA, masking engine bugs real git would
// surface; failing loudly keeps the fake trustworthy.
func (f *fakeGit) headBranch(op string) string {
	if f.head == "" {
		panic("fakeGit." + op + ": HEAD is detached")
	}
	return f.head
}

func (f *fakeGit) CreateBranch(name string) error {
	if err := f.fail("CreateBranch"); err != nil {
		return err
	}
	if f.head == "" {
		return fmt.Errorf("cannot create branch %q with a detached HEAD", name)
	}
	if _, ok := f.branches[name]; ok {
		return fmt.Errorf("branch %q exists", name)
	}
	f.branches[name] = f.branches[f.head]
	f.head = name
	return nil
}

func (f *fakeGit) CreateBranchAt(name, ref string) error {
	if err := f.fail("CreateBranchAt"); err != nil {
		return err
	}
	if _, ok := f.branches[name]; ok {
		return fmt.Errorf("branch %q exists", name)
	}
	id := f.resolve(ref)
	if id == "" {
		return fmt.Errorf("unknown revision %q", ref)
	}
	f.branches[name] = id
	return nil
}

func (f *fakeGit) DeleteBranch(name string, force bool) error {
	if name == f.head {
		return fmt.Errorf("cannot delete the current branch %q", name)
	}
	if _, ok := f.linkedWorktrees[name]; ok {
		// git refuses to delete a branch checked out in another worktree; the
		// engine must tear that worktree down (WorktreeRemove) first.
		return fmt.Errorf("cannot delete branch %q checked out at another worktree", name)
	}
	if _, ok := f.branches[name]; !ok {
		return fmt.Errorf("no such branch %q", name)
	}
	if err := f.deleteErr[name]; err != nil {
		return err
	}
	if !force {
		merged, err := f.IsAncestor(name, f.head)
		if err != nil {
			return err
		}
		if !merged {
			return fmt.Errorf("branch %q is not fully merged", name)
		}
	}
	delete(f.branches, name)
	return nil
}

// DeleteBranches mirrors real `git branch -D a b c`: every deletable name is
// deleted even when another fails, and the first failure is returned — a
// non-nil error may accompany a partial delete, which is what applyPrune's
// per-survivor retry path exists to sort out.
func (f *fakeGit) DeleteBranches(names []string, force bool) error {
	f.calls["DeleteBranches"]++
	var firstErr error
	for _, name := range names {
		if err := f.DeleteBranch(name, force); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

func (f *fakeGit) ForceBranch(name, ref string) error {
	if err := f.fail("ForceBranch"); err != nil {
		return err
	}
	if name == f.head {
		return fmt.Errorf("cannot force the current branch %q", name)
	}
	id := f.resolve(ref)
	if id == "" {
		return fmt.Errorf("unknown revision %q", ref)
	}
	f.branches[name] = id
	return nil
}

func (f *fakeGit) UpdateRef(ref, sha string) error {
	if err := f.fail("UpdateRef"); err != nil {
		return err
	}
	name := strings.TrimPrefix(ref, "refs/heads/")
	id := f.resolve(sha)
	if id == "" {
		return fmt.Errorf("unknown revision %q", sha)
	}
	f.branches[name] = id
	return nil
}

// UpdateRefs mirrors the shell's transactional contract: every SHA must
// resolve before any ref moves.
func (f *fakeGit) UpdateRefs(updates map[string]string) error {
	if err := f.fail("UpdateRefs"); err != nil {
		return err
	}
	resolved := map[string]string{}
	for ref, sha := range updates {
		id := f.resolve(sha)
		if id == "" {
			return fmt.Errorf("unknown revision %q", sha)
		}
		resolved[strings.TrimPrefix(ref, "refs/heads/")] = id
	}
	for name, id := range resolved {
		f.branches[name] = id
	}
	return nil
}

// UpdateRefsCas mirrors the shell's compare-and-swap batch: every New must
// resolve AND every Old expectation must hold — "" unverified, the zero oid
// requires the branch absent, an oid requires the tip to equal it — before
// any ref moves. One mismatch fails the whole batch naming the ref, like git
// reporting a CAS failure on `update <ref> <new> <old>`.
func (f *fakeGit) UpdateRefsCas(updates map[string]git.RefUpdate) error {
	if err := f.fail("UpdateRefsCas"); err != nil {
		return err
	}
	type casUpdate struct {
		name string
		id   string
	}
	resolved := map[string]casUpdate{}
	for ref, u := range updates {
		name := strings.TrimPrefix(ref, "refs/heads/")
		id := f.resolve(u.New)
		if id == "" {
			return fmt.Errorf("unknown revision %q", u.New)
		}
		tip, exists := f.branches[name]
		switch u.Old {
		case "":
		case zeroSHA:
			if exists {
				return fmt.Errorf("cannot lock ref %q: ref already exists but expected it not to", ref)
			}
		default:
			if !exists || tip != u.Old {
				return fmt.Errorf("cannot lock ref %q: is at %q but expected %q", ref, tip, u.Old)
			}
		}
		resolved[name] = casUpdate{name: name, id: id}
	}
	for _, u := range resolved {
		f.branches[u.name] = u.id
	}
	return nil
}

func (f *fakeGit) RenameBranch(oldName, newName string) error {
	if err := f.fail("RenameBranch"); err != nil {
		return err
	}
	tip, ok := f.branches[oldName]
	if !ok {
		return fmt.Errorf("no such branch %q", oldName)
	}
	delete(f.branches, oldName)
	f.branches[newName] = tip
	if f.head == oldName {
		f.head = newName
	}
	return nil
}

func (f *fakeGit) commit(subject string) {
	head := f.headBranch("commit")
	id := f.newID()
	f.commits[id] = &fakeCommit{id: id, parent: f.branches[head], subject: subject, content: map[string]bool{id: true}}
	f.branches[head] = id
}

func (f *fakeGit) Commit(message string, _ bool) error {
	if !f.staged {
		return fmt.Errorf("no staged changes")
	}
	if f.commitErr != nil {
		return f.commitErr
	}
	f.commit(message)
	f.staged = false
	f.clean = true
	return nil
}

func (f *fakeGit) amend(subject string) {
	head := f.headBranch("amend")
	old := f.commits[f.branches[head]]
	id := f.newID()
	// Amending rewrites the commit, not its content: keep the old token set so
	// a branch amended after its squash-merge is still content-contained.
	f.commits[id] = &fakeCommit{id: id, parent: old.parent, subject: subject, content: old.content}
	f.branches[head] = id
}

func (f *fakeGit) AmendNoEdit(_ bool) error {
	if err := f.fail("AmendNoEdit"); err != nil {
		return err
	}
	head := f.headBranch("AmendNoEdit")
	f.amend(f.commits[f.branches[head]].subject)
	f.staged = false
	f.clean = true
	return nil
}

func (f *fakeGit) AmendMessage(message string, _ bool) error {
	if err := f.fail("AmendMessage"); err != nil {
		return err
	}
	f.amend(message)
	f.staged = false
	f.clean = true
	return nil
}

func (f *fakeGit) ResetSoft(ref string) error {
	if err := f.fail("ResetSoft"); err != nil {
		return err
	}
	if f.head == "" {
		return fmt.Errorf("cannot reset with a detached HEAD")
	}
	id := f.resolve(ref)
	if id == "" {
		return fmt.Errorf("unknown revision %q", ref)
	}
	f.branches[f.head] = id
	f.staged = true
	f.clean = false
	return nil
}

// TestFakeGitRejectsDetachedHead asserts the HEAD-mutating fake methods fail
// loudly when HEAD is detached instead of fabricating an empty-SHA branch.
func TestFakeGitRejectsDetachedHead(t *testing.T) {
	// A fresh detached fakeGit per assertion, so each guard is exercised in
	// isolation: a future regression in one fails only its own case instead of
	// cascading through the rest (which would mask the real culprit).
	detached := func() *fakeGit { f := newFakeGit(); f.head = ""; return f }

	if err := detached().CreateBranch("x"); err == nil {
		t.Error("CreateBranch on a detached HEAD should error")
	}
	if err := detached().ResetSoft("main"); err == nil {
		t.Error("ResetSoft on a detached HEAD should error")
	}
	assertDetachedPanic(t, "commit", func() { detached().commit("x") })
	assertDetachedPanic(t, "amend", func() { detached().amend("x") })
	assertDetachedPanic(t, "AmendNoEdit", func() { _ = detached().AmendNoEdit(false) })
	// AmendMessage delegates to f.amend, which calls headBranch("amend"), so the
	// located panic message says "amend", not "AmendMessage".
	assertDetachedPanic(t, "amend", func() { _ = detached().AmendMessage("x", false) })
}

func assertDetachedPanic(t *testing.T, op string, fn func()) {
	t.Helper()
	want := "fakeGit." + op + ": HEAD is detached"
	defer func() {
		r := recover()
		if r == nil {
			t.Errorf("%s on a detached HEAD did not panic", op)
			return
		}
		if msg, ok := r.(string); !ok || !strings.Contains(msg, want) {
			t.Errorf("panic = %v, want it to contain %q", r, want)
		}
	}()
	fn()
}

// RebaseOnto replays the branch's commits after oldBase onto newBase, mirroring
// "git rebase --onto". If the branch is marked to conflict, it stops mid-rebase
// (leaving a rebase in progress) until RebaseContinue is called.
func (f *fakeGit) RebaseOnto(newBase, oldBase, branch string) error {
	f.rebaseLog = append(f.rebaseLog, rebaseCall{newBase, oldBase, branch})
	if err := f.rebaseErr[branch]; err != nil {
		f.head = branch
		return err
	}
	if f.conflictNext[branch] || f.conflictEvery[branch] {
		f.rebaseActive = true
		f.rebaseBranch = branch
		f.rebaseNewBase = f.resolve(newBase)
		f.rebaseOldBase = f.resolve(oldBase)
		f.rebaseInWT["."] = true // the main worktree owns this rebase
		f.rebaseWT = "."
		return fmt.Errorf("conflict rebasing %q", branch)
	}
	return f.replay(newBase, oldBase, branch)
}

// replay applies the branch's commits after oldBase onto newBase.
func (f *fakeGit) replay(newBase, oldBase, branch string) error {
	newBaseID := f.resolve(newBase)
	oldBaseID := f.resolve(oldBase)
	if newBaseID == "" || oldBaseID == "" {
		return fmt.Errorf("unknown base")
	}
	var chain []*fakeCommit
	for cur := f.branches[branch]; cur != "" && cur != oldBaseID; cur = f.commits[cur].parent {
		chain = append(chain, f.commits[cur])
	}
	parent := newBaseID
	for i := len(chain) - 1; i >= 0; i-- {
		id := f.newID()
		// Rebasing preserves each commit's diff: copy the content tokens so a
		// branch rebased after its squash-merge is still content-contained.
		f.commits[id] = &fakeCommit{id: id, parent: parent, subject: chain[i].subject, content: chain[i].content}
		parent = id
	}
	f.branches[branch] = parent
	f.head = branch
	return nil
}

func (f *fakeGit) RebaseInProgress() (bool, error) {
	if err := f.fail("RebaseInProgress"); err != nil {
		return false, err
	}
	return f.rebaseActive, nil
}

func (f *fakeGit) RebaseHeadName() (string, error) {
	if err := f.fail("RebaseHeadName"); err != nil {
		return "", err
	}
	return f.rebaseBranch, nil
}

// RebaseOntoSHA reports the target recorded when the rebase paused — the fake
// equivalent of rebase-merge/onto. rebaseOntoErr models unreadable/corrupt
// metadata: the accessor fails but the paused rebase is left intact.
func (f *fakeGit) RebaseOntoSHA() (string, error) {
	if !f.rebaseActive {
		return "", fmt.Errorf("no rebase in progress")
	}
	if f.rebaseOntoErr != nil {
		return "", f.rebaseOntoErr
	}
	return f.rebaseNewBase, nil
}

// RebaseAbort ends an in-progress rebase. The conflicting RebaseOnto never moved
// the branch (it only paused), so clearing the rebase state restores the
// pre-rebase tip, mirroring "git rebase --abort".
func (f *fakeGit) RebaseAbort() error {
	if f.rebaseAbortErr != nil {
		return f.rebaseAbortErr
	}
	if !f.rebaseActive {
		return fmt.Errorf("no rebase in progress")
	}
	delete(f.rebaseInWT, f.rebaseWT)
	f.rebaseActive, f.rebaseBranch, f.rebaseNewBase, f.rebaseOldBase, f.rebaseWT = false, "", "", "", ""
	return nil
}

// CommitRange mirrors `rev-list include ^exclude` over the fake's linear
// parent chains: walk from include, stopping at anything reachable from
// exclude.
func (f *fakeGit) CommitRange(exclude, include string) (map[string]bool, error) {
	if err := f.fail("CommitRange"); err != nil {
		return nil, err
	}
	to := f.resolve(include)
	ex := f.resolve(exclude)
	if to == "" || ex == "" {
		return nil, fmt.Errorf("unknown revision in range %q..%q", exclude, include)
	}
	excluded := map[string]bool{}
	for cur := ex; cur != ""; cur = f.commits[cur].parent {
		excluded[cur] = true
	}
	set := map[string]bool{}
	for cur := to; cur != "" && !excluded[cur]; cur = f.commits[cur].parent {
		set[cur] = true
	}
	return set, nil
}

// RebaseContinue resolves the modeled conflict and finishes the rebase.
func (f *fakeGit) RebaseContinue() error {
	if err := f.fail("RebaseContinue"); err != nil {
		return err
	}
	if !f.rebaseActive {
		return fmt.Errorf("no rebase in progress")
	}
	if f.rebaseRestall {
		return fmt.Errorf("rebase still has conflicts") // re-stall: leaves rebaseActive set
	}
	branch, newBase, oldBase := f.rebaseBranch, f.rebaseNewBase, f.rebaseOldBase
	delete(f.conflictNext, branch)
	delete(f.rebaseInWT, f.rebaseWT)
	f.rebaseActive, f.rebaseBranch, f.rebaseNewBase, f.rebaseOldBase, f.rebaseWT = false, "", "", "", ""
	return f.replay(newBase, oldBase, branch)
}

func (f *fakeGit) IsAncestor(ancestor, descendant string) (bool, error) {
	f.isAncestorCalls++
	if err := f.fail("IsAncestor"); err != nil {
		return false, err
	}
	a := f.resolve(ancestor)
	d := f.resolve(descendant)
	if a == "" || d == "" {
		return false, fmt.Errorf("unknown revision in ancestry check %q..%q", ancestor, descendant)
	}
	for cur := d; cur != ""; cur = f.commits[cur].parent {
		if cur == a {
			return true, nil
		}
	}
	return false, nil
}

func mustFakeIsAncestor(t *testing.T, f *fakeGit, ancestor, descendant string) bool {
	t.Helper()
	ok, err := f.IsAncestor(ancestor, descendant)
	if err != nil {
		t.Fatalf("IsAncestor(%q, %q): %v", ancestor, descendant, err)
	}
	return ok
}

func (f *fakeGit) MergeBase(a, b string) (string, error) {
	if err := f.fail("MergeBase"); err != nil {
		return "", err
	}
	seen := map[string]bool{}
	for cur := f.resolve(a); cur != ""; cur = f.commits[cur].parent {
		seen[cur] = true
	}
	for cur := f.resolve(b); cur != ""; cur = f.commits[cur].parent {
		if seen[cur] {
			return cur, nil
		}
	}
	return "", fmt.Errorf("no merge base for %q and %q", a, b)
}

func (f *fakeGit) CommitSubjects(base, branch string) ([]string, error) {
	if err := f.fail("CommitSubjects"); err != nil {
		return nil, err
	}
	baseID := f.resolve(base)
	var subs []string
	for cur := f.branches[branch]; cur != "" && cur != baseID; cur = f.commits[cur].parent {
		subs = append(subs, f.commits[cur].subject)
	}
	return subs, nil
}

func (f *fakeGit) Add(_ ...string) error {
	if err := f.fail("Add"); err != nil {
		return err
	}
	f.staged = true
	f.clean = false
	return nil
}

func (f *fakeGit) HasStagedChanges() (bool, error) {
	if err := f.fail("HasStagedChanges"); err != nil {
		return false, err
	}
	return f.staged, nil
}

func (f *fakeGit) HasUnstagedChanges() (bool, error) {
	if err := f.fail("HasUnstagedChanges"); err != nil {
		return false, err
	}
	return !f.clean && !f.staged, nil
}

func (f *fakeGit) IsClean() (bool, error) {
	if err := f.fail("IsClean"); err != nil {
		return false, err
	}
	return f.clean && !f.staged, nil
}
