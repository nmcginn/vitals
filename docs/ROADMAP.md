# Roadmap

The backlog for the nightly loop. Tasks are in priority order: take the first unchecked task whose **Needs** have all merged and that is not waiting on an open question.

Each task lists acceptance criteria. A task is done when they hold, tests cover them, and `make check` passes, and, for any task that touches darwin code, when the macOS CI job is green. In the same PR, move it to **Done** as one line naming what landed and its decision numbers. The PR and `docs/DECISIONS.md` hold the detail, so this file stays short.

If a task is larger than one PR, land the smallest **complete** slice (one the maintainer meets as a finished change), tick nothing, and split the rest into new tasks here. Split only at a seam: a surface this PR does not touch, or a decision the maintainer should make first. An entry that exists only because the last change stopped short of the bar in `CLAUDE.md` is unfinished work, not a follow-up, and it belongs in the PR that raised it.

Items marked *(maintainer)* in acceptance criteria are checks only the maintainer can run, on the Mac Mini or with tools the session doesn't have. The PR lists them under "for the reviewer to try" rather than claiming them.

---

## Status

Update this at the end of every session.

- Current: v1 is built, M14 (GPU utilisation) has merged, and M15 (SoC temperatures) is in review. What's left of v1 are the maintainer's checks on the Mini: the one-hour soak (M12), the launchd run (M11), and `curl` text output (M10).
- Next up: M16, CPU, GPU, and ANE power on darwin.
- Open PRs waiting on review: M15, SoC temperatures on darwin.

## Open questions for the maintainer

Questions the loop could not answer without guessing at behaviour, architecture, or the plan. Each names the task it blocks. When the maintainer answers, record the answer in `docs/DECISIONS.md`, add a *Decided* note to the task, and delete the question here.

- **Do newer chips' IOHID sensors carry macmon's CPU and GPU names?** (Raised in M15; blocks nothing yet.) Decision 45 follows macmon, whose IOHID path matches `pACC`/`eACC`/`GPU MTR Temp Sensor`. Published M1 listings have those names, but macmon switched to SMC keys for later chips and macOS releases, which suggests the Mini's M4 may list only `PMU tdie*`/`tdev*` sensors, leaving `temps` null. Run `go test -v -run TestDarwinCollect ./internal/collector` on the Mini: it logs every sensor name. If none match, the options are (a) leave darwin temps null on that chip; (b) add an `SoC mean`/`SoC max` pair from `PMU tdie*` when no CPU or GPU names exist; (c) another mapping from the logged names. Recommendation: (b), which is honest about not knowing which die is which.

---

## Open

### M16: CPU, GPU, and ANE power on darwin (v1.1)

- [ ] **Watts per domain, from cumulative energy counters.**
  *Needs:* M15 (the `dlopen` pattern).
  IOReport's Energy Model channels, from `libIOReport.dylib` via `dlopen` per decision 44, with the subscription created once. The collector returns cumulative energy per domain (CPU, GPU, ANE) as raw counters. The sampler turns them into watts over the actual elapsed time, like every other rate. It's darwin-only, so it lives in the `"darwin"` sub-object, in a new `power` group with its own `errors` entry. Prometheus carries the cumulative counter and the rate, following PLAN's pattern for network bytes. Record the names in a decision.

  *Acceptance:* sampler tests with a fake collector: the first sample is `null`, a counter that goes backwards is `null`, watts use the actual elapsed time, and a skipped tick still divides by the real gap. Conversion tests from captured channel values, covering unit scaling (mJ, µJ, nJ as IOReport reports them). A missing library or symbol is a `power` error, not a crash. Goldens for all four formats. `make budget` passes. *(maintainer)* `SOAK_MINUTES=60 make soak` on the Mini stays under 1% of one core with power on, and watts agree with macmon to within 10%.

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
- **M12: Budget.** Allocation and growth tests for the tick and the renderers in `make budget` (part of `make check`), benchmarks for collect, tick, and each renderer, the dashboard's sparklines downsampled in place, and `make soak` (decision 40). *(maintainer)* The one-hour idle check on the Mini is still to run: `SOAK_MINUTES=60 make soak`.
- **M13: Idle interfaces.** `shownInterfaces` in `render/shown.go` drops interfaces with zero rx and tx totals from the dashboard and text, and JSON and Prometheus list every one (decision 41). *(maintainer)* the Mini's network card shows only the ports in use.
- **M14: GPU utilisation.** The `gpu` group from the `IOAccelerator` `PerformanceStatistics` "Device Utilization %" through the shim, `darwin.gpu.utilization_pct` in JSON, `vitals_gpu_utilization_ratio`, a GPU card with a sparkline, and a `GPU` line in text (decision 46). *(maintainer)* it tracks Activity Monitor's GPU History under load.
- **M15: SoC temperatures.** `vitals_temps_open`/`vitals_temps` in the shim through `dlopen` of IOKit's private IOHID event system, `tempsFromHID` aggregating macmon's CPU and GPU sensor names to `CPU mean`/`max` and `GPU mean`/`max` in `temps`, readings outside 0 to 150 °C dropped (decisions 45, 47). *(maintainer)* CPU and GPU agree with macmon on the Mini; see the open question.

---

## Ideas, not yet scheduled

The loop never starts these on its own. The maintainer moves one into **Open** to schedule it.

- Optional top-N processes by CPU and memory, behind a flag and off by default. Scheduling it means amending priority 4 and the non-goals, which rule out process lists. Without root, macOS may also refuse to read other users' processes, which would hide the system daemons that matter most on the Mini. Check that before scheduling it.
