package cmd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/andyrewlee/stacked/internal/git"
	"github.com/andyrewlee/stacked/internal/stack"
)

// collectNames walks a decoded JSON tree collecting every node name; it
// fails if a child slot is null (a pruned visit must be omitted, not emitted
// as null) or a name renders twice.
func collectNames(t *testing.T, n *logNode, counts map[string]int) {
	t.Helper()
	if n == nil {
		t.Fatal("null node reached")
	}
	counts[n.Name]++
	for _, c := range n.Children {
		if c == nil {
			t.Fatalf("node %q has a null child — pruned visits must be skipped, not emitted", n.Name)
		}
		collectNames(t, c, counts)
	}
}

// countTextNames counts each name's rendered "○ name" line in text log
// output. Test output is uncolored (piped stdout), so every line is exactly
// "<indent>○ <name>" with no annotations when the logData maps are empty.
func countTextNames(t *testing.T, text string) map[string]int {
	t.Helper()
	counts := map[string]int{}
	for _, line := range strings.Split(text, "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 || (fields[0] != "○" && fields[0] != "◉") {
			continue
		}
		counts[fields[1]]++
	}
	return counts
}

func syntheticLogData(index map[string][]string) *logData {
	return &logData{
		index:     index,
		cur:       "",
		drift:     map[string]bool{},
		tips:      map[string]string{},
		subjects:  map[string]string{},
		ancestors: map[ancestorPair]bool{},
		wtInfo:    map[string]worktreeInfo{},
	}
}

func syntheticState(branches map[string]string) *stack.State {
	s := &stack.State{Trunk: "main", Branches: map[string]*stack.Branch{}}
	for name, parent := range branches {
		s.Branches[name] = &stack.Branch{Name: name, Parent: parent, ParentSHA: "sha-" + parent}
	}
	return s
}

// TestLogCycleTraversalBounded pins the defensive bound on both log
// renderers: a corrupt child index (self-cycle, reachable cycle, duplicate
// edge) cannot recurse forever or render a name twice. Persisted corruption
// is refused at the decoder — this bounds the renderers themselves.
func TestLogCycleTraversalBounded(t *testing.T) {
	cases := []struct {
		name     string
		branches map[string]string
		index    map[string][]string
		want     map[string]int // expected render count per name
	}{
		{
			name:     "self cycle on trunk",
			branches: map[string]string{},
			index:    map[string][]string{"main": {"main"}},
			want:     map[string]int{"main": 1},
		},
		{
			name:     "reachable a-b-a cycle",
			branches: map[string]string{"a": "main", "b": "a"},
			index:    map[string][]string{"main": {"a"}, "a": {"b"}, "b": {"a"}},
			want:     map[string]int{"main": 1, "a": 1, "b": 1},
		},
		{
			name:     "duplicate child reference",
			branches: map[string]string{"a": "main"},
			index:    map[string][]string{"main": {"a", "a"}},
			want:     map[string]int{"main": 1, "a": 1},
		},
		{
			name:     "siblings sharing a child",
			branches: map[string]string{"a": "main", "b": "main", "shared": "a"},
			index:    map[string][]string{"main": {"a", "b"}, "a": {"shared"}, "b": {"shared"}},
			want:     map[string]int{"main": 1, "a": 1, "b": 1, "shared": 1},
		},
		{
			name:     "healthy chain",
			branches: map[string]string{"a": "main", "b": "a"},
			index:    map[string][]string{"main": {"a"}, "a": {"b"}},
			want:     map[string]int{"main": 1, "a": 1, "b": 1},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := syntheticState(tc.branches)
			d := syntheticLogData(tc.index)

			jsonOut := captureStdout(t, func() {
				if err := printLogJSON(s, d); err != nil {
					t.Fatalf("printLogJSON: %v", err)
				}
			})
			if strings.Contains(jsonOut, "null") {
				t.Fatalf("JSON output contains a null child:\n%s", jsonOut)
			}
			var root logNode
			if err := json.Unmarshal([]byte(jsonOut), &root); err != nil {
				t.Fatalf("JSON output does not decode: %v\n%s", err, jsonOut)
			}
			if root.Name != "main" {
				t.Fatalf("root name = %q, want main", root.Name)
			}
			jsonCounts := map[string]int{}
			collectNames(t, &root, jsonCounts)
			for name, want := range tc.want {
				if jsonCounts[name] != want {
					t.Fatalf("JSON rendered %q %d times, want %d:\n%s", name, jsonCounts[name], want, jsonOut)
				}
				delete(jsonCounts, name)
			}
			for name, got := range jsonCounts {
				t.Fatalf("JSON rendered unexpected name %q (%d times):\n%s", name, got, jsonOut)
			}

			text := captureStdout(t, func() {
				printLogTree(s, d)
			})
			textCounts := countTextNames(t, text)
			for name, want := range tc.want {
				if textCounts[name] != want {
					t.Fatalf("text rendered %q %d times, want %d:\n%s", name, textCounts[name], want, text)
				}
			}
		})
	}
}

