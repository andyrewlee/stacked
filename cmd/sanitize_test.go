package cmd

import "testing"

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
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := sanitizeForTerminal(tt.in); got != tt.want {
				t.Fatalf("sanitizeForTerminal(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}
