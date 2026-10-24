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
		"proc/stat":                 {Data: []byte("cpu  1 2 3 4 5 6 7 8 0 0\nintr 0\nbtime 1700000000\nprocesses 1\n")},
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
		want := HostInfo{Hostname: "vm", OS: "linux", OSVersion: "Ubuntu 24.04.5 LTS", Kernel: "6.18.44-fc-v153", Arch: runtime.GOARCH}
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
		for _, g := range []string{GroupCPU, GroupMemory, GroupDisks, GroupNetwork, GroupTemps} {
			want[g] = "not implemented yet"
		}
		if !maps.Equal(got, want) {
			t.Errorf("errors = %v\nwant     %v", got, want)
		}
	})
}
