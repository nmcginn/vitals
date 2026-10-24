// Package collector reads raw counters and gauges from the OS. It computes no
// rates: the sampler owns all delta math (decision 4).
package collector

import (
	"context"
	"errors"
	"time"
)

// Group names, used as keys in Raw.Errors and in every renderer's error output.
const (
	GroupHost    = "host"
	GroupCPU     = "cpu"
	GroupMemory  = "memory"
	GroupDisks   = "disks"
	GroupNetwork = "network"
	GroupTemps   = "temps"
)

// pending marks the groups whose collectors haven't landed yet. Each milestone
// in docs/ROADMAP.md removes its group from the callers.
func pending(groups ...string) map[string]error {
	m := make(map[string]error, len(groups))
	for _, g := range groups {
		m[g] = errors.New("not implemented yet")
	}
	return m
}

// unknownModel is HostInfo.CPUModel when the OS doesn't name the CPU.
const unknownModel = "unknown"

// Collector is implemented once per OS. Only the sampler goroutine calls it.
type Collector interface {
	// Collect returns raw counters and gauges. A failure in one group sets
	// that group's error and leaves the others populated; the error return is
	// for when nothing at all could be collected. The caller owns the returned
	// value: implementations must not retain or reuse its slices, except for
	// static values they never write again (CPU.Darwin).
	Collect(ctx context.Context) (Raw, error)
	// Static returns what doesn't change while the process runs. It is read
	// once at startup.
	Static() HostInfo
}

// HostInfo is read once at startup.
type HostInfo struct {
	Hostname  string
	OS        string // GOOS
	OSVersion string // kern.osproductversion, or PRETTY_NAME; "" if unreadable
	Kernel    string
	Arch      string
	CPUModel  string // "unknown" when the OS doesn't say
}

// Raw is one collection. A nil group pointer (or nil slice for disks and
// network) means the group wasn't collected; Errors says why.
type Raw struct {
	Host    *Host
	CPU     *CPU
	Memory  *Memory
	Disks   []Disk
	Network []Interface
	Temps   []Temp // nil or empty when the machine has no sensors
	Errors  map[string]error
}

// Host holds the host group's gauges.
type Host struct {
	UptimeSeconds uint64
	BootTime      time.Time
	Load          [3]float64
}

// CPU holds cumulative tick counters. Each platform folds its own tick kinds
// into busy and idle (Linux counts iowait as idle). Darwin is nil off darwin,
// and on darwin when the P/E split couldn't be read; Errors then says why
// while usage is still reported (decision 20).
type CPU struct {
	Total   CPUTicks
	PerCore []CPUTicks
	Darwin  *CPUDarwin
}

type CPUTicks struct {
	Busy, Idle uint64
}

// CPUDarwin is passed through to the snapshot unchanged, so it carries its
// JSON names here rather than being copied into a twin type.
type CPUDarwin struct {
	PCores    int      `json:"p_cores"`
	ECores    int      `json:"e_cores"`
	CoreKinds []string `json:"core_kinds"`
}

// Memory is all gauges, so the snapshot uses it as is.
type Memory struct {
	TotalBytes     uint64        `json:"total_bytes"`
	AvailableBytes uint64        `json:"available_bytes"`
	UsedBytes      uint64        `json:"used_bytes"`
	SwapTotalBytes uint64        `json:"swap_total_bytes"`
	SwapUsedBytes  uint64        `json:"swap_used_bytes"`
	Darwin         *MemoryDarwin `json:"darwin,omitempty"`
}

type MemoryDarwin struct {
	WiredBytes      uint64 `json:"wired_bytes"`
	CompressedBytes uint64 `json:"compressed_bytes"`
	ActiveBytes     uint64 `json:"active_bytes"`
	InactiveBytes   uint64 `json:"inactive_bytes"`
	FileBackedBytes uint64 `json:"file_backed_bytes"`
	Pressure        string `json:"pressure"` // normal, warn, or critical
}

// Disk is one mount. IO is nil when the mount can't be mapped to a physical
// device (decision 7).
type Disk struct {
	Mount, Device, FS     string
	TotalBytes, FreeBytes uint64
	IO                    *DiskIO
}

// DiskIO holds the cumulative byte counters of the device behind a mount.
// Device keys the counters across samples; mounts sharing it share the rates.
type DiskIO struct {
	Device                string
	ReadBytes, WriteBytes uint64
}

type Interface struct {
	Name                 string
	RxBytes, TxBytes     uint64
	RxPackets, TxPackets uint64
}

type Temp struct {
	Label   string  `json:"label"`
	Celsius float64 `json:"celsius"`
}
