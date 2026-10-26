# vitals

A tiny, read-only system monitor for one machine. It's built for a Mac Mini, and it works on Linux too.

`vitals` is a single Go binary that reports the vital statistics of the host it runs on: CPU (with Apple Silicon's performance and efficiency cores), memory (with macOS memory pressure, wired, and compressed), disks, network, and, on Linux, temperatures. It serves them as an HTML dashboard that works with JavaScript off, JSON, plain text that reads well at 80 columns, and Prometheus metrics. It has no dependencies to install and no config file. It listens on loopback by default, and aims to stay under 20 MB of memory and 1% of one core.

> **Status: first light.** It runs and serves the host group (uptime, boot time, load, OS and kernel) CPU usage (total and per core, with the P/E split on Apple Silicon), memory and swap (with wired, compressed, and memory pressure on macOS), per-interface network totals and rates, per-mount disk usage with each mount's device read and write rates, and, on Linux, hwmon temperatures, as JSON and as a dashboard at `/` on macOS and Linux. The text and Prometheus formats are next, one reviewed pull request at a time. See [`docs/ROADMAP.md`](docs/ROADMAP.md).

## Running it

```sh
go run .                      # listens on 127.0.0.1:9110
open http://localhost:9110/   # the dashboard
curl localhost:9110/api/v1/stats?pretty=1
```

| Endpoint | Returns |
| --- | --- |
| `/` | the dashboard: collapsible cards and a light/dark/auto toggle, both remembered if JavaScript is on, fully usable without it; reloads every sample (`?refresh=N` seconds, `?refresh=0` to stop); `503` before the first sample |
| `/api/v1/stats` | the latest sample as JSON (`?pretty=1` to indent); `503` before the first sample |
| `/api/v1/history` | every buffered sample, oldest first; `?since=<unix seconds>` keeps those taken after it |
| `/metrics` | the latest sample for a Prometheus scrape (exposition format 0.0.4, `vitals_` names, ratios 0 to 1); `503` before the first sample |
| `/healthz` | `ok` once the first sample exists, `503 no sample yet` before |

Flags: `-listen` (default `127.0.0.1:9110`), `-interval` (`2s`, at least `1s`), `-history` (`15m`), `-disk-exclude` and `-net-exclude` (regexes that replace the built-in lists), `-log-level` (`debug`, `info`, `warn`, `error`), and `-version`. A bad value exits with status 2. There is no authentication: binding anything but loopback logs a warning.

## Development

```sh
go mod download
make run
make check     # everything CI runs; run before pushing
make shim-check  # on a Mac: lint, analyze, and sanitize the C shim (part of check)
make golden    # regenerate renderer golden files; review the diff
```

These need Go 1.27, pinned in `go.mod` (with `GOTOOLCHAIN=auto`, the default, any recent Go fetches it). Building the macOS binary needs a Mac with the Xcode Command Line Tools, because the darwin collector uses cgo against system frameworks. The Linux binaries are static and build anywhere.

## How it's built

Most changes are written by Claude and land as one reviewed pull request per night. [`docs/ROADMAP.md`](docs/ROADMAP.md) is the backlog that drives it, [`docs/PLAN.md`](docs/PLAN.md) is the design (from the original [`docs/SPEC.md`](docs/SPEC.md)), and [`CLAUDE.md`](CLAUDE.md) holds the priorities everything is judged by.

## Licence

MIT. See [`LICENSE`](LICENSE).
