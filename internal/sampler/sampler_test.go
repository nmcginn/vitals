package sampler

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nmcginn/vitals/internal/collector"
)

// fakeCollector returns scripted collections in order. A step with block set
// waits for its context to expire, like a collector stuck on a slow syscall.
type fakeCollector struct {
	steps []step
	i     int
}

type step struct {
	raw   collector.Raw
	err   error
	block bool
}

func (f *fakeCollector) Collect(ctx context.Context) (collector.Raw, error) {
	st := f.steps[f.i]
	f.i++
	if st.block {
		<-ctx.Done()
		return collector.Raw{}, ctx.Err()
	}
	return st.raw, st.err
}

func (f *fakeCollector) Static() collector.HostInfo {
	return collector.HostInfo{Hostname: "mini", OS: "darwin", Kernel: "25.1.0", Arch: "arm64", CPUModel: "Apple M4"}
}

var t0 = time.Date(2026, 10, 21, 14, 2, 6, 0, time.UTC)

// harness runs a sampler against scripted collections and a fake clock that
// the test moves by hand between ticks.
func harness(t *testing.T, interval time.Duration, steps ...step) (*Sampler, *Store, *time.Time, *bytes.Buffer) {
	t.Helper()
	var logs bytes.Buffer
	store := NewStore(450)
	s := New(&fakeCollector{steps: steps}, store, interval, slog.New(slog.NewTextHandler(&logs, nil)))
	now := t0
	s.now = func() time.Time { return now }
	return s, store, &now, &logs
}

func netRaw(rx, tx, rxp, txp uint64) collector.Raw {
	return collector.Raw{Network: []collector.Interface{{Name: "en0", RxBytes: rx, TxBytes: tx, RxPackets: rxp, TxPackets: txp}}}
}

func cpuRaw(busy, idle uint64, cores ...collector.CPUTicks) collector.Raw {
	return collector.Raw{CPU: &collector.CPU{Total: collector.CPUTicks{Busy: busy, Idle: idle}, PerCore: cores}}
}

func full(seed uint64) collector.Raw {
	return collector.Raw{
		Host:    &collector.Host{UptimeSeconds: 100 + seed, BootTime: t0.Add(-time.Hour), Load: [3]float64{0.4, 0.3, 0.2}},
		CPU:     &collector.CPU{Total: collector.CPUTicks{Busy: seed, Idle: seed}, PerCore: []collector.CPUTicks{{Busy: seed, Idle: seed}, {Busy: seed, Idle: seed}}},
		Memory:  &collector.Memory{TotalBytes: 16 << 30, AvailableBytes: 7 << 30, UsedBytes: 9 << 30},
		Disks:   []collector.Disk{{Mount: "/", Device: "/dev/disk3s1", FS: "apfs", TotalBytes: 1000, FreeBytes: 750, IO: &collector.DiskIO{Device: "disk0", ReadBytes: seed, WriteBytes: seed}}},
		Network: []collector.Interface{{Name: "en0", RxBytes: seed, TxBytes: seed, RxPackets: seed, TxPackets: seed}},
	}
}

func f64(p *float64) string {
	if p == nil {
		return "null"
	}
	b, _ := json.Marshal(*p)
	return string(b)
}

