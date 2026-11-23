package stack

import "github.com/andyrewlee/stacked/internal/git"

// Git is the port the stack engine uses to manipulate the underlying git
// repository. internal/git.Shell is the production implementation; tests use an
// in-memory fake (see fakegit_test.go) so the engine can be exercised without
// spawning git.
type Git interface {
	RevParse(ref string) (string, error)
	RebaseOnto(newBase, oldBase, branch string) error
	BranchExists(name string) bool
	Tips() (map[string]string, error)
	TipsFor(names []string) (map[string]string, error)
	MergedInto(ref string) (map[string]bool, error)
	ChangesContainedIn(upstream, branch string) (bool, error)
	Checkout(name string) error
	CheckoutDetach(ref string) error
	CreateBranch(name string) error
	DeleteBranch(name string, force bool) error
	DeleteBranches(names []string, force bool) error
	UpdateRef(ref, sha string) error
	UpdateRefsCas(updates map[string]git.RefUpdate) error
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
	CommitList(base, branch string) ([]git.CommitInfo, error)
	CommitBody(rev string) (string, error)
	HasStagedChanges() (bool, error)
	HasUnstagedChanges() (bool, error)
	IsClean() (bool, error)
	RebaseInProgress() (bool, error)
	RebaseHeadName() (string, error)
	RebaseOntoSHA() (string, error)
	RebaseContinue() error
	RebaseAbort() error
	Worktrees() ([]git.Worktree, error)
	RebaseOntoIn(dir, newBase, oldBase, branch string) error
	RebaseAbortIn(dir string) error
	RebaseInProgressIn(dir string) (bool, error)
	RebaseHeadNameIn(dir string) (string, error)
	IsCleanIn(dir string) (bool, error)
	DirExists(dir string) bool
	RepoRoot() (string, error)
	LooseBranchTip(name string) (sha string, ok bool)
}

// Remote is the port the engine uses to interact with a git remote during sync.
// internal/git.RemoteShell is the production implementation; tests use a fake.
type Remote interface {
	Exists(name string) bool
	Fetch(name string) error
	FastForward(trunk, remote, ownerDir string, checkedOutHere bool) (string, error)
}

// Env bundles the git port with a persistence hook so engine operations can
// checkpoint the state at safe points without knowing how or where it is
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
