package cmd

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"

	"stacked/internal/git"
	"stacked/internal/stack"
)

// loadState loads the persisted stack state. If stacked has not been initialized
// in this repo, the underlying stack.ErrNotInitialized is returned unchanged so
// that callers can print it directly.
func loadState() (*stack.State, error) {
	s, err := stack.Load()
	if err != nil {
		if errors.Is(err, stack.ErrNotInitialized) {
			return nil, err
		}
		return nil, fmt.Errorf("loading stack state: %w", err)
	}
	return s, nil
}

// gitShell is the production git port used by the stack engine.
var gitShell stack.Git = git.Shell{}

// stackEnv builds the engine environment for s, persisting via s.Save.
func stackEnv(s *stack.State) stack.Env {
	return stack.Env{Git: gitShell, Save: s.Save}
}

func rejectArgs(command string, args []string) error {
	if len(args) == 0 {
		return nil
	}
	return fmt.Errorf("%s takes no positional arguments, got %q", command, args[0])
}

// acquireLock takes the repository stack lock; the caller must defer the
// returned release function. It serializes mutating commands across concurrent
// st processes (a no-op on platforms without flock).
func acquireLock() (func(), error) {
	return stack.Lock()
}

// lockAndLoad acquires the stack lock and then loads the state, so a mutating
// command holds the lock across its whole read-modify-write. The caller must
// defer the returned release function. stack.ErrNotInitialized is returned
// unchanged for callers to print.
func lockAndLoad() (*stack.State, func(), error) {
	release, err := acquireLock()
	if err != nil {
		return nil, nil, err
	}
	s, err := loadState()
	if err != nil {
		release()
		return nil, nil, err
	}
	return s, release, nil
}

// mutate runs a stack-mutating engine operation under the repo lock with an undo
// snapshot, then persists and renders the result. The op mutates s and returns
// an OpResult; locking, undo, save, and rendering are handled here so each
// command stays a thin adapter.
func mutate(label string, asJSON bool, op func(stack.Env, *stack.State) (*stack.OpResult, error)) error {
	s, release, err := lockAndLoad()
	if err != nil {
		return err
	}
	defer release()
	if err := s.RecordUndo(label); err != nil {
		return err
	}
	undoEntry, _, _ := stack.PeekUndo()
	res, err := op(stackEnv(s), s)
	if err != nil {
		if cleanupErr := cleanupNoopUndoOnError(s, err); cleanupErr != nil {
			return fmt.Errorf("%w; additionally failed to clean up undo entry: %v", err, cleanupErr)
		}
		return err
	}
	if err := s.Save(); err != nil {
		return fmt.Errorf("saving stack state: %w", err)
	}
	if err := finalizeUndoOnSuccess(s, undoEntry); err != nil {
		return fmt.Errorf("finalizing undo entry: %w", err)
	}
	return renderResult(res, asJSON)
}

func dropNoopUndo(s *stack.State, entry *stack.UndoEntry, opErr error) error {
	if entry == nil || errors.Is(opErr, stack.ErrConflict) {
		return nil
	}
	unchanged, err := sameState(s, entry.State)
	if err != nil || !unchanged {
		return err
	}
	for name, want := range entry.Refs {
		got, err := git.RevParse("refs/heads/" + name)
		if err != nil || got != want {
			return nil
		}
	}
	return stack.DropUndo()
}

func finalizeUndoOnSuccess(s *stack.State, entry *stack.UndoEntry) error {
	if entry == nil {
		return stack.TrimUndo()
	}
	unchanged, err := sameState(s, entry.State)
	if err != nil {
		return err
	}
	if !unchanged {
		return stack.TrimUndo()
	}
	if refsUnchanged(entry) {
		return stack.DropUndo()
	}
	return stack.TrimUndo()
}

func cleanupNoopUndoOnError(s *stack.State, opErr error) error {
	entry, _, _ := stack.PeekUndo()
	if err := dropNoopUndo(s, entry, opErr); err != nil {
		return err
	}
	return stack.TrimUndo()
}

func refsUnchanged(entry *stack.UndoEntry) bool {
	for name, want := range entry.Refs {
		got, err := git.RevParse("refs/heads/" + name)
		if err != nil || got != want {
			return false
		}
	}
	return true
}

