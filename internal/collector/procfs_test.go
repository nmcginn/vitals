package collector

import (
	"context"
	"io/fs"
	"maps"
	"os"
	"runtime"
	"strings"
	"testing"
	"testing/fstest"
	"time"
)

// minimal is the smallest tree the host group can be read from. Tests copy
// it and change one file.
func minimal() fstest.MapFS {
	return fstest.MapFS{
		"proc/uptime":               {Data: []byte("1000.00 3000.00\n")},
		"proc/loadavg":              {Data: []byte("1.00 0.50 0.25 2/200 999\n")},
		"proc/stat":                 {Data: []byte("cpu  1 2 3 4 5 6 7 8 0 0\ncpu0 1 2 3 4 5 6 7 8 0 0\nintr 0\nbtime 1700000000\nprocesses 1\n")},
		"proc/meminfo":              {Data: []byte("MemTotal: 1000 kB\nMemAvailable: 600 kB\nSwapTotal: 100 kB\nSwapFree: 75 kB\n")},
		"proc/sys/kernel/osrelease": {Data: []byte("6.12.48-amd64\n")},
		"proc/sys/kernel/hostname":  {Data: []byte("box\n")},
		"etc/os-release":            {Data: []byte("NAME=\"Debian GNU/Linux\"\nPRETTY_NAME=\"Debian GNU/Linux 13 (trixie)\"\n")},
	}
}

func with(changes map[string]string, removals ...string) fs.FS {
	m := minimal()
	for name, data := range changes {
		m[name] = &fstest.MapFile{Data: []byte(data)}
	}
	for _, name := range removals {
		delete(m, name)
	}
	return m
}

func collect(t *testing.T, fsys fs.FS) (*procfs, Raw) {
	t.Helper()
	p := newProcfs(fsys)
	raw, err := p.Collect(context.Background())
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	return p, raw
}

