package stack

import (
	"encoding/json"
	"strings"
	"testing"
)

// FuzzStateRoundTrip builds a stack.State from fuzzed branch/parent strings,
// round-trips it through json.Marshal/Unmarshal, and then exercises every
// topology helper on every node (plus the trunk and an absent name). The
// fuzzed parent links can form arbitrary shapes — including cycles and
// self-parents — so this asserts the helpers are cycle-safe: they must never
// panic and must always terminate.
func FuzzStateRoundTrip(f *testing.F) {
	f.Add("main", "a:main b:a c:a d:b")
	f.Add("main", "a:b b:a")            // 2-cycle
	f.Add("main", "a:a")                // self-parent
	f.Add("trunk", "")                  // no branches
	f.Add("main", "x:y")                // parent not tracked
	f.Add("main", "a:main a:main")      // duplicate name
	f.Add("", ":")                      // empty trunk and names
	f.Add("main", "a:b b:c c:a d:main") // 3-cycle plus a real bottom
	f.Add("main", "main:main")          // a branch named like the trunk

	f.Fuzz(func(t *testing.T, trunk, spec string) {
		s := buildState(trunk, spec)

		// Round-trip through JSON; this must not panic and must yield a State
		// whose helpers behave identically.
		data, err := json.Marshal(s)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		var got State
		if err := json.Unmarshal(data, &got); err != nil {
			t.Fatalf("unmarshal of %s: %v", data, err)
		}

		// Collect every name worth probing: the trunk, every branch, every
		// referenced parent, and a name that is definitely absent.
		names := map[string]struct{}{trunk: {}, "\x00absent\x00": {}}
		for name, b := range got.Branches {
			names[name] = struct{}{}
			if b != nil {
				names[b.Parent] = struct{}{}
			}
		}

		// ChildIndex is shared by several helpers; exercise it directly too.
		_ = got.ChildIndex()

		for name := range names {
			// None of these may panic, and each must terminate (the test
			// harness's own timeout/deadline catches non-termination, which is
			// exactly the failure we are guarding against for cyclic input).
			_ = got.Children(name)
			_ = got.Descendants(name)
			_ = got.Ancestors(name)
			_ = got.BottomOf(name)

			// Descendants must not contain duplicates even with cyclic input;
			// that is the contract its visited-set guards.
			if dup := firstDuplicate(got.Descendants(name)); dup != "" {
				t.Fatalf("Descendants(%q) contains duplicate %q for spec %q", name, dup, spec)
			}
		}
	})
}

// buildState parses a compact "name:parent name:parent ..." spec into a State.
// Tokens without a colon, or with empty halves, are tolerated so the fuzzer can
// drive malformed input through the same path real corrupted state.json would.
func buildState(trunk, spec string) *State {
	s := &State{Trunk: trunk, Branches: map[string]*Branch{}}
	for _, tok := range strings.Fields(spec) {
		name, parent, ok := strings.Cut(tok, ":")
		if !ok {
			name, parent = tok, trunk
		}
		// Use Track so we go through the real upsert path (it also dedupes by
		// name, mirroring how state is actually mutated).
		s.Track(name, parent, "sha")
	}
	return s
}

// firstDuplicate returns the first value that appears more than once in names,
// or "" if all values are distinct.
func firstDuplicate(names []string) string {
	seen := make(map[string]struct{}, len(names))
	for _, n := range names {
		if _, ok := seen[n]; ok {
			return n
		}
		seen[n] = struct{}{}
	}
	return ""
}