func TestRates(t *testing.T) {
	ctx := context.Background()

	t.Run("every rate is null in JSON on the first sample", func(t *testing.T) {
		s, store, _, _ := harness(t, 2*time.Second, step{raw: full(1000)})
		s.tick(ctx)
		b, err := json.Marshal(store.Latest())
		if err != nil {
			t.Fatal(err)
		}
		got := string(b)
		for _, want := range []string{
			`"usage_pct":null`, `"per_core_pct":[null,null]`,
			`"read_bps":null`, `"write_bps":null`,
			`"rx_bps":null`, `"tx_bps":null`, `"rx_pps":null`, `"tx_pps":null`,
		} {
			if !strings.Contains(got, want) {
				t.Errorf("first-sample JSON lacks %s:\n%s", want, got)
			}
		}
	})

	t.Run("rates divide by the actual elapsed time, not the interval", func(t *testing.T) {
		s, store, now, _ := harness(t, 2*time.Second, step{raw: full(1000)}, step{raw: full(1000 + 3100)})
		s.tick(ctx)
		*now = now.Add(3100 * time.Millisecond)
		s.tick(ctx)
		snap := store.Latest()
		for name, got := range map[string]*float64{
			"rx_bps": snap.Network[0].RxBps, "tx_bps": snap.Network[0].TxBps,
			"rx_pps": snap.Network[0].RxPps, "tx_pps": snap.Network[0].TxPps,
			"read_bps": snap.Disks[0].ReadBps, "write_bps": snap.Disks[0].WriteBps,
		} {
			if got == nil || *got != 1000 {
				t.Errorf("%s = %s, want 3100 ÷ 3.1 = 1000", name, f64(got))
			}
		}
	})

	backwards := []struct {
		name      string
		prev, cur collector.Raw
		get       func(*Snapshot) *float64
	}{
		{"rx rate is null when the counter resets", netRaw(5000, 0, 0, 0), netRaw(10, 0, 0, 0),
			func(s *Snapshot) *float64 { return s.Network[0].RxBps }},
		{"tx rate is null when a 32-bit counter wraps at 2³²", netRaw(0, 1<<32-296, 0, 0), netRaw(0, 200, 0, 0),
			func(s *Snapshot) *float64 { return s.Network[0].TxBps }},
		{"packet rate is null when the counter goes backwards", netRaw(0, 0, 9, 9), netRaw(0, 0, 8, 10),
			func(s *Snapshot) *float64 { return s.Network[0].RxPps }},
		{"cpu usage is null when busy ticks go backwards", cpuRaw(500, 500), cpuRaw(400, 700),
			func(s *Snapshot) *float64 { return s.CPU.UsagePct }},
		{"cpu usage is null when idle ticks go backwards", cpuRaw(500, 500), cpuRaw(700, 400),
			func(s *Snapshot) *float64 { return s.CPU.UsagePct }},
		{"disk read rate is null when the device counter resets",
			collector.Raw{Disks: []collector.Disk{{Mount: "/", IO: &collector.DiskIO{Device: "sda", ReadBytes: 900}}}},
			collector.Raw{Disks: []collector.Disk{{Mount: "/", IO: &collector.DiskIO{Device: "sda", ReadBytes: 5}}}},
			func(s *Snapshot) *float64 { return s.Disks[0].ReadBps }},
	}
	for _, tc := range backwards {
		t.Run(tc.name, func(t *testing.T) {
			s, store, now, _ := harness(t, 2*time.Second, step{raw: tc.prev}, step{raw: tc.cur})
			s.tick(ctx)
			*now = now.Add(2 * time.Second)
			s.tick(ctx)
			if got := tc.get(store.Latest()); got != nil {
				t.Errorf("rate = %s, want null", f64(got))
			}
		})
	}

	t.Run("counters crossing 2³² give a positive rate and a total above 2³²", func(t *testing.T) {
		s, store, now, _ := harness(t, 2*time.Second, step{raw: netRaw(4_294_967_000, 0, 0, 0)}, step{raw: netRaw(4_294_968_000, 0, 0, 0)})
		s.tick(ctx)
		*now = now.Add(2 * time.Second)
		s.tick(ctx)
		n := store.Latest().Network[0]
		if n.RxBps == nil || *n.RxBps != 500 {
			t.Errorf("rx_bps = %s, want 1000 ÷ 2 = 500", f64(n.RxBps))
		}
		if n.RxTotalBytes <= 1<<32 {
			t.Errorf("rx_total_bytes = %d, want above 2³²", n.RxTotalBytes)
		}
	})

	t.Run("rates are null when the clock didn't move", func(t *testing.T) {
		s, store, _, _ := harness(t, 2*time.Second, step{raw: netRaw(0, 0, 0, 0)}, step{raw: netRaw(10, 0, 0, 0)})
		s.tick(ctx)
		s.tick(ctx)
		if got := store.Latest().Network[0].RxBps; got != nil {
			t.Errorf("rx_bps = %s with zero elapsed time, want null", f64(got))
		}
	})

	t.Run("an interface or device that first appears has null rates", func(t *testing.T) {
		second := netRaw(100, 100, 1, 1)
		second.Network = append(second.Network, collector.Interface{Name: "en1", RxBytes: 50})
		second.Disks = []collector.Disk{{Mount: "/Volumes/x", TotalBytes: 10, IO: &collector.DiskIO{Device: "disk4", ReadBytes: 7}}}
		s, store, now, _ := harness(t, 2*time.Second, step{raw: netRaw(0, 0, 0, 0)}, step{raw: second})
		s.tick(ctx)
		*now = now.Add(2 * time.Second)
		s.tick(ctx)
		snap := store.Latest()
		if snap.Network[0].RxBps == nil {
			t.Error("en0 rx_bps is null, want a rate")
		}
		if snap.Network[1].RxBps != nil || snap.Disks[0].ReadBps != nil {
			t.Errorf("new en1 rx_bps = %s, new disk read_bps = %s, want null", f64(snap.Network[1].RxBps), f64(snap.Disks[0].ReadBps))
		}
	})
}

