package collector

import (
	"errors"
	"fmt"
	"time"
)

// Pure conversion of the darwin shim's output, untagged so Linux tests cover
// it (decision 5). darwin.go copies the C structs into these mirrors.

// timeval mirrors kern.boottime's struct timeval.
type timeval struct{ Sec, Usec int64 }

// loadavg mirrors vm.loadavg's struct loadavg: fixed-point averages over fscale.
type loadavg struct {
	Ldavg  [3]uint32
	Fscale int64
}

// hostFromSysctl builds the host group from kern.boottime and vm.loadavg.
// darwin has no uptime counter of its own that matches `uptime` and Activity
// Monitor, which both count from the boot time, so neither do we.
func hostFromSysctl(boot timeval, la loadavg, now time.Time) (Host, error) {
	if la.Fscale <= 0 {
		return Host{}, fmt.Errorf("sysctl vm.loadavg: fscale is %d, want > 0", la.Fscale)
	}
	bt := time.Unix(boot.Sec, boot.Usec*1000)
	if boot.Sec <= 0 || bt.After(now) {
		return Host{}, errors.New("sysctl kern.boottime: boot time is not in the past; is the clock set?")
	}
	h := Host{UptimeSeconds: uint64(now.Sub(bt) / time.Second), BootTime: bt}
	for i, v := range la.Ldavg {
		h.Load[i] = float64(v) / float64(la.Fscale)
	}
	return h, nil
}

// cpuLoad mirrors one core of host_processor_info(PROCESSOR_CPU_LOAD_INFO):
// cumulative ticks indexed by CPU_STATE_USER, _SYSTEM, _IDLE, and _NICE.
type cpuLoad [4]uint32

const (
	stateUser = iota
	stateSystem
	stateIdle
	stateNice
)

// cpuFromLoad folds each core's ticks into busy and idle, and sums the cores
// for the total. The kernel's counters are 32 bits. Widening them keeps the
// total from wrapping early, and a core's own wrap (2³² ticks: at least 16
// months at the 100 Hz tick rate) looks like a backwards counter, which the
// sampler reports as a null rate for that one sample.
func cpuFromLoad(loads []cpuLoad) CPU {
	c := CPU{PerCore: make([]CPUTicks, len(loads))}
	for i, l := range loads {
		t := CPUTicks{
			Busy: uint64(l[stateUser]) + uint64(l[stateSystem]) + uint64(l[stateNice]),
			Idle: uint64(l[stateIdle]),
		}
		c.PerCore[i] = t
		c.Total.Busy += t.Busy
		c.Total.Idle += t.Idle
	}
	return c
}

// vmMemory mirrors the shim's struct vitals_memory: page counts from
// host_statistics64(HOST_VM_INFO64), plus the sysctls around them.
type vmMemory struct {
	MemSize, PageSize                 uint64
	Active, Inactive, Wire, Purgeable uint32
	Compressor, Internal, External    uint32
	SwapTotal, SwapUsed               uint64
	PressureLevel                     int32
	PressureOK                        bool
}

// The levels kern.memorystatus_vm_pressure_level reports, from the kernel's
// kVMPressureNormal, kVMPressureWarning, and kVMPressureCritical.
var pressureLevels = map[int32]string{1: "normal", 2: "warn", 4: "critical"}

