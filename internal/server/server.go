// Package server maps requests to renderers. It reads the store and never the
// collector, so no request can trigger collection, and it knows nothing about
// sockets: main.go owns the listener.
package server

import (
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/nmcginn/vitals/internal/render"
	"github.com/nmcginn/vitals/internal/sampler"
)

type Options struct {
	Log   *slog.Logger // where render failures go
	Build render.Build // reported by /metrics as vitals_build_info
}

const (
	typeHTML = "text/html; charset=utf-8"
	typeJSON = "application/json; charset=utf-8"
	typeText = "text/plain; charset=utf-8"
	typeProm = "text/plain; version=0.0.4; charset=utf-8"
)

// csp is PLAN's policy for HTML: inline styles, data: images, and the one
// inline script whose hash render publishes (decision 34). Nothing else runs,
// and nothing reaches the network.
var csp = "default-src 'none'; style-src 'unsafe-inline'; img-src data:; script-src " + render.ScriptHash

// New returns the handler for every route. Method patterns make ServeMux
// answer anything but GET and HEAD with 405 and "Allow: GET, HEAD".
func New(store *sampler.Store, opts Options) http.Handler {
	s := &server{store: store, log: opts.Log, build: opts.Build}
	if s.log == nil {
		s.log = slog.New(slog.DiscardHandler)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", s.negotiated(formatHTML))
	mux.HandleFunc("GET /healthz", s.healthz)
	mux.HandleFunc("GET /api/v1/stats", s.negotiated(formatJSON))
	mux.HandleFunc("GET /api/v1/history", s.history)
	mux.HandleFunc("GET /metrics", s.metrics)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// On every response, errors and 404s included.
		h := w.Header()
		h.Set("Cache-Control", "no-store")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "no-referrer")
		mux.ServeHTTP(w, r)
	})
}

type server struct {
	store *sampler.Store
	log   *slog.Logger
	build render.Build
}

func (s *server) healthz(w http.ResponseWriter, r *http.Request) {
	if s.store.Latest() == nil {
		write(w, http.StatusServiceUnavailable, typeText, []byte("no sample yet\n"))
		return
	}
	write(w, http.StatusOK, typeText, []byte("ok\n"))
}

// negotiated serves the latest snapshot in whichever format the request asks
// for, with def when it doesn't say. / and /api/v1/stats differ only in def
// (PLAN), so /?format=json is the stats JSON and /api/v1/stats?format=html is
// the dashboard.
func (s *server) negotiated(def format) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// On the 406 and the 503 too: a cache must not hand either to a client
		// that asked differently.
		w.Header().Set("Vary", "Accept")
		f, err := negotiate(r.URL.Query().Get("format"), r.Header.Get("Accept"), def)
		if err != nil {
			write(w, http.StatusNotAcceptable, typeText, []byte(err.Error()+"\n"))
			return
		}
		snap := s.store.Latest()
		if snap == nil {
			write(w, http.StatusServiceUnavailable, typeText, []byte("no sample yet\n"))
			return
		}
		switch f {
		case formatHTML:
			b, err := render.HTML(snap, s.store.History(), page(r))
			w.Header().Set("Content-Security-Policy", csp)
			s.respond(w, typeHTML, b, err)
		case formatJSON:
			b, err := render.JSON(snap, pretty(r))
			s.respond(w, typeJSON, b, err)
		case formatText:
			write(w, http.StatusOK, typeText, render.Text(snap))
		case formatProm:
			write(w, http.StatusOK, typeProm, render.Prom(snap, s.build))
		}
	}
}

// page reads ?refresh=. Anything but a whole number of seconds, 0 or more, is
// ignored, and the page refreshes at the sample interval (PLAN).
func page(r *http.Request) render.Page {
	n, err := strconv.Atoi(r.URL.Query().Get("refresh"))
	return render.Page{Refresh: n, RefreshSet: err == nil && n >= 0}
}

// metrics answers 503 before the first sample, like the other snapshot
// endpoints, so the scrape fails and Prometheus records up=0 rather than a
// sample with no metrics in it.
func (s *server) metrics(w http.ResponseWriter, r *http.Request) {
	snap := s.store.Latest()
	if snap == nil {
		write(w, http.StatusServiceUnavailable, typeText, []byte("no sample yet\n"))
		return
	}
	write(w, http.StatusOK, typeProm, render.Prom(snap, s.build))
}

// history serves the ring buffer, oldest first. ?since=<unix seconds> keeps
// only the samples taken after that second, so a client polling with the last
// sampled_at it saw gets each sample once.
func (s *server) history(w http.ResponseWriter, r *http.Request) {
	h := s.store.History()
	if v := r.URL.Query().Get("since"); v != "" {
		sec, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			write(w, http.StatusBadRequest, typeText,
				[]byte("since must be a whole number of Unix seconds, e.g. ?since=1761055326\n"))
			return
		}
		h = since(h, time.Unix(sec, 0))
	}
	b, err := render.History(h, pretty(r))
	s.respond(w, typeJSON, b, err)
}

// since returns the snapshots taken after t. h is oldest first.
func since(h []*sampler.Snapshot, t time.Time) []*sampler.Snapshot {
	for i, snap := range h {
		if snap.SampledAt.After(t) {
			return h[i:]
		}
	}
	return nil
}

func pretty(r *http.Request) bool { return r.URL.Query().Get("pretty") == "1" }

func (s *server) respond(w http.ResponseWriter, ctype string, b []byte, err error) {
	if err != nil {
		s.log.Error("rendering a response failed", "err", err)
		write(w, http.StatusInternalServerError, typeText, []byte("rendering failed; see the server log\n"))
		return
	}
	write(w, http.StatusOK, ctype, b)
}

// write sends a complete body. For HEAD, net/http sends the headers and drops
// the body.
func write(w http.ResponseWriter, status int, ctype string, b []byte) {
	w.Header().Set("Content-Type", ctype)
	w.Header().Set("Content-Length", strconv.Itoa(len(b)))
	w.WriteHeader(status)
	w.Write(b)
}
