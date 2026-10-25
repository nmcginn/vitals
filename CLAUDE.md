# vitals: working agreement

`vitals` is a single-binary, read-only Go web server that reports the vital statistics of the one machine it runs on: a Mac Mini first, Linux boxes second. It serves a server-rendered dashboard, JSON, terminal-friendly text, and Prometheus metrics from one process that uses less memory than a browser tab. Good looks like this: the maintainer glances at the dashboard on their phone, and the numbers agree with Activity Monitor. `curl mini:9110/?format=text` reads well in an 80-column terminal. Prometheus scrapes `/metrics` without complaint. Months later, the process is still under 20 MB and nobody has had to think about it.

Most work here is done by Claude, one pull request per night, reviewed by the maintainer in the morning. That cadence shapes everything below: each change must stand on its own and be verifiable without the reviewer setting anything up.

`docs/PLAN.md` is the design, digested from `docs/SPEC.md` (the original spec). `docs/ROADMAP.md` is what's next. `docs/DECISIONS.md` is why things are the way they are. Read the relevant parts before changing anything. Where PLAN and SPEC disagree, SPEC wins and the disagreement is an open question.

## Priorities

In this order, when they conflict:

1. **Right on the Mac.** `darwin/arm64` is the primary target. Mac-specific metrics (memory pressure, wired and compressed memory, P/E cores, per-disk I/O) are first-class. Memory used, wired, compressed, and pressure agree with Activity Monitor to within 5%. Rates come from deltas divided by the *actual* elapsed time. A rate that can't be known (first sample, counter reset) is `null`, never `0` and never negative. Counters are 64-bit everywhere: network totals keep counting past 4 GiB. Linux is best-effort and never holds up darwin work, and Linux parity is not a goal.
2. **One binary, nothing installed.** Standard library only: no third-party Go modules, not even `golang.org/x/sys`. On darwin the binary links only `libSystem`, IOKit, and CoreFoundation (`otool -L` shows only `/usr/lib` and `/System/Library`). Linux builds are `CGO_ENABLED=0` and fully static. No config files, no assets on disk, and no network requests from the dashboard.
3. **Light.** At the default 2s interval: under 20 MB RSS and under 1% of one core at idle, and RSS stops growing once the ring buffer is full. Serving a request never triggers collection. The dashboard is under 50 KB. Avoid per-tick allocations that grow with history, and avoid anything that spawns a process.
4. **Read-only and safe by default.** No endpoint changes anything. Listen on `127.0.0.1:9110` by default, and warn once at startup when bound elsewhere. Never require root. Never output environment variables, command lines, usernames, or process lists. Security headers and server timeouts are exactly as `docs/PLAN.md` lists them.
5. **Small and readable.** The spec's 1,000 to 1,200 lines of Go plus one small C shim was an initial estimate, not a budget (decision 29): clean code beats a lower count, so never make code denser to save lines, and never pad it either. Choose the plain version over the clever one, and don't build abstractions for the non-goals (multi-host, auth, persistence, tsnet). The dashboard works fully with JavaScript disabled, and text output fits 80 columns.

## Architecture

**Collection happens in exactly one place, and everything else is a pure function of a `Snapshot`.** The collector returns raw cumulative counters and gauges from the OS and computes nothing. The sampler goroutine owns the collector, does all the delta and rate math, and publishes immutable snapshots to the store. HTTP handlers only read the store and hand a snapshot to a renderer. This boundary is what makes the project testable on a Linux CI box with no Mac attached: rate math runs against a fake collector, renderers run against fixed snapshots, and Linux parsers run against fixture files.

- `internal/collector`: OS access only. Linux reads through an `fs.FS` root (plus injected `statfs`), and darwin goes through the C shim in `darwin_shim.c`. Pure Go conversion of shim output lives in untagged files so Linux tests cover it.
- `internal/sampler`: ticker loop, delta math, ring buffer, and store. No OS calls, no HTTP.
- `internal/render`: one pure renderer per format, from snapshot (and history) to bytes. No `net/http`, no clock, no store.
- `internal/server`: routes, negotiation, and headers. `New(store, opts) http.Handler` knows nothing about sockets. `main.go` owns flags, the listener, `http.Server`, signals, and shutdown.

See `docs/ARCHITECTURE.md` for the module map.

## Commands

- `go mod download`: install dependencies (there are none, so this checks `go.mod` and fetches the pinned toolchain)
- `make run`: run it locally at `-log-level debug`
- `make check`: everything CI runs. **Must pass before anything is pushed.** It runs gofmt, `go vet`, staticcheck (pinned), `go test -race ./...`, `make shim-check` (on a Mac: clang warnings, the static analyzer, and ASan/UBSan over the C shim), and static Linux builds.
- `make build`: release binaries into `dist/`. Both Linux binaries build anywhere, and the darwin binary builds only on a Mac.
- `make golden`: regenerate renderer golden files (`go test ./internal/render -update`). Review the diff before committing.

Until milestone M0 lands, these commands do not exist yet. Creating them is M0's job. `make golden` arrives with M1, and the full `make build` with M11.

**Darwin code only compiles on a Mac.** Cloud sessions are Linux, so `make check` there skips everything behind `//go:build darwin`. CI has a macOS job (arm64) that runs the darwin build and integration test. After pushing a change that touches darwin code, that job is the check. Read its result before finishing, and fix a failure.

## Testing standards

