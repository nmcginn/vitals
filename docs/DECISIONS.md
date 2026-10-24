# Decisions

Short records of choices that would otherwise get re-litigated. Each says what was decided and why, in a few lines; the PR that landed it has the detail. Append as they are made, and amend rather than delete when one is reversed. Code comments cite these by number, so numbers are never reused or renumbered.

Decisions marked *setup default* were made while bootstrapping, without asking. The maintainer may overrule any of them in review of the setup PR.

---

### 1. Go 1.27, standard library only, cgo only on darwin

The spec fixes Go (1.22 at minimum, current stable preferred) and forbids third-party modules. Go 1.27.x is pinned with the `go` and `toolchain` directives in `go.mod`, so CI and cloud sessions fetch the same toolchain. staticcheck is pinned by version and run with `go run …@version` from the Makefile, not as a `tool` directive, because a `tool` directive would add `require` lines to `go.mod`. That keeps the module requirement-free (*setup default*). Raising either pin is a PR of its own. cgo is used only for the darwin collector, against system frameworks. Linux builds stay `CGO_ENABLED=0`.

### 2. One reviewable pull request per night

Work lands as one PR per night, from a scheduled routine running `/nightly`, sized as one complete change rather than to a line count. This keeps `master` releasable and a human on every change. The procedure lives in `.claude/commands/nightly.md` so it can be improved by ordinary PRs. See `CLAUDE.md`.

### 3. CI is gated until M0

`.github/workflows/ci.yml` skips its checks until `go.mod` exists, so the setup PR is green without pretending to test anything. M0 removes the gate as part of its acceptance criteria.

### 4. Collection in one place, everything else a pure function of a snapshot

Only the sampler goroutine calls the collector. Collectors return raw counters and compute nothing. The sampler does all delta and rate math. Renderers are pure functions of a snapshot (and history). Handlers only read the store. This is the spec's architecture. It's recorded here because it is what makes almost everything testable on a Linux box with no Mac and no live OS state, and because "just collect it in the handler" is the shortcut it exists to prevent.

### 5. Darwin is verified by a macOS CI job, with conversion logic in untagged Go

Nightly sessions run on Linux, where darwin cgo code can't compile. CI therefore has a `macos-15` (arm64) job that builds with cgo and runs the darwin integration test. GitHub's macOS runners are free for public repos. The shim stays calls-and-copies only. Turning shim structs into `Raw` happens in `darwin_conv.go`, which has no build tag, so its tests run in every session. The real-hardware checks (Activity Monitor agreement, the P/E split, the 4 GiB rollover) remain the maintainer's. *Setup default.*

### 6. Linux parsers read through `fs.FS`, with syscalls injected

From the spec: `/proc` and `/sys` parsing takes an `fs.FS` root, so fixtures in `testdata/` drive it on any OS. The parsers live in untagged files for the same reason. `statfs` is passed in as a function value, so disk tests never touch the real filesystem. `linux.go` (`//go:build linux`) only wires `os.DirFS("/")` and `syscall.Statfs`.

### 7. A mount's I/O rates are its physical device's rates

The spec asks for "read/write bytes/s per physical device", but its JSON puts `read_bps` and `write_bps` on each mount. Each mount reports the rates of the physical device that backs it, and mounts sharing a device show the same numbers. A mount that can't be mapped to a device gets `null`, never 0 and never a guess. The alternative, a separate `disk_io` array keyed by device, would change the documented JSON shape. *Setup default.*

### 8. Prometheus output is checked by an in-repo grammar test

`promtool check metrics` is an acceptance item, but promtool is a separate download, and the loop's checks need no network. A test in `internal/render` checks the exposition format 0.0.4 rules that promtool enforces: HELP and TYPE order, name and label syntax, escaping, and unique series. The maintainer runs promtool against a live scrape as the final word. *Setup default.*

### 9. Milestone order: the model first, then one metric group per night, then the renderers

M1 defines `Raw`, `Snapshot`, and all rate math against a fake collector before any OS code exists, so every later collector only fills in fields and the JSON shape is fixed once. M2 is the first runnable slice on the Mac (host metrics over HTTP). Each metric group then lands for both platforms in one PR, darwin first within the PR. The HTML, Prometheus, and text renderers come after, written once against the complete snapshot. *Setup default.*

### 10. The spec's flat layout, with `main.go` at the root

The usual Go layout puts the binary in `cmd/<name>/`. The spec puts `main.go` at the module root with everything else in `internal/`, which suits a single small binary. The spec's layout wins.

### 11. The spec is committed as `docs/SPEC.md`

The original specification is kept verbatim, as the reference PLAN is derived from. It drops only the author byline, because the repo is public and committed text names no one, and it redraws an embedded diagram that didn't survive export. Gaps the spec leaves open are filled in PLAN and marked **Assumption**.

### 12. staticcheck is built with the toolchain go.mod pins

