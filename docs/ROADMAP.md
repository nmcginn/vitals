# Roadmap

The backlog for the nightly loop. Tasks are in priority order: take the first unchecked task whose **Needs** have all merged and that is not waiting on an open question.

Each task lists acceptance criteria. A task is done when they hold, tests cover them, and `make check` passes, and, for any task that touches darwin code, when the macOS CI job is green. In the same PR, move it to **Done** as one line naming what landed and its decision numbers. The PR and `docs/DECISIONS.md` hold the detail, so this file stays short.

If a task is larger than one PR, land the smallest **complete** slice (one the maintainer meets as a finished change), tick nothing, and split the rest into new tasks here. Split only at a seam: a surface this PR does not touch, or a decision the maintainer should make first. An entry that exists only because the last change stopped short of the bar in `CLAUDE.md` is unfinished work, not a follow-up, and it belongs in the PR that raised it.

Items marked *(maintainer)* in acceptance criteria are checks only the maintainer can run, on the Mac Mini or with tools the session doesn't have. The PR lists them under "for the reviewer to try" rather than claiming them.

---

## Status

Update this at the end of every session.

- Current: M11, packaging and the README, in review.
- Next up: M12, the budget check (all its Needs have merged).
- Open PRs waiting on review: M11.

## Open questions for the maintainer

Questions the loop could not answer without guessing at behaviour, architecture, or the plan. Each names the task it blocks. When the maintainer answers, record the answer in `docs/DECISIONS.md`, add a *Decided* note to the task, and delete the question here.

- **Should the defaults hide interfaces that have never moved a byte?** (Blocks nothing; decision 25 deferred it to "once the dashboard shows how noisy that is".) On an M5 MacBook Air the dashboard's network card lists eight interfaces, and seven of them (`gif0`, `stf0`, `en1` to `en4`, `ap1`) read 0 B/s with 0 B total, so `en0` sits at the bottom of a phone-length list. Options: (a) keep PLAN's list as is; (b) add `gif.*`, `stf.*`, and `ap.*` to the default exclude list, which leaves the Thunderbolt `en1` to `en4`; (c) leave the list alone and have the renderers skip interfaces whose rx and tx totals are both 0, so a port shows up the moment it is used. Recommendation: (c), since it needs no guessing at names and also covers Linux's idle bridges, but it hides something JSON still shows, which is the maintainer's call. Lands in `filter.go` for (b), or in every renderer for (c).

---

## Open

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
- **M6: Disks.** Per-mount usage on both platforms (free is `f_bavail`), each mount's device I/O from `/proc/diskstats` via `/sys/block` and from the IORegistry's `IOBlockStorageDriver`, the default mount list with one mount per device, and `-disk-exclude` (decisions 26, 27, 28). *Decided* after M6: network filesystems join the default exclude list (decision 30).
- **M7: Linux temperatures.** hwmon sensors labelled from `temp*_label` or `<chip>/temp<N>`, unique labels by suffix, unreadable sensors skipped, `"temps": null` with no sensors (decision 31).
- **M8: Dashboard.** `/` renders `render.HTML` from the latest snapshot and history: cards per group with errors in place, SVG sparklines of at most 120 peak points, disk and network tables, meta refresh with `?refresh=`, light and dark with a remembered theme toggle, collapsible cards that stay closed across refreshes, and a CSP that allows only that script's hash (decisions 32, 33, 34).
- **M9: Prometheus.** `render.Prom` at `/metrics` in exposition format 0.0.4 with PLAN's names plus `vitals_host_info`, disk rates once per physical device, unknowns omitted, an in-repo grammar test, and the footer's `Metrics` link (decision 35).
- **M10: Negotiation and text.** `negotiate` in `internal/server` (`?format=`, then `Accept` by q and specificity, then the default; `406` listing the formats; `Vary: Accept`) on `/` and `/api/v1/stats`, `render.Text` at 80 columns with names cut by `…` and errors wrapped, and the footer's `Text` link (decisions 36, 37).
- **M11: Packaging.** `make build` (darwin only on a Mac, `MACOSX_DEPLOYMENT_TARGET=13.0`), `dist-check` in `make check` (static Linux binaries, `otool -L`, `plutil -lint`, `systemd-analyze verify`, `-version`), `contrib/com.vitals.plist` and `contrib/vitals.service`, and the README's install and security notes (decisions 38, 39).

---

## Ideas, not yet scheduled

The loop never starts these on its own. The maintainer moves one into **Open** to schedule it.

- v1.1: Apple Silicon sensors (SoC temperatures, CPU/GPU/ANE power, GPU utilisation) through IOReport and IOHID, without root. Load IOReport at runtime with `dlopen`, and omit the fields if the symbols are missing. macmon is a reference.
- A tailnet listener via `tsnet`, after research. Only `main.go` changes, by swapping `net.Listen` for `tsnet.Server.Listen`. Note that this would bring in third-party modules, which needs a decision of its own.
- Optional top-N processes by CPU and memory, behind a flag and off by default.
- The final project name (`vitals` is a placeholder).
