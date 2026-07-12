package stack

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
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

func TestLoadCorruptState(t *testing.T) {
	initGitRepo(t)
	if _, err := Init("main"); err != nil {
		t.Fatalf("Init: %v", err)
	}
	path, err := statePath()
	if err != nil {
		t.Fatalf("statePath: %v", err)
	}
	if err := os.WriteFile(path, []byte("{not json\n"), 0o644); err != nil {
		t.Fatalf("write corrupt state: %v", err)
	}

	_, err = Load()
	if err == nil {
		t.Fatal("Load corrupt state succeeded, want error")
	}
	msg := err.Error()
	if !strings.Contains(msg, path) && !strings.Contains(msg, "state.json") {
		t.Fatalf("Load corrupt state error = %q, want state path", msg)
	}
	if !strings.Contains(msg, "fix or delete") && !strings.Contains(msg, "re-run st init") {
		t.Fatalf("Load corrupt state error = %q, want recovery hint", msg)
	}
	if errors.Is(err, ErrNotInitialized) {
		t.Fatalf("Load corrupt state error = %v, want generic error not ErrNotInitialized", err)
	}
}

func TestStackedDirPerWorkingDirectory(t *testing.T) {
	repoA := initGitRepo(t)
	pathA, err := statePath()
	if err != nil {
		t.Fatalf("statePath repo A: %v", err)
	}

	repoB := initGitRepo(t)
	pathB, err := statePath()
	if err != nil {
		t.Fatalf("statePath repo B: %v", err)
	}

	if pathA == pathB {
		t.Fatalf("state paths should differ across working directories: %s", pathA)
	}
	if want := filepath.Join(repoA, ".git", "stacked", "state.json"); pathA != want {
		t.Errorf("statePath repo A = %q, want %q", pathA, want)
	}
	if want := filepath.Join(repoB, ".git", "stacked", "state.json"); pathB != want {
		t.Errorf("statePath repo B = %q, want %q", pathB, want)
	}
}

func TestAtomicWriteFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nested", "data.txt")

	// Writes through a missing parent directory and reads back exactly.
	if err := atomicWriteFile(path, []byte("hello\n")); err != nil {
		t.Fatalf("atomicWriteFile: %v", err)
	}
	if got, err := os.ReadFile(path); err != nil || string(got) != "hello\n" {
		t.Fatalf("read back = %q, %v; want %q", got, err, "hello\n")
	}

	// Overwriting replaces the content and leaves no temp files behind.
	if err := atomicWriteFile(path, []byte("world\n")); err != nil {
		t.Fatalf("atomicWriteFile overwrite: %v", err)
	}
	if got, _ := os.ReadFile(path); string(got) != "world\n" {
		t.Errorf("after overwrite = %q, want %q", got, "world\n")
	}
	files, err := os.ReadDir(filepath.Join(dir, "nested"))
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 {
		var names []string
		for _, f := range files {
			names = append(names, f.Name())
		}
		t.Errorf("directory has %d entries %v, want exactly the target file", len(files), names)
	}
}

func TestRestoreStateRoundTrips(t *testing.T) {
	dir := initGitRepo(t)

	if err := RestoreState([]byte(`{"trunk":"main","branches":{}}`)); err != nil {
		t.Fatalf("RestoreState: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(dir, ".git", "stacked", "state.json"))
	if err != nil {
		t.Fatalf("read state file: %v", err)
	}
	if len(data) == 0 || data[len(data)-1] != '\n' {
		t.Error("RestoreState did not write a trailing newline")
	}
	s, err := Load()
	if err != nil {
		t.Fatalf("Load after RestoreState: %v", err)
	}
	if s.Trunk != "main" {
		t.Errorf("restored trunk = %q, want main", s.Trunk)
	}
}

// Save goes through the atomic temp+rename writer; it must not leave any .tmp
// turds behind in the stacked dir (twin of TestWriteUndoLeavesNoTempFiles).
func TestSaveLeavesNoTempFiles(t *testing.T) {
	initGitRepo(t)

	s := &State{Trunk: "main", Branches: map[string]*Branch{}}
	if err := s.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}
	dir, err := stackedDir()
	if err != nil {
		t.Fatalf("stackedDir: %v", err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read stacked dir: %v", err)
	}
	sawState := false
	for _, e := range entries {
		if e.Name() == "state.json" {
			sawState = true
		}
		if strings.Contains(e.Name(), ".tmp") {
			t.Errorf("leftover temp file after Save: %s", e.Name())
		}
	}
	if !sawState {
		t.Fatal("Save did not produce state.json")
	}
}

// A failed temp-file creation (unwritable dir) must leave the original file
// byte-identical — the crash-consistency half that fires on permission
// problems and full disks.
func TestAtomicWriteFileUnwritableDir(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("a read-only directory bit does not block file creation on windows; the rename-failure test covers windows")
	}
	if os.Geteuid() == 0 {
		t.Skip("chmod is advisory for root")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "data.txt")
	if err := atomicWriteFile(path, []byte("old\n")); err != nil {
		t.Fatalf("seed write: %v", err)
	}
	if err := os.Chmod(dir, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })

	err := atomicWriteFile(path, []byte("new\n"))
	if err == nil || !strings.Contains(err.Error(), "create temp file") {
		t.Fatalf("error = %v, want create temp file failure", err)
	}
	if got, readErr := os.ReadFile(path); readErr != nil || string(got) != "old\n" {
		t.Fatalf("original = %q, %v; want untouched %q", got, readErr, "old\n")
	}
	entries, readErr := os.ReadDir(dir)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if len(entries) != 1 {
		var names []string
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Fatalf("dir entries = %v, want only the original file", names)
	}
}

// A failed rename (target is a non-empty directory, which fails on every
// platform) must clean its temp file up and leave the target untouched.
func TestAtomicWriteFileRenameFailure(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "data.txt")
	if err := os.MkdirAll(filepath.Join(path, "x"), 0o755); err != nil {
		t.Fatal(err)
	}

	err := atomicWriteFile(path, []byte("new\n"))
	if err == nil || !strings.Contains(err.Error(), "rename temp file") {
		t.Fatalf("error = %v, want rename temp file failure", err)
	}
	if _, statErr := os.Stat(filepath.Join(path, "x")); statErr != nil {
		t.Fatalf("target directory's child disturbed: %v", statErr)
	}
	entries, readErr := os.ReadDir(dir)
	if readErr != nil {
		t.Fatal(readErr)
	}
	for _, e := range entries {
		if strings.Contains(e.Name(), ".tmp") {
			t.Errorf("leftover temp file after failed rename: %s", e.Name())
		}
	}
}
