package git

// Shell is the production implementation of the stack engine's git port: every
// method shells out to the real git binary via the package-level functions. It
// is a zero-size value, so `git.Shell{}` is free to construct.
type Shell struct{}

func (Shell) RevParse(ref string) (string, error)          { return RevParse(ref) }
func (Shell) RebaseOnto(newBase, oldBase, br string) error { return RebaseOnto(newBase, oldBase, br) }
func (Shell) BranchExists(name string) bool                { return BranchExists(name) }
func (Shell) Checkout(name string) error                   { return Checkout(name) }
func (Shell) CreateBranch(name string) error               { return CreateBranch(name) }
func (Shell) DeleteBranch(name string, force bool) error   { return DeleteBranch(name, force) }
func (Shell) ForceBranch(name, ref string) error           { return ForceBranch(name, ref) }
func (Shell) ResetSoft(ref string) error                   { return ResetSoft(ref) }
func (Shell) Commit(message string, all bool) error        { return Commit(message, all) }
func (Shell) AmendNoEdit(all bool) error                   { return AmendNoEdit(all) }
func (Shell) AmendMessage(message string, all bool) error  { return AmendMessage(message, all) }
func (Shell) Add(paths ...string) error                    { return Add(paths...) }
func (Shell) RenameBranch(oldName, newName string) error   { return RenameBranch(oldName, newName) }
func (Shell) MergeBase(a, b string) (string, error)        { return MergeBase(a, b) }
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
func (Shell) IsClean() (bool, error)          { return IsClean() }
func (Shell) RebaseInProgress() (bool, error) { return RebaseInProgress() }
func (Shell) RebaseHeadName() (string, error) { return RebaseHeadName() }
func (Shell) RebaseContinue() error           { return RebaseContinue() }
