package cmd

import (
	"errors"
	"reflect"
	"testing"

	"github.com/andyrewlee/stacked/internal/git"
	"github.com/andyrewlee/stacked/internal/stack"
)

// branchStubGit is a stack.Git stub that counts CurrentBranch probes behind
// a programmable answer and succeeds at every mutating call. It pins the
// per-instance memo's contract (gitenv.go): one probe per port until a
// HEAD-moving wrapper clears it. Embedding the port interface means any
// unstubbed method panics — the tests below only exercise the listed ones.
type branchStubGit struct {
	stack.Git
	probes     int
	answer     string
	err        error
	rootProbes int
	root       string
	rootErr    error
}

func (g *branchStubGit) CurrentBranch() (string, error) {
	g.probes++
	return g.answer, g.err
}

func (g *branchStubGit) RepoRoot() (string, error) {
	g.rootProbes++
	return g.root, g.rootErr
}

func (g *branchStubGit) Checkout(name string) error              { return nil }
func (g *branchStubGit) CheckoutDetach(ref string) error         { return nil }
func (g *branchStubGit) RenameBranch(o, n string) error          { return nil }
func (g *branchStubGit) WorktreeRemove(dir string, f bool) error { return nil }
func (g *branchStubGit) RebaseOnto(a, b, c string) error         { return nil }
func (g *branchStubGit) RebaseOntoIn(d, a, b, c string) error    { return nil }
func (g *branchStubGit) RebaseContinue() error                   { return nil }
func (g *branchStubGit) RebaseAbort() error                      { return nil }
func (g *branchStubGit) RebaseAbortIn(dir string) error          { return nil }

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
		invoke func(c *cachedPort) error
		want   []any
	}
	cases := []call{
		{"RebaseOnto", func(c *cachedPort) error { return c.RebaseOnto("nb", "ob", "br") }, []any{"RebaseOnto", "nb", "ob", "br"}},
		{"RebaseOntoIn", func(c *cachedPort) error { return c.RebaseOntoIn("dir", "nb", "ob", "br") }, []any{"RebaseOntoIn", "dir", "nb", "ob", "br"}},
		{"RebaseContinue", func(c *cachedPort) error { return c.RebaseContinue() }, []any{"RebaseContinue"}},
		{"RebaseAbort", func(c *cachedPort) error { return c.RebaseAbort() }, []any{"RebaseAbort"}},
		{"RebaseAbortIn", func(c *cachedPort) error { return c.RebaseAbortIn("dir") }, []any{"RebaseAbortIn", "dir"}},
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
					c := &cachedPort{Git: g}
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

// TestCachedPortCurrentBranchMemo pins the per-port memo: repeated reads
// probe git once, a detached-HEAD error memoizes like a name, and the
// memoized answer cannot leak between ports — a fresh port probes again.
func TestCachedPortCurrentBranchMemo(t *testing.T) {
	g := &branchStubGit{answer: "feat", err: nil}
	c := &cachedPort{Git: g}
	for i := 0; i < 3; i++ {
		name, err := c.CurrentBranch()
		if err != nil || name != "feat" {
			t.Fatalf("read %d = %q, %v", i, name, err)
		}
	}
	if g.probes != 1 {
		t.Fatalf("memoized port probed %d times, want 1", g.probes)
	}

	// A detached HEAD memoizes the error, not just the name.
	gd := &branchStubGit{err: git.ErrDetachedHEAD}
	cd := &cachedPort{Git: gd}
	for i := 0; i < 2; i++ {
		if _, err := cd.CurrentBranch(); err != git.ErrDetachedHEAD {
			t.Fatalf("detached read %d = %v, want ErrDetachedHEAD", i, err)
		}
	}
	if gd.probes != 1 {
		t.Fatalf("detached memo probed %d times, want 1", gd.probes)
	}

	// RepoRoot memoizes the same way — the worktree-removal loops re-ask it
	// once per candidate through CwdWithinWorktree.
	gr := &branchStubGit{root: "/repo"}
	cr := &cachedPort{Git: gr}
	for i := 0; i < 3; i++ {
		if root, err := cr.RepoRoot(); err != nil || root != "/repo" {
			t.Fatalf("root read %d = %q, %v", i, root, err)
		}
	}
	if gr.rootProbes != 1 {
		t.Fatalf("repo-root memo probed %d times, want 1", gr.rootProbes)
	}

	// The memo is per instance: a fresh port probes again and can see a
	// different answer — no cross-command leak through a shared port.
	g2 := &branchStubGit{answer: "main", err: nil}
	if name, err := (&cachedPort{Git: g2}).CurrentBranch(); err != nil || name != "main" {
		t.Fatalf("fresh port read = %q, %v", name, err)
	}
}

// TestCachedPortCurrentBranchInvalidates pins the second half of the
// contract: every mutating wrapper clears this port's memo, so the next
// read re-probes and observes the new HEAD.
func TestCachedPortCurrentBranchInvalidates(t *testing.T) {
	mutations := map[string]func(c *cachedPort) error{
		"WorktreeRemove": func(c *cachedPort) error { return c.WorktreeRemove("/wt", false) },
		"Checkout":       func(c *cachedPort) error { return c.Checkout("b") },
		"CheckoutDetach": func(c *cachedPort) error { return c.CheckoutDetach("deadbeef") },
		"RenameBranch":   func(c *cachedPort) error { return c.RenameBranch("o", "n") },
		"RebaseOnto":     func(c *cachedPort) error { return c.RebaseOnto("nb", "ob", "br") },
		"RebaseOntoIn":   func(c *cachedPort) error { return c.RebaseOntoIn("d", "nb", "ob", "br") },
		"RebaseContinue": func(c *cachedPort) error { return c.RebaseContinue() },
		"RebaseAbort":    func(c *cachedPort) error { return c.RebaseAbort() },
		"RebaseAbortIn":  func(c *cachedPort) error { return c.RebaseAbortIn("d") },
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			g := &branchStubGit{answer: "before", root: "/old"}
			c := &cachedPort{Git: g}
			if _, err := c.CurrentBranch(); err != nil {
				t.Fatal(err)
			}
			if _, err := c.RepoRoot(); err != nil {
				t.Fatal(err)
			}
			g.answer = "after"
			g.root = "/new"
			if err := mutate(c); err != nil {
				t.Fatalf("%s: %v", name, err)
			}
			got, err := c.CurrentBranch()
			if err != nil || got != "after" {
				t.Fatalf("post-%s read = %q, %v — memo not cleared", name, got, err)
			}
			if g.probes != 2 {
				t.Fatalf("%s: probes = %d, want 2 (prime + re-probe)", name, g.probes)
			}
			root, err := c.RepoRoot()
			if err != nil || root != "/new" {
				t.Fatalf("post-%s root = %q, %v — memo not cleared", name, root, err)
			}
			if g.rootProbes != 2 {
				t.Fatalf("%s: root probes = %d, want 2", name, g.rootProbes)
			}
		})
	}
}
