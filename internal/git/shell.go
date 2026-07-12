package git

// Shell is the production implementation of the stack engine's git port: every
// method shells out to the real git binary via the package-level functions. It
// is a zero-size value, so `git.Shell{}` is free to construct.
type Shell struct{}

// QuietShell is the production git port variant used by JSON-mode commands.
// It avoids inheriting git's human stdout/stderr for rebase operations so the
// CLI can keep JSON output parseable.
type QuietShell struct {
	Shell
}

func (Shell) RevParse(ref string) (string, error)          { return RevParse(ref) }
func (Shell) RebaseOnto(newBase, oldBase, br string) error { return RebaseOnto(newBase, oldBase, br) }
func (Shell) BranchExists(name string) bool                { return BranchExists(name) }
func (Shell) Tips() (map[string]string, error)             { return Tips() }
func (Shell) TipsFor(names []string) (map[string]string, error) {
	return TipsFor(names)
}

func (Shell) MergedInto(ref string) (map[string]bool, error) {
	return MergedInto(ref)
}
func (Shell) Checkout(name string) error                  { return Checkout(name) }
func (Shell) CheckoutDetach(ref string) error             { return CheckoutDetach(ref) }
func (Shell) CreateBranch(name string) error              { return CreateBranch(name) }
func (Shell) CreateBranchAt(name, ref string) error       { return CreateBranchAt(name, ref) }
func (Shell) DeleteBranch(name string, force bool) error  { return DeleteBranch(name, force) }
func (Shell) ForceBranch(name, ref string) error          { return ForceBranch(name, ref) }
func (Shell) UpdateRef(ref, sha string) error             { return UpdateRef(ref, sha) }
func (Shell) UpdateRefs(updates map[string]string) error  { return UpdateRefs(updates) }
func (Shell) ResetSoft(ref string) error                  { return ResetSoft(ref) }
func (Shell) Commit(message string, all bool) error       { return Commit(message, all) }
func (Shell) AmendNoEdit(all bool) error                  { return AmendNoEdit(all) }
func (Shell) AmendMessage(message string, all bool) error { return AmendMessage(message, all) }
func (Shell) Add(paths ...string) error                   { return Add(paths...) }
func (Shell) RenameBranch(oldName, newName string) error  { return RenameBranch(oldName, newName) }
func (Shell) MergeBase(a, b string) (string, error)       { return MergeBase(a, b) }
func (Shell) IsAncestor(ancestor, descendant string) (bool, error) {
	return IsAncestor(ancestor, descendant)
}
func (Shell) CurrentBranch() (string, error) { return CurrentBranch() }
func (Shell) CommitSubjects(base, br string) ([]string, error) {
	return CommitSubjects(base, br)
}
func (Shell) HasStagedChanges() (bool, error) { return HasStagedChanges() }
func (Shell) HasUnstagedChanges() (bool, error) {
	return HasUnstagedChanges()
}
func (Shell) IsClean() (bool, error)                          { return IsClean() }
func (Shell) RebaseInProgress() (bool, error)                 { return RebaseInProgress() }
func (Shell) RebaseHeadName() (string, error)                 { return RebaseHeadName() }
func (Shell) RebaseContinue() error                           { return RebaseContinue() }
func (Shell) RebaseAbort() error                              { return RebaseAbort() }
func (Shell) AncestorSet(ref string) (map[string]bool, error) { return AncestorSet(ref) }
func (Shell) Worktrees() ([]Worktree, error)                  { return Worktrees() }
func (Shell) RebaseOntoIn(dir, newBase, oldBase, br string) error {
	return RebaseOntoIn(dir, newBase, oldBase, br)
}
func (Shell) RebaseAbortIn(dir string) error              { return RebaseAbortIn(dir) }
func (Shell) IsCleanIn(dir string) (bool, error)          { return IsCleanIn(dir) }
func (Shell) WorktreeRemove(dir string, force bool) error { return WorktreeRemove(dir, force) }

func (QuietShell) RebaseOnto(newBase, oldBase, br string) error {
	return RebaseOntoQuiet(newBase, oldBase, br)
}
func (QuietShell) RebaseContinue() error { return RebaseContinueQuiet() }
