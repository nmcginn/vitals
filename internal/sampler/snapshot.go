package sampler

import (
	"time"

	"github.com/nmcginn/vitals/internal/collector"
)

// Snapshot is one published sample. It is never mutated after it reaches the
// store, so renderers share it without copying. A nil pointer is a value that
// can't be known (a rate on the first sample, a group that failed) and renders
// as null, never as 0.
type Snapshot struct {
	SampledAt       time.Time         `json:"sampled_at"`
	IntervalSeconds float64           `json:"interval_s"`
	Host            Host              `json:"host"`
	CPU             *CPU              `json:"cpu"`
	Memory          *collector.Memory `json:"memory"`
	Disks           []Disk            `json:"disks"`
	Network         []Interface       `json:"network"`
	Temps           []collector.Temp  `json:"temps"`
	Darwin          *Darwin           `json:"darwin,omitempty"`
	Errors          map[string]string `json:"errors"`
}

// Host combines the static host info with the host group's gauges, which are
// null when that group failed.
type Host struct {
	Hostname      string      `json:"hostname"`
	OS            string      `json:"os"`
	OSVersion     string      `json:"os_version,omitempty"`
	Kernel        string      `json:"kernel"`
	Arch          string      `json:"arch"`
	UptimeSeconds *uint64     `json:"uptime_s"`
	BootTime      *time.Time  `json:"boot_time"`
	Load          *[3]float64 `json:"load"`
}

type CPU struct {
	UsagePct   *float64             `json:"usage_pct"`
	PerCorePct []*float64           `json:"per_core_pct"`
	Cores      int                  `json:"cores"`
	Model      string               `json:"model"`
	Darwin     *collector.CPUDarwin `json:"darwin,omitempty"`
}

// Darwin holds the groups only darwin has (decision 46). It is present on
// darwin, where a nil group failed and Errors says why, and nil elsewhere.
type Darwin struct {
	GPU *collector.GPU `json:"gpu"`
}

type Disk struct {
	Mount      string   `json:"mount"`
	Device     string   `json:"device"`
	FS         string   `json:"fs"`
	TotalBytes uint64   `json:"total_bytes"`
	UsedBytes  uint64   `json:"used_bytes"`
	FreeBytes  uint64   `json:"free_bytes"`
	UsedPct    *float64 `json:"used_pct"`
	ReadBps    *float64 `json:"read_bps"`
	WriteBps   *float64 `json:"write_bps"`
	// IODevice names the physical device the rates come from (decision 7),
	// "" when the mount has none. Prometheus labels rates with it; JSON
	// reports rates per mount and leaves it out.
	IODevice string `json:"-"`
}

type Interface struct {
	Name         string   `json:"iface"`
	RxBps        *float64 `json:"rx_bps"`
	TxBps        *float64 `json:"tx_bps"`
	RxPps        *float64 `json:"rx_pps"`
	TxPps        *float64 `json:"tx_pps"`
	RxTotalBytes uint64   `json:"rx_total_bytes"`
	TxTotalBytes uint64   `json:"tx_total_bytes"`
}

