package stack

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/andyrewlee/stacked/internal/git"
)

// repoDirs bundles the two rev-parse resolutions every stacked path derives
// from: the repository's common git dir and the stacked metadata dir under
// it. Both are cwd-keyed — cmd integration tests chdir between repos in one
// process, and each cwd must resolve its own pair.
type repoDirs struct{ stacked, common string }

var stackedDirCache sync.Map // cwd -> repoDirs

// stateSchemaVersion is the version of the state.json schema this binary
// writes and understands. Save stamps it into every file; Load refuses a file
// with a higher version (see ErrStateTooNew) so that downgrading st cannot
// silently drop fields a newer st wrote. History: v1 is the original schema;
// files written before versioning existed carry no version field and load as
// v0, which is v1-compatible. When the schema changes, bump this and give Load
// a migrate-or-refuse path for each older version.
const stateSchemaVersion = 1

// stackedDir returns the absolute path of the per-repository stacked metadata
// directory. It uses the common git dir so the stack is shared across all linked
// worktrees of a repository rather than being per-worktree.
//
// Port-boundary note: this reaches git through the package-level helper
// git.GitCommonDir, not Env.Git — deliberately. It is a persistence-layer
// environment probe (where does the state file live) that runs before Env is
// constructed: cmd locks and loads the state, then builds Env{Git, Save: s.Save}.
// It is not part of any engine op, so no engine test needs to fake it — and a
// port method could not serve it anyway: FakeGit has no repository to locate,
// and threading Git into Load/Save/Lock would make s.Save depend on the Env it
// belongs to.
func stackedDir() (string, error) {
	dirs, err := repoDirsForCwd()
	return dirs.stacked, err
}

// CommonDir returns the repository's common git dir through the same
// cwd-keyed resolution as stackedDir, so a process that both locates the
// state file and derives worktree paths probes `git rev-parse
// --git-common-dir` once per cwd, not once per consumer.
func CommonDir() (string, error) {
	dirs, err := repoDirsForCwd()
	return dirs.common, err
}

// repoDirsForCwd resolves (or recalls) the repo's dirs for the current
// working directory. Keyed by cwd, not sync.Once: cmd integration tests
// chdir between repos in one process, and each repo must resolve its own
// common git dir.
func repoDirsForCwd() (repoDirs, error) {
	cwd, err := os.Getwd()
	if err == nil {
		if dirs, ok := stackedDirCache.Load(cwd); ok {
			return dirs.(repoDirs), nil
		}
	}

	gitDir, gerr := git.GitCommonDir()
	if gerr != nil {
		return repoDirs{}, fmt.Errorf("locate git dir: %w", gerr)
	}
	dirs := repoDirs{stacked: filepath.Join(gitDir, "stacked"), common: gitDir}
	if err == nil {
		stackedDirCache.Store(cwd, dirs)
	}
	return dirs, nil
}

// statePath returns the absolute path of the stacked state file,
// <common git dir>/stacked/state.json.
func statePath() (string, error) {
	dir, err := stackedDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "state.json"), nil
}

// Init creates a new stacked state file for the given trunk branch. It returns
// an error if the state file already exists.
func Init(trunk string) (*State, error) {
	path, err := statePath()
	if err != nil {
		return nil, err
	}
	if _, err := os.Stat(path); err == nil {
		return nil, fmt.Errorf("stacked is already initialized (%s)", path)
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("stat state file: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("create stacked dir: %w", err)
	}
	s := &State{Trunk: trunk, Branches: make(map[string]*Branch)}
	if err := s.Save(); err != nil {
		return nil, err
	}
	return s, nil
}

// Load reads and parses the stacked state file. It returns ErrNotInitialized if
// the file does not exist.
func Load() (*State, error) {
	path, err := statePath()
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, ErrNotInitialized
		}
		return nil, fmt.Errorf("read state file: %w", err)
	}
	s, err := decodeState(data)
	if err != nil {
		if errors.Is(err, ErrStateTooNew) {
			return nil, err
		}
		return nil, fmt.Errorf("parse state file %s (fix or delete it and re-run st init): %w", path, err)
	}
	return s, nil
}

