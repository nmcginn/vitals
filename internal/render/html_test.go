package render

import (
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"regexp"
	"strings"
	"testing"

	"github.com/nmcginn/vitals/internal/collector"
	"github.com/nmcginn/vitals/internal/sampler"
)

func mustHTML(t *testing.T, s *sampler.Snapshot, h []*sampler.Snapshot, p Page) string {
	t.Helper()
	b, err := HTML(s, h, p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// htmlCases are the golden pages; the property tests below run over all of
// them too.
var htmlCases = []struct {
	name, file string
	snap       func() *sampler.Snapshot
	history    int
}{
	{"a full darwin snapshot with history", "darwin_full.html", darwinFull, 30},
	{"a linux snapshot with temps, a failed group, and awkward disks", "linux_partial.html", linuxPartial, 30},
	{"a first-sample snapshot", "darwin_first.html", darwinFirst, 1},
	{"a snapshot where cpu, memory, and one disk failed", "darwin_errors.html", darwinErrors, 30},
}

func TestHTMLGolden(t *testing.T) {
	for _, tc := range htmlCases {
		t.Run(tc.name+" matches its golden file", func(t *testing.T) {
			s := tc.snap()
			golden(t, tc.file, []byte(mustHTML(t, s, historyOf(s, tc.history), Page{})))
		})
	}
}

// eightInterfaces is darwinFull with eight interfaces, the size PLAN budgets.
func eightInterfaces() *sampler.Snapshot {
	s := darwinFull()
	nic := s.Network[0]
	s.Network = nil
	for i := range 8 {
		nic.Name = fmt.Sprintf("en%d", i)
		s.Network = append(s.Network, nic)
	}
	return s
}

func TestHTMLBudget(t *testing.T) {
	t.Run("with 450 samples of history and 8 interfaces the page is under 50 KB", func(t *testing.T) {
		s := eightInterfaces()
		page := mustHTML(t, s, historyOf(s, 450), Page{})
		if len(page) >= 50_000 {
			t.Errorf("page is %d bytes, want under 50,000", len(page))
		}
		t.Logf("page is %d bytes", len(page))
	})
}

var (
	eventAttr   = regexp.MustCompile(`(?i)\son[a-z]+\s*=`)
	externalURL = regexp.MustCompile(`(?i)https?:`)
	svgOpen     = regexp.MustCompile(`<svg[^>]*>`)
	svgTitled   = regexp.MustCompile(`<svg[^>]*><title>[^<]+</title>`)
)

func TestHTMLProperties(t *testing.T) {
	pages := map[string]string{}
	for _, tc := range htmlCases {
		s := tc.snap()
		pages[tc.name] = mustHTML(t, s, historyOf(s, tc.history), Page{})
	}
	s := eightInterfaces()
	pages["the 450-sample page"] = mustHTML(t, s, historyOf(s, 450), Page{})

	for name, page := range pages {
		t.Run(name+" has one script, the embedded theme script, no event handlers, and no external URLs", func(t *testing.T) {
			if n := strings.Count(strings.ToLower(page), "<script"); n != 1 {
				t.Errorf("page has %d scripts, want 1", n)
			}
			if !strings.Contains(page, "<script>"+string(script)+"</script>") {
				t.Error("the script on the page differs from assets/theme.js, so its hash won't match the CSP")
			}
			if m := eventAttr.FindString(page); m != "" {
				t.Errorf("page has an event attribute: %q", m)
			}
			if m := externalURL.FindString(page); m != "" {
				t.Errorf("page refers to %q", m)
			}
		})

		t.Run(name+" gives every svg a title", func(t *testing.T) {
			open, titled := len(svgOpen.FindAllString(page, -1)), len(svgTitled.FindAllString(page, -1))
			if open == 0 || open != titled {
				t.Errorf("%d svgs, %d with a title first", open, titled)
			}
			want := fmt.Sprintf(`viewBox="0 0 %d %d"`, sparkW, sparkH)
			if n := strings.Count(page, want); n != open {
				t.Errorf("%d of %d svgs have %s, the box sparkPath draws in", n, open, want)
			}
		})
	}
}

func TestHTMLContent(t *testing.T) {
	t.Run("disks and network are tables, and each disk's percentage is text", func(t *testing.T) {
		page := mustHTML(t, linuxPartial(), nil, Page{})
		for _, want := range []string{
			`<summary><h2 id="disks">Disks</h2></summary>` + "\n<table>",
			`<span class="num">20.0%</span>`, `<span class="num">75.0%</span>`,
			// the zero-size filesystem has no percentage, and says so
			`<td class="used"><span class="num">—</span><span class="bar"><i style="width:0%"></i></span>`,
		} {
			if !strings.Contains(page, want) {
				t.Errorf("page lacks %q", want)
			}
		}
		if page := mustHTML(t, darwinFull(), nil, Page{}); !strings.Contains(page, `<summary><h2 id="network">Network</h2></summary>`+"\n<table>") {
			t.Error("network is not a table")
		}
	})

	t.Run("a disk bar is coloured by level and never drawn past its track", func(t *testing.T) {
		s := darwinFull()
		s.Disks = []sampler.Disk{{Mount: "/a", UsedPct: p(85.0)}, {Mount: "/b", UsedPct: p(97.5)}, {Mount: "/c", UsedPct: p(140.0)}}
		page := mustHTML(t, s, nil, Page{})
		for _, want := range []string{
			`class="bar warn"><i style="width:85.0%">`,
			`class="bar crit"><i style="width:97.5%">`,
			`class="bar crit"><i style="width:100.0%">`, `140.0%`,
		} {
			if !strings.Contains(page, want) {
				t.Errorf("page lacks %q", want)
			}
		}
	})

	t.Run("every rate on the first sample is —", func(t *testing.T) {
		page := mustHTML(t, darwinFirst(), historyOf(darwinFirst(), 1), Page{})
		for _, unit := range []string{"B/s", "pkt/s"} {
			if strings.Contains(page, unit) {
				t.Errorf("first-sample page shows a rate in %s", unit)
			}
		}
		if strings.Contains(page, `<span class="big num">—</span>`) == false {
			t.Error("CPU usage is not —")
		}
		if !strings.Contains(page, `<td class="r num">—<br>—</td>`) {
			t.Error("disk read and write are not —")
		}
		if n := strings.Count(page, `<td class="r num">—<small>—</small>`); n != 2 {
			t.Errorf("%d network rate cells are —, want 2 (rx and tx)", n)
		}
		if !strings.Contains(page, "no history yet") {
			t.Error("sparkline titles don't say there is no history yet")
		}
	})

	t.Run("a failed group's card says what failed while the others render", func(t *testing.T) {
		page := mustHTML(t, darwinErrors(), nil, Page{})
		for _, want := range []string{
			`<p class="error">CPU unavailable: host_processor_info: (os/kern) failure</p>`,
			`<p class="error">Memory unavailable: host_statistics64: (os/kern) failure</p>`,
			`<p class="error">Disks: statfs &#34;/Volumes/Backup&#34;: input/output error</p>` + "\n<table>",
			`<th scope="row">en0</th>`,
		} {
			if !strings.Contains(page, want) {
				t.Errorf("page lacks %q", want)
			}
		}
	})

	t.Run("a failed network group shows its error and no empty table", func(t *testing.T) {
		page := mustHTML(t, linuxPartial(), nil, Page{})
		if !strings.Contains(page, "Network unavailable: reading /proc/net/dev: permission denied</p>\n</details>\n</section>") {
			t.Error("network card doesn't end with its error")
		}
	})

	t.Run("filtering out every disk and interface says so", func(t *testing.T) {
		s := darwinFull()
		s.Disks, s.Network = []sampler.Disk{}, []sampler.Interface{}
		page := mustHTML(t, s, nil, Page{})
		for _, want := range []string{"No disks to show.", "No interfaces to show."} {
			if !strings.Contains(page, want) {
				t.Errorf("page lacks %q", want)
			}
		}
	})

	t.Run("a hostname with markup is escaped", func(t *testing.T) {
		s := darwinFull()
		s.Host.Hostname = `<script>alert("x")</script>`
		page := mustHTML(t, s, nil, Page{})
		if strings.Contains(page, "<script>alert") || !strings.Contains(page, "&lt;script&gt;alert(&#34;x&#34;)&lt;/script&gt;") {
			t.Errorf("hostname not escaped:\n%s", page[:min(len(page), 400)])
		}
	})

	t.Run("the temperatures card is omitted when temps is null", func(t *testing.T) {
		if page := mustHTML(t, darwinFull(), nil, Page{}); strings.Contains(page, "Temperatures") {
			t.Error("darwin page has a temperatures card")
		}
		if page := mustHTML(t, linuxPartial(), nil, Page{}); !strings.Contains(page, "<td class=\"r num\">39.9\u00a0°C</td>") {
			t.Error("linux page lacks its temperatures")
		}
	})

	t.Run("a temps error with no sensors still gets a card that says so", func(t *testing.T) {
		s := linuxPartial()
		s.Temps, s.Errors = nil, map[string]string{"temps": "listing /sys/class/hwmon: permission denied"}
		if page := mustHTML(t, s, nil, Page{}); !strings.Contains(page, "Temperatures unavailable: listing /sys/class/hwmon: permission denied") {
			t.Error("the temps error isn't shown")
		}
	})

	t.Run("darwin cores are labelled P or E, and linux cores by number", func(t *testing.T) {
		if page := mustHTML(t, darwinFull(), nil, Page{}); !strings.Contains(page, `<span class="label">P0</span>`) || !strings.Contains(page, `<span class="label">E9</span>`) {
			t.Error("darwin cores aren't tagged")
		}
		if page := mustHTML(t, linuxPartial(), nil, Page{}); !strings.Contains(page, `<span class="label">0</span>`) {
			t.Error("linux core isn't numbered")
		}
	})

	t.Run("an unknown memory pressure level says unknown", func(t *testing.T) {
		s := darwinFull()
		d := *s.Memory.Darwin
		d.Pressure = nil
		m := *s.Memory
		m.Darwin = &d
		s.Memory = &m
		if page := mustHTML(t, s, nil, Page{}); !strings.Contains(page, "<dt>Pressure</dt><dd>unknown</dd>") {
			t.Error("pressure isn't unknown")
		}
	})

	t.Run("no swap says none", func(t *testing.T) {
		if page := mustHTML(t, darwinFull(), nil, Page{}); !strings.Contains(page, `<dt>Swap</dt><dd class="num">none</dd>`) {
			t.Error("zero swap isn't none")
		}
	})

	t.Run("a failed host group shows — for uptime and load", func(t *testing.T) {
		s := darwinFull()
		s.Host.UptimeSeconds, s.Host.BootTime, s.Host.Load = nil, nil, nil
		s.Errors = map[string]string{"host": "sysctl kern.boottime: operation not permitted"}
		page := mustHTML(t, s, nil, Page{})
		for _, want := range []string{
			`up <span class="num">—</span>`,
			`<dt>Load</dt><dd class="num">—</dd>`,
			"Host details unavailable: sysctl kern.boottime: operation not permitted",
		} {
			if !strings.Contains(page, want) {
				t.Errorf("page lacks %q", want)
			}
		}
	})
}

func TestHTMLTheme(t *testing.T) {
	page := mustHTML(t, darwinFull(), nil, Page{})

	t.Run("without JavaScript the toggle stays hidden and the page follows the system", func(t *testing.T) {
		if !strings.Contains(page, `<button type="button" id="theme" hidden>`) {
			t.Error("the toggle isn't hidden in the markup")
		}
		if !strings.Contains(page, "<html lang=\"en\">\n") {
			t.Error("the markup sets a theme on <html>; only the script may")
		}
		if !strings.Contains(page, "@media (prefers-color-scheme: dark)") {
			t.Error("no system dark mode")
		}
	})

	t.Run("the stylesheet honours a chosen light or dark theme over the system's", func(t *testing.T) {
		for _, want := range []string{
			`:root:not([data-theme="light"])`, // system dark, unless light was chosen
			`:root[data-theme="dark"] {`,
			`:root[data-theme="light"] { color-scheme: light; }`,
		} {
			if !strings.Contains(page, want) {
				t.Errorf("stylesheet lacks %s", want)
			}
		}
	})

	t.Run("the script makes no network requests and touches only its own storage keys", func(t *testing.T) {
		js := string(script)
		for _, banned := range []string{"fetch", "XMLHttpRequest", "WebSocket", "EventSource", "sendBeacon", "import", "src", "http", "cookie", "eval", "innerHTML"} {
			if strings.Contains(js, banned) {
				t.Errorf("theme.js uses %q", banned)
			}
		}
		if strings.Count(js, "localStorage") != 2 || strings.Count(js, `"vitals-theme"`) != 2 || strings.Count(js, `"vitals-closed"`) != 2 {
			t.Error("theme.js should read and write only the vitals-theme and vitals-closed keys, through load and save")
		}
	})

	t.Run("ScriptHash is the CSP source for the script's exact bytes", func(t *testing.T) {
		sum := sha256.Sum256([]byte(script))
		if want := "'sha256-" + base64.StdEncoding.EncodeToString(sum[:]) + "'"; ScriptHash != want {
			t.Errorf("ScriptHash = %s, want %s", ScriptHash, want)
		}
	})
}

var cardOpen = regexp.MustCompile(`<section aria-labelledby="(\w+)">\n<details data-card="(\w+)" open>\n<summary><h2 id="(\w+)">`)

func TestHTMLCollapsibleCards(t *testing.T) {
	for _, tc := range htmlCases {
		s := tc.snap()
		page := mustHTML(t, s, historyOf(s, tc.history), Page{})

		t.Run(tc.name+": every card is an open details whose summary is its heading", func(t *testing.T) {
			cards := cardOpen.FindAllStringSubmatch(page, -1)
			if n := strings.Count(page, "<section"); n == 0 || len(cards) != n || strings.Count(page, "</details>\n</section>") != n {
				t.Fatalf("%d sections, %d built as collapsible cards", n, len(cards))
			}
			for _, c := range cards {
				if c[1] != c[2] || c[2] != c[3] {
					t.Errorf("card names disagree: section %q, details %q, heading %q", c[1], c[2], c[3])
				}
			}
		})

		t.Run(tc.name+": every card has a stylesheet rule that hides it closed from first paint", func(t *testing.T) {
			for _, c := range cardOpen.FindAllStringSubmatch(page, -1) {
				rule := `:root[data-closed~="` + c[2] + `"] [data-card="` + c[2] + `"] > :not(summary)`
				if !strings.Contains(page, rule) {
					t.Errorf("no rule %s, so a closed %s card flashes open on each refresh", rule, c[2])
				}
			}
		})
	}

	t.Run("the temperatures card is collapsible too when it appears", func(t *testing.T) {
		if page := mustHTML(t, linuxPartial(), nil, Page{}); !strings.Contains(page, `<details data-card="temps" open>`) {
			t.Error("temps card isn't collapsible")
		}
	})
}

var metaRefresh = regexp.MustCompile(`<meta http-equiv="refresh" content="(\d+)">`)

func TestHTMLRefresh(t *testing.T) {
	for _, tc := range []struct {
		name     string
		interval float64
		page     Page
		want     string // "" for no meta refresh
		self     string
	}{
		{"2 at the default interval", 2, Page{}, "2", "/"},
		{"5 at -interval 5s", 5, Page{}, "5", "/"},
		{"2 at -interval 1s, the minimum", 1, Page{}, "2", "/"},
		{"a fractional interval rounds up", 2.5, Page{}, "3", "/"},
		{"absent with ?refresh=0", 2, Page{Refresh: 0, RefreshSet: true}, "", "/?refresh=0"},
		{"10 with ?refresh=10", 2, Page{Refresh: 10, RefreshSet: true}, "10", "/?refresh=10"},
		{"2 with ?refresh=1", 5, Page{Refresh: 1, RefreshSet: true}, "2", "/?refresh=2"},
	} {
		t.Run("the meta refresh is "+tc.name, func(t *testing.T) {
			s := darwinFull()
			s.IntervalSeconds = tc.interval
			page := mustHTML(t, s, nil, tc.page)
			got := ""
			if m := metaRefresh.FindStringSubmatch(page); m != nil {
				got = m[1]
			}
			if got != tc.want {
				t.Errorf("meta refresh = %q, want %q", got, tc.want)
			}
			if n := strings.Count(page, `http-equiv="refresh"`); n > 1 || (tc.want == "") != (n == 0) {
				t.Errorf("%d refresh tags", n)
			}
			if !strings.Contains(page, `<h1><a href="`+tc.self+`">`) {
				t.Errorf("the link back to the page isn't %q", tc.self)
			}
		})
	}

	t.Run("links back to the dashboard carry ?refresh= through, except the pause toggle", func(t *testing.T) {
		page := mustHTML(t, darwinFull(), nil, Page{Refresh: 10, RefreshSet: true})
		for _, href := range regexp.MustCompile(`href="(/[^"]*)"`).FindAllStringSubmatch(page, -1) {
			switch u := href[1]; {
			case strings.HasPrefix(u, "/api/"):
			case u == "/?refresh=10", u == "/?refresh=0":
			default:
				t.Errorf("dashboard link %q drops ?refresh=10", u)
			}
		}
		if !strings.Contains(page, `<a href="/?refresh=0">Pause refresh</a>`) {
			t.Error("no pause link")
		}
	})

	t.Run("a paused page links to resuming at the default", func(t *testing.T) {
		page := mustHTML(t, darwinFull(), nil, Page{Refresh: 0, RefreshSet: true})
		if !strings.Contains(page, "Refresh paused · <a href=\"/\">Resume refresh</a>") {
			t.Error("no resume link")
		}
	})
}

func TestHumanize(t *testing.T) {
	for _, tc := range []struct {
		got, want string
	}{
		{humanBytes(0), "0 B"},
		{humanBytes(1023), "1023 B"},
		{humanBytes(1024), "1.0 KiB"},
		{humanBytes(17179869184), "16.0 GiB"},
		{humanBytes(1<<64 - 1), "16.0 EiB"},
		{humanRate(nil), "—"},
		{humanRate(p(40960.0)), "40.0 KiB/s"},
		{humanRate(p(0.4)), "0 B/s"},
		{humanPPS(nil), "—"},
		{humanPPS(p(21.4)), "21 pkt/s"},
		{humanPct(nil), "—"},
		{humanPct(p(6.25)), "6.2%"},
		{duration(864000e9), "10d 0h"},
		{duration(3*3600e9 + 12*60e9 + 5e9), "3h 12m"},
		{duration(4*60e9 + 10e9), "4m 10s"},
		{duration(45e9), "45s"},
	} {
		t.Run(fmt.Sprintf("formats as %q", tc.want), func(t *testing.T) {
			if tc.got != tc.want {
				t.Errorf("got %q, want %q", tc.got, tc.want)
			}
		})
	}
}

func TestSystemLine(t *testing.T) {
	for _, tc := range []struct {
		host sampler.Host
		want string
	}{
		{sampler.Host{OS: "darwin", OSVersion: "26.1", Kernel: "25.1.0", Arch: "arm64"}, "macOS 26.1 · Darwin 25.1.0 · arm64"},
		{sampler.Host{OS: "darwin", Kernel: "25.1.0", Arch: "arm64"}, "Darwin 25.1.0 · arm64"},
		{sampler.Host{OS: "linux", OSVersion: "Debian GNU/Linux 13 (trixie)", Kernel: "6.12.48-amd64", Arch: "amd64"}, "Debian GNU/Linux 13 (trixie) · Linux 6.12.48-amd64 · amd64"},
		{sampler.Host{OS: "linux", Kernel: "6.1.0", Arch: "arm64"}, "Linux 6.1.0 · arm64"},
	} {
		t.Run("reads "+tc.want, func(t *testing.T) {
			if got := system(tc.host); got != tc.want {
				t.Errorf("got %q", got)
			}
		})
	}
}

func TestSparkSeries(t *testing.T) {
	t.Run("an interface missing from a sample is a gap in its line", func(t *testing.T) {
		h := historyOf(darwinFull(), 5) // sample 3 has no network group
		rx, _ := netSeries(h, "en0")
		if !isNaN(rx[3]) || isNaN(rx[4]) {
			t.Errorf("rx = %v", rx)
		}
	})

	t.Run("memory with zero total is a gap, not a division by zero", func(t *testing.T) {
		s := darwinFull()
		s.Memory = &collector.Memory{}
		if m := memSeries([]*sampler.Snapshot{s}); !isNaN(m[0]) {
			t.Errorf("m = %v", m)
		}
	})
}

func isNaN(v float64) bool { return v != v }