// build turns a collection into a snapshot. prev is nil on the first sample,
// and every rate is then null. elapsed is the actual time between the two
// collections, not the configured interval.
func build(info collector.HostInfo, prev *collector.Raw, cur collector.Raw, elapsed time.Duration, at time.Time, interval time.Duration) *Snapshot {
	if prev == nil {
		prev = &collector.Raw{}
	}
	secs := elapsed.Seconds()
	s := &Snapshot{
		SampledAt:       at.UTC().Truncate(time.Second),
		IntervalSeconds: interval.Seconds(),
		Host: Host{
			Hostname:  info.Hostname,
			OS:        info.OS,
			OSVersion: info.OSVersion,
			Kernel:    info.Kernel,
			Arch:      info.Arch,
		},
		Memory: cur.Memory,
		Errors: make(map[string]string, len(cur.Errors)),
	}
	if h := cur.Host; h != nil {
		up, boot, load := h.UptimeSeconds, h.BootTime.UTC().Truncate(time.Second), h.Load
		s.Host.UptimeSeconds, s.Host.BootTime, s.Host.Load = &up, &boot, &load
	}
	if c := cur.CPU; c != nil {
		s.CPU = &CPU{Cores: len(c.PerCore), Model: info.CPUModel, Darwin: c.Darwin, PerCorePct: make([]*float64, len(c.PerCore))}
		if p := prev.CPU; p != nil {
			s.CPU.UsagePct = usage(p.Total, c.Total)
			// A core going offline renumbers the rest, so per-core rates are
			// only trusted when the core count is unchanged.
			if len(p.PerCore) == len(c.PerCore) {
				for i := range c.PerCore {
					s.CPU.PerCorePct[i] = usage(p.PerCore[i], c.PerCore[i])
				}
			}
		}
	}
	if cur.Disks != nil {
		prevIO := map[string]*collector.DiskIO{}
		for _, d := range prev.Disks {
			if d.IO != nil {
				prevIO[d.IO.Device] = d.IO
			}
		}
		s.Disks = make([]Disk, len(cur.Disks))
		for i, d := range cur.Disks {
			out := Disk{Mount: d.Mount, Device: d.Device, FS: d.FS, TotalBytes: d.TotalBytes, FreeBytes: d.FreeBytes}
			if d.FreeBytes <= d.TotalBytes {
				out.UsedBytes = d.TotalBytes - d.FreeBytes
			}
			if d.TotalBytes > 0 {
				out.UsedPct = ptr(float64(out.UsedBytes) / float64(d.TotalBytes) * 100)
			}
			if d.IO != nil {
				out.IODevice = d.IO.Device
				if p := prevIO[d.IO.Device]; p != nil {
					out.ReadBps = rate(p.ReadBytes, d.IO.ReadBytes, secs)
					out.WriteBps = rate(p.WriteBytes, d.IO.WriteBytes, secs)
				}
			}
			s.Disks[i] = out
		}
	}
	if cur.Network != nil {
		prevIf := map[string]collector.Interface{}
		for _, n := range prev.Network {
			prevIf[n.Name] = n
		}
		s.Network = make([]Interface, len(cur.Network))
		for i, n := range cur.Network {
			out := Interface{Name: n.Name, RxTotalBytes: n.RxBytes, TxTotalBytes: n.TxBytes}
			if p, ok := prevIf[n.Name]; ok {
				out.RxBps = rate(p.RxBytes, n.RxBytes, secs)
				out.TxBps = rate(p.TxBytes, n.TxBytes, secs)
				out.RxPps = rate(p.RxPackets, n.RxPackets, secs)
				out.TxPps = rate(p.TxPackets, n.TxPackets, secs)
			}
			s.Network[i] = out
		}
	}
	if len(cur.Temps) > 0 {
		s.Temps = cur.Temps
	}
	if info.OS == "darwin" {
		s.Darwin = &Darwin{GPU: cur.GPU}
	}
	for g, err := range cur.Errors {
		s.Errors[g] = err.Error()
	}
	return s
}

// rate is the per-second change in a cumulative counter, or nil when it can't
// be known: a counter that went backwards (a reset, or a wrap) or no elapsed
// time. Never negative.
func rate(prev, cur uint64, secs float64) *float64 {
	if cur < prev || secs <= 0 {
		return nil
	}
	return ptr(float64(cur-prev) / secs)
}

// usage is busy ticks over all ticks, as a percentage. Nil when no ticks
// passed (which would be 0/0) or either counter went backwards.
func usage(prev, cur collector.CPUTicks) *float64 {
	if cur.Busy < prev.Busy || cur.Idle < prev.Idle {
		return nil
	}
	busy, idle := cur.Busy-prev.Busy, cur.Idle-prev.Idle
	if busy+idle == 0 {
		return nil
	}
	return ptr(float64(busy) / float64(busy+idle) * 100)
}

func ptr[T any](v T) *T { return &v }
