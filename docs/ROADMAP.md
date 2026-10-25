# Roadmap

The backlog for the nightly loop. Tasks are in priority order: take the first unchecked task whose **Needs** have all merged and that is not waiting on an open question.

Each task lists acceptance criteria. A task is done when they hold, tests cover them, and `make check` passes, and, for any task that touches darwin code, when the macOS CI job is green. In the same PR, move it to **Done** as one line naming what landed and its decision numbers. The PR and `docs/DECISIONS.md` hold the detail, so this file stays short.

If a task is larger than one PR, land the smallest **complete** slice (one the maintainer meets as a finished change), tick nothing, and split the rest into new tasks here. Split only at a seam: a surface this PR does not touch, or a decision the maintainer should make first. An entry that exists only because the last change stopped short of the bar in `CLAUDE.md` is unfinished work, not a follow-up, and it belongs in the PR that raised it.

Items marked *(maintainer)* in acceptance criteria are checks only the maintainer can run, on the Mac Mini or with tools the session doesn't have. The PR lists them under "for the reviewer to try" rather than claiming them.

---

## Status

Update this at the end of every session.

- Current: M5 (network) in review.
- Next up: M6.
- Open PRs waiting on review: the M5 network PR.

## Open questions for the maintainer

Questions the loop could not answer without guessing at behaviour, architecture, or the plan. Each names the task it blocks. When the maintainer answers, record the answer in `docs/DECISIONS.md`, add a *Decided* note to the task, and delete the question here.

- **The line budget.** After M5, non-test Go is 1,506 lines (plus a 307-line shim and header), against the spec's "roughly 1,000 to 1,200" for all of v1. Two groups (disks on both platforms, Linux temperatures) and three renderers remain; at the pace so far (about 240 lines a group) v1 lands well past 2,000. Options: (a) treat the figure as a guide and keep reporting the count; (b) set a new ceiling (say 2,000); (c) hold 1,200 and cut scope or density to fit. Recommendation: (a) or (b), because the code is not padded, and fitting 1,200 means denser code, against priority 5. Blocks nothing yet; it decides how hard M6 onwards should squeeze.

---

## Open

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
- **M2: First light.** The host group on darwin (shim + `darwin_conv.go`) and Linux (`procfs.go` over `fs.FS`), `main.go` with every flag, the hardened server, `internal/server` with `/healthz`, `/api/v1/stats`, and `/api/v1/history` (decisions 16, 17, 18).
- **M3: CPU.** Total and per-core usage on both platforms, the model name, and the darwin P/E split from `IODeviceTree:/cpus` checked against `hw.perflevel` (decisions 20, 21).
- **M4: Memory.** Memory and swap on both platforms, with darwin's Activity Monitor formula, wired, compressed, active, inactive, file-backed, and pressure (decisions 22, 23).
- **M5: Network.** Per-interface totals and rates on both platforms, darwin from `IFMIB_IFDATA` for true 64-bit counters, and the default interface list with `-net-exclude` in `filter.go` (decisions 24, 25).

---

## Ideas, not yet scheduled

The loop never starts these on its own. The maintainer moves one into **Open** to schedule it.

- v1.1: Apple Silicon sensors (SoC temperatures, CPU/GPU/ANE power, GPU utilisation) through IOReport and IOHID, without root. Load IOReport at runtime with `dlopen`, and omit the fields if the symbols are missing. macmon is a reference.
- A tailnet listener via `tsnet`, after research. Only `main.go` changes, by swapping `net.Listen` for `tsnet.Server.Listen`. Note that this would bring in third-party modules, which needs a decision of its own.
- Optional top-N processes by CPU and memory, behind a flag and off by default.
- The final project name (`vitals` is a placeholder).
