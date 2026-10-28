package sampler

import (
	"context"
	"fmt"
	"log/slog"
	"runtime"
	"testing"
	"time"

	"github.com/nmcginn/vitals/internal/collector"
)

// replayCollector hands out collections built before the measurement starts,
// so what the tests count is the sampler's work and not the fake's.
type replayCollector struct {
	raws []collector.Raw
	i    int
}

func (r *replayCollector) Collect(context.Context) (collector.Raw, error) {
	raw := r.raws[r.i%len(r.raws)]
	r.i++
	return raw, nil
}

func (r *replayCollector) Static() collector.HostInfo {
	return collector.HostInfo{Hostname: "mini", OS: "darwin", Kernel: "25.1.0", Arch: "arm64", CPUModel: "Apple M4"}
}

// mini is collection n on an M4 Mac Mini at its busiest: 10 cores, two disks,
// and the eight interfaces a Mac lists by default, every counter moving.
func mini(n uint64) collector.Raw {
	cores := make([]collector.CPUTicks, 10)
	for i := range cores {
		cores[i] = collector.CPUTicks{Busy: 100 * n, Idle: 900 * n}
	}
	pressure := "normal"
	ifaces := make([]collector.Interface, 8)
	for i := range ifaces {
		ifaces[i] = collector.Interface{Name: fmt.Sprintf("en%d", i), RxBytes: 5 << 32 * n, TxBytes: 1000 * n, RxPackets: 10 * n, TxPackets: 10 * n}
	}
	return collector.Raw{
		Host: &collector.Host{UptimeSeconds: 864000 + 2*n, BootTime: t0.Add(-240 * time.Hour), Load: [3]float64{0.41, 0.38, 0.35}},
		CPU: &collector.CPU{
			Total:   collector.CPUTicks{Busy: 1000 * n, Idle: 9000 * n},
			PerCore: cores,
			Darwin:  &collector.CPUDarwin{PCores: 4, ECores: 6, CoreKinds: []string{"P", "P", "P", "P", "E", "E", "E", "E", "E", "E"}},
		},
		Memory: &collector.Memory{
			TotalBytes: 16 << 30, AvailableBytes: 7 << 30, UsedBytes: 9 << 30,
			Darwin: &collector.MemoryDarwin{WiredBytes: 2 << 30, CompressedBytes: 1 << 29, Pressure: &pressure},
		},
		Disks: []collector.Disk{
			{Mount: "/", Device: "/dev/disk3s1s1", FS: "apfs", TotalBytes: 494384795648, FreeBytes: 374125711360,
				IO: &collector.DiskIO{Device: "disk0", ReadBytes: 4096 * n, WriteBytes: 8192 * n}},
			{Mount: "/Volumes/Backup", Device: "/dev/disk5s1", FS: "apfs", TotalBytes: 4000787030016, FreeBytes: 1000196757504,
				IO: &collector.DiskIO{Device: "disk4", ReadBytes: 4096 * n, WriteBytes: 8192 * n}},
		},
		Network: ifaces,
		Errors:  map[string]error{},
	}
}

// replay returns a sampler over n prebuilt collections and a clock that moves
// one interval per tick.
func replay(n, capacity int) (*Sampler, *Store) {
	raws := make([]collector.Raw, n)
	for i := range raws {
		raws[i] = mini(uint64(i + 1))
	}
	store := NewStore(capacity)
	s := New(&replayCollector{raws: raws}, store, 2*time.Second, slog.New(slog.DiscardHandler))
	now := t0
	s.now = func() time.Time { now = now.Add(2 * time.Second); return now }
	return s, store
}

// tickAllocs is the allocation budget of one tick on the mini fixture. Every
// known value in a snapshot is its own pointer, so that an unknown one can be
// nil (decision 14), which makes the count a sum over what the machine has:
//
//	 7  the snapshot, its errors map, the collection kept as prev, the timeout context
//	 3  host: uptime, boot time, load
//	13  cpu: the struct, the per-core slice, total usage, 10 cores' usage
//	 7  disks: the slice, 2 mounts' used %, 2 mounts' read and write rates
//	33  network: the slice, 8 interfaces' 4 rates
//
// A 16-interface machine would allocate more, and that's fine. What this guards
// is a tick that starts allocating per sample of history, or an accidental
// copy of the collection.
const tickAllocs = 7 + 3 + 13 + 7 + 33

