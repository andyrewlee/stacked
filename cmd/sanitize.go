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
	if !needsTerminalSanitizing(s) {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); {
		c := s[i]
		if c < utf8.RuneSelf {
			if isTerminalControl(rune(c)) {
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
		if isTerminalControl(r) {
			fmt.Fprintf(&b, "\\x%02x", r)
		} else {
			b.WriteString(s[i : i+size])
		}
		i += size
	}
	return b.String()
}

func needsTerminalSanitizing(s string) bool {
	for i := 0; i < len(s); {
		c := s[i]
		if c < utf8.RuneSelf {
			if isTerminalControl(rune(c)) {
				return true
			}
			i++
			continue
		}
		r, size := utf8.DecodeRuneInString(s[i:])
		if r == utf8.RuneError && size == 1 {
			return true
		}
		if isTerminalControl(r) {
			return true
		}
		i += size
	}
	return false
}

func isTerminalControl(r rune) bool {
	return r < 0x20 || r == 0x7f || (r >= 0x80 && r <= 0x9f)
}