- Every bug fix starts with a failing test.
- Test behaviour, not implementation. Test names are sentences describing the guarantee, via `t.Run`: `"rate is null when the counter goes backwards"`, not `"case 3"`.
- Prefer table-driven tests. Renderers use golden files in `testdata/`, regenerated with `-update`.
- Always include the awkward cases: the first sample (rates `null`); a counter that resets or wraps (missing rate, never negative); a collection that overruns its timeout (tick skipped, not queued); one group failing while the others are served; network counters past 2³² bytes; a single-core machine and a machine with zero swap; no temperature sensors (card and JSON omitted or `null`, as PLAN specifies); every disk or interface filtered out; mount points containing spaces (`\040` in `/proc/self/mounts`); `HEAD` requests; `Accept: */*`, a missing `Accept`, and `q=0`; `?refresh=0`; a hostname that needs HTML and Prometheus label escaping; long mount paths at 80 columns.
- No test needs live credentials, the network, root, or the maintainer's Mac. Linux behaviour is tested from captured `/proc` and `/sys` fixtures. Darwin's live behaviour is covered by one `//go:build darwin` integration test that runs on the macOS CI runner.
- Never weaken, skip, or delete a test to get to green. If a test is genuinely wrong, change it deliberately and say so in the PR.

## Conventions

- Go 1.27, pinned by the `go` and `toolchain` directives in `go.mod`, so CI and local checks agree. staticcheck is pinned by version in the Makefile and run with `go run`, so `go.mod` stays free of requirements (decision 1).
- Add a dependency only when it earns its place, and record anything notable in `docs/DECISIONS.md`. Here, that means never for Go modules (priority 2).
- Comments explain *why*. The code already says what.
- Error messages are written for a user having a bad day: say what failed and what to do next. A bad flag exits with status 2 and names the flag and the accepted values.
- Units go in names: `RxBytes`, `UptimeSeconds`, `UsedPct`, and in JSON `_bytes`, `_bps`, `_s`, `_pct`. Machine formats carry raw units (bytes, seconds, percentages 0 to 100, °C). Only the HTML and text renderers humanize, with binary units (KiB, MiB, GiB).
- JSON field names are `snake_case`, and timestamps are RFC 3339 UTC. Platform-only fields live in a `"darwin": {…}` sub-object and are omitted elsewhere.
- Log with `log/slog`. A failing metric group is logged at most once per minute.
- All C lives in `internal/collector/darwin_shim.c` and `.h`: calls and struct copies only, no logic, public SDK headers only.
- In committed text, commit messages, PRs, and PR comments, refer to people by role ("the maintainer", "the reviewer"), never by name.

## Guardrails

- Never push to `master`. Never merge your own PR. Never force-push a branch with an open PR.
- Never leave `master` broken.
- No third-party Go modules, including gopsutil, the Prometheus client, and `golang.org/x/*`. The spec rules them out, and the single-binary goal depends on it.
- No private macOS API (IOReport, SMC) in v1. It belongs to the v1.1 stretch and must be loaded with `dlopen` when it comes. Linking it would break the binary on the next macOS update.
- Nothing from the non-goals: no process lists, write endpoints, persistence, auth, alerting, or multi-host support. Don't add hooks or interfaces "for later" either. The handler/listener split is the only accommodation for tsnet.
- No JavaScript on the dashboard, and no external requests of any kind (fonts, CDNs, images). The CSP forbids them anyway.
- Never require or test as root, and never shell out (`vm_stat`, `sysctl`, `df`) to collect a metric. That costs CPU and breaks priority 3.
- Never change a number from the spec (a budget, default, timeout, header, or flag) to make something pass. Those changes are the maintainer's, through an open question.

## The nightly loop

Each night a fresh session picks up the next task. The full procedure is in `.claude/commands/nightly.md`: run `/nightly`. In short:

1. Read the **Status** and **Open questions** in `docs/ROADMAP.md`, and check open PRs. Review feedback comes first.
2. Take the first unchecked task whose **Needs** have merged and that is not waiting on an open question.
3. Branch from `master` as `nightly/<yyyy-mm-dd>-<slug>`.
4. Build it with tests, run `make check`, and check the thing itself, not just the checks.
5. Open one PR that explains what changed, how each acceptance criterion was verified, and what deserves the closest look. Update the roadmap, decisions, and architecture docs in the same PR.

### What one pull request means

One complete change, not one line count. There is no line target, and size is not the test. The reviewer should be able to read it over coffee and see a whole thought. So the first question is "is this finished?", and only then "is this small?".

A PR carries whatever it takes to be complete: the change, its tests, fixtures, and docs. Tests often run several times the length of the code they cover, and a change is never worth splitting because its tests are long.

Splitting is a claim, and a follow-up has to earn it. One of these must be true:

- it lands on a surface this PR does not touch, such as another command, another view, or a module that does not exist yet;
- it turns on a decision that is the maintainer's to make, so building it now would be guessing at the answer;
- it is a night's work on its own.

Finishing what you just built is none of those. If a follow-up exists only because the change stops short of the priorities above, it is not a follow-up. It is the rest of the task, and it ships now. Two thirds of a change tonight and the last third tomorrow costs the reviewer two readings of the same paragraph.

The tell is in your own writing: when the review notes say "the cost is that…", what follows is usually the half you should have finished.

### When blocked

If a decision belongs to the maintainer (product behaviour, feel, an ambiguous spec, anything that changes the architecture or the plan), don't guess:

1. Add it to **Open questions for the maintainer** in `docs/ROADMAP.md`. Give the question, the options you see, your recommendation, and the task it blocks.
2. Move on to the next task that doesn't depend on it.
3. If nothing else can proceed, open a PR titled `[BLOCKED] <question>` containing only the roadmap change, so the question reaches the maintainer in the morning.

When the maintainer answers, in the roadmap or a PR comment, the next session records the answer in `docs/DECISIONS.md`, adds a *Decided* note to the task it unblocks, and deletes the question.
