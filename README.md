# vitals

A tiny, read-only system monitor for one machine. It's built for a Mac Mini, and it works on Linux too.

`vitals` is a single Go binary that reports the vital statistics of the host it runs on: CPU (with Apple Silicon's performance and efficiency cores), memory (with macOS memory pressure, wired, and compressed), disks, network, and, on Linux, temperatures. It serves them as a JavaScript-free HTML dashboard, JSON, plain text that reads well at 80 columns, and Prometheus metrics. It has no dependencies to install and no config file. It listens on loopback by default, and aims to stay under 20 MB of memory and 1% of one core.

> **Status: bootstrapping.** The toolchain, CI, and `-version` exist; the metrics are being built one reviewed pull request at a time. See [`docs/ROADMAP.md`](docs/ROADMAP.md).

## Development

```sh
go mod download
make run
make check     # everything CI runs; run before pushing
```

These need Go 1.27, pinned in `go.mod` (with `GOTOOLCHAIN=auto`, the default, any recent Go fetches it). Building the macOS binary needs a Mac with the Xcode Command Line Tools, because the darwin collector uses cgo against system frameworks. The Linux binaries are static and build anywhere.

## How it's built

Most changes are written by Claude and land as one reviewed pull request per night. [`docs/ROADMAP.md`](docs/ROADMAP.md) is the backlog that drives it, [`docs/PLAN.md`](docs/PLAN.md) is the design (from the original [`docs/SPEC.md`](docs/SPEC.md)), and [`CLAUDE.md`](CLAUDE.md) holds the priorities everything is judged by.

## Licence

MIT. See [`LICENSE`](LICENSE).
