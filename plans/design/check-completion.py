#!/usr/bin/env python3
"""Validate the design-017 branch-completion contract artifacts.

Design-time consistency checks only: fixtures vs. acceptance doc vs. the
design doc's invariants. Runtime truth is the future implementation tests'
job, not this script's.
"""
import json
import sys
from pathlib import Path

HERE = Path(__file__).resolve().parent
DESIGN = HERE / "017-branch-completion.md"
EXAMPLES = HERE / "017-completion-cases.json"
ACCEPTANCE = HERE / "017-completion-acceptance.md"

KNOWN_COMMANDS = {"checkout", "co", "onto", "move", "track", "worktree", "wt"}

REQUIRED_SCENARIOS = {
    "checkout_tracked_and_trunk", "checkout_alias_co",
    "onto_excludes_moving_subtree", "track_untracked_only",
    "track_parent_flag_value", "double_dash_terminator",
    "worktree_create_candidates", "worktree_rm_owners",
    "punctuation_and_unicode", "detached_head", "no_repository",
    "uninitialized_state", "future_state_schema",
    "thousands_of_branches",
}

SILENT_SCENARIOS = {"detached_head", "no_repository", "uninitialized_state",
                    "future_state_schema", "thousands_of_branches",
                    "punctuation_and_unicode"}

failures = []


def fail(msg):
    failures.append(msg)


def main():
    for p in (DESIGN, EXAMPLES, ACCEPTANCE):
        if not p.exists():
            fail(f"missing {p.name}")
    if not EXAMPLES.exists():
        return report()

    design = DESIGN.read_text() if DESIGN.exists() else ""
    acc = ACCEPTANCE.read_text() if ACCEPTANCE.exists() else ""

    for heading in ("## Current contract", "## Candidate policy",
                    "## Endpoint protocol", "## Shell integration",
                    "## Performance and failures", "## Examples",
                    "## Acceptance", "## Deferred work"):
        if heading not in design:
            fail(f"design doc missing heading {heading!r}")
    for term in ("argv", "newline", "detached", "uninitialized", "future",
                 "stderr", "bash", "zsh", "fish"):
        if term not in design:
            fail(f"design doc missing required term {term!r}")

    try:
        scenarios = json.loads(EXAMPLES.read_text())
    except json.JSONDecodeError as e:
        fail(f"examples JSON invalid: {e}")
        return report()
    if not isinstance(scenarios, list):
        fail("examples root must be a list")
        return report()

    names = {s.get("scenario") for s in scenarios if isinstance(s, dict)}
    for req in REQUIRED_SCENARIOS - names:
        fail(f"missing scenario {req!r}")
    for extra in names - REQUIRED_SCENARIOS:
        fail(f"unregistered scenario {extra!r}")

    for s in scenarios:
        n = s.get("scenario", "<unnamed>")
        for key in ("command", "words", "cursor", "setup", "expected",
                    "proposed"):
            if key not in s:
                fail(f"{n}: missing key {key!r}")
        cmd = s.get("command")
        if cmd not in KNOWN_COMMANDS:
            fail(f"{n}: unknown command/alias {cmd!r}")
        words = s.get("words", [])
        if not isinstance(words, list) or len(words) < 2 or words[0] != "st":
            fail(f"{n}: words must start with ['st', <cmd>, ...]")
        if isinstance(words, list) and len(words) > 1 and words[1] != cmd:
            fail(f"{n}: words[1] {words[1]!r} must equal command {cmd!r}")
        cur = s.get("cursor")
        if not isinstance(cur, int) or cur < 1 or cur > len(words):
            fail(f"{n}: cursor {cur!r} out of range for words")
        exp = s.get("expected", [])
        if not isinstance(exp, list):
            fail(f"{n}: expected must be a list")
        elif len(exp) > 1 and all(isinstance(x, str) for x in exp):
            if exp != sorted(set(exp)):
                fail(f"{n}: expected must be sorted and unique")
        if n in SILENT_SCENARIOS:
            mnh = s.get("mustNotHappen", [])
            if not any("stderr" in str(m) or "network" in str(m) or
                       "evaluation" in str(m) or "subprocess" in str(m)
                       for m in mnh):
                fail(f"{n}: mustNotHappen must assert silence/inertness")
        for p in s.get("proposed", []):
            if not isinstance(p, str) or not p:
                fail(f"{n}: malformed proposed entry {p!r}")

    for n in sorted(names):
        if n not in acc:
            fail(f"scenario {n!r} not covered in acceptance doc")

    report()


def report():
    if failures:
        print("check-completion: FAIL")
        for f in failures:
            print(f"  - {f}")
        sys.exit(1)
    print("check-completion: OK")


if __name__ == "__main__":
    main()
