package stack

import "stacked/internal/git"

// Git is the port the stack engine uses to manipulate the underlying git
// repository. internal/git.Shell is the production implementation; tests use an
// in-memory fake (see fakegit_test.go) so the engine can be exercised without
// spawning git. The method set is intentionally the subset the engine needs.
type Git interface {
	RevParse(ref string) (string, error)
	RebaseOnto(newBase, oldBase, branch string) error
	BranchExists(name string) bool
	Tips() (map[string]string, error)
	// TipsFor returns tips for the named local branches. Missing branches are
	// omitted.
	TipsFor(names []string) (map[string]string, error)
	// MergedInto returns the local branches whose tips are ancestors of ref,
	// equivalent to checking IsAncestor(branch, ref) for every local branch.
	MergedInto(ref string) (map[string]bool, error)
	Checkout(name string) error
	CheckoutDetach(ref string) error
	CreateBranch(name string) error
	CreateBranchAt(name, ref string) error
	DeleteBranch(name string, force bool) error
	ForceBranch(name, ref string) error
	UpdateRef(ref, sha string) error
	ResetSoft(ref string) error
	Commit(message string, all bool) error
	AmendNoEdit(all bool) error
	AmendMessage(message string, all bool) error
	Add(paths ...string) error
	RenameBranch(oldName, newName string) error
	MergeBase(a, b string) (string, error)
	IsAncestor(ancestor, descendant string) (bool, error)
	CurrentBranch() (string, error)
	CommitSubjects(base, branch string) ([]string, error)
	HasStagedChanges() (bool, error)
	HasUnstagedChanges() (bool, error)
	IsClean() (bool, error)
	RebaseInProgress() (bool, error)
	RebaseHeadName() (string, error)
	RebaseContinue() error
	RebaseAbort() error
	// AncestorSet returns the set of commit SHAs reachable from ref, so callers
	// can answer many ancestry questions about ref with map lookups.
	AncestorSet(ref string) (map[string]bool, error)
	// Worktrees returns every worktree linked to the repository (including the
	// main worktree), parsed from `git worktree list --porcelain`.
	Worktrees() ([]git.Worktree, error)
	// RebaseOntoIn runs RebaseOnto inside the worktree at dir (git -C <dir>), so
	// a branch checked out in another worktree is rebased by its OWNER — git
	// forbids rebasing a branch checked out elsewhere. Used only by the
	// command-triggered cross-worktree restack cascade.
	RebaseOntoIn(dir, newBase, oldBase, branch string) error
	// RebaseAbortIn aborts an in-progress rebase inside the worktree at dir, so a
	// cross-worktree rebase that hit a conflict can be rolled back rather than
	// left paused in a worktree the main process cannot drive.
	RebaseAbortIn(dir string) error
	// IsCleanIn reports whether the worktree at dir has no staged or unstaged
	// changes, so the cascade can skip a dirty dependent worktree.
	IsCleanIn(dir string) (bool, error)
	// WorktreeRemove removes the linked worktree at dir. git refuses to delete a
	// branch checked out in another worktree, so a lifecycle op (delete/fold/
	// prune) that removes such a branch must first tear down its (clean) worktree
	// through this port. force is left false by the engine: a dirty worktree is
	// never auto-removed (the engine checks IsCleanIn first), so in-progress work
	// is never silently discarded.
	WorktreeRemove(dir string, force bool) error
}

// Remote is the port the engine uses to interact with a git remote during sync.
// internal/git.RemoteShell is the production implementation; tests use a fake.
type Remote interface {
	Exists(name string) bool
	Fetch(name string) error
	// FastForward fast-forwards the local trunk to <remote>/<trunk> and returns a
	// short human-readable description of the result. The engine resolves where
	// the trunk is checked out: checkedOutHere means this process's worktree;
	// ownerDir names another worktree owning the trunk (the implementation must
	// advance it there); both zero means the trunk is checked out nowhere and
	// only the ref itself may move, fast-forward only.
	FastForward(trunk, remote, ownerDir string, checkedOutHere bool) (string, error)
}

// Env bundles the git port with a persistence hook so engine operations can
// checkpoint the state at safe points (e.g. after each branch restacks, so a
// later conflict cannot lose progress) without knowing how or where it is
// stored. In tests Save is nil (a no-op); in the CLI it is State.Save.
type Env struct {
	Git  Git
	Save func() error
}

// save persists the state if a Save hook is configured.
func (e Env) save() error {
	if e.Save == nil {
		return nil
	}
	return e.Save()
}
