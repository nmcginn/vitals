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
