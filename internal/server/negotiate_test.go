package server

import (
	"net/http"
	"strings"
	"testing"

	"github.com/nmcginn/vitals/internal/sampler"
)

func TestNegotiate(t *testing.T) {
	const fail format = "406"
	for _, tc := range []struct {
		name          string
		query, accept string
		def           format
		want          format
	}{
		{"?format=html is the dashboard", "html", "", formatJSON, formatHTML},
		{"?format=json is JSON", "json", "", formatHTML, formatJSON},
		{"?format=text is text", "text", "", formatHTML, formatText},
		{"?format=prom is Prometheus", "prom", "", formatHTML, formatProm},
		{"?format= beats Accept", "text", "application/json", formatHTML, formatText},
		{"?format=xml is 406", "xml", "", formatHTML, fail},
		{"?format= is case-sensitive, like its other parameters", "JSON", "", formatHTML, fail},
		{"an unknown ?format= is 406 even when Accept would match", "xml", "text/html", formatHTML, fail},
		{"an empty ?format= is no format", "", "application/json", formatHTML, formatJSON},

		{"a missing Accept is the default on /", "", "", formatHTML, formatHTML},
		{"a missing Accept is the default on /api/v1/stats", "", "", formatJSON, formatJSON},
		{"*/* is the default on /", "", "*/*", formatHTML, formatHTML},
		{"*/* is the default on /api/v1/stats", "", "*/*", formatJSON, formatJSON},
		{"Accept: application/json is JSON", "", "application/json", formatHTML, formatJSON},
		{"Accept: text/html is HTML", "", "text/html", formatJSON, formatHTML},
		{"Accept: text/plain is text", "", "text/plain", formatHTML, formatText},
		{"a higher q wins over header order", "", "text/html;q=0.5, application/json;q=0.9", formatHTML, formatJSON},
		{"equal q goes to the type listed first", "", "application/json, text/html", formatHTML, formatJSON},
		{"q=0 excludes a type even under */*", "", "text/html;q=0, */*", formatHTML, formatText},
		{"q=0 on the only type is 406", "", "application/json;q=0", formatJSON, fail},
		{"*/*;q=0 with nothing else is 406", "", "*/*;q=0", formatHTML, fail},
		{"text/* on / is HTML, its default", "", "text/*", formatHTML, formatHTML},
		{"text/* on /api/v1/stats is text", "", "text/*", formatJSON, formatText},
		{"text/* loses to a higher-q exact type", "", "text/*;q=0.5, application/json", formatHTML, formatJSON},
		{"an exact type beats text/* at the same q, by order", "", "text/plain, text/*", formatHTML, formatText},
		{"text/html;q=0 with text/* is text", "", "text/html;q=0, text/*", formatHTML, formatText},
		{"text/plain; version=0.0.4 is Prometheus", "", "text/plain; version=0.0.4", formatHTML, formatProm},
		{"a quoted version is Prometheus too", "", `text/plain;version="0.0.4"`, formatHTML, formatProm},
		{"bare text/plain is text, not Prometheus", "", "text/plain", formatJSON, formatText},
		{"text/plain with another version is text", "", "text/plain; version=1.0.0", formatHTML, formatText},
		{"application/openmetrics-text is Prometheus", "", "application/openmetrics-text; version=1.0.0", formatHTML, formatProm},
		{"a wildcard never reaches Prometheus", "", "image/png, text/plain;version=0.0.4;q=0.1, */*;q=0.5", formatHTML, formatHTML},
		{"*/* with every other format excluded is 406, not Prometheus", "", "text/html;q=0, text/plain;q=0, application/json;q=0, */*", formatHTML, fail},
		{"Prometheus excluded with q=0 still leaves text/plain as text", "", "text/plain;version=0.0.4;q=0, text/plain", formatHTML, formatText},
		{"Accept: image/png is 406", "", "image/png", formatHTML, fail},
		{"image/png, */*;q=0.1 is the default", "", "image/png, */*;q=0.1", formatHTML, formatHTML},
		{"image/png, */*;q=0.1 is the default on /api/v1/stats", "", "image/png, */*;q=0.1", formatJSON, formatJSON},
		{"a browser's Accept is HTML even on /api/v1/stats", "", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8", formatJSON, formatHTML},
		{"curl's */* is the default", "", "*/*", formatJSON, formatJSON},
		{"types and parameters are case-insensitive", "", "Application/JSON; Q=1", formatHTML, formatJSON},
		{"spaces around ranges are ignored", "", "  text/plain  ;  q=0.3 ,  text/html ; q=0.2 ", formatJSON, formatText},
		{"a malformed range is dropped, leaving the default", "", "garbage", formatJSON, formatJSON},
		{"a range with a bad q is dropped", "", "text/html;q=high, application/json;q=0.1", formatHTML, formatJSON},
		{"a q above 1 is dropped", "", "text/html;q=2", formatJSON, formatJSON},
		{"*/html is not a valid range and is dropped", "", "*/html", formatJSON, formatJSON},
		{"an empty Accept is the default", "", " ", formatHTML, formatHTML},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := negotiate(tc.query, tc.accept, tc.def)
			if err != nil {
				got = fail
			}
			if got != tc.want {
				t.Errorf("negotiate(%q, %q, %s) = %s, want %s", tc.query, tc.accept, tc.def, got, tc.want)
			}
		})
	}
}

