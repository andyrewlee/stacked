package cmd

import (
	"errors"
	"reflect"
	"testing"

	"github.com/andyrewlee/stacked/internal/git"
	"github.com/andyrewlee/stacked/internal/stack"
)

// rebaseRecordingGit is a stack.Git stub that records the arguments each
// rebase method received and returns a configured error. Embedding the port
// interface means every other method panics if reached — the test only
// exercises the rebase family.
type rebaseRecordingGit struct {
	stack.Git
	calls [][]any
	err   error
}

func (g *rebaseRecordingGit) RebaseOnto(newBase, oldBase, branch string) error {
	g.calls = append(g.calls, []any{"RebaseOnto", newBase, oldBase, branch})
	return g.err
}

func (g *rebaseRecordingGit) RebaseOntoIn(dir, newBase, oldBase, branch string) error {
	g.calls = append(g.calls, []any{"RebaseOntoIn", dir, newBase, oldBase, branch})
	return g.err
}

func (g *rebaseRecordingGit) RebaseContinue() error {
	g.calls = append(g.calls, []any{"RebaseContinue"})
	return g.err
}

func (g *rebaseRecordingGit) RebaseAbort() error {
	g.calls = append(g.calls, []any{"RebaseAbort"})
	return g.err
}

func (g *rebaseRecordingGit) RebaseAbortIn(dir string) error {
	g.calls = append(g.calls, []any{"RebaseAbortIn", dir})
	return g.err
}

// TestCachedPortRebaseInvalidatesOwnership pins the cache-invalidation
// contract of every rebase wrapper: each forwards its arguments verbatim and
// returns the inner error unchanged (errors.Is preserved), and each resets
// the memoized worktree list — on failure too, because a failed rebase can
// still have moved or detached HEAD in a worktree. These tests are serial:
// they mutate the package-global cache.
func TestCachedPortRebaseInvalidatesOwnership(t *testing.T) {
	sentinel := errors.New("rebase exploded")

	type call struct {
		name   string
		invoke func(c cachedPort) error
		want   []any
	}
	cases := []call{
		{"RebaseOnto", func(c cachedPort) error { return c.RebaseOnto("nb", "ob", "br") }, []any{"RebaseOnto", "nb", "ob", "br"}},
		{"RebaseOntoIn", func(c cachedPort) error { return c.RebaseOntoIn("dir", "nb", "ob", "br") }, []any{"RebaseOntoIn", "dir", "nb", "ob", "br"}},
		{"RebaseContinue", func(c cachedPort) error { return c.RebaseContinue() }, []any{"RebaseContinue"}},
		{"RebaseAbort", func(c cachedPort) error { return c.RebaseAbort() }, []any{"RebaseAbort"}},
		{"RebaseAbortIn", func(c cachedPort) error { return c.RebaseAbortIn("dir") }, []any{"RebaseAbortIn", "dir"}},
	}

	for _, innerErr := range []error{nil, sentinel} {
		name := "success"
		if innerErr != nil {
			name = "error"
		}
		t.Run(name, func(t *testing.T) {
			for _, tc := range cases {
				t.Run(tc.name, func(t *testing.T) {
					g := &rebaseRecordingGit{err: innerErr}
					c := cachedPort{Git: g}
					seedWorktreeCache(t, []git.Worktree{{Path: "/wt", Branch: "b"}})

					err := tc.invoke(c)
					if !errors.Is(err, innerErr) {
						t.Fatalf("%s error = %v, want errors.Is(%v)", tc.name, err, innerErr)
					}
					if len(g.calls) != 1 || !reflect.DeepEqual(g.calls[0], tc.want) {
						t.Fatalf("%s forwarded call = %v, want %v", tc.name, g.calls, tc.want)
					}
					worktreeCacheState.Lock()
					probed, wts := worktreeCacheState.probed, worktreeCacheState.wts
					worktreeCacheState.Unlock()
					if probed || wts != nil {
						t.Fatalf("%s left the worktree cache warm (probed=%v wts=%v)", tc.name, probed, wts)
					}
				})
			}
		})
	}
}
