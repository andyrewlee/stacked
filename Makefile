BINARY := st

.DEFAULT_GOAL := ci
.PHONY: ci build fmt fmt-check vet test clean

# Basic gate: formatting, vet, build, and tests. (Hardened later with strict
# linting, e2e, and coverage once the CLI and test suites exist.)
ci: fmt-check vet build test

build:
	go build ./...

fmt:
	gofmt -w .

fmt-check:
	@out=$$(gofmt -l .); \
	if [ -n "$$out" ]; then echo "gofmt needed on:"; echo "$$out"; exit 1; fi

vet:
	go vet ./...

test:
	go test ./... -count=1

clean:
	rm -f $(BINARY) cover.out
