BINARY := st
VERSION := $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -X github.com/andyrewlee/stacked/cmd.version=$(VERSION)

# Coverage gate threshold (percent). Overridable: `make cover COVERAGE_MIN=80`.
COVERAGE_MIN ?= 75

# golangci-lint is an external binary, never a go.mod dependency. v2 is required:
# .golangci.yml uses the v2 schema and its bundled gofumpt formatter. Keep the
# pin in sync with the version documented in README/CONTRIBUTING.
GOLANGCI_VERSION := v2.12.2

# GoReleaser is likewise an external binary (release-time only). The pin's major
# must match the .goreleaser.yaml config schema `version:`.
GORELEASER_VERSION := v2.17.0

# The minimum git version st requires at runtime — enforced by check-git-version
# here and by git.RequireMinVersion in the binary. Keep in sync with the
# README/CONTRIBUTING "Git N.NN+" requirement text.
GIT_MIN_VERSION := 2.17

# `make ci` is the single source of truth for the closed feedback loop.
.DEFAULT_GOAL := ci

.PHONY: ci build install fmt fmt-check vet vet-cross lint check-deps check-lint-version check-go-version check-git-version check-tools check-golangci check-goreleaser-version check-release-version check-release-ready check-install golden test test-fast e2e cover hooks clean release snapshot

# THE gate: there is no remote CI — this Makefile is the whole pipeline.
# Fails fast, in order. The Go-toolchain-only steps (vet/vet-cross/build) run
# before lint, so a missing or wrong golangci-lint never hides a compile/vet
# failure; lint still precedes the slow `cover` step. `cover` runs the whole
# suite once (race + combined in-process/e2e coverage), so ci does not run the
# tests three times. check-install covers the installer legs (install.sh
# syntax, goreleaser schema/asset parity, the minisign decision matrix); its
# optional tools skip loudly unless CI_STRICT=1.
ci: check-deps check-lint-version check-go-version check-git-version check-goreleaser-version check-tools check-shell fmt-check vet vet-cross build lint cover check-install

build:
	go build -ldflags "$(LDFLAGS)" -o $(BINARY) ./cmd/st

install:
	go install -ldflags "$(LDFLAGS)" ./cmd/st

# Formatting runs through golangci-lint's `fmt` so `make fmt` and
# `make fmt-check` apply exactly the formatters the gate enforces (.golangci.yml:
# gofumpt with this module path). Plain gofmt would be a weaker, different
# gate: a file can be gofmt-clean but gofumpt-dirty.
fmt: check-golangci
	golangci-lint fmt

fmt-check: check-golangci
	@golangci-lint fmt --diff || { \
		echo "gofumpt needs to be run (make fmt)"; \
		exit 1; \
	}

vet:
	go vet ./...

# The non-flock lock fallback (lock_other.go, lock_owner_windows.go,
# lock_owner_plan9.go) is excluded from every native build by its build tags,
# so a plain `go vet` never compiles it. Vet the two GOOSes that select those
# files so a breakage cannot land green.
vet-cross:
	GOOS=windows GOARCH=amd64 go vet ./...
	GOOS=plan9 GOARCH=amd64 go vet ./...

# Enforce the project's hardest invariant: the shipped tool stays standard-library
# only. Fail if go.mod declares any dependency or a go.sum appears. Run by `make ci`
# (and therefore by the pre-push hook), so a new dependency can never land
# green.
check-deps:
	@if grep -qE '^require' go.mod; then \
		echo "go.mod declares a require directive; this project must stay standard-library only"; \
		exit 1; \
	fi
	@if [ -f go.sum ]; then \
		echo "go.sum exists; this project must have no module dependencies"; \
		exit 1; \
	fi
	@echo "deps: standard library only"

# The lint version is pinned in three hand-synced places. Enforce agreement so
# the Makefile and contributor docs cannot silently drift apart.
check-lint-version:
	@ok=1; \
	for f in README.md CONTRIBUTING.md; do \
		pins=$$(sed -nE 's/.*golangci-lint@((v[0-9]+\.[0-9]+\.[0-9]+)).*/\1/p' $$f); \
		if [ "$$pins" != "$(GOLANGCI_VERSION)" ]; then \
			echo "$$f pins golangci-lint '$${pins:-<none>}' (want $(GOLANGCI_VERSION) from Makefile)"; \
			ok=0; \
		fi; \
	done; \
	[ $$ok -eq 1 ] || exit 1; \
	echo "lint pin: $(GOLANGCI_VERSION) consistent across Makefile, README, CONTRIBUTING"

