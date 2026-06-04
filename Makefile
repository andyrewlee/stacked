BINARY := st
VERSION := $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -X stacked/cmd.version=$(VERSION)

# Coverage gate threshold (percent). Overridable: `make cover COVERAGE_MIN=80`.
COVERAGE_MIN ?= 75

# `make ci` is the single source of truth for the closed feedback loop.
.DEFAULT_GOAL := ci

.PHONY: ci build install fmt fmt-check vet lint test test-fast e2e cover hooks clean release snapshot

# Full local gate: mirrors .github/workflows/ci.yml. Fails fast, in order.
# `cover` runs the whole suite once (race + combined in-process/e2e coverage),
# so ci does not run the tests three times.
ci: fmt-check lint vet build cover

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

lint:
	golangci-lint run ./...

# Sub-second inner loop for engine work: the pure stack logic over the fake git,
# no race instrumentation, no real-git spawning, no e2e. Hit this constantly.
test-fast:
	go test ./internal/... -count=1

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

# Cut a release from the current git tag with GoReleaser (needs GITHUB_TOKEN).
release:
	goreleaser release --clean

# Build release artifacts locally without publishing (dry run).
snapshot:
	goreleaser build --snapshot --clean
