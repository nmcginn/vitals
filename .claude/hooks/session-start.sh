#!/usr/bin/env bash
# Prepares a Claude Code cloud session for vitals: fetches the Go toolchain
# pinned in go.mod and warms the pinned staticcheck, so the first `make check`
# doesn't stall on downloads. Local sessions are left alone.
set -euo pipefail

[ "${CLAUDE_CODE_REMOTE:-}" = "true" ] || exit 0
cd "${CLAUDE_PROJECT_DIR:-$(dirname "$0")/../..}"

# Before M0 there is no go.mod; nothing to install yet.
[ -f go.mod ] || exit 0

# GOTOOLCHAIN=auto (the default) fetches the toolchain go.mod pins.
go mod download

# Warm the pinned staticcheck so the first `make check` doesn't pay for it.
if grep -q '^staticcheck:' Makefile 2>/dev/null; then
  make --no-print-directory -s staticcheck >/dev/null 2>&1 || true
fi
