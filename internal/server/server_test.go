package server

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"html"
	"io"
	"log/slog"
	"maps"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nmcginn/vitals/internal/collector"
	"github.com/nmcginn/vitals/internal/render"
	"github.com/nmcginn/vitals/internal/sampler"
)

type fakeCollector struct{ calls atomic.Int32 }

func (f *fakeCollector) Collect(context.Context) (collector.Raw, error) {
	f.calls.Add(1)
	return collector.Raw{Host: &collector.Host{UptimeSeconds: 42, BootTime: time.Unix(1700000000, 0), Load: [3]float64{1, 2, 3}}}, nil
}

func (f *fakeCollector) Static() collector.HostInfo {
	return collector.HostInfo{Hostname: "mini", OS: "darwin", Kernel: "25.1.0", Arch: "arm64"}
}

// sampled returns a store holding one real sample, taken by a real sampler,
// and the collector it came from.
func sampled(t *testing.T) (*sampler.Store, *fakeCollector) {
	t.Helper()
	c := &fakeCollector{}
	store := sampler.NewStore(10)
	s := sampler.New(c, store, time.Hour, slog.New(slog.DiscardHandler))
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { s.Run(ctx); close(done) }()
	for deadline := time.Now().Add(5 * time.Second); store.Latest() == nil; time.Sleep(time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("the sampler published nothing")
		}
	}
	cancel()
	<-done
	return store, c
}

type response struct {
	status int
	header http.Header
	body   string
}

func do(t *testing.T, h http.Handler, method, target string, body io.Reader) response {
	t.Helper()
	return send(t, h, method, target, body, nil)
}

func send(t *testing.T, h http.Handler, method, target string, body io.Reader, header http.Header) response {
	t.Helper()
	srv := httptest.NewServer(h)
	defer srv.Close()
	req, err := http.NewRequest(method, srv.URL+target, body)
	if err != nil {
		t.Fatal(err)
	}
	maps.Copy(req.Header, header)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	b, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatal(err)
	}
	return response{res.StatusCode, res.Header, string(b)}
}

func get(t *testing.T, h http.Handler, target string) response {
	t.Helper()
	return do(t, h, http.MethodGet, target, nil)
}

var routes = []string{"/", "/healthz", "/api/v1/stats", "/api/v1/history", "/metrics"}

func TestHealthz(t *testing.T) {
	t.Run("503 no sample yet before the first sample", func(t *testing.T) {
		r := get(t, New(sampler.NewStore(10), Options{}), "/healthz")
		if r.status != 503 || r.body != "no sample yet\n" {
			t.Errorf("got %d %q", r.status, r.body)
		}
	})

	t.Run("200 ok once a sample exists", func(t *testing.T) {
		store, _ := sampled(t)
		r := get(t, New(store, Options{}), "/healthz")
		if r.status != 200 || r.body != "ok\n" || r.header.Get("Content-Type") != typeText {
			t.Errorf("got %d %q %q", r.status, r.header.Get("Content-Type"), r.body)
		}
	})
}

func TestStats(t *testing.T) {
	t.Run("503 before the first sample", func(t *testing.T) {
		r := get(t, New(sampler.NewStore(10), Options{}), "/api/v1/stats")
		if r.status != 503 || r.body != "no sample yet\n" {
			t.Errorf("got %d %q", r.status, r.body)
		}
	})

	store, _ := sampled(t)
	h := New(store, Options{})

	t.Run("the latest snapshot as compact JSON", func(t *testing.T) {
		r := get(t, h, "/api/v1/stats")
		if r.status != 200 || r.header.Get("Content-Type") != typeJSON {
			t.Fatalf("got %d %q", r.status, r.header.Get("Content-Type"))
		}
		var snap struct {
			Host struct {
				Hostname string   `json:"hostname"`
				Uptime   *float64 `json:"uptime_s"`
			} `json:"host"`
			Errors map[string]string `json:"errors"`
		}
		if err := json.Unmarshal([]byte(r.body), &snap); err != nil {
			t.Fatalf("%v: %s", err, r.body)
		}
		if snap.Host.Hostname != "mini" || snap.Host.Uptime == nil || *snap.Host.Uptime != 42 {
			t.Errorf("host = %+v", snap.Host)
		}
		if strings.Count(r.body, "\n") != 1 {
			t.Errorf("compact JSON spans lines:\n%s", r.body)
		}
	})

	t.Run("?pretty=1 indents, and other values don't", func(t *testing.T) {
		if r := get(t, h, "/api/v1/stats?pretty=1"); !strings.Contains(r.body, "\n  \"host\": {") {
			t.Errorf("not indented:\n%s", r.body)
		}
		if r := get(t, h, "/api/v1/stats?pretty=yes"); strings.Count(r.body, "\n") != 1 {
			t.Errorf("?pretty=yes indented:\n%s", r.body)
		}
	})
}

