package sampler

import (
	"sync"
	"sync/atomic"
	"time"
)

// Capacity is how many samples the ring buffer holds: history ÷ interval,
// rounded down. Flag validation rejects a history shorter than the interval.
func Capacity(history, interval time.Duration) int {
	return int(history / interval)
}

// Store holds the latest snapshot and a fixed-capacity ring of history. The
// sampler is its only writer; handlers read it.
type Store struct {
	latest atomic.Pointer[Snapshot]

	mu    sync.RWMutex
	ring  []*Snapshot // fixed length; allocated once so a full buffer doesn't grow
	start int         // index of the oldest sample
	n     int         // samples held, up to len(ring)
}

func NewStore(capacity int) *Store {
	return &Store{ring: make([]*Snapshot, max(capacity, 1))}
}

// Latest is the most recent snapshot, or nil before the first sample.
func (s *Store) Latest() *Snapshot { return s.latest.Load() }

// History returns the buffered snapshots, oldest first. The slice is the
// caller's to keep; the snapshots it points to are shared and must not be
// modified.
func (s *Store) History() []*Snapshot {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]*Snapshot, s.n)
	for i := range out {
		out[i] = s.ring[(s.start+i)%len(s.ring)]
	}
	return out
}

// push appends a snapshot, evicting the oldest when the buffer is full.
func (s *Store) push(snap *Snapshot) {
	s.mu.Lock()
	if s.n < len(s.ring) {
		s.ring[(s.start+s.n)%len(s.ring)] = snap
		s.n++
	} else {
		s.ring[s.start] = snap
		s.start = (s.start + 1) % len(s.ring)
	}
	s.mu.Unlock()
	s.latest.Store(snap)
}
