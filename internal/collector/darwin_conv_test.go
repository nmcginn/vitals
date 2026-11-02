package collector

import (
	"fmt"
	"math"
	"regexp"
	"slices"
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

func TestMemoryFromVM(t *testing.T) {
	// A 16 GiB Apple Silicon Mac (16 KiB pages) under light load: the shape of
	// host_statistics64 and vm.swapusage there, with page counts written to
	// match rather than captured. 16 GiB is 1,048,576 pages.
	mini := func() vmMemory {
		return vmMemory{
			MemSize: 16 << 30, PageSize: 16384,
			Active: 380_000, Inactive: 360_000, Wire: 150_000, Purgeable: 10_000,
			Compressor: 40_000, Internal: 400_000, External: 300_000,
			SwapTotal: 2 << 30, SwapUsed: 1 << 30,
			PressureLevel: 1, PressureOK: true,
		}
	}

	t.Run("used is internal minus purgeable, plus wired and compressed, and available is the rest", func(t *testing.T) {
		m, err := memoryFromVM(mini())
		if err != nil {
			t.Fatal(err)
		}
		used := uint64(400_000-10_000+150_000+40_000) * 16384
		want := Memory{TotalBytes: 16 << 30, UsedBytes: used, AvailableBytes: 16<<30 - used, SwapTotalBytes: 2 << 30, SwapUsedBytes: 1 << 30}
		got := *m
		got.Darwin = nil
		if got != want {
			t.Errorf("memory = %+v\nwant     %+v", got, want)
		}
		d := m.Darwin
		if d == nil || d.WiredBytes != 150_000*16384 || d.CompressedBytes != 40_000*16384 || d.ActiveBytes != 380_000*16384 ||
			d.InactiveBytes != 360_000*16384 || d.FileBackedBytes != 300_000*16384 {
			t.Errorf("darwin = %+v", d)
		}
	})

	t.Run("2²⁰ pages of 16 KiB multiply in 64 bits rather than wrapping at 4 GiB", func(t *testing.T) {
		v := mini()
		v.MemSize, v.Wire, v.Internal, v.Purgeable, v.Compressor = 32<<30, 1<<20, 0, 0, 0
		m, err := memoryFromVM(v)
		if err != nil {
			t.Fatal(err)
		}
		if m.Darwin.WiredBytes != 16<<30 || m.UsedBytes != 16<<30 {
			t.Errorf("wired = %d, used = %d, want %d", m.Darwin.WiredBytes, m.UsedBytes, uint64(16<<30))
		}
	})

	t.Run("no swap in use is zeros, not an error", func(t *testing.T) {
		v := mini()
		v.SwapTotal, v.SwapUsed = 0, 0
		m, err := memoryFromVM(v)
		if err != nil || m.SwapTotalBytes != 0 || m.SwapUsedBytes != 0 {
			t.Errorf("memory = %+v, err %v", m, err)
		}
	})

	for level, want := range map[int32]string{1: "normal", 2: "warn", 4: "critical"} {
		t.Run(fmt.Sprintf("pressure level %d is %s", level, want), func(t *testing.T) {
			v := mini()
			v.PressureLevel = level
			m, err := memoryFromVM(v)
			if err != nil || m.Darwin.Pressure == nil || *m.Darwin.Pressure != want {
				t.Errorf("pressure = %v, err %v, want %s", m.Darwin.Pressure, err, want)
			}
		})
	}

	for _, tc := range []struct {
		name, want string
		level      int32
		ok         bool
	}{
		{"pressure level 0 is an error, not a guess, and the figures are kept", "is 0", 0, true},
		{"pressure level 3 is an error, not a guess, and the figures are kept", "is 3", 3, true},
		{"a failed pressure sysctl is an error, and the figures are kept", "pressure is unknown", 1, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			v := mini()
			v.PressureLevel, v.PressureOK = tc.level, tc.ok
			m, err := memoryFromVM(v)
			if err == nil || !strings.Contains(err.Error(), tc.want) || !strings.Contains(err.Error(), "kern.memorystatus_vm_pressure_level") {
				t.Errorf("err = %v, want one naming the sysctl and %q", err, tc.want)
			}
			if m == nil || m.Darwin == nil || m.TotalBytes != 16<<30 || m.Darwin.WiredBytes == 0 {
				t.Fatalf("memory = %+v, want the figures kept", m)
			}
			if m.Darwin.Pressure != nil {
				t.Errorf("pressure = %q, want nil", *m.Darwin.Pressure)
			}
		})
	}

	for _, tc := range []struct {
		name, want string
		change     func(*vmMemory)
	}{
		{"a zero hw.memsize is an error", "hw.memsize", func(v *vmMemory) { v.MemSize = 0 }},
		{"a zero page size is an error", "hw.pagesize", func(v *vmMemory) { v.PageSize = 0 }},
		{"more purgeable than internal pages is an error, not a wrapped count", "purgeable", func(v *vmMemory) { v.Purgeable = v.Internal + 1 }},
		{"used beyond hw.memsize is an error, not a wrapped available", "more than hw.memsize", func(v *vmMemory) { v.Wire = 1 << 20 }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			v := mini()
			tc.change(&v)
			m, err := memoryFromVM(v)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("err = %v, want one mentioning %q", err, tc.want)
			}
			if m != nil {
				t.Errorf("memory = %+v alongside an error, want nil", m)
			}
		})
	}
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

