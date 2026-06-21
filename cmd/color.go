package cmd

import "os"

// colorEnabled reports whether ANSI color should be emitted. It is true only
// when stdout is a terminal and NO_COLOR is unset, so piped/redirected output
// (including tests) stays plain.
var colorEnabled = detectColor()

func detectColor() bool {
	if os.Getenv("NO_COLOR") != "" {
		return false
	}
	info, err := os.Stdout.Stat()
	if err != nil {
		return false
	}
	return info.Mode()&os.ModeCharDevice != 0
}

const (
	ansiReset  = "\x1b[0m"
	ansiBold   = "\x1b[1m"
	ansiDim    = "\x1b[2m"
	ansiGreen  = "\x1b[32m"
	ansiYellow = "\x1b[33m"
	ansiCyan   = "\x1b[36m"
)

// paint wraps s in the given ANSI codes when color is enabled.
func paint(s string, codes ...string) string {
	if !colorEnabled || len(codes) == 0 {
		return s
	}
	prefix := ""
	for _, c := range codes {
		prefix += c
	}
	return prefix + s + ansiReset
}
