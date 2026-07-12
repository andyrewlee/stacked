package stack

// Tests moved with the pure .worktreeinclude helpers from cmd/ (plan 068 slice
// 1); assertions are unchanged. The fs-bound containment suite stays in
// cmd/worktree_test.go.

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseIncludePatterns(t *testing.T) {
	in := "# a comment\n\nnode_modules\n  target  \n# trailing\n./build\n../outside.txt\n/abs/path\nsub/../../escape\n"
	got := ParseIncludePatterns(in)
	want := []string{"node_modules", "target", "./build", "../outside.txt", "/abs/path", "sub/../../escape"}
	if len(got) != len(want) {
		t.Fatalf("ParseIncludePatterns = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("entry[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestValidateWorktreeIncludePathRejectsUnsafePaths(t *testing.T) {
	absolute := filepath.Join(t.TempDir(), "outside.txt")
	unsafe := []string{
		absolute,
		".",
		"..",
		"../outside.txt",
		"dir/../../outside.txt",
	}
	for _, rel := range unsafe {
		if _, err := ValidateWorktreeIncludePath(rel); err == nil {
			t.Errorf("ValidateWorktreeIncludePath(%q) error = nil, want unsafe path error", rel)
		}
	}

	if got, err := ValidateWorktreeIncludePath("./safe/../ignored.txt"); err != nil || got != "ignored.txt" {
		t.Fatalf("ValidateWorktreeIncludePath safe path = %q, %v; want ignored.txt, nil", got, err)
	}
}

// FuzzMatchGlobSegments hardens the recursive `**` matcher added in #127:
// never panic, the only legitimate error is filepath.ErrBadPattern, and the
// matcher is deterministic. The `**` backtracking is worst-case exponential
// in the number of ** segments — this fuzz proves safety, not speed, so the
// inputs are bounded.
func FuzzMatchGlobSegments(f *testing.F) {
	f.Add("packages/**/node_modules", "packages/one/node_modules")
	f.Add("a/**/f.txt", "a/b/f.txt")
	f.Add("a/**/f.txt", "a/f.txt") // ** matches zero dirs
	f.Add("**/**/x", "a/b/x")      // adjacent **
	f.Add("dist/*", "dist/a.js")
	f.Add("*", "")
	f.Add("[", "x") // bad pattern -> ErrBadPattern, not a panic
	f.Add("", "")

	f.Fuzz(func(t *testing.T, patStr, nameStr string) {
		pat := strings.Split(patStr, "/")
		name := strings.Split(nameStr, "/")
		if len(pat) > 16 || len(name) > 64 {
			return
		}
		ok, err := MatchGlobSegments(pat, name)
		if err != nil && !errors.Is(err, filepath.ErrBadPattern) {
			t.Fatalf("MatchGlobSegments(%q,%q) returned unexpected error type: %v", patStr, nameStr, err)
		}
		ok2, err2 := MatchGlobSegments(pat, name)
		if ok != ok2 || (err == nil) != (err2 == nil) {
			t.Fatalf("MatchGlobSegments not deterministic for (%q,%q)", patStr, nameStr)
		}
	})
}