# The Go pin lives in go.mod (source of truth) and the README/CONTRIBUTING
# "Go 1.NN+" prose. Enforce agreement so a toolchain bump cannot silently leave
# the docs behind (the same hazard check-lint-version guards for the lint pin).
check-go-version:
	@want=$$(sed -nE 's/^go ([0-9]+[.][0-9]+).*/\1/p' go.mod); \
	ok=1; \
	for f in README.md CONTRIBUTING.md; do \
		pin=$$(sed -nE 's/.*Go ([0-9]+[.][0-9]+)[+].*/\1/p' $$f | head -1); \
		if [ "$$pin" != "$$want" ]; then \
			echo "$$f documents 'Go $${pin:-<none>}+' (want Go $$want+ from go.mod)"; \
			ok=0; \
		fi; \
	done; \
	[ $$ok -eq 1 ] || exit 1; \
	echo "go pin: $$want consistent across go.mod, README, CONTRIBUTING"

# The git floor ($(GIT_MIN_VERSION)) is the documented runtime requirement —
# declared in three hand-synced places (this var, the README/CONTRIBUTING
# "Git N.NN+" prose, and internal/git's MinVersion), each enforced here: the
# installed git must meet it AND every declaration must agree, so a floor bump
# cannot silently leave the docs or the runtime check behind (the same hazard
# check-lint-version/check-go-version guard).
check-git-version:
	@have=$$(git version 2>/dev/null | sed -nE 's/.*git version ([0-9]+)\.([0-9]+)(\.[0-9]+)?.*/\1.\2/p'); \
	if [ -z "$$have" ]; then \
		echo "cannot parse 'git version' output"; \
		exit 1; \
	fi; \
	ok=$$(printf '%s\n%s\n' "$(GIT_MIN_VERSION)" "$$have" | sort -t. -k1,1n -k2,2n | head -1); \
	if [ "$$ok" != "$(GIT_MIN_VERSION)" ]; then \
		echo "git $$have is below the required floor $(GIT_MIN_VERSION)"; \
		exit 1; \
	fi; \
	agree=1; \
	for f in README.md CONTRIBUTING.md; do \
		pin=$$(grep -oE 'Git [0-9]+\.[0-9]+\+' $$f | head -1 | sed -nE 's/Git ([0-9.]+)\+/\1/p'); \
		if [ "$$pin" != "$(GIT_MIN_VERSION)" ]; then \
			echo "$$f documents 'Git $${pin:-<none>}+' (want Git $(GIT_MIN_VERSION)+ from Makefile)"; \
			agree=0; \
		fi; \
	done; \
	code=$$(sed -nE 's/.*MinVersion = \[3\]int{([0-9]+), ([0-9]+), [0-9]+}.*/\1.\2/p' internal/git/git.go | head -1); \
	if [ "$$code" != "$(GIT_MIN_VERSION)" ]; then \
		echo "internal/git MinVersion is '$${code:-<none>}' (want $(GIT_MIN_VERSION) from Makefile)"; \
		agree=0; \
	fi; \
	[ $$agree -eq 1 ] || exit 1; \
	echo "git floor: $$have >= $(GIT_MIN_VERSION); pin agrees across Makefile, README, CONTRIBUTING, internal/git"

