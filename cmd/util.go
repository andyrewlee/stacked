package cmd

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strconv"
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

// loadStateAndCurrent loads the stack state and the current branch together, the
// common preamble for commands that position relative to where HEAD currently is.
func loadStateAndCurrent() (*stack.State, string, error) {
	s, err := loadState()
	if err != nil {
		return nil, "", err
	}
	cur, err := currentBranch()
	if err != nil {
		return nil, "", err
	}
	return s, cur, nil
}

// gitShell is the production git port used by the stack engine.
var gitShell stack.Git = git.Shell{}

// stackEnv builds the engine environment for s, persisting via s.Save. In JSON
// mode the quiet git port is used so rebase output cannot corrupt the payload.
func stackEnv(s *stack.State, asJSON bool) stack.Env {
	g := gitShell
	if asJSON {
		if _, ok := g.(git.Shell); ok {
			g = git.QuietShell{}
		}
	}
	return stack.Env{Git: g, Save: s.Save}
}

func rejectArgs(command string, args []string) error {
	if len(args) == 0 {
		return nil
	}
	return fmt.Errorf("%s takes no positional arguments, got %q", command, args[0])
}

// usageLine returns a command's "usage: ..." line, derived from its single
// registered Command.Usage so the printed and registered usage cannot drift.
func usageLine(name string) string {
	if c, ok := byName[name]; ok {
		return "usage: " + c.Usage
	}
	return "usage: st " + name
}

// newFlagSet builds a command's flag set: a ContinueOnError set whose Usage prints
// the registry-derived usage line, with the standard --json boolean already wired
// in. The command adds its own flags afterward. This makes "every command speaks
// --json" true by construction and removes the per-command usage-string copy that
// could drift from the registered Usage. (completion has no --json and builds its
// flag set directly; commands that show flag defaults override Usage to add
// fs.PrintDefaults after the derived line.)
func newFlagSet(name string, asJSON *bool) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.Usage = func() { fmt.Fprintln(fs.Output(), usageLine(name)) }
	fs.BoolVar(asJSON, "json", false, "output the result as JSON")
	return fs
}

// parseCount parses a navigation command's arguments with fs and returns its
// optional positional step count (default 1). It is the single implementation
// of the count contract: at most one positional, an integer, at least 1.
func parseCount(fs *flag.FlagSet, args []string, command string) (int, error) {
	if err := parseArgs(fs, args); err != nil {
		return 0, err
	}
	rest := fs.Args()
	if len(rest) > 1 {
		return 0, fmt.Errorf("%s takes at most one step count, got %q", command, rest[1])
	}
	if len(rest) == 0 {
		return 1, nil
	}
	n, err := strconv.Atoi(rest[0])
	if err != nil {
		return 0, fmt.Errorf("invalid step count %q: %w", rest[0], err)
	}
	if n < 1 {
		return 0, fmt.Errorf("step count must be at least 1, got %d", n)
	}
	return n, nil
}

func usageUnlessJSON(fs *flag.FlagSet, args []string) {
	if !jsonRequested(args) {
		fs.Usage()
	}
}

// parseFlagSet parses args with fs while keeping the flag package quiet: the
// dispatcher (renderError) is the single reporter of a parse error, so the flag
// package's own error line and its automatic usage dump are suppressed during
// Parse. This avoids a duplicated error in plain mode and keeps a non-JSON usage
// block out of --json output. Commands that want to show usage call
// fs.Usage()/usageUnlessJSON explicitly after parsing.
func parseFlagSet(fs *flag.FlagSet, args []string) error {
	output := fs.Output()
	usage := fs.Usage
	fs.SetOutput(io.Discard)
	fs.Usage = func() {}
	defer func() {
		fs.SetOutput(output)
		fs.Usage = usage
	}()
	err := fs.Parse(args)
	if errors.Is(err, flag.ErrHelp) && !jsonRequested(args) {
		fs.SetOutput(os.Stdout)
		fs.Usage = usage
		fs.Usage()
	}
	return err
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

// mutateState runs a stack-mutating op under the repo lock with the undo-snapshot
// protocol: it records an undo entry, runs op, and on success persists and
// finalizes the entry (trimming it, or dropping it when op was a no-op); on
// error it cleans up the tentative entry. It does NOT render — callers render
// from the (possibly mutated) state. This is the single implementation of the
// locking/undo/save protocol; mutate is the common case, and commands with a
// custom result shape (sync, repair) call it directly. The op closure may close
// over a Remote, so remote-dependent mutations fit the same protocol.
func mutateState(label string, asJSON bool, op func(stack.Env, *stack.State) error) error {
	s, release, err := lockAndLoad()
	if err != nil {
		return err
	}
	defer release()
	env := stackEnv(s, asJSON)
	if err := s.RecordUndo(env.Git, label); err != nil {
		return err
	}
	undoEntry, _, _ := stack.PeekUndo()
	if err := op(env, s); err != nil {
		if cleanupErr := stack.CleanupUndoOnError(env.Git, s, err); cleanupErr != nil {
			return stack.AlsoFailed(err, "clean up undo entry", cleanupErr)
		}
		return err
	}
	if err := s.Save(); err != nil {
		return fmt.Errorf("saving stack state: %w", err)
	}
	if err := stack.FinalizeUndo(env.Git, s, undoEntry); err != nil {
		return fmt.Errorf("finalizing undo entry: %w", err)
	}
	return nil
}

// mutate runs an engine operation that returns an OpResult through mutateState
// and renders the result, so each command stays a thin adapter.
func mutate(label string, asJSON bool, op func(stack.Env, *stack.State) (*stack.OpResult, error)) error {
	var res *stack.OpResult
	if err := mutateState(label, asJSON, func(env stack.Env, s *stack.State) error {
		r, err := op(env, s)
		res = r
		return err
	}); err != nil {
		return err
	}
	return renderResult(res, asJSON)
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
		if len(a) > 1 && a[0] == '-' && !looksNumeric(a) {
			flags = append(flags, a)
			// If this flag expects a separate value (not boolean, no "="),
			// pull the next token along as its value.
			if !strings.Contains(a, "=") && !isBoolFlag(fs, a) {
				if i+1 >= len(args) {
					return parseFlagSet(fs, flags)
				}
				i++
				flags = append(flags, args[i])
			}
			continue
		}
		positional = append(positional, a)
	}
	// A positional that looks like a flag (the negative count "-3" routed here by
	// looksNumeric) must be shielded from fs.Parse, which would otherwise reject
	// it as an unknown flag. A leading "--" terminates flag parsing; skip adding
	// one when the caller already supplied it (the explicit-terminator case).
	combined := append([]string(nil), flags...)
	if len(positional) > 0 && positional[0] != "--" {
		combined = append(combined, "--")
	}
	combined = append(combined, positional...)
	return parseFlagSet(fs, combined)
}

// looksNumeric reports whether the token is an integer (optionally negative).
// A negative number like "-3" is then treated as a positional argument rather
// than an unknown flag, so a navigation command's parseCount can report its own
// "step count must be at least 1" message instead of the stdlib's opaque "flag
// provided but not defined: -3".
func looksNumeric(s string) bool {
	_, err := strconv.Atoi(s)
	return err == nil
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
