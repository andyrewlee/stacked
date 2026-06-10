package cmd

import (
	"io"
	"os"
	"strings"
	"testing"
)

// captureStdout swaps os.Stdout for a pipe while fn runs and returns everything
// fn wrote to stdout. TestMain points os.Stdout at /dev/null for the rest of the
// suite; this helper temporarily restores a real pipe so output-shape tests can
// assert on what a command prints, then puts os.Stdout back.
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	orig := os.Stdout
	os.Stdout = w
	defer func() { os.Stdout = orig }()

	done := make(chan string, 1)
	go func() {
		var b strings.Builder
		_, _ = io.Copy(&b, r)
		done <- b.String()
	}()

	fn()
	_ = w.Close()
	out := <-done
	_ = r.Close()
	return out
}
