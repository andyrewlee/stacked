package cmd

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

// sanitizeForTerminal makes git- or state-derived text safe to print to a
// terminal by replacing control characters with visible escaped bytes. Raw
// ESC/OSC bytes from commit subjects, state.json, or worktree paths could
// otherwise inject terminal escape sequences into st's human output.
func sanitizeForTerminal(s string) string {
	return sanitizeControls(s, isTerminalControl)
}

// sanitizeErrorForTerminal escapes terminal control bytes in a (possibly
// multi-line) error message. Unlike sanitizeForTerminal it preserves \n and
// \t: wrapped git stderr legitimately spans lines, and escaping its newlines
// would make long errors unreadable. All other C0, DEL, and C1 bytes are
// escaped.
func sanitizeErrorForTerminal(s string) string {
	return sanitizeControls(s, isErrorTerminalControl)
}

// sanitizeControls walks s once, escaping every rune (or invalid byte) the
// isControl predicate flags; it is allocation-free when s is already clean.
func sanitizeControls(s string, isControl func(rune) bool) string {
	if !needsSanitizing(s, isControl) {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); {
		c := s[i]
		if c < utf8.RuneSelf {
			if isControl(rune(c)) {
				fmt.Fprintf(&b, "\\x%02x", c)
			} else {
				b.WriteByte(c)
			}
			i++
			continue
		}
		r, size := utf8.DecodeRuneInString(s[i:])
		if r == utf8.RuneError && size == 1 {
			fmt.Fprintf(&b, "\\x%02x", c)
			i++
			continue
		}
		if isControl(r) {
			fmt.Fprintf(&b, "\\x%02x", r)
		} else {
			b.WriteString(s[i : i+size])
		}
		i += size
	}
	return b.String()
}

func needsSanitizing(s string, isControl func(rune) bool) bool {
	for i := 0; i < len(s); {
		c := s[i]
		if c < utf8.RuneSelf {
			if isControl(rune(c)) {
				return true
			}
			i++
			continue
		}
		r, size := utf8.DecodeRuneInString(s[i:])
		if r == utf8.RuneError && size == 1 {
			return true
		}
		if isControl(r) {
			return true
		}
		i += size
	}
	return false
}

func isTerminalControl(r rune) bool {
	return r < 0x20 || r == 0x7f || (r >= 0x80 && r <= 0x9f)
}

// isErrorTerminalControl is isTerminalControl minus the whitespace that is
// legitimate formatting in multi-line error text.
func isErrorTerminalControl(r rune) bool {
	return isTerminalControl(r) && r != '\n' && r != '\t'
}

// The helpers below are the terminal-facing (sanitized) twins of plain
// summary builders and joiners used in JSON payloads: text output sanitizes,
// JSON stays raw (encoding/json already escapes).

func joinTerminalNames(names []string) string {
	safe := make([]string, 0, len(names))
	for _, name := range names {
		safe = append(safe, sanitizeForTerminal(name))
	}
	return joinNames(safe)
}

func navEmitText(asJSON bool, branch, summary, textSummary string) error {
	return emit(asJSON, struct {
		Branch  string `json:"branch"`
		Summary string `json:"summary"`
	}{branch, summary}, func() { out("%s\n", textSummary) })
}

func terminalSummary(verb, branch, dest string) string {
	if dest == "" {
		return fmt.Sprintf("%s %s", verb, sanitizeForTerminal(branch))
	}
	if shimActive() {
		return fmt.Sprintf("%s %s (worktree: %s)", verb, sanitizeForTerminal(branch), sanitizeForTerminal(dest))
	}
	return teleportHintForTerminal(branch, dest)
}

func teleportHintForTerminal(branch, dest string) string {
	safeBranch := sanitizeForTerminal(branch)
	safeDest := sanitizeForTerminal(dest)
	return fmt.Sprintf("%s is in worktree %s\nrun: cd %s", safeBranch, safeDest, safeDest)
}

func topSummaryForTerminal(branch, dest string) string {
	if dest == "" {
		return fmt.Sprintf("switched to %s (top of stack)", sanitizeForTerminal(branch))
	}
	if shimActive() {
		return fmt.Sprintf("switched to %s (top of stack, worktree: %s)", sanitizeForTerminal(branch), sanitizeForTerminal(dest))
	}
	return teleportHintForTerminal(branch, dest)
}

func alreadyAtSummaryForTerminal(prefix, branch string) string {
	return fmt.Sprintf("%s: %s", prefix, sanitizeForTerminal(branch))
}

func branchPointSummaryForTerminal(branch string) string {
	return fmt.Sprintf("multiple children of %s; pick one and run \"st checkout <name>\":", sanitizeForTerminal(branch))
}