func TestMetrics(t *testing.T) {
	t.Run("503 before the first sample, so the scrape fails", func(t *testing.T) {
		r := get(t, New(sampler.NewStore(10), Options{}), "/metrics")
		if r.status != 503 || r.body != "no sample yet\n" {
			t.Errorf("got %d %q", r.status, r.body)
		}
	})

	store, _ := sampled(t)
	h := New(store, Options{Build: render.Build{Version: "v1.2.3", Commit: "abc1234"}})

	t.Run("the latest snapshot in exposition format 0.0.4", func(t *testing.T) {
		r := get(t, h, "/metrics")
		if r.status != 200 || r.header.Get("Content-Type") != "text/plain; version=0.0.4; charset=utf-8" {
			t.Fatalf("got %d %q", r.status, r.header.Get("Content-Type"))
		}
		for _, want := range []string{
			`vitals_build_info{version="v1.2.3",commit="abc1234"} 1`,
			`vitals_host_info{hostname="mini",`,
			"vitals_uptime_seconds 42\n",
		} {
			if !strings.Contains(r.body, want) {
				t.Errorf("body lacks %q:\n%s", want, r.body)
			}
		}
	})

	t.Run("does not negotiate: ?format= is ignored", func(t *testing.T) {
		r := do(t, h, http.MethodGet, "/metrics?format=json", nil)
		if r.header.Get("Content-Type") != typeProm {
			t.Errorf("Content-Type = %q", r.header.Get("Content-Type"))
		}
	})
}

