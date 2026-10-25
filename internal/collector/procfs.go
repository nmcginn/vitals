package collector

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"time"
)

// procfs reads Linux's /proc and /etc through an fs.FS rooted at "/", so tests
// run it against fixture trees on any OS (decision 6). Paths have no leading
// slash, as fs.FS requires.
type procfs struct {
	fsys   fs.FS
	statfs statfsFunc
	opts   Options
	info   HostInfo
}

// statfsFunc gives a mounted filesystem's size and the bytes an unprivileged
// user can still write to it. It's injected so disk tests never touch the real
// filesystem (decision 6).
type statfsFunc func(mount string) (total, avail uint64, err error)

func newProcfs(fsys fs.FS, statfs statfsFunc, opts Options) *procfs {
	p := &procfs{fsys: fsys, statfs: statfs, opts: opts}
	p.info = HostInfo{
		Hostname:  p.line("proc/sys/kernel/hostname"),
		OS:        "linux",
		OSVersion: p.osVersion(),
		Kernel:    p.line("proc/sys/kernel/osrelease"),
		Arch:      runtime.GOARCH,
		CPUModel:  p.cpuModel(),
	}
	return p
}

func (p *procfs) Static() HostInfo { return p.info }

func (p *procfs) Collect(ctx context.Context) (Raw, error) {
	raw := Raw{Errors: pending(GroupTemps)}
	// The host and CPU groups both read /proc/stat, which on a big machine is
	// mostly one long intr line, so it's read once.
	if stat, err := p.read("proc/stat"); err != nil {
		raw.Errors[GroupHost], raw.Errors[GroupCPU] = err, err
	} else {
		if h, err := p.host(stat); err != nil {
			raw.Errors[GroupHost] = err
		} else {
			raw.Host = &h
		}
		if c, err := cpuFromStat(stat); err != nil {
			raw.Errors[GroupCPU] = err
		} else {
			raw.CPU = &c
		}
	}
	if m, err := p.memory(); err != nil {
		raw.Errors[GroupMemory] = err
	} else {
		raw.Memory = &m
	}
	if n, err := p.network(); err != nil {
		raw.Errors[GroupNetwork] = err
	} else {
		raw.Network = keepIfaces(n, p.opts.NetExclude)
	}
	// A mount that statfs can't read is left out and named in the error, and
	// the others are still served.
	d, err := p.disks()
	if raw.Disks = d; err != nil {
		raw.Errors[GroupDisks] = err
	}
	return raw, nil
}

// disks reads /proc/self/mounts, "device mount fstype options 0 0", keeps
// what the exclude list allows, and asks statfs for each one's size.
func (p *procfs) disks() ([]Disk, error) {
	s, err := p.read("proc/self/mounts")
	if err != nil {
		return nil, err
	}
	var all []Disk
	n := 0
	for l := range strings.Lines(s) {
		n++
		f := strings.Fields(l)
		if len(f) < 3 {
			return nil, fmt.Errorf("parsing /proc/self/mounts: line %d is %q, want a device, a mount point, and a type", n, strings.TrimSpace(l))
		}
		all = append(all, Disk{Device: unescapeMount(f[0]), Mount: unescapeMount(f[1]), FS: f[2]})
	}
	ds := keepDisks(all, p.opts.DiskExclude)
	io, ioErr := p.diskIO()
	var bad []string
	kept := ds[:0]
	for _, d := range ds {
		if d.TotalBytes, d.FreeBytes, err = p.statfs(d.Mount); err != nil {
			bad = append(bad, fmt.Sprintf("statfs %s: %v", d.Mount, err))
			continue
		}
		d.IO = io(d.Device)
		kept = append(kept, d)
	}
	if ioErr != nil {
		bad = append(bad, ioErr.Error())
	}
	if len(bad) > 0 {
		return kept, errors.New(strings.Join(bad, "; "))
	}
	return kept, nil
}

