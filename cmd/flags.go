package cmd

// Flag and argument parsing shared by every command: the registry-derived
// usage line, the standard --json flag set, quiet parse-error reporting, and
// the flags-before-positionals reshuffle.

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
)

func rejectArgs(command string, args []string) error {
	if len(args) == 0 {
		return nil
	}
	return fmt.Errorf("%s takes no positional arguments, got %q", command, args[0])
}

// parsePlain parses the arguments of a command that takes no positionals and
// only the standard --json flag: it builds the flag set, parses it, and rejects
// any stray positional, returning whether --json was requested. It is the shared
// preamble of the read/no-arg commands (abort, bottom, continue, fold, guide,
// log, repair, status, top, undo, validate), so that contract lives in one place.
func parsePlain(command string, args []string) (bool, error) {
	var asJSON bool
	fs := newFlagSet(command, &asJSON)
	if err := parseFlagSet(fs, args); err != nil {
		return false, err
	}
	if err := rejectArgs(command, fs.Args()); err != nil {
		return false, err
	}
	return asJSON, nil
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
	if errors.Is(err, flag.ErrHelp) {
		if !jsonRequested(args) {
			fs.SetOutput(os.Stdout)
			fs.Usage = usage
			fs.Usage()
		}
		return err
	}
	if err != nil && fs.Name() != "" {
		// An unknown/malformed flag otherwise dead-ends on the raw stdlib message;
		// point at the command's help, like unknownCommandErr does for commands.
		// %w preserves the sentinel chain and the "provided but not defined" text.
		return fmt.Errorf("%w (run \"st help %s\" for usage)", err, fs.Name())
	}
	return err
}

// parseArgs parses args with fs after moving flag arguments ahead of positional
// ones. The standard library "flag" package stops parsing at the first non-flag
// argument, so without this a flag placed after a positional name (e.g. "st
// create feat -m msg") would be left unparsed. parseArgs reshuffles the
// arguments so such flags are still recognized, then calls fs.Parse.
//
// A flag that takes a value in the "-flag value" form (i.e. not a boolean flag
// and not written as "-flag=value") consumes the following token as its value,
// so the two are kept together. A bare "--" terminates flag parsing:
// everything after it is treated as positional, in order.
func parseArgs(fs *flag.FlagSet, args []string) error {
	var flags, positional []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			positional = append(positional, args[i+1:]...)
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
	// it as an unknown flag.
	combined := append([]string(nil), flags...)
	if len(positional) > 0 {
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
