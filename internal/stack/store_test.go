package stack

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// initGitRepo creates a throwaway git repository in a temporary directory and
// chdirs into it for the duration of the test. statePath and friends operate on
// the repo containing the current working directory, so the working directory
// must point at the temp repo.
func initGitRepo(t *testing.T) string {
	t.Helper()

	dir := t.TempDir()

	cmds := [][]string{
		{"init", "-b", "main"},
		{"config", "user.email", "test@example.com"},
		{"config", "user.name", "Test"},
	}
	for _, args := range cmds {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v failed: %v\n%s", args, err, out)
		}
	}

	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("chdir to temp repo: %v", err)
	}
	t.Cleanup(func() {
		if err := os.Chdir(wd); err != nil {
			t.Errorf("restore working dir: %v", err)
		}
	})

	// Resolve symlinks (macOS TempDir lives under /var -> /private/var) so the
	// returned path matches what git reports.
	resolved, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatalf("eval symlinks: %v", err)
	}
	return resolved
}

func TestInitLoadSaveRoundTrip(t *testing.T) {
	dir := initGitRepo(t)

	// Init creates the state file under .git/stacked/state.json.
	s, err := Init("main")
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	if s.Trunk != "main" {
		t.Errorf("Init trunk = %q, want main", s.Trunk)
	}

	wantPath := filepath.Join(dir, ".git", "stacked", "state.json")
	if _, err := os.Stat(wantPath); err != nil {
		t.Fatalf("state file not created at %s: %v", wantPath, err)
	}

	// Initializing twice must fail.
	if _, err := Init("main"); err == nil {
		t.Error("Init second time succeeded, want error")
	}

	// Mutate and persist.
	s.Track("feature-a", "main", "deadbeef")
	s.Track("feature-b", "feature-a", "cafef00d")
	if err := s.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}

	// The persisted JSON must be valid and end with a trailing newline.
	data, err := os.ReadFile(wantPath)
	if err != nil {
		t.Fatalf("read state file: %v", err)
	}
	if len(data) == 0 || data[len(data)-1] != '\n' {
		t.Error("state file does not end with a trailing newline")
	}

	// Load reads the same data back.
	loaded, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if loaded.Trunk != "main" {
		t.Errorf("loaded trunk = %q, want main", loaded.Trunk)
	}
	if len(loaded.Branches) != 2 {
		t.Fatalf("loaded %d branches, want 2", len(loaded.Branches))
	}

	a, ok := loaded.Get("feature-a")
	if !ok {
		t.Fatal("feature-a missing after Load")
	}
	if a.Parent != "main" || a.ParentSHA != "deadbeef" {
		t.Errorf("feature-a = {Parent:%q ParentSHA:%q}, want {main deadbeef}", a.Parent, a.ParentSHA)
	}
	b, ok := loaded.Get("feature-b")
	if !ok {
		t.Fatal("feature-b missing after Load")
	}
	if b.Parent != "feature-a" || b.ParentSHA != "cafef00d" {
		t.Errorf("feature-b = {Parent:%q ParentSHA:%q}, want {feature-a cafef00d}", b.Parent, b.ParentSHA)
	}

	// Topology survives the round-trip.
	if got := loaded.Descendants("main"); len(got) != 2 || got[0] != "feature-a" || got[1] != "feature-b" {
		t.Errorf("Descendants(main) after round-trip = %v, want [feature-a feature-b]", got)
	}
}

func TestLoadNotInitialized(t *testing.T) {
	initGitRepo(t)

	if _, err := Load(); err != ErrNotInitialized {
		t.Errorf("Load on uninitialized repo = %v, want ErrNotInitialized", err)
	}
}
