package render

import (
	"strings"
	"testing"

	"github.com/nmcginn/vitals/internal/sampler"
)

// listed reports which of the four formats list the named interface.
type listed struct{ html, text, json, prom bool }

func listings(t *testing.T, s *sampler.Snapshot, name string) listed {
	t.Helper()
	js, err := JSON(s, false)
	if err != nil {
		t.Fatal(err)
	}
	return listed{
		html: strings.Contains(mustHTML(t, s, historyOf(s, 30), Page{}), `<th scope="row">`+name+`</th>`),
		text: strings.Contains(string(Text(s)), "\n  "+name+" "),
		json: strings.Contains(string(js), `"iface":"`+name+`"`),
		prom: strings.Contains(string(Prom(s, Build{})), `iface="`+name+`"`),
	}
}

func TestIdleInterfaces(t *testing.T) {
	everywhere := listed{true, true, true, true}
	machineOnly := listed{json: true, prom: true}
	for _, tc := range []struct {
		name     string
		rx, tx   uint64
		nullRate bool
		want     listed
	}{
		{"an interface that has never moved a byte is listed in JSON and Prometheus only", 0, 0, false, machineOnly},
		{"one byte received is enough to show an interface everywhere", 1, 0, false, everywhere},
		{"one byte sent is enough to show an interface everywhere", 0, 1, false, everywhere},
		{"traffic past 2^32 bytes shows an interface everywhere", 5 << 32, 0, false, everywhere},
		{"on the first sample an idle interface is hidden by its totals alone", 0, 0, true, machineOnly},
		{"on the first sample a busy interface is shown though its rates are null", 1, 1, true, everywhere},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := darwinFull()
			s.Network = append(s.Network, sampler.Interface{Name: "en7", RxTotalBytes: tc.rx, TxTotalBytes: tc.tx})
			if !tc.nullRate {
				n := &s.Network[len(s.Network)-1]
				n.RxBps, n.TxBps, n.RxPps, n.TxPps = p(0.0), p(0.0), p(0.0), p(0.0)
			}
			if got := listings(t, s, "en7"); got != tc.want {
				t.Errorf("en7 listed in %+v, want %+v", got, tc.want)
			}
			if got := listings(t, s, "en0"); got != everywhere {
				t.Errorf("en0 listed in %+v, want everywhere", got)
			}
		})
	}

	t.Run("when every interface is idle the dashboard and text say there are none to show", func(t *testing.T) {
		s := darwinFull()
		s.Network = []sampler.Interface{{Name: "gif0"}, {Name: "stf0"}, {Name: "ap1"}}
		page := mustHTML(t, s, historyOf(s, 30), Page{})
		if !strings.Contains(page, `<p class="none">No interfaces to show.</p>`) || strings.Contains(page, "Receive</th>") {
			t.Error("the network card has a table rather than saying there are no interfaces to show")
		}
		if out := string(Text(s)); !strings.Contains(out, "Network\n  No interfaces to show.\n") {
			t.Errorf("text lacks No interfaces to show:\n%s", out)
		}
		for _, name := range []string{"gif0", "stf0", "ap1"} {
			if got := listings(t, s, name); got != machineOnly {
				t.Errorf("%s listed in %+v, want JSON and Prometheus only", name, got)
			}
		}
	})

	t.Run("a hidden interface gets no sparkline, and the shown ones keep theirs", func(t *testing.T) {
		s := darwinFull()
		s.Network = []sampler.Interface{{Name: "gif0"}, s.Network[0], {Name: "stf0"}, {Name: "en1", RxTotalBytes: 1}}
		page := mustHTML(t, s, historyOf(s, 30), Page{})
		for _, name := range []string{"gif0", "stf0"} {
			if strings.Contains(page, name) {
				t.Errorf("the page mentions idle %s", name)
			}
		}
		for _, name := range []string{"en0", "en1"} {
			if !strings.Contains(page, "<title>"+name+" receive (solid) and send (dashed)") {
				t.Errorf("%s has no sparkline", name)
			}
		}
	})

	t.Run("shown interfaces keep the OS's order", func(t *testing.T) {
		got := shownInterfaces([]sampler.Interface{
			{Name: "en2", TxTotalBytes: 1}, {Name: "gif0"}, {Name: "en0", RxTotalBytes: 1}, {Name: "stf0"}, {Name: "en1", RxTotalBytes: 1},
		})
		var names []string
		for _, n := range got {
			names = append(names, n.Name)
		}
		if strings.Join(names, " ") != "en2 en0 en1" {
			t.Errorf("shown %v, want [en2 en0 en1]", names)
		}
	})

	t.Run("a failed network group still reads unavailable, not that there are none to show", func(t *testing.T) {
		s := linuxPartial()
		if out := string(Text(s)); strings.Contains(out, "No interfaces") || !strings.Contains(out, "Network unavailable:") {
			t.Errorf("text:\n%s", out)
		}
		if page := mustHTML(t, s, nil, Page{}); strings.Contains(page, "No interfaces") {
			t.Error("the page says no interfaces alongside the error")
		}
	})
}