func TestCPUUsage(t *testing.T) {
	ctx := context.Background()
	two := func(raw1, raw2 collector.Raw) *Snapshot {
		s, store, now, _ := harness(t, 2*time.Second, step{raw: raw1}, step{raw: raw2})
		s.tick(ctx)
		*now = now.Add(2 * time.Second)
		s.tick(ctx)
		return store.Latest()
	}

	t.Run("usage is busy delta over total delta, as a percentage", func(t *testing.T) {
		snap := two(
			cpuRaw(100, 900, collector.CPUTicks{Busy: 50, Idle: 450}, collector.CPUTicks{Busy: 50, Idle: 450}),
			cpuRaw(130, 970, collector.CPUTicks{Busy: 80, Idle: 470}, collector.CPUTicks{Busy: 50, Idle: 500}),
		)
		if got := snap.CPU.UsagePct; got == nil || *got != 30 {
			t.Errorf("usage_pct = %s, want 30 ÷ 100 = 30", f64(got))
		}
		if got := snap.CPU.PerCorePct[0]; got == nil || *got != 60 {
			t.Errorf("core 0 = %s, want 60", f64(got))
		}
		if got := snap.CPU.PerCorePct[1]; got == nil || *got != 0 {
			t.Errorf("core 1 = %s, want 0 (idle but ticking)", f64(got))
		}
		if snap.CPU.Cores != 2 || snap.CPU.Model != "Apple M4" {
			t.Errorf("cores = %d, model = %q", snap.CPU.Cores, snap.CPU.Model)
		}
	})

	t.Run("a core with no ticks elapsed is null, not NaN", func(t *testing.T) {
		snap := two(
			cpuRaw(10, 10, collector.CPUTicks{Busy: 5, Idle: 5}),
			cpuRaw(10, 10, collector.CPUTicks{Busy: 5, Idle: 5}),
		)
		if snap.CPU.UsagePct != nil || snap.CPU.PerCorePct[0] != nil {
			t.Errorf("usage = %s, core 0 = %s, want null for 0/0", f64(snap.CPU.UsagePct), f64(snap.CPU.PerCorePct[0]))
		}
	})

	t.Run("per-core usage is null when the core count changes, total still computed", func(t *testing.T) {
		snap := two(
			cpuRaw(10, 10, collector.CPUTicks{}, collector.CPUTicks{}),
			cpuRaw(20, 20, collector.CPUTicks{Busy: 5, Idle: 5}),
		)
		if snap.CPU.PerCorePct[0] != nil {
			t.Errorf("core 0 = %s after a core went offline, want null", f64(snap.CPU.PerCorePct[0]))
		}
		if got := snap.CPU.UsagePct; got == nil || *got != 50 {
			t.Errorf("usage_pct = %s, want 50", f64(got))
		}
	})

	t.Run("tick counters that go backwards give null, never a negative or huge usage", func(t *testing.T) {
		for _, tc := range []struct {
			name      string
			prev, cur collector.CPUTicks
		}{
			{"busy went back", collector.CPUTicks{Busy: 100, Idle: 100}, collector.CPUTicks{Busy: 50, Idle: 300}},
			{"idle went back", collector.CPUTicks{Busy: 100, Idle: 100}, collector.CPUTicks{Busy: 300, Idle: 50}},
			// A darwin core's 32-bit counter wrapping, as the collector widens it.
			{"a 32-bit wrap", collector.CPUTicks{Busy: 1<<32 - 10, Idle: 5}, collector.CPUTicks{Busy: 20, Idle: 15}},
		} {
			snap := two(cpuRaw(tc.prev.Busy, tc.prev.Idle, tc.prev, collector.CPUTicks{Busy: 1, Idle: 1}),
				cpuRaw(tc.cur.Busy, tc.cur.Idle, tc.cur, collector.CPUTicks{Busy: 2, Idle: 2}))
			if snap.CPU.UsagePct != nil || snap.CPU.PerCorePct[0] != nil {
				t.Errorf("%s: usage = %s, core 0 = %s, want null", tc.name, f64(snap.CPU.UsagePct), f64(snap.CPU.PerCorePct[0]))
			}
			if got := snap.CPU.PerCorePct[1]; got == nil || *got != 50 {
				t.Errorf("%s: core 1 = %s, want 50 (its counters moved forward)", tc.name, f64(got))
			}
		}
	})

	t.Run("usage stays within 0 to 100 on a single-core machine", func(t *testing.T) {
		snap := two(cpuRaw(0, 0, collector.CPUTicks{}), cpuRaw(200, 0, collector.CPUTicks{Busy: 200}))
		if got := snap.CPU.UsagePct; got == nil || *got != 100 {
			t.Errorf("usage_pct = %s, want 100", f64(got))
		}
	})
}

