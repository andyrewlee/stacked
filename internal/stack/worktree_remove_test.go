package stack

import (
	"errors"
	"testing"
)

// The removal classifier's gate order is a contract: path-safety checks run
// before any probe, and the standing holds (locked, stale) win over state
// probes (paused, dirty) so a worktree that is several things at once reports
// the most stable reason first.
func TestClassifyWorktreeRemovalGateOrder(t *testing.T) {
	f := newFakeGit()
	staleErr := errors.New("probe must not run")

	cases := []struct {
		name    string
		probes  RemovalProbes
		want    WorktreeRemovalVerdict
		wantErr bool
	}{
		{
			name:   "locked beats stale",
			probes: RemovalProbes{Locked: true, Stale: true},
			want:   RemovalLocked,
		},
		{
			name:   "stale beats paused and dirty without probing",
			probes: RemovalProbes{Stale: true, Paused: func(string) (bool, error) { return false, staleErr }},
			want:   RemovalStale,
		},
		{
			name:   "paused beats dirty",
			probes: RemovalProbes{Paused: func(string) (bool, error) { return true, nil }},
			want:   RemovalPausedRebase,
		},
		{
			name:   "dirty after all holds clear",
			probes: RemovalProbes{Paused: func(string) (bool, error) { return false, nil }, Clean: func(string) (bool, error) { return false, nil }},
			want:   RemovalDirty,
		},
		{
			name:   "clean live worktree is removable",
			probes: RemovalProbes{Paused: func(string) (bool, error) { return false, nil }, Clean: func(string) (bool, error) { return true, nil }},
			want:   RemovalOK,
		},
		{
			name:    "probe errors surface",
			probes:  RemovalProbes{Paused: func(string) (bool, error) { return false, staleErr }},
			wantErr: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			verdict, err := ClassifyWorktreeRemoval(f, "/wt/x", "/main", tc.probes)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("verdict = %v, want the probe error surfaced", verdict)
				}
				return
			}
			if err != nil {
				t.Fatalf("classify: %v", err)
			}
			if verdict != tc.want {
				t.Fatalf("verdict = %v, want %v", verdict, tc.want)
			}
		})
	}
}
