package cmd

import (
	"fmt"
	"strings"
)

// sanitizeForTerminal makes git- or state-derived text safe to print to a
// terminal by replacing control characters with visible escaped bytes. Raw
// ESC/OSC bytes from commit subjects, state.json, or worktree paths could
// otherwise inject terminal escape sequences into st's human output.
func sanitizeForTerminal(s string) string {
	if !strings.ContainsFunc(s, isTerminalControl) {
		return s
	}
	var b strings.Builder
	for _, r := range s {
		if isTerminalControl(r) {
			fmt.Fprintf(&b, "\\x%02x", r)
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

func isTerminalControl(r rune) bool {
	return r < 0x20 || r == 0x7f || (r >= 0x80 && r <= 0x9f)
}