func TestDashboard(t *testing.T) {
	t.Run("503 before the first sample", func(t *testing.T) {
		r := get(t, New(sampler.NewStore(10), Options{}), "/")
		if r.status != 503 || r.body != "no sample yet\n" {
			t.Errorf("got %d %q", r.status, r.body)
		}
	})

	store, _ := sampled(t)
	h := New(store, Options{})

	t.Run("/ is the HTML dashboard with PLAN's Content-Security-Policy, allowing only its own script", func(t *testing.T) {
		r := get(t, h, "/")
		if r.status != 200 || r.header.Get("Content-Type") != "text/html; charset=utf-8" {
			t.Fatalf("got %d %q", r.status, r.header.Get("Content-Type"))
		}
		// The script source is computed here from the page itself, so this
		// fails if what's served and what the header allows ever drift apart.
		start, end := strings.Index(r.body, "<script>"), strings.Index(r.body, "</script>")
		if start < 0 || end < start {
			t.Fatal("the page has no script")
		}
		sum := sha256.Sum256([]byte(r.body[start+len("<script>") : end]))
		want := "default-src 'none'; style-src 'unsafe-inline'; img-src data:; script-src 'sha256-" + base64.StdEncoding.EncodeToString(sum[:]) + "'"
		if got := r.header.Get("Content-Security-Policy"); got != want {
			t.Errorf("Content-Security-Policy = %q\nwant %q", got, want)
		}
		if !strings.HasPrefix(r.body, "<!doctype html>") || !strings.Contains(r.body, "<h1><a href=\"/\">mini</a></h1>") {
			t.Errorf("not the dashboard:\n%s", r.body)
		}
	})

	t.Run("every link on the dashboard answers 200", func(t *testing.T) {
		links := regexp.MustCompile(`href="(/[^"]*)"`).FindAllStringSubmatch(get(t, h, "/").body, -1)
		if len(links) == 0 {
			t.Fatal("the dashboard has no links")
		}
		for _, l := range links {
			target := html.UnescapeString(l[1])
			if r := get(t, h, target); r.status != 200 {
				t.Errorf("%s is %d", target, r.status)
			}
		}
	})

	t.Run("other paths under / are 404, not the dashboard", func(t *testing.T) {
		if r := get(t, h, "/index.html"); r.status != 404 {
			t.Errorf("got %d", r.status)
		}
	})

	// The sampler here runs at a one-hour interval, so the default refresh
	// is 3600.
	for _, tc := range []struct{ query, want string }{
		{"", `content="3600"`},
		{"?refresh=10", `content="10"`},
		{"?refresh=1", `content="2"`},
		{"?refresh=0", ""},
		{"?refresh=-5", `content="3600"`},
		{"?refresh=2.5", `content="3600"`},
		{"?refresh=soon", `content="3600"`},
		{"?refresh=", `content="3600"`},
		{"?refresh=99999999999999999999", `content="3600"`},
	} {
		name := "the meta refresh for " + tc.query + " is " + tc.want
		if tc.want == "" {
			name = "there is no meta refresh for " + tc.query
		}
		t.Run(name, func(t *testing.T) {
			r := get(t, h, "/"+tc.query)
			has := strings.Contains(r.body, `http-equiv="refresh"`)
			if tc.want == "" && has || tc.want != "" && !strings.Contains(r.body, `<meta http-equiv="refresh" `+tc.want+`>`) {
				t.Errorf("refresh tag wrong in:\n%s", r.body[:min(len(r.body), 300)])
			}
		})
	}
}

func TestHistory(t *testing.T) {
	t.Run("an empty array before the first sample", func(t *testing.T) {
		r := get(t, New(sampler.NewStore(10), Options{}), "/api/v1/history")
		if r.status != 200 || r.body != "[]\n" || r.header.Get("Content-Type") != typeJSON {
			t.Errorf("got %d %q %q", r.status, r.header.Get("Content-Type"), r.body)
		}
	})

	store, _ := sampled(t)
	h := New(store, Options{})
	at := store.Latest().SampledAt.Unix()

	for _, tc := range []struct {
		name, query string
		want        int
	}{
		{"every sample with no ?since", "", 1},
		{"an empty ?since is the same as none", "?since=", 1},
		{"samples after ?since are kept", "?since=" + itoa(at-1), 1},
		{"a sample taken in the ?since second is dropped", "?since=" + itoa(at), 0},
		{"?since in the future gives an empty array", "?since=" + itoa(at+3600), 0},
		{"?since=0 keeps everything", "?since=0", 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := get(t, h, "/api/v1/history"+tc.query)
			var got []map[string]any
			if err := json.Unmarshal([]byte(r.body), &got); err != nil || r.status != 200 {
				t.Fatalf("%d %v: %s", r.status, err, r.body)
			}
			if len(got) != tc.want || got == nil {
				t.Errorf("%d samples (%s), want %d", len(got), r.body, tc.want)
			}
		})
	}

	for _, v := range []string{"abc", "1.5", "1e9", "-", "99999999999999999999"} {
		t.Run("?since="+v+" is 400 naming the parameter", func(t *testing.T) {
			r := get(t, h, "/api/v1/history?since="+v)
			if r.status != 400 || !strings.Contains(r.body, "since") || r.header.Get("Content-Type") != typeText {
				t.Errorf("got %d %q", r.status, r.body)
			}
		})
	}
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }

