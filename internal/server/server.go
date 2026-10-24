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
	Log *slog.Logger // where render failures go
}

const (
	typeJSON = "application/json; charset=utf-8"
	typeText = "text/plain; charset=utf-8"
)

// New returns the handler for every route. Method patterns make ServeMux
// answer anything but GET and HEAD with 405 and "Allow: GET, HEAD".
func New(store *sampler.Store, opts Options) http.Handler {
	s := &server{store: store, log: opts.Log}
	if s.log == nil {
		s.log = slog.New(slog.DiscardHandler)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.healthz)
	mux.HandleFunc("GET /api/v1/stats", s.stats)
	mux.HandleFunc("GET /api/v1/history", s.history)
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
}

func (s *server) healthz(w http.ResponseWriter, r *http.Request) {
	if s.store.Latest() == nil {
		write(w, http.StatusServiceUnavailable, typeText, []byte("no sample yet\n"))
		return
	}
	write(w, http.StatusOK, typeText, []byte("ok\n"))
}

func (s *server) stats(w http.ResponseWriter, r *http.Request) {
	snap := s.store.Latest()
	if snap == nil {
		write(w, http.StatusServiceUnavailable, typeText, []byte("no sample yet\n"))
		return
	}
	b, err := render.JSON(snap, pretty(r))
	s.respond(w, typeJSON, b, err)
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