func TestDisksFromStatfs(t *testing.T) {
	// getfsstat on an Apple M5 MacBook Air (macOS 26), as `mount` and `df -k`
	// listed it: every APFS volume of the internal disk reports the
	// container's size and free space.
	const bsize, container, avail = 4096, 120686613, 89231218
	mac := []fsMount{
		{"/", "/dev/disk3s1s1", "apfs", bsize, container, avail},
		{"/dev", "devfs", "devfs", 512, 400, 0},
		{"/System/Volumes/VM", "/dev/disk3s6", "apfs", bsize, container, avail},
		{"/System/Volumes/Preboot", "/dev/disk3s2", "apfs", bsize, container, avail},
		{"/System/Volumes/Update", "/dev/disk3s4", "apfs", bsize, container, avail},
		{"/System/Volumes/xarts", "/dev/disk1s2", "apfs", bsize, 140800, 135061},
		{"/System/Volumes/iSCPreboot", "/dev/disk1s1", "apfs", bsize, 140800, 135061},
		{"/System/Volumes/Hardware", "/dev/disk1s3", "apfs", bsize, 140800, 135061},
		{"/System/Volumes/Data", "/dev/disk3s5", "apfs", bsize, container, avail},
		{"/System/Volumes/Data/home", "map auto_home", "autofs", bsize, 0, 0},
		{"/System/Volumes/Update/SFR/mnt1", "/dev/disk2s1", "apfs", bsize, 1310709, 923918},
		{"/System/Volumes/Update/mnt1", "/dev/disk3s1", "apfs", bsize, container, avail},
		{"/Volumes/Backup Disk", "/dev/disk5s1", "apfs", bsize, 488378646, 300000000},
		{"/Volumes/share", "//nas._smb._tcp.local/share", "smbfs", 4096, 1000, 400},
	}

	t.Run("only / and the user's volumes are left once devfs, autofs, and /System/Volumes are excluded", func(t *testing.T) {
		got := disksFromStatfs(mac, nil)
		want := []Disk{
			{Mount: "/", Device: "/dev/disk3s1s1", FS: "apfs", TotalBytes: container * bsize, FreeBytes: avail * bsize},
			{Mount: "/Volumes/Backup Disk", Device: "/dev/disk5s1", FS: "apfs", TotalBytes: 488378646 * bsize, FreeBytes: 300000000 * bsize},
			{Mount: "/Volumes/share", Device: "//nas._smb._tcp.local/share", FS: "smbfs", TotalBytes: 4096000, FreeBytes: 1638400},
		}
		if !slices.Equal(got, want) {
			t.Errorf("disks = %+v\nwant    %+v", got, want)
		}
	})

	t.Run("free is the container's available space, so / counts every volume's use as Finder does", func(t *testing.T) {
		d := disksFromStatfs(mac[:1], nil)[0]
		// 494 GB with 365 GB available: 129 GB used, 26%, where df's 5% is
		// the sealed system volume alone.
		if used := d.TotalBytes - d.FreeBytes; used != (container-avail)*bsize {
			t.Errorf("used = %d, want %d", used, (container-avail)*bsize)
		}
	})

	t.Run("a user's -disk-exclude replaces the defaults", func(t *testing.T) {
		got := disksFromStatfs(mac, regexp.MustCompile(`^(devfs|autofs|smbfs|/Volumes/.*)$`))
		if len(got) != 10 || got[0].Mount != "/" || got[1].Mount != "/System/Volumes/VM" {
			t.Errorf("got %d disks starting %+v", len(got), got)
		}
	})

	t.Run("sizes multiply in 64 bits past 4 GiB of blocks", func(t *testing.T) {
		d := disksFromStatfs([]fsMount{{"/", "/dev/disk3s1s1", "apfs", 1 << 20, 1 << 33, 1 << 32}}, nil)[0]
		if d.TotalBytes != 1<<53 || d.FreeBytes != 1<<52 {
			t.Errorf("total %d, free %d", d.TotalBytes, d.FreeBytes)
		}
	})
}

