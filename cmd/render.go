package cmd

// Output rendering shared by every command: JSON emission, OpResult text
// rendering, and the navigation-result emitter.

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"

	"github.com/andyrewlee/stacked/internal/stack"
)

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
	restacked, deleted, tracked := "restacked", "deleted", "tracked"
	if res.DryRun {
		restacked, deleted, tracked = "would restack", "would delete", "would track"
	}
	out("%s\n", sanitizeForTerminal(res.Summary))
	if len(res.Restacked) > 0 {
		out("%s: %s\n", restacked, joinTerminalNames(res.Restacked))
	}
	if len(res.Deleted) > 0 {
		out("%s: %s\n", deleted, joinTerminalNames(res.Deleted))
	}
	if len(res.Tracked) > 0 {
		names := make([]string, 0, len(res.Tracked))
		for n := range res.Tracked {
			names = append(names, n)
		}
		sort.Strings(names)
		pairs := make([]string, 0, len(names))
		for _, n := range names {
			pairs = append(pairs, n+" (parent: "+res.Tracked[n]+")")
		}
		out("%s: %s\n", tracked, joinTerminalNames(pairs))
	}
	for _, n := range res.Notes {
		out("%s\n", sanitizeForTerminal(n))
	}
	return nil
}

// out writes formatted output to stdout. It adds no trailing newline unless one
// is present in the format string.
func out(format string, a ...any) {
	fmt.Fprintf(os.Stdout, format, a...)
}

// navEmit renders the result of a navigation command (the branch HEAD ended on
// plus a human summary) as JSON or text. The text summary is sanitized as a
// whole with the newline-preserving predicate so teleport hints keep their
// structural "\n" (the two-line "… is in worktree …\nrun: cd …") while
// ESC/OSC/C1 bytes in a branch name or worktree path still escape. JSON gets
// the raw summary; encoding/json escapes it.
func navEmit(asJSON bool, branch, summary string) error {
	return navEmitText(asJSON, branch, summary, sanitizeControls(summary, isErrorTerminalControl))
}
