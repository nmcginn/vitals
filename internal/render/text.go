package render

import (
	"bytes"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/nmcginn/vitals/internal/sampler"
)

// textWidth is PLAN's terminal: no line of Text is longer, in runes.
const textWidth = 80

// Column widths in the disk and network tables, sized for the widest value
// each can hold ("1023.9 TiB", "1023.9 MiB/s", "100.0%"), so the figures line
// up from one sample to the next. Names get what's left, up to their longest.
const (
	bytesCol = 10
	rateCol  = 12
	pctCol   = 6
)

// Text renders a snapshot as aligned plain text for a terminal, at most 80
// columns wide (decision 37). Like the dashboard it humanizes with binary
// units, shows "—" for what can't be known, and names a failed group where its
// figures would be. Names that don't fit are cut short with "…", never
// wrapped, so the columns stay put; error messages wrap instead, because the
// end of one is often the part that says what to do.
func Text(s *sampler.Snapshot) []byte {
	var w textWriter
	w.host(s)
	w.cpu(s)
	w.memory(s)
	w.disks(s)
	w.network(s)
	w.temps(s)
	return w.Bytes()
}

// A group that failed outright is named in its heading. One that still
// returned figures alongside its error (the P/E split, an unknown pressure
// level, one unreadable mount) shows them, with the error beneath.

func (w *textWriter) host(s *sampler.Snapshot) {
	h := s.Host
	w.flow("", "  ", " · ", h.Hostname, system(h))
	uptime := "—"
	if h.UptimeSeconds != nil {
		uptime = duration(time.Duration(*h.UptimeSeconds) * time.Second)
	}
	load := "—"
	if l := h.Load; l != nil {
		load = strconv.FormatFloat(l[0], 'f', 2, 64) + " " + strconv.FormatFloat(l[1], 'f', 2, 64) + " " + strconv.FormatFloat(l[2], 'f', 2, 64)
	}
	w.flow("", "  ", " · ", "up "+uptime, "load "+load, "sampled "+s.SampledAt.Format("2006-01-02 15:04:05 UTC"))
	w.problem("", "Host details unavailable: ", s.Errors["host"])
}

func (w *textWriter) cpu(s *sampler.Snapshot) {
	w.blank()
	c := s.CPU
	if c == nil {
		w.problem("", "CPU unavailable: ", s.Errors["cpu"])
		return
	}
	count := strconv.Itoa(c.Cores) + " cores"
	if c.Cores == 1 {
		count = "1 core"
	}
	if d := c.Darwin; d != nil {
		count += " (" + strconv.Itoa(d.PCores) + " P + " + strconv.Itoa(d.ECores) + " E)"
	}
	model := c.Model
	if model == "" {
		model = "unknown model"
	}
	w.flow("", "  ", " · ", "CPU "+humanPct(c.UsagePct), model, count)
	w.problem("  ", "", s.Errors["cpu"])
	w.cores(cores(c))
}

func (w *textWriter) memory(s *sampler.Snapshot) {
	w.blank()
	m := s.Memory
	if m == nil {
		w.problem("", "Memory unavailable: ", s.Errors["memory"])
		return
	}
	w.flow("", "  ", " · ", "Memory "+humanBytes(m.UsedBytes)+" of "+humanBytes(m.TotalBytes)+" used ("+humanPct(memPct(m))+")")
	w.problem("  ", "", s.Errors["memory"])
	swap := "none"
	if m.SwapTotalBytes > 0 {
		swap = humanBytes(m.SwapUsedBytes) + " of " + humanBytes(m.SwapTotalBytes)
	}
	items := []string{"available " + humanBytes(m.AvailableBytes), "swap " + swap}
	if d := m.Darwin; d != nil {
		pressure := "unknown"
		if d.Pressure != nil {
			pressure = *d.Pressure
		}
		items = append(items, "pressure "+pressure, "wired "+humanBytes(d.WiredBytes),
			"compressed "+humanBytes(d.CompressedBytes), "active "+humanBytes(d.ActiveBytes),
			"inactive "+humanBytes(d.InactiveBytes), "file-backed "+humanBytes(d.FileBackedBytes))
	}
	w.flow("  ", "  ", " · ", items...)
}

func (w *textWriter) disks(s *sampler.Snapshot) {
	w.blank()
	err := s.Errors["disks"]
	if s.Disks == nil && err != "" {
		w.problem("", "Disks unavailable: ", err)
		return
	}
	w.line("Disks")
	w.problem("  ", "", err)
	if len(s.Disks) == 0 {
		w.line("  No disks to show.")
		return
	}
	mounts := make([]string, len(s.Disks))
	for i, d := range s.Disks {
		mounts[i] = d.Mount
	}
	name := nameWidth(textWidth-2-5-2*bytesCol-pctCol-2*rateCol, "MOUNT", mounts)
	row := func(mount, used, size, pct, read, write string) {
		w.line("  " + left(mount, name) + " " + right(used, bytesCol) + " " + right(size, bytesCol) + " " +
			right(pct, pctCol) + " " + right(read, rateCol) + " " + right(write, rateCol))
	}
	row("MOUNT", "USED", "SIZE", "USE%", "READ", "WRITE")
	for _, d := range s.Disks {
		row(d.Mount, humanBytes(d.UsedBytes), humanBytes(d.TotalBytes), humanPct(d.UsedPct), humanRate(d.ReadBps), humanRate(d.WriteBps))
	}
}

