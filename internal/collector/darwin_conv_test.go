package collector

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestHostFromSysctl(t *testing.T) {
	now := time.Date(2026, 10, 21, 14, 2, 6, 900_000_000, time.UTC)
	boot := timeval{Sec: now.Add(-10 * 24 * time.Hour).Unix(), Usec: 250_000}
	// FSCALE is 1 << 11 on darwin.
	la := loadavg{Ldavg: [3]uint32{1024, 2048, 3072}, Fscale: 2048}

	t.Run("boot time keeps its microseconds, and uptime is whole seconds since it", func(t *testing.T) {
		h, err := hostFromSysctl(boot, la, now)
		if err != nil {
			t.Fatal(err)
		}
		if want := time.Unix(boot.Sec, 250_000_000); !h.BootTime.Equal(want) {
			t.Errorf("boot time = %v, want %v", h.BootTime, want)
		}
		// 10 days and 0.65 s.
		if h.UptimeSeconds != 864000 {
			t.Errorf("uptime = %d, want 864000", h.UptimeSeconds)
		}
	})

	t.Run("load averages are the fixed-point values over fscale", func(t *testing.T) {
		h, _ := hostFromSysctl(boot, la, now)
		if h.Load != [3]float64{0.5, 1, 1.5} {
			t.Errorf("load = %v, want [0.5 1 1.5]", h.Load)
		}
		h, _ = hostFromSysctl(boot, loadavg{Fscale: 2048}, now)
		if h.Load != [3]float64{} {
			t.Errorf("idle load = %v, want zeros", h.Load)
		}
	})

	for _, tc := range []struct {
		name, want string
		boot       timeval
		la         loadavg
	}{
		{"an fscale of zero is an error, not a division by zero", "fscale", boot, loadavg{Ldavg: [3]uint32{1, 1, 1}}},
		{"a boot time in the future is an error, not a huge uptime", "boot time", timeval{Sec: now.Unix() + 60}, la},
		{"a zero boot time is an error", "boot time", timeval{}, la},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := hostFromSysctl(tc.boot, tc.la, now)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("err = %v, want one mentioning %q", err, tc.want)
			}
		})
	}
}

func TestCPUFromLoad(t *testing.T) {
	t.Run("busy is user, system, and nice; idle is idle; the total sums the cores", func(t *testing.T) {
		c := cpuFromLoad([]cpuLoad{{10, 20, 300, 1}, {5, 5, 90, 0}})
		want := []CPUTicks{{Busy: 31, Idle: 300}, {Busy: 10, Idle: 90}}
		if len(c.PerCore) != 2 || c.PerCore[0] != want[0] || c.PerCore[1] != want[1] {
			t.Errorf("per core = %+v, want %+v", c.PerCore, want)
		}
		if c.Total != (CPUTicks{Busy: 41, Idle: 390}) {
			t.Errorf("total = %+v, want {41 390}", c.Total)
		}
	})

	t.Run("32-bit tick counters add up past 2³² without wrapping", func(t *testing.T) {
		const big = 1<<32 - 1
		c := cpuFromLoad([]cpuLoad{{big, big, big, big}, {big, 0, big, 0}})
		if want := uint64(big) * 4; c.Total.Busy != want {
			t.Errorf("total busy = %d, want %d", c.Total.Busy, want)
		}
		if c.PerCore[0].Busy != 3*big {
			t.Errorf("core 0 busy = %d, want %d", c.PerCore[0].Busy, uint64(3*big))
		}
	})
}

