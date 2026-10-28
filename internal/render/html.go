package render

import (
	"bytes"
	"crypto/sha256"
	"embed"
	"encoding/base64"
	"fmt"
	"html/template"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/nmcginn/vitals/internal/collector"
	"github.com/nmcginn/vitals/internal/sampler"
)

//go:embed assets/dashboard.tmpl assets/style.css assets/theme.js
var assets embed.FS

var dashboardTmpl = template.Must(template.New("dashboard.tmpl").Funcs(template.FuncMap{
	"bytes":  func(n uint64) string { return nbsp(humanBytes(n)) },
	"rate":   func(bps *float64) string { return nbsp(humanRate(bps)) },
	"pps":    func(pps *float64) string { return nbsp(humanPPS(pps)) },
	"pct":    humanPct,
	"mempct": func(m *collector.Memory) string { return humanPct(memPct(m)) },
	"width":  barWidth,
	"level":  barLevel,
	"temp":   func(c float64) string { return strconv.FormatFloat(c, 'f', 1, 64) + " °C" },
	"load":   func(l [3]float64) string { return fmt.Sprintf("%.2f %.2f %.2f", l[0], l[1], l[2]) },
}).ParseFS(assets, "assets/dashboard.tmpl"))

// nbsp keeps a figure and its unit on one line when a narrow table wraps.
func nbsp(s string) string { return strings.ReplaceAll(s, " ", " ") }

// style and script are inlined into every page, so a page load is one
// response.
var (
	style  = template.CSS(asset("assets/style.css"))
	script = template.JS(asset("assets/theme.js"))
)

// ScriptHash is the CSP source that allows the page's one inline script and
// nothing else (decision 34). It is the hash of the exact bytes between the
// <script> tags, which html/template writes unchanged for a template.JS value.
var ScriptHash = func() string {
	sum := sha256.Sum256([]byte(script))
	return "'sha256-" + base64.StdEncoding.EncodeToString(sum[:]) + "'"
}()

func asset(name string) string {
	b, err := assets.ReadFile(name)
	if err != nil {
		panic(err) // embedded at build time, so only a bad name gets here
	}
	return string(b)
}

// Page is what the request adds to the snapshot: the ?refresh= it asked for.
type Page struct {
	Refresh    int  // seconds; 0 turns the meta refresh off
	RefreshSet bool // false when ?refresh= was absent or not a whole number ≥ 0
}

// minRefresh is PLAN's floor: reloading faster than the sampler can't show
// anything new.
const minRefresh = 2

// HTML renders the dashboard: the latest snapshot, with sparklines drawn from
// history (oldest first).
func HTML(s *sampler.Snapshot, history []*sampler.Snapshot, p Page) ([]byte, error) {
	v := dashboard{
		S:      s,
		Style:  style,
		Script: script,
		System: system(s.Host),
		Uptime: "—",
		Window: window(history),
		Cores:  cores(s.CPU),
	}
	if u := s.Host.UptimeSeconds; u != nil {
		v.Uptime = duration(time.Duration(*u) * time.Second)
	}
	v.Refresh, v.Self = refresh(s.IntervalSeconds, p)
	if v.Refresh > 0 {
		v.Toggle, v.ToggleLabel = "/?refresh=0", "Pause refresh"
	} else {
		v.Toggle, v.ToggleLabel = "/", "Resume refresh"
	}

	cpu := cpuSeries(history)
	v.CPUSpark = spark{Path: sparkPath(cpu, 100), Title: v.title("CPU usage", "now "+humanPct(cpuNow(s)))}
	mem := memSeries(history)
	v.MemSpark = spark{Path: sparkPath(mem, 100), Title: v.title("Memory used", "now "+humanPct(memPct(s.Memory)))}
	for _, n := range s.Network {
		rx, tx := netSeries(history, n.Name)
		top := peak(rx, tx) // one scale, so rx and tx compare at a glance
		v.NetSparks = append(v.NetSparks, netSpark{
			Rx:    sparkPath(rx, top),
			Tx:    sparkPath(tx, top),
			Title: v.title(n.Name+" receive (solid) and send (dashed)", "peak "+humanRate(&top)),
		})
	}

	var b bytes.Buffer
	if err := dashboardTmpl.Execute(&b, v); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}

