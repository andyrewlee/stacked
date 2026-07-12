package cmd

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/andyrewlee/stacked/internal/stack"
)

// withArgs runs fn with os.Args set to {"st"} + args, restoring os.Args after.
func withArgs(t *testing.T, args []string, fn func()) {
	t.Helper()
	orig := os.Args
	os.Args = append([]string{"st"}, args...)
	defer func() { os.Args = orig }()
	fn()
}

func captureStderr(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	orig := os.Stderr
	os.Stderr = w
	defer func() { os.Stderr = orig }()

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

func TestExecuteHelp(t *testing.T) {
	for _, args := range [][]string{nil, {"help"}, {"-h"}, {"--help"}} {
		var code int
		out := captureStdout(t, func() {
			withArgs(t, args, func() { code = Execute() })
		})
		if code != 0 {
			t.Fatalf("Execute(%v) = %d, want 0", args, code)
		}
		// Help lists registered commands plus the built-in pseudo-commands.
		for _, want := range []string{"st - manage stacked diffs", "create", "help", "version"} {
			if !strings.Contains(out, want) {
				t.Fatalf("help(%v) missing %q:\n%s", args, want, out)
			}
		}
	}
}

func TestExecuteVersion(t *testing.T) {
	for _, args := range [][]string{{"version"}, {"-v"}, {"--version"}} {
		var code int
		out := captureStdout(t, func() {
			withArgs(t, args, func() { code = Execute() })
		})
		if code != 0 {
			t.Fatalf("Execute(%v) = %d, want 0", args, code)
		}
		if !strings.HasPrefix(out, "st "+version) {
			t.Fatalf("version(%v) = %q, want prefix %q", args, out, "st "+version)
		}
	}
}

func TestExecuteSubcommandHelp(t *testing.T) {
	for _, args := range [][]string{{"status", "--help"}, {"bottom", "--help"}, {"completion", "--help"}} {
		var code int
		var stdout string
		stderr := executeCapturingOutput(t, args, &code, &stdout)
		if code != 0 {
			t.Fatalf("Execute(%v) = %d, want 0", args, code)
		}
		if stderr != "" {
			t.Fatalf("Execute(%v) wrote stderr:\n%s", args, stderr)
		}
		if !strings.Contains(stdout, "usage: st "+args[0]) {
			t.Fatalf("Execute(%v) missing usage on stdout:\n%s", args, stdout)
		}
	}
}

func TestExecuteHelpJSON(t *testing.T) {
	// `st help --json` and `st --help --json` emit a commands array, exit 0.
	for _, args := range [][]string{{"help", "--json"}, {"--help", "--json"}} {
		var code int
		out := captureStdout(t, func() {
			withArgs(t, args, func() { code = Execute() })
		})
		if code != 0 {
			t.Fatalf("Execute(%v) = %d, want 0", args, code)
		}
		var payload struct {
			Commands []commandInfo `json:"commands"`
		}
		if err := json.Unmarshal([]byte(out), &payload); err != nil {
			t.Fatalf("help --json not parseable for %v: %v\n%s", args, err, out)
		}
		names := map[string]bool{}
		for _, c := range payload.Commands {
			names[c.Name] = true
		}
		for _, want := range []string{"create", "help", "version"} {
			if !names[want] {
				t.Fatalf("help --json missing %q in %v", want, names)
			}
		}
	}

	// `st help <command> --json` emits a single object.
	var code int
	out := captureStdout(t, func() {
		withArgs(t, []string{"help", "create", "--json"}, func() { code = Execute() })
	})
	if code != 0 {
		t.Fatalf("Execute(help create --json) = %d, want 0", code)
	}
	var info commandInfo
	if err := json.Unmarshal([]byte(out), &info); err != nil {
		t.Fatalf("help create --json not parseable: %v\n%s", err, out)
	}
	if info.Name != "create" {
		t.Errorf("help create --json name = %q, want create", info.Name)
	}
	for _, topic := range []string{"help", "version"} {
		out = captureStdout(t, func() {
			withArgs(t, []string{"help", topic, "--json"}, func() { code = Execute() })
		})
		if code != 0 {
			t.Fatalf("Execute(help %s --json) = %d, want 0", topic, code)
		}
		if err := json.Unmarshal([]byte(out), &info); err != nil {
			t.Fatalf("help %s --json not parseable: %v\n%s", topic, err, out)
		}
		if info.Name != topic {
			t.Errorf("help %s --json name = %q, want %s", topic, info.Name, topic)
		}
	}

	// `st version --json`.
	out = captureStdout(t, func() {
		withArgs(t, []string{"version", "--json"}, func() { code = Execute() })
	})
	if code != 0 {
		t.Fatalf("Execute(version --json) = %d, want 0", code)
	}
	var v struct {
		Version string `json:"version"`
	}
	if err := json.Unmarshal([]byte(out), &v); err != nil {
		t.Fatalf("version --json not parseable: %v\n%s", err, out)
	}
	if v.Version != version {
		t.Errorf("version --json = %q, want %q", v.Version, version)
	}
}

func TestExecuteBuiltInUnknownFlags(t *testing.T) {
	cases := []struct {
		args     []string
		wantJSON bool
	}{
		{[]string{"help", "--jsno"}, false},
		{[]string{"help", "--bad", "--json"}, true},
		{[]string{"version", "--bad"}, false},
		{[]string{"version", "--bad", "--json"}, true},
	}
	for _, tc := range cases {
		var code int
		var stdout string
		stderr := executeCapturingOutput(t, tc.args, &code, &stdout)
		if code != 1 {
			t.Fatalf("Execute(%v) = %d, want 1", tc.args, code)
		}
		if stdout != "" {
			t.Fatalf("Execute(%v) wrote stdout:\n%s", tc.args, stdout)
		}
		if tc.wantJSON {
			var payload map[string]any
			if err := json.Unmarshal([]byte(stderr), &payload); err != nil {
				t.Fatalf("Execute(%v) stderr not JSON: %v\n%s", tc.args, err, stderr)
			}
			continue
		}
		if !strings.Contains(stderr, "unknown") {
			t.Fatalf("Execute(%v) stderr missing unknown-flag message:\n%s", tc.args, stderr)
		}
	}
}

// Every command except completion and shell accepts --json and must say so in
// its registry usage string, so `st help <cmd>` documents the flag (CLI-6,
// DOC-2/3).
func TestEveryCommandDocumentsJSONInUsage(t *testing.T) {
	for _, c := range registry {
		if c.Name == "completion" || c.Name == "shell" {
			continue // these emit shell scripts, not JSON
		}
		if !strings.Contains(c.Usage, "--json") {
			t.Errorf("command %q usage %q does not document --json", c.Name, c.Usage)
		}
	}
	if u := byName["sync"].Usage; !strings.Contains(u, "--dry-run") {
		t.Errorf("sync usage %q does not document --dry-run", u)
	}
}

func TestJSONExceptionTextMatchesCommandFlags(t *testing.T) {
	exceptions := jsonExceptionCommands(t)
	plainExceptions := strings.Join(exceptions, " and ")
	quotedExceptions := "`" + strings.Join(exceptions, "` and `") + "`"

	for _, tc := range []struct {
		name string
		path string
		want string
	}{
		{
			name: "docs/AGENT.md",
			path: "../docs/AGENT.md",
			want: "except " + quotedExceptions + " accepts `--json`",
		},
		{
			name: "README.md",
			path: "../README.md",
			want: "except " + quotedExceptions + " (plus `help`/`version`) accepts `--json`",
		},
	} {
		text, err := os.ReadFile(tc.path)
		if err != nil {
			t.Fatalf("read %s: %v", tc.name, err)
		}
		if !strings.Contains(string(text), tc.want) {
			t.Errorf("%s does not document JSON exceptions %q", tc.name, tc.want)
		}
	}

	var guideText string
	var err error
	guideText = captureStdout(t, func() { err = runGuide(nil) })
	if err != nil {
		t.Fatalf("runGuide: %v", err)
	}
	wantGuide := "except " + plainExceptions + " accepts --json"
	if !strings.Contains(guideText, wantGuide) {
		t.Errorf("st guide does not document JSON exceptions %q", wantGuide)
	}
}

func jsonExceptionCommands(t *testing.T) []string {
	t.Helper()

	var exceptions []string
	for _, c := range registry {
		hasJSON := false
		for _, f := range commandFlags(c) {
			if f.Name == "json" {
				hasJSON = true
				break
			}
		}
		if !hasJSON {
			exceptions = append(exceptions, c.Name)
		}
	}
	if len(exceptions) == 0 {
		t.Fatal("no registered commands lack --json")
	}
	return exceptions
}

// TestEveryCommandDefinesJSONFlag is the runtime counterpart to
// TestEveryCommandDocumentsJSONInUsage: advertising --json in the usage string
// is worthless if the FlagSet never defines the flag (copy the usage line, forget
// the fs.BoolVar, and the command fails at runtime with "unknown flag" while all
// tests stay green). Every command except completion and shell must *parse*
// --json. Flag parsing runs before any lock/load/git work, so this needs no
// initialized repo; it tolerates the legitimate downstream failures (not
// initialized, missing positional args, not a git repo) and only fails on a
// genuinely undefined flag.
func TestEveryCommandDefinesJSONFlag(t *testing.T) {
	t.Chdir(t.TempDir()) // hermetic: an empty, non-stacked dir; commands fail past the parse, harmlessly
	resetWorktreeCache()
	for _, c := range registry {
		if c.Name == "completion" || c.Name == "shell" {
			continue // these emit shell scripts, not JSON
		}
		var err error
		// Swallow any usage/output the command prints on its (expected) failure.
		_ = captureStdout(t, func() { err = c.Run([]string{"--json"}) })
		if err != nil && strings.Contains(err.Error(), "provided but not defined") {
			t.Errorf("command %q advertises --json but its FlagSet does not define it: %v", c.Name, err)
		}
	}
}

// TestSubcommandHelpJSON pins the fix for the silent `st <cmd> -h --json`: it
// must emit the same machine-readable info as `st help <cmd> --json` (byte for
// byte), and aliases must resolve the same way.
func TestSubcommandHelpJSON(t *testing.T) {
	dashH := captureStdout(t, func() {
		withArgs(t, []string{"create", "-h", "--json"}, func() { _ = Execute() })
	})
	if strings.TrimSpace(dashH) == "" {
		t.Fatal("st create -h --json produced no output")
	}
	var info commandInfo
	if err := json.Unmarshal([]byte(dashH), &info); err != nil {
		t.Fatalf("st create -h --json not parseable: %v\n%s", err, dashH)
	}
	if info.Name != "create" {
		t.Errorf("st create -h --json name = %q, want create", info.Name)
	}

	helpForm := captureStdout(t, func() {
		withArgs(t, []string{"help", "create", "--json"}, func() { _ = Execute() })
	})
	if dashH != helpForm {
		t.Errorf("st create -h --json != st help create --json\n-h:\n%s\nhelp:\n%s", dashH, helpForm)
	}

	aliasForm := captureStdout(t, func() {
		withArgs(t, []string{"co", "-h", "--json"}, func() { _ = Execute() })
	})
	var aliasInfo commandInfo
	if err := json.Unmarshal([]byte(aliasForm), &aliasInfo); err != nil || aliasInfo.Name != "checkout" {
		t.Errorf("st co -h --json should resolve to checkout: %q (err %v)", aliasForm, err)
	}
}

// TestRenderErrorConflictFields asserts the --json error envelope surfaces the
// conflicted branch and parent as structured fields (not just in the message).
func TestRenderErrorConflictFields(t *testing.T) {
	err := &stack.ConflictError{Action: "rebasing", Branch: "feat-b", Onto: "feat-a"}
	stderr := captureStderr(t, func() { renderError(err, true) })
	var env struct {
		Error struct {
			Code, Message, Branch, Onto string
		}
	}
	if e := json.Unmarshal([]byte(stderr), &env); e != nil {
		t.Fatalf("renderError JSON not parseable: %v\n%s", e, stderr)
	}
	if env.Error.Code != "conflict" {
		t.Errorf("code = %q, want conflict", env.Error.Code)
	}
	if env.Error.Branch != "feat-b" || env.Error.Onto != "feat-a" {
		t.Errorf("branch/onto = %q/%q, want feat-b/feat-a", env.Error.Branch, env.Error.Onto)
	}
}

// TestHelpReportsOnlyRealFlags asserts every flag help advertises for a command
// is actually accepted by it, so the introspection flag sets in flagsets.go
// cannot drift into listing a flag the command rejects.
func TestHelpReportsOnlyRealFlags(t *testing.T) {
	t.Chdir(t.TempDir())
	resetWorktreeCache()
	for _, c := range registry {
		for _, f := range commandFlags(c) {
			var err error
			_ = captureStdout(t, func() { err = c.Run([]string{"--" + f.Name, "x"}) })
			if err != nil && strings.Contains(err.Error(), "provided but not defined") {
				t.Errorf("help lists flag --%s for %q but Run rejects it: %v", f.Name, c.Name, err)
			}
		}
	}
}

// TestHelpJSONIncludesFlags pins the structured flag list: help <cmd> --json
// reports each declared flag with its type.
func TestHelpJSONIncludesFlags(t *testing.T) {
	out := captureStdout(t, func() {
		withArgs(t, []string{"help", "submit", "--json"}, func() { _ = Execute() })
	})
	var info commandInfo
	if err := json.Unmarshal([]byte(out), &info); err != nil {
		t.Fatalf("help submit --json not parseable: %v\n%s", err, out)
	}
	got := map[string]string{}
	for _, f := range info.Flags {
		got[f.Name] = f.Type
	}
	for name, typ := range map[string]string{"remote": "string", "dry-run": "bool", "json": "bool"} {
		if got[name] != typ {
			t.Errorf("submit flag %q type = %q, want %q (flags: %+v)", name, got[name], typ, info.Flags)
		}
	}
}

// TestUnknownFlagPointsAtHelp asserts an unknown flag's error points at the
// command's help, mirroring the unknown-command path (the raw stdlib message is
// a dead end otherwise).
func TestUnknownFlagPointsAtHelp(t *testing.T) {
	t.Chdir(t.TempDir())
	resetWorktreeCache()
	err := byName["status"].Run([]string{"--nope"})
	if err == nil {
		t.Fatal("unknown flag should error")
	}
	if !strings.Contains(err.Error(), `provided but not defined`) {
		t.Errorf("expected the stdlib flag error, got: %v", err)
	}
	if !strings.Contains(err.Error(), `run "st help status"`) {
		t.Errorf("unknown-flag error should point at st help status: %v", err)
	}
}

func TestSuggestCommand(t *testing.T) {
	cases := []struct{ in, want string }{
		{"creat", "create"},   // distance 1
		{"statuss", "status"}, // distance 1
		{"loggg", "log"},      // distance 2 (boundary)
		{"createxyz", ""},     // distance 3 -> too far
		{"zzzzzzzz", ""},      // far from everything
	}
	for _, tc := range cases {
		if got := suggestCommand(tc.in); got != tc.want {
			t.Errorf("suggestCommand(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestUnknownCommandErr(t *testing.T) {
	near := unknownCommandErr("creat").Error()
	if !strings.Contains(near, `did you mean "create"?`) {
		t.Errorf("near typo should suggest create: %q", near)
	}
	if !strings.Contains(near, `run "st help"`) {
		t.Errorf("missing help pointer: %q", near)
	}
	far := unknownCommandErr("zzzzzzzz").Error()
	if strings.Contains(far, "did you mean") {
		t.Errorf("far name should not suggest: %q", far)
	}
	if !strings.Contains(far, `run "st help"`) {
		t.Errorf("missing help pointer: %q", far)
	}
}

// TestHelpForCommandUnknownEmitsPointer pins the unification: `st help <unknown>`
// now routes through unknownCommandErr, so it gives the same "run st help"
// pointer the dispatcher's not-found path always did.
func TestHelpForCommandUnknownEmitsPointer(t *testing.T) {
	stderr := captureStderr(t, func() { _ = helpForCommand("frobnicate", false) })
	if !strings.Contains(stderr, `run "st help"`) {
		t.Errorf("st help <unknown> should point at st help: %q", stderr)
	}
}

// TestEveryCommandUsageMatchesRegistry locks the drift hazard newFlagSet removed:
// each command's -h output must begin with the exact usage line derived from its
// registered Command.Usage, so the printed and registered usage can never diverge
// again. -h returns flag.ErrHelp before any git/state work, so no repo is needed.
func TestEveryCommandUsageMatchesRegistry(t *testing.T) {
	t.Chdir(t.TempDir())
	resetWorktreeCache()
	for _, c := range registry {
		got := captureStdout(t, func() { _ = c.Run([]string{"-h"}) })
		first, _, _ := strings.Cut(got, "\n")
		if want := "usage: " + c.Usage; first != want {
			t.Errorf("command %q -h first line = %q, want %q", c.Name, first, want)
		}
	}
}

func TestExecuteUnknownCommand(t *testing.T) {
	// Plain mode: exit 1 and nothing on stdout (the diagnostic goes to stderr,
	// verified by the e2e suite).
	var code int
	stdout := captureStdout(t, func() {
		withArgs(t, []string{"frobnicate"}, func() { code = Execute() })
	})
	if code != 1 {
		t.Fatalf("Execute(unknown) = %d, want 1", code)
	}
	if strings.TrimSpace(stdout) != "" {
		t.Fatalf("unknown command wrote stdout:\n%s", stdout)
	}

	// --json: a parseable error envelope on stderr, still nothing on stdout.
	var stdoutJSON string
	errOut := executeCapturingOutput(t, []string{"frobnicate", "--json"}, &code, &stdoutJSON)
	if code != 1 {
		t.Fatalf("Execute(unknown --json) = %d, want 1", code)
	}
	if stdoutJSON != "" {
		t.Fatalf("unknown command --json wrote stdout:\n%s", stdoutJSON)
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(errOut), &payload); err != nil {
		t.Fatalf("unknown --json stderr not parseable: %v\n%s", err, errOut)
	}
}

func TestExecuteCommandError(t *testing.T) {
	// `st up` in an uninitialized repo returns ErrNotInitialized, which the
	// dispatcher maps to the dedicated exit code 3.
	newRepo(t)
	var code int
	withArgs(t, []string{"up"}, func() {
		_ = captureStdout(t, func() { code = Execute() })
	})
	if code != 3 {
		t.Fatalf("Execute(up uninitialized) = %d, want 3 (not_initialized)", code)
	}
}

func TestExecuteJSONErrorIsParseable(t *testing.T) {
	var code int
	var stdout string
	errOut := executeCapturingOutput(t, []string{"create", "--json"}, &code, &stdout)
	if code != 1 {
		t.Fatalf("Execute(create --json) = %d, want 1", code)
	}
	if stdout != "" {
		t.Fatalf("JSON error wrote stdout:\n%s", stdout)
	}
	if strings.Contains(errOut, "usage:") {
		t.Fatalf("JSON error included usage text:\n%s", errOut)
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(errOut), &payload); err != nil {
		t.Fatalf("JSON error was not parseable: %v\n%s", err, errOut)
	}
}

func TestExecuteJSONFlagParseErrorIsParseable(t *testing.T) {
	cases := [][]string{
		{"create", "--json", "--bad"},
		{"create", "--json=true", "--bad"},
		{"status", "--json", "--bad"},
		{"status", "--json=true", "--bad"},
	}
	for _, args := range cases {
		var code int
		var stdout string
		errOut := executeCapturingOutput(t, args, &code, &stdout)
		if code != 1 {
			t.Fatalf("Execute(%v) = %d, want 1", args, code)
		}
		if stdout != "" {
			t.Fatalf("JSON flag parse error wrote stdout for %v:\n%s", args, stdout)
		}
		if strings.Contains(errOut, "usage:") {
			t.Fatalf("JSON flag parse error included flag output for %v:\n%s", args, errOut)
		}
		var payload map[string]any
		if err := json.Unmarshal([]byte(errOut), &payload); err != nil {
			t.Fatalf("JSON flag parse error was not parseable for %v: %v\n%s", args, err, errOut)
		}
	}
}

func executeCapturingOutput(t *testing.T, args []string, code *int, stdout *string) string {
	t.Helper()
	return captureStderr(t, func() {
		*stdout = captureStdout(t, func() {
			withArgs(t, args, func() { *code = Execute() })
		})
	})
}

func TestExitCodeAndErrorCodeMapping(t *testing.T) {
	cases := []struct {
		err  error
		code int
		json string
	}{
		{stack.ErrNotInitialized, 3, "not_initialized"},
		{stack.ErrConflict, 2, "conflict"},
		{stack.ErrDirty, 4, "dirty"},
		{errors.New("boom"), 1, "error"},
		{fmt.Errorf("wrapped: %w", stack.ErrConflict), 2, "conflict"},
	}
	for _, c := range cases {
		if got := exitCode(c.err); got != c.code {
			t.Errorf("exitCode(%v) = %d, want %d", c.err, got, c.code)
		}
		if got := errorCode(c.err); got != c.json {
			t.Errorf("errorCode(%v) = %q, want %q", c.err, got, c.json)
		}
	}

	// st guide advertises these same codes via errorCodeSummary; assert each
	// table name actually appears in the guide text so the two cannot drift.
	guideText := captureStdout(t, func() { _ = runGuide(nil) })
	for _, c := range errorClasses {
		// Assert the code=name pair errorCodeSummary() renders, not the bare name:
		// the word "conflict" already appears elsewhere in the guide, so a bare-name
		// check would not catch the conflict row drifting from the table.
		want := fmt.Sprintf("%d=%s", c.code, c.name)
		if !strings.Contains(guideText, want) {
			t.Errorf("st guide text missing error code %q:\n%s", want, guideText)
		}
	}
}

func TestJSONRequested(t *testing.T) {
	if !jsonRequested([]string{"create", "x", "--json"}) {
		t.Error("want true when --json present")
	}
	if !jsonRequested([]string{"create", "--json=true"}) {
		t.Error("want true when --json=true present")
	}
	if jsonRequested([]string{"create", "--json=false"}) {
		t.Error("want false when --json=false present")
	}
	if jsonRequested([]string{"create", "--", "--json"}) {
		t.Error("want false when --json is after a -- terminator")
	}
	if jsonRequested([]string{"log"}) {
		t.Error("want false when absent")
	}
}

// A panic in a command must be recovered into a structured failure with a
// distinct exit code (not 2, the runtime's panic code, which means "conflict").
func TestExecuteRecoversFromPanic(t *testing.T) {
	// Register a throwaway command that panics, removing it afterward so the
	// global registry stays clean for the other (sequential) tests.
	register(&Command{
		Name:    "panic-boom",
		Summary: "test-only panicking command",
		Usage:   "st panic-boom",
		Run:     func([]string) error { panic("kaboom") },
	})
	defer func() {
		registry = registry[:len(registry)-1]
		delete(byName, "panic-boom")
	}()

	// JSON mode: stderr must be a parseable envelope with code "internal".
	var code int
	var stdout string
	errOut := executeCapturingOutput(t, []string{"panic-boom", "--json"}, &code, &stdout)
	if code != exitInternal {
		t.Fatalf("Execute(panic --json) = %d, want %d", code, exitInternal)
	}
	if stdout != "" {
		t.Fatalf("panic wrote stdout:\n%s", stdout)
	}
	var payload struct {
		Error struct{ Code, Message string }
	}
	if err := json.Unmarshal([]byte(errOut), &payload); err != nil {
		t.Fatalf("panic envelope not parseable: %v\n%s", err, errOut)
	}
	if payload.Error.Code != "internal" {
		t.Errorf("panic error code = %q, want internal", payload.Error.Code)
	}

	// Plain mode: a human-readable message, still exit exitInternal.
	code = 0
	errOut = executeCapturingOutput(t, []string{"panic-boom"}, &code, &stdout)
	if code != exitInternal {
		t.Fatalf("Execute(panic) = %d, want %d", code, exitInternal)
	}
	if !strings.Contains(errOut, "internal error") {
		t.Errorf("plain panic output missing 'internal error':\n%s", errOut)
	}
}

func TestExecuteDispatchesSubcommand(t *testing.T) {
	newRepo(t)
	mustInit(t)
	mustCreate(t, "feat-a", "a.txt", "a\n", "a")

	// A real subcommand run through the dispatcher should succeed (exit 0).
	var code int
	withArgs(t, []string{"status"}, func() {
		_ = captureStdout(t, func() { code = Execute() })
	})
	if code != 0 {
		t.Fatalf("Execute(status) = %d, want 0", code)
	}
}
