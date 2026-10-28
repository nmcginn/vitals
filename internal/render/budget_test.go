package render

import (
	"runtime"
	"testing"
)

// raceEnabled is set by race_test.go. The race detector makes sync.Pool drop
// items at random, so allocation counts under it measure the detector; `make
// budget` runs these tests without it.
var raceEnabled bool

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

// Text, Prom, and JSON take only the latest snapshot, so history can't reach
// them. The dashboard and /api/v1/history are the renderers it can grow.
func TestRenderAllocations(t *testing.T) {
	if raceEnabled {
		t.Skip("allocation counts are meaningless under the race detector; make budget runs this")
	}
	s := eightInterfaces()
	short, full := historyOf(s, sparkPoints), historyOf(s, 450)

	t.Run("the dashboard allocates as many times for 450 samples of history as for 120", func(t *testing.T) {
		a := testing.AllocsPerRun(20, func() { HTML(s, short, Page{}) })
		b := testing.AllocsPerRun(20, func() { HTML(s, full, Page{}) })
		if b > a {
			t.Errorf("%.0f allocations for 450 samples, %.0f for 120; want no more", b, a)
		}
	})

	t.Run("the dashboard allocates no more bytes for 450 samples of history than for 120, give or take 4 KiB", func(t *testing.T) {
		// Both pages draw sparkPoints points per line, so their output is the
		// same size, give or take the digits in the coordinates. Copying the
		// history into series first cost 36 KiB more at 450 samples.
		a := allocatedPerRun(20, func() { HTML(s, short, Page{}) })
		b := allocatedPerRun(20, func() { HTML(s, full, Page{}) })
		if b > a+4<<10 {
			t.Errorf("%d bytes for 450 samples, %d for 120; want at most 4 KiB more", b, a)
		}
	})

	t.Run("history JSON allocates at most twice its own size, compact or pretty", func(t *testing.T) {
		// Its output grows with history, as it must, and its buffer grows by
		// doubling, so the count of allocations grows with the log of it.
		// Pretty output is the compact form re-indented, so it holds both
		// for a moment.
		for _, pretty := range []bool{false, true} {
			out, _ := History(full, pretty)
			if got := allocatedPerRun(10, func() { History(full, pretty) }); got > 2*uint64(len(out)) {
				t.Errorf("pretty=%v: allocated %d bytes for %d bytes of output, want at most twice that", pretty, got, len(out))
			}
		}
	})
}

// The per-request paths, on the fixture PLAN budgets: 8 interfaces and a full
// 15-minute buffer.

func BenchmarkHTML(b *testing.B) {
	s := eightInterfaces()
	h := historyOf(s, 450)
	b.ReportAllocs()
	for b.Loop() {
		HTML(s, h, Page{})
	}
}

func BenchmarkJSON(b *testing.B) {
	s := eightInterfaces()
	b.ReportAllocs()
	for b.Loop() {
		JSON(s, false)
	}
}

func BenchmarkHistory(b *testing.B) {
	s := eightInterfaces()
	h := historyOf(s, 450)
	b.ReportAllocs()
	for b.Loop() {
		History(h, false)
	}
}

func BenchmarkText(b *testing.B) {
	s := eightInterfaces()
	b.ReportAllocs()
	for b.Loop() {
		Text(s)
	}
}

func BenchmarkProm(b *testing.B) {
	s := eightInterfaces()
	b.ReportAllocs()
	for b.Loop() {
		Prom(s, Build{})
	}
}