func TestDiskIOFromShim(t *testing.T) {
	t.Run("the driver's counters are keyed by the whole disk's name", func(t *testing.T) {
		got := diskIOFromShim(0, "disk0", 689591828480, 451120357376)
		if got == nil || *got != (DiskIO{Device: "disk0", ReadBytes: 689591828480, WriteBytes: 451120357376}) {
			t.Errorf("got %+v", got)
		}
	})
	for rc, why := range map[int]string{-1: "no IOMedia by that name", -2: "no IOBlockStorageDriver above it", -3: "a VM disk without statistics"} {
		t.Run(fmt.Sprintf("%s (%d) gives no counters, so null rates, not a failed group", why, rc), func(t *testing.T) {
			if got := diskIOFromShim(rc, "disk0", 1, 2); got != nil {
				t.Errorf("got %+v, want nil", got)
			}
		})
	}
	t.Run("a driver whose disk has no BSD name gives no counters, since nothing could key them", func(t *testing.T) {
		if got := diskIOFromShim(0, "", 1, 2); got != nil {
			t.Errorf("got %+v, want nil", got)
		}
	})
}

func TestGPUFromAccelerators(t *testing.T) {
	// The shim's copies of what `ioreg -r -c IOAccelerator` shows: an Apple
	// Silicon AGXAccelerator's PerformanceStatistics carries "Device
	// Utilization %" beside "Renderer" and "Tiler Utilization %", as an
	// integer the shim reads as a double.
	busy := gpuReading{HasStats: true, HasUtil: true, Util: 37}
	for _, tc := range []struct {
		name string
		n    int
		rs   []gpuReading
		want *GPU
		err  string
	}{
		{"one accelerator's Device Utilization % is the reading", 1, []gpuReading{busy}, &GPU{UtilizationPct: 37}, ""},
		{"an idle GPU is 0, a real reading", 1, []gpuReading{{HasStats: true, HasUtil: true, Util: 0}}, &GPU{UtilizationPct: 0}, ""},
		{"a GPU at 100% is in range", 1, []gpuReading{{HasStats: true, HasUtil: true, Util: 100}}, &GPU{UtilizationPct: 100}, ""},
		{"two GPUs report the busier one", 2, []gpuReading{{HasStats: true, HasUtil: true, Util: 5}, busy}, &GPU{UtilizationPct: 37}, ""},
		{"a second GPU without statistics doesn't hide the first one's reading", 2, []gpuReading{busy, {}}, &GPU{UtilizationPct: 37}, ""},
		{"a missing key is a failed group, never 0", 1, []gpuReading{{HasStats: true}},
			nil, `IOAccelerator 1's PerformanceStatistics has no "Device Utilization %"`},
		{"an accelerator without PerformanceStatistics is a failed group", 1, []gpuReading{{}},
			nil, "IOAccelerator 1 has no PerformanceStatistics"},
		{"a value over 100 is a failed group, never clamped", 1, []gpuReading{{HasStats: true, HasUtil: true, Util: 4294967295}},
			nil, `IOAccelerator 1 reports "Device Utilization %" of 4.294967295e+09, outside 0 to 100`},
		{"a negative value is a failed group", 1, []gpuReading{{HasStats: true, HasUtil: true, Util: -1}},
			nil, `IOAccelerator 1 reports "Device Utilization %" of -1, outside 0 to 100`},
		{"NaN is a failed group", 1, []gpuReading{{HasStats: true, HasUtil: true, Util: math.NaN()}},
			nil, `IOAccelerator 1 reports "Device Utilization %" of NaN, outside 0 to 100`},
		{"with no good reading, the first accelerator's problem is the one named", 2, []gpuReading{{}, {HasStats: true}},
			nil, "IOAccelerator 1 has no PerformanceStatistics"},
		{"no accelerator at all is a failed group", 0, nil,
			nil, "the IORegistry lists no IOAccelerator, so there is no GPU to read"},
		{"a failed registry lookup is a failed group", -1, nil,
			nil, "listing IOAccelerator services in the IORegistry failed"},
		{"more accelerators than the shim holds is a failed group", 9, make([]gpuReading, 8),
			nil, "the IORegistry lists 9 IOAccelerator services, more than the shim's 8"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := gpuFromAccelerators(tc.n, tc.rs)
			if tc.err == "" && err != nil || tc.err != "" && (err == nil || err.Error() != tc.err) {
				t.Errorf("error %v, want %q", err, tc.err)
			}
			if (got == nil) != (tc.want == nil) || got != nil && *got != *tc.want {
				t.Errorf("got %+v, want %+v", got, tc.want)
			}
		})
	}
}
