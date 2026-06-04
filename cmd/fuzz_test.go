package cmd

import (
	"flag"
	"strings"
	"testing"
)

// FuzzParseArgs fuzzes parseArgs against a flag set that mirrors the
// create/modify commands (a value flag -m/--message and the boolean flags
// -a/--all/--commit). It asserts that parseArgs never panics and that it
// conserves non-flag tokens, checked against a reference scan computed
// independently of the flag package:
//
//   - No invention: every token parseArgs reports as positional came from the
//     original arguments, and never more times than it appeared there.
//   - Bounded loss: it never silently drops unambiguous positionals beyond what
//     the value flags present can absorb. parseArgs reshuffles flags ahead of
//     positionals, so a trailing value flag like "-m" can legitimately swallow a
//     positional as its value after the move; the number of such absorptions is
//     bounded by the number of value-flag occurrences, so we allow exactly that
//     many lost positionals and no more.
//
// Both invariants are robust to the stdlib flag quirks around lone "-", "--" and
// "---" tokens (which are flag-like and therefore excluded from the
// unambiguous-positional set, while still permitted to pass through).
func FuzzParseArgs(f *testing.F) {
	f.Add("")
	f.Add("feat")
	f.Add("-m msg feat")
	f.Add("feat -m msg")
	f.Add("feat --message msg bar")
	f.Add("-a --commit")
	f.Add("feat -a")
	f.Add("-m=msg feat")
	f.Add("-- -m feat")
	f.Add("--all feat extra -m hello")
	f.Add("- -- ---")
	f.Add("-unknown value feat")

	f.Fuzz(func(t *testing.T, s string) {
		// Split on spaces; drop empty tokens so we don't fabricate args the
		// shell never would (consecutive spaces). The flag package treats an
		// empty string as a positional, but it can never appear on a real
		// command line, so excluding it keeps the reference honest.
		var args []string
		for _, tok := range strings.Split(s, " ") {
			if tok != "" {
				args = append(args, tok)
			}
		}

		newFS := func() *flag.FlagSet {
			var message string
			var all, commit bool
			fs := flag.NewFlagSet("fuzz", flag.ContinueOnError)
			fs.SetOutput(discard{})
			fs.StringVar(&message, "m", "", "")
			fs.StringVar(&message, "message", "", "")
			fs.BoolVar(&all, "a", false, "")
			fs.BoolVar(&all, "all", false, "")
			fs.BoolVar(&commit, "commit", false, "")
			return fs
		}

		// Reference: the unambiguous positional tokens, computed directly from
		// args without invoking parseArgs. We mirror only parseArgs's
		// value-flag-swallows-next-token rule; tokens that look like flags
		// (including "-", "--", "---") are intentionally NOT counted here.
		wantClear := referencePositional(newFS(), args)

		fs := newFS()
		// parseArgs may legitimately return an error (e.g. an unknown flag or a
		// value flag with no value). We only require that it does not panic.
		if err := parseArgs(fs, args); err != nil {
			return
		}
		got := fs.Args()

		gotCount := counts(got)
		argCount := counts(args)

		// Invariant 1 (no invention): every token parseArgs reports as
		// positional must have come from the original argument list, and never
		// more times than it appeared there.
		for tok, n := range gotCount {
			if n > argCount[tok] {
				t.Fatalf("parseArgs invented/duplicated token %q for %q: got %d, input had %d\n  got %q", tok, args, n, argCount[tok], got)
			}
		}

		// Invariant 2 (bounded loss): unambiguous positionals may only go
		// missing when a value flag absorbs one as its argument after parseArgs
		// moves flags ahead of positionals. Count the total positionals dropped
		// and require it not exceed the number of value-flag occurrences.
		valueFlags := countValueFlags(newFS(), args)
		var dropped int
		for tok, n := range counts(wantClear) {
			if miss := n - gotCount[tok]; miss > 0 {
				dropped += miss
			}
		}
		if dropped > valueFlags {
			t.Fatalf("parseArgs dropped %d positional(s) for %q but only %d value flag(s) could absorb them\n  want(clear) %q\n  got %q", dropped, args, valueFlags, wantClear, got)
		}
	})
}

// countValueFlags returns how many tokens in args are value-flag occurrences in
// the "-flag value" form (a registered non-boolean flag, written without "=").
// Each such occurrence can absorb at most one positional once parseArgs moves
// flags ahead of positionals, so this bounds the legitimate positional loss.
func countValueFlags(fs *flag.FlagSet, args []string) int {
	var n int
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			break
		}
		if len(a) > 1 && a[0] == '-' {
			if !strings.Contains(a, "=") && !isBoolFlag(fs, a) && fs.Lookup(strings.TrimLeft(a, "-")) != nil {
				n++
				if i+1 < len(args) {
					i++ // its inline value, when present, is not itself a flag
				}
			}
		}
	}
	return n
}

// referencePositional reproduces parseArgs's notion of which tokens are
// UNAMBIGUOUS positionals, computed directly from args without invoking
// parseArgs. A value flag in "-flag value" form (not boolean, no "=") swallows
// the following token, so that token is not counted; a bare "--" makes the
// remainder positional; flag-like tokens are skipped. Lone "-" is treated as a
// positional (it does not satisfy the len>1 flag test), matching parseArgs.
func referencePositional(fs *flag.FlagSet, args []string) []string {
	var positional []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			positional = append(positional, args[i+1:]...)
			break
		}
		if len(a) > 1 && a[0] == '-' {
			if !strings.Contains(a, "=") && !isBoolFlag(fs, a) && i+1 < len(args) {
				i++ // consumed as this flag's value
			}
			continue
		}
		positional = append(positional, a)
	}
	return positional
}

// counts returns the multiset of tokens in s.
func counts(s []string) map[string]int {
	m := make(map[string]int, len(s))
	for _, tok := range s {
		m[tok]++
	}
	return m
}

// discard is an io.Writer that drops flag parsing's error output so the fuzz
// run stays quiet.
type discard struct{}

func (discard) Write(p []byte) (int, error) { return len(p), nil }

// FuzzRemoteToHTTPS fuzzes remoteToHTTPS with arbitrary input strings. It asserts
// the function never panics and that any non-empty web URL it returns always
// starts with "http".
func FuzzRemoteToHTTPS(f *testing.F) {
	f.Add("git@example.com:owner/repo.git")
	f.Add("ssh://git@example.com/owner/repo.git")
	f.Add("https://example.com/owner/repo.git")
	f.Add("http://host.tld/g/p.git")
	f.Add("git@host.tld:group/sub/proj.git")
	f.Add("origin")
	f.Add("")
	f.Add("git@:")
	f.Add("ssh://git@/")
	f.Add("https://")
	f.Add("file:///tmp/repo")
	f.Add("git@host:")

	f.Fuzz(func(t *testing.T, raw string) {
		webURL, host := remoteToHTTPS(raw)
		if webURL != "" && !strings.HasPrefix(webURL, "http") {
			t.Fatalf("remoteToHTTPS(%q) returned non-empty webURL %q that does not start with http", raw, webURL)
		}
		_ = host // informational second return; exercised for coverage
	})
}