func TestNegotiatedEndpoints(t *testing.T) {
	store, _ := sampled(t)
	h := New(store, Options{})
	empty := New(sampler.NewStore(10), Options{})

	types := map[string]string{"html": typeHTML, "json": typeJSON, "text": typeText, "prom": typeProm}
	for _, route := range []string{"/", "/api/v1/stats"} {
		for f, ctype := range types {
			t.Run(route+"?format="+f+" is "+ctype+" with Vary: Accept", func(t *testing.T) {
				r := get(t, h, route+"?format="+f)
				if r.status != 200 || r.header.Get("Content-Type") != ctype || r.header.Get("Vary") != "Accept" {
					t.Errorf("got %d %q, Vary %q", r.status, r.header.Get("Content-Type"), r.header.Get("Vary"))
				}
				if hasCSP := r.header.Get("Content-Security-Policy") != ""; hasCSP != (f == "html") {
					t.Errorf("Content-Security-Policy = %q", r.header.Get("Content-Security-Policy"))
				}
			})
			t.Run("HEAD "+route+"?format="+f+" sends the GET headers and no body", func(t *testing.T) {
				g, r := get(t, h, route+"?format="+f), do(t, h, http.MethodHead, route+"?format="+f, nil)
				if r.status != 200 || r.body != "" || r.header.Get("Content-Type") != g.header.Get("Content-Type") ||
					r.header.Get("Content-Length") != g.header.Get("Content-Length") {
					t.Errorf("HEAD = %d %q, GET %q", r.status, r.header, g.header)
				}
			})
		}

		t.Run(route+"?format=xml is 406 listing the supported formats, with Vary: Accept", func(t *testing.T) {
			r := get(t, h, route+"?format=xml")
			if r.status != 406 || r.header.Get("Content-Type") != typeText || r.header.Get("Vary") != "Accept" {
				t.Fatalf("got %d %q, Vary %q", r.status, r.header.Get("Content-Type"), r.header.Get("Vary"))
			}
			for _, line := range strings.Split(r.body, "\n") {
				if len(line) > 80 {
					t.Errorf("406 body line wider than 80 columns: %q", line)
				}
			}
			for _, want := range []string{"?format=html, json, text, or prom", "text/html", "application/json", "text/plain", "version=0.0.4", "application/openmetrics-text"} {
				if !strings.Contains(r.body, want) {
					t.Errorf("406 body lacks %q:\n%s", want, r.body)
				}
			}
		})

		t.Run(route+" with Accept: image/png is 406", func(t *testing.T) {
			r := doAccept(t, h, http.MethodGet, route, "image/png")
			if r.status != 406 {
				t.Errorf("got %d", r.status)
			}
		})

		t.Run("POST "+route+" is 405, not 406, whatever format it asks for", func(t *testing.T) {
			r := doAccept(t, h, http.MethodPost, route+"?format=xml", "image/png")
			if r.status != 405 || r.header.Get("Allow") != "GET, HEAD" {
				t.Errorf("got %d, Allow %q", r.status, r.header.Get("Allow"))
			}
		})

		t.Run(route+" before the first sample is 503 with Vary: Accept", func(t *testing.T) {
			r := get(t, empty, route+"?format=text")
			if r.status != 503 || r.header.Get("Vary") != "Accept" {
				t.Errorf("got %d, Vary %q", r.status, r.header.Get("Vary"))
			}
		})
	}

	t.Run("Accept chooses the format when ?format= is absent", func(t *testing.T) {
		if r := doAccept(t, h, http.MethodGet, "/", "application/json"); r.header.Get("Content-Type") != typeJSON {
			t.Errorf("/ with Accept: application/json is %q", r.header.Get("Content-Type"))
		}
		if r := doAccept(t, h, http.MethodGet, "/api/v1/stats", "text/plain"); r.header.Get("Content-Type") != typeText {
			t.Errorf("/api/v1/stats with Accept: text/plain is %q", r.header.Get("Content-Type"))
		}
	})

	t.Run("/?format=json is the same JSON as /api/v1/stats", func(t *testing.T) {
		if a, b := get(t, h, "/?format=json&pretty=1"), get(t, h, "/api/v1/stats?pretty=1"); a.body != b.body {
			t.Errorf("bodies differ:\n%s\n%s", a.body, b.body)
		}
	})

	t.Run("/?format=prom is the same exposition as /metrics", func(t *testing.T) {
		if a, b := get(t, h, "/?format=prom"), get(t, h, "/metrics"); a.body != b.body {
			t.Errorf("bodies differ:\n%s\n%s", a.body, b.body)
		}
	})

	t.Run("/?format=text is the text renderer, under 80 columns", func(t *testing.T) {
		r := get(t, h, "/?format=text")
		if !strings.HasPrefix(r.body, "mini · ") {
			t.Errorf("not the text output:\n%s", r.body)
		}
	})

	t.Run("endpoints that don't negotiate set no Vary", func(t *testing.T) {
		for _, route := range []string{"/healthz", "/api/v1/history", "/metrics"} {
			if v := get(t, h, route).header.Get("Vary"); v != "" {
				t.Errorf("%s has Vary %q", route, v)
			}
		}
	})
}

func doAccept(t *testing.T, h http.Handler, method, target, accept string) response {
	t.Helper()
	return send(t, h, method, target, nil, http.Header{"Accept": {accept}})
}