func TestGauges(t *testing.T) {
	ctx := context.Background()
	one := func(raw collector.Raw) *Snapshot {
		s, store, _, _ := harness(t, 2*time.Second, step{raw: raw})
		s.tick(ctx)
		return store.Latest()
	}

	t.Run("disk used is total minus free, and used % is used over total", func(t *testing.T) {
		d := one(full(1)).Disks[0]
		if d.UsedBytes != 250 || d.UsedPct == nil || *d.UsedPct != 25 {
			t.Errorf("used = %d, used_pct = %s, want 250 and 25", d.UsedBytes, f64(d.UsedPct))
		}
	})

	t.Run("a zero-size filesystem has a null used %, not NaN", func(t *testing.T) {
		d := one(collector.Raw{Disks: []collector.Disk{{Mount: "/empty"}}}).Disks[0]
		if d.UsedPct != nil {
			t.Errorf("used_pct = %s, want null", f64(d.UsedPct))
		}
	})

	t.Run("a mount with no physical device has null rates on every sample", func(t *testing.T) {
		raw := collector.Raw{Disks: []collector.Disk{{Mount: "/", TotalBytes: 10}}}
		s, store, now, _ := harness(t, 2*time.Second, step{raw: raw}, step{raw: raw})
		s.tick(ctx)
		*now = now.Add(2 * time.Second)
		s.tick(ctx)
		if d := store.Latest().Disks[0]; d.ReadBps != nil || d.WriteBps != nil {
			t.Errorf("read_bps = %s, write_bps = %s, want null", f64(d.ReadBps), f64(d.WriteBps))
		}
	})

	t.Run("every interface and disk filtered out gives empty lists, not null", func(t *testing.T) {
		b, _ := json.Marshal(one(collector.Raw{Disks: []collector.Disk{}, Network: []collector.Interface{}}))
		if !strings.Contains(string(b), `"disks":[]`) || !strings.Contains(string(b), `"network":[]`) {
			t.Errorf("got %s, want empty disks and network arrays", b)
		}
	})

	t.Run("no temperature sensors gives temps null and errors {}", func(t *testing.T) {
		b, _ := json.Marshal(one(collector.Raw{Temps: []collector.Temp{}}))
		if !strings.Contains(string(b), `"temps":null`) || !strings.Contains(string(b), `"errors":{}`) {
			t.Errorf("got %s, want temps null and errors {}", b)
		}
	})

	t.Run("host gauges are filled from the host group, timestamps in UTC seconds", func(t *testing.T) {
		snap := one(full(1))
		h := snap.Host
		if h.UptimeSeconds == nil || *h.UptimeSeconds != 101 || h.Load == nil || h.Load[0] != 0.4 {
			t.Errorf("host = %+v", h)
		}
		if h.Hostname != "mini" || h.BootTime == nil || h.BootTime.Location() != time.UTC {
			t.Errorf("host = %+v", h)
		}
		if snap.SampledAt != t0 || snap.IntervalSeconds != 2 {
			t.Errorf("sampled_at = %v, interval_s = %v", snap.SampledAt, snap.IntervalSeconds)
		}
	})
}

