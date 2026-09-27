package cmd

import (
	"fmt"
	"os/exec"
	"strings"
	"testing"
)

// BenchmarkLogHistory measures runLog as history depth and rendered branch
// count grow. Baseline (pre-fix) materialized every commit reachable from the
// rendered tips via `git rev-list --parents`; the target answers each distinct
// (childTip, parentTip) ancestry question with a bounded merge-base probe.
// Before/after numbers live in plans/010-log-benchmark-results.md.
func BenchmarkLogHistory(b *testing.B) {
	for _, commits := range []int{1000, 20000} {
		for _, branches := range []int{0, 1, 10, 50} {
			b.Run(fmt.Sprintf("commits=%d/branches=%d", commits, branches), func(b *testing.B) {
				benchLogRepo(b, commits, branches)
				b.ReportAllocs()
				b.ResetTimer()
				for i := 0; i < b.N; i++ {
					if err := runLog([]string{"--json"}); err != nil {
						b.Fatalf("log --json: %v", err)
					}
				}
			})
		}
	}
}

// benchLogRepo builds a disposable repo with commits linear commits on main
// and branches tracked branches stacked at evenly spaced tips, then chdirs
// into it. All fixture work happens before the caller's timer starts: history
// is streamed in one `git fast-import` (marks :1..:commits on main, `reset`
// records pointing each branch at its mark) rather than per-commit spawns.
func benchLogRepo(b *testing.B, commits, branches int) {
	b.Helper()
	dir := b.TempDir()
	b.Chdir(dir)
	resetWorktreeCache()
	benchRun(b, "git", "init", "-q", "-b", "main")
	benchRun(b, "git", "config", "user.email", "test@example.com")
	benchRun(b, "git", "config", "user.name", "test")

	var stream strings.Builder
	fmt.Fprintf(&stream, "commit refs/heads/main\nmark :1\n")
	fmt.Fprintf(&stream, "committer bench <b@b> 1700000000 +0000\ndata 5\ninit\n")
	fmt.Fprintf(&stream, "M 644 inline f.txt\ndata 5\nbase\n\n")
	for i := 2; i <= commits; i++ {
		fmt.Fprintf(&stream, "commit refs/heads/main\nmark :%d\n", i)
		msg := fmt.Sprintf("c%d", i)
		fmt.Fprintf(&stream, "committer bench <b@b> %d +0000\ndata %d\n%s\nfrom :%d\n\n", 1700000000+i, len(msg), msg, i-1)
	}
	// Branch tips ascend the chain: feat-i points at mark commits*i/(K+1), so
	// each tracked branch's tip strictly contains its parent's tip.
	for i := 1; i <= branches; i++ {
		mark := commits * i / (branches + 1)
		name := fmt.Sprintf("feat-%02d", i)
		fmt.Fprintf(&stream, "reset refs/heads/%s\nfrom :%d\n\n", name, mark)
	}
	fi := exec.Command("git", "fast-import", "--quiet")
	fi.Stdin = strings.NewReader(stream.String())
	if out, err := fi.CombinedOutput(); err != nil {
		b.Fatalf("fast-import: %v\n%s", err, out)
	}

	if err := runInit([]string{"--trunk", "main"}); err != nil {
		b.Fatalf("init: %v", err)
	}
	parent := "main"
	for i := 1; i <= branches; i++ {
		name := fmt.Sprintf("feat-%02d", i)
		if err := runTrack([]string{name, "--parent", parent}); err != nil {
			b.Fatalf("track %s: %v", name, err)
		}
		parent = name
	}
}

func benchRun(b *testing.B, name string, args ...string) {
	b.Helper()
	if out, err := exec.Command(name, args...).CombinedOutput(); err != nil {
		b.Fatalf("%s %v: %v\n%s", name, args, err, out)
	}
}
