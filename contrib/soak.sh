#!/bin/sh
# soak.sh runs vitals at its default interval and history for a while, polling
# /healthz and the dashboard as a phone left open on it would, and reports the
# process's RSS and CPU time as it goes. It exits 1 when the last reading is
# over the budget: 20 MB of RSS, or 1% of one core averaged over the run.
# Megabytes are decimal, as in the spec and Activity Monitor.
#
# Usage: contrib/soak.sh [minutes]   (make soak, SOAK_MINUTES=60 make soak)
#
# The ring buffer fills after 15 minutes at the defaults, so only a run longer
# than that shows whether RSS stops growing once it is full.
#
# It listens on 127.0.0.1:9111 (SOAK_LISTEN to change it), so a vitals already
# running on the default port doesn't get in the way. It reads RSS and CPU time
# from /proc on Linux and from ps on macOS. The polling and measuring spawn
# processes; vitals itself spawns none.
set -eu

minutes=${1:-5}
listen=${SOAK_LISTEN:-127.0.0.1:9111}
url=http://$listen

tmp=$(mktemp -d)
pid=
cleanup() {
	if [ -n "$pid" ]; then kill "$pid" 2>/dev/null || true; fi
	rm -rf "$tmp"
}
trap cleanup EXIT
trap 'exit 130' INT TERM

go build -o "$tmp/vitals" .
"$tmp/vitals" -listen "$listen" 2>"$tmp/log" &
pid=$!

# rss_kib and cpu_cs print the process's resident set in KiB and its user plus
# system CPU time in hundredths of a second.
case $(uname -s) in
Linux)
	hz=$(getconf CLK_TCK)
	rss_kib() { awk '/^VmRSS:/ { print $2 }' "/proc/$pid/status"; }
	# Fields 14 and 15, counted after the ")" that ends the command name,
	# which may itself contain spaces.
	cpu_cs() { sed 's/.*) //' "/proc/$pid/stat" | awk -v hz="$hz" '{ printf "%d\n", ($12 + $13) * 100 / hz }'; }
	;;
Darwin)
	rss_kib() { ps -o rss= -p "$pid" | tr -d ' '; }
	# ps prints [[dd-]hh:]mm:ss.ss, so the fields from the right are seconds,
	# minutes, hours, and days.
	cpu_cs() {
		ps -o time= -p "$pid" | awk '{
			n = split($1, f, /[-:]/); m[1] = 1; m[2] = 60; m[3] = 3600; m[4] = 86400; s = 0
			for (k = 1; k <= n; k++) s += f[n - k + 1] * m[k]
			printf "%d\n", s * 100
		}'
	}
	;;
*)
	echo "soak: no way to read RSS and CPU time on $(uname -s)" >&2
	exit 1
	;;
esac

# Wait for the first sample, which /healthz reports.
i=0
until curl -fs -o /dev/null "$url/healthz"; do
	i=$((i + 1))
	if [ $i -gt 50 ] || ! kill -0 "$pid" 2>/dev/null; then
		echo "soak: vitals didn't become healthy on $url; its log:" >&2
		cat "$tmp/log" >&2
		exit 1
	fi
	sleep 0.2
done

start=$(date +%s)
cpu0=$(cpu_cs)
printf 'soak: %s minutes on %s, pid %s\n' "$minutes" "$listen" "$pid"
printf '%8s %10s %12s %10s\n' elapsed rss_mb cpu_s avg_cpu_pct
report() {
	now=$(date +%s)
	rss=$(rss_kib)
	cpu=$(cpu_cs)
	awk -v t=$((now - start)) -v rss="$rss" -v cpu=$((cpu - cpu0)) 'BEGIN {
		printf "%7ds %10.1f %12.2f %10.2f\n", t, rss * 1024 / 1e6, cpu / 100, (t > 0 ? cpu / t : 0)
	}'
}
report

end=$((start + minutes * 60))
next=$((start + 60))
while [ "$(date +%s)" -lt "$end" ]; do
	if ! curl -fs -o /dev/null "$url/healthz" || ! curl -fs -o /dev/null "$url/"; then
		echo "soak: a poll of $url failed; vitals's log:" >&2
		cat "$tmp/log" >&2
		exit 1
	fi
	if [ "$(date +%s)" -ge "$next" ]; then
		report
		next=$((next + 60))
	fi
	sleep 2
done
report

# The verdict is on the last reading: RSS now, and CPU averaged over the run.
now=$(date +%s)
awk -v t=$((now - start)) -v rss="$(rss_kib)" -v cpu=$(($(cpu_cs) - cpu0)) 'BEGIN {
	pct = cpu / t
	over = 0
	mb = rss * 1024 / 1e6
	if (mb >= 20) { printf "soak: RSS is %.1f MB, over the 20 MB budget\n", mb; over = 1 }
	if (pct >= 1) { printf "soak: CPU averaged %.2f%% of one core, over the 1%% budget\n", pct; over = 1 }
	if (!over) printf "soak: within budget: %.1f MB RSS, %.2f%% of one core\n", mb, pct
	exit over
}'