// TestLogHealthyOrderUnchanged compares a healthy main→a→b tree against its
// established shape and order: JSON nests a under main and b under a; text
// prints deepest-first so b precedes a precedes main.
func TestLogHealthyOrderUnchanged(t *testing.T) {
	s := syntheticState(map[string]string{"a": "main", "b": "a"})
	d := syntheticLogData(map[string][]string{"main": {"a"}, "a": {"b"}})

	jsonOut := captureStdout(t, func() {
		if err := printLogJSON(s, d); err != nil {
			t.Fatalf("printLogJSON: %v", err)
		}
	})
	var root logNode
	if err := json.Unmarshal([]byte(jsonOut), &root); err != nil {
		t.Fatalf("JSON output does not decode: %v", err)
	}
	if root.Name != "main" || len(root.Children) != 1 || root.Children[0].Name != "a" {
		t.Fatalf("healthy JSON tree wrong at root: %+v", root)
	}
	a := root.Children[0]
	if len(a.Children) != 1 || a.Children[0].Name != "b" || len(a.Children[0].Children) != 0 {
		t.Fatalf("healthy JSON tree wrong below a: %+v", a)
	}

	text := captureStdout(t, func() {
		printLogTree(s, d)
	})
	ib := strings.Index(text, "○ b")
	ia := strings.Index(text, "○ a")
	im := strings.Index(text, "○ main")
	if ib < 0 || ia < 0 || im < 0 || ib >= ia || ia >= im {
		t.Fatalf("healthy text order wrong (b=%d a=%d main=%d):\n%s", ib, ia, im, text)
	}
}

// corruptStateWithTrunkRecord rewrites state.json so the ordinary a/b records
// stay but the trunk itself appears as a tracked branch — the structural
// corruption the shared decoder must refuse.
func corruptStateWithTrunkRecord(t *testing.T, stateFile string) {
	t.Helper()
	raw, err := os.ReadFile(stateFile)
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	var branches map[string]json.RawMessage
	if err := json.Unmarshal(doc["branches"], &branches); err != nil {
		t.Fatal(err)
	}
	branches["main"] = json.RawMessage(`{"name":"main","parent":"main"}`)
	encoded, err := json.Marshal(branches)
	if err != nil {
		t.Fatal(err)
	}
	doc["branches"] = encoded
	writeJSONFile(t, stateFile, doc)
}

// TestTrunkStateRefusesBeforeMutation proves every command path — reads and
// mutations alike — refuses a persisted trunk record at the shared decoder
// before touching state, journal, refs, or HEAD. Fresh fixtures per subtest
// so a refusal cannot hide a later write.
func TestTrunkStateRefusesBeforeMutation(t *testing.T) {
	commands := []struct {
		name string
		run  func() error
	}{
		{"log text", func() error { return runLog(nil) }},
		{"log json", func() error { return runLog([]string{"--json"}) }},
		{"prune", func() error { return runPrune(nil) }},
		{"sync --no-fetch", func() error { return runSync([]string{"--no-fetch"}) }},
		{"validate", func() error { return runValidate(nil) }},
		{"repair", func() error { return runRepair(nil) }},
	}
	for _, tc := range commands {
		t.Run(tc.name, func(t *testing.T) {
			newRepo(t)
			mustInit(t)
			mustCreate(t, "feat-a", "a.txt", "a\n", "a")
			mustCreate(t, "feat-b", "b.txt", "b\n", "b")

			gitDir, err := git.GitCommonDir()
			if err != nil {
				t.Fatal(err)
			}
			stateFile := filepath.Join(gitDir, "stacked", "state.json")
			corruptStateWithTrunkRecord(t, stateFile)
			snap := takeRepoSnapshot(t)

			err = tc.run()
			if err == nil || !strings.Contains(err.Error(), "corrupted") {
				t.Fatalf("%s = %v, want a corruption error", tc.name, err)
			}
			snap.check(t)
		})
	}
}