`go run honnef.co/go/tools/cmd/staticcheck@v0.8.2` switches to go1.26 (the toolchain staticcheck declares), and a staticcheck built that way can't type-check Go 1.27's standard library. The Makefile sets `GOTOOLCHAIN=$(go env GOVERSION)` for that one command, so it builds with the module's go1.27.3. The pin stays at v0.8.2, the latest release. Raising it later is a PR of its own.

### 13. Snapshot nulls: unknown is null, missing is null, empty is `[]`

A value that can't be known is a nil pointer and renders as `null`: every rate on the first sample, after a backwards counter, or across a zero elapsed time; `used_pct` of a zero-size filesystem; CPU usage over zero ticks. A group the collector couldn't read is `null` as a whole (`"network": null` with its message in `errors`), but a filtered-out list is `[]`. The host object always keeps its static fields (hostname, OS, kernel, arch), so a failed host group only nulls `uptime_s`, `boot_time`, and `load`. Per-core usage is `null` for a sample where the core count changed, because offline cores renumber the rest. `temps` entries are `{"label", "celsius"}`, which PLAN left open. Timestamps are truncated to whole seconds; rates are unrounded floats. The shape is pinned by `internal/render/testdata/*.json` and a test that compares its keys with the PLAN example.

### 14. An overrun or failed collection publishes nothing

The sampler calls `Collect` with a context that expires at 80% of the interval. If that deadline has passed when `Collect` returns, the result is discarded and the ticker is reset, so a tick that came due during the stall doesn't fire straight after it (a Go ticker otherwise delivers one pending tick). A `Collect` error (nothing at all collected) also skips the tick. Either way the previous good sample stays the baseline, so the next rate spans the gap over the actual elapsed time. Collectors must honour the context: the sampler never runs two collections at once. The sample time is taken just before `Collect` is called.

### 15. Gauges pass through in collector types

Memory, the darwin CPU fields, and temperatures need no math, so the snapshot uses the collector's structs, which carry their JSON tags, instead of copying them into twin types. Groups with rates (host, CPU, disks, network) have their own snapshot types. To let untagged Go in `internal/collector` build on Linux, `darwin_shim.c` carries `//go:build darwin`.

### 16. Boot time comes from the kernel; darwin uptime counts from it

Linux reads `btime` from `/proc/stat` for `boot_time` and `/proc/uptime` for `uptime_s`. Deriving boot time as now − uptime would wobble by a second between samples (and in `vitals_boot_time_seconds`). darwin has `kern.boottime` but no uptime sysctl that matches `uptime` or Activity Monitor, so `darwin_conv.go` computes uptime as now − boot time, in whole seconds. A boot time that isn't in the past, or a `vm.loadavg` fscale of 0, fails the host group rather than reporting a nonsense uptime or an infinite load. Hostname on Linux is `/proc/sys/kernel/hostname` (read through the same `fs.FS`); on darwin it's `os.Hostname`. Static fields that can't be read are left empty, not errors.

### 17. API edge cases: 503 before the first sample, `?since` is exclusive

`/api/v1/stats` before the first sample answers `503 no sample yet`, like `/healthz`, since there is no snapshot to render. `/api/v1/history` before it is `200 []`. `?since=<unix seconds>` keeps samples taken strictly after that second, so a client polling with the last `sampled_at` it saw gets each sample once. It must be a whole number (`?since=1.5` is `400`); an empty `?since=` means no filter. `?pretty=1` indents both JSON endpoints. Unrouted paths are the standard `404`. Every response, errors included, carries `Cache-Control: no-store`, `nosniff`, and `no-referrer`.

### 18. Unbuilt groups are errors until their milestone lands

Until M3 to M7 land, `cpu`, `memory`, `disks`, `network`, and (on Linux) `temps` are `null` with `"not implemented yet"` in `errors`, and the sampler logs each once a minute at `warn`, like any failing group. On darwin `temps` is simply `null` with no error, which is its v1 behaviour. Each milestone deletes its group from `pending(…)` in its platform's `Collect`. `-disk-exclude` and `-net-exclude` are parsed and validated now, and wired to collectors in M6 and M5.

### 19. The shim is linted, analyzed, and sanitized on the Mac, apart from the cgo build

`make shim-check`, part of `make check` and skipped off a Mac, runs three passes over `darwin_shim.c`. First, clang with a listed set of warnings and `-Werror`. Second, the clang static analyzer with `-analyzer-werror`, which catches leaked CoreFoundation references and malloc'd buffers before M3 to M6 add them. Third, the collector tests with ASan and UBSan. The checks can't go in `#cgo CFLAGS`, because cgo compiles its own generated C with the same flags, and that code fails `-Werror`. The warnings are listed by name, because `-Weverything` grows with every clang release and an Xcode update would break the build. `go test -asan` doesn't support darwin, so the sanitizers go in through `CGO_CFLAGS`/`CGO_LDFLAGS`. ASan sees only C-allocated memory and C stack frames, not structs that Go allocates and passes in. Each pass was shown to fail on a bug planted in the shim. A darwin build without cgo fails in `nocgo_darwin.go` with an identifier that names the fix, rather than with `undefined: collector.New`.
