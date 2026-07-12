package git

import (
	"strings"
	"testing"
)

// FuzzParseWorktrees hardens the `git worktree list --porcelain` parser: no
// panic on arbitrary input, branch refs always stripped of refs/heads/, and a
// record is only ever opened by a "worktree" line.
func FuzzParseWorktrees(f *testing.F) {
	f.Add("worktree /repo\nHEAD deadbeef\nbranch refs/heads/main\n\n")
	f.Add("worktree /repo\nbare\n\nworktree /wt/a\nHEAD abc\nbranch refs/heads/feat-a\n")
	f.Add("worktree /wt/x\nHEAD abc\ndetached\n")
	f.Add("worktree /wt/y\nlocked\n")
	f.Add("")
	f.Add("branch refs/heads/orphan\n") // attribute before any worktree line
	f.Add("worktree\nbranch\nHEAD\n")   // keys with no values

	f.Fuzz(func(t *testing.T, out string) {
		got := parseWorktrees(out)
		for _, wt := range got {
			if strings.HasPrefix(wt.Branch, "refs/heads/") {
				t.Fatalf("parseWorktrees left an unstripped branch ref %q", wt.Branch)
			}
		}
		var worktreeLines int
		for _, line := range strings.Split(out, "\n") {
			key, _, _ := strings.Cut(line, " ")
			if key == "worktree" {
				worktreeLines++
			}
		}
		if len(got) > worktreeLines {
			t.Fatalf("parseWorktrees produced %d records from %d worktree lines", len(got), worktreeLines)
		}
	})
}