// memoryFromVM computes the memory group the way Activity Monitor does (PLAN's
// assumption): used is app memory (internal − purgeable) + wired + compressed,
// and available is the rest. Page counts are widened before multiplying, since
// 2²⁰ pages of 16 KiB is past 2³². Counts that don't add up are an error rather
// than a clamped guess. An unknown pressure level keeps the figures, which are
// right without it, and returns the error alongside them (decision 22).
func memoryFromVM(v vmMemory) (*Memory, error) {
	switch {
	case v.MemSize == 0:
		return nil, errors.New("sysctl hw.memsize: total memory is 0")
	case v.PageSize == 0:
		return nil, errors.New("sysctl hw.pagesize: page size is 0")
	case v.Purgeable > v.Internal:
		return nil, fmt.Errorf("host_statistics64: %d purgeable pages, but only %d internal", v.Purgeable, v.Internal)
	}
	pages := func(n uint32) uint64 { return uint64(n) * v.PageSize }
	used := pages(v.Internal-v.Purgeable) + pages(v.Wire) + pages(v.Compressor)
	if used > v.MemSize {
		return nil, fmt.Errorf("host_statistics64: %d bytes used, more than hw.memsize's %d", used, v.MemSize)
	}
	m := &Memory{
		TotalBytes:     v.MemSize,
		UsedBytes:      used,
		AvailableBytes: v.MemSize - used,
		SwapTotalBytes: v.SwapTotal,
		SwapUsedBytes:  v.SwapUsed,
		Darwin: &MemoryDarwin{
			WiredBytes:      pages(v.Wire),
			CompressedBytes: pages(v.Compressor),
			ActiveBytes:     pages(v.Active),
			InactiveBytes:   pages(v.Inactive),
			FileBackedBytes: pages(v.External),
		},
	}
	if !v.PressureOK {
		return m, errors.New("sysctl kern.memorystatus_vm_pressure_level failed, so pressure is unknown")
	}
	level, ok := pressureLevels[v.PressureLevel]
	if !ok {
		return m, fmt.Errorf("kern.memorystatus_vm_pressure_level is %d; only 1, 2, and 4 (normal, warn, critical) are known", v.PressureLevel)
	}
	m.Darwin.Pressure = &level
	return m, nil
}

// cpuNode mirrors one child of IODeviceTree:/cpus: its "logical-cpu-id"
// (-1 when missing) and the first byte of its "cluster-type" (0 when missing).
type cpuNode struct {
	ID      int
	Cluster byte
}

// perfLevels mirrors hw.nperflevels and hw.perflevel{0,1}.logicalcpu. Levels
// is 0 where hw.nperflevels doesn't exist.
type perfLevels struct {
	Levels  int
	Logical [2]int
}

// coreKinds tags each of ncpu logical cores P or E. A chip with one
// performance level (a VM, an Intel Mac) is all P. Otherwise each core's kind
// is its registry entry's cluster-type, and the totals must match the perflevel
// sysctls. Anything that doesn't add up is an error rather than a guess: the
// registry lists efficiency cores first on some chips, so no fallback by
// position would be right.
func coreKinds(ncpu int, pl perfLevels, nodes []cpuNode) (*CPUDarwin, error) {
	if ncpu <= 0 {
		return nil, errors.New("host_processor_info reported no CPUs at startup")
	}
	d := &CPUDarwin{CoreKinds: make([]string, ncpu)}
	switch {
	case pl.Levels <= 1:
		for i := range d.CoreKinds {
			d.CoreKinds[i] = "P"
		}
		d.PCores = ncpu
		return d, nil
	case pl.Levels > 2:
		return nil, fmt.Errorf("hw.nperflevels is %d; only P and E cores are known", pl.Levels)
	}
	for _, n := range nodes {
		switch {
		case n.ID < 0:
			continue // not a core
		case n.ID >= ncpu:
			return nil, fmt.Errorf("IODeviceTree:/cpus has logical-cpu-id %d, but there are %d CPUs", n.ID, ncpu)
		case d.CoreKinds[n.ID] != "":
			return nil, fmt.Errorf("IODeviceTree:/cpus lists logical-cpu-id %d twice", n.ID)
		case n.Cluster == 'P':
			d.PCores++
		case n.Cluster == 'E':
			d.ECores++
		case n.Cluster == 0:
			return nil, fmt.Errorf("IODeviceTree:/cpus: logical CPU %d has no cluster-type", n.ID)
		default:
			return nil, fmt.Errorf("IODeviceTree:/cpus: logical CPU %d has cluster-type %q, want P or E", n.ID, string(n.Cluster))
		}
		d.CoreKinds[n.ID] = string(n.Cluster)
	}
	for i, k := range d.CoreKinds {
		if k == "" {
			return nil, fmt.Errorf("IODeviceTree:/cpus has no entry for logical CPU %d", i)
		}
	}
	if d.PCores != pl.Logical[0] || d.ECores != pl.Logical[1] {
		return nil, fmt.Errorf("IODeviceTree:/cpus tags %dP+%dE, but hw.perflevel0/1.logicalcpu say %d+%d",
			d.PCores, d.ECores, pl.Logical[0], pl.Logical[1])
	}
	return d, nil
}
