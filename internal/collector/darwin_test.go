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
		if info.OS != "darwin" || info.Kernel == "" || info.OSVersion == "" || info.Arch == "" || info.Hostname == "" {
			t.Errorf("Static() = %+v, want every field set", info)
		}
	})

	t.Run("two collections give a sane, non-decreasing host group", func(t *testing.T) {
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
	})
}