func TestProcfsHost(t *testing.T) {
	t.Run("a captured Ubuntu VM gives its static fields, uptime, load, and boot time", func(t *testing.T) {
		p, raw := collect(t, os.DirFS("testdata/ubuntu-vm"))
		// No /proc/cpuinfo was captured from this VM, so the model is unknown.
		want := HostInfo{Hostname: "vm", OS: "linux", OSVersion: "Ubuntu 24.04.5 LTS", Kernel: "6.18.44-fc-v153", Arch: runtime.GOARCH, CPUModel: "unknown"}
		if got := p.Static(); got != want {
			t.Errorf("Static() = %+v\nwant      %+v", got, want)
		}
		if raw.Errors[GroupHost] != nil {
			t.Fatalf("host error: %v", raw.Errors[GroupHost])
		}
		h := raw.Host
		if h.UptimeSeconds != 268 || h.Load != [3]float64{0.08, 0.40, 0.22} || !h.BootTime.Equal(time.Unix(1792859366, 0)) {
			t.Errorf("host = %+v", h)
		}
	})

	t.Run("fractional uptime seconds are truncated, not rounded", func(t *testing.T) {
		for in, want := range map[string]uint64{"0.99 1.00\n": 0, "86400.50 1.00\n": 86400, "42 1\n": 42} {
			_, raw := collect(t, with(map[string]string{"proc/uptime": in}))
			if raw.Host == nil || raw.Host.UptimeSeconds != want {
				t.Errorf("uptime from %q = %+v, want %d", in, raw.Host, want)
			}
		}
	})

	t.Run("a missing /etc/os-release falls back to /usr/lib/os-release", func(t *testing.T) {
		p, _ := collect(t, with(map[string]string{"usr/lib/os-release": "PRETTY_NAME=\"Fedora Linux 43\"\n"}, "etc/os-release"))
		if got := p.Static().OSVersion; got != "Fedora Linux 43" {
			t.Errorf("os_version = %q", got)
		}
	})

	t.Run("no os-release at all leaves os_version empty and is not an error", func(t *testing.T) {
		p, raw := collect(t, with(nil, "etc/os-release"))
		if got := p.Static().OSVersion; got != "" {
			t.Errorf("os_version = %q, want empty", got)
		}
		if raw.Errors[GroupHost] != nil || raw.Host == nil {
			t.Errorf("host error %v, host %+v", raw.Errors[GroupHost], raw.Host)
		}
	})

	t.Run("PRETTY_NAME may be double-quoted with escapes, single-quoted, or bare", func(t *testing.T) {
		for in, want := range map[string]string{
			`PRETTY_NAME="Alpine \"edge\""`:  `Alpine "edge"`,
			`PRETTY_NAME='Arch Linux'`:       "Arch Linux",
			"PRETTY_NAME=NixOS":              "NixOS",
			"ID=x\n  PRETTY_NAME=\"Void\"  ": "Void",
		} {
			p, _ := collect(t, with(map[string]string{"etc/os-release": in + "\n"}))
			if got := p.Static().OSVersion; got != want {
				t.Errorf("os_version from %q = %q, want %q", in, got, want)
			}
		}
	})

	t.Run("an unreadable kernel or hostname file leaves that field empty", func(t *testing.T) {
		p, _ := collect(t, with(nil, "proc/sys/kernel/osrelease", "proc/sys/kernel/hostname"))
		if got := p.Static(); got.Kernel != "" || got.Hostname != "" || got.OS != "linux" {
			t.Errorf("Static() = %+v", got)
		}
	})

	failures := []struct {
		name, want string
		fsys       fs.FS
	}{
		{"a missing /proc/uptime fails the host group and names the file", "reading /proc/uptime",
			with(nil, "proc/uptime")},
		{"a non-numeric uptime fails the host group", "parsing /proc/uptime",
			with(map[string]string{"proc/uptime": "soon idle\n"})},
		{"an empty /proc/uptime fails the host group", "parsing /proc/uptime",
			with(map[string]string{"proc/uptime": ""})},
		{"a negative uptime fails the host group", "parsing /proc/uptime",
			with(map[string]string{"proc/uptime": "-5.00 1.00\n"})},
		{"a short /proc/loadavg fails the host group", "parsing /proc/loadavg",
			with(map[string]string{"proc/loadavg": "0.1 0.2\n"})},
		{"a garbled load average fails the host group", "parsing /proc/loadavg",
			with(map[string]string{"proc/loadavg": "0.1 x 0.3 1/2 3\n"})},
		{"a /proc/stat without btime fails the host group", "no btime",
			with(map[string]string{"proc/stat": "cpu  1 2 3 4\n"})},
		{"a garbled btime fails the host group", "btime",
			with(map[string]string{"proc/stat": "btime yesterday\n"})},
	}
	for _, tc := range failures {
		t.Run(tc.name, func(t *testing.T) {
			_, raw := collect(t, tc.fsys)
			err := raw.Errors[GroupHost]
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("host error = %v, want one containing %q", err, tc.want)
			}
			if raw.Host != nil {
				t.Errorf("host = %+v, want nil for a failed group", raw.Host)
			}
		})
	}

	t.Run("groups that haven't landed say so in errors, and nothing else does", func(t *testing.T) {
		_, raw := collect(t, minimal())
		got := map[string]string{}
		for g, err := range raw.Errors {
			got[g] = err.Error()
		}
		want := map[string]string{}
		for _, g := range []string{GroupDisks, GroupNetwork, GroupTemps} {
			want[g] = "not implemented yet"
		}
		if !maps.Equal(got, want) {
			t.Errorf("errors = %v\nwant     %v", got, want)
		}
	})
}

