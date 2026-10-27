# vitals

A tiny, read-only system monitor for one machine. It's built for a Mac Mini, and it works on Linux too.

`vitals` is a single Go binary that reports the vital statistics of the host it runs on: CPU (with Apple Silicon's performance and efficiency cores), memory (with macOS memory pressure, wired, and compressed), disks, network, and, on Linux, temperatures. It serves them as an HTML dashboard that works with JavaScript off, JSON, plain text that reads well at 80 columns, and Prometheus metrics. It has no dependencies to install and no config file. It listens on loopback by default, and aims to stay under 20 MB of memory and 1% of one core.

> **Status: v1 feature-complete.** It serves the host group (uptime, boot time, load, OS and kernel), CPU usage (total and per core, with the P/E split on Apple Silicon), memory and swap (with wired, compressed, and memory pressure on macOS), per-interface network totals and rates, per-mount disk usage with each mount's device read and write rates, and, on Linux, hwmon temperatures, as a dashboard, JSON, 80-column text, and Prometheus metrics. The budget check (memory and CPU over time) is next. See [`docs/ROADMAP.md`](docs/ROADMAP.md).

## Install on a Mac

There are no release downloads yet, so build it on the Mac it will run on. That needs Go (any recent release fetches the pinned 1.27) and the Xcode Command Line Tools (`xcode-select --install`), because the macOS collector uses cgo against system frameworks. It runs on macOS 13 or later.

```sh
git clone https://github.com/nmcginn/vitals && cd vitals
make build                                   # dist/vitals-darwin-arm64, plus the Linux binaries
sudo mkdir -p /usr/local/bin
sudo install -m 755 dist/vitals-darwin-arm64 /usr/local/bin/vitals
vitals -version
```

`sudo` is only for copying into `/usr/local/bin`. vitals itself never needs root. To run it from login onwards and restart it if it exits, install the launchd user agent. launchd doesn't expand `~`, so `sed` fills in your home directory for the log path:

```sh
mkdir -p ~/Library/LaunchAgents
sed "s|__HOME__|$HOME|g" contrib/com.vitals.plist > ~/Library/LaunchAgents/com.vitals.plist
launchctl bootstrap gui/$(id -u) ~/Library/LaunchAgents/com.vitals.plist
curl localhost:9110/healthz                  # ok, within a couple of seconds
```

It runs as you, and logs to `~/Library/Logs/vitals.log`. After installing a new binary, `launchctl kickstart -k gui/$(id -u)/com.vitals` restarts it. `launchctl bootout gui/$(id -u)/com.vitals` stops it and keeps it stopped until the next `bootstrap`. A user agent starts when you log in, so on a headless Mini turn on automatic login (System Settings → Users & Groups) or it won't run after a reboot.

To see the dashboard from your phone, it has to listen beyond loopback: add `<string>-listen</string>` and `<string>0.0.0.0:9110</string>` after the binary in the plist's `ProgramArguments`, then `bootout` and `bootstrap` again. Read the security notes below first.

## Install on Linux

The Linux binaries are static and build anywhere, with `make build` or `CGO_ENABLED=0 go build -o vitals .`. The systemd unit runs vitals as a throwaway unprivileged user (`DynamicUser=yes`) that sees the system read-only:

```sh
make build
sudo install -m 755 dist/vitals-linux-amd64 /usr/local/bin/vitals   # or vitals-linux-arm64
sudo install -m 644 contrib/vitals.service /etc/systemd/system/
sudo systemctl daemon-reload
sudo systemctl enable --now vitals
journalctl -u vitals                        # its log
```

To listen beyond loopback, `sudo systemctl edit vitals` and override `ExecStart` (an empty `ExecStart=` line first, then `ExecStart=/usr/local/bin/vitals -listen 0.0.0.0:9110`).

## Using it

```sh
open http://localhost:9110/              # the dashboard
curl localhost:9110/?format=text         # aligned text for a terminal
curl localhost:9110/api/v1/stats?pretty=1
```

| Endpoint | Returns |
| --- | --- |
| `/` | the dashboard: collapsible cards and a light/dark/auto toggle, both remembered if JavaScript is on, fully usable without it; reloads every sample (`?refresh=N` seconds, `?refresh=0` to stop); `503` before the first sample |
| `/api/v1/stats` | the latest sample as JSON (`?pretty=1` to indent); `503` before the first sample |
| `/api/v1/history` | every buffered sample, oldest first; `?since=<unix seconds>` keeps those taken after it |
| `/metrics` | the latest sample for a Prometheus scrape (exposition format 0.0.4, `vitals_` names, ratios 0 to 1); `503` before the first sample |
| `/healthz` | `ok` once the first sample exists, `503 no sample yet` before |

`/` and `/api/v1/stats` serve any of the four formats: `?format=html`, `json`, `text`, or `prom`, or else the `Accept` header (`text/html`, `application/json`, `text/plain`, and `text/plain; version=0.0.4` or `application/openmetrics-text` for Prometheus). Without either, `/` is the dashboard and `/api/v1/stats` is JSON. A format they can't serve is `406`, listing the ones they can.

Flags: `-listen` (default `127.0.0.1:9110`), `-interval` (`2s`, at least `1s`), `-history` (`15m`), `-disk-exclude` and `-net-exclude` (regexes that replace the built-in lists), `-log-level` (`debug`, `info`, `warn`, `error`), and `-version`. A bad value exits with status 2. There is no config file.

## Security notes

- **Read-only.** Every endpoint answers `GET` and `HEAD` only, and nothing changes anything. Request bodies are ignored.
- **No authentication.** Anyone who can reach the port can read it. That's why it listens on `127.0.0.1` by default and logs a warning when bound anywhere else. Prefer a private network (a VPN or tailnet, or a firewall rule) to exposing it publicly.
- **What it reveals:** the hostname, OS and kernel versions, CPU model, mount points and filesystem types, interface names, and the numbers. It never outputs environment variables, command lines, usernames, or processes.
- **Unprivileged.** It never needs root, never runs another program, and makes no network requests. The dashboard loads nothing beyond the page itself, and its Content-Security-Policy forbids it from doing so.
- **Hardened server.** Header, read, write, and idle timeouts are set, request headers are capped at 16 KB, and every response has `X-Content-Type-Options: nosniff`, `Referrer-Policy: no-referrer`, and `Cache-Control: no-store`.

## Development

```sh
go mod download
make run
make check     # everything CI runs; run before pushing
make shim-check  # on a Mac: lint, analyze, and sanitize the C shim (part of check)
make golden    # regenerate renderer golden files; review the diff
make build     # release binaries into dist/ (the macOS one only on a Mac)
```

These need Go 1.27, pinned in `go.mod` (with `GOTOOLCHAIN=auto`, the default, any recent Go fetches it). `make check` also builds the release binaries and checks them: the Linux ones are static, the macOS one links only system libraries, the launchd and systemd files lint, and `-version` reports what was built.

## How it's built

Most changes are written by Claude and land as one reviewed pull request per night. [`docs/ROADMAP.md`](docs/ROADMAP.md) is the backlog that drives it, [`docs/PLAN.md`](docs/PLAN.md) is the design (from the original [`docs/SPEC.md`](docs/SPEC.md)), and [`CLAUDE.md`](CLAUDE.md) holds the priorities everything is judged by.

## Licence

MIT. See [`LICENSE`](LICENSE).
