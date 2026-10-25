# Homelab System Monitor — Specification

*Spec date: Oct 21, 2026. Committed verbatim from the original, except that the author byline is dropped and the embedded diagram, which did not survive export, is redrawn below as text from the surrounding description. Later changes by the maintainer are marked *Amended* with their decision number.*

## Overview and goals

`vitals` (working name) is a single-process, read-only Go web server. It reports the vital statistics of the one host it runs on, as server-rendered HTML, JSON, plain text, or Prometheus text format.

Goals, in priority order:

1. **macOS first.** The primary target is `darwin/arm64` (Apple Silicon Mac Mini). Mac-specific metrics such as memory pressure, per-disk I/O, and later temperatures and power are first-class, not afterthoughts. `linux/amd64` and `linux/arm64` are secondary, best-effort targets; Linux parity is not required.
2. **Single binary, no installed dependencies.** On macOS the binary links only what ships with the OS (`libSystem`, IOKit, CoreFoundation). It is built on a Mac with cgo enabled for darwin. Linux builds use `CGO_ENABLED=0` and are fully static. No config files and no external assets.
3. **Lightweight.** Targets at a 2s sample interval: under 20 MB RSS and under 1% of one core at idle. Serving a request never triggers metric collection.
4. **Read-only.** No endpoint changes state on the host or in the process.
5. **Minimal JavaScript, graceful fallback.** The dashboard is fully functional with JS disabled. A small inline script may add conveniences on top, such as a theme toggle, and never fetches anything. *Amended: decision 34.*
6. **Small and readable.** Roughly 1,000–1,200 lines of Go plus a small C shim for darwin. Standard library only; no third-party Go modules.

## Non-goals

These are out of scope for v1. Do not implement them, and do not add abstractions in anticipation of them, except where noted.

- **Multi-host aggregation.** Each instance reports only its own host. No agents, no fan-out, no central server.
- **Authentication and TLS.** Exposure is controlled by the bind address. A tailnet listener via `tsnet` is planned as future work; the only accommodation now is the handler/listener split described under Architecture.
- **Process lists.** Expensive to collect and the most likely to leak sensitive information.
- **Persistence.** History lives in a fixed-size in-memory ring buffer and is lost on restart. No database, no files written.
- **Alerting, thresholds, or notifications.**
- **Runtime configuration or any write endpoint.**
- **Windows.** Not a target platform.

## Architecture

The process runs three parts: a background sampler that owns all collection, a read-only snapshot store, and an HTTP handler that only renders. Requests never call the collector.

```text
data path (sampler ticker):  Collector.Collect ──▶ Sampler (deltas, rates) ──▶ Snapshot ──▶ Store (latest + ring buffer)
request path (per request):  net/http ──▶ Handler (route, negotiate) ──▶ Store (read only) ──▶ Renderer ──▶ bytes
```

The top row runs on the sampler's ticker. The bottom row runs once per request and stops at the store.

**Handler/listener split.** `internal/server` exposes `New(store *sampler.Store, opts Options) http.Handler` and knows nothing about sockets. `main.go` creates the `net.Listener` and the `http.Server`. This split is the only accommodation for future `tsnet` support.

**Collector.** One interface, implemented per OS with build tags. It returns raw cumulative counters and instantaneous gauges, and computes no rates.

```go
type Collector interface {
    // Collect returns raw counters and gauges. A failure in one group sets
    // that group's error and leaves the others populated.
    Collect(ctx context.Context) (Raw, error)
    Static() HostInfo // hostname, OS, kernel, arch, CPU model: read once at startup
}
```

**Sampler.** One goroutine on a `time.Ticker` at `-interval`. Each tick calls `Collect` with a timeout of 80% of the interval, computes rates against the previous `Raw` using the actual elapsed time, builds an immutable `Snapshot`, and pushes it into a fixed-capacity ring buffer. If a collection overruns, skip the tick; never queue ticks.

**Store.** The ring buffer is guarded by a `sync.RWMutex`, or holds the latest snapshot in an `atomic.Pointer[Snapshot]` with the history under a mutex. Readers get the latest snapshot or a copied history slice. Snapshots are never mutated after publication, so renderers share them without copying.

**Render.** Renderers are pure functions from `Snapshot` (plus history, for the dashboard) to bytes, one per format. The handler picks a renderer from the negotiated format, then sets headers.

## Metrics (v1)

v1 collects six groups. On macOS, collection calls Mach, sysctl, and IOKit directly through a small C shim (`darwin_shim.c`) via cgo; no gopsutil. On Linux it is hand-rolled from `/proc` and `/sys`. Fields that exist on only one platform go in a platform sub-object in JSON (`"darwin": {…}`) and are omitted elsewhere.

