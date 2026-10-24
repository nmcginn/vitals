# Roadmap

The backlog for the nightly loop. Tasks are in priority order: take the first unchecked task whose **Needs** have all merged and that is not waiting on an open question.

Each task lists acceptance criteria. A task is done when they hold, tests cover them, and `make check` passes, and, for any task that touches darwin code, when the macOS CI job is green. In the same PR, move it to **Done** as one line naming what landed and its decision numbers. The PR and `docs/DECISIONS.md` hold the detail, so this file stays short.

If a task is larger than one PR, land the smallest **complete** slice (one the maintainer meets as a finished change), tick nothing, and split the rest into new tasks here. Split only at a seam: a surface this PR does not touch, or a decision the maintainer should make first. An entry that exists only because the last change stopped short of the bar in `CLAUDE.md` is unfinished work, not a follow-up, and it belongs in the PR that raised it.

Items marked *(maintainer)* in acceptance criteria are checks only the maintainer can run, on the Mac Mini or with tools the session doesn't have. The PR lists them under "for the reviewer to try" rather than claiming them.

---

## Status

Update this at the end of every session.

- Current: M0 merged. M1 (the model) is open as a PR.
- Next up: M2 (needs M1 merged).
- Open PRs waiting on review: the M1 PR.

## Open questions for the maintainer

Questions the loop could not answer without guessing at behaviour, architecture, or the plan. Each names the task it blocks. When the maintainer answers, record the answer in `docs/DECISIONS.md`, add a *Decided* note to the task, and delete the question here.

- (none open.)

---

## Open

### M2: First light: host metrics, served

- [ ] **`vitals` runs on the Mac and Linux and serves host vitals over HTTP.**
  *Needs:* M1.
  The host group on both platforms. Darwin uses sysctl `kern.osproductversion`, `kern.osrelease`, `hw.machine`, `kern.boottime`, and `vm.loadavg` through the shim, with conversion in `darwin_conv.go`. Linux uses `/proc/sys/kernel/osrelease`, `/proc/uptime`, `/proc/loadavg`, and `/etc/os-release`, read through `fs.FS`. `main.go` gets every flag from PLAN with validation (exit status 2 and a message naming the flag), `log/slog` at `-log-level`, `net.Listen` (logging the actual bound address at `info`, so `-listen 127.0.0.1:0` is usable), `http.Server` with the exact timeouts and `MaxHeaderBytes` from PLAN, the non-loopback `warn` line, and graceful shutdown on `SIGINT`/`SIGTERM` (stop the sampler, then `Shutdown` with a 5s deadline). `internal/server.New(store, opts)` with `/healthz`, `/api/v1/stats` (JSON only until M10), and `/api/v1/history` (`?since=`), the common security and cache headers, and `405` with `Allow: GET, HEAD`. Other groups report "not implemented" in `errors` until their milestone lands.

  *Acceptance:* Linux host parsing is tested against fixtures, including a `/proc/uptime` with fractional seconds and a missing `/etc/os-release` (field omitted, no error). Darwin conversion of the boottime timeval and the loadavg fixed-point struct is tested on Linux. The darwin integration test (macOS CI) calls `Collect` twice: uptime > 0, the boot time is in the past, load averages ≥ 0, and the second uptime ≥ the first. Server tests via `httptest`: `/healthz` is `503 no sample yet` before the first sample and `200 ok` after. `HEAD` returns headers and no body. `POST` returns `405` with `Allow`. Every response carries `Cache-Control: no-store`, `nosniff`, and `no-referrer`. `?since=` filters history, and `?since=abc` is `400`. Flag tests: `-interval 500ms`, `-history 1s` with `-interval 2s`, a bad regex, and an unknown `-log-level` each exit 2 with a message naming the flag. `-listen 0.0.0.0:0` logs exactly one warn line, and `127.0.0.1:0` and `[::1]:0` log none. A test starts the real binary on `127.0.0.1:0` and sees `/healthz` turn `200` within two intervals. *(maintainer)* `make run` on the Mini, then `curl localhost:9110/api/v1/stats` shows the host group.

### M3: CPU

