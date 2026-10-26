# Architecture

## Shape

*Intended, with these as-built: `main.go`, `Makefile`, `collector.go`, `darwin.go`, `nocgo_darwin.go`, `darwin_shim.c/.h`, `darwin_conv.go`, `linux.go`, `procfs.go` (host, CPU, memory, network, disks, and temperatures), `filter.go` (disks and interfaces), `internal/sampler/` (`sampler.go`, `snapshot.go`, `store.go`), `internal/server/server.go` (`/`, `/healthz`, `/api/v1/stats`, `/api/v1/history`, `/metrics`), `render/json.go` (`JSON`, `History`), `render/prom.go` (`Prom`), and `render/html.go`, `sparkline.go`, and `assets/` (the dashboard). Not yet: the text renderer, negotiation, and `contrib/`. Update in every PR that adds, moves, or removes a module.*

The complete JSON shape is fixed by the golden file [`internal/render/testdata/darwin_full.json`](../internal/render/testdata/darwin_full.json) (decision 13).

```
main.go                       flags, slog, net.Listen, http.Server, signals, graceful shutdown
internal/collector/
  collector.go                Collector interface; Raw (counters + gauges per group, per-group errors); HostInfo
  darwin.go                   //go:build darwin — cgo: calls the shim, hands C structs to darwin_conv.go
  nocgo_darwin.go             darwin without cgo: fails the build with a message naming the fix
  darwin_shim.c / .h          Mach, sysctl, IOKit calls; plain C structs out; no logic; `make shim-check` lints it
  darwin_conv.go              untagged — pure conversion of shim output to Raw (P/E tagging, pressure, memory formula)
  linux.go                    //go:build linux — wires os.DirFS("/") and syscall.Statfs into procfs.go
  procfs.go                   untagged — /proc and /sys parsers over fs.FS
  filter.go                   default disk / interface exclude lists; -disk-exclude / -net-exclude
  testdata/                   captured /proc and /sys trees; darwin struct fixtures
internal/sampler/
  sampler.go                  ticker loop, 80% timeout, skip-on-overrun, rate-limited group error logs
  snapshot.go                 Snapshot types; build(): all delta and rate math
  store.go                    ring buffer, Store (latest + copied history), Capacity()
internal/server/              New(store, opts) http.Handler; routes; negotiation; common headers
internal/render/
  json.go text.go prom.go html.go   one pure renderer per format
  sparkline.go                SVG path generation from history
  assets/                     dashboard.tmpl, style.css (go:embed)
  testdata/                   golden files (regenerate with `make golden`)
contrib/                      com.vitals.plist (launchd), vitals.service (systemd)
Makefile
```

## The rule

**Collection happens in exactly one place. Everything else is a pure function of a `Snapshot`.**

- **collector** talks to the OS and nothing else. It returns raw cumulative counters and instantaneous gauges, and never a rate, a percentage of change, or a formatted string. On Linux, every read goes through an `fs.FS` and an injected `statfs`. On darwin, every read goes through the C shim, and the shim's output is converted by untagged pure Go. It doesn't import `sampler`, `server`, or `render`.
- **sampler** owns the only goroutine that calls `Collect`. It holds the previous `Raw`, does all delta and rate math against an injected clock, builds immutable snapshots, and publishes them to the `Store`. It makes no OS calls and doesn't import `net/http`.
- **render** turns a snapshot (and, for the dashboard, history) into bytes. It has no clock, no store, no `net/http`, and no globals beyond embedded assets. Same input, same bytes, which is what makes golden files work.
- **server** maps requests to a format and a renderer, reads the store, and sets headers. It never calls the collector, so a request can't trigger collection. It knows nothing about sockets.
- **main.go** is the only place that touches sockets, signals, `os.Exit`, and flag parsing.

This boundary is load-bearing. Rate math, wraparound, first-sample nulls, every renderer, negotiation, and every Linux parser are reachable from a test on any OS, with no live machine state, no network, and no Mac. The only code that needs real hardware is the thin cgo layer, and the macOS CI job covers that (decision 5).

## Testing

Run `make check`. The suite needs no credentials and no network, and that is a property to defend rather than a coincidence. Darwin-tagged tests run only on macOS, in CI's `macos` job.

- **Sampler:** a fake collector with scripted counters and a fake clock covers rates over actual elapsed time, first-sample nulls, backwards counters giving null, skipped overrun ticks, partial group failure, and ring eviction. Run under `-race`.
- **Linux collector:** parsers over `fstest.MapFS` or `testdata/` trees, captured from real machines, including awkward ones (escaped mount points, missing `MemAvailable`, arm64 cpuinfo, sensors returning errors).
- **Darwin conversion:** untagged unit tests over captured struct values, covering P/E tagging, the memory formula, pressure mapping, and boottime and loadavg conversion.
- **Darwin integration:** `//go:build darwin`. It calls `Collect` twice and checks invariants (totals > 0, percentages within 0 to 100, cumulative counters non-decreasing, P + E = total). It never asserts specific values, so it holds on a CI VM.
- **Renderers:** golden files for JSON, HTML, text, and Prometheus from fixed snapshots, regenerated by `make golden`. Each golden diff is reviewed as part of the PR. There are property tests for the 50 KB page limit, the one hashed theme script and no external URL, 80 columns, and the exposition grammar.
- **Server:** `httptest` tests for routes, `405`/`Allow`, table-driven negotiation and `406`, `Vary`, headers, `HEAD`, and `/healthz` before and after the first sample.
- **main:** flag validation (exit 2), and one test that starts the built binary on `127.0.0.1:0` and waits for `/healthz`.

## Errors

- Collectors return per-group errors inside `Raw`. A group failure never fails the whole sample. `Collect` returns a non-nil error only when nothing at all could be collected.
- The sampler copies group errors into `Snapshot.Errors` (`map[group]string`), and logs each failing group at most once per minute via `slog`.
- Messages say what failed and where: `memory: reading /proc/meminfo: MemAvailable missing (kernel older than 3.14?)`. They never contain environment variables, usernames, or command lines.
- Renderers show errors in place. JSON has `errors`, the dashboard's card for the failing group says what failed, Prometheus sets `vitals_collector_error{group}`, and text prints a line.
- Startup errors (a bad flag, a failed listen) exit with a message naming the flag or address. Bad flags exit with status 2.