func TestSince(t *testing.T) {
	t0 := time.Date(2026, 10, 21, 14, 2, 6, 0, time.UTC)
	h := []*sampler.Snapshot{{SampledAt: t0}, {SampledAt: t0.Add(2 * time.Second)}, {SampledAt: t0.Add(4 * time.Second)}}
	for _, tc := range []struct {
		name  string
		after time.Duration
		want  int
	}{
		{"a time before the oldest sample keeps all of them", -time.Second, 3},
		{"the oldest sample's own time drops just that one", 0, 2},
		{"a time between samples keeps the later ones", 3 * time.Second, 1},
		{"the newest sample's time keeps none", 4 * time.Second, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := since(h, t0.Add(tc.after))
			if len(got) != tc.want || (tc.want > 0 && got[len(got)-1] != h[2]) {
				t.Errorf("since(t0%+v) kept %d, want the newest %d", tc.after, len(got), tc.want)
			}
		})
	}
	t.Run("an empty history stays empty", func(t *testing.T) {
		if got := since(nil, t0); len(got) != 0 {
			t.Errorf("got %d", len(got))
		}
	})
}

func TestMethodsAndHeaders(t *testing.T) {
	store, c := sampled(t)
	h := New(store, Options{})
	empty := New(sampler.NewStore(10), Options{})

	for _, route := range routes {
		t.Run("HEAD "+route+" sends the GET headers and no body", func(t *testing.T) {
			g, r := get(t, h, route), do(t, h, http.MethodHead, route, nil)
			if r.status != 200 || r.body != "" {
				t.Errorf("HEAD = %d with body %q", r.status, r.body)
			}
			if r.header.Get("Content-Type") != g.header.Get("Content-Type") || r.header.Get("Content-Length") != g.header.Get("Content-Length") {
				t.Errorf("HEAD headers %v differ from GET %v", r.header, g.header)
			}
		})

		for _, m := range []string{http.MethodPost, http.MethodPut, http.MethodDelete, http.MethodPatch, http.MethodOptions} {
			t.Run(m+" "+route+" is 405 with Allow: GET, HEAD", func(t *testing.T) {
				r := do(t, h, m, route, strings.NewReader("x=1"))
				if r.status != 405 || r.header.Get("Allow") != "GET, HEAD" {
					t.Errorf("got %d, Allow %q", r.status, r.header.Get("Allow"))
				}
			})
		}
	}

	t.Run("a GET with a body ignores it", func(t *testing.T) {
		if r := do(t, h, http.MethodGet, "/healthz", strings.NewReader(strings.Repeat("x", 1<<16))); r.status != 200 {
			t.Errorf("got %d", r.status)
		}
	})

	cases := map[string]response{
		"200":         get(t, h, "/api/v1/stats"),
		"503":         get(t, empty, "/healthz"),
		"400":         get(t, h, "/api/v1/history?since=abc"),
		"404":         get(t, h, "/nope"),
		"405":         do(t, h, http.MethodPost, "/healthz", nil),
		"HEAD":        do(t, h, http.MethodHead, "/api/v1/history", nil),
		"trailing /":  get(t, h, "/healthz/"),
		"history 200": get(t, h, "/api/v1/history"),
		"dashboard":   get(t, h, "/"),
		"metrics":     get(t, h, "/metrics"),
	}
	for name, r := range cases {
		t.Run(name+" responses carry no-store, nosniff, no-referrer, and a utf-8 Content-Type", func(t *testing.T) {
			for k, want := range map[string]string{
				"Cache-Control":          "no-store",
				"X-Content-Type-Options": "nosniff",
				"Referrer-Policy":        "no-referrer",
			} {
				if got := r.header.Get(k); got != want {
					t.Errorf("%s = %q, want %q", k, got, want)
				}
			}
			if ct := r.header.Get("Content-Type"); !strings.HasSuffix(ct, "charset=utf-8") {
				t.Errorf("Content-Type = %q", ct)
			}
		})
	}

	t.Run("serving requests never calls the collector", func(t *testing.T) {
		before := c.calls.Load()
		for _, route := range routes {
			get(t, h, route)
		}
		if after := c.calls.Load(); after != before {
			t.Errorf("collector called %d times by requests", after-before)
		}
	})
}
