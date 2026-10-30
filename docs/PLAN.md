# Plan

Derived from [`docs/SPEC.md`](SPEC.md), the original specification. Where they disagree, SPEC wins: raise the disagreement as an open question in `docs/ROADMAP.md` rather than picking one. Anything below marked **Assumption** fills a gap the spec leaves open. Each assumption was a call made at setup, and the maintainer may overrule it.

## Goal

`vitals` is a single-process, read-only Go web server. It reports the vital statistics of the host it runs on as server-rendered HTML, JSON, plain text, or Prometheus text format. The primary host is an Apple Silicon Mac Mini (`darwin/arm64`), and Mac-specific metrics are first-class. `linux/amd64` and `linux/arm64` are secondary, best-effort targets. It ships as one binary with no installed dependencies and no config files. It stays light enough to forget about: under 20 MB RSS and under 1% of one core at a 2s sample interval.

## Non-goals

Out of scope for v1. Don't implement these, and don't add abstractions in anticipation of them:

- **Multi-host aggregation.** No agents, no fan-out, no central server.
- **Authentication and TLS.** Exposure is controlled by the bind address. To reach it over a tailnet, run `tailscale serve` in front of it; there is no built-in `tsnet` listener (decision 42).
- **Process lists.** They're expensive and the most likely thing to leak sensitive information.
- **Persistence.** History is an in-memory ring buffer, lost on restart. No database and no files written.
- **Alerting, thresholds, notifications.**
- **Runtime configuration, or any write endpoint.**
- **Windows.**
- **Third-party Go modules**, and private macOS APIs (IOReport) in v1. v1.1 loads them at runtime with `dlopen`, as decision 44 allows.

## Who it's for

The maintainer, running a Mac Mini (and a Linux box or two) in a homelab. They reach for `vitals` to answer "is the Mini OK?" from a phone on the couch, from `curl` in a terminal over SSH, or from a Prometheus scrape. It has to be trustworthy without being tended: a number that disagrees with Activity Monitor, or a process that slowly grows, defeats the point.

## Design

### Process model

The process runs three parts. A background **sampler** owns all collection. A **store** holds read-only snapshots. An **HTTP handler** only renders. Requests never call the collector.

```text
data path (sampler ticker):  Collector.Collect ──▶ Sampler (deltas, rates) ──▶ Snapshot ──▶ Store (latest + ring buffer)
request path (per request):  net/http ──▶ Handler (route, negotiate) ──▶ Store (read only) ──▶ Renderer ──▶ bytes
```

**Collector.** One interface, with one implementation per OS selected by build tags. It returns raw cumulative counters and instantaneous gauges, and computes no rates.

```go
type Collector interface {
    // Collect returns raw counters and gauges. A failure in one group sets
    // that group's error and leaves the others populated.
    Collect(ctx context.Context) (Raw, error)
    Static() HostInfo // hostname, OS, kernel, arch, CPU model: read once at startup
}
```

**Sampler.** One goroutine on a `time.Ticker` at `-interval`. Each tick does the following:

1. Call `Collect` with a timeout of **80% of the interval**.
2. Compute rates against the previous `Raw`, using the **actual elapsed time** between the two samples.
3. Build an immutable `Snapshot`.
4. Push the snapshot into a fixed-capacity ring buffer.

If a collection overruns, skip the tick. Never queue ticks. A counter that goes backwards, whether from a reset or a wrap, gives a missing rate for that sample, never a negative one.

**Store.** Either a ring buffer under a `sync.RWMutex`, or the latest snapshot in an `atomic.Pointer[Snapshot]` with the history under a mutex. Readers get the latest snapshot or a *copied* history slice. Snapshots are never mutated after publication, so renderers share them without copying.

**Render.** Renderers are pure functions from `Snapshot` (plus history, for the dashboard) to bytes, one per format. The handler picks a renderer from the negotiated format, then sets headers.

**Handler/listener split.** `internal/server` exposes `New(store *sampler.Store, opts Options) http.Handler` and knows nothing about sockets. `main.go` creates the `net.Listener` and the `http.Server`. A future `tsnet` listener changes only `main.go`.

