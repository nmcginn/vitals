# Pinned by version so CI and sessions agree; run with `go run` so go.mod stays
# free of requirements (decision 1).
STATICCHECK_VERSION := v0.8.2

VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT  ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
DATE    ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
# Expand each once, so dist-check compares against what the build injected.
VERSION := $(VERSION)
COMMIT  := $(COMMIT)
DATE    := $(DATE)
LDFLAGS := -s -w -X main.version=$(VERSION) -X main.commit=$(COMMIT) -X main.date=$(DATE)

# The oldest macOS the darwin binary runs on. 13.0 is Go 1.27's own floor, so
# nothing older could run it anyway (decision 38). Exported so every cgo
# compile, the shim's clang passes and the tests included, targets the same
# release and warns about anything newer.
export MACOSX_DEPLOYMENT_TARGET := 13.0

.PHONY: help run fmt-check vet staticcheck test budget soak shim-check golden build-linux build-darwin build dist-check check

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

# The allocation tests skip themselves under -race, where sync.Pool drops items
# at random and the counts measure the detector, so they run again without it.
budget: ## Allocation and growth tests, without the race detector
	go test -count=1 -run 'Allocations|DoesNotGrow' ./...

SOAK_MINUTES ?= 5

soak: ## Run at the defaults for SOAK_MINUTES (5), reporting RSS and CPU against the budget
	contrib/soak.sh $(SOAK_MINUTES)

# The shim gets its own clang pass because cgo compiles its generated C with the
# same CFLAGS, and that code fails -Werror. The warnings are listed rather than
# -Weverything, which grows with every clang release and would break the build on
# an Xcode update. `go test -asan` doesn't support darwin, so the sanitizers go in
# through CGO_CFLAGS (decision 19).
SHIM := internal/collector/darwin_shim.c
SHIM_WARNINGS := -std=c11 -Wall -Wextra -Wpedantic -Wconversion -Wshadow -Wstrict-prototypes -Wmissing-prototypes -Wunguarded-availability -Werror
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

# The darwin binary needs cgo against the macOS SDK, so it builds only on a Mac.
build-darwin: ## The darwin/arm64 binary into dist/ (Mac only)
	@if [ "$$(uname -s)" != Darwin ]; then echo "build-darwin: skipped, dist/vitals-darwin-arm64 builds only on a Mac (it needs cgo and the macOS SDK)"; exit 0; fi; \
	set -x; mkdir -p dist; \
	CGO_ENABLED=1 GOOS=darwin GOARCH=arm64 go build -trimpath -ldflags "$(LDFLAGS)" -o dist/vitals-darwin-arm64 .

build: build-linux build-darwin ## Release binaries into dist/

# Checks the release binaries, not the code: the Linux ones are static, the
# darwin one links only system libraries (priority 2), the service files parse
# without warnings (systemd-analyze exits 0 on unknown keys, so its output counts),
# and the binary for this machine reports the version it was built with.
dist-check: build ## Check the release binaries and contrib/ files
	@set -e; \
	for b in dist/vitals-linux-amd64 dist/vitals-linux-arm64; do \
		file -b $$b | grep -q 'statically linked' || { echo "$$b is not statically linked: $$(file -b $$b)"; exit 1; }; \
		echo "$$b: statically linked"; \
	done; \
	if [ "$$(uname -s)" = Darwin ]; then \
		bad=$$(otool -L dist/vitals-darwin-arm64 | tail -n +2 | awk '{print $$1}' | grep -v -e '^/usr/lib/' -e '^/System/Library/' || true); \
		if [ -n "$$bad" ]; then echo "dist/vitals-darwin-arm64 links outside /usr/lib and /System/Library:"; echo "$$bad"; exit 1; fi; \
		echo "dist/vitals-darwin-arm64: links only /usr/lib and /System/Library"; \
		plutil -lint contrib/com.vitals.plist; \
	else echo "dist-check: otool and plutil skipped, they run only on a Mac"; fi; \
	if command -v systemd-analyze >/dev/null; then \
		tmp=$$(mktemp -d); trap 'rm -rf "$$tmp"' EXIT; \
		sed "s|^ExecStart=/usr/local/bin/vitals|ExecStart=$$PWD/dist/vitals-linux-amd64|" contrib/vitals.service > "$$tmp/vitals.service"; \
		out=$$(systemd-analyze verify "$$tmp/vitals.service" 2>&1) || { echo "$$out"; exit 1; }; \
		if echo "$$out" | grep -q vitals.service; then echo "systemd-analyze warned about contrib/vitals.service:"; echo "$$out"; exit 1; fi; \
		echo "contrib/vitals.service: systemd-analyze verify passed"; \
	else echo "dist-check: systemd-analyze skipped, not installed"; fi; \
	native=dist/vitals-$$(go env GOOS)-$$(go env GOARCH); \
	out=$$($$native -version); echo "$$native -version: $$out"; \
	case "$$out" in *"$(VERSION)"*"$(COMMIT)"*"$(DATE)"*) ;; *) echo "$$native -version doesn't report $(VERSION), $(COMMIT), and $(DATE)"; exit 1;; esac

check: fmt-check vet staticcheck test budget shim-check dist-check ## Everything CI runs
