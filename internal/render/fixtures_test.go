package render

import (
	"time"

	"github.com/nmcginn/vitals/internal/collector"
	"github.com/nmcginn/vitals/internal/sampler"
)

// Fixed snapshots shared by every renderer's golden tests.

var sampledAt = time.Date(2026, 10, 21, 14, 2, 6, 0, time.UTC)

func p[T any](v T) *T { return &v }

// darwinFull is a second sample on an M4 Mac Mini: every group, every rate.
func darwinFull() *sampler.Snapshot {
	boot := time.Date(2026, 10, 11, 14, 2, 6, 0, time.UTC)
	return &sampler.Snapshot{
		SampledAt:       sampledAt,
		IntervalSeconds: 2,
		Host: sampler.Host{
			Hostname: "mini", OS: "darwin", OSVersion: "26.1", Kernel: "25.1.0", Arch: "arm64",
			UptimeSeconds: p(uint64(864000)), BootTime: &boot, Load: &[3]float64{0.41, 0.38, 0.35},
		},
		CPU: &sampler.CPU{
			UsagePct:   p(6.2),
			PerCorePct: []*float64{p(9.1), p(4.0), p(12.5), p(7.0), p(3.2), p(2.8), p(5.5), p(1.0), p(0.0), p(16.0)},
			Cores:      10,
			Model:      "Apple M4",
			Darwin: &collector.CPUDarwin{
				PCores: 4, ECores: 6,
				CoreKinds: []string{"P", "P", "P", "P", "E", "E", "E", "E", "E", "E"},
			},
		},
		Memory: &collector.Memory{
			TotalBytes: 17179869184, AvailableBytes: 7516192768, UsedBytes: 9663676416,
			SwapTotalBytes: 0, SwapUsedBytes: 0,
			Darwin: &collector.MemoryDarwin{
				WiredBytes: 2147483648, CompressedBytes: 536870912, ActiveBytes: 4294967296,
				InactiveBytes: 3221225472, FileBackedBytes: 2684354560, Pressure: p("normal"),
			},
		},
		Disks: []sampler.Disk{{
			Mount: "/", Device: "/dev/disk3s1", FS: "apfs",
			TotalBytes: 494384795648, UsedBytes: 120259084288, FreeBytes: 374125711360,
			UsedPct: p(24.3), ReadBps: p(0.0), WriteBps: p(40960.0),
		}},
		Network: []sampler.Interface{{
			Name: "en0", RxBps: p(12800.0), TxBps: p(3400.0), RxPps: p(21.0), TxPps: p(14.0),
			RxTotalBytes: 98234111, TxTotalBytes: 12001221,
		}},
		Errors: map[string]string{},
	}
}

// darwinFirst is the first sample after startup: no rates yet.
func darwinFirst() *sampler.Snapshot {
	s := darwinFull()
	s.CPU.UsagePct = nil
	for i := range s.CPU.PerCorePct {
		s.CPU.PerCorePct[i] = nil
	}
	s.Disks[0].ReadBps, s.Disks[0].WriteBps = nil, nil
	n := &s.Network[0]
	n.RxBps, n.TxBps, n.RxPps, n.TxPps = nil, nil, nil, nil
	return s
}

// linuxPartial is a Linux box with sensors, a long mount path containing a
// space, a mount with no physical device, a zero-size filesystem, and a
// failed network group.
func linuxPartial() *sampler.Snapshot {
	boot := time.Date(2026, 9, 1, 8, 0, 0, 0, time.UTC)
	return &sampler.Snapshot{
		SampledAt:       sampledAt,
		IntervalSeconds: 2,
		Host: sampler.Host{
			Hostname: `box<"&">`, OS: "linux", OSVersion: "Debian GNU/Linux 13 (trixie)", Kernel: "6.12.48-amd64", Arch: "amd64",
			UptimeSeconds: p(uint64(4384926)), BootTime: &boot, Load: &[3]float64{1.5, 0.75, 0.25},
		},
		CPU: &sampler.CPU{UsagePct: p(100.0), PerCorePct: []*float64{p(100.0)}, Cores: 1, Model: "Intel(R) Celeron(R) N5105 @ 2.00GHz"},
		Memory: &collector.Memory{
			TotalBytes: 8221089792, AvailableBytes: 5368709120, UsedBytes: 2852380672,
		},
		Disks: []sampler.Disk{
			{
				Mount: "/", Device: "/dev/nvme0n1p2", FS: "ext4",
				TotalBytes: 250790436864, UsedBytes: 50158087372, FreeBytes: 200632349492,
				UsedPct: p(20.0), ReadBps: p(1536.0), WriteBps: p(204800.5),
			},
			{
				Mount: "/srv/media library/archive", Device: "nas:/export", FS: "nfs4",
				TotalBytes: 4000787030016, UsedBytes: 3000590272512, FreeBytes: 1000196757504,
				UsedPct: p(75.0),
			},
			{Mount: "/mnt/empty", Device: "/dev/loop9", FS: "ext4"},
		},
		Temps: []collector.Temp{
			{Label: "Package id 0", Celsius: 48},
			{Label: "nvme/temp1", Celsius: 39.85},
		},
		Errors: map[string]string{"network": "reading /proc/net/dev: permission denied"},
	}
}
