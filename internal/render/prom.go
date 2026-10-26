package render

import (
	"bytes"
	"strconv"
	"strings"

	"github.com/nmcginn/vitals/internal/collector"
	"github.com/nmcginn/vitals/internal/sampler"
)

// Build names the binary for vitals_build_info. main sets it from -ldflags.
type Build struct {
	Version, Commit string
}

// groups are every group vitals_collector_error reports, in output order, so
// a group that is fine reads 0 rather than vanishing.
var groups = []string{
	collector.GroupHost, collector.GroupCPU, collector.GroupMemory,
	collector.GroupDisks, collector.GroupNetwork, collector.GroupTemps,
}

// pressures are darwin's memory pressure levels. Exactly one reads 1.
var pressures = []string{"normal", "warn", "critical"}

// Prom renders a snapshot in Prometheus exposition format 0.0.4 (PLAN's
// names, decision 35). Prometheus has no null, so an unknown value is left
// out, and a family with no samples is left out entirely, HELP and TYPE too.
// Ratios run 0 to 1; everything else is in base units.
func Prom(s *sampler.Snapshot, b Build) []byte {
	var w promWriter
	w.family("vitals_build_info", "gauge", "Always 1; the labels name the running build.").
		add("1", "version", b.Version, "commit", b.Commit)
	h := s.Host
	w.family("vitals_host_info", "gauge", "Always 1; the labels describe the host.").
		add("1", "hostname", h.Hostname, "os", h.OS, "os_version", h.OSVersion, "kernel", h.Kernel, "arch", h.Arch)
	if h.UptimeSeconds != nil {
		w.family("vitals_uptime_seconds", "gauge", "Seconds since boot.").add(uintStr(*h.UptimeSeconds))
	}
	if h.BootTime != nil {
		w.family("vitals_boot_time_seconds", "gauge", "Boot time as Unix seconds.").add(strconv.FormatInt(h.BootTime.Unix(), 10))
	}
	if h.Load != nil {
		f := w.family("vitals_load_average", "gauge", "Load average over the period.")
		for i, period := range []string{"1m", "5m", "15m"} {
			f.add(floatStr(h.Load[i]), "period", period)
		}
	}

	if c := s.CPU; c != nil {
		f := w.family("vitals_cpu_usage_ratio", "gauge", `Busy share of CPU time since the last sample, 0 to 1; cpu="total" is every core together.`)
		f.ratio(c.UsagePct, "cpu", "total")
		for i, pct := range c.PerCorePct {
			f.ratio(pct, "cpu", strconv.Itoa(i))
		}
		if d := c.Darwin; d != nil {
			f := w.family("vitals_cpu_core_info", "gauge", "Always 1; kind is P for a performance core, E for an efficiency core.")
			for i, kind := range d.CoreKinds {
				f.add("1", "cpu", strconv.Itoa(i), "kind", kind)
			}
		}
	}

	if m := s.Memory; m != nil {
		w.family("vitals_memory_total_bytes", "gauge", "Physical memory.").add(uintStr(m.TotalBytes))
		w.family("vitals_memory_available_bytes", "gauge", "Memory available to start new work without swapping.").add(uintStr(m.AvailableBytes))
		w.family("vitals_memory_used_bytes", "gauge", "Memory in use; on darwin, Activity Monitor's Memory Used.").add(uintStr(m.UsedBytes))
		if d := m.Darwin; d != nil {
			w.family("vitals_memory_wired_bytes", "gauge", "Wired memory, which can't be paged out.").add(uintStr(d.WiredBytes))
			w.family("vitals_memory_compressed_bytes", "gauge", "Memory held by the compressor.").add(uintStr(d.CompressedBytes))
			w.family("vitals_memory_active_bytes", "gauge", "Active memory.").add(uintStr(d.ActiveBytes))
			w.family("vitals_memory_inactive_bytes", "gauge", "Inactive memory.").add(uintStr(d.InactiveBytes))
			w.family("vitals_memory_file_backed_bytes", "gauge", "File-backed (cached) memory.").add(uintStr(d.FileBackedBytes))
			// An unknown level is left out rather than reported as no level.
			if d.Pressure != nil {
				f := w.family("vitals_memory_pressure", "gauge", "1 for the current memory pressure level, 0 for the others.")
				for _, level := range pressures {
					f.add(boolStr(*d.Pressure == level), "level", level)
				}
			}
		}
		w.family("vitals_swap_total_bytes", "gauge", "Swap space.").add(uintStr(m.SwapTotalBytes))
		w.family("vitals_swap_used_bytes", "gauge", "Swap space in use.").add(uintStr(m.SwapUsedBytes))
	}

	size := w.family("vitals_filesystem_size_bytes", "gauge", "Filesystem size.")
	used := w.family("vitals_filesystem_used_bytes", "gauge", "Filesystem space in use.")
	free := w.family("vitals_filesystem_free_bytes", "gauge", "Filesystem space an unprivileged user can still write.")
	read := w.family("vitals_disk_read_bytes_per_second", "gauge", "Bytes read per second from the physical device.")
	written := w.family("vitals_disk_written_bytes_per_second", "gauge", "Bytes written per second to the physical device.")
	seen := map[string]bool{}
	for _, d := range s.Disks {
		fs := []string{"mount", d.Mount, "device", d.Device, "fs", d.FS}
		size.add(uintStr(d.TotalBytes), fs...)
		used.add(uintStr(d.UsedBytes), fs...)
		free.add(uintStr(d.FreeBytes), fs...)
		// Mounts on one device share its rates (decision 7), so each device
		// is written once and a sum across devices counts every byte once.
		if d.IODevice == "" || seen[d.IODevice] {
			continue
		}
		seen[d.IODevice] = true
		read.rate(d.ReadBps, "device", d.IODevice)
		written.rate(d.WriteBps, "device", d.IODevice)
	}

	rxB := w.family("vitals_network_receive_bytes_per_second", "gauge", "Bytes received per second.")
	txB := w.family("vitals_network_transmit_bytes_per_second", "gauge", "Bytes sent per second.")
	rxP := w.family("vitals_network_receive_packets_per_second", "gauge", "Packets received per second.")
	txP := w.family("vitals_network_transmit_packets_per_second", "gauge", "Packets sent per second.")
	rxT := w.family("vitals_network_receive_bytes_total", "counter", "Bytes received since the interface came up.")
	txT := w.family("vitals_network_transmit_bytes_total", "counter", "Bytes sent since the interface came up.")
	for _, n := range s.Network {
		rxB.rate(n.RxBps, "iface", n.Name)
		txB.rate(n.TxBps, "iface", n.Name)
		rxP.rate(n.RxPps, "iface", n.Name)
		txP.rate(n.TxPps, "iface", n.Name)
		rxT.add(uintStr(n.RxTotalBytes), "iface", n.Name)
		txT.add(uintStr(n.TxTotalBytes), "iface", n.Name)
	}

	temps := w.family("vitals_temperature_celsius", "gauge", "Sensor temperature in degrees Celsius.")
	for _, t := range s.Temps {
		temps.add(floatStr(t.Celsius), "sensor", t.Label)
	}

	errs := w.family("vitals_collector_error", "gauge", "1 when the group failed on this sample, 0 otherwise.")
	for _, g := range groups {
		_, failed := s.Errors[g]
		errs.add(boolStr(failed), "group", g)
	}
	return w.bytes()
}

