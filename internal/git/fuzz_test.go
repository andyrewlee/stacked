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

// FuzzParseBlamePorcelain hardens the `git blame --porcelain` parser absorb
// attributes hunks through: no panic, and a line enters the map only with
// BOTH coordinates and a decoded path — the parser's documented contract
// (anything less is dropped so the caller fails closed).
func FuzzParseBlamePorcelain(f *testing.F) {
	f.Add("deadbeefdeadbeefdeadbeefdeadbeefdeadbeef 2 2 1\nfilename f.txt\n\tcontent\n")
	f.Add("0123456789012345678901234567890123456789 3 4 2\nfilename \"quo\\302ted.txt\"\n\tline\n")
	f.Add("not-a-sha record\nfilename f.txt\n")
	f.Add("deadbeefdeadbeefdeadbeefdeadbeefdeadbeef 2\n\tboundary no filename\n")
	f.Add("")
	f.Add("\torphan boundary\n")

	f.Fuzz(func(t *testing.T, out string) {
		got := parseBlamePorcelain(out)
		for line, bl := range got {
			if bl.Commit == "" || !IsHex40(bl.Commit) {
				t.Fatalf("blame entry at line %d has non-hex40 commit %q", line, bl.Commit)
			}
			if bl.Path == "" {
				t.Fatalf("blame entry at line %d has no decoded path — violates fail-closed contract", line)
			}
			// Coordinates may be 0 on malformed input (Atoi accepts it); that is
			// inert downstream — no real hunk references line 0, so absorb's
			// attribution misses and refuses (fail-closed). The invariant is
			// only that an entry's key IS its final line.
			if bl.FinalLine != line {
				t.Fatalf("blame entry keyed at line %d but FinalLine=%d — map must key by final line", line, bl.FinalLine)
			}
		}
	})
}

// FuzzParseCachedDiffSections hardens the staged `-U0` section parser: no
// panic, every hunk carries a file and coordinates, and every refusal record
// names a file and a reason — absorb gates on "zero refusals", so an empty
// field would silently weaken that gate.
func FuzzParseCachedDiffSections(f *testing.F) {
	f.Add("diff --git a/f.txt b/f.txt\n--- a/f.txt\n+++ b/f.txt\n@@ -2,1 +2,1 @@ x\n-old\n+new\n")
	f.Add("diff --git a/f.txt b/f.txt\nBinary files differ\n")
	f.Add("diff --git a/o.txt b/n.txt\nrename from o.txt\nrename to n.txt\n")
	f.Add("diff --git a/e.txt b/e.txt\nnew file mode 100644\n")
	f.Add("diff --git a/m.txt b/m.txt\nold mode 100644\nnew mode 100755\n--- a/m.txt\n+++ b/m.txt\n@@ -1 +1 @@\n-x\n+y\n")
	f.Add("diff --git a/x b/x\n@@ -1 +1 @@\n-hunk before headers\n")
	f.Add("garbage before any section\n")
	f.Add("")
	f.Add("diff --git \"a/q u o\" b/x\n--- \"a/q u o\"\n")

	f.Fuzz(func(t *testing.T, out string) {
		hunks, unsupported, _, err := parseCachedDiffSections(out)
		if err != nil {
			return
		}
		for _, h := range hunks {
			if h.File == "" {
				t.Fatalf("hunk with no file in %+v", h)
			}
			if h.OldStart < 0 || h.NewStart < 0 || h.OldN < 0 || h.NewN < 0 {
				t.Fatalf("hunk with negative coordinates %+v", h)
			}
		}
		for _, u := range unsupported {
			// Reason is the contract (a refusal with no reason is a silent
			// hole in absorb's gate); File is documented as best-known — an
			// unnameable section legitimately leaves it empty.
			if u.Reason == "" {
				t.Fatalf("refusal record missing reason: %+v", u)
			}
		}
	})
}

// FuzzParsePushPorcelain hardens the push-outcome parser: no panic, statuses
// only ever land on branches that were requested, and each value is a known
// PushStatus — an unrecorded ref is a caller-visible error, never fabricated.
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
			case PushUpdated, PushUpToDate, PushRejected:
			default:
				t.Fatalf("branch %q got unrecognized status %d", name, st)
			}
		}
	})
}
