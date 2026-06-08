package cmd

import (
	"encoding/json"
	"strings"
	"testing"

	"stacked/internal/stack"
)

// TestValidateProblemCategories constructs each malformed state and asserts
// validate reports the specific problem, returns an error (non-zero exit), and
// reflects it in the --json {ok:false, problems:[...]} shape (TEST-5). The cycle
// case exercises cyclePath's bespoke logic.
func TestValidateProblemCategories(t *testing.T) {
	cases := []struct {
		name    string
		setup   func(t *testing.T) // create any real git branches the case needs
		state   func(sha string) *stack.State
		wantSub string
	}{
		{
			name:  "missing_trunk",
			setup: func(t *testing.T) {},
			state: func(sha string) *stack.State {
				return &stack.State{Trunk: "ghost", Branches: map[string]*stack.Branch{}}
			},
			wantSub: `trunk branch "ghost" does not exist`,
		},
		{
			name:  "missing_git_branch",
			setup: func(t *testing.T) {},
			state: func(sha string) *stack.State {
				return &stack.State{Trunk: "main", Branches: map[string]*stack.Branch{
					"gone": {Name: "gone", Parent: "main", ParentSHA: sha},
				}}
			},
			wantSub: "gone is tracked but its git branch is missing",
		},
		{
			name:  "untracked_parent",
			setup: func(t *testing.T) { mustRun(t, "git", "branch", "a") },
			state: func(sha string) *stack.State {
				return &stack.State{Trunk: "main", Branches: map[string]*stack.Branch{
					"a": {Name: "a", Parent: "ghost", ParentSHA: sha},
				}}
			},
			wantSub: `a has parent "ghost" which is not the trunk or a tracked branch`,
		},
		{
			name:  "cycle",
			setup: func(t *testing.T) { mustRun(t, "git", "branch", "a"); mustRun(t, "git", "branch", "b") },
			state: func(sha string) *stack.State {
				return &stack.State{Trunk: "main", Branches: map[string]*stack.Branch{
					"a": {Name: "a", Parent: "b", ParentSHA: sha},
					"b": {Name: "b", Parent: "a", ParentSHA: sha},
				}}
			},
			wantSub: "part of a parent cycle",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			newRepo(t)
			c.setup(t)
			sha := mustRun(t, "git", "rev-parse", "HEAD")
			if err := c.state(sha).Save(); err != nil {
				t.Fatalf("save state: %v", err)
			}

			// Text mode: prints the problem and returns an error.
			var err error
			out := captureStdout(t, func() { err = runValidate(nil) })
			if err == nil {
				t.Fatalf("validate returned nil error, want a problem; output:\n%s", out)
			}
			if !strings.Contains(out, c.wantSub) {
				t.Fatalf("validate output missing %q:\n%s", c.wantSub, out)
			}

			// JSON mode: ok=false and the problem appears in problems[].
			outJSON := captureStdout(t, func() { _ = runValidate([]string{"--json"}) })
			var payload struct {
				OK       bool     `json:"ok"`
				Problems []string `json:"problems"`
			}
			if e := json.Unmarshal([]byte(outJSON), &payload); e != nil {
				t.Fatalf("validate --json not parseable: %v\n%s", e, outJSON)
			}
			if payload.OK {
				t.Fatalf("validate --json ok=true, want false")
			}
			found := false
			for _, p := range payload.Problems {
				if strings.Contains(p, c.wantSub) {
					found = true
				}
			}
			if !found {
				t.Fatalf("validate --json problems missing %q: %v", c.wantSub, payload.Problems)
			}
		})
	}
}
