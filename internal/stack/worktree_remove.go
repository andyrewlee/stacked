package stack

// WorktreeRemovalVerdict classifies whether a branch's owning worktree may
// be removed, and the canonical reason when not. The removal sites — `st
// worktree rm`, `st worktree rm --all`, the engine's delete-time release,
// and undo's created-worktree cleanup — share this classification and
// differ only in how they render it: a hard error, a skipped row, or a
// release-path refusal. Each site's error text and the rm --all `wtSkip*`
// JSON contract strings stay local to the site; this type only decides the
// verdict.
type WorktreeRemovalVerdict int

const (
	RemovalOK WorktreeRemovalVerdict = iota
	// RemovalCheckedOutMain — the owner is the main worktree: not a
	// removable linked worktree.
	RemovalCheckedOutMain
	// RemovalInsideCaller — removing the worktree would delete the
	// process's own cwd.
	RemovalInsideCaller
	// RemovalLocked — the worktree is locked (git worktree lock). A lock is
	// a standing hold: whatever it was set for, the worktree must not be
	// torn down until it is unlocked.
	RemovalLocked
	// RemovalStale — the registration is dead: the worktree's directory is
	// gone (prunable, or the path fails to stat). Probes on the path can
	// only fail; the remedy is `st worktree prune`, which frees the
	// branch's checkout. Checked after Locked: a locked stale row reports
	// the lock, then staleness on the next run — each step's remedy works.
	RemovalStale
	// RemovalPausedRebase — a rebase is in progress inside the worktree.
	RemovalPausedRebase
	// RemovalDirty — uncommitted changes; in-progress work is never
	// silently discarded.
	RemovalDirty
)

// RemovalProbes supplies the per-worktree answers
// ClassifyWorktreeRemoval evaluates. Each getter is invoked lazily, only
// when the gates before it passed — a single-shot path passes the live
// probe, rm --all's bounded fan-out passes a closure over its precomputed
// slots, and a nil getter means the site does not run that gate at all:
// the engine's release path has no paused gate (a paused worktree
// classifies through its dirty index, as it always did), and undo's
// cleanup answers "clean" from its own precomputed plan slots. Locked is
// not a probe — the flag arrives already parsed on the caller's
// git.Worktree entry.
type RemovalProbes struct {
	Locked bool
	// Stale, like Locked, arrives as a known fact on the caller's
	// git.Worktree entry (WorktreeStale(owner)) — it needs no probe: a dead
	// registration's path is gone, so the Paused/Clean probes below it
	// could only report `git -C` noise.
	Stale  bool
	Paused func(path string) (bool, error)
	Clean  func(path string) (bool, error)
}

// ClassifyWorktreeRemoval evaluates the removal gates for the worktree at
// ownerPath in canonical order — main-owner, inside-cwd, locked, stale,
// paused, dirty —
// and returns the first failing verdict, or RemovalOK when all pass. The
// caller resolved the owner itself (LinkedOwnerOf for the rm paths,
// OwnerOf for the engine's release — they differ on whether the main
// worktree counts as an owner, which is why mainPath is an input rather
// than an internal lookup; "" disables the main-owner gate). A probe error
// is returned unclassified alongside RemovalOK — callers check err before
// the verdict.
func ClassifyWorktreeRemoval(g Git, ownerPath, mainPath string, probes RemovalProbes) (WorktreeRemovalVerdict, error) {
	if ownerPath == mainPath && mainPath != "" {
		return RemovalCheckedOutMain, nil
	}
	// Removing the worktree the caller is standing in would delete this
	// process's own cwd; the probe is fail-closed so a lookup error
	// surfaces rather than guessing "not inside".
	if within, err := CwdWithinWorktree(g, ownerPath); err != nil {
		return RemovalOK, err
	} else if within {
		return RemovalInsideCaller, nil
	}
	if probes.Locked {
		return RemovalLocked, nil
	}
	if probes.Stale {
		return RemovalStale, nil
	}
	if probes.Paused != nil {
		paused, err := probes.Paused(ownerPath)
		if err != nil {
			return RemovalOK, err
		}
		if paused {
			return RemovalPausedRebase, nil
		}
	}
	if probes.Clean != nil {
		clean, err := probes.Clean(ownerPath)
		if err != nil {
			return RemovalOK, err
		}
		if !clean {
			return RemovalDirty, nil
		}
	}
	return RemovalOK, nil
}