- [ ] **Total and per-core CPU usage, with the P/E split on Apple Silicon.**
  *Needs:* M2.
  Darwin: `host_processor_info(PROCESSOR_CPU_LOAD_INFO)` tick counters per core (copied and then freed with `vm_deallocate` in the shim), `machdep.cpu.brand_string`, `hw.perflevel0/1.logicalcpu`, and per-core kind from `IODeviceTree:/cpus/cpuN` `cluster-type` (see the P/E tagging assumption in PLAN). Linux: `/proc/stat` per-CPU jiffies and `/proc/cpuinfo` model name. Usage % is computed in the sampler from tick deltas.

  *Acceptance:* sampler tests show usage is busy-delta ÷ total-delta × 100, within 0 to 100. A core whose total delta is 0 gives `null`, not NaN. Tick counters that go backwards give `null`. Linux fixtures cover an 8-core x86 `/proc/stat`, a single-core machine, an arm64 `/proc/cpuinfo` with no `model name` line (model falls back to `CPU implementer`/`CPU part` or `"unknown"`, never an error), and offline CPUs missing from `/proc/stat`. Darwin conversion tests (on Linux) cover tagging from a 4P+6E fixture and from a single-perflevel fixture (all one kind, `e_cores` 0). The darwin integration test checks that `p_cores + e_cores == cores`, that `len(core_kinds) == cores`, and that per-core usage is within 0 to 100 on the second sample. *(maintainer)* the Mini shows the correct P and E split for its chip.

### M4: Memory

- [ ] **Memory and swap, with wired, compressed, and pressure on the Mac.**
  *Needs:* M2.
  Darwin: `host_statistics64(HOST_VM_INFO64)`, sysctl `hw.memsize`, `hw.pagesize`, `vm.swapusage`, and `kern.memorystatus_vm_pressure_level`, with used/available per the PLAN assumption. Also wired, compressed, active, inactive, and file-backed, all in bytes. Linux: `/proc/meminfo`, with used = total − available.

  *Acceptance:* darwin conversion tests (on Linux) use a captured `vm_statistics64` fixture. Used and available match the PLAN formula exactly. Pages × page size is computed in 64 bits, so a 16 KiB page × 2²⁰ pages doesn't overflow. Pressure levels 1, 2, and 4 map to `normal`, `warn`, and `critical`, and any other value maps to an error in `errors.memory` rather than a guess. Linux fixtures cover a normal `meminfo`, one with `SwapTotal: 0 kB`, and an old kernel without `MemAvailable` (error recorded, other fields served). The darwin integration test checks total > 0, used ≤ total, available ≤ total, and that pressure is one of the three strings. *(maintainer)* used, wired, compressed, and pressure agree with Activity Monitor to within 5% under steady load.

### M5: Network

- [ ] **Per-interface throughput and totals, with 64-bit counters that never wrap at 4 GiB.**
  *Needs:* M2.
  Darwin: sysctl `NET_RT_IFLIST2`, walking `if_msghdr2` records for `if_data64` in the shim (never `getifaddrs`). Linux: `/proc/net/dev`. The default interface exclude list and `-net-exclude` (a regex that replaces the default list), in `filter.go`. Rates and packet rates are computed in the sampler.

  *Acceptance:* the sampler computes rx/tx bytes/s and packets/s from deltas, giving `null` on the first sample and on a backwards counter. A test with totals moving from 4,294,967,000 to 4,294,968,000 (across 2³²) gives a positive rate, and the total exceeds 2³². Filter tests: each default pattern (`lo`, `lo0`, `veth*`, `docker*`, `br-*`, `utun*`, `awdl*`, `llw*`, `bridge*`, `anpi*`) is excluded, and `en0`, `eth0`, and `tailscale0` are kept. `-net-exclude '^en1$'` replaces the defaults rather than adding to them. All interfaces filtered out gives `"network": []`, not `null` or an error. Linux fixtures include an interface name with a dash and counters above 2³². The darwin integration test checks at least one interface with non-decreasing totals across two samples. *(maintainer)* totals keep counting past 4 GiB during a large transfer.

### M6: Disks