func TestFailures(t *testing.T) {
	ctx := context.Background()

	t.Run("one group's error lands in errors while the others are populated", func(t *testing.T) {
		raw := full(1)
		raw.Network = nil
		raw.Errors = map[string]error{collector.GroupNetwork: errors.New("reading /proc/net/dev: permission denied")}
		s, store, _, _ := harness(t, 2*time.Second, step{raw: raw})
		s.tick(ctx)
		snap := store.Latest()
		if got := snap.Errors["network"]; got != "reading /proc/net/dev: permission denied" {
			t.Errorf("errors[network] = %q", got)
		}
		if snap.Network != nil {
			t.Errorf("network = %v, want null for a failed group", snap.Network)
		}
		if snap.CPU == nil || snap.Memory == nil || len(snap.Disks) != 1 || snap.Host.UptimeSeconds == nil {
			t.Errorf("other groups missing: %+v", snap)
		}
	})

	t.Run("a failed host group leaves the static fields and nulls the gauges", func(t *testing.T) {
		s, store, _, _ := harness(t, 2*time.Second, step{raw: collector.Raw{Errors: map[string]error{"host": errors.New("x")}}})
		s.tick(ctx)
		b, _ := json.Marshal(store.Latest().Host)
		want := `{"hostname":"mini","os":"darwin","kernel":"25.1.0","arch":"arm64","uptime_s":null,"boot_time":null,"load":null}`
		if string(b) != want {
			t.Errorf("host = %s\nwant   %s", b, want)
		}
	})

	t.Run("a collection that overruns its timeout skips the tick", func(t *testing.T) {
		s, store, now, logs := harness(t, 50*time.Millisecond,
			step{raw: netRaw(0, 0, 0, 0)}, step{block: true}, step{raw: netRaw(4000, 0, 0, 0)})
		s.tick(ctx)
		first := store.Latest()
		*now = now.Add(2 * time.Second)
		if s.tick(ctx) {
			t.Error("tick reported success for an overrun")
		}
		if store.Latest() != first || len(store.History()) != 1 {
			t.Error("an overrun tick published a snapshot")
		}
		if !strings.Contains(logs.String(), "overran") {
			t.Errorf("no overrun log line: %q", logs.String())
		}
		*now = now.Add(2 * time.Second)
		s.tick(ctx)
		if got := store.Latest().Network[0].RxBps; got == nil || *got != 1000 {
			t.Errorf("rx_bps after the skip = %s, want 4000 ÷ 4s since the last good sample = 1000", f64(got))
		}
	})

	t.Run("a collection that fails entirely publishes nothing", func(t *testing.T) {
		s, store, _, logs := harness(t, 2*time.Second, step{err: errors.New("no host_statistics64")})
		s.tick(ctx)
		if store.Latest() != nil {
			t.Error("a failed collection published a snapshot")
		}
		if !strings.Contains(logs.String(), "no host_statistics64") {
			t.Errorf("failure not logged: %q", logs.String())
		}
	})

	t.Run("a failing group is logged at most once per minute", func(t *testing.T) {
		bad := collector.Raw{Errors: map[string]error{"memory": errors.New("MemAvailable missing")}}
		s, _, now, logs := harness(t, 2*time.Second, step{raw: bad}, step{raw: bad}, step{raw: bad}, step{raw: bad})
		for _, gap := range []time.Duration{0, 2 * time.Second, 57 * time.Second, 2 * time.Second} {
			*now = now.Add(gap)
			s.tick(ctx)
		}
		// Ticks at 0s, 2s, 59s, and 61s: logged at 0s and again at 61s.
		if got := strings.Count(logs.String(), "MemAvailable missing"); got != 2 {
			t.Errorf("logged %d times, want 2:\n%s", got, logs.String())
		}
	})
}

// blockingCollector ignores its context for the first collection, like a
// syscall that can't be interrupted, and records when each collection starts.
type blockingCollector struct {
	mu        sync.Mutex
	starts    []time.Time
	ends      []time.Time
	stall     time.Duration
	inFlight  int
	maxFlight int
}

func (b *blockingCollector) Collect(ctx context.Context) (collector.Raw, error) {
	b.mu.Lock()
	b.starts = append(b.starts, time.Now())
	n := len(b.starts)
	b.inFlight++
	b.maxFlight = max(b.maxFlight, b.inFlight)
	b.mu.Unlock()
	if n == 1 {
		time.Sleep(b.stall)
	}
	b.mu.Lock()
	b.inFlight--
	b.ends = append(b.ends, time.Now())
	b.mu.Unlock()
	return collector.Raw{}, nil
}

func (b *blockingCollector) Static() collector.HostInfo { return collector.HostInfo{} }

func TestRunDoesNotQueueTicks(t *testing.T) {
	t.Run("after a collection that overruns several intervals, the next starts a full interval later", func(t *testing.T) {
		const interval = 40 * time.Millisecond
		c := &blockingCollector{stall: 5 * interval}
		store := NewStore(10)
		s := New(c, store, interval, slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)))
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan struct{})
		go func() { s.Run(ctx); close(done) }()
		time.Sleep(5*interval + interval*3)
		cancel()
		<-done

		c.mu.Lock()
		defer c.mu.Unlock()
		if c.maxFlight != 1 {
			t.Errorf("%d collections ran at once, want 1", c.maxFlight)
		}
		if len(c.starts) < 2 {
			t.Fatalf("only %d collections ran", len(c.starts))
		}
		// The five ticks that came due during the stall must not fire back to
		// back once it ends.
		if gap := c.starts[1].Sub(c.ends[0]); gap < interval*3/4 {
			t.Errorf("next collection began %v after the overrun ended, want about %v", gap, interval)
		}
		if len(store.History()) > len(c.starts)-1 {
			t.Errorf("history has %d samples from %d collections, want the overrun discarded", len(store.History()), len(c.starts))
		}
	})
}