// promWriter collects families in the order they are declared and writes the
// ones that have samples.
type promWriter struct {
	families []*family
}

type family struct {
	name, typ, help string
	samples         []string // each a whole line, without the newline
}

func (w *promWriter) family(name, typ, help string) *family {
	f := &family{name: name, typ: typ, help: help}
	w.families = append(w.families, f)
	return f
}

// add appends a sample. labels are name, value pairs.
func (f *family) add(value string, labels ...string) {
	var b strings.Builder
	b.WriteString(f.name)
	for i := 0; i < len(labels); i += 2 {
		if i == 0 {
			b.WriteByte('{')
		} else {
			b.WriteByte(',')
		}
		b.WriteString(labels[i])
		b.WriteString(`="`)
		labelEscaper.WriteString(&b, labels[i+1])
		b.WriteByte('"')
	}
	if len(labels) > 0 {
		b.WriteByte('}')
	}
	b.WriteByte(' ')
	b.WriteString(value)
	f.samples = append(f.samples, b.String())
}

// rate adds a sample only when the rate is known.
func (f *family) rate(v *float64, labels ...string) {
	if v != nil {
		f.add(floatStr(*v), labels...)
	}
}

// ratio adds a percentage as a 0 to 1 ratio, only when it is known. Fifteen
// significant digits, so dividing by 100 writes 2.8% as 0.028 rather than its
// float error, 0.027999999999999997.
func (f *family) ratio(pct *float64, labels ...string) {
	if pct != nil {
		f.add(strconv.FormatFloat(*pct/100, 'g', 15, 64), labels...)
	}
}

func (w *promWriter) bytes() []byte {
	var b bytes.Buffer
	for _, f := range w.families {
		if len(f.samples) == 0 {
			continue
		}
		b.WriteString("# HELP " + f.name + " " + f.help + "\n")
		b.WriteString("# TYPE " + f.name + " " + f.typ + "\n")
		for _, s := range f.samples {
			b.WriteString(s)
			b.WriteByte('\n')
		}
	}
	return b.Bytes()
}

// labelEscaper applies the exposition format's three label value escapes.
var labelEscaper = strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`)

// uintStr writes counters and byte gauges as integers: exact past 2^53, and
// easier to read than 4.000787030016e+12.
func uintStr(n uint64) string { return strconv.FormatUint(n, 10) }

func floatStr(v float64) string { return strconv.FormatFloat(v, 'g', -1, 64) }

func boolStr(v bool) string {
	if v {
		return "1"
	}
	return "0"
}
