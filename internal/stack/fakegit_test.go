package stack

import (
	"fmt"
	"strconv"
	"strings"
	"testing"

	"stacked/internal/git"
)

// fakeCommit is a node in the in-memory commit DAG.
type fakeCommit struct {
	id      string
	parent  string // parent commit id ("" for the root)
	subject string
}

// fakeGit is an in-memory implementation of the Git port. It models a commit DAG
// and branch refs faithfully enough to exercise the restack engine without
// spawning git. Rebases never conflict (conflict handling is covered by the
// real-git integration and e2e suites); this fake exists to prove the engine's
// topology/parentSHA bookkeeping under thousands of random operations.
type fakeGit struct {
	commits  map[string]*fakeCommit
	branches map[string]string // branch -> tip commit id
	head     string            // current branch
	seq      int

	// conflict modeling: a branch in conflictNext stops mid-rebase the next time
	// it is rebased, mirroring a real merge conflict that the caller resolves with
	// RebaseContinue.
	conflictNext    map[string]bool
	rebaseActive    bool
	rebaseRestall   bool // when set, RebaseContinue fails and leaves the rebase paused
	rebaseBranch    string
	rebaseNewBase   string
	rebaseOldBase   string
	rebaseAbortErr  error
	staged          bool
	clean           bool
	checkoutErr     map[string]error
	deleteErr       map[string]error
	rebaseErr       map[string]error
	commitErr       error
	isAncestorCalls int
	mergedIntoCalls int
	// detachedAt is the commit a CheckoutDetach left HEAD on ("" when HEAD is
	// on a branch).
	detachedAt string

	// linkedWorktrees models extra (non-main) worktrees by branch -> path. The
	// main worktree (f.head) is synthesized in Worktrees().
	linkedWorktrees map[string]string
	// dirtyWT marks linked worktrees (by branch) as having a dirty tree, so
	// IsCleanIn can model a skipped dependent in the cascade tests.
	dirtyWT map[string]bool
}

func newFakeGit() *fakeGit {
	f := &fakeGit{
		commits:         map[string]*fakeCommit{},
		branches:        map[string]string{},
		conflictNext:    map[string]bool{},
		checkoutErr:     map[string]error{},
		deleteErr:       map[string]error{},
		rebaseErr:       map[string]error{},
		clean:           true,
		linkedWorktrees: map[string]string{},
	}
	id := f.newID()
	f.commits[id] = &fakeCommit{id: id, subject: "init"}
	f.branches["main"] = id
	f.head = "main"
	return f
}

// conflictOn makes the next rebase of branch stop on a conflict.
func (f *fakeGit) conflictOn(branch string) { f.conflictNext[branch] = true }

func (f *fakeGit) newID() string {
	f.seq++
	return "c" + strconv.Itoa(f.seq)
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
	if _, ok := f.commits[ref]; ok {
		return ref
	}
	return ""
}

func (f *fakeGit) RevParse(ref string) (string, error) {
	if id := f.resolve(ref); id != "" {
		return id, nil
	}
	return "", fmt.Errorf("unknown revision %q", ref)
}

func (f *fakeGit) CurrentBranch() (string, error) {
	if f.head == "" {
		return "", fmt.Errorf("detached HEAD")
	}
	return f.head, nil
}

func (f *fakeGit) BranchExists(name string) bool {
	_, ok := f.branches[name]
	return ok
}

func (f *fakeGit) Tips() (map[string]string, error) {
	tips := make(map[string]string, len(f.branches))
	for name, tip := range f.branches {
		tips[name] = tip
	}
	return tips, nil
}

func (f *fakeGit) TipsFor(names []string) (map[string]string, error) {
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
	revParseCalls int
	tipsCalls     int
	tipsForCalls  int
	tipsForNames  [][]string
}

