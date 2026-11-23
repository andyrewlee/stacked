// ops_combine.go — history-combining ops: Squash rewrites a branch's commits
// into one and restacks its descendants.
package stack

import (
	"fmt"
	"strings"
)

// Squash collapses every commit on the current branch (since its parent) into
// one, then restacks its descendants. With an empty message the squashed
// message is composed from the existing commit subjects.
func Squash(env Env, s *State, message string) (*OpResult, error) {
	g := env.Git
	if err := requireClean(g); err != nil {
		return nil, err
	}
	cur, b, err := currentTracked(g, s)
	if err != nil {
		return nil, err
	}
	needs, err := s.NeedsRestack(g, cur)
	if err != nil {
		return nil, err
	}
	if needs {
		return nil, fmt.Errorf("%q needs restack before squashing (run: st restack)", cur)
	}

	base := b.ParentSHA
	subjects, err := g.CommitSubjects(base, cur)
	if err != nil {
		return nil, err
	}
	if len(subjects) <= 1 {
		return &OpResult{Summary: fmt.Sprintf("%s already has a single commit; nothing to squash", cur), Branch: cur}, nil
	}
	origTip, err := g.RevParse(branchTipRef(cur))
	if err != nil {
		return nil, fmt.Errorf("resolving %q before squash: %w", cur, err)
	}
	if message == "" {
		message = subjects[len(subjects)-1]
		var body []string
		for i := len(subjects) - 2; i >= 0; i-- {
			body = append(body, "- "+subjects[i])
		}
		if len(body) > 0 {
			message += "\n\n" + strings.Join(body, "\n")
		}
	}

	if err := g.ResetSoft(base); err != nil {
		return nil, fmt.Errorf("resetting %q to base: %w", cur, err)
	}
	if err := g.Commit(message, false); err != nil {
		err = fmt.Errorf("creating squashed commit on %q: %w", cur, err)
		if restoreErr := g.ResetSoft(origTip); restoreErr != nil {
			return nil, AlsoFailed(err, fmt.Sprintf("restore %q to %s", cur, origTip), restoreErr)
		}
		return nil, err
	}

	upstack, err := finishUpstack(env, s, cur)
	if err != nil {
		return nil, err
	}
	return &OpResult{Summary: fmt.Sprintf("Squashed %d commits on %s into one", len(subjects), cur), Branch: cur, Restacked: upstack.restacked, Notes: upstack.notes}, nil
}