# The GoReleaser pin lives in GORELEASER_VERSION (Makefile) — it is the version
# release tooling installs. Its major must match the .goreleaser.yaml config
# schema `version:` (a v2 config needs a v2 tool), and an installed goreleaser
# binary must be exactly the pin (like check-golangci).
check-goreleaser-version:
	@pin=$(GORELEASER_VERSION); major=$${pin#v}; major=$${major%%.*}; \
	cfg=$$(sed -nE 's/^version:[[:space:]]*([0-9]+).*/\1/p' .goreleaser.yaml | head -1); \
	if [ "$$cfg" != "$$major" ]; then \
		echo ".goreleaser.yaml declares config version '$${cfg:-<none>}' but Makefile pins goreleaser $(GORELEASER_VERSION)"; \
		exit 1; \
	fi; \
	if command -v goreleaser >/dev/null 2>&1; then \
		have=$$(goreleaser --version 2>&1 | sed -nE 's/^GitVersion:[[:space:]]*v?([0-9]+\.[0-9]+\.[0-9]+).*/\1/p' | head -1); \
		if [ "v$$have" != "$(GORELEASER_VERSION)" ]; then \
			echo "installed goreleaser '$${have:-<unknown>}' != pin $(GORELEASER_VERSION)"; \
			exit 1; \
		fi; \
		echo "goreleaser pin: $(GORELEASER_VERSION) installed; .goreleaser.yaml schema v$$cfg matches"; \
	else \
		echo "goreleaser pin: $(GORELEASER_VERSION); .goreleaser.yaml schema v$$cfg matches (binary not installed — fine for non-release work)"; \
	fi

# Inventory of every external tool the build/release/install paths touch
# beyond the POSIX coreutils the scripts assume (sed/grep/awk/sort/sh) and the
# go/git toolchains (check-go-version/check-git-version). Each entry is either
# pinned — verified by its own check-* target (golangci-lint, goreleaser) — or
# optional: optional tools gate only the legs that use them (installer checks,
# release signing/verify), which skip loudly when they are absent. Presence is
# reported here so a missing tool surfaces in the gate, not mid-release;
# CI_STRICT=1 makes check-install require goreleaser and minisign.
check-tools:
	@missing=""; \
	for t in curl tar goreleaser minisign shellcheck; do \
		command -v $$t >/dev/null 2>&1 || missing="$$missing $$t"; \
	done; \
	if ! command -v sha256sum >/dev/null 2>&1 && ! command -v shasum >/dev/null 2>&1; then \
		missing="$$missing sha256sum-or-shasum"; \
	fi; \
	if [ -n "$$missing" ]; then \
		echo "optional install/release tools absent:$$missing (their legs skip; CI_STRICT=1 requires goreleaser+minisign+shellcheck)"; \
	else \
		echo "tools: curl, tar, sha256, goreleaser, minisign, shellcheck all present"; \
	fi

# Shellcheck is presence-optional like minisign (its diagnostics aren't
# pin-sensitive for this repo, so no version check): absent skips loudly,
# CI_STRICT=1 requires. `-s sh` pins the POSIX scripts' dialect; the bash
# scripts/hooks run under their own shebangs with -x so sourced-path checks
# work. Findings are fixed or annotated inline — never globally disabled.
check-shell:
	@if command -v shellcheck >/dev/null 2>&1; then \
		shellcheck -s sh install.sh scripts/check-release-ready.sh && \
		shellcheck -x scripts/check-install-assets.sh \
			scripts/check-install-signatures.sh scripts/cover.sh \
			.githooks/pre-commit .githooks/pre-push && \
		echo "shellcheck: 7 scripts clean"; \
	elif [ "$${CI_STRICT:-0}" = "1" ]; then \
		echo "shellcheck required for the shell leg (CI_STRICT=1)"; exit 1; \
	else echo "skip: shellcheck not installed (set CI_STRICT=1 to require)"; fi

# The installer checks local CI runs around the test suite: install.sh syntax,
# the installer↔goreleaser asset-name parity check, and the minisign signature
# decision matrix. goreleaser and minisign are OPTIONAL locally — absent tools
# skip loudly; CI_STRICT=1 (pre-release runs) makes them required.
check-install:
	@sh -n install.sh
	@if command -v goreleaser >/dev/null 2>&1; then \
		goreleaser check && scripts/check-install-assets.sh; \
	elif [ "$${CI_STRICT:-0}" = "1" ]; then \
		echo "goreleaser required for installer checks (CI_STRICT=1)"; exit 1; \
	else echo "skip: goreleaser not installed (set CI_STRICT=1 to require)"; fi
	@if command -v minisign >/dev/null 2>&1; then \
		bash scripts/check-install-signatures.sh; \
	elif [ "$${CI_STRICT:-0}" = "1" ]; then \
		echo "minisign required for signature matrix (CI_STRICT=1)"; exit 1; \
	else echo "skip: minisign not installed (set CI_STRICT=1 to require)"; fi

# Regenerate golden test fixtures after an intended, reviewed output change.
# -timeout 20m defeats Go's default 10m ceiling: a hung regeneration should
# report a timeout, not panic mid-write.
golden:
	go test -timeout 20m ./cmd -run Golden -update

# The lint binary must be exactly $(GOLANGCI_VERSION) — the documented pin.
# A different golangci-lint release ships a different bundled gofumpt and
# different linter diagnostics, so "any v2" could format or lint differently
# than the gate. This preflight is a shared prerequisite of fmt, fmt-check,
# and lint (make runs it once per invocation).
check-golangci:
	@command -v golangci-lint >/dev/null 2>&1 || { \
		echo "golangci-lint not found on PATH."; \
		echo "install: go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_VERSION)"; \
		echo "  (the binary lands in $$(go env GOPATH 2>/dev/null || echo "$${HOME:-~}/go")/bin — ensure it is on PATH)"; \
		exit 1; \
	}
	@have=$$(golangci-lint version --short 2>/dev/null); \
	if [ -z "$$have" ]; then \
		have=$$(golangci-lint version 2>&1 | sed -nE 's/.*version (v?[0-9]+\.[0-9]+\.[0-9]+).*/\1/p' | head -1); \
	fi; \
	if [ "v$${have#v}" != "$(GOLANGCI_VERSION)" ]; then \
		echo "expected golangci-lint $(GOLANGCI_VERSION), got '$${have:-<unknown>}'; install per CONTRIBUTING.md:"; \
		echo "  go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_VERSION)"; \
		exit 1; \
	fi

lint: check-golangci
	golangci-lint run ./...

# Fast inner loop for engine work: the stack engine package (fake-git model tests
# plus fast unit tests), no race instrumentation and no e2e. Sub-second; hit this
# constantly. The slower real-git port tests and the e2e suite run in `make test`
# and `make ci`.
test-fast:
	go test ./internal/stack/... -count=1

# In-process suite with the race detector (cmd + internal); no e2e.
# -timeout 20m defeats Go's default 10m ceiling — the cmd suite already
# approaches it (cover.sh needed the same bump); a hang must report a
# timeout, not die with a bare panic.
test:
	go test -timeout 20m ./cmd/... ./internal/... -race -count=1

# Black-box e2e suite: builds and drives the real binary as a subprocess.
# -timeout 20m for the same reason as `test`.
e2e:
	go test -timeout 20m ./e2e/... -count=1

# Whole suite, once: race-checked in-process tests + e2e, merged coverage, gated.
cover:
	COVERAGE_MIN=$(COVERAGE_MIN) ./scripts/cover.sh

# Install the repo git hooks (fast loop pre-commit, full loop pre-push).
hooks:
	git config core.hooksPath .githooks
	chmod +x .githooks/*

clean:
	rm -f $(BINARY) cover.out
	rm -rf dist

# The release tag must match the compiled-in fallback version, or plain
# `go build` source builds of the tagged tree report the wrong version.
# Takes RELEASE_TAG=vX.Y.Z or derives it from an exact tag on HEAD.
check-release-version:
	@tag=$${RELEASE_TAG:-$$(git describe --tags --exact-match 2>/dev/null)}; \
	if [ -z "$$tag" ]; then echo "no release tag (set RELEASE_TAG=vX.Y.Z or tag HEAD)"; exit 1; fi; \
	want=$${tag#v}; \
	have=$$(sed -nE 's/^const defaultVersion = "([^"]+)"$$/\1/p' cmd/root.go); \
	if [ "$$have" != "$$want" ]; then \
		echo "cmd/root.go defaultVersion is '$$have' but the release tag is '$$tag' - bump the const first"; \
		exit 1; \
	fi; \
	echo "release pin: defaultVersion $$have matches tag $$tag"

# Preflight before publishing: refuses to ship artifacts install.sh cannot
# verify — the embedded minisign public key must be real and the signing
# secret key file present. Releases are cut locally; no CI job gates this.
check-release-ready:
	@sh scripts/check-release-ready.sh

# Cut a release from the current git tag with GoReleaser (needs GITHUB_TOKEN).
release: check-release-version check-release-ready
	goreleaser release --clean

# Build release artifacts locally without publishing (dry run).
snapshot:
	goreleaser build --snapshot --clean