type dashboard struct {
	S           *sampler.Snapshot
	Style       template.CSS
	Script      template.JS
	Refresh     int    // the meta refresh in seconds; 0 for none
	Self        string // this page, carrying ?refresh= when the request set it
	Toggle      string // this page with the refresh paused, or resumed
	ToggleLabel string
	System      string // "macOS 26.1 · Darwin 25.1.0 · arm64"
	Uptime      string
	Window      string // how far back the sparklines reach: "15 min"
	Cores       []core
	CPUSpark    spark
	MemSpark    spark
	NetSparks   []netSpark // one per S.Network entry, in the same order
}

type spark struct{ Path, Title string }

type netSpark struct{ Rx, Tx, Title string }

type core struct {
	Label string // "P3", or "3" where cores have no kind
	Pct   *float64
}

// title is a sparkline's accessible name: what it shows, over how long, and
// a figure to read it by.
func (v dashboard) title(what, figure string) string {
	if v.Window == "" {
		return what + ": no history yet"
	}
	return what + " over the last " + v.Window + ", " + figure
}

// refresh picks the meta refresh and the link back to this page. Without a
// valid ?refresh=, the page reloads once per sample and links carry nothing,
// so they follow the interval. An explicit value is kept on the links, after
// raising anything from 1 to the minimum.
func refresh(interval float64, p Page) (secs int, self string) {
	if !p.RefreshSet {
		return max(int(math.Ceil(interval)), minRefresh), "/"
	}
	secs = p.Refresh
	if secs > 0 {
		secs = max(secs, minRefresh)
	}
	return secs, "/?refresh=" + strconv.Itoa(secs)
}

// system is the header's platform line.
func system(h sampler.Host) string {
	product, kernel := h.OSVersion, h.OS+" "+h.Kernel
	switch h.OS {
	case "darwin":
		kernel = "Darwin " + h.Kernel
		if product != "" {
			product = "macOS " + product
		}
	case "linux":
		kernel = "Linux " + h.Kernel
	}
	var parts []string
	for _, p := range []string{product, kernel, h.Arch} {
		if p != "" {
			parts = append(parts, p)
		}
	}
	return strings.Join(parts, " · ")
}

// cores pairs each core's usage with its kind (P or E) where the platform
// tags them.
func cores(c *sampler.CPU) []core {
	if c == nil {
		return nil
	}
	out := make([]core, len(c.PerCorePct))
	for i, pct := range c.PerCorePct {
		label := strconv.Itoa(i)
		if c.Darwin != nil && i < len(c.Darwin.CoreKinds) {
			label = c.Darwin.CoreKinds[i] + label
		}
		out[i] = core{Label: label, Pct: pct}
	}
	return out
}

func cpuNow(s *sampler.Snapshot) *float64 {
	if s.CPU == nil {
		return nil
	}
	return s.CPU.UsagePct
}

// memPct is used memory as a percentage of total, nil when unknown.
func memPct(m *collector.Memory) *float64 {
	if pct := memUsedPct(m); !math.IsNaN(pct) {
		return &pct
	}
	return nil
}

// memUsedPct is memPct as a value, NaN when unknown, so a sparkline can read
// every sample without allocating a pointer for each.
func memUsedPct(m *collector.Memory) float64 {
	if m == nil || m.TotalBytes == 0 {
		return math.NaN()
	}
	return float64(m.UsedBytes) / float64(m.TotalBytes) * 100
}

// The series functions draw history (oldest first) as at most sparkPoints
// values each.

