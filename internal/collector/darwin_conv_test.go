package collector

import (
	"strings"
	"testing"
	"time"
)

func TestHostFromSysctl(t *testing.T) {
	now := time.Date(2026, 10, 21, 14, 2, 6, 900_000_000, time.UTC)
	boot := timeval{Sec: now.Add(-10 * 24 * time.Hour).Unix(), Usec: 250_000}
	// FSCALE is 1 << 11 on darwin.
	la := loadavg{Ldavg: [3]uint32{1024, 2048, 3072}, Fscale: 2048}

	t.Run("boot time keeps its microseconds, and uptime is whole seconds since it", func(t *testing.T) {
		h, err := hostFromSysctl(boot, la, now)
		if err != nil {
			t.Fatal(err)
		}
		if want := time.Unix(boot.Sec, 250_000_000); !h.BootTime.Equal(want) {
			t.Errorf("boot time = %v, want %v", h.BootTime, want)
		}
		// 10 days and 0.65 s.
		if h.UptimeSeconds != 864000 {
			t.Errorf("uptime = %d, want 864000", h.UptimeSeconds)
		}
	})

	t.Run("load averages are the fixed-point values over fscale", func(t *testing.T) {
		h, _ := hostFromSysctl(boot, la, now)
		if h.Load != [3]float64{0.5, 1, 1.5} {
			t.Errorf("load = %v, want [0.5 1 1.5]", h.Load)
		}
		h, _ = hostFromSysctl(boot, loadavg{Fscale: 2048}, now)
		if h.Load != [3]float64{} {
			t.Errorf("idle load = %v, want zeros", h.Load)
		}
	})

	for _, tc := range []struct {
		name, want string
		boot       timeval
		la         loadavg
	}{
		{"an fscale of zero is an error, not a division by zero", "fscale", boot, loadavg{Ldavg: [3]uint32{1, 1, 1}}},
		{"a boot time in the future is an error, not a huge uptime", "boot time", timeval{Sec: now.Unix() + 60}, la},
		{"a zero boot time is an error", "boot time", timeval{}, la},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := hostFromSysctl(tc.boot, tc.la, now)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("err = %v, want one mentioning %q", err, tc.want)
			}
		})
	}
}
