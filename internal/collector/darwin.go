//go:build darwin

package collector

/*
#cgo LDFLAGS: -framework IOKit -framework CoreFoundation
#include "darwin_shim.h"
*/
import "C"

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"os"
	"time"
)

type darwin struct {
	info HostInfo
	// The P/E split is fixed for the life of the process, so it's read once
	// and shared by every collection (decision 20).
	kinds    *CPUDarwin
	kindsErr error
	// Reused each tick, so it isn't 4 KiB of garbage every 2s. Only the
	// sampler goroutine calls Collect.
	load [C.VITALS_MAX_CPUS]C.struct_vitals_cpu_load
}

// New returns the collector for this OS.
func New() Collector {
	var s C.struct_vitals_host_static
	C.vitals_host_static(&s)
	hostname, _ := os.Hostname()
	d := &darwin{info: HostInfo{
		Hostname:  hostname,
		OS:        "darwin",
		OSVersion: C.GoString(&s.os_version[0]),
		Kernel:    C.GoString(&s.kernel[0]),
		Arch:      C.GoString(&s.machine[0]),
		CPUModel:  cmp.Or(C.GoString(&s.cpu_model[0]), unknownModel),
	}}
	levels := perfLevels{Levels: int(s.nperflevels), Logical: [2]int{int(s.perflevel_logical[0]), int(s.perflevel_logical[1])}}
	d.kinds, d.kindsErr = coreKinds(int(C.vitals_cpu_load(&d.load[0], C.VITALS_MAX_CPUS)), levels, cpuNodes())
	return d
}

// cpuNodes reads the children of IODeviceTree:/cpus, or none if the registry
// has no /cpus (coreKinds then reports the missing entries).
func cpuNodes() []cpuNode {
	var buf [C.VITALS_MAX_CPUS]C.struct_vitals_cpu_node
	n := min(int(C.vitals_cpu_nodes(&buf[0], C.VITALS_MAX_CPUS)), C.VITALS_MAX_CPUS)
	nodes := make([]cpuNode, max(n, 0))
	for i := range nodes {
		nodes[i] = cpuNode{ID: int(buf[i].logical_id), Cluster: byte(buf[i].cluster_type)}
	}
	return nodes
}

func (d *darwin) Static() HostInfo { return d.info }

// Temperatures are absent on darwin in v1 (they need private API), so temps
// is simply empty rather than an error.
func (d *darwin) Collect(ctx context.Context) (Raw, error) {
	raw := Raw{Errors: pending(GroupDisks, GroupNetwork)}
	var h C.struct_vitals_host
	switch C.vitals_host(&h) {
	case 0:
		host, err := hostFromSysctl(
			timeval{Sec: int64(h.boot_sec), Usec: int64(h.boot_usec)},
			loadavg{Ldavg: [3]uint32{uint32(h.ldavg[0]), uint32(h.ldavg[1]), uint32(h.ldavg[2])}, Fscale: int64(h.fscale)},
			time.Now())
		if err != nil {
			raw.Errors[GroupHost] = err
		} else {
			raw.Host = &host
		}
	case -1:
		raw.Errors[GroupHost] = errors.New("reading sysctl kern.boottime failed")
	default:
		raw.Errors[GroupHost] = errors.New("reading sysctl vm.loadavg failed")
	}
	var err error
	if raw.CPU, err = d.cpu(); err != nil {
		raw.Errors[GroupCPU] = err
	}
	if raw.Memory, err = memory(); err != nil {
		raw.Errors[GroupMemory] = err
	}
	return raw, nil
}

// memory returns the memory group, which may come with an error when only the
// pressure level is unknown (decision 22).
func memory() (*Memory, error) {
	var m C.struct_vitals_memory
	switch C.vitals_memory(&m) {
	case 0:
	case -1:
		return nil, errors.New("reading sysctl hw.memsize failed")
	case -2:
		return nil, errors.New("reading sysctl hw.pagesize failed")
	case -3:
		return nil, errors.New("host_statistics64(HOST_VM_INFO64) failed")
	default:
		return nil, errors.New("reading sysctl vm.swapusage failed")
	}
	return memoryFromVM(vmMemory{
		MemSize:       uint64(m.memsize),
		PageSize:      uint64(m.pagesize),
		Active:        uint32(m.active_count),
		Inactive:      uint32(m.inactive_count),
		Wire:          uint32(m.wire_count),
		Purgeable:     uint32(m.purgeable_count),
		Compressor:    uint32(m.compressor_page_count),
		Internal:      uint32(m.internal_page_count),
		External:      uint32(m.external_page_count),
		SwapTotal:     uint64(m.swap_total),
		SwapUsed:      uint64(m.swap_used),
		PressureLevel: int32(m.pressure_level),
		PressureOK:    m.pressure_ok != 0,
	})
}

// cpu returns the CPU group, or nil and an error when there are no ticks. With
// ticks but no P/E split it returns both: usage is still worth reporting.
func (d *darwin) cpu() (*CPU, error) {
	n := int(C.vitals_cpu_load(&d.load[0], C.VITALS_MAX_CPUS))
	switch {
	case n <= 0:
		return nil, errors.New("host_processor_info(PROCESSOR_CPU_LOAD_INFO) failed")
	case n > C.VITALS_MAX_CPUS:
		return nil, fmt.Errorf("host_processor_info reported %d CPUs, more than the shim's %d", n, C.VITALS_MAX_CPUS)
	}
	loads := make([]cpuLoad, n)
	for i := range loads {
		t := d.load[i].ticks
		loads[i] = cpuLoad{uint32(t[0]), uint32(t[1]), uint32(t[2]), uint32(t[3])}
	}
	c := cpuFromLoad(loads)
	switch {
	case d.kindsErr != nil:
		return &c, fmt.Errorf("P/E cores: %w", d.kindsErr)
	case len(d.kinds.CoreKinds) != n:
		return &c, fmt.Errorf("P/E cores: tagged %d at startup, but host_processor_info now reports %d", len(d.kinds.CoreKinds), n)
	}
	c.Darwin = d.kinds
	return &c, nil
}