func cpuSeries(h []*sampler.Snapshot) series {
	return downsample(len(h), func(i int) float64 { return known(cpuNow(h[i])) })
}

func memSeries(h []*sampler.Snapshot) series {
	return downsample(len(h), func(i int) float64 { return memUsedPct(h[i].Memory) })
}

// netSeries is one interface's rx and tx rates. A snapshot without the
// interface (it appeared later, or the group failed) is a gap.
func netSeries(h []*sampler.Snapshot, name string) (rx, tx series) {
	rx = downsample(len(h), func(i int) float64 { return known(netRate(h[i], name, false)) })
	tx = downsample(len(h), func(i int) float64 { return known(netRate(h[i], name, true)) })
	return rx, tx
}

// netRate is the named interface's rx or tx rate in s, nil when s doesn't
// list it.
func netRate(s *sampler.Snapshot, name string, tx bool) *float64 {
	for _, n := range s.Network {
		if n.Name == name {
			if tx {
				return n.TxBps
			}
			return n.RxBps
		}
	}
	return nil
}

func known(v *float64) float64 {
	if v == nil {
		return math.NaN()
	}
	return *v
}

// window is how much time the history spans, rounded for a label. It is ""
// with fewer than two samples, when there is no line to label.
func window(h []*sampler.Snapshot) string {
	if len(h) < 2 {
		return ""
	}
	d := h[len(h)-1].SampledAt.Sub(h[0].SampledAt)
	if d < time.Minute {
		return strconv.Itoa(int(d.Seconds())) + " s"
	}
	return strconv.Itoa(int(math.Round(d.Minutes()))) + " min"
}

// duration is an uptime in its two largest units: "10d 4h", "3h 12m", "45s".
func duration(d time.Duration) string {
	days, d := d/(24*time.Hour), d%(24*time.Hour)
	hours, d := d/time.Hour, d%time.Hour
	mins, secs := d/time.Minute, (d%time.Minute)/time.Second
	switch {
	case days > 0:
		return fmt.Sprintf("%dd %dh", days, hours)
	case hours > 0:
		return fmt.Sprintf("%dh %dm", hours, mins)
	case mins > 0:
		return fmt.Sprintf("%dm %ds", mins, secs)
	}
	return fmt.Sprintf("%ds", secs)
}

// humanBytes uses binary units, one decimal place from KiB up.
func humanBytes(n uint64) string {
	const units = "KMGTPE"
	if n < 1024 {
		return strconv.FormatUint(n, 10) + " B"
	}
	v, i := float64(n)/1024, 0
	for v >= 1024 && i < len(units)-1 {
		v /= 1024
		i++
	}
	return strconv.FormatFloat(v, 'f', 1, 64) + " " + units[i:i+1] + "iB"
}

// humanRate is bytes per second, or "—" when the rate can't be known.
func humanRate(bps *float64) string {
	if bps == nil {
		return "—"
	}
	return humanBytes(uint64(math.Round(*bps))) + "/s"
}

func humanPPS(pps *float64) string {
	if pps == nil {
		return "—"
	}
	return strconv.FormatFloat(*pps, 'f', 0, 64) + " pkt/s"
}

func humanPct(pct *float64) string {
	if pct == nil {
		return "—"
	}
	return strconv.FormatFloat(*pct, 'f', 1, 64) + "%"
}

// barWidth is a bar's CSS width in percent, clamped so a bad value can't draw
// outside its track. Unknown is an empty bar; the text beside it says "—".
func barWidth(pct *float64) string {
	if pct == nil {
		return "0"
	}
	return strconv.FormatFloat(min(max(*pct, 0), 100), 'f', 1, 64)
}

// barLevel colours a bar. The percentage is always printed beside it, so the
// colour only reinforces.
func barLevel(pct *float64) string {
	switch {
	case pct == nil:
		return ""
	case *pct >= 90:
		return "crit"
	case *pct >= 80:
		return "warn"
	}
	return ""
}