func TestProcfsCPU(t *testing.T) {
	cpuStat := func(lines ...string) fs.FS {
		return with(map[string]string{"proc/stat": strings.Join(lines, "\n") + "\nbtime 1700000000\n"})
	}

	t.Run("an 8-core x86 machine gives the total and eight cores, iowait counted as idle", func(t *testing.T) {
		_, raw := collect(t, os.DirFS("testdata/x86-8core"))
		if err := raw.Errors[GroupCPU]; err != nil || raw.CPU == nil {
			t.Fatalf("cpu error %v", err)
		}
		// cpu  1692629 3933 433525 40163676 40209 0 25855 0 0 0
		if want := (CPUTicks{Busy: 1692629 + 3933 + 433525 + 25855, Idle: 40163676 + 40209}); raw.CPU.Total != want {
			t.Errorf("total = %+v, want %+v", raw.CPU.Total, want)
		}
		if len(raw.CPU.PerCore) != 8 {
			t.Fatalf("got %d cores, want 8", len(raw.CPU.PerCore))
		}
		// cpu0 194865 196 40860 5080411 8957 0 3761 0 0 0
		if want := (CPUTicks{Busy: 194865 + 196 + 40860 + 3761, Idle: 5080411 + 8957}); raw.CPU.PerCore[0] != want {
			t.Errorf("cpu0 = %+v, want %+v", raw.CPU.PerCore[0], want)
		}
		if raw.CPU.Darwin != nil {
			t.Errorf("darwin = %+v on Linux, want nil", raw.CPU.Darwin)
		}
	})

	t.Run("the captured Ubuntu VM has four cores", func(t *testing.T) {
		_, raw := collect(t, os.DirFS("testdata/ubuntu-vm"))
		if raw.CPU == nil || len(raw.CPU.PerCore) != 4 {
			t.Fatalf("cpu = %+v, error %v; want four cores", raw.CPU, raw.Errors[GroupCPU])
		}
	})

	t.Run("steal counts as busy, and guest time, already inside user, is not counted twice", func(t *testing.T) {
		//                user nice system idle iowait irq softirq steal guest guest_nice
		_, raw := collect(t, cpuStat("cpu  100 10 20 500 5 1 2 30 80 4", "cpu0 100 10 20 500 5 1 2 30 80 4"))
		if want := (CPUTicks{Busy: 100 + 10 + 20 + 1 + 2 + 30, Idle: 505}); raw.CPU == nil || raw.CPU.Total != want {
			t.Errorf("total = %+v, want %+v", raw.CPU, want)
		}
	})

	t.Run("a kernel that prints only four columns still parses", func(t *testing.T) {
		_, raw := collect(t, cpuStat("cpu  1 2 3 4", "cpu0 1 2 3 4"))
		if want := (CPUTicks{Busy: 6, Idle: 4}); raw.CPU == nil || raw.CPU.PerCore[0] != want {
			t.Errorf("cpu0 = %+v, want %+v", raw.CPU, want)
		}
	})

	t.Run("a single-core machine has one core", func(t *testing.T) {
		_, raw := collect(t, cpuStat("cpu  7 0 3 90 0 0 0 0 0 0", "cpu0 7 0 3 90 0 0 0 0 0 0"))
		if raw.CPU == nil || len(raw.CPU.PerCore) != 1 || raw.CPU.PerCore[0] != (CPUTicks{Busy: 10, Idle: 90}) {
			t.Errorf("cpu = %+v, want one core {10 90}", raw.CPU)
		}
	})

	t.Run("offline CPUs are missing from /proc/stat, and the online ones are listed in order", func(t *testing.T) {
		_, raw := collect(t, cpuStat("cpu  3 0 0 30 0 0 0 0 0 0", "cpu0 1 0 0 10 0 0 0 0 0 0", "cpu1 1 0 0 10 0 0 0 0 0 0", "cpu3 1 0 0 10 0 0 0 0 0 0"))
		if raw.CPU == nil || len(raw.CPU.PerCore) != 3 {
			t.Errorf("cpu = %+v, error %v; want the three online cores", raw.CPU, raw.Errors[GroupCPU])
		}
	})

	for _, tc := range []struct {
		name, want string
		fsys       fs.FS
	}{
		{"a malformed cpu line fails the cpu group and names the line", "cpu1", cpuStat("cpu  1 2 3 4", "cpu0 1 2 3 4", "cpu1 1 2 x 4")},
		{"a cpu line with too few columns is an error", "cpu0", cpuStat("cpu  1 2 3 4", "cpu0 1 2 3")},
		{"no total cpu line is an error", "no cpu line", cpuStat("cpu0 1 2 3 4")},
		{"no per-CPU lines is an error", "no per-CPU", cpuStat("cpu  1 2 3 4")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, raw := collect(t, tc.fsys)
			err := raw.Errors[GroupCPU]
			if err == nil || !strings.Contains(err.Error(), tc.want) || !strings.Contains(err.Error(), "/proc/stat") {
				t.Errorf("err = %v, want one naming /proc/stat and %q", err, tc.want)
			}
			if raw.CPU != nil {
				t.Errorf("cpu = %+v, want nil for a failed group", raw.CPU)
			}
			if raw.Host == nil {
				t.Errorf("host group failed too: %v", raw.Errors[GroupHost])
			}
		})
	}

	t.Run("an unreadable /proc/stat fails both groups that read it", func(t *testing.T) {
		_, raw := collect(t, with(nil, "proc/stat"))
		if raw.Errors[GroupCPU] == nil || raw.Errors[GroupHost] == nil {
			t.Errorf("errors = %v, want host and cpu", raw.Errors)
		}
	})
}

