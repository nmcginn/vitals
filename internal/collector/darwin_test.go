//go:build darwin

package collector

import (
	"context"
	"testing"
	"time"
)

// The darwin integration test: the live OS through the shim. It checks
// invariants, never specific values, so it holds on a CI VM (decision 5).
func TestDarwinCollect(t *testing.T) {
	c := New()

	t.Run("static fields are read from sysctl", func(t *testing.T) {
		info := c.Static()
		if info.OS != "darwin" || info.Kernel == "" || info.OSVersion == "" || info.Arch == "" || info.Hostname == "" ||
			info.CPUModel == "" || info.CPUModel == unknownModel {
			t.Errorf("Static() = %+v, want every field set", info)
		}
	})

	t.Run("two collections give sane, non-decreasing host and CPU groups", func(t *testing.T) {
		first, err := c.Collect(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		time.Sleep(1100 * time.Millisecond)
		second, err := c.Collect(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		for i, raw := range []Raw{first, second} {
			if raw.Errors[GroupHost] != nil || raw.Host == nil {
				t.Fatalf("collection %d: host error %v", i+1, raw.Errors[GroupHost])
			}
			h := raw.Host
			if h.UptimeSeconds == 0 {
				t.Errorf("collection %d: uptime is 0", i+1)
			}
			if !h.BootTime.Before(time.Now()) {
				t.Errorf("collection %d: boot time %v is not in the past", i+1, h.BootTime)
			}
			for _, l := range h.Load {
				if l < 0 {
					t.Errorf("collection %d: load %v has a negative average", i+1, h.Load)
				}
			}
		}
		if second.Host.UptimeSeconds < first.Host.UptimeSeconds {
			t.Errorf("uptime went backwards: %d then %d", first.Host.UptimeSeconds, second.Host.UptimeSeconds)
		}
		if !second.Host.BootTime.Equal(first.Host.BootTime) {
			t.Errorf("boot time moved: %v then %v", first.Host.BootTime, second.Host.BootTime)
		}

		for i, raw := range []Raw{first, second} {
			c := raw.CPU
			if raw.Errors[GroupCPU] != nil || c == nil || c.Darwin == nil {
				t.Fatalf("collection %d: cpu error %v", i+1, raw.Errors[GroupCPU])
			}
			cores := len(c.PerCore)
			if d := c.Darwin; cores == 0 || d.PCores+d.ECores != cores || len(d.CoreKinds) != cores {
				t.Errorf("collection %d: %d cores, but p_cores %d + e_cores %d and %d core kinds", i+1, cores, d.PCores, d.ECores, len(d.CoreKinds))
			}
			t.Logf("collection %d: %d cores, kinds %v", i+1, cores, c.Darwin.CoreKinds)
		}
		if len(second.CPU.PerCore) != len(first.CPU.PerCore) {
			t.Fatalf("core count changed: %d then %d", len(first.CPU.PerCore), len(second.CPU.PerCore))
		}
		// Usage is the sampler's to compute, but the ticks it divides must be
		// non-decreasing, and a second is long enough for every core to tick.
		for i, b := range second.CPU.PerCore {
			a := first.CPU.PerCore[i]
			if b.Busy < a.Busy || b.Idle < a.Idle {
				t.Errorf("core %d ticks went backwards: %+v then %+v", i, a, b)
				continue
			}
			busy, all := b.Busy-a.Busy, b.Busy-a.Busy+b.Idle-a.Idle
			if all == 0 {
				t.Errorf("core %d didn't tick in %v", i, 1100*time.Millisecond)
				continue
			}
			if pct := float64(busy) / float64(all) * 100; pct < 0 || pct > 100 {
				t.Errorf("core %d usage %.1f%%, want 0 to 100", i, pct)
			}
		}
	})
}
