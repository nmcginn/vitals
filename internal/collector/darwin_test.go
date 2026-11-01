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
	c := New(Options{})

	t.Run("static fields are read from sysctl", func(t *testing.T) {
		info := c.Static()
		if info.OS != "darwin" || info.Kernel == "" || info.OSVersion == "" || info.Arch == "" || info.Hostname == "" ||
			info.CPUModel == "" || info.CPUModel == unknownModel {
			t.Errorf("Static() = %+v, want every field set", info)
		}
	})

	t.Run("two collections give sane, non-decreasing host, CPU, memory, network, and disk groups", func(t *testing.T) {
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
		for i, raw := range []Raw{first, second} {
			m := raw.Memory
			if raw.Errors[GroupMemory] != nil || m == nil || m.Darwin == nil {
				t.Fatalf("collection %d: memory error %v", i+1, raw.Errors[GroupMemory])
			}
			if m.TotalBytes == 0 || m.UsedBytes > m.TotalBytes || m.AvailableBytes > m.TotalBytes || m.UsedBytes+m.AvailableBytes != m.TotalBytes {
				t.Errorf("collection %d: total %d, used %d, available %d", i+1, m.TotalBytes, m.UsedBytes, m.AvailableBytes)
			}
			if m.SwapUsedBytes > m.SwapTotalBytes {
				t.Errorf("collection %d: swap used %d of %d", i+1, m.SwapUsedBytes, m.SwapTotalBytes)
			}
			if p := m.Darwin.Pressure; p == nil || (*p != "normal" && *p != "warn" && *p != "critical") {
				t.Errorf("collection %d: pressure %v, want normal, warn, or critical", i+1, p)
			}
			t.Logf("collection %d: total %d, used %d, wired %d, compressed %d, swap %d of %d, pressure %s", i+1,
				m.TotalBytes, m.UsedBytes, m.Darwin.WiredBytes, m.Darwin.CompressedBytes, m.SwapUsedBytes, m.SwapTotalBytes, *m.Darwin.Pressure)
		}
		for i, raw := range []Raw{first, second} {
			if err := raw.Errors[GroupNetwork]; err != nil || len(raw.Network) == 0 {
				t.Fatalf("collection %d: network %v, error %v; want at least one interface", i+1, raw.Network, err)
			}
			for _, n := range raw.Network {
				if defaultNetExclude.MatchString(n.Name) {
					t.Errorf("collection %d: %s should have been filtered out", i+1, n.Name)
				}
			}
			t.Logf("collection %d: %+v", i+1, raw.Network)
		}
		before := map[string]Interface{}
		for _, n := range first.Network {
			before[n.Name] = n
		}
		matched := 0
		for _, b := range second.Network {
			a, ok := before[b.Name]
			if !ok {
				continue
			}
			matched++
			if b.RxBytes < a.RxBytes || b.TxBytes < a.TxBytes || b.RxPackets < a.RxPackets || b.TxPackets < a.TxPackets {
				t.Errorf("%s counters went backwards: %+v then %+v", b.Name, a, b)
			}
		}
		if matched == 0 {
			t.Errorf("no interface was in both collections: %v then %v", first.Network, second.Network)
		}
		for i, raw := range []Raw{first, second} {
			if err := raw.Errors[GroupDisks]; err != nil {
				t.Fatalf("collection %d: disks error %v", i+1, err)
			}
			var root *Disk
			for j, d := range raw.Disks {
				if d.Mount == "/" {
					root = &raw.Disks[j]
				}
				if defaultDiskExclude.MatchString(d.Mount) || defaultDiskExclude.MatchString(d.FS) {
					t.Errorf("collection %d: %s (%s) should have been filtered out", i+1, d.Mount, d.FS)
				}
			}
			if root == nil {
				t.Fatalf("collection %d: no / in %+v", i+1, raw.Disks)
			}
			if root.TotalBytes == 0 || root.FreeBytes > root.TotalBytes {
				t.Errorf("collection %d: / total %d, free %d", i+1, root.TotalBytes, root.FreeBytes)
			}
			if pct := float64(root.TotalBytes-root.FreeBytes) / float64(root.TotalBytes) * 100; pct < 0 || pct > 100 {
				t.Errorf("collection %d: / used %.1f%%, want 0 to 100", i+1, pct)
			}
			for _, d := range raw.Disks {
				t.Logf("collection %d: %s on %s (%s): %d of %d free, io %+v", i+1, d.Device, d.Mount, d.FS, d.FreeBytes, d.TotalBytes, d.IO)
			}
		}
		// A CI VM's virtual disk may have no statistics, which is null rates,
		// so the counters are checked only where both collections have them.
		io := map[string]*DiskIO{}
		for _, d := range first.Disks {
			io[d.Mount] = d.IO
		}
		for _, b := range second.Disks {
			if a := io[b.Mount]; a != nil && b.IO != nil && (b.IO.ReadBytes < a.ReadBytes || b.IO.WriteBytes < a.WriteBytes) {
				t.Errorf("%s I/O counters went backwards: %+v then %+v", b.Mount, a, b.IO)
			}
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
	t.Run("the GPU group reads 0 to 100, or fails without a GPU reading, as on a CI VM", func(t *testing.T) {
		// A VM's paravirtual GPU may have no IOAccelerator, or one without
		// statistics, so a failed group passes as long as it says why and the
		// other groups were served.
		raw, err := c.Collect(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		gerr := raw.Errors[GroupGPU]
		switch {
		case raw.GPU != nil && gerr != nil:
			t.Errorf("gpu is %+v and failed with %v; want one or the other", raw.GPU, gerr)
		case raw.GPU != nil:
			if u := raw.GPU.UtilizationPct; !(u >= 0 && u <= 100) {
				t.Errorf("gpu utilization %v%%, want 0 to 100", u)
			}
			t.Logf("gpu utilization %v%%", raw.GPU.UtilizationPct)
		case gerr == nil:
			t.Error("gpu is nil without an error")
		default:
			t.Logf("no gpu reading: %v", gerr)
		}
		if raw.CPU == nil || raw.Memory == nil || raw.Host == nil {
			t.Errorf("the GPU group took others down with it: %+v", raw.Errors)
		}
	})
}
