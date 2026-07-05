package e2e

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestCrashShapeRecovery(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name                    string
		build                   func(r *repo)
		wantValidateExit        int
		wantValidateContains    []string
		wantNeedsRestack        []string
		recover                 [][]string
		wantTrackedAfterRecover []string
	}{
		{
			name: "modify_mid_upstack_parent_amended",
			// internal/stack/engine.go:263-270: Modify amends the parent before
			// finishUpstack restacks descendants and records their new bases.
			build: func(r *repo) {
				r.stOK("checkout", "feat-a")
				r.writeFile("a.txt", "a2\n")
				r.git("commit", "-qa", "--amend", "--no-edit")
			},
			wantValidateExit:     0,
			wantValidateContains: []string{"warnings:", "feat-b needs restack"},
			wantNeedsRestack:     []string{"feat-b"},
			recover:              [][]string{{"restack"}},
		},
		{
			name: "fold_after_git_before_save",
			// internal/stack/engine.go:374-394: Fold force-moves the parent,
			// checks it out, and deletes the folded branch before RemoveBranch is
			// saved to state.json.
			build: func(r *repo) {
				r.git("branch", "-f", "feat-b", r.rev("feat-c"))
				r.git("checkout", "feat-b")
				r.git("branch", "-D", "feat-c")
			},
			wantValidateExit:     1,
			wantValidateContains: []string{"problems:", "feat-c is tracked but its git branch is missing"},
			recover:              [][]string{{"repair"}, {"restack"}},
		},
		{
			name: "delete_after_state_save_before_child_restack",
			// internal/stack/engine.go:582-596: Delete removes the branch, saves
			// children re-parented onto the grandparent, then restacks those
			// children to drop the deleted branch's commits.
			build: func(r *repo) {
				oldBase := r.rev("feat-b")
				r.git("branch", "-D", "feat-b")
				rewriteCrashShapeState(r, func(s *crashShapeState) {
					delete(s.Branches, "feat-b")
					s.Branches["feat-c"].Parent = "feat-a"
					s.Branches["feat-c"].ParentSHA = oldBase
				})
			},
			wantValidateExit:     0,
			wantValidateContains: []string{"warnings:", "feat-c needs restack"},
			wantNeedsRestack:     []string{"feat-c"},
			recover:              [][]string{{"restack"}},
		},
		{
			name: "create_after_checkout_before_save",
			// internal/stack/engine.go:190-207: Create checks out the new git
			// branch before Track/save. An untracked branch is legal for
			// validate; `st track` adopts it as the current stack tip.
			build: func(r *repo) {
				r.git("checkout", "-b", "feat-d")
			},
			wantValidateExit:        0,
			wantValidateContains:    []string{"no problems found"},
			recover:                 [][]string{{"track"}},
			wantTrackedAfterRecover: []string{"feat-d"},
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			r := newRepo(t)
			r.initStack()
			r.create("feat-a", "a.txt", "a\n", "a")
			r.create("feat-b", "b.txt", "b\n", "b")
			r.create("feat-c", "c.txt", "c\n", "c")

			tt.build(r)

			res := r.st("validate")
			wantExit(t, res, tt.wantValidateExit)
			for _, sub := range tt.wantValidateContains {
				wantStdoutContains(t, res, sub)
			}

			root := crashShapeLog(t, r)
			for _, name := range tt.wantNeedsRestack {
				node := findNode(root, name)
				if node == nil {
					t.Fatalf("log --json missing branch %q", name)
				}
				if !node.NeedsRestack {
					t.Fatalf("%s needsRestack = false, want true", name)
				}
			}

			for _, args := range tt.recover {
				r.stOK(args...)
			}

			res = r.stOK("validate")
			wantStdoutContains(t, res, "no problems found")
			assertCrashShapeLogHealthy(t, crashShapeLog(t, r))
			for _, name := range tt.wantTrackedAfterRecover {
				if findNode(crashShapeLog(t, r), name) == nil {
					t.Fatalf("log --json missing recovered branch %q", name)
				}
			}
		})
	}
}

func crashShapeLog(t *testing.T, r *repo) *logNode {
	t.Helper()
	res := r.stOK("log", "--json")
	var root logNode
	if err := json.Unmarshal([]byte(res.stdout), &root); err != nil {
		t.Fatalf("log --json invalid: %v\n%s", err, res.stdout)
	}
	return &root
}

type crashShapeBranch struct {
	Name      string `json:"name"`
	Parent    string `json:"parent"`
	ParentSHA string `json:"parentSHA"`
}

type crashShapeState struct {
	Trunk    string                       `json:"trunk"`
	Branches map[string]*crashShapeBranch `json:"branches"`
	Pending  map[string]string            `json:"pendingReparent,omitempty"`
}

func rewriteCrashShapeState(r *repo, update func(*crashShapeState)) {
	r.t.Helper()
	path := filepath.Join(r.dir, ".git", "stacked", "state.json")
	data, err := os.ReadFile(path)
	if err != nil {
		r.t.Fatalf("read state.json: %v", err)
	}
	var state crashShapeState
	if err := json.Unmarshal(data, &state); err != nil {
		r.t.Fatalf("decode state.json: %v\n%s", err, data)
	}
	update(&state)
	data, err = json.MarshalIndent(state, "", "  ")
	if err != nil {
		r.t.Fatalf("encode state.json: %v", err)
	}
	if err := os.WriteFile(path, append(data, '\n'), 0o644); err != nil {
		r.t.Fatalf("write state.json: %v", err)
	}
}

func assertCrashShapeLogHealthy(t *testing.T, root *logNode) {
	t.Helper()
	var walk func(*logNode)
	walk = func(n *logNode) {
		if n == nil {
			return
		}
		if n.NeedsRestack {
			t.Fatalf("%s still needs restack after recovery", n.Name)
		}
		for _, child := range n.Children {
			walk(child)
		}
	}
	walk(root)
}
