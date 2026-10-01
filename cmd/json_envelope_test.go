package cmd

// The --json error contract is a cross-command matrix, not a per-command
// property: every failure must surface as {"error":{"code","message",...}} on
// stderr, with the exit status matching the code's class, and stdout staying
// clean. The dispatcher's renderError is the single emitter, so these tests
// pin the whole matrix — if a future command bypasses it, the row fails.

import (
	"encoding/json"
	"testing"

	"github.com/andyrewlee/stacked/internal/stack"
)

// jsonErrorEnvelope mirrors the stderr shape docs/AGENT.md promises: a top-level
// "error" object with a machine code and human message, plus structured
// branch/onto fields on conflict.
type jsonErrorEnvelope struct {
	Error struct {
		Code    string `json:"code"`
		Message string `json:"message"`
		Branch  string `json:"branch"`
		Onto    string `json:"onto"`
	} `json:"error"`
}

// envelopeExitCode maps the documented error.code vocabulary to its exit
// status, so a row need only name the expected code — the exit assertion comes
// from the shared contract, never hand-picked per row.
var envelopeExitCode = map[string]int{
	"error":           1,
	"conflict":        2,
	"not_initialized": 3,
	"dirty":           4,
	"locked":          5,
	"locked_guard":    5,
	"internal":        70,
}

// requireErrorEnvelope runs args through Execute under --json already present
// in args, then asserts: exit code matches the documented class for wantCode,
// stdout is empty, and stderr carries a parseable envelope whose code is
// wantCode with a non-empty message.
func requireErrorEnvelope(t *testing.T, args []string, wantCode string) jsonErrorEnvelope {
	t.Helper()
	wantExit, ok := envelopeExitCode[wantCode]
	if !ok {
		t.Fatalf("unknown error code %q — extend envelopeExitCode", wantCode)
	}
	var code int
	var stdout string
	stderr := executeCapturingOutput(t, args, &code, &stdout)
	if code != wantExit {
		t.Fatalf("Execute(%v) = %d, want %d (%s)", args, code, wantExit, wantCode)
	}
	if stdout != "" {
		t.Fatalf("Execute(%v) wrote stdout on failure:\n%s", args, stdout)
	}
	var env jsonErrorEnvelope
	if err := json.Unmarshal([]byte(stderr), &env); err != nil {
		t.Fatalf("Execute(%v) stderr is not the JSON envelope: %v\n%s", args, err, stderr)
	}
	if env.Error.Code != wantCode {
		t.Fatalf("Execute(%v) error.code = %q, want %q", args, env.Error.Code, wantCode)
	}
	if env.Error.Message == "" {
		t.Fatalf("Execute(%v) error.message is empty", args)
	}
	return env
}

// TestJSONEnvelopeParseError pins that a flag-parse failure under --json emits
// the standard envelope (exit 1 / code "error") for EVERY JSON-capable
// command, not just the ones with dedicated tests. Parse errors flow through
// the same dispatcher as domain errors, so a command whose flagset diverged
// would break the contract here.
func TestJSONEnvelopeParseError(t *testing.T) {
	for _, c := range registry {
		if c.Hidden {
			continue // __complete speaks its own argv protocol
		}
		hasJSON := false
		for _, f := range commandFlags(c) {
			if f.Name == "json" {
				hasJSON = true
				break
			}
		}
		if !hasJSON {
			continue // completion and shell emit shell scripts, not JSON
		}
		t.Run(c.Name, func(t *testing.T) {
			requireErrorEnvelope(t, []string{c.Name, "--json", "--bogus"}, "error")
		})
	}
	// The built-in pseudo-commands share the same dispatcher path.
	for _, name := range []string{"help", "version"} {
		t.Run(name, func(t *testing.T) {
			requireErrorEnvelope(t, []string{name, "--json", "--bogus"}, "error")
		})
	}
}

