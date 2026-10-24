//go:build darwin

package collector

/*
#cgo LDFLAGS: -framework IOKit -framework CoreFoundation
#include "darwin_shim.h"
*/
import "C"

import (
	"context"
	"errors"
	"os"
	"time"
)

type darwin struct{ info HostInfo }

// New returns the collector for this OS.
func New() Collector {
	var s C.struct_vitals_host_static
	C.vitals_host_static(&s)
	hostname, _ := os.Hostname()
	return &darwin{info: HostInfo{
		Hostname:  hostname,
		OS:        "darwin",
		OSVersion: C.GoString(&s.os_version[0]),
		Kernel:    C.GoString(&s.kernel[0]),
		Arch:      C.GoString(&s.machine[0]),
	}}
}

func (d *darwin) Static() HostInfo { return d.info }

// Temperatures are absent on darwin in v1 (they need private API), so temps
// is simply empty rather than an error.
func (d *darwin) Collect(ctx context.Context) (Raw, error) {
	raw := Raw{Errors: pending(GroupCPU, GroupMemory, GroupDisks, GroupNetwork)}
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
	return raw, nil
}
