package cmd

import (
	"flag"
	"reflect"
	"strings"
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
		wantErr  string
	}{
		{"flag before positional", []string{"-m", "msg", "feat"}, result{message: "msg"}, []string{"feat"}, ""},
		{"value flag after positional", []string{"feat", "-m", "msg"}, result{message: "msg"}, []string{"feat"}, ""},
		{"long value flag after positional", []string{"feat", "--message", "msg"}, result{message: "msg"}, []string{"feat"}, ""},
		{"bool flag after positional", []string{"feat", "-a"}, result{all: true}, []string{"feat"}, ""},
		{"bool flag before positional", []string{"-a", "feat"}, result{all: true}, []string{"feat"}, ""},
		{"equals form keeps value", []string{"feat", "-m=msg"}, result{message: "msg"}, []string{"feat"}, ""},
		{"value flag does not swallow positional", []string{"feat", "-m", "msg", "bar"}, result{message: "msg"}, []string{"feat", "bar"}, ""},
		{"double dash escapes a flag-like positional", []string{"--", "-m"}, result{}, []string{"-m"}, ""},
		{"trailing terminator is consumed", []string{"feat", "--"}, result{}, []string{"feat"}, ""},
		{"terminator after positional escapes later flag", []string{"feat", "--", "-m"}, result{}, []string{"feat", "-m"}, ""},
		{"terminator only", []string{"--"}, result{}, nil, ""},
		{"flag then terminator then flag-like positional", []string{"-a", "--", "-m"}, result{all: true}, []string{"-m"}, ""},
		{"only bool flags", []string{"-a", "--commit"}, result{all: true, commit: true}, nil, ""},
		{"negative number is positional", []string{"-3"}, result{}, []string{"-3"}, ""},
		{"missing value flag after positional", []string{"feat", "-m"}, result{}, nil, "flag needs an argument"},
		{"no args", nil, result{}, nil, ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var r result
			fs := newFS(&r)
			err := parseArgs(fs, tt.in)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("parseArgs(%q) error = %v, want containing %q", tt.in, err, tt.wantErr)
				}
				return
			}
			if err != nil {
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

// TestParseCount pins the shared step-count contract used by the navigation
// commands: optional, at most one, integer, at least 1.
func TestParseCount(t *testing.T) {
	tests := []struct {
		name    string
		in      []string
		want    int
		wantErr string
	}{
		{"default", nil, 1, ""},
		{"explicit count", []string{"3"}, 3, ""},
		{"count with json flag", []string{"2", "--json"}, 2, ""},
		{"too many positionals", []string{"1", "2"}, 0, "takes at most one step count"},
		{"not a number", []string{"abc"}, 0, `invalid step count "abc"`},
		{"zero", []string{"0"}, 0, "step count must be at least 1"},
		// A negative count is now read as a positional (see parseArgs), so it
		// reaches the friendly range check instead of the stdlib's flag error.
		{"negative", []string{"-2"}, 0, "step count must be at least 1, got -2"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fs := flag.NewFlagSet("nav", flag.ContinueOnError)
			var asJSON bool
			fs.BoolVar(&asJSON, "json", false, "")
			got, err := parseCount(fs, tt.in, "nav")
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("parseCount(%q) error = %v, want containing %q", tt.in, err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseCount(%q): %v", tt.in, err)
			}
			if got != tt.want {
				t.Fatalf("parseCount(%q) = %d, want %d", tt.in, got, tt.want)
			}
		})
	}
}

// TestParseBuiltinArgs pins the one scanner behind help and version: --json
// forms, unknown-flag rejection, and topic handling per allowTopic.
func TestParseBuiltinArgs(t *testing.T) {
	tests := []struct {
		name       string
		command    string
		in         []string
		allowTopic bool
		wantTopic  string
		wantJSON   bool
		wantErr    string
	}{
		{"no args", "help", nil, true, "", false, ""},
		{"json flag", "version", []string{"--json"}, false, "", true, ""},
		{"json equals form", "version", []string{"--json=true"}, false, "", true, ""},
		{"json false", "help", []string{"--json=false"}, true, "", false, ""},
		{"bad json value", "help", []string{"--json=banana"}, true, "", false, "invalid --json value"},
		{"topic allowed", "help", []string{"create"}, true, "create", false, ""},
		{"topic with json", "help", []string{"create", "--json"}, true, "create", true, ""},
		{"second topic rejected", "help", []string{"a", "b"}, true, "", false, "help takes at most one command"},
		{"topic not allowed", "version", []string{"extra"}, false, "", false, "version takes no positional arguments"},
		{"unknown flag", "help", []string{"--frob"}, true, "", false, `unknown help flag "--frob"`},
		{"unknown flag named", "version", []string{"--frob"}, false, "", false, `unknown version flag "--frob"`},
		{"terminator makes flag positional", "help", []string{"--", "--json"}, true, "--json", false, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			topic, asJSON, err := parseBuiltinArgs(tt.command, tt.in, tt.allowTopic)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("parseBuiltinArgs(%q) error = %v, want containing %q", tt.in, err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseBuiltinArgs(%q): %v", tt.in, err)
			}
			if topic != tt.wantTopic || asJSON != tt.wantJSON {
				t.Fatalf("parseBuiltinArgs(%q) = (%q, %v), want (%q, %v)", tt.in, topic, asJSON, tt.wantTopic, tt.wantJSON)
			}
		})
	}
}
