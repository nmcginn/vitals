package render

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/nmcginn/vitals/internal/collector"
	"github.com/nmcginn/vitals/internal/sampler"
)

func TestTextGolden(t *testing.T) {
	for _, tc := range []struct {
		name, file string
		snap       *sampler.Snapshot
	}{
		{"a full darwin snapshot", "darwin_full.txt", darwinFull()},
		{"a first-sample snapshot with every rate unknown", "darwin_first.txt", darwinFirst()},
		{"a linux snapshot with temps, a failed group, and awkward disks", "linux_partial.txt", linuxPartial()},
		{"a darwin snapshot with failed groups", "darwin_errors.txt", darwinErrors()},
	} {
		t.Run(tc.name+" matches its golden file", func(t *testing.T) {
			golden(t, tc.file, Text(tc.snap))
		})
	}
}

// awkward is a snapshot built to be too wide: a 60-character mount point, a
// long hostname and OS name, a long interface name and sensor label, a
// many-core CPU, and a long error, with a control character in the hostname.
func awkward() *sampler.Snapshot {
	s := linuxPartial()
	s.Host.Hostname = "build-runner-07.rack-3.datacenter.example.internal\x1b[31m"
	s.Host.OSVersion = "Some Enterprise Linux Server release 9.6 (Long Codename Edition)"
	s.CPU.Model = strings.Repeat("Very Long Processor Model Name ", 4)
	s.CPU.PerCorePct = make([]*float64, 128)
	for i := range s.CPU.PerCorePct {
		s.CPU.PerCorePct[i] = p(100.0)
	}
	s.CPU.Cores = 128
	s.Disks[1].Mount = "/srv/media library/archive/" + strings.Repeat("x", 33) // 60 characters
	s.Disks[1].UsedBytes, s.Disks[1].TotalBytes = 1<<60, 1<<60
	s.Disks[1].ReadBps, s.Disks[1].WriteBps = p(1023.9*1024*1024*1024*1024), p(0.0)
	s.Network = []sampler.Interface{{Name: "enp0s31f6-with-a-long-alias", RxBps: p(1e15), RxTotalBytes: 1 << 63, TxTotalBytes: 5 << 32}}
	s.Temps = append(s.Temps, collector.Temp{Label: strings.Repeat("acpitz/temp", 10), Celsius: -273.15})
	s.Errors = map[string]string{
		"host":  "reading /proc/uptime: " + strings.Repeat("the quick brown fox ", 8),
		"disks": `statfs "/mnt/` + strings.Repeat("y", 90) + `": input/output error`,
		"temps": "listing /sys/class/hwmon: permission denied",
	}
	return s
}

func TestTextWidth(t *testing.T) {
	for name, s := range map[string]*sampler.Snapshot{
		"full":    darwinFull(),
		"first":   darwinFirst(),
		"linux":   linuxPartial(),
		"errors":  darwinErrors(),
		"awkward": awkward(),
	} {
		t.Run("no line of the "+name+" snapshot is wider than 80 columns", func(t *testing.T) {
			for _, line := range strings.Split(string(Text(s)), "\n") {
				if n := utf8.RuneCountInString(line); n > 80 {
					t.Errorf("%d columns: %q", n, line)
				}
			}
		})
	}

	out := string(Text(awkward()))
	t.Run("a 60-character mount point is cut short with …, not wrapped", func(t *testing.T) {
		if !strings.Contains(out, "  /srv/media library/arc… ") {
			t.Errorf("no truncated mount in:\n%s", out)
		}
		if strings.Contains(out, "xxxxxxxxxx") {
			t.Errorf("the mount's tail was printed:\n%s", out)
		}
	})
	t.Run("a long error wraps rather than being cut, so none of it is lost", func(t *testing.T) {
		if strings.Count(out, "fox") != 8 || !strings.Contains(out, "  quick brown fox the quick brown fox the quick brown fox\n") {
			t.Errorf("the host error lost words:\n%s", out)
		}
	})
	t.Run("control characters from the OS are not passed to the terminal", func(t *testing.T) {
		if strings.ContainsRune(out, '\x1b') {
			t.Errorf("an escape character reached the output:\n%q", out)
		}
	})
	t.Run("all 128 cores of a big machine are listed", func(t *testing.T) {
		if got := strings.Count(out, "100.0%"); got < 128 {
			t.Errorf("%d cores shown, want 128", got)
		}
	})
}

