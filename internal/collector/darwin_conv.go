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
