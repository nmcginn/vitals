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
