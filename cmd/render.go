package cmd

// Output rendering shared by every command: JSON emission, OpResult text
// rendering, and the navigation-result emitter.

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"stacked/internal/stack"
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
	restacked, deleted := "restacked", "deleted"
	if res.DryRun {
		restacked, deleted = "would restack", "would delete"
	}
	out("%s\n", sanitizeForTerminal(res.Summary))
	if len(res.Restacked) > 0 {
		out("%s: %s\n", restacked, joinTerminalNames(res.Restacked))
	}
	if len(res.Deleted) > 0 {
		out("%s: %s\n", deleted, joinTerminalNames(res.Deleted))
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

// joinNames renders a list of branch names for display.
func joinNames(names []string) string {
	return strings.Join(names, ", ")
}

// navEmit renders the result of a navigation command (the branch HEAD ended on
// plus a human summary) as JSON or text.
func navEmit(asJSON bool, branch, summary string) error {
	return navEmitText(asJSON, branch, summary, sanitizeForTerminal(summary))
}
