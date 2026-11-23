package git

import (
	"strings"
	"testing"
)

// FuzzParseWorktrees hardens the legacy `git worktree list --porcelain` parser.
func FuzzParseWorktrees(f *testing.F) {
	f.Add("worktree /repo\nHEAD deadbeef\nbranch refs/heads/main\n\n")
	f.Add("worktree /repo\nbare\n\nworktree /wt/a\nHEAD abc\nbranch refs/heads/feat-a\n")
	f.Add("worktree /wt/x\nHEAD abc\ndetached\n")
	f.Add("worktree /wt/y\nlocked\n")
	f.Add("")
	f.Add("branch refs/heads/orphan\n")
	f.Add("worktree\nbranch\nHEAD\n")

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

// FuzzParseWorktreesZ hardens the NUL-terminated worktree parser.
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

// FuzzParsePushPorcelain hardens the push-outcome parser.
func FuzzParsePushPorcelain(f *testing.F) {
	f.Add("To /remote\n=\trefs/heads/a:refs/heads/a\t[up to date]\nDone\n")
	f.Add("To /r\n!\trefs/heads/b:refs/heads/b\t[rejected] non-fast-forward\n*\trefs/heads/a:refs/heads/a\t[new branch]\n")
	f.Add(" \trefs/heads/a:refs/heads/a\tupdated\n+\trefs/heads/c:refs/heads/c\tforced\n")
	f.Add("-\trefs/heads/b:refs/heads/b\tdeleted\n !\tmalformed\n\tshort\n")
	f.Add("!\trefs/heads/x:refs/heads/y\tnot requested\n")
	f.Add("")
	f.Add("To /r\r\n=\trefs/heads/a:refs/heads/a\tcrlf\r\n")

	branches := []string{"a", "b", "c"}
	f.Fuzz(func(t *testing.T, out string) {
		res := &PushResult{Status: map[string]PushStatus{}}
		parsePushPorcelain(out, branches, res)
		for name, st := range res.Status {
			found := false
			for _, b := range branches {
				if name == b {
					found = true
				}
			}
			if !found {
				t.Fatalf("status recorded for unrequested branch %q", name)
			}
			switch st {
			case PushCreated, PushUpdated, PushUpToDate, PushRejected:
			default:
				t.Fatalf("branch %q got unrecognized status %d", name, st)
			}
		}
	})
}
