package render

import "github.com/nmcginn/vitals/internal/sampler"

// shownInterfaces is the interfaces the dashboard and text list: those that
// have moved at least one byte either way since boot (decision 41). It looks
// at totals, not rates, so the first sample, whose rates are all null, hides
// the same interfaces as every later one. JSON and Prometheus don't call it:
// they list every interface the filters keep. It returns ifaces itself when
// nothing is idle, which is the usual case on Linux.
func shownInterfaces(ifaces []sampler.Interface) []sampler.Interface {
	idle := 0
	for _, n := range ifaces {
		if isIdle(n) {
			idle++
		}
	}
	if idle == 0 {
		return ifaces
	}
	shown := make([]sampler.Interface, 0, len(ifaces)-idle)
	for _, n := range ifaces {
		if !isIdle(n) {
			shown = append(shown, n)
		}
	}
	return shown
}

func isIdle(n sampler.Interface) bool { return n.RxTotalBytes == 0 && n.TxTotalBytes == 0 }