| Group | Fields | macOS source (primary) | Linux source (secondary) |
| --- | --- | --- | --- |
| Host | hostname, OS product version, kernel version, arch, uptime (s), boot time, load avg 1/5/15 | `os.Hostname`; sysctl `kern.osproductversion`, `kern.osrelease`, `hw.machine`, `kern.boottime`, `vm.loadavg` | `/proc/sys/kernel/osrelease`, `/proc/uptime`, `/proc/loadavg` |
| CPU | total and per-core usage %, model name; macOS also performance vs efficiency core counts, with per-core % tagged P or E | `host_processor_info(PROCESSOR_CPU_LOAD_INFO)` tick deltas; sysctl `machdep.cpu.brand_string`, `hw.perflevel0.*` / `hw.perflevel1.*` | `/proc/stat` deltas, `/proc/cpuinfo` |
| Memory | total, available, used, swap total/used; macOS also wired, compressed, active, inactive, file-backed, and memory pressure level (normal / warn / critical) | `host_statistics64(HOST_VM_INFO64)`; sysctl `hw.memsize`, `hw.pagesize`, `vm.swapusage`, `kern.memorystatus_vm_pressure_level` | `/proc/meminfo` (used = total − available) |
| Disks | per mount: device, mount point, fs type, total, used, free, used %; read/write bytes/s per physical device | `getfsstat` + `statfs`; IOKit `IOBlockStorageDriver` "Statistics" dictionary for byte counters | `/proc/self/mounts`, `statfs`, `/proc/diskstats` deltas |
| Network | per interface: rx/tx bytes/s, rx/tx packets/s, cumulative totals | sysctl `NET_RT_IFLIST2` (`if_msghdr2` / `if_data64`). Do not use `getifaddrs`, whose `if_data` counters are 32-bit and wrap at 4 GiB | `/proc/net/dev` deltas |
| Temperatures | per sensor: label, °C | Not in v1; see Future work (v1.1) | `/sys/class/hwmon/*` |

Collection rules:

- **Rates come from deltas** between consecutive samples, divided by actual elapsed time (not the configured interval). The first sample has no rates; JSON reports them as `null`, the dashboard as "—".
- **Units in machine formats are raw:** bytes, bytes/s, seconds, percentages as floats 0–100, temperatures in °C. Only the HTML and text renderers humanize (binary units: KiB, MiB, GiB).
- **Default filters:** skip pseudo filesystems (`proc`, `sysfs`, `tmpfs`, `devtmpfs`, `overlay`, `squashfs`, `cgroup*`, etc.) and snap mounts; skip interfaces `lo`, `lo0`, `veth*`, `docker*`, `br-*`, `utun*`, `awdl*`, `llw*`, `bridge*`, `anpi*`. Both lists are overridable by regex flags.
- **Partial failure is normal.** If one group fails, log it at most once per minute, mark that group's error in the snapshot, and still serve every other group.

## HTTP API and content negotiation

Use the standard library `net/http` `ServeMux` with Go 1.22+ method patterns. All endpoints accept only `GET` and `HEAD`; anything else returns `405` with an `Allow` header.

| Endpoint | Default format | Negotiable | Returns |
| --- | --- | --- | --- |
| `/` | HTML | yes | Dashboard: current snapshot plus sparklines from history |
| `/api/v1/stats` | JSON | yes | Current snapshot only |
| `/api/v1/history` | JSON | no | Ring buffer contents, oldest first; optional `?since=<unix seconds>` |
| `/metrics` | Prometheus text | no | Current snapshot in exposition format 0.0.4 |
| `/healthz` | text | no | `ok` with `200` once the first sample exists, `503` before |

Format selection for negotiable endpoints, first match wins:

1. `?format=` query parameter: `html`, `json`, `text`, or `prom`.
2. `Accept` header, honouring q-values: `text/html`, `application/json`, `text/plain`, `application/openmetrics-text` or `text/plain; version=0.0.4` (Prometheus).
3. The endpoint's default. `*/*` and a missing header both mean the default.

An unknown `format` value, or an `Accept` header that matches nothing and has no wildcard, returns `406` with the supported types listed in the body. Every negotiated response sets `Vary: Accept`.

All responses set `Cache-Control: no-store` and a correct `Content-Type` with `charset=utf-8`. JSON is not indented unless `?pretty=1`. Plain text is aligned columns readable in a terminal at 80 columns, so `curl host:port/?format=text` is useful on its own.

JSON snapshot shape (abbreviated; field names are `snake_case`, timestamps RFC 3339 UTC):

