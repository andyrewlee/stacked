package cmd

import (
	"strings"
	"testing"
)

// TestPrintHelpListsEveryCommand derives the expectation from the live registry,
// so a newly registered command is covered automatically instead of drifting
// from a hand-maintained list (TEST-8).
func TestPrintHelpListsEveryCommand(t *testing.T) {
	out := captureStdout(t, func() { printHelp(false) })
	for _, c := range registry {
		if !strings.Contains(out, c.Name) {
			t.Errorf("help output is missing registered command %q", c.Name)
		}
	}
	for _, pseudo := range []string{"help", "version"} {
		if !strings.Contains(out, pseudo) {
			t.Errorf("help output is missing pseudo-command %q", pseudo)
		}
	}
}

// TestHelpForCommandText covers the text path of per-command help (TEST-9): a
// known command prints summary + usage (+ aliases); an unknown one goes through
// the error path with exit 1.
func TestHelpForCommandText(t *testing.T) {
	var code int
	out := captureStdout(t, func() {
		withArgs(t, []string{"help", "status"}, func() { code = Execute() })
	})
	if code != 0 {
		t.Fatalf("help status = %d, want 0", code)
	}
	for _, want := range []string{"status", "usage:", "stat"} { // stat is an alias
		if !strings.Contains(out, want) {
			t.Errorf("help status missing %q:\n%s", want, out)
		}
	}

	code = 0
	var stdout string
	errOut := executeCapturingOutput(t, []string{"help", "no-such-cmd"}, &code, &stdout)
	if code != 1 {
		t.Fatalf("help no-such-cmd = %d, want 1", code)
	}
	if strings.TrimSpace(stdout) != "" {
		t.Fatalf("help unknown wrote stdout:\n%s", stdout)
	}
	if !strings.Contains(errOut, "no-such-cmd") {
		t.Errorf("help unknown error missing the name:\n%s", errOut)
	}
}

// TestPaint covers the color helper in both states (the paint path was 0%).
func TestPaint(t *testing.T) {
	defer func(prev bool) { colorEnabled = prev }(colorEnabled)

	colorEnabled = false
	if got := paint("x", ansiGreen); got != "x" {
		t.Errorf("paint disabled = %q, want plain x", got)
	}

	colorEnabled = true
	if got := paint("x", ansiGreen); got != ansiGreen+"x"+ansiReset {
		t.Errorf("paint enabled = %q, want wrapped", got)
	}
	if got := paint("x"); got != "x" {
		t.Errorf("paint with no codes = %q, want plain x", got)
	}
}
