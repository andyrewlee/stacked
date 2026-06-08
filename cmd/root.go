// Package cmd implements the stacked command-line dispatcher and all of its
// subcommands. Subcommands self-register via init() functions calling register.
package cmd

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"runtime/debug"
	"strconv"
	"strings"
	"text/tabwriter"

	"stacked/internal/stack"
)

// version is the stacked release version reported by the version command. It is
// overridable at build time via -ldflags "-X stacked/cmd.version=<v>".
var version = "0.1.0"

// Command describes a single stacked subcommand.
type Command struct {
	// Name is the primary invocation name of the command.
	Name string
	// Aliases are alternate names that also invoke the command.
	Aliases []string
	// Summary is a one-line description shown in help output.
	Summary string
	// Usage is a short usage string, e.g. "st create <name> [-m <msg>] [-a]".
	Usage string
	// Run executes the command with the arguments following the command name.
	Run func(args []string) error
}

// registry holds all registered commands in registration order.
var registry []*Command

// byName maps each command name and alias to its Command.
var byName = map[string]*Command{}

// register adds c to the registry and indexes its name and aliases. It is meant
// to be called from subcommand init() functions.
func register(c *Command) {
	registry = append(registry, c)
	byName[c.Name] = c
	for _, alias := range c.Aliases {
		byName[alias] = c
	}
}

// exitInternal is the exit code for a recovered panic (an internal bug). It is
// deliberately outside the semantic range 1-4 so an agent never mistakes a crash
// for a resolvable condition — in particular not for a conflict (exit 2), which
// is the code the Go runtime would otherwise use for an unrecovered panic.
const exitInternal = 70

// Execute parses os.Args, dispatches to the matching subcommand, and returns the
// process exit code. With no arguments, or help/-h/--help, it prints help and
// returns 0. version/-v/--version prints the version and returns 0. An unknown
// command prints an error to stderr and returns 1. A command that returns an
// error prints "st: <err>" to stderr and returns 1. A panic in any command is
// recovered into a structured error and exitInternal so the process never
// crashes with a raw stack trace and the runtime's exit code 2. help and version
// accept --json and emit the same information as a machine-readable payload.
func Execute() (rc int) {
	args := os.Args[1:]

	defer func() {
		if r := recover(); r != nil {
			renderInternalError(r, jsonRequested(args))
			rc = exitInternal
		}
	}()

	if len(args) == 0 {
		printHelp(false)
		return 0
	}

	name := args[0]
	switch name {
	case "help", "-h", "--help":
		rest := args[1:]
		topic, asJSON, err := parseHelpArgs(rest)
		if err != nil {
			renderError(err, jsonRequested(rest))
			return exitCode(err)
		}
		if topic != "" {
			return helpForCommand(topic, asJSON)
		}
		printHelp(asJSON)
		return 0
	case "version", "-v", "--version":
		asJSON, err := parseVersionArgs(args[1:])
		if err != nil {
			renderError(err, jsonRequested(args[1:]))
			return exitCode(err)
		}
		printVersion(asJSON)
		return 0
	}

	cmd, ok := byName[name]
	if !ok {
		// Report through the standard error path so --json gets the structured
		// envelope on stderr and stdout stays clean for machine parsing.
		err := fmt.Errorf("unknown command %q (run \"st help\" for usage)", name)
		renderError(err, jsonRequested(args[1:]))
		return exitCode(err)
	}

	if err := cmd.Run(args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0 // usage was already printed by the command's flag set
		}
		renderError(err, jsonRequested(args[1:]))
		return exitCode(err)
	}
	return 0
}

// commandInfo is the machine-readable description of a command, emitted by the
// help commands under --json.
type commandInfo struct {
	Name    string   `json:"name"`
	Summary string   `json:"summary"`
	Usage   string   `json:"usage"`
	Aliases []string `json:"aliases,omitempty"`
}