// TestJSONEnvelopeNotInitialized pins exit 3 / "not_initialized" for every
// command that needs stack state, run in a real git repo that was never
// `st init`ed. Commands with a documented different answer — abort tolerates
// the missing state and fails later on the absent rebase; undo/reporting
// commands that legitimately work without state — are pinned to their actual
// contract so the difference is deliberate, not drift.
func TestJSONEnvelopeNotInitialized(t *testing.T) {
	cases := []struct {
		name    string
		args    []string
		code    string // expected error.code; "" means the command succeeds
		success bool   // command runs fine without stack state
	}{
		{name: "abort", code: "error"}, // tolerated uninit, then "no rebase in progress"
		{name: "absorb", code: "not_initialized"},
		{name: "bottom", code: "not_initialized"},
		{name: "checkout", code: "not_initialized"},
		{name: "continue", code: "not_initialized"},
		{name: "create", args: []string{"x"}, code: "not_initialized"},
		{name: "delete", args: []string{"x"}, code: "not_initialized"},
		{name: "down", code: "not_initialized"},
		{name: "fold", code: "not_initialized"},
		{name: "guide", success: true}, // static text, never touches state
		{name: "log", code: "not_initialized"},
		{name: "modify", code: "not_initialized"},
		{name: "onto", args: []string{"x"}, code: "not_initialized"},
		{name: "prune", code: "not_initialized"},
		{name: "rename", args: []string{"a", "b"}, code: "not_initialized"},
		{name: "repair", code: "not_initialized"},
		{name: "restack", code: "not_initialized"},
		{name: "squash", code: "not_initialized"},
		{name: "status", code: "not_initialized"},
		{name: "submit", code: "not_initialized"},
		{name: "sync", code: "not_initialized"},
		{name: "top", code: "not_initialized"},
		{name: "track", args: []string{"x"}, code: "not_initialized"},
		{name: "undo", success: true}, // empty journal is a documented success: {"undone":false}
		{name: "untrack", args: []string{"x"}, code: "not_initialized"},
		{name: "up", code: "not_initialized"},
		{name: "validate", code: "not_initialized"},
		{name: "worktree", args: []string{"x"}, code: "not_initialized"},
		{name: "worktree", args: []string{"ls"}, success: true}, // pure `git worktree list`
	}
	for _, tc := range cases {
		run := tc.name + " " + jsonJoin(tc.args)
		t.Run(run, func(t *testing.T) {
			newRepo(t)
			args := append(append([]string{tc.name}, tc.args...), "--json")
			if tc.success {
				var code int
				stderr := executeCapturingOutput(t, args, &code, new(string))
				if code != 0 {
					t.Fatalf("Execute(%v) = %d, want 0; stderr:\n%s", args, code, stderr)
				}
				return
			}
			requireErrorEnvelope(t, args, tc.code)
		})
	}
}

func jsonJoin(args []string) string {
	out := ""
	for i, a := range args {
		if i > 0 {
			out += " "
		}
		out += a
	}
	return out
}

// TestJSONEnvelopeUsageError pins that arg-rejection (missing/extra
// positionals) under --json lands in the same envelope as flag-parse errors:
// exit 1, code "error".
func TestJSONEnvelopeUsageError(t *testing.T) {
	cases := [][]string{
		{"create"},                // missing branch name
		{"delete"},                // missing branch name
		{"onto"},                  // missing target
		{"rename", "a", "b", "c"}, // too many positionals (one or two are legal)
		{"worktree"},              // no subcommand at all
		{"undo", "1", "2"},        // rejects extra positionals
		{"status", "extra"},       // rejects positionals
		{"untrack", "a", "b"},     // rejects positionals past the optional branch
	}
	for _, args := range cases {
		t.Run(jsonJoin(args), func(t *testing.T) {
			requireErrorEnvelope(t, append(args, "--json"), "error")
		})
	}
}