```json
{
  "sampled_at": "2026-10-21T14:02:06Z",
  "interval_s": 2,
  "host": {"hostname": "mini", "os": "darwin", "kernel": "25.1.0", "arch": "arm64", "uptime_s": 864000, "load": [0.41, 0.38, 0.35]},
  "cpu": {"usage_pct": 6.2, "per_core_pct": [9.1, 4.0], "cores": 10, "model": "Apple M4", "darwin": {"p_cores": 4, "e_cores": 6, "core_kinds": ["P", "P"]}},
  "memory": {"total_bytes": 17179869184, "available_bytes": 7516192768, "used_bytes": 9663676416, "swap_total_bytes": 0, "swap_used_bytes": 0, "darwin": {"wired_bytes": 2147483648, "compressed_bytes": 536870912, "pressure": "normal"}},
  "disks": [{"mount": "/", "device": "/dev/disk3s1", "fs": "apfs", "total_bytes": 494384795648, "used_bytes": 120259084288, "used_pct": 24.3, "read_bps": 0, "write_bps": 40960}],
  "network": [{"iface": "en0", "rx_bps": 12800, "tx_bps": 3400, "rx_pps": 21, "tx_pps": 14, "rx_total_bytes": 98234111, "tx_total_bytes": 12001221}],
  "temps": null,
  "errors": {}
}
```

Prometheus metric names use the `vitals_` prefix with base units, for example `vitals_cpu_usage_ratio{cpu="total"}`, `vitals_memory_available_bytes`, `vitals_filesystem_used_bytes{mount="/"}`, `vitals_network_receive_bytes_per_second{iface="en0"}`. Write the exposition by hand; do not add the Prometheus client library.

## Dashboard

The dashboard is one server-rendered HTML page, built with `html/template`, under 50 KB, with no external requests. Any JavaScript is minimal, inline, and optional: everything works without it. *Amended: decision 34.*

- **Layout, top to bottom:** a header line (hostname, OS/kernel, uptime, sampled-at time), then cards for CPU, memory, disks, network, and temperatures (omitted when there are none). Single column on narrow screens, a two-column grid from about 900px.
- **Sparklines:** inline SVG generated server-side from the ring buffer. Show a CPU total line, a memory used line, and a combined rx/tx line per network interface. Use a fixed `viewBox`, no axes, and label the current value as text beside the line.
- **Disk usage:** a horizontal bar per mount using a styled `<meter>` or a CSS width. Use colour to reinforce, never as the only signal: show the percentage as text too.
- **Refresh:** `<meta http-equiv="refresh">` set to the sample interval (minimum 2s). `?refresh=0` disables it; `?refresh=N` overrides it. Carry the `refresh` value through any links on the page.
- **Styling:** one embedded stylesheet (via `embed`) inlined into a `<style>` tag, so each page load is one response. Support light and dark through `prefers-color-scheme` using CSS custom properties. Use the system UI font stack for text and a monospace stack with `font-variant-numeric: tabular-nums` for figures, so numbers don't jitter between refreshes.
- **Accessibility:** semantic headings per card, real `<table>` elements for disks and network, and a `<title>` element in every SVG.

## Configuration and security

All configuration is set by command-line flags with safe defaults. There is no config file. Invalid values exit with a clear message and status 2.

| Flag | Default | Meaning |
| --- | --- | --- |
| `-listen` | `127.0.0.1:9110` | Bind address. Set to a Tailscale IP to expose on the tailnet only |
| `-interval` | `2s` | Sample interval; minimum `1s` |
| `-history` | `15m` | Ring buffer window; capacity = history ÷ interval (450 samples at defaults) |
| `-disk-exclude` | built-in list | Regex of mount points or fs types to skip; replaces the default list |
| `-net-exclude` | built-in list | Regex of interface names to skip; replaces the default list |
| `-log-level` | `info` | `debug`, `info`, `warn`, `error` via `log/slog` |
| `-version` | — | Print version, commit, build date, and exit |

Security requirements:

- **Loopback by default.** If `-listen` resolves to a non-loopback address, log one `warn` line at startup saying the dashboard is reachable from the network and has no authentication.
- **Unprivileged.** Must run correctly as a normal user on both platforms. Never require root.
- **Hardened server:** set `ReadHeaderTimeout` 5s, `ReadTimeout` 10s, `WriteTimeout` 10s, `IdleTimeout` 60s, `MaxHeaderBytes` 16 KB. Ignore request bodies.
- **Response headers:** `X-Content-Type-Options: nosniff`, `Referrer-Policy: no-referrer`, and on HTML `Content-Security-Policy: default-src 'none'; style-src 'unsafe-inline'; img-src data:; script-src 'sha256-…'`, where the hash is that of the page's one inline script. *Amended: decision 34.*
- **No secrets** in output: no environment variables, command lines, or usernames.
- **Graceful shutdown** on `SIGINT` and `SIGTERM`: stop the sampler, then `Server.Shutdown` with a 5s deadline.

## Project layout, build, and testing

The module is flat and uses `internal/` packages. There are no third-party Go modules. The darwin collector uses cgo against system frameworks only (IOKit, CoreFoundation); all C lives in one shim file.