func TestTextContent(t *testing.T) {
	t.Run("the first sample shows — for every rate", func(t *testing.T) {
		out := string(Text(darwinFirst()))
		for _, want := range []string{"CPU — · ", "P0      —", "24.3%            —            —\n", "en0              —            —"} {
			if !strings.Contains(out, want) {
				t.Errorf("lacks %q:\n%s", want, out)
			}
		}
		if strings.Contains(out, "0 B/s") {
			t.Errorf("an unknown rate printed as zero:\n%s", out)
		}
	})

	t.Run("a failed group is named where its figures would be", func(t *testing.T) {
		out := string(Text(darwinErrors()))
		for _, want := range []string{
			"CPU unavailable: host_processor_info: (os/kern) failure\n",
			"Memory unavailable: host_statistics64: (os/kern) failure\n",
			"GPU unavailable: the IORegistry lists no IOAccelerator, so there is no GPU to\n  read\n",
			"Disks\n  statfs \"/Volumes/Backup\": input/output error\n  MOUNT",
		} {
			if !strings.Contains(out, want) {
				t.Errorf("lacks %q:\n%s", want, out)
			}
		}
	})

	t.Run("a group with figures and an error shows both, and isn't called unavailable", func(t *testing.T) {
		s := darwinFull()
		s.CPU.Darwin = nil
		s.Memory.Darwin.Pressure = nil
		s.Errors = map[string]string{
			"cpu":    "reading the P/E split: no cpus in IODeviceTree",
			"memory": "kern.memorystatus_vm_pressure_level: unknown level 3",
		}
		out := string(Text(s))
		for _, want := range []string{
			"CPU 6.2% · Apple M4 · 10 cores\n  reading the P/E split: no cpus in IODeviceTree\n  0   9.1%",
			"used (56.2%)\n  kern.memorystatus_vm_pressure_level: unknown level 3\n  available",
			"pressure unknown",
		} {
			if !strings.Contains(out, want) {
				t.Errorf("lacks %q:\n%s", want, out)
			}
		}
		if strings.Contains(out, "unavailable") {
			t.Errorf("a group with figures is called unavailable:\n%s", out)
		}
	})

	t.Run("filtering out every disk and interface says so", func(t *testing.T) {
		s := darwinFull()
		s.Disks, s.Network = []sampler.Disk{}, []sampler.Interface{}
		out := string(Text(s))
		for _, want := range []string{"Disks\n  No disks to show.\n", "Network\n  No interfaces to show.\n"} {
			if !strings.Contains(out, want) {
				t.Errorf("lacks %q:\n%s", want, out)
			}
		}
	})

	t.Run("darwin has a GPU line between memory and disks, and linux has none", func(t *testing.T) {
		if out := string(Text(darwinFull())); !strings.Contains(out, "\n\nGPU 23.0%\n\nDisks\n") {
			t.Errorf("lacks the GPU line:\n%s", out)
		}
		if out := string(Text(linuxPartial())); strings.Contains(out, "GPU") {
			t.Errorf("linux output mentions a GPU:\n%s", out)
		}
	})

	t.Run("an unknown GPU reading without an error is —, not 0", func(t *testing.T) {
		s := darwinFull()
		s.Darwin.GPU = nil
		if out := string(Text(s)); !strings.Contains(out, "\nGPU —\n") {
			t.Errorf("lacks GPU —:\n%s", out)
		}
	})

	t.Run("no sensors and no temps error means no temperatures section", func(t *testing.T) {
		if out := string(Text(darwinFull())); strings.Contains(out, "Temperatures") {
			t.Errorf("a temperatures section with nothing in it:\n%s", out)
		}
	})

	t.Run("a single core reads 1 core, and zero swap reads none", func(t *testing.T) {
		out := string(Text(linuxPartial()))
		for _, want := range []string{" · 1 core\n", "swap none"} {
			if !strings.Contains(out, want) {
				t.Errorf("lacks %q:\n%s", want, out)
			}
		}
	})

	t.Run("network totals past 2^32 bytes are shown in full", func(t *testing.T) {
		s := darwinFull()
		s.Network[0].RxTotalBytes = 5 << 32
		if out := string(Text(s)); !strings.Contains(out, "20.0 GiB") {
			t.Errorf("lacks 20.0 GiB:\n%s", out)
		}
	})

	t.Run("a hostname with HTML characters is printed as is", func(t *testing.T) {
		if out := string(Text(linuxPartial())); !strings.HasPrefix(out, `box<"&"> · `) {
			t.Errorf("hostname altered:\n%s", out)
		}
	})

	t.Run("no line ends in a space", func(t *testing.T) {
		for _, line := range strings.Split(string(Text(linuxPartial())), "\n") {
			if strings.HasSuffix(line, " ") {
				t.Errorf("trailing space: %q", line)
			}
		}
	})
}
