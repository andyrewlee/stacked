package git

import (
	"strings"
	"testing"
)

// FuzzParseWorktrees hardens the legacy `git worktree list --porcelain` parser:
// no panic on arbitrary input, branch refs always stripped of refs/heads/, and
// a record is only ever opened by a "worktree" line.
func FuzzParseWorktrees(f *testing.F) {
	f.Add("worktree /repo\nHEAD deadbeef\nbranch refs/heads/main\n\n")
	f.Add("worktree /repo\nbare\n\nworktree /wt/a\nHEAD abc\nbranch refs/heads/feat-a\n")
	f.Add("worktree /wt/x\nHEAD abc\ndetached\n")
	f.Add("worktree /wt/y\nlocked\n")
	f.Add("")
	f.Add("branch refs/heads/orphan\n") // attribute before any worktree line
	f.Add("worktree\nbranch\nHEAD\n")   // keys with no values

	f.Fuzz(func(t *testing.T, out string) {
		got, _ := parseWorktreesLegacy(out)
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

// FuzzParseWorktreesZ hardens the NUL-terminated parser: no panic on arbitrary
// input and branch refs always stripped of refs/heads/. NUL-separated fields
// cannot smuggle records, so every returned Worktree opened from a "worktree "
// field in the input.
func FuzzParseWorktreesZ(f *testing.F) {
	f.Add("worktree /repo\x00HEAD deadbeef\x00branch refs/heads/main\x00\x00")
	f.Add("worktree /wt/a\nb c\x00HEAD abc\x00detached\x00locked why not\x00\x00")
	f.Add("worktree /bare\x00bare\x00\x00")
	f.Add("")
	f.Add("HEAD abc\x00worktree /x\x00")
	f.Add("worktree\x00branch\x00")

	f.Fuzz(func(t *testing.T, out string) {
		got, _ := parseWorktreesZ(out)
		for _, wt := range got {
			if strings.HasPrefix(wt.Branch, "refs/heads/") {
				t.Fatalf("parseWorktreesZ left an unstripped branch ref %q", wt.Branch)
			}
			if strings.Contains(wt.Path, "\x00") {
				t.Fatalf("parseWorktreesZ path contains a NUL: %q", wt.Path)
			}
		}
	})
}
