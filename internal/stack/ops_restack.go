// ops_restack.go — the restack entry points: Restack (current subtree) and
// RestackAllOp (the whole forest). The rebase mechanics they drive live in
// restack.go; their dry-run planners live there too.
package stack

// Restack rebases the current branch and its upstack onto their parents. From
// the trunk it restacks every tracked branch. Requires a clean working tree.
func Restack(env Env, s *State) (*OpResult, error) {
	g := env.Git
	if err := requireClean(g); err != nil {
		return nil, err
	}
	start, err := g.CurrentBranch()
	if err != nil {
		return nil, err
	}

	var rebased []string
	if start != s.Trunk {
		did, err := s.restackBranch(env, start)
		if err != nil {
			return nil, err
		}
		if did {
			rebased = append(rebased, start)
		}
	}
	up, err := s.restackUpstack(env, start)
	if err != nil {
		err = restoreHEADAfterNonConflict(env, start, s.Trunk, err)
		return nil, err
	}
	rebased = append(rebased, up...)
	return restackEpilogue(env, s, start, rebased)
}

// RestackAllOp restacks every tracked branch (parents before children),
// regardless of the current branch — the whole forest is the trunk's upstack,
// so unlike Restack it never rebases the current branch specially (the walk
// covers it) and the current branch may even be untracked. Requires a clean
// tree; restores HEAD afterwards.
func RestackAllOp(env Env, s *State) (*OpResult, error) {
	g := env.Git
	if err := requireClean(g); err != nil {
		return nil, err
	}
	start, err := g.CurrentBranch()
	if err != nil {
		return nil, err
	}
	rebased, err := restackAll(env, s)
	if err != nil {
		return nil, restoreHEADAfterNonConflict(env, start, s.Trunk, err)
	}
	return restackEpilogue(env, s, start, rebased)
}