func (g *tipReadSpyGit) RevParse(ref string) (string, error) {
	g.revParseCalls++
	return g.Git.RevParse(ref)
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

func (f *fakeGit) MergedInto(ref string) (map[string]bool, error) {
	f.mergedIntoCalls++
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

// addWorktree registers a fake linked worktree for branch at path, a test seam
// for exercising multi-worktree code paths without spawning git.
func (f *fakeGit) addWorktree(path, branch string) { f.linkedWorktrees[branch] = path }

// Worktrees synthesizes the main worktree (the current f.head, when on a
// branch) plus any registered linked worktrees. It is read-only and tolerates a
// detached HEAD (it simply omits the main worktree's branch entry).
func (f *fakeGit) Worktrees() ([]git.Worktree, error) {
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
func (f *fakeGit) RebaseOntoIn(_ /*dir*/, newBase, oldBase, branch string) error {
	if err := f.rebaseErr[branch]; err != nil {
		return err
	}
	if f.conflictNext[branch] {
		f.rebaseActive = true
		f.rebaseBranch = branch
		f.rebaseNewBase = f.resolve(newBase)
		f.rebaseOldBase = f.resolve(oldBase)
		return fmt.Errorf("conflict rebasing %q", branch)
	}
	savedHead := f.head
	err := f.replay(newBase, oldBase, branch)
	f.head = savedHead // the owner worktree rebases; main HEAD does not move
	return err
}

func (f *fakeGit) RebaseAbortIn(_ string) error { return f.RebaseAbort() }

func (f *fakeGit) IsCleanIn(dir string) (bool, error) {
	// Map the worktree dir back to its branch to honor markWorktreeDirty.
	for branch, path := range f.linkedWorktrees {
		if path == dir {
			return !f.dirtyWT[branch], nil
		}
	}
	return true, nil
}

// WorktreeRemove tears down the linked worktree at dir, mirroring git's refusal
// to remove a dirty worktree without --force. It deregisters the branch so a
// follow-up DeleteBranch no longer hits "checked out in another worktree".
func (f *fakeGit) WorktreeRemove(dir string, force bool) error {
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

func (f *fakeGit) ForceBranch(name, ref string) error {
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
	name := strings.TrimPrefix(ref, "refs/heads/")
	id := f.resolve(sha)
	if id == "" {
		return fmt.Errorf("unknown revision %q", sha)
	}
	f.branches[name] = id
	return nil
}

func (f *fakeGit) RenameBranch(oldName, newName string) error {
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
	f.commits[id] = &fakeCommit{id: id, parent: f.branches[head], subject: subject}
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
	f.commits[id] = &fakeCommit{id: id, parent: old.parent, subject: subject}
	f.branches[head] = id
}

func (f *fakeGit) AmendNoEdit(_ bool) error {
	head := f.headBranch("AmendNoEdit")
	f.amend(f.commits[f.branches[head]].subject)
	f.staged = false
	f.clean = true
	return nil
}

func (f *fakeGit) AmendMessage(message string, _ bool) error {
	f.amend(message)
	f.staged = false
	f.clean = true
	return nil
}

func (f *fakeGit) ResetSoft(ref string) error {
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
	if err := f.rebaseErr[branch]; err != nil {
		f.head = branch
		return err
	}
	if f.conflictNext[branch] {
		f.rebaseActive = true
		f.rebaseBranch = branch
		f.rebaseNewBase = f.resolve(newBase)
		f.rebaseOldBase = f.resolve(oldBase)
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
		f.commits[id] = &fakeCommit{id: id, parent: parent, subject: chain[i].subject}
		parent = id
	}
	f.branches[branch] = parent
	f.head = branch
	return nil
}

func (f *fakeGit) RebaseInProgress() (bool, error) { return f.rebaseActive, nil }
func (f *fakeGit) RebaseHeadName() (string, error) { return f.rebaseBranch, nil }

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
	f.rebaseActive, f.rebaseBranch, f.rebaseNewBase, f.rebaseOldBase = false, "", "", ""
	return nil
}

// AncestorSet returns the commit ids reachable from ref (ref and its ancestors),
// mirroring "git rev-list ref" over the in-memory DAG.
func (f *fakeGit) AncestorSet(ref string) (map[string]bool, error) {
	start := f.resolve(ref)
	if start == "" {
		return nil, fmt.Errorf("unknown revision %q", ref)
	}
	set := map[string]bool{}
	for cur := start; cur != ""; cur = f.commits[cur].parent {
		set[cur] = true
	}
	return set, nil
}

// RebaseContinue resolves the modeled conflict and finishes the rebase.
func (f *fakeGit) RebaseContinue() error {
	if !f.rebaseActive {
		return fmt.Errorf("no rebase in progress")
	}
	if f.rebaseRestall {
		return fmt.Errorf("rebase still has conflicts") // re-stall: leaves rebaseActive set
	}
	branch, newBase, oldBase := f.rebaseBranch, f.rebaseNewBase, f.rebaseOldBase
	delete(f.conflictNext, branch)
	f.rebaseActive, f.rebaseBranch, f.rebaseNewBase, f.rebaseOldBase = false, "", "", ""
	return f.replay(newBase, oldBase, branch)
}

func (f *fakeGit) IsAncestor(ancestor, descendant string) (bool, error) {
	f.isAncestorCalls++
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
	baseID := f.resolve(base)
	var subs []string
	for cur := f.branches[branch]; cur != "" && cur != baseID; cur = f.commits[cur].parent {
		subs = append(subs, f.commits[cur].subject)
	}
	return subs, nil
}

func (f *fakeGit) Add(_ ...string) error {
	f.staged = true
	f.clean = false
	return nil
}

func (f *fakeGit) HasStagedChanges() (bool, error) { return f.staged, nil }
func (f *fakeGit) HasUnstagedChanges() (bool, error) {
	return !f.clean && !f.staged, nil
}
func (f *fakeGit) IsClean() (bool, error) { return f.clean && !f.staged, nil }