// TestJSONEnvelopeDirty pins exit 4 / "dirty" for every command gated by
// requireClean — mutating and preview forms alike — when the working tree has
// uncommitted changes.
func TestJSONEnvelopeDirty(t *testing.T) {
	cases := [][]string{
		{"restack"},
		{"restack", "--all"},
		{"restack", "--dry-run"},
		{"fold"},
		{"fold", "--dry-run"},
		{"squash"},
		{"squash", "--dry-run"},
		{"onto", "feat-a"},
		{"onto", "feat-a", "--dry-run"},
		{"delete", "feat-b"},
		{"delete", "feat-b", "--dry-run"},
		{"sync", "--dry-run"},
	}
	for _, args := range cases {
		t.Run(jsonJoin(args), func(t *testing.T) {
			newRepo(t)
			mustInit(t)
			mustCreate(t, "feat-a", "f.txt", "A\n", "a")
			mustCreate(t, "feat-b", "f.txt", "A\nB\n", "b")
			write(t, "f.txt", "A\nB\ndirty\n") // uncommitted change
			requireErrorEnvelope(t, append(args, "--json"), "dirty")
		})
	}
}

// TestJSONEnvelopeLocked pins the lock-asymmetry contract: under a held repo
// lock every mutating command exits 5 / "locked", while read-only commands —
// status, log, validate, and the --dry-run previews — still succeed.
func TestJSONEnvelopeLocked(t *testing.T) {
	newRepo(t)
	mustInit(t)
	mustCreate(t, "feat-a", "f.txt", "A\n", "a")
	mustCreate(t, "feat-b", "f.txt", "A\nB\n", "b") // so fold --dry-run has a foldable branch
	release, err := stack.Lock()
	if err != nil {
		t.Fatalf("Lock: %v", err)
	}
	defer release()

	for _, args := range [][]string{
		{"create", "x"},
		{"restack"},
		{"modify"},
		{"delete", "feat-a"},
		{"sync"},
		{"submit"},
		{"track", "x"},
		{"untrack", "feat-a"},
		{"undo"},
		{"abort"},
		{"continue"},
		{"repair"},
		{"worktree", "x"},
		{"worktree", "rm", "x"},
	} {
		requireErrorEnvelope(t, append(args, "--json"), "locked")
	}

	// Read-only commands bypass the lock: they must still run.
	for _, args := range [][]string{
		{"status"},
		{"log"},
		{"validate"},
		{"restack", "--dry-run"},
		{"fold", "--dry-run"},
		{"sync", "--dry-run"},
	} {
		var code int
		var stdout string
		stderr := executeCapturingOutput(t, append(args, "--json"), &code, &stdout)
		if code != 0 {
			t.Fatalf("read-only Execute(%v --json) under lock = %d, want 0; stderr:\n%s", args, code, stderr)
		}
	}
}

// TestJSONEnvelopeConflictFields proves the structured conflict fields
// (branch/onto) survive the whole pipeline — engine ConflictError → dispatcher
// → stderr — on both the mutate path and continue's separate lock path.
func TestJSONEnvelopeConflictFields(t *testing.T) {
	newRepo(t)
	mustInit(t)
	mustCreate(t, "feat-a", "f.txt", "A\n", "a")
	mustCreate(t, "feat-b", "f.txt", "A\nB\n", "b")
	mustCheckout(t, "feat-a")
	write(t, "f.txt", "X\n") // amending feat-a conflicts with feat-b's edit

	// mutate path: Modify's cascade rebases feat-b onto the amended feat-a.
	env := requireErrorEnvelope(t, []string{"modify", "-a", "--json"}, "conflict")
	if env.Error.Branch != "feat-b" || env.Error.Onto != "feat-a" {
		t.Fatalf("conflict envelope fields = %q/%q, want feat-b/feat-a", env.Error.Branch, env.Error.Onto)
	}

	// continue path (lockAndLoad, not mutateState): re-stalling on the still-
	// unresolved conflict must carry the same structured fields.
	env = requireErrorEnvelope(t, []string{"continue", "--json"}, "conflict")
	if env.Error.Branch != "feat-b" || env.Error.Onto != "feat-a" {
		t.Fatalf("continue conflict envelope fields = %q/%q, want feat-b/feat-a", env.Error.Branch, env.Error.Onto)
	}
}
