package cmd

import (
	"strings"
	"testing"
)

func TestSanitizeForTerminal(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{name: "plain ascii", in: "hello world", want: "hello world"},
		{name: "utf8", in: "hello héllo→", want: "hello héllo→"},
		{name: "escape", in: "\x1b[31mred", want: "\\x1b[31mred"},
		{name: "osc", in: "\x1b]0;title\x07", want: "\\x1b]0;title\\x07"},
		{name: "del", in: "a\x7fb", want: "a\\x7fb"},
		{name: "c1", in: "a\u009bb", want: "a\\x9bb"},
		{name: "raw c1 byte", in: string([]byte{'a', 0x9b, 'b'}), want: "a\\x9bb"},
		{name: "invalid utf8 byte", in: string([]byte{'a', 0xff, 'b'}), want: "a\\xffb"},
		{name: "newline and tab", in: "a\n\tb", want: "a\\x0a\\x09b"},
		// Cf format chars: bidi overrides and zero-width runes render
		// invisibly or reorder display — escaped like the Cc bytes are.
		{name: "bidi override", in: "a\u202eb", want: "a\\x202eb"},
		{name: "zero-width space", in: "a\u200bb", want: "a\\x200bb"},
		{name: "bom", in: "\ufeffa", want: "\\xfeffa"},
		{name: "word joiner", in: "a\u2060b", want: "a\\x2060b"},
		{name: "tag char", in: "a\U000e0001b", want: "a\\xe0001b"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := sanitizeForTerminal(tt.in); got != tt.want {
				t.Fatalf("sanitizeForTerminal(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestSanitizeErrorForTerminal(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{name: "plain ascii", in: "no such branch", want: "no such branch"},
		{name: "multi-line keeps newline escapes esc", in: "rebase failed:\n\x1b[31mhint\x1b[0m", want: "rebase failed:\n\\x1b[31mhint\\x1b[0m"},
		{name: "tab preserved", in: "a\tb", want: "a\tb"},
		{name: "carriage return escaped", in: "a\rb", want: "a\\x0db"},
		{name: "del and c1 escaped", in: "a\x7f\u009bb", want: "a\\x7f\\x9bb"},
		{name: "invalid utf8 byte", in: string([]byte{'a', 0xff, 'b'}), want: "a\\xffb"},
		// Cf chars are escaped in error text too — a wrapped git stderr
		// carrying a bidi override is the same paste-hijack vector.
		{name: "bidi override in error", in: "no branch a\u202eb", want: "no branch a\\x202eb"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := sanitizeErrorForTerminal(tt.in); got != tt.want {
				t.Fatalf("sanitizeErrorForTerminal(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

// TestTeleportHintControlPath pins the control-byte boundary: a destination
// whose bytes terminal sanitization would escape can no longer be named by the
// escaped spelling, so no executable `run: cd` is offered — only a direction
// to the shell integration or the JSON field. The terminal rendering still
// escapes the control bytes, and the raw summary keeps the exact path bytes.
func TestTeleportHintControlPath(t *testing.T) {
	dest := "/wt/evil\nworktree /fake"

	term := teleportHintForTerminal("feat", dest)
	if strings.Contains(term, "run: cd") {
		t.Errorf("control-byte path must not offer an executable cd: %q", term)
	}
	// The path's newline byte must be visibly escaped (\x0a), not raw — a raw
	// byte would let the embedded text forge a second output line.
	if strings.Contains(term, "\nworktree /fake") {
		t.Errorf("embedded newline leaked raw into terminal display: %q", term)
	}
	if !strings.Contains(term, `evil\x0aworktree /fake`) {
		t.Errorf("escaped path bytes missing from display: %q", term)
	}
	if !strings.Contains(term, "shell integration") {
		t.Errorf("no direction given for control-byte path: %q", term)
	}

	raw := teleportHint("feat", dest)
	if strings.Contains(raw, "run: cd") {
		t.Errorf("raw summary must not offer an executable cd either: %q", raw)
	}
	if !strings.Contains(raw, dest) {
		t.Errorf("raw summary must keep exact path bytes: %q", raw)
	}
}