func TestCoreKinds(t *testing.T) {
	// Captured from IODeviceTree:/cpus on an M5 Mac (4P + 6E), in registry
	// order: the efficiency cores come first, so tagging by position against
	// hw.perflevel0 (the P cores) would get every core wrong.
	m5 := []cpuNode{
		{0, 'E'}, {1, 'E'}, {2, 'E'}, {3, 'E'}, {4, 'E'}, {5, 'E'},
		{6, 'P'}, {7, 'P'}, {8, 'P'}, {9, 'P'},
	}
	m5Levels := perfLevels{Levels: 2, Logical: [2]int{4, 6}}

	t.Run("a 4P+6E chip is tagged per core from cluster-type", func(t *testing.T) {
		d, err := coreKinds(10, m5Levels, m5)
		if err != nil {
			t.Fatal(err)
		}
		if d.PCores != 4 || d.ECores != 6 {
			t.Errorf("p_cores = %d, e_cores = %d, want 4 and 6", d.PCores, d.ECores)
		}
		if got, want := strings.Join(d.CoreKinds, ""), "EEEEEEPPPP"; got != want {
			t.Errorf("core kinds = %s, want %s", got, want)
		}
	})

	t.Run("cores are placed by logical-cpu-id, not by registry order", func(t *testing.T) {
		nodes := []cpuNode{{3, 'P'}, {0, 'E'}, {2, 'P'}, {1, 'E'}}
		d, err := coreKinds(4, perfLevels{Levels: 2, Logical: [2]int{2, 2}}, nodes)
		if err != nil {
			t.Fatal(err)
		}
		if got := strings.Join(d.CoreKinds, ""); got != "EEPP" {
			t.Errorf("core kinds = %s, want EEPP", got)
		}
	})

	t.Run("registry children without a logical-cpu-id are not cores, and are skipped", func(t *testing.T) {
		nodes := append([]cpuNode{{-1, 0}}, m5...)
		if _, err := coreKinds(10, m5Levels, nodes); err != nil {
			t.Errorf("err = %v, want nil", err)
		}
	})

	for _, tc := range []struct {
		name   string
		levels perfLevels
		nodes  []cpuNode
	}{
		{"a single performance level tags every core P, with no registry needed", perfLevels{Levels: 1, Logical: [2]int{8, 0}}, nil},
		{"no hw.nperflevels at all (older macOS) is one level", perfLevels{}, nil},
		{"a single level ignores whatever the registry says", perfLevels{Levels: 1}, []cpuNode{{0, 'E'}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d, err := coreKinds(8, tc.levels, tc.nodes)
			if err != nil {
				t.Fatal(err)
			}
			if d.PCores != 8 || d.ECores != 0 || strings.Join(d.CoreKinds, "") != "PPPPPPPP" {
				t.Errorf("got %+v, want 8 P cores, 0 E cores", d)
			}
		})
	}

	for _, tc := range []struct {
		name, want string
		levels     perfLevels
		nodes      []cpuNode
	}{
		{"counts that disagree with hw.perflevel are an error, not a guess", "hw.perflevel", perfLevels{Levels: 2, Logical: [2]int{6, 4}}, m5},
		{"a core missing from the registry is an error", "logical CPU 9", m5Levels, m5[:9]},
		{"a cluster-type other than P or E is an error", `"X"`, m5Levels, append(m5[:9:9], cpuNode{9, 'X'})},
		{"a missing cluster-type is an error", "no cluster-type", m5Levels, append(m5[:9:9], cpuNode{9, 0})},
		{"a logical-cpu-id seen twice is an error", "twice", m5Levels, append(m5[:9:9], cpuNode{8, 'P'})},
		{"a logical-cpu-id past the core count is an error", "logical-cpu-id 10", m5Levels, append(m5[:9:9], cpuNode{10, 'P'})},
		{"three performance levels are an error until someone sees one", "hw.nperflevels is 3", perfLevels{Levels: 3}, m5},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d, err := coreKinds(10, tc.levels, tc.nodes)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("err = %v, want one mentioning %s", err, tc.want)
			}
			if d != nil {
				t.Errorf("got %+v alongside an error, want nil", d)
			}
		})
	}

	// host_processor_info failing at startup reports -1 CPUs.
	for _, levels := range []perfLevels{{Levels: 1}, m5Levels} {
		t.Run(fmt.Sprintf("no CPU count at startup is an error, not a panic, with %d levels", levels.Levels), func(t *testing.T) {
			d, err := coreKinds(-1, levels, m5)
			if err == nil || !strings.Contains(err.Error(), "host_processor_info") {
				t.Errorf("err = %v, want one naming host_processor_info", err)
			}
			if d != nil {
				t.Errorf("got %+v alongside an error, want nil", d)
			}
		})
	}
}