// TestUndoTrunkSnapshotAdapterBarrier pins the journal barrier at the cmd
// adapter: an undo entry whose serialized state tracks the trunk must refuse
// before the entry is dropped, HEAD moves, or the branch it created is
// deleted — the same refusal the engine test pins, at the boundary the user
// actually crosses.
func TestUndoTrunkSnapshotAdapterBarrier(t *testing.T) {
	newRepo(t)
	mustInit(t)
	mustCreate(t, "feat-a", "a.txt", "a\n", "a")
	mustCreate(t, "feat-b", "b.txt", "b\n", "b") // entry records createdBranches=[feat-b]

	gitDir, err := git.GitCommonDir()
	if err != nil {
		t.Fatal(err)
	}
	undoFile := filepath.Join(gitDir, "stacked", "undo.json")

	// Replace the newest entry's state snapshot with a trunk-record document,
	// leaving every other journal byte intact.
	raw, err := os.ReadFile(undoFile)
	if err != nil {
		t.Fatal(err)
	}
	var entries []map[string]json.RawMessage
	if err := json.Unmarshal(raw, &entries); err != nil {
		t.Fatal(err)
	}
	if len(entries) == 0 {
		t.Fatal("expected at least one undo entry")
	}
	entries[len(entries)-1]["state"] = json.RawMessage(
		`{"version":1,"trunk":"main","branches":{"main":{"parent":"main"},"feat-a":{"name":"feat-a","parent":"main"}}}`)
	writeJSONFile(t, undoFile, entries)

	snap := takeRepoSnapshot(t)

	if err := runUndo(nil); err == nil || !strings.Contains(err.Error(), "corrupted") {
		t.Fatalf("undo = %v, want a corruption error", err)
	}
	snap.check(t)

	// The entry stayed in the journal: ListUndo still sees the same count.
	listed, err := stack.ListUndo()
	if err != nil {
		t.Fatal(err)
	}
	if len(listed) != len(entries) {
		t.Fatalf("journal has %d entries after refused undo, want %d", len(listed), len(entries))
	}
}

// TestUndoTrunkCorruptCurrentStateRecovery pins the intentional asymmetry: a
// corrupt CURRENT state falls back to the journal snapshot (unlike a corrupt
// snapshot, which refuses). Undo restores the valid snapshot bytes and drops
// the entry — the decoder guard protects snapshots without disabling the
// nil-current-state recovery path.
func TestUndoTrunkCorruptCurrentStateRecovery(t *testing.T) {
	newRepo(t)
	mustInit(t)
	mustCreate(t, "feat-a", "a.txt", "a\n", "a")

	gitDir, err := git.GitCommonDir()
	if err != nil {
		t.Fatal(err)
	}
	stateFile := filepath.Join(gitDir, "stacked", "state.json")
	entriesBefore, err := stack.ListUndo()
	if err != nil {
		t.Fatal(err)
	}
	corruptStateWithTrunkRecord(t, stateFile)

	if err := runUndo(nil); err != nil {
		t.Fatalf("undo with valid snapshot over corrupt current state: %v", err)
	}

	// The valid snapshot was restored: feat-a (created after it) is untracked
	// and its branch deleted, and the journal dropped exactly one entry.
	s := stateT(t)
	if s.IsTracked("feat-a") {
		t.Fatal("undo did not restore the snapshot state")
	}
	if mustRun(t, "git", "branch", "--list", "feat-a") != "" {
		t.Fatal("undo left the created branch behind")
	}
	entriesAfter, err := stack.ListUndo()
	if err != nil {
		t.Fatal(err)
	}
	if len(entriesAfter) != len(entriesBefore)-1 {
		t.Fatalf("journal has %d entries after undo, want %d", len(entriesAfter), len(entriesBefore)-1)
	}
}
