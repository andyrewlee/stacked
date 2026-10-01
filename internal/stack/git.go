package stack

import "github.com/andyrewlee/stacked/internal/git"

// Git is the port the stack engine uses to manipulate the underlying git
// repository. internal/git.Shell is the production implementation; tests use an
// in-memory fake (see fakegit_test.go) so the engine can be exercised without
// spawning git. The method set is intentionally the subset the engine needs.
// Engine code must never exec git itself — every git interaction inside an
// operation goes through this port so tests can intercept it. (Persistence-layer
// environment probes that run before Env exists, e.g. locating the git dir for
// the state file, are exempt; see stackedDir in store.go.)
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
	// ChangesContainedIn reports whether every content change branch makes
	// relative to its merge base with upstream is already present in
	// upstream's tree — the squash-merge / fully-cherry-picked case that
	// ancestry checks cannot see: branch's tip is no ancestor, yet merging it
	// would add nothing. Exact content equality, never a heuristic: a branch
	// carrying any content upstream lacks is not contained.
	ChangesContainedIn(upstream, branch string) (bool, error)
	Checkout(name string) error
	CheckoutDetach(ref string) error
	CreateBranch(name string) error
	CreateBranchAt(name, ref string) error
	DeleteBranch(name string, force bool) error
	ForceBranch(name, ref string) error
	UpdateRef(ref, sha string) error
	// UpdateRefs applies every ref->SHA update as ONE transaction: on any
	// failure no ref moves (all or nothing — the fake and the shell must both
	// honor this). An update creates a missing ref, which is what resurrects
	// pruned branches on undo.
	UpdateRefs(updates map[string]string) error
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
	// RebaseOntoSHA returns the commit the in-progress rebase is replaying
	// onto (rebase-merge/onto metadata), captured while the rebase is still
	// paused. Continue records it as the rebased branch's ParentSHA — the
	// target actually incorporated — instead of the parent's possibly-moved
	// current tip. An error means the target could not be determined; the
	// caller must surface it rather than guess.
	RebaseOntoSHA() (string, error)
	RebaseContinue() error
	RebaseAbort() error
	// CommitRange returns the SHAs in exclude..include (reachable from
	// include, not from exclude) in one bounded rev-list walk.
	CommitRange(exclude, include string) (map[string]bool, error)
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
	// RebaseInProgressIn reports whether a rebase is already in progress in the
	// worktree at dir — including one st did not start. Rebase metadata is
	// per-worktree (it lives under that worktree's own git dir), so a paused
	// rebase elsewhere is invisible to RebaseInProgress and vice versa.
	RebaseInProgressIn(dir string) (bool, error)
	// IsCleanIn reports whether the worktree at dir has no staged or unstaged
	// changes, so the cascade can skip a dirty dependent worktree.
	IsCleanIn(dir string) (bool, error)
	// RepoRoot returns the top level of the worktree containing the process's
	// current directory (rev-parse --show-toplevel) — the answer to "which
	// worktree am I standing in", used by guards that must never delete the
	// caller's own cwd.
	RepoRoot() (string, error)
	// DiffCachedHunks returns the staged text-change regions (git diff
	// --cached -U0) plus an UnsupportedRecord for every staged section that
	// is not plain text hunks (binary, mode change, rename, quoted path).
	// Contract: every staged change appears in one of the two slices, so a
	// caller gating on "zero refusals" covers the whole staged diff.
	DiffCachedHunks() ([]git.Hunk, []git.UnsupportedRecord, error)
	// BlamePorcelain maps each final line of file at rev to its provenance
	// (git blame --line-porcelain): the commit that last touched the line,
	// the line's number in THAT commit's version, and the path it carried
	// there. Implementations must drop entries whose metadata is incomplete
	// — a missing map key means unattributable, never "fill in defaults".
	BlamePorcelain(file, rev string) (map[int]git.BlameLine, error)
	// DiffCachedPatchesFor returns, per target, a minimal staged patch
	// containing ONLY that target's hunks (keyed by the DiffCachedHunks
	// tuple), with post-image line numbers corrected for omitted same-file
	// hunks — assembled from ONE `diff --cached` capture so absorb's
	// per-target loop does not rerun the full-index diff.
	DiffCachedPatchesFor(want map[string][]git.Hunk) (map[string][]byte, error)
	// AmendTipWithPatch rewrites branch's tip commit to also contain patch via
	// a temporary-index amend (read-tree/apply --cached/write-tree/commit-tree)
	// that touches no worktree and preserves the tip's author, message, and
	// parents. On any failure — including a patch that does not apply to that
	// tree — the repository is untouched. Returns the new tip SHA.
	AmendTipWithPatch(branch string, patch []byte) (string, error)
	// ResetHardIn runs `git reset --hard <ref>` in the worktree at dir (""
	// means the current worktree). Absorb-only: called after the staged content
	// is safely committed in the target branch (to drop the now-redundant
	// staged copy) or on a verified-clean worktree to sync it to its amended
	// HEAD — never anywhere uncommitted work could be lost.
	ResetHardIn(dir, ref string) error
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