### Metrics (v1)

Six groups. On macOS, collection calls Mach, sysctl, and IOKit directly through a small C shim (`darwin_shim.c`) via cgo. On Linux, collection is hand-rolled from `/proc` and `/sys`. Fields that exist on only one platform go in a platform sub-object in JSON (`"darwin": {…}`) and are omitted elsewhere.

| Group | Fields | macOS source (primary) | Linux source (secondary) |
| --- | --- | --- | --- |
| Host | hostname, OS product version, kernel version, arch, uptime (s), boot time, load avg 1/5/15 | `os.Hostname`; sysctl `kern.osproductversion`, `kern.osrelease`, `hw.machine`, `kern.boottime`, `vm.loadavg` | `/proc/sys/kernel/osrelease`, `/proc/uptime`, `/proc/loadavg` |
| CPU | total and per-core usage %, model name; macOS also performance vs efficiency core counts, with per-core % tagged P or E | `host_processor_info(PROCESSOR_CPU_LOAD_INFO)` tick deltas; sysctl `machdep.cpu.brand_string`, `hw.perflevel0.*` / `hw.perflevel1.*` | `/proc/stat` deltas, `/proc/cpuinfo` |
| Memory | total, available, used, swap total/used; macOS also wired, compressed, active, inactive, file-backed, and memory pressure level (normal / warn / critical) | `host_statistics64(HOST_VM_INFO64)`; sysctl `hw.memsize`, `hw.pagesize`, `vm.swapusage`, `kern.memorystatus_vm_pressure_level` | `/proc/meminfo` (used = total − available) |
| Disks | per mount: device, mount point, fs type, total, used, free, used %; read/write bytes/s per physical device | `getfsstat` + `statfs`; IOKit `IOBlockStorageDriver` "Statistics" dictionary for byte counters | `/proc/self/mounts`, `statfs`, `/proc/diskstats` deltas |
| Network | per interface: rx/tx bytes/s, rx/tx packets/s, cumulative totals | sysctl `IFMIB_IFDATA` (`ifmibdata` / `if_data64`), per decision 24. **Not** `NET_RT_IFLIST2` or `getifaddrs`, whose counters wrap at 4 GiB for unprivileged callers | `/proc/net/dev` deltas |
| Temperatures | per sensor: label, °C | Not in v1. v1.1: the IOHID event system through `dlopen`, aggregated to CPU and GPU (M15, decisions 44, 45) | `/sys/class/hwmon/*` |

Collection rules:

- **Rates come from deltas** between consecutive samples, divided by actual elapsed time, not the configured interval. The first sample has no rates. JSON reports them as `null`, and the dashboard shows "—".
- **Units in machine formats are raw:** bytes, bytes/s, seconds, percentages as floats 0 to 100, and temperatures in °C. Only the HTML and text renderers humanize, with binary units (KiB, MiB, GiB).
- **Default filters:** skip pseudo filesystems (`proc`, `sysfs`, `tmpfs`, `devtmpfs`, `overlay`, `squashfs`, `cgroup*`, etc.) and snap mounts, and network filesystems (`nfs`, `nfs4`, `cifs`, `smb3`, `fuse.sshfs`, `9p`), whose `statfs` can hang the sampler when the server is gone (decision 30). Skip the interfaces `lo`, `lo0`, `veth*`, `docker*`, `br-*`, `utun*`, `awdl*`, `llw*`, `bridge*`, and `anpi*`. The `-disk-exclude` and `-net-exclude` regex flags replace these lists.
- **Partial failure is normal.** If one group fails, log it at most once per minute, record that group's error in the snapshot's `errors` map, and still serve every other group.

Platform details the spec leaves open:

- **Assumption: darwin "used" memory** is computed the way Activity Monitor computes "Memory Used": (internal pages − purgeable pages + wired pages + compressor-occupied pages) × page size. Available = total − used. The acceptance bar is agreement with Activity Monitor to within 5%, which the maintainer checks on the Mini. If the formula misses that bar, change the formula, not the bar.
- **Assumption: darwin P/E tagging per core** comes from the `cluster-type` property of each `IODeviceTree:/cpus/cpuN` registry entry. That is public IOKit. It is cross-checked against the `hw.perflevel0/1.logicalcpu` counts. When the machine reports a single performance level, as a VM might, every core is tagged with that one kind and `e_cores` is 0. The invariant P + E = total always holds.
- **Assumption: disks on darwin.** The default exclude list also skips `devfs`, `autofs`, `nullfs`, and every mount under `/System/Volumes/`. APFS volumes there share a container with `/`, and listing them would show the same space several times. `/` represents the container.
- **Decision 7: disk I/O per mount.** Each mount's `read_bps` and `write_bps` are the rates of the physical device that backs it. Mounts that share a device show the same rates. On darwin, the mapping walks the IORegistry from the mount's BSD name up to its `IOBlockStorageDriver`. On Linux, it goes from the partition to its parent in `/sys/class/block`. A mount that can't be mapped shows `null`.
- **Assumption: host OS fields.** `os` is the GOOS (`"darwin"`, `"linux"`). `os_version` is `kern.osproductversion` on darwin and `PRETTY_NAME` from `/etc/os-release` on Linux, omitted if unreadable. `boot_time` is RFC 3339 UTC.

### HTTP API

Routes use the standard library `net/http` `ServeMux` with Go 1.22+ method patterns. All endpoints accept only `GET` and `HEAD`. Anything else returns `405` with an `Allow: GET, HEAD` header.

| Endpoint | Default format | Negotiable | Returns |
| --- | --- | --- | --- |
| `/` | HTML | yes | Dashboard: current snapshot plus sparklines from history |
| `/api/v1/stats` | JSON | yes | Current snapshot only |
| `/api/v1/history` | JSON | no | Ring buffer contents, oldest first; optional `?since=<unix seconds>` |
| `/metrics` | Prometheus text | no | Current snapshot in exposition format 0.0.4 |
| `/healthz` | text | no | `ok` with `200` once the first sample exists, `503` before |

Format selection for negotiable endpoints, first match wins:

1. The `?format=` query parameter: `html`, `json`, `text`, or `prom`.
2. The `Accept` header, honouring q-values: `text/html`, `application/json`, `text/plain`, `application/openmetrics-text` or `text/plain; version=0.0.4` (Prometheus).
3. The endpoint's default. `*/*` and a missing header both mean the default.

An unknown `format` value, or an `Accept` header that matches nothing and has no wildcard, returns `406` with the supported types listed in the body. Every negotiated response sets `Vary: Accept`.

- **Assumption:** both negotiable endpoints accept all four formats, and only their defaults differ. So `/?format=json` is the stats JSON and `/api/v1/stats?format=html` is the dashboard.
- **Assumption:** `text/plain` without `version=0.0.4` means the text renderer. `q=0` excludes a type.
- **Assumption:** a malformed `?since=` returns `400` naming the parameter. The `503` body from `/healthz` before the first sample is `no sample yet`.

All responses set `Cache-Control: no-store`, a correct `Content-Type` with `charset=utf-8`, `X-Content-Type-Options: nosniff`, and `Referrer-Policy: no-referrer`. HTML responses also set `Content-Security-Policy: default-src 'none'; style-src 'unsafe-inline'; img-src data:; script-src 'sha256-…'`, the hash being the SHA-256 of the dashboard's one inline script (decision 34). JSON is not indented unless `?pretty=1`. Plain text is aligned columns readable in a terminal at **80 columns**. Request bodies are ignored.

### JSON snapshot shape

Field names are `snake_case`, and timestamps are RFC 3339 UTC. Abbreviated example from the spec:

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