// unescapeMount undoes the octal escapes the kernel writes in mount paths:
// \040 for a space, \011 a tab, \012 a newline, and \134 a backslash.
func unescapeMount(s string) string {
	if !strings.Contains(s, `\`) {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+4 <= len(s) {
			if c, err := strconv.ParseUint(s[i+1:i+4], 8, 8); err == nil {
				b.WriteByte(byte(c))
				i += 3
				continue
			}
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

// diskIO returns a lookup from a mount's device to the cumulative counters of
// the whole disk behind it (decision 7): /dev/nvme0n1p2 counts as nvme0n1. A
// partition is a directory with a "partition" file under its disk in
// /sys/block. A device-mapper volume (/dev/mapper/vg0-data) is found by its
// dm/name and counted as itself, since it may span several disks. Anything
// else, such as a network share or /dev/root, has no counters, which the
// sampler shows as null rates. /proc/diskstats counts 512-byte sectors on
// every device, whatever its real sector size.
func (p *procfs) diskIO() (func(device string) *DiskIO, error) {
	none := func(string) *DiskIO { return nil }
	s, err := p.read("proc/diskstats")
	if err != nil {
		return none, err
	}
	// "major minor name reads merged sectors ms writes merged sectors ..."
	stats := map[string][2]uint64{}
	for l := range strings.Lines(s) {
		f := strings.Fields(l)
		if len(f) < 10 {
			return none, fmt.Errorf("parsing /proc/diskstats: line %q has %d fields, want at least 10", strings.TrimSpace(l), len(f))
		}
		r, err1 := strconv.ParseUint(f[5], 10, 64)
		w, err2 := strconv.ParseUint(f[9], 10, 64)
		if err1 != nil || err2 != nil {
			return none, fmt.Errorf("parsing /proc/diskstats: %s has sector counts %q and %q", f[2], f[5], f[9])
		}
		stats[f[2]] = [2]uint64{r * 512, w * 512}
	}
	disks, err := fs.ReadDir(p.fsys, "sys/block")
	if err != nil {
		return none, fmt.Errorf("reading /sys/block: %w", err)
	}
	whole, mapper := map[string]string{}, map[string]string{}
	for _, d := range disks {
		disk := d.Name()
		whole[disk] = disk
		if name := p.line("sys/block/" + disk + "/dm/name"); name != "" {
			mapper[name] = disk
		}
		parts, _ := fs.ReadDir(p.fsys, "sys/block/"+disk)
		for _, part := range parts {
			if !strings.HasPrefix(part.Name(), disk) {
				continue
			}
			if _, err := fs.Stat(p.fsys, "sys/block/"+disk+"/"+part.Name()+"/partition"); err == nil {
				whole[part.Name()] = disk
			}
		}
	}
	return func(device string) *DiskIO {
		name, ok := strings.CutPrefix(device, "/dev/")
		if !ok {
			return nil
		}
		if m, ok := strings.CutPrefix(name, "mapper/"); ok {
			name = mapper[m]
		}
		disk, ok := whole[name]
		st, ok2 := stats[disk]
		if !ok || !ok2 {
			return nil
		}
		return &DiskIO{Device: disk, ReadBytes: st[0], WriteBytes: st[1]}
	}, nil
}

// network reads /proc/net/dev: two header lines, then one line per interface,
// "name: rx bytes, packets, errs, drop, fifo, frame, compressed, multicast,
// then tx bytes, packets, errs, drop, fifo, colls, carrier, compressed". The
// counters are 64-bit on every kernel this runs on. A big counter can butt up
// against the colon, so the name is cut there; Linux forbids ':' in names.
func (p *procfs) network() ([]Interface, error) {
	s, err := p.read("proc/net/dev")
	if err != nil {
		return nil, err
	}
	var ifs []Interface
	n := 0
	for l := range strings.Lines(s) {
		if n++; n <= 2 {
			continue
		}
		name, rest, ok := strings.Cut(l, ":")
		name = strings.TrimSpace(name)
		f := strings.Fields(rest)
		if !ok || name == "" || len(f) < 16 {
			return nil, fmt.Errorf("parsing /proc/net/dev: line %d is %q, want a name, a colon, and 16 counters", n, strings.TrimSpace(l))
		}
		var v [4]uint64
		for i, col := range []int{0, 1, 8, 9} {
			if v[i], err = strconv.ParseUint(f[col], 10, 64); err != nil {
				return nil, fmt.Errorf("parsing /proc/net/dev: %s column %d is %q, not a counter", name, col+1, f[col])
			}
		}
		ifs = append(ifs, Interface{Name: name, RxBytes: v[0], RxPackets: v[1], TxBytes: v[2], TxPackets: v[3]})
	}
	return ifs, nil
}

// memory reads /proc/meminfo, whose values are in KiB despite the "kB". Used is
// total − MemAvailable, the kernel's own estimate of what can be had without
// swapping. Kernels before 3.14 lack it, and adding up free, buffers, and cache
// instead would be a guess the kernel itself warns against, so it's an error.
func (p *procfs) memory() (Memory, error) {
	s, err := p.read("proc/meminfo")
	if err != nil {
		return Memory{}, err
	}
	keys := []string{"MemTotal", "MemAvailable", "SwapTotal", "SwapFree"}
	got := make(map[string]uint64, len(keys))
	for l := range strings.Lines(s) {
		k, v, _ := strings.Cut(l, ":")
		if !slices.Contains(keys, k) {
			continue
		}
		f := strings.Fields(v)
		if len(f) == 0 {
			return Memory{}, fmt.Errorf("parsing /proc/meminfo: %s has no value", k)
		}
		n, err := strconv.ParseUint(f[0], 10, 64)
		if err != nil {
			return Memory{}, fmt.Errorf("parsing /proc/meminfo: %s is %q, not a size in kB", k, strings.TrimSpace(v))
		}
		got[k] = n * 1024
	}
	for _, k := range keys {
		if _, ok := got[k]; !ok {
			if k == "MemAvailable" {
				return Memory{}, errors.New("parsing /proc/meminfo: no MemAvailable line; it needs Linux 3.14 or later")
			}
			return Memory{}, fmt.Errorf("parsing /proc/meminfo: no %s line", k)
		}
	}
	m := Memory{TotalBytes: got["MemTotal"], AvailableBytes: got["MemAvailable"], SwapTotalBytes: got["SwapTotal"]}
	// Subtracting past zero would wrap to a huge uint64, so a part bigger than
	// its whole is an error, not a number.
	switch {
	case m.AvailableBytes > m.TotalBytes:
		return Memory{}, fmt.Errorf("parsing /proc/meminfo: MemAvailable %d kB is more than MemTotal %d kB", m.AvailableBytes/1024, m.TotalBytes/1024)
	case got["SwapFree"] > m.SwapTotalBytes:
		return Memory{}, fmt.Errorf("parsing /proc/meminfo: SwapFree %d kB is more than SwapTotal %d kB", got["SwapFree"]/1024, m.SwapTotalBytes/1024)
	}
	m.UsedBytes = m.TotalBytes - m.AvailableBytes
	m.SwapUsedBytes = m.SwapTotalBytes - got["SwapFree"]
	return m, nil
}

func (p *procfs) read(name string) (string, error) {
	b, err := fs.ReadFile(p.fsys, name)
	if err != nil {
		return "", fmt.Errorf("reading /%s: %w", name, err)
	}
	return string(b), nil
}

// line is a one-line file's contents, or "" when it can't be read: for static
// fields, which are omitted rather than failing the group.
func (p *procfs) line(name string) string {
	s, _ := p.read(name)
	return strings.TrimSpace(s)
}

// fields reads a file and splits it on spaces, failing if it has fewer than n.
func (p *procfs) fields(name string, n int) ([]string, error) {
	s, err := p.read(name)
	if err != nil {
		return nil, err
	}
	f := strings.Fields(s)
	if len(f) < n {
		return nil, fmt.Errorf("parsing /%s: %q has %d fields, want %d", name, s, len(f), n)
	}
	return f, nil
}

func (p *procfs) host(stat string) (Host, error) {
	var h Host
	// "350735.47 234388.90": seconds since boot, then idle seconds.
	f, err := p.fields("proc/uptime", 1)
	if err != nil {
		return h, err
	}
	up, err := strconv.ParseFloat(f[0], 64)
	if err != nil || up < 0 {
		return h, fmt.Errorf("parsing /proc/uptime: %q is not a number of seconds", f[0])
	}
	h.UptimeSeconds = uint64(up)

	// "0.41 0.38 0.35 1/123 4567"
	if f, err = p.fields("proc/loadavg", 3); err != nil {
		return h, err
	}
	for i := range h.Load {
		if h.Load[i], err = strconv.ParseFloat(f[i], 64); err != nil {
			return h, fmt.Errorf("parsing /proc/loadavg: %q is not a load average", f[i])
		}
	}

	// The kernel's own boot time, rather than now − uptime, which would wobble
	// by a second between samples.
	for l := range strings.Lines(stat) {
		if v, ok := strings.CutPrefix(l, "btime "); ok {
			sec, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64)
			if err != nil {
				return h, fmt.Errorf("parsing /proc/stat: btime %q is not a Unix time", strings.TrimSpace(v))
			}
			h.BootTime = time.Unix(sec, 0)
			return h, nil
		}
	}
	return h, errors.New("parsing /proc/stat: no btime line")
}

// cpuFromStat reads the cpu lines of /proc/stat: the total, then one per online
// CPU (offline ones have no line). Each is "cpuN user nice system idle iowait
// irq softirq steal guest guest_nice" in ticks. Older kernels stop after idle.
// iowait is idle time, and steal is busy: the CPU was taken from this guest.
// guest and guest_nice are already inside user and nice, so they're skipped.
func cpuFromStat(stat string) (CPU, error) {
	var c CPU
	total := false
	for l := range strings.Lines(stat) {
		name, rest, _ := strings.Cut(l, " ")
		if !strings.HasPrefix(name, "cpu") {
			continue
		}
		f := strings.Fields(rest)
		if len(f) < 4 {
			return CPU{}, fmt.Errorf("parsing /proc/stat: %s has %d columns, want at least 4", name, len(f))
		}
		var v [8]uint64
		for i := 0; i < len(f) && i < len(v); i++ {
			n, err := strconv.ParseUint(f[i], 10, 64)
			if err != nil {
				return CPU{}, fmt.Errorf("parsing /proc/stat: %s column %d is %q, not a tick count", name, i+1, f[i])
			}
			v[i] = n
		}
		t := CPUTicks{Busy: v[0] + v[1] + v[2] + v[5] + v[6] + v[7], Idle: v[3] + v[4]}
		if name == "cpu" {
			c.Total, total = t, true
		} else {
			c.PerCore = append(c.PerCore, t)
		}
	}
	switch {
	case !total:
		return CPU{}, errors.New("parsing /proc/stat: no cpu line")
	case len(c.PerCore) == 0:
		return CPU{}, errors.New("parsing /proc/stat: no per-CPU lines")
	}
	return c, nil
}

// cpuModel names the CPU from /proc/cpuinfo's first "model name". arm64
// kernels print no name, only the implementer and part IDs, so those stand in.
func (p *procfs) cpuModel() string {
	s, err := p.read("proc/cpuinfo")
	if err != nil {
		return unknownModel
	}
	var impl, part string
	for l := range strings.Lines(s) {
		k, v, ok := strings.Cut(l, ":")
		if !ok {
			continue
		}
		// Some Xeons pad the name with runs of spaces.
		v = strings.Join(strings.Fields(v), " ")
		switch strings.TrimSpace(k) {
		case "model name":
			if v != "" {
				return v
			}
		case "CPU implementer":
			impl = cmp.Or(impl, v)
		case "CPU part":
			part = cmp.Or(part, v)
		}
	}
	if impl != "" && part != "" {
		return "implementer " + impl + ", part " + part
	}
	return unknownModel
}

// osVersion is PRETTY_NAME from os-release, which lives in /usr/lib when /etc
// doesn't have it. "" when neither exists.
func (p *procfs) osVersion() string {
	for _, name := range []string{"etc/os-release", "usr/lib/os-release"} {
		s, err := p.read(name)
		if err != nil {
			continue
		}
		for l := range strings.Lines(s) {
			v, ok := strings.CutPrefix(strings.TrimSpace(l), "PRETTY_NAME=")
			if !ok {
				continue
			}
			if u, err := strconv.Unquote(v); err == nil {
				return u
			}
			return strings.Trim(v, `'"`)
		}
		return ""
	}
	return ""
}