func sameState(s *stack.State, raw []byte) (bool, error) {
	var prev stack.State
	if err := json.Unmarshal(raw, &prev); err != nil {
		return false, err
	}
	if s.Trunk != prev.Trunk {
		return false, nil
	}
	if (s.PendingReparent == nil) != (prev.PendingReparent == nil) {
		return false, nil
	}
	if s.PendingReparent != nil && *s.PendingReparent != *prev.PendingReparent {
		return false, nil
	}
	if len(s.Branches) != len(prev.Branches) {
		return false, nil
	}
	for name, got := range s.Branches {
		want, ok := prev.Branches[name]
		if !ok || got == nil || want == nil || *got != *want {
			return false, nil
		}
	}
	return true, nil
}

// emit renders a command result as indented JSON when asJSON, otherwise runs
// textFn for human-readable output. It is the single rendering path for the
// read/operational commands, mirroring what mutate() does for mutations.
func emit(asJSON bool, v any, textFn func()) error {
	if asJSON {
		data, err := json.MarshalIndent(v, "", "  ")
		if err != nil {
			return err
		}
		out("%s\n", data)
		return nil
	}
	textFn()
	return nil
}

// renderResult prints an OpResult as text (default) or JSON.
func renderResult(res *stack.OpResult, asJSON bool) error {
	if res == nil {
		return nil
	}
	if asJSON {
		data, err := json.MarshalIndent(res, "", "  ")
		if err != nil {
			return err
		}
		out("%s\n", data)
		return nil
	}
	restacked, deleted := "restacked", "deleted"
	if res.DryRun {
		restacked, deleted = "would restack", "would delete"
	}
	out("%s\n", res.Summary)
	if len(res.Restacked) > 0 {
		out("%s: %s\n", restacked, joinNames(res.Restacked))
	}
	if len(res.Deleted) > 0 {
		out("%s: %s\n", deleted, joinNames(res.Deleted))
	}
	for _, n := range res.Notes {
		out("%s\n", n)
	}
	return nil
}

// currentBranch returns the name of the currently checked-out branch.
func currentBranch() (string, error) {
	return git.CurrentBranch()
}

// out writes formatted output to stdout. It adds no trailing newline unless one
// is present in the format string.
func out(format string, a ...any) {
	fmt.Fprintf(os.Stdout, format, a...)
}

// joinNames renders a list of branch names for display.
func joinNames(names []string) string {
	return strings.Join(names, ", ")
}

// navEmit renders the result of a navigation command (the branch HEAD ended on
// plus a human summary) as JSON or text.
func navEmit(asJSON bool, branch, summary string) error {
	return emit(asJSON, struct {
		Branch  string `json:"branch"`
		Summary string `json:"summary"`
	}{branch, summary}, func() { out("%s\n", summary) })
}

// parseArgs parses args with fs after moving flag arguments ahead of positional
// ones. The standard library "flag" package stops parsing at the first non-flag
// argument, so without this a flag placed after a positional name (e.g. "st
// create feat -m msg") would be left unparsed. parseArgs reshuffles the
// arguments so such flags are still recognized, then calls fs.Parse.
//
// A flag that takes a value in the "-flag value" form (i.e. not a boolean flag
// and not written as "-flag=value") consumes the following token as its value,
// so the two are kept together. A bare "--" terminates flag parsing: it and
// everything after it is treated as positional, in order.
func parseArgs(fs *flag.FlagSet, args []string) error {
	var flags, positional []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			positional = append(positional, args[i:]...)
			break
		}
		if len(a) > 1 && a[0] == '-' {
			flags = append(flags, a)
			// If this flag expects a separate value (not boolean, no "="),
			// pull the next token along as its value.
			if !strings.Contains(a, "=") && !isBoolFlag(fs, a) && i+1 < len(args) {
				i++
				flags = append(flags, args[i])
			}
			continue
		}
		positional = append(positional, a)
	}
	return fs.Parse(append(flags, positional...))
}

// isBoolFlag reports whether the flag named by the argument token (e.g. "-a" or
// "--all") is a registered boolean flag on fs.
func isBoolFlag(fs *flag.FlagSet, token string) bool {
	name := strings.TrimLeft(token, "-")
	f := fs.Lookup(name)
	if f == nil {
		return false
	}
	bf, ok := f.Value.(interface{ IsBoolFlag() bool })
	return ok && bf.IsBoolFlag()
}
