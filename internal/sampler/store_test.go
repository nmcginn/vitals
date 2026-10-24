package sampler

import (
	"sync"
	"testing"
	"time"
)

func TestCapacity(t *testing.T) {
	for _, tc := range []struct {
		name              string
		history, interval time.Duration
		want              int
	}{
		{"15m at 2s holds 450 samples", 15 * time.Minute, 2 * time.Second, 450},
		{"15m at 5s holds 180 samples", 15 * time.Minute, 5 * time.Second, 180},
		{"a history that isn't a whole number of intervals rounds down", 15 * time.Second, 2 * time.Second, 7},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := Capacity(tc.history, tc.interval); got != tc.want {
				t.Errorf("Capacity(%v, %v) = %d, want %d", tc.history, tc.interval, got, tc.want)
			}
		})
	}
}

func TestStore(t *testing.T) {
	stamp := func(i int) *Snapshot { return &Snapshot{SampledAt: t0.Add(time.Duration(i) * time.Second)} }

	t.Run("latest is nil before the first sample", func(t *testing.T) {
		s := NewStore(450)
		if s.Latest() != nil || len(s.History()) != 0 {
			t.Error("a new store is not empty")
		}
	})

	t.Run("at capacity 450 the oldest sample is evicted first", func(t *testing.T) {
		s := NewStore(450)
		for i := range 451 + 10 {
			s.push(stamp(i))
		}
		h := s.History()
		if len(h) != 450 {
			t.Fatalf("history holds %d, want 450", len(h))
		}
		for i, snap := range h {
			if want := t0.Add(time.Duration(11+i) * time.Second); !snap.SampledAt.Equal(want) {
				t.Fatalf("history[%d] = %v, want %v (oldest first)", i, snap.SampledAt, want)
			}
		}
		if s.Latest() != h[449] {
			t.Error("latest is not the newest history entry")
		}
	})

	t.Run("history returned to callers is a copy", func(t *testing.T) {
		s := NewStore(4)
		s.push(stamp(0))
		s.push(stamp(1))
		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			defer wg.Done()
			for i := range 1000 {
				s.push(stamp(i + 2))
			}
		}()
		go func() {
			defer wg.Done()
			for range 1000 {
				h := s.History()
				for i := range h {
					h[i] = nil
				}
				_ = append(h, nil)
			}
		}()
		wg.Wait()
		for i, snap := range s.History() {
			if snap == nil {
				t.Fatalf("history[%d] is nil: a caller's edit reached the store", i)
			}
		}
	})
}