// raceEnabled is set by race_test.go. The race detector makes sync.Pool drop
// items at random, so allocation counts under it measure the detector; `make
// budget` runs these tests without it.
var raceEnabled bool

func TestTickAllocations(t *testing.T) {
	if raceEnabled {
		t.Skip("allocation counts are meaningless under the race detector; make budget runs this")
	}
	ctx := context.Background()
	for _, tc := range []struct {
		name string
		fill int
	}{
		{"the second tick stays within the tick budget", 1},
		{"the tick that fills the buffer stays within the tick budget", 449},
		{"a tick into a full buffer stays within the tick budget", 450},
		{"a tick after the buffer has wrapped twice stays within the tick budget", 900},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, _ := replay(tc.fill+201, 450)
			for range tc.fill {
				s.tick(ctx)
			}
			if got := testing.AllocsPerRun(200, func() { s.tick(ctx) }); got > tickAllocs {
				t.Errorf("a tick allocates %.0f times, want at most %d", got, tickAllocs)
			}
		})
	}

	t.Run("a tick into a buffer that has wrapped allocates no more bytes than the second tick, give or take 512", func(t *testing.T) {
		// Counts alone would miss a tick that copied the history, which is one
		// allocation of 8 bytes per sample.
		bytesAfter := func(fill int) uint64 {
			s, _ := replay(fill+201, 450)
			for range fill {
				s.tick(ctx)
			}
			return allocatedPerRun(200, func() { s.tick(ctx) })
		}
		if second, wrapped := bytesAfter(1), bytesAfter(900); wrapped > second+512 {
			t.Errorf("a tick allocates %d bytes after 900 ticks and %d after one, want at most 512 more", wrapped, second)
		}
	})
}

// allocatedPerRun is the bytes f allocates on average, the byte-count twin of
// testing.AllocsPerRun.
func allocatedPerRun(runs int, f func()) uint64 {
	f() // warm up, as AllocsPerRun does
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	for range runs {
		f()
	}
	runtime.ReadMemStats(&after)
	return (after.TotalAlloc - before.TotalAlloc) / uint64(runs)
}

// heapAfterGC is the live heap once everything unreachable has been freed.
func heapAfterGC() uint64 {
	var m runtime.MemStats
	runtime.GC()
	runtime.GC()
	runtime.ReadMemStats(&m)
	return m.HeapAlloc
}

// heapMargin is how far the live heap may drift between two measurements of
// the same steady state: runtime bookkeeping, a few hundred bytes in practice.
// One snapshot of the mini fixture is about 1.5 KiB, so a store or sampler that
// kept even a tenth of the evicted snapshots would overshoot it many times.
const heapMargin = 16 << 10

func TestFullBufferDoesNotGrow(t *testing.T) {
	t.Run("the retained heap after two buffers' worth of ticks is within a fixed margin of the heap after one", func(t *testing.T) {
		ctx := context.Background()
		// Built up front, so the collections the fake hands out are already
		// on the heap before the first measurement.
		s, store := replay(900, 450)
		empty := heapAfterGC()
		for range 450 {
			s.tick(ctx)
		}
		one := heapAfterGC()
		for range 450 {
			s.tick(ctx)
		}
		two := heapAfterGC()
		runtime.KeepAlive(s)
		runtime.KeepAlive(store)
		t.Logf("a full buffer holds %d bytes, about %d per snapshot", one-empty, (one-empty)/450)
		if one <= empty {
			t.Fatalf("filling the buffer didn't grow the heap (%d to %d bytes), so this test measures nothing", empty, one)
		}
		if two > one+heapMargin {
			t.Errorf("the live heap grew by %d bytes over the second 450 ticks, want at most %d", two-one, heapMargin)
		}
	})
}

func BenchmarkTick(b *testing.B) {
	ctx := context.Background()
	s, _ := replay(1000, 450)
	for range 450 {
		s.tick(ctx)
	}
	b.ReportAllocs()
	for b.Loop() {
		s.tick(ctx)
	}
}
