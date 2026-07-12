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
