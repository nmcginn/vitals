package render

import (
	"bytes"
	"embed"
	"fmt"
	"html/template"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/nmcginn/vitals/internal/collector"
	"github.com/nmcginn/vitals/internal/sampler"
)

//go:embed assets/dashboard.tmpl assets/style.css
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

// style is inlined into every page, so a page load is one response.
var style = func() template.CSS {
	b, err := assets.ReadFile("assets/style.css")
	if err != nil {
		panic(err)
	}
	return template.CSS(b)
}()

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

	cpu := downsample(cpuSeries(history))
	v.CPUSpark = spark{Path: sparkPath(cpu, 100), Title: v.title("CPU usage", "now "+humanPct(cpuNow(s)))}
	mem := downsample(memSeries(history))
	v.MemSpark = spark{Path: sparkPath(mem, 100), Title: v.title("Memory used", "now "+humanPct(memPct(s.Memory)))}
	for _, n := range s.Network {
		rx, tx := netSeries(history, n.Name)
		rx, tx = downsample(rx), downsample(tx)
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
	if m == nil || m.TotalBytes == 0 {
		return nil
	}
	pct := float64(m.UsedBytes) / float64(m.TotalBytes) * 100
	return &pct
}

func cpuSeries(h []*sampler.Snapshot) series {
	out := make(series, len(h))
	for i, s := range h {
		out[i] = known(cpuNow(s))
	}
	return out
}

func memSeries(h []*sampler.Snapshot) series {
	out := make(series, len(h))
	for i, s := range h {
		out[i] = known(memPct(s.Memory))
	}
	return out
}

// netSeries is one interface's rx and tx rates. A snapshot without the
// interface (it appeared later, or the group failed) is a gap.
func netSeries(h []*sampler.Snapshot, name string) (rx, tx series) {
	rx, tx = make(series, len(h)), make(series, len(h))
	for i, s := range h {
		rx[i], tx[i] = math.NaN(), math.NaN()
		for _, n := range s.Network {
			if n.Name == name {
				rx[i], tx[i] = known(n.RxBps), known(n.TxBps)
				break
			}
		}
	}
	return rx, tx
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