```text
vitals/
├── main.go                  # flags, logging, net.Listen, signal handling
├── internal/
│   ├── collector/
│   │   ├── collector.go     # Collector interface, raw counter types
│   │   ├── darwin.go        # //go:build darwin — cgo bindings, Go-side types
│   │   ├── darwin_shim.c/.h # Mach, sysctl, IOKit calls; plain C structs out
│   │   ├── linux.go         # //go:build linux — /proc, /sys parsers
│   │   └── testdata/        # captured /proc and /sys fixtures
│   ├── sampler/             # ticker loop, delta math, ring buffer
│   ├── server/              # http.Handler, routes, negotiation, headers
│   └── render/
│       ├── html.go  json.go  text.go  prom.go
│       ├── sparkline.go     # SVG path generation
│       └── assets/          # dashboard.tmpl, style.css (go:embed)
├── contrib/                 # com.vitals.plist (launchd), vitals.service (systemd)
├── Makefile
└── README.md
```

**Build.** `make build` runs on a Mac and produces `dist/vitals-darwin-arm64` with `CGO_ENABLED=1` (Xcode Command Line Tools required) and `dist/vitals-linux-{amd64,arm64}` with `CGO_ENABLED=0`, all with `-trimpath -ldflags "-s -w -X main.version=… -X main.commit=…"`. Set `MACOSX_DEPLOYMENT_TARGET` to the oldest macOS release you run. Use the current stable Go release, Go 1.22 at minimum. The shim keeps to the public SDK headers; anything private (IOReport) waits for v1.1 and is loaded at runtime, never linked. The README covers launchd first, then systemd.

**Testing.**

- **Darwin collector:** a `//go:build darwin` integration test that calls `Collect` twice and checks invariants: totals above zero, percentages within 0–100, cumulative counters non-decreasing, P + E core counts equal the total. The shim's C code has no logic beyond calls and struct copies, so nothing in it needs separate unit tests.
- **Linux parsers:** read through an `fs.FS` root so tests run against fixtures in `testdata/` on any OS, including the Mac.
- **Sampler:** a fake `Collector` with scripted counters tests rate math, counter wraparound or reset (treat as a missing rate, never negative), first-sample `null`s, and ring-buffer eviction.
- **Negotiation:** table-driven tests over query, `Accept`, and q-value combinations, including the `406` and `405` cases.
- **Renderers:** golden-file tests for HTML, text, and Prometheus output from a fixed snapshot, with `-update` to regenerate.
- **Quality gates:** `go vet`, `staticcheck`, `go test -race ./...`, all clean on macOS; Linux runs the same minus the darwin test.

## Acceptance criteria and future work

v1 is done when every item below holds on the Mac Mini. Items marked (Linux) must also hold on one Linux host.

- [ ] `make build` produces all three binaries; `otool -L` on the darwin binary lists only `/usr/lib` and `/System/Library` entries; `file` reports the Linux binaries as statically linked
- [ ] `vitals` with no flags listens on `127.0.0.1:9110` and `/healthz` returns `200` within two intervals (Linux)
- [ ] Memory used, wired, compressed, and pressure level agree with Activity Monitor to within 5% under a steady load
- [ ] Per-core CPU shows the correct P and E split for the machine's chip
- [ ] Network totals keep counting past 4 GiB on one interface (transfer a large file to check)
- [ ] `/` renders a complete dashboard with JS disabled, under 50 KB, with no requests beyond the page itself (Linux)
- [ ] `/api/v1/stats` JSON matches the documented shape; first-sample rates are `null` (Linux)
- [ ] `curl localhost:9110/?format=text` is readable at 80 columns
- [ ] `/metrics` is accepted by `promtool check metrics`
- [ ] Negotiation, `405`, and `406` behave as specified (covered by tests)
- [ ] Idle at defaults for 1 hour: RSS under 20 MB, average CPU under 1% of one core, RSS not growing after the ring buffer fills
- [ ] Runs as a non-root user under the provided launchd plist; systemd unit (Linux)
- [ ] `go vet`, `staticcheck`, and `go test -race ./...` pass (Linux, minus the darwin test)

Future work, not part of v1:

- **v1.1 stretch: Apple Silicon sensors.** SoC temperatures, CPU/GPU/ANE power in watts, and GPU utilisation through IOReport and IOHID, without root. IOReport is private API: load it at runtime with `dlopen`, and degrade to omitting the fields if the symbols are missing after a macOS update. The rootless open-source tool macmon is a working reference.
- **tailnet listener via `tsnet`.** Pending research. Only `main.go` should change: swap `net.Listen` for `tsnet.Server.Listen`.
- **Optional top-N processes** by CPU and memory, behind a flag and off by default.

Open question: the final project name. `vitals` is a placeholder.
