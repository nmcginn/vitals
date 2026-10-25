package collector

import (
	"regexp"
	"slices"
	"testing"
)

func TestKeepIfaces(t *testing.T) {
	ifaces := func(names ...string) []Interface {
		ifs := make([]Interface, len(names))
		for i, n := range names {
			ifs[i] = Interface{Name: n, RxBytes: uint64(i)}
		}
		return ifs
	}
	names := func(ifs []Interface) []string {
		n := []string{}
		for _, i := range ifs {
			n = append(n, i.Name)
		}
		return n
	}

	for _, name := range []string{"lo", "lo0", "veth3f2a1b0", "docker0", "br-5e1f0c2d9a7b", "utun4", "awdl0", "llw0", "bridge0", "anpi1"} {
		t.Run("the default list excludes "+name, func(t *testing.T) {
			if got := keepIfaces(ifaces(name), nil); len(got) != 0 {
				t.Errorf("kept %v", names(got))
			}
		})
	}

	// "xlo0" and "lo1" check that the default patterns are anchored and that
	// "lo" isn't a prefix match.
	for _, name := range []string{"en0", "eth0", "tailscale0", "wg-home", "enp3s0", "xlo0", "lo1"} {
		t.Run("the default list keeps "+name, func(t *testing.T) {
			if got := keepIfaces(ifaces(name), nil); len(got) != 1 {
				t.Errorf("dropped %s", name)
			}
		})
	}

	t.Run("a user's regex replaces the defaults: ^en1$ drops en1 and keeps lo0 and utun0", func(t *testing.T) {
		got := keepIfaces(ifaces("lo0", "en0", "en1", "utun0", "en10"), regexp.MustCompile(`^en1$`))
		if want := []string{"lo0", "en0", "utun0", "en10"}; !slices.Equal(names(got), want) {
			t.Errorf("kept %v, want %v", names(got), want)
		}
	})

	t.Run("kept interfaces keep their order and counters", func(t *testing.T) {
		got := keepIfaces(ifaces("lo0", "en0", "utun0", "en1"), nil)
		want := []Interface{{Name: "en0", RxBytes: 1}, {Name: "en1", RxBytes: 3}}
		if !slices.Equal(got, want) {
			t.Errorf("kept %+v, want %+v", got, want)
		}
	})

	t.Run("no interfaces at all is an empty list, not nil", func(t *testing.T) {
		if got := keepIfaces(nil, nil); got == nil || len(got) != 0 {
			t.Errorf("got %#v, want an empty, non-nil slice", got)
		}
	})
}
