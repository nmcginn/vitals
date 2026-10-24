# Pinned by version so CI and sessions agree; run with `go run` so go.mod stays
# free of requirements (decision 1).
STATICCHECK_VERSION := v0.8.2

VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT  ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
DATE    ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS := -s -w -X main.version=$(VERSION) -X main.commit=$(COMMIT) -X main.date=$(DATE)

.PHONY: help run fmt-check vet staticcheck test golden build-linux check

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

golden: ## Regenerate renderer golden files; review the diff before committing
	go test ./internal/render -update

build-linux: ## Static Linux binaries into dist/
	@mkdir -p dist
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags "$(LDFLAGS)" -o dist/vitals-linux-amd64 .
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -trimpath -ldflags "$(LDFLAGS)" -o dist/vitals-linux-arm64 .

check: fmt-check vet staticcheck test build-linux ## Everything CI runs
