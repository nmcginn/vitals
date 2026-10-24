# Pinned by version so CI and sessions agree; run with `go run` so go.mod stays
# free of requirements (decision 1).
STATICCHECK_VERSION := v0.8.2

VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT  ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
DATE    ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS := -s -w -X main.version=$(VERSION) -X main.commit=$(COMMIT) -X main.date=$(DATE)

.PHONY: help run fmt-check vet staticcheck test shim-check golden build-linux check

help: ## List the targets
	@grep -E '^[a-z-]+:.*##' $(MAKEFILE_LIST) | sed 's/:.*## /\t/'

run: ## Run locally at debug log level
	go run . -log-level debug

fmt-check: ## Fail if any file needs gofmt
	@out=$$(gofmt -l .); if [ -n "$$out" ]; then echo "gofmt needed on:"; echo "$$out"; exit 1; fi

vet: ## go vet
	go vet ./...

# Build staticcheck with the toolchain go.mod pins: left alone it switches to the
# toolchain it was released with (go1.26) and can't load Go 1.27's standard library.
staticcheck: ## staticcheck, pinned
	GOTOOLCHAIN=$$(go env GOVERSION) go run honnef.co/go/tools/cmd/staticcheck@$(STATICCHECK_VERSION) ./...

test: ## Tests with the race detector
	go test -race ./...

# The shim gets its own clang pass because cgo compiles its generated C with the
# same CFLAGS, and that code fails -Werror. The warnings are listed rather than
# -Weverything, which grows with every clang release and would break the build on
# an Xcode update. `go test -asan` doesn't support darwin, so the sanitizers go in
# through CGO_CFLAGS (decision 19).
SHIM := internal/collector/darwin_shim.c
SHIM_WARNINGS := -std=c11 -Wall -Wextra -Wpedantic -Wconversion -Wshadow -Wstrict-prototypes -Wmissing-prototypes -Werror
SHIM_SANITIZE := -fsanitize=address,undefined -fno-sanitize-recover=all -fno-omit-frame-pointer

shim-check: ## Lint, analyze, and sanitize the darwin C shim (skipped off a Mac)
	@if [ "$$(uname -s)" != Darwin ]; then echo "shim-check: skipped, the shim builds only on a Mac"; exit 0; fi; \
	set -ex; \
	clang -fsyntax-only $(SHIM_WARNINGS) $(SHIM); \
	clang --analyze -Xclang -analyzer-werror -o /dev/null $(SHIM); \
	CGO_ENABLED=1 CGO_CFLAGS="-O1 -g $(SHIM_SANITIZE)" CGO_LDFLAGS="$(SHIM_SANITIZE)" go test -count=1 ./internal/collector

golden: ## Regenerate renderer golden files; review the diff before committing
	go test ./internal/render -update

build-linux: ## Static Linux binaries into dist/
	@mkdir -p dist
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags "$(LDFLAGS)" -o dist/vitals-linux-amd64 .
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -trimpath -ldflags "$(LDFLAGS)" -o dist/vitals-linux-arm64 .

check: fmt-check vet staticcheck test shim-check build-linux ## Everything CI runs