func TestProcfsMemory(t *testing.T) {
	meminfo := func(s string) fs.FS { return with(map[string]string{"proc/meminfo": s}) }

	t.Run("a machine with swap gives used as total minus available, and swap used as total minus free", func(t *testing.T) {
		_, raw := collect(t, os.DirFS("testdata/x86-8core"))
		if err := raw.Errors[GroupMemory]; err != nil || raw.Memory == nil {
			t.Fatalf("memory error %v", err)
		}
		// MemTotal 32765012, MemAvailable 20154772, SwapTotal 8388604, SwapFree 7340028 (kB).
		want := Memory{
			TotalBytes:     32765012 * 1024,
			AvailableBytes: 20154772 * 1024,
			UsedBytes:      (32765012 - 20154772) * 1024,
			SwapTotalBytes: 8388604 * 1024,
			SwapUsedBytes:  (8388604 - 7340028) * 1024,
		}
		if *raw.Memory != want {
			t.Errorf("memory = %+v\nwant     %+v", *raw.Memory, want)
		}
	})

	t.Run("the captured Ubuntu VM has no swap, which is zeros and not an error", func(t *testing.T) {
		_, raw := collect(t, os.DirFS("testdata/ubuntu-vm"))
		if err := raw.Errors[GroupMemory]; err != nil || raw.Memory == nil {
			t.Fatalf("memory error %v", err)
		}
		m := raw.Memory
		if m.TotalBytes != 16477348*1024 || m.UsedBytes != (16477348-15956948)*1024 || m.SwapTotalBytes != 0 || m.SwapUsedBytes != 0 {
			t.Errorf("memory = %+v", *m)
		}
	})

	t.Run("there is no darwin object on Linux", func(t *testing.T) {
		_, raw := collect(t, minimal())
		if raw.Memory == nil || raw.Memory.Darwin != nil {
			t.Errorf("memory = %+v, want one without darwin", raw.Memory)
		}
	})

	t.Run("used memory fits in 64 bits past 4 TiB", func(t *testing.T) {
		// 6 TiB, in kB, is more than 2³² kB.
		_, raw := collect(t, meminfo("MemTotal: 6442450944 kB\nMemAvailable: 1073741824 kB\nSwapTotal: 0 kB\nSwapFree: 0 kB\n"))
		if raw.Memory == nil || raw.Memory.UsedBytes != (6442450944-1073741824)*1024 {
			t.Errorf("memory = %+v, error %v", raw.Memory, raw.Errors[GroupMemory])
		}
	})

	t.Run("an old kernel without MemAvailable fails the memory group, says why, and the others are served", func(t *testing.T) {
		// Linux 3.10, as on CentOS 7 before the backport.
		_, raw := collect(t, meminfo("MemTotal:        1015024 kB\nMemFree:          113020 kB\nBuffers:           48404 kB\nCached:           524068 kB\nSwapCached:            0 kB\nSwapTotal:       2097148 kB\nSwapFree:        2097148 kB\n"))
		err := raw.Errors[GroupMemory]
		if err == nil || !strings.Contains(err.Error(), "MemAvailable") || !strings.Contains(err.Error(), "3.14") {
			t.Errorf("memory error = %v, want one naming MemAvailable and Linux 3.14", err)
		}
		if raw.Memory != nil {
			t.Errorf("memory = %+v, want nil rather than a guess", raw.Memory)
		}
		if raw.Host == nil || raw.CPU == nil {
			t.Errorf("host %v, cpu %v: the other groups should be served", raw.Errors[GroupHost], raw.Errors[GroupCPU])
		}
	})

	for _, tc := range []struct {
		name, want string
		fsys       fs.FS
	}{
		{"a missing /proc/meminfo fails the memory group and names the file", "reading /proc/meminfo", with(nil, "proc/meminfo")},
		{"a missing MemTotal is an error", "no MemTotal", meminfo("MemAvailable: 1 kB\nSwapTotal: 0 kB\nSwapFree: 0 kB\n")},
		{"missing swap lines are an error, not zero swap", "no SwapTotal", meminfo("MemTotal: 2 kB\nMemAvailable: 1 kB\n")},
		{"a garbled value is an error naming the line", "MemTotal", meminfo("MemTotal: lots kB\nMemAvailable: 1 kB\nSwapTotal: 0 kB\nSwapFree: 0 kB\n")},
		{"an empty value is an error", "MemAvailable has no value", meminfo("MemTotal: 2 kB\nMemAvailable:\nSwapTotal: 0 kB\nSwapFree: 0 kB\n")},
		{"available above total is an error, not a wrapped used", "more than MemTotal", meminfo("MemTotal: 1 kB\nMemAvailable: 2 kB\nSwapTotal: 0 kB\nSwapFree: 0 kB\n")},
		{"swap free above swap total is an error, not a wrapped swap used", "more than SwapTotal", meminfo("MemTotal: 2 kB\nMemAvailable: 1 kB\nSwapTotal: 0 kB\nSwapFree: 4 kB\n")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, raw := collect(t, tc.fsys)
			err := raw.Errors[GroupMemory]
			if err == nil || !strings.Contains(err.Error(), tc.want) || !strings.Contains(err.Error(), "/proc/meminfo") {
				t.Errorf("err = %v, want one naming /proc/meminfo and %q", err, tc.want)
			}
			if raw.Memory != nil {
				t.Errorf("memory = %+v, want nil for a failed group", raw.Memory)
			}
		})
	}
}