func parseHelpArgs(args []string) (topic string, asJSON bool, err error) {
	afterTerminator := false
	for _, a := range args {
		if !afterTerminator && a == "--" {
			afterTerminator = true
			continue
		}
		if !afterTerminator {
			if v, ok, err := parseJSONFlag(a); ok {
				if err != nil {
					return "", false, err
				}
				asJSON = v
				continue
			}
			if strings.HasPrefix(a, "-") {
				return "", false, fmt.Errorf("unknown help flag %q", a)
			}
		}
		if topic != "" {
			return "", false, fmt.Errorf("help takes at most one command, got %q", a)
		}
		topic = a
	}
	return topic, asJSON, nil
}

func parseVersionArgs(args []string) (bool, error) {
	var asJSON bool
	afterTerminator := false
	for _, a := range args {
		if !afterTerminator && a == "--" {
			afterTerminator = true
			continue
		}
		if !afterTerminator {
			if v, ok, err := parseJSONFlag(a); ok {
				if err != nil {
					return false, err
				}
				asJSON = v
				continue
			}
			if strings.HasPrefix(a, "-") {
				return false, fmt.Errorf("unknown version flag %q", a)
			}
		}
		return false, fmt.Errorf("version takes no positional arguments, got %q", a)
	}
	return asJSON, nil
}

func parseJSONFlag(arg string) (enabled, ok bool, err error) {
	switch {
	case arg == "--json" || arg == "-json":
		return true, true, nil
	case strings.HasPrefix(arg, "--json="):
		v, err := strconv.ParseBool(strings.TrimPrefix(arg, "--json="))
		if err != nil {
			return false, true, fmt.Errorf("invalid --json value %q", strings.TrimPrefix(arg, "--json="))
		}
		return v, true, nil
	case strings.HasPrefix(arg, "-json="):
		v, err := strconv.ParseBool(strings.TrimPrefix(arg, "-json="))
		if err != nil {
			return false, true, fmt.Errorf("invalid -json value %q", strings.TrimPrefix(arg, "-json="))
		}
		return v, true, nil
	default:
		return false, false, nil
	}
}

// helpForCommand prints help for a single command: its summary, usage line
// (which documents its flags), and aliases — as text, or as a JSON object with
// asJSON.
func helpForCommand(name string, asJSON bool) int {
	info, ok := commandInfoForName(name)
	if !ok {
		err := fmt.Errorf("unknown command %q", name)
		renderError(err, asJSON)
		return exitCode(err)
	}
	if asJSON {
		data, _ := json.MarshalIndent(info, "", "  ")
		fmt.Printf("%s\n", data)
		return 0
	}
	fmt.Println(info.Summary)
	fmt.Println()
	fmt.Printf("usage: %s\n", info.Usage)
	if len(info.Aliases) > 0 {
		fmt.Printf("aliases: %s\n", strings.Join(info.Aliases, ", "))
	}
	return 0
}

func commandInfoForName(name string) (commandInfo, bool) {
	if c, ok := byName[name]; ok {
		return commandInfo{c.Name, c.Summary, c.Usage, c.Aliases}, true
	}
	switch name {
	case "help", "-h", "--help":
		return commandInfo{Name: "help", Summary: "show this help", Usage: "st help [<command>] [--json]", Aliases: []string{"-h", "--help"}}, true
	case "version", "-v", "--version":
		return commandInfo{Name: "version", Summary: "print the version", Usage: "st version [--json]", Aliases: []string{"-v", "--version"}}, true
	default:
		return commandInfo{}, false
	}
}

// exitCode maps an error to a stable exit status so agents can branch on the
// category without parsing messages.
func exitCode(err error) int {
	switch {
	case errors.Is(err, stack.ErrNotInitialized):
		return 3
	case errors.Is(err, stack.ErrConflict):
		return 2
	case errors.Is(err, stack.ErrDirty):
		return 4
	default:
		return 1
	}
}

// errorCode returns the short machine code for an error, used in --json output.
func errorCode(err error) string {
	switch {
	case errors.Is(err, stack.ErrNotInitialized):
		return "not_initialized"
	case errors.Is(err, stack.ErrConflict):
		return "conflict"
	case errors.Is(err, stack.ErrDirty):
		return "dirty"
	default:
		return "error"
	}
}