- [ ] **Per-mount usage, and read/write throughput per physical device.**
  *Needs:* M2.
  Darwin: `getfsstat` + `statfs`, and IOKit `IOBlockStorageDriver` "Statistics" (`Bytes (Read)`, `Bytes (Write)`) mapped to mounts per decision 7. Linux: `/proc/self/mounts`, `statfs` (injected, so tests don't hit the real filesystem), `/proc/diskstats` sectors × 512, and the partition-to-parent mapping via `/sys/class/block`. Default filesystem and mount exclusions per PLAN, including the darwin additions, and `-disk-exclude` (a regex over mount point or fs type that replaces the defaults).

  *Acceptance:* used = total − free, and used % = used ÷ total × 100. A zero-size filesystem gives `null` % rather than NaN. Linux fixtures cover `\040` (space) and `\011` escapes in mount points, bind mounts of the same device, snap mounts (excluded), `overlay`, `tmpfs`, `cgroup2`, and `squashfs` (excluded), and an NVMe partition (`nvme0n1p2` → `nvme0n1`) and a SATA one (`sda1` → `sda`) mapped to their devices. A mount whose device can't be mapped gets `null` rates, not 0. Darwin conversion tests cover a `statfs` fixture and the `/System/Volumes/*` exclusion. The darwin integration test checks that `/` is present with total > 0 and used % within 0 to 100, and that I/O counters are non-decreasing where present. A VM device without statistics gives `null` rates, not a failed group.

### M7: Linux temperatures

- [ ] **Temperature sensors on Linux, absent cleanly everywhere else.**
  *Needs:* M2.
  `/sys/class/hwmon/hwmon*/temp*_input` (millidegrees → °C), labelled from `temp*_label` when present, else `<name>/temp<N>` from the hwmon's `name` file. On darwin, `temps` stays `null` in v1.

  *Acceptance:* fixtures cover `coretemp` with labels, `nvme` without labels, a sensor reading `-ENODATA` (skipped, other sensors served), and a machine with no hwmon directory at all (`"temps": null`, no error). Labels are unique (duplicates get a numeric suffix), so Prometheus series never collide.

### M8: Dashboard

- [ ] **A complete, JavaScript-free dashboard at `/`.**
  *Needs:* M2.
  `render/html.go` with `html/template`, `assets/dashboard.tmpl` and `assets/style.css` (embedded, and inlined into `<style>`), and `sparkline.go` (SVG paths from history). The layout, sparklines, disk bars, refresh rules, light and dark modes, typography, and accessibility as PLAN specifies. The route `/` serves HTML with the CSP header. Negotiation arrives in M10.

  *Acceptance:* golden-file tests render a fixed snapshot plus history: a full darwin snapshot, a Linux snapshot with temps, a first-sample snapshot ("—" for every rate), and a snapshot with errors (the failing group's card says what failed while the others render). With 450 samples of history and 8 interfaces, the page is under 50 KB (a test asserts it). The page contains no `<script`, no `on*=` attributes, and no `http://` or `https://` URLs. Every `<svg>` has a `<title>`. Disks and network are `<table>`s, and each disk shows its percentage as text. The meta refresh is 2 at the default interval, 5 at `-interval 5s`, absent with `?refresh=0`, 10 with `?refresh=10`, and 2 with `?refresh=1`. Links carry `refresh` through. Sparkline tests cover an empty history, a single point, a flat line, and `null` gaps. A hostname containing `<script>` is escaped. The temperatures card is omitted when `temps` is `null`. `/` sends `Content-Security-Policy` exactly as PLAN lists it. *(maintainer)* the dashboard looks right on a phone and at desktop width, in both colour schemes.

### M9: Prometheus `/metrics`

- [ ] **A scrape target Prometheus accepts.**
  *Needs:* M2.
  `render/prom.go`, a hand-written exposition format 0.0.4 per PLAN's naming, served at `/metrics` with `Content-Type: text/plain; version=0.0.4; charset=utf-8`.

  *Acceptance:* a golden file from a fixed full snapshot includes the four metric names the spec fixes, exactly. An in-repo grammar test (decision 8) checks every family: `# HELP` and `# TYPE` precede its samples, names match `[a-zA-Z_:][a-zA-Z0-9_:]*`, no family appears twice, there are no duplicate label sets, label values escape `\`, `"`, and newlines, and there are no `NaN` values unless intended. Unknown rates are omitted (the first-sample golden file has no `_per_second` samples). Ratios are within 0 to 1. A mount point and hostname with quotes and backslashes are escaped correctly. *(maintainer)* `promtool check metrics` accepts a live scrape.

### M10: Content negotiation and the text renderer

- [ ] **`/` and `/api/v1/stats` speak HTML, JSON, text, and Prometheus, as the client asks.**
  *Needs:* M8, M9.
  A pure `negotiate(query, accept, default) (format, error)` in `internal/server`, and `render/text.go` (aligned columns, binary units, readable at 80 columns). Both negotiable endpoints accept all four formats. Every negotiated response sets `Vary: Accept`.

  *Acceptance:* a table-driven negotiation test covers: `?format=` beating `Accept`; each of the four format values; `?format=xml` → 406 listing the supported types; `Accept: application/json` → JSON; `Accept: text/html;q=0.5, application/json;q=0.9` → JSON; `q=0` excluding a type; `*/*` and a missing header → the endpoint default; `text/*` → text or HTML per q and order; `text/plain; version=0.0.4` → Prometheus while bare `text/plain` → text; `application/openmetrics-text` → Prometheus; `Accept: image/png` → 406; and `image/png, */*;q=0.1` → default. `405` still wins over negotiation for `POST`. A text golden file comes from a full snapshot, and a test asserts that no line exceeds 80 columns, even with a 60-character mount point (truncated with `…`, not wrapped). The first-sample text shows "—" for rates. *(maintainer)* `curl localhost:9110/?format=text` reads well in a terminal.

### M11: Packaging and the README

- [ ] **Release binaries, service units, and a README that gets the Mini running in five minutes.**
  *Needs:* M2.
  `make build`: on Linux it produces `dist/vitals-linux-{amd64,arm64}` (`CGO_ENABLED=0`) and reports that the darwin binary needs a Mac. On a Mac it also produces `dist/vitals-darwin-arm64` (`CGO_ENABLED=1`, `MACOSX_DEPLOYMENT_TARGET` set in the Makefile). Every build uses `-trimpath -ldflags "-s -w -X main.version=… -X main.commit=… -X main.date=…"`. `contrib/com.vitals.plist` is a launchd user agent, unprivileged, with `KeepAlive`, logs to `~/Library/Logs`. `contrib/vitals.service` is a systemd unit with `DynamicUser=yes` and hardening (`NoNewPrivileges`, `ProtectSystem=strict`, `ProtectHome`). The README covers install, launchd first, then systemd, then the flags, endpoints, and security notes.

  *Acceptance:* the Linux CI job runs `make build`, and `file` reports both Linux binaries as statically linked (checked in CI). The macOS CI job runs `make build`, and a script asserts every `otool -L` entry is under `/usr/lib` or `/System/Library`. `plutil -lint` passes on the plist (macOS CI), and `systemd-analyze verify` passes on the unit where available (Linux CI). `vitals -version` from the built binary prints the injected version, commit, and date. *(maintainer)* it runs as a non-root user under the launchd plist on the Mini.

### M12: Hold the budget

- [ ] **Evidence that it stays under 20 MB and 1% CPU, and stops growing once the buffer fills.**
  *Needs:* M3, M4, M5, M6, M8, M10.
  Allocation tests and benchmarks on the per-tick path (collect → snapshot → store) and the per-request paths (each renderer), a test that the store's memory is flat once 450 samples have wrapped, and a `make soak` script that runs the binary at defaults for N minutes, polling `/healthz` and the dashboard, and reports RSS (from `/proc` or `ps`) and CPU time at the start and end.

  *Acceptance:* `testing.AllocsPerRun` bounds the steady-state tick, and the bound is recorded in the test with a comment saying why it's that number. After 2 × capacity ticks with a fake collector, the retained heap after GC is within a small fixed margin of what it was after 1 × capacity (the test asserts it). The renderers don't allocate in proportion to history length beyond the output itself. A 5-minute `make soak` run in the session (Linux) reports RSS under 20 MB and CPU under 1% of one core, and the PR quotes its numbers. *(maintainer)* idle at defaults for one hour on the Mini: RSS under 20 MB, average CPU under 1% of one core, and RSS not growing after the ring buffer fills.

---

## Done

- **M0: Bootstrap the toolchain.** `go.mod` (no requires), `-version` main, the cgo shim proving the darwin link line, `make check`, and ungated two-job CI (decisions 1, 3, 12).
- **M1: The model.** `collector.Raw`/`HostInfo`, `Snapshot` with nullable rates, the sampler (80% timeout, skip-on-overrun, rates over actual elapsed time), the ring buffer and `Store`, `render.JSON` with goldens, and `make golden` (decisions 13, 14, 15).

---

## Ideas, not yet scheduled

The loop never starts these on its own. The maintainer moves one into **Open** to schedule it.

- v1.1: Apple Silicon sensors (SoC temperatures, CPU/GPU/ANE power, GPU utilisation) through IOReport and IOHID, without root. Load IOReport at runtime with `dlopen`, and omit the fields if the symbols are missing. macmon is a reference.
- A tailnet listener via `tsnet`, after research. Only `main.go` changes, by swapping `net.Listen` for `tsnet.Server.Listen`. Note that this would bring in third-party modules, which needs a decision of its own.
- Optional top-N processes by CPU and memory, behind a flag and off by default.
- The final project name (`vitals` is a placeholder).