func TestProcfsCPUModel(t *testing.T) {
	for _, tc := range []struct {
		name, want string
		fsys       fs.FS
	}{
		{"x86 names the model from model name", "Intel(R) Core(TM) i7-9700 CPU @ 3.00GHz", os.DirFS("testdata/x86-8core")},
		{"arm64 has no model name, so the implementer and part IDs stand in", "implementer 0x41, part 0xd0c", os.DirFS("testdata/arm64-graviton2")},
		{"runs of spaces in model name are collapsed", "Intel(R) Xeon(R) CPU E5-2670 0 @ 2.60GHz",
			with(map[string]string{"proc/cpuinfo": "processor\t: 0\nmodel name\t: Intel(R) Xeon(R) CPU           E5-2670 0 @ 2.60GHz  \n"})},
		{"neither model name nor implementer gives unknown", "unknown", with(map[string]string{"proc/cpuinfo": "processor\t: 0\nBogoMIPS\t: 50.00\n"})},
		{"an unreadable /proc/cpuinfo gives unknown, not an error", "unknown", minimal()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p, raw := collect(t, tc.fsys)
			if got := p.Static().CPUModel; got != tc.want {
				t.Errorf("model = %q, want %q", got, tc.want)
			}
			if tc.want == "unknown" && raw.Errors[GroupCPU] != nil {
				t.Errorf("cpu error %v, want none for an unknown model", raw.Errors[GroupCPU])
			}
		})
	}
}