// decodeState parses serialized State bytes and enforces the schema barrier
// shared by every entry point that interprets them — the on-disk state file
// (Load) and undo journal snapshots (Undo, ValidateUndoState). Malformed
// bytes are a plain error, a version above stateSchemaVersion is a wrapped
// ErrStateTooNew, and a v0 document (no version field — written before
// versioning existed) is accepted as v1-compatible. Branches is always
// non-nil in the result so an accepted legacy snapshot behaves identically
// to a current one.
func decodeState(data []byte) (*State, error) {
	var s State
	if err := json.Unmarshal(data, &s); err != nil {
		return nil, err
	}
	if s.Version > stateSchemaVersion {
		return nil, fmt.Errorf("%w (schema v%d; this st understands v%d) — upgrade st or check for a downgrade", ErrStateTooNew, s.Version, stateSchemaVersion)
	}
	if s.Branches == nil {
		s.Branches = make(map[string]*Branch)
	}
	// A hand-edited or corrupted file can disagree with itself: the map key
	// is the identity every topology helper keys on, while Branch.Name is
	// what messages display. A divergence reads as phantom "different name"
	// claims downstream — refuse it at the single entry point all state
	// bytes pass through. A missing Name is a legacy-v0 file: backfill it.
	for key, b := range s.Branches {
		if b == nil {
			return nil, fmt.Errorf("state file is corrupted: branch %q has no record", key)
		}
		if b.Name == "" {
			b.Name = key
			continue
		}
		if b.Name != key {
			return nil, fmt.Errorf("state file is corrupted: branch map key %q does not match its recorded name %q (run `st validate` / `st repair`, or fix and reload)", key, b.Name)
		}
	}
	return &s, nil
}

// ValidateUndoState applies the state schema barrier to a serialized State
// carried by an undo journal entry — the compatibility check cmd must run
// before undo preparation touches worktrees, refs, or cwd. Compatible bytes
// (including legacy v0) return nil; a snapshot written by a newer st returns
// a wrapped ErrStateTooNew; malformed bytes return a plain error the caller
// surfaces like the parse errors Undo itself produces.
func ValidateUndoState(data []byte) error {
	_, err := decodeState(data)
	return err
}

// DecodeUndoState parses the State snapshot a journal entry carries under the
// same schema barrier as ValidateUndoState, returning the State itself.
// Multi-step undo uses it to chain steps: after undoing entry j+1 the live
// state IS entry j+1's snapshot, so entry j's preview/gates run against
// DecodeUndoState(entry[j+1].State).
func DecodeUndoState(data []byte) (*State, error) {
	return decodeState(data)
}

// Save atomically writes the state to disk as pretty-printed JSON with a
// trailing newline. The schema version is stamped on every write so a future
// (or older) st can tell which schema produced the file.
func (s *State) Save() error {
	path, err := statePath()
	if err != nil {
		return err
	}
	s.Version = stateSchemaVersion
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return fmt.Errorf("encode state: %w", err)
	}
	return atomicWriteFile(path, append(data, '\n'))
}

// atomicWriteFile writes data to path so a reader never observes a half-written
// file and the result survives an OS/power crash: it writes a temporary file in
// the same directory, fsyncs it, renames it over path (rename is atomic within a
// filesystem), and best-effort fsyncs the parent directory so the rename entry
// itself is durable. The parent directory is created if necessary. A crash or
// full disk mid-write leaves either the old file or the new one intact, never a
// truncated mix. This is the single writer used for all on-disk stacked metadata
// (state.json and undo.json).
func atomicWriteFile(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create stacked dir: %w", err)
	}
	tmp, err := os.CreateTemp(dir, filepath.Base(path)+".*.tmp")
	if err != nil {
		return fmt.Errorf("create temp file: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("write temp file: %w", err)
	}
	// Flush the file's data before the rename: without it, most filesystems can
	// make the rename durable while the new file's bytes are still in the page
	// cache, so a power loss could resurrect a zero-length or partial file —
	// exactly the truncated mix the rename is meant to rule out.
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("sync temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temp file: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("rename temp file: %w", err)
	}
	syncDir(dir)
	return nil
}

// syncDir flushes a directory entry to disk so a rename within it is durable
// across an OS/power crash, not merely visible to readers in the running
// kernel. Best-effort: directory fsync is not supported on every platform (for
// example Windows), and a failure here does not make the freshly written file
// wrong — only less crash-durable.
func syncDir(dir string) {
	d, err := os.Open(dir)
	if err != nil {
		return
	}
	_ = d.Sync()
	_ = d.Close()
}
