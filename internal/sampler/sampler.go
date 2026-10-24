// Package sampler owns collection: it calls the collector on a ticker, does all
// delta and rate math, and publishes immutable snapshots to a Store. It makes
// no OS calls of its own.
package sampler

import (
	"context"
	"log/slog"
	"time"

	"github.com/nmcginn/vitals/internal/collector"
)

// errorLogEvery limits how often one failing group is logged.
const errorLogEvery = time.Minute

type Sampler struct {
	c        collector.Collector
	store    *Store
	interval time.Duration
	info     collector.HostInfo
	log      *slog.Logger
	now      func() time.Time // injected by tests

	prev   *collector.Raw
	prevAt time.Time
	logged map[string]time.Time // group (or "collect") → last time logged
}

func New(c collector.Collector, store *Store, interval time.Duration, log *slog.Logger) *Sampler {
	return &Sampler{
		c: c, store: store, interval: interval, info: c.Static(), log: log,
		now: time.Now, logged: map[string]time.Time{},
	}
}

// Run samples once immediately, then every interval until ctx is done.
func (s *Sampler) Run(ctx context.Context) {
	t := time.NewTicker(s.interval)
	defer t.Stop()
	for {
		if !s.tick(ctx) {
			// The collection ran long. Restart the ticker so the next sample is
			// a full interval away, rather than firing a tick that piled up
			// while we waited.
			t.Reset(s.interval)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// tick collects once and publishes a snapshot. It returns false when the
// collection overran its timeout, in which case the result is discarded and
// nothing is published: the tick is skipped, and the next rate spans the gap
// using the actual elapsed time.
func (s *Sampler) tick(ctx context.Context) bool {
	timeout := s.interval * 8 / 10
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	at := s.now()
	raw, err := s.c.Collect(cctx)
	if cctx.Err() != nil {
		if ctx.Err() == nil {
			s.logError(at, "collect", "collection overran its timeout; tick skipped", "timeout", timeout)
		}
		return false
	}
	if err != nil {
		s.logError(at, "collect", "collection failed; tick skipped", "err", err)
		return true
	}
	for g, gerr := range raw.Errors {
		s.logError(at, g, "metric group failed", "err", gerr)
	}

	var elapsed time.Duration
	if s.prev != nil {
		elapsed = at.Sub(s.prevAt)
	}
	s.store.push(build(s.info, s.prev, raw, elapsed, at, s.interval))
	s.prev, s.prevAt = &raw, at
	return true
}

// logError logs at most once per errorLogEvery per key, so a group that fails
// on every tick doesn't flood the log.
func (s *Sampler) logError(at time.Time, key, msg string, args ...any) {
	if last, ok := s.logged[key]; ok && at.Sub(last) < errorLogEvery {
		return
	}
	s.logged[key] = at
	s.log.Warn(msg, append([]any{"group", key}, args...)...)
}
