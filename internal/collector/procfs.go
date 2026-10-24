package collector

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// procfs reads Linux's /proc and /etc through an fs.FS rooted at "/", so tests
// run it against fixture trees on any OS (decision 6). Paths have no leading
// slash, as fs.FS requires.
type procfs struct {
	fsys fs.FS
	info HostInfo
}

func newProcfs(fsys fs.FS) *procfs {
	p := &procfs{fsys: fsys}
	p.info = HostInfo{
		Hostname:  p.line("proc/sys/kernel/hostname"),
		OS:        "linux",
		OSVersion: p.osVersion(),
		Kernel:    p.line("proc/sys/kernel/osrelease"),
		Arch:      runtime.GOARCH,
	}
	return p
}

func (p *procfs) Static() HostInfo { return p.info }

func (p *procfs) Collect(ctx context.Context) (Raw, error) {
	raw := Raw{Errors: pending(GroupCPU, GroupMemory, GroupDisks, GroupNetwork, GroupTemps)}
	if h, err := p.host(); err != nil {
		raw.Errors[GroupHost] = err
	} else {
		raw.Host = &h
	}
	return raw, nil
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

func (p *procfs) host() (Host, error) {
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
	s, err := p.read("proc/stat")
	if err != nil {
		return h, err
	}
	for l := range strings.Lines(s) {
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
