package cmd

import (
	"flag"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"testing"
)

var updateGolden = flag.Bool("update", false, "update golden files")

var (
	ansiRE = regexp.MustCompile("\x1b\\[[0-9;]*m")
	shaRE  = regexp.MustCompile(`"parentSHA": "[0-9a-f]{40}"`)
)

// golden compares got against testdata/<name>.golden, or rewrites it under
// -update. Golden tests make user-visible output changes deliberate, reviewable
// diffs.
// testdataDir resolves testdata against this source file so golden lookups work
// even after a test t.Chdir's into a temp repo.
func testdataDir() string {
	_, file, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(file), "testdata")
}

func golden(t *testing.T, name, got string) {
	t.Helper()
	path := filepath.Join(testdataDir(), name+".golden")
	if *updateGolden {
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read golden %s (regenerate with: go test ./cmd -run Golden -update): %v", path, err)
	}
	if got != string(want) {
		t.Fatalf("output mismatch for %s\n--- got ---\n%s\n--- want ---\n%s", name, got, want)
	}
}

func TestGoldenHelp(t *testing.T) {
	golden(t, "help", ansiRE.ReplaceAllString(captureStdout(t, func() { printHelp(false) }), ""))
}

func TestGoldenLogTree(t *testing.T) {
	newRepo(t)
	mustInit(t)
	mustCreate(t, "feat-a", "a.txt", "a\n", "add a")
	mustCreate(t, "feat-b", "b.txt", "b\n", "add b")
	out := captureStdout(t, func() { _ = runLog(nil) })
	golden(t, "log_tree", ansiRE.ReplaceAllString(out, ""))
}

func TestGoldenLogJSON(t *testing.T) {
	newRepo(t)
	mustInit(t)
	mustCreate(t, "feat-a", "a.txt", "a\n", "add a")
	mustCreate(t, "feat-b", "b.txt", "b\n", "add b")
	out := captureStdout(t, func() { _ = runLog([]string{"--json"}) })
	golden(t, "log_json", shaRE.ReplaceAllString(out, `"parentSHA": "<sha>"`))
}

func TestGoldenStatusJSON(t *testing.T) {
	newRepo(t)
	mustInit(t)
	mustCreate(t, "feat-a", "a.txt", "a\n", "add a")
	out := captureStdout(t, func() { _ = runStatus([]string{"--json"}) })
	golden(t, "status_json", out)
}
