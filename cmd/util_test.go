package cmd

import (
	"flag"
	"reflect"
	"testing"
)

// TestParseArgs exercises the flag-after-positional reshuffling that parseArgs
// does on top of the stdlib flag package (which otherwise stops parsing at the
// first non-flag argument).
func TestParseArgs(t *testing.T) {
	type result struct {
		message string
		all     bool
		commit  bool
	}

	// newFS builds a flag set mirroring the create/modify commands: a value flag
	// (-m/--message), boolean flags (-a/--all), and a standalone bool (--commit).
	newFS := func(r *result) *flag.FlagSet {
		fs := flag.NewFlagSet("test", flag.ContinueOnError)
		fs.StringVar(&r.message, "m", "", "")
		fs.StringVar(&r.message, "message", "", "")
		fs.BoolVar(&r.all, "a", false, "")
		fs.BoolVar(&r.all, "all", false, "")
		fs.BoolVar(&r.commit, "commit", false, "")
		return fs
	}

	tests := []struct {
		name     string
		in       []string
		want     result
		wantArgs []string
	}{
		{"flag before positional", []string{"-m", "msg", "feat"}, result{message: "msg"}, []string{"feat"}},
		{"value flag after positional", []string{"feat", "-m", "msg"}, result{message: "msg"}, []string{"feat"}},
		{"long value flag after positional", []string{"feat", "--message", "msg"}, result{message: "msg"}, []string{"feat"}},
		{"bool flag after positional", []string{"feat", "-a"}, result{all: true}, []string{"feat"}},
		{"bool flag before positional", []string{"-a", "feat"}, result{all: true}, []string{"feat"}},
		{"equals form keeps value", []string{"feat", "-m=msg"}, result{message: "msg"}, []string{"feat"}},
		{"value flag does not swallow positional", []string{"feat", "-m", "msg", "bar"}, result{message: "msg"}, []string{"feat", "bar"}},
		{"double dash escapes a flag-like positional", []string{"--", "-m"}, result{}, []string{"-m"}},
		{"only bool flags", []string{"-a", "--commit"}, result{all: true, commit: true}, nil},
		{"no args", nil, result{}, nil},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var r result
			fs := newFS(&r)
			if err := parseArgs(fs, tt.in); err != nil {
				t.Fatalf("parseArgs(%q) error: %v", tt.in, err)
			}
			if r != tt.want {
				t.Errorf("flags = %+v, want %+v", r, tt.want)
			}
			got := fs.Args()
			if len(got) == 0 {
				got = nil
			}
			if !reflect.DeepEqual(got, tt.wantArgs) {
				t.Errorf("positional args = %q, want %q", got, tt.wantArgs)
			}
		})
	}
}
