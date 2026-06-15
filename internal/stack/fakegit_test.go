package stack

import (
	"fmt"
	"strconv"
	"strings"
	"testing"
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
	conflictNext  map[string]bool
	rebaseActive  bool
	rebaseRestall bool // when set, RebaseContinue fails and leaves the rebase paused
	rebaseBranch  string
	rebaseNewBase string
	rebaseOldBase string
	staged        bool
	clean         bool
	checkoutErr   map[string]error
	deleteErr     map[string]error
	rebaseErr     map[string]error
	commitErr     error
	// detachedAt is the commit a CheckoutDetach left HEAD on ("" when HEAD is
	// on a branch).
	detachedAt string
}

func newFakeGit() *fakeGit {
	f := &fakeGit{
		commits:      map[string]*fakeCommit{},
		branches:     map[string]string{},
		conflictNext: map[string]bool{},
		checkoutErr:  map[string]error{},
		deleteErr:    map[string]error{},
		rebaseErr:    map[string]error{},
		clean:        true,
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

func (f *fakeGit) CreateBranch(name string) error {
	if _, ok := f.branches[name]; ok {
		return fmt.Errorf("branch %q exists", name)
	}
	f.branches[name] = f.branches[f.head]
	f.head = name
	return nil
}

func (f *fakeGit) DeleteBranch(name string, force bool) error {
	if name == f.head {
		return fmt.Errorf("cannot delete the current branch %q", name)
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
	id := f.newID()
	f.commits[id] = &fakeCommit{id: id, parent: f.branches[f.head], subject: subject}
	f.branches[f.head] = id
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
	old := f.commits[f.branches[f.head]]
	id := f.newID()
	f.commits[id] = &fakeCommit{id: id, parent: old.parent, subject: subject}
	f.branches[f.head] = id
}

func (f *fakeGit) AmendNoEdit(_ bool) error {
	f.amend(f.commits[f.branches[f.head]].subject)
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
	id := f.resolve(ref)
	if id == "" {
		return fmt.Errorf("unknown revision %q", ref)
	}
	f.branches[f.head] = id
	f.staged = true
	f.clean = false
	return nil
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
