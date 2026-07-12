BINARY := st
VERSION := $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -X github.com/andyrewlee/stacked/cmd.version=$(VERSION)

# Coverage gate threshold (percent). Overridable: `make cover COVERAGE_MIN=80`.
COVERAGE_MIN ?= 75

# golangci-lint is an external binary, never a go.mod dependency. v2 is required:
# .golangci.yml uses the v2 schema and its bundled gofumpt formatter. Keep this
# in sync with the version pinned in .github/workflows/ci.yml.
GOLANGCI_VERSION := v2.12.2

# `make ci` is the single source of truth for the closed feedback loop.
.DEFAULT_GOAL := ci

.PHONY: ci build install fmt fmt-check vet vet-cross lint check-deps check-lint-version check-go-version golden test test-fast e2e cover hooks clean release snapshot

# Full local gate: mirrors .github/workflows/ci.yml. Fails fast, in order. The
# Go-toolchain-only steps (vet/vet-cross/build) run before lint, so a missing or
# wrong golangci-lint never hides a compile/vet failure; lint still precedes the
# slow `cover` step. `cover` runs the whole suite once (race + combined
# in-process/e2e coverage), so ci does not run the tests three times.
ci: check-deps check-lint-version check-go-version fmt-check vet vet-cross build lint cover

build:
	go build -ldflags "$(LDFLAGS)" -o $(BINARY) ./cmd/st

install:
	go install -ldflags "$(LDFLAGS)" ./cmd/st

fmt:
	gofmt -w .

fmt-check:
	@out=$$(gofmt -l .); \
	if [ -n "$$out" ]; then \
		echo "gofmt needs to be run on:"; \
		echo "$$out"; \
		exit 1; \
	fi

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
# (and therefore by CI and the pre-push hook), so a new dependency can never land
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

# The lint version is pinned in four hand-synced places. Enforce agreement so
# local make ci, CI, and contributor docs cannot silently drift apart.
check-lint-version:
	@ok=1; \
	for f in .github/workflows/ci.yml README.md CONTRIBUTING.md; do \
		case $$f in \
			.github/workflows/ci.yml) \
				pins=$$(sed -nE 's/^[[:space:]]*version:[[:space:]]*(v[0-9]+\.[0-9]+\.[0-9]+)[[:space:]]*$$/\1/p' $$f);; \
			*) \
				pins=$$(sed -nE 's/.*golangci-lint@((v[0-9]+\.[0-9]+\.[0-9]+)).*/\1/p' $$f);; \
		esac; \
		if [ "$$pins" != "$(GOLANGCI_VERSION)" ]; then \
			echo "$$f pins golangci-lint '$${pins:-<none>}' (want $(GOLANGCI_VERSION) from Makefile)"; \
			ok=0; \
		fi; \
	done; \
	[ $$ok -eq 1 ] || exit 1; \
	echo "lint pin: $(GOLANGCI_VERSION) consistent across Makefile, ci.yml, README, CONTRIBUTING"

# The Go pin lives in go.mod (source of truth), ci.yml's GO_VERSION env, and
# the README/CONTRIBUTING "Go 1.NN+" prose. Enforce agreement so a toolchain
# bump cannot silently leave CI or the docs behind (the same hazard
# check-lint-version guards for the lint pin).
check-go-version:
	@want=$$(sed -nE 's/^go ([0-9]+[.][0-9]+).*/\1/p' go.mod); \
	ok=1; \
	ci=$$(sed -nE "s/^[[:space:]]*GO_VERSION:[[:space:]]*'([0-9]+[.][0-9]+)'.*/\1/p" .github/workflows/ci.yml); \
	if [ "$$ci" != "$$want" ]; then \
		echo ".github/workflows/ci.yml pins GO_VERSION '$${ci:-<none>}' (want $$want from go.mod)"; \
		ok=0; \
	fi; \
	for f in README.md CONTRIBUTING.md; do \
		pin=$$(sed -nE 's/.*Go ([0-9]+[.][0-9]+)[+].*/\1/p' $$f | head -1); \
		if [ "$$pin" != "$$want" ]; then \
			echo "$$f documents 'Go $${pin:-<none>}+' (want Go $$want+ from go.mod)"; \
			ok=0; \
		fi; \
	done; \
	[ $$ok -eq 1 ] || exit 1; \
	echo "go pin: $$want consistent across go.mod, ci.yml, README, CONTRIBUTING"

# Regenerate golden test fixtures after an intended, reviewed output change.
golden:
	go test ./cmd -run Golden -update

lint:
	@command -v golangci-lint >/dev/null 2>&1 || { \
		echo "golangci-lint not found on PATH."; \
		echo "install: go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_VERSION)"; \
		exit 1; \
	}
	@golangci-lint version 2>&1 | grep -qE '(version |v)2\.' || { \
		echo "golangci-lint v2 required (have: $$(golangci-lint version 2>&1 | head -1))."; \
		echo "install: go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_VERSION)"; \
		exit 1; \
	}
	golangci-lint run ./...

# Fast inner loop for engine work: the stack engine package (fake-git model tests
# plus fast unit tests), no race instrumentation and no e2e. Sub-second; hit this
# constantly. The slower real-git port tests and the e2e suite run in `make test`
# and `make ci`.
test-fast:
	go test ./internal/stack/... -count=1

# In-process suite with the race detector (cmd + internal); no e2e.
test:
	go test ./cmd/... ./internal/... -race -count=1

# Black-box e2e suite: builds and drives the real binary as a subprocess.
e2e:
	go test ./e2e/... -count=1

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

# Cut a release from the current git tag with GoReleaser (needs GITHUB_TOKEN).
release:
	goreleaser release --clean

# Build release artifacts locally without publishing (dry run).
snapshot:
	goreleaser build --snapshot --clean