func (w *textWriter) network(s *sampler.Snapshot) {
	w.blank()
	if s.Network == nil && s.Errors["network"] != "" {
		w.problem("", "Network unavailable: ", s.Errors["network"])
		return
	}
	w.line("Network")
	w.problem("  ", "", s.Errors["network"])
	shown := shownInterfaces(s.Network)
	if len(shown) == 0 {
		w.line("  No interfaces to show.")
		return
	}
	ifaces := make([]string, len(shown))
	for i, n := range shown {
		ifaces[i] = n.Name
	}
	name := nameWidth(textWidth-2-4-2*rateCol-2*bytesCol, "IFACE", ifaces)
	row := func(iface, rx, tx, rxTotal, txTotal string) {
		w.line("  " + left(iface, name) + " " + right(rx, rateCol) + " " + right(tx, rateCol) + " " +
			right(rxTotal, bytesCol) + " " + right(txTotal, bytesCol))
	}
	row("IFACE", "RECEIVE", "SEND", "RX TOTAL", "TX TOTAL")
	for _, n := range shown {
		row(n.Name, humanRate(n.RxBps), humanRate(n.TxBps), humanBytes(n.RxTotalBytes), humanBytes(n.TxTotalBytes))
	}
}

// temps is there when there are sensors, or when reading them failed, as on
// the dashboard, so a failure isn't silently hidden.
func (w *textWriter) temps(s *sampler.Snapshot) {
	err := s.Errors["temps"]
	if len(s.Temps) == 0 && err == "" {
		return
	}
	w.blank()
	if len(s.Temps) == 0 {
		w.problem("", "Temperatures unavailable: ", err)
		return
	}
	w.line("Temperatures")
	w.problem("  ", "", err)
	const tempCol = 9 // "-273.1 °C"
	labels := make([]string, len(s.Temps))
	for i, t := range s.Temps {
		labels[i] = t.Label
	}
	name := nameWidth(textWidth-2-1-tempCol, "", labels)
	for _, t := range s.Temps {
		w.line("  " + left(t.Label, name) + " " + right(strconv.FormatFloat(t.Celsius, 'f', 1, 64)+" °C", tempCol))
	}
}

// nameWidth is the width of a table's name column: its longest name or
// header, capped at what the figures leave free.
func nameWidth(limit int, header string, names []string) int {
	n := utf8.RuneCountInString(header)
	for _, name := range names {
		n = max(n, utf8.RuneCountInString(clean(name)))
	}
	return min(n, limit)
}

type textWriter struct{ bytes.Buffer }

func (w *textWriter) line(s string) {
	w.WriteString(strings.TrimRight(s, " "))
	w.WriteByte('\n')
}

func (w *textWriter) blank() { w.WriteByte('\n') }

// flow writes items joined by sep, moving to a new line, indented by rest,
// whenever the next item would pass the width. An item too long for a line of
// its own is cut short.
func (w *textWriter) flow(first, rest, sep string, items ...string) {
	cur := first
	for i, item := range items {
		item = fit(item, textWidth-utf8.RuneCountInString(rest))
		if i > 0 && utf8.RuneCountInString(cur+sep+item) > textWidth {
			w.line(cur)
			cur = rest + item
			continue
		}
		if i > 0 {
			cur += sep
		}
		cur += item
	}
	w.line(cur)
}

// problem writes a group's error after its lead-in, wrapped at spaces, with
// continuation lines indented two more spaces. Nothing when err is "".
func (w *textWriter) problem(indent, lead, err string) {
	if err == "" {
		return
	}
	words := strings.Fields(lead + clean(err))
	more := indent + "  "
	room := textWidth - utf8.RuneCountInString(more)
	cur := indent
	for i, word := range words {
		word = fit(word, room)
		if i > 0 && utf8.RuneCountInString(cur)+1+utf8.RuneCountInString(word) > textWidth {
			w.line(cur)
			cur = more + word
			continue
		}
		if i > 0 {
			cur += " "
		}
		cur += word
	}
	w.line(cur)
}

// cores lays per-core usage out in as many columns as fit: "P0   9.1%".
func (w *textWriter) cores(cs []core) {
	label := 0
	for _, c := range cs {
		label = max(label, utf8.RuneCountInString(c.Label))
	}
	cell := label + 1 + pctCol
	per := max((textWidth-2+3)/(cell+3), 1)
	var b strings.Builder
	for i, c := range cs {
		if i%per == 0 {
			if i > 0 {
				w.line(b.String())
				b.Reset()
			}
			b.WriteString("  ")
		} else {
			b.WriteString("   ")
		}
		b.WriteString(left(c.Label, label) + " " + right(humanPct(c.Pct), pctCol))
	}
	if b.Len() > 0 {
		w.line(b.String())
	}
}

// clean replaces control characters, so a hostname, mount point, or sensor
// label can't move the cursor or recolour the terminal it's printed to.
func clean(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return '?'
		}
		return r
	}, s)
}

// fit cleans s and cuts it to at most n runes, ending in "…" when cut.
func fit(s string, n int) string {
	s = clean(s)
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)
	return string(r[:n-1]) + "…"
}

func left(s string, n int) string {
	s = fit(s, n)
	return s + strings.Repeat(" ", n-utf8.RuneCountInString(s))
}

func right(s string, n int) string {
	s = fit(s, n)
	return strings.Repeat(" ", n-utf8.RuneCountInString(s)) + s
}