// renderError writes a command failure: a structured JSON envelope on stderr in
// --json mode, or "st: <err>" otherwise.
func renderError(err error, asJSON bool) {
	if asJSON {
		enc := json.NewEncoder(os.Stderr)
		enc.SetIndent("", "  ")
		_ = enc.Encode(map[string]any{
			"error": map[string]string{"code": errorCode(err), "message": err.Error()},
		})
		return
	}
	fmt.Fprintf(os.Stderr, "st: %s\n", err)
}

// renderInternalError reports a recovered panic. In --json mode it writes the
// standard error envelope with code "internal" to stderr, so the machine
// contract holds even on a crash; otherwise it writes a one-line message plus
// the stack trace to stderr for a human bug report.
func renderInternalError(r any, asJSON bool) {
	msg := fmt.Sprintf("internal error: %v", r)
	if asJSON {
		enc := json.NewEncoder(os.Stderr)
		enc.SetIndent("", "  ")
		_ = enc.Encode(map[string]any{
			"error": map[string]string{"code": "internal", "message": msg},
		})
		return
	}
	fmt.Fprintln(os.Stderr, "st: "+msg)
	os.Stderr.Write(debug.Stack())
}

// jsonRequested reports whether --json (or -json) appears before a "--"
// terminator in args, so the dispatcher can format errors accordingly.
func jsonRequested(args []string) bool {
	for _, a := range args {
		if a == "--" {
			return false
		}
		if a == "--json" || a == "-json" {
			return true
		}
		for _, prefix := range []string{"--json=", "-json="} {
			if strings.HasPrefix(a, prefix) {
				v, err := strconv.ParseBool(strings.TrimPrefix(a, prefix))
				return err != nil || v
			}
		}
	}
	return false
}

// printHelp writes the list of registered commands (in registration order) and
// the general usage line to stdout, as text or, with asJSON, as a JSON object
// with a "commands" array.
func printHelp(asJSON bool) {
	if asJSON {
		infos := make([]commandInfo, 0, len(registry)+2)
		for _, c := range registry {
			infos = append(infos, commandInfo{c.Name, c.Summary, c.Usage, c.Aliases})
		}
		helpInfo, _ := commandInfoForName("help")
		versionInfo, _ := commandInfoForName("version")
		infos = append(infos, helpInfo, versionInfo)
		data, _ := json.MarshalIndent(map[string]any{"commands": infos}, "", "  ")
		fmt.Printf("%s\n", data)
		return
	}
	fmt.Println("st - manage stacked diffs on top of git")
	fmt.Println()
	fmt.Println("usage: st <command> [args]")
	fmt.Println()
	fmt.Println("commands:")

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	for _, c := range registry {
		fmt.Fprintf(w, "  %s\t%s\n", c.Name, c.Summary)
	}
	// Built-in pseudo-commands.
	fmt.Fprintf(w, "  %s\t%s\n", "help", "show this help")
	fmt.Fprintf(w, "  %s\t%s\n", "version", "print the version")
	w.Flush()
}

// printVersion prints the release version along with the embedded build/VCS
// metadata (commit, build time, Go version) when it is available, as text or,
// with asJSON, as a JSON object.
func printVersion(asJSON bool) {
	var rev, when, goVersion string
	var dirty bool
	if info, ok := debug.ReadBuildInfo(); ok {
		goVersion = info.GoVersion
		for _, s := range info.Settings {
			switch s.Key {
			case "vcs.revision":
				rev = s.Value
			case "vcs.time":
				when = s.Value
			case "vcs.modified":
				dirty = s.Value == "true"
			}
		}
	}
	if len(rev) > 12 {
		rev = rev[:12]
	}
	if rev != "" && dirty {
		rev += " (dirty)"
	}

	if asJSON {
		data, _ := json.MarshalIndent(struct {
			Version string `json:"version"`
			Commit  string `json:"commit,omitempty"`
			Built   string `json:"built,omitempty"`
			Go      string `json:"go,omitempty"`
		}{version, rev, when, goVersion}, "", "  ")
		fmt.Printf("%s\n", data)
		return
	}

	fmt.Printf("st %s\n", version)
	if rev != "" {
		fmt.Printf("commit: %s\n", rev)
	}
	if when != "" {
		fmt.Printf("built:  %s\n", when)
	}
	if goVersion != "" {
		fmt.Printf("go:     %s\n", goVersion)
	}
}