The fields the example abbreviates (`os_version`, `boot_time`, the remaining darwin memory fields, a disk's `free_bytes`) follow the same conventions. The complete shape is fixed by a golden file in M1, and `docs/ARCHITECTURE.md` links to it. Rates unknown on the first sample (`usage_pct`, `per_core_pct` entries, every `_bps` and `_pps`) are `null`. `temps` is `null` when there are no sensors, which on darwin is always in v1. `errors` maps a group name (`host`, `cpu`, `memory`, `disks`, `network`, `temps`) to its error message, and is `{}` when every group is fine.

### Prometheus exposition

Metric names use the `vitals_` prefix with base units, and the exposition is written by hand with no client library. The spec fixes these four:

- `vitals_cpu_usage_ratio{cpu="total"}`
- `vitals_memory_available_bytes`
- `vitals_filesystem_used_bytes{mount="/"}`
- `vitals_network_receive_bytes_per_second{iface="en0"}`

**Assumption:** the rest follow the same pattern, and M9 may refine them while keeping those four exactly:

- per-core `vitals_cpu_usage_ratio{cpu="0"}` and `vitals_cpu_core_info{cpu="0",kind="P"} 1`
- `vitals_memory_{total,used,wired,compressed,…}_bytes`, `vitals_swap_{total,used}_bytes`, and `vitals_memory_pressure{level="normal|warn|critical"}` (1 for the current level, 0 otherwise)
- `vitals_filesystem_{size,free}_bytes{mount,device,fs}`
- `vitals_disk_{read,written}_bytes_per_second{device}`
- `vitals_network_{receive,transmit}_{bytes,packets}_per_second{iface}` and `vitals_network_{receive,transmit}_bytes_total{iface}` (counters)
- `vitals_temperature_celsius{sensor}`, `vitals_load_average{period="1m|5m|15m"}`, `vitals_uptime_seconds`, `vitals_boot_time_seconds`
- `vitals_collector_error{group}` (0 or 1), and `vitals_build_info{version,commit}` set to 1
- As built in M9 (decision 35): also `vitals_host_info{hostname,os,os_version,kernel,arch}` set to 1, and the disk rates' `device` is the physical device, written once per device

A rate that is unknown on this sample is omitted, because Prometheus has no null. Ratios run 0 to 1, not 0 to 100.

### Dashboard

One server-rendered HTML page, built with `html/template`, **under 50 KB**, with no external requests. JavaScript is minimal and optional (decision 34): one inline script adds the light/dark/auto theme toggle, and everything else works with scripts off.

- **Layout, top to bottom:** a header line (hostname, OS/kernel, uptime, sampled-at time), then cards for CPU, memory, disks, network, and temperatures (omitted when there are none). Single column on narrow screens, and a two-column grid from about **900px**.
- **Sparklines:** inline SVG generated server-side from the ring buffer. A CPU total line, a memory used line, and a combined rx/tx line per network interface. Fixed `viewBox`, no axes, and the current value as text beside the line.
- **Disk usage:** a horizontal bar per mount, using a styled `<meter>` or a CSS width. Colour reinforces but is never the only signal: the percentage is shown as text too.
- **Refresh:** `<meta http-equiv="refresh">` set to the sample interval, with a **minimum of 2s**. `?refresh=0` disables it, and `?refresh=N` overrides it. Links on the page carry the `refresh` value through. **Assumption:** a non-integer or negative `refresh` is ignored, and a value of 1 is raised to 2.
- **Styling:** one embedded stylesheet (via `embed`) inlined into a `<style>` tag, so each page load is one response. Light and dark modes through `prefers-color-scheme`, using CSS custom properties, which a footer toggle (Auto, Light, Dark) overrides and remembers in the browser (decision 34). The system UI font stack for text, and a monospace stack with `font-variant-numeric: tabular-nums` for figures, so numbers don't jitter between refreshes.
- **Collapsible cards:** each card is a `<details open>` with its heading as the `<summary>`. The script remembers which cards were closed across refreshes (decision 34).
- **Accessibility:** a semantic heading per card, real `<table>` elements for disks and network, and a `<title>` element in every SVG.

### Configuration

Command-line flags only, with safe defaults. There is no config file. Invalid values exit with a clear message and **status 2**.

| Flag | Default | Meaning |
| --- | --- | --- |
| `-listen` | `127.0.0.1:9110` | Bind address. Set to a Tailscale IP to expose on the tailnet only |
| `-interval` | `2s` | Sample interval; minimum `1s` |
| `-history` | `15m` | Ring buffer window; capacity = history ÷ interval (**450 samples** at defaults) |
| `-disk-exclude` | built-in list | Regex of mount points or fs types to skip; replaces the default list |
| `-net-exclude` | built-in list | Regex of interface names to skip; replaces the default list |
| `-log-level` | `info` | `debug`, `info`, `warn`, `error` via `log/slog` |
| `-version` | — | Print version, commit, build date, and exit |

**Assumption:** a `-history` shorter than `-interval`, or a regex that doesn't compile, is an invalid value (status 2).

### Security

- **Loopback by default.** If `-listen` resolves to a non-loopback address, log one `warn` line at startup saying the dashboard is reachable from the network and has no authentication.
- **Unprivileged.** It runs correctly as a normal user on both platforms and never requires root.
- **Hardened server:** `ReadHeaderTimeout` 5s, `ReadTimeout` 10s, `WriteTimeout` 10s, `IdleTimeout` 60s, `MaxHeaderBytes` 16 KB.
- **No secrets in output:** no environment variables, command lines, or usernames.
- **Graceful shutdown** on `SIGINT` and `SIGTERM`: stop the sampler, then `Server.Shutdown` with a 5s deadline.

### Build

- `make build` produces `dist/vitals-darwin-arm64` with `CGO_ENABLED=1` (Xcode Command Line Tools required) on a Mac, and `dist/vitals-linux-{amd64,arm64}` with `CGO_ENABLED=0`.
- Every build uses `-trimpath -ldflags "-s -w -X main.version=… -X main.commit=…"`. **Assumption:** the build date is also set, via `-X main.date=…`, for `-version`.
- `MACOSX_DEPLOYMENT_TARGET` is set to the oldest macOS release the maintainer runs.
- The C shim keeps to the public SDK headers. Private symbols (v1.1) are declared by hand in the shim and resolved with `dlopen`, never linked (decision 44).
- `contrib/` holds `com.vitals.plist` (launchd) and `vitals.service` (systemd). The README covers launchd first, then systemd.

## Intended architecture

```text
vitals/
├── main.go                  # flags, logging, net.Listen, http.Server, signals, shutdown
├── internal/
│   ├── collector/
│   │   ├── collector.go     # Collector interface, Raw and HostInfo types
│   │   ├── darwin.go        # //go:build darwin — cgo bindings, calls into the shim
│   │   ├── darwin_shim.c/.h # Mach, sysctl, IOKit calls; plain C structs out
│   │   ├── darwin_conv.go   # untagged: pure conversion of shim structs to Raw (tested on Linux)
│   │   ├── linux.go         # //go:build linux — wires os.DirFS("/") and syscall.Statfs
│   │   ├── procfs.go        # untagged: /proc and /sys parsers over fs.FS (tested everywhere)
│   │   ├── filter.go        # default disk and interface exclude lists
│   │   └── testdata/        # captured /proc and /sys fixtures
│   ├── sampler/             # ticker loop, delta math, ring buffer, Store
│   ├── server/              # http.Handler, routes, negotiation, headers
│   └── render/
│       ├── html.go  json.go  text.go  prom.go
│       ├── sparkline.go     # SVG path generation
│       ├── assets/          # dashboard.tmpl, style.css (go:embed)
│       └── testdata/        # golden files
├── contrib/                 # com.vitals.plist (launchd), vitals.service (systemd)
├── Makefile
└── README.md
```

This follows the spec's layout. It adds three untagged files (`darwin_conv.go`, `procfs.go`, `filter.go`), so that platform logic is testable on any OS (decisions 5 and 6). The boundary: `collector` does OS access only, `sampler` does math only, `render` is pure, and `server` reads the store. Only `main.go` touches sockets, signals, and process exit.

## Testing strategy

- **Darwin collector:** a `//go:build darwin` integration test that calls `Collect` twice and checks invariants: totals above zero, percentages within 0 to 100, cumulative counters non-decreasing, and P + E core counts equal to the total. It runs on the macOS arm64 CI runner (decision 5). The shim's C has no logic beyond calls and struct copies, so it needs no unit tests. The Go-side conversion is pure and has unit tests on any OS.
- **Linux parsers:** read through an `fs.FS` root, so they run against fixtures in `testdata/` on any OS, including the Mac.
- **Sampler:** a fake `Collector` with scripted counters tests the rate math with a fake clock: counter wraparound or reset (a missing rate, never negative), first-sample `null`s, overrun ticks being skipped, and ring-buffer eviction.
- **Negotiation:** table-driven tests over query, `Accept`, and q-value combinations, including the `406` and `405` cases.
- **Renderers:** golden-file tests for JSON, HTML, text, and Prometheus output from a fixed snapshot, with `-update` to regenerate. HTML has a size test (under 50 KB with a full 450-sample history) and a test that its only script is the theme script whose hash is in the CSP, and that it has no `http://` or `https://` reference. Text has a test that no line exceeds 80 columns. Prometheus has an in-repo grammar test for exposition format 0.0.4 (decision 8).
- **Budgets:** allocation tests on the per-tick and per-request paths, and a test that a full ring buffer doesn't grow. The one-hour idle RSS and CPU check is the maintainer's, on the Mini.
- **Quality gates:** `go vet`, `staticcheck`, and `go test -race ./...`, all clean on Linux in every session and on macOS in CI.

## Risks and unknowns

- **Darwin code can't be compiled in the nightly session.** Cloud sessions are Linux, and cgo for darwin needs the macOS SDK. Mitigation: keep the shim to calls and copies, keep conversion logic in untagged pure Go, and treat the macOS CI job as the check. M0 proves the cgo wiring on the runner.
- **The macOS CI runner is a VM.** It may report one performance level, no memory pressure changes, or virtual block devices without IOKit statistics. The darwin integration test asserts invariants, not specific values, so it holds in a VM. The real-hardware checks (Activity Monitor agreement, the P/E split, the 4 GiB rollover) are the maintainer's acceptance items. M2 and M3 find out first.
- **The Activity Monitor memory formula** is not documented by Apple (see the darwin "used" memory assumption above). The maintainer's check after M4 finds out.
- **Mapping mounts to physical devices on darwin** (APFS container → physical store) may need more IORegistry walking than a small shim wants. M6 finds out. The fallback is `null` rates for unmapped mounts, never a guess.
- **The 1,000 to 1,200 line figure** was an initial estimate, and v1 will pass it (decision 29). It is not a budget: report the count in each PR so growth stays visible, but don't trade readability for lines.
- **staticcheck lags new Go releases.** If the pinned staticcheck can't load Go 1.27 code, M0 finds out, and the fix is raising the staticcheck pin, not lowering Go.

## Milestones

The task-level breakdown, with Needs and acceptance criteria, is in `docs/ROADMAP.md`. In brief:

- **M0** Bootstrap the toolchain: go.mod, Makefile, `make check`, Linux and macOS CI, a cgo smoke test on the macOS runner.
- **M1** The model: `Raw` and `Snapshot` types for all six groups, the sampler with all delta math, the ring buffer and store, and the JSON renderer.
- **M2** First light: the host group on both platforms, `main.go` with every flag, the hardened server, `/healthz`, `/api/v1/stats`, and `/api/v1/history`. Runnable on the Mac.
- **M3** CPU on both platforms, with the P/E split on darwin.
- **M4** Memory on both platforms, with wired, compressed, and pressure on darwin.
- **M5** Network on both platforms, 64-bit counters, and `-net-exclude`.
- **M6** Disks on both platforms: usage, per-device I/O, and `-disk-exclude`.
- **M7** Linux temperatures.
- **M8** The HTML dashboard at `/`.
- **M9** Prometheus `/metrics`.
- **M10** Content negotiation and the text renderer.
- **M11** Packaging: `make build`, launchd and systemd units, and the README.
- **M12** The budget check: allocation and growth tests, plus a soak script.
- **M13** Hide interfaces that have never moved a byte on the dashboard and in text.
- **M14** v1.1: GPU utilisation on darwin, from public IOKit.
- **M15** v1.1: SoC temperatures on darwin through `dlopen`, aggregated to CPU and GPU.
- **M16** v1.1: CPU, GPU, and ANE power on darwin from IOReport energy counters.
