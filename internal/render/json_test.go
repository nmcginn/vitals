package render

import (
	"bytes"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/nmcginn/vitals/internal/sampler"
)

var update = flag.Bool("update", false, "rewrite golden files in testdata/")

// golden compares got with testdata/name, or rewrites it under -update.
func golden(t *testing.T, name string, got []byte) {
	t.Helper()
	path := filepath.Join("testdata", name)
	if *update {
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v (run `make golden` to create it)", err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("%s differs from the golden file; run `make golden` and review the diff.\ngot:\n%s", path, got)
	}
}

func mustJSON(t *testing.T, s *sampler.Snapshot, pretty bool) []byte {
	t.Helper()
	b, err := JSON(s, pretty)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestJSONGolden(t *testing.T) {
	for _, tc := range []struct {
		name, file string
		snap       *sampler.Snapshot
	}{
		{"a full darwin snapshot", "darwin_full.json", darwinFull()},
		{"a first-sample snapshot with every rate null", "darwin_first.json", darwinFirst()},
		{"a linux snapshot with temps, a failed group, and awkward disks", "linux_partial.json", linuxPartial()},
	} {
		t.Run(tc.name+" matches its golden file", func(t *testing.T) {
			golden(t, tc.file, mustJSON(t, tc.snap, true))
		})
	}
}

func TestJSONFormatting(t *testing.T) {
	t.Run("compact output is one line ending in a newline", func(t *testing.T) {
		b := mustJSON(t, darwinFull(), false)
		if bytes.Count(b, []byte("\n")) != 1 || b[len(b)-1] != '\n' {
			t.Errorf("compact output is not one line:\n%s", b)
		}
	})

	t.Run("pretty output decodes to the same value as compact", func(t *testing.T) {
		var a, b any
		if err := json.Unmarshal(mustJSON(t, darwinFull(), false), &a); err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(mustJSON(t, darwinFull(), true), &b); err != nil {
			t.Fatal(err)
		}
		ab, _ := json.Marshal(a)
		bb, _ := json.Marshal(b)
		if !bytes.Equal(ab, bb) {
			t.Error("pretty and compact JSON differ in content")
		}
		if !bytes.Contains(mustJSON(t, darwinFull(), true), []byte("\n  \"host\": {")) {
			t.Error("pretty output is not indented by two spaces")
		}
	})

	t.Run("sampled_at and boot_time are RFC 3339 UTC", func(t *testing.T) {
		b := mustJSON(t, darwinFull(), false)
		for _, want := range []string{`"sampled_at":"2026-10-21T14:02:06Z"`, `"boot_time":"2026-10-11T14:02:06Z"`} {
			if !bytes.Contains(b, []byte(want)) {
				t.Errorf("output lacks %s", want)
			}
		}
	})

	t.Run("platform-only fields are omitted on linux", func(t *testing.T) {
		if b := mustJSON(t, linuxPartial(), false); bytes.Contains(b, []byte(`"darwin"`)) {
			t.Errorf("linux snapshot has a darwin object:\n%s", b)
		}
	})
}

// planExample is the JSON example in docs/PLAN.md, verbatim.
const planExample = `{
  "sampled_at": "2026-10-21T14:02:06Z",
  "interval_s": 2,
  "host": {"hostname": "mini", "os": "darwin", "kernel": "25.1.0", "arch": "arm64", "uptime_s": 864000, "load": [0.41, 0.38, 0.35]},
  "cpu": {"usage_pct": 6.2, "per_core_pct": [9.1, 4.0], "cores": 10, "model": "Apple M4", "darwin": {"p_cores": 4, "e_cores": 6, "core_kinds": ["P", "P"]}},
  "memory": {"total_bytes": 17179869184, "available_bytes": 7516192768, "used_bytes": 9663676416, "swap_total_bytes": 0, "swap_used_bytes": 0, "darwin": {"wired_bytes": 2147483648, "compressed_bytes": 536870912, "pressure": "normal"}},
  "disks": [{"mount": "/", "device": "/dev/disk3s1", "fs": "apfs", "total_bytes": 494384795648, "used_bytes": 120259084288, "used_pct": 24.3, "read_bps": 0, "write_bps": 40960}],
  "network": [{"iface": "en0", "rx_bps": 12800, "tx_bps": 3400, "rx_pps": 21, "tx_pps": 14, "rx_total_bytes": 98234111, "tx_total_bytes": 12001221}],
  "temps": null,
  "errors": {}
}`

// documentedAdditions are the keys PLAN says the example abbreviates. Any
// other key in the output is an undocumented change to the shape.
var documentedAdditions = []string{
	"disks[].free_bytes",
	"host.boot_time",
	"host.os_version",
	"memory.darwin.active_bytes",
	"memory.darwin.file_backed_bytes",
	"memory.darwin.inactive_bytes",
}

// keyPaths lists every object key in a decoded JSON value as a dotted path,
// with arrays of objects written as name[].
func keyPaths(v any, prefix string, out map[string]bool) {
	switch v := v.(type) {
	case map[string]any:
		for k, child := range v {
			path := k
			if prefix != "" {
				path = prefix + "." + k
			}
			out[path] = true
			keyPaths(child, path, out)
		}
	case []any:
		for _, child := range v {
			keyPaths(child, strings.TrimSuffix(prefix, "[]")+"[]", out)
		}
	}
}

func TestJSONShape(t *testing.T) {
	paths := func(b []byte) map[string]bool {
		var v any
		if err := json.Unmarshal(b, &v); err != nil {
			t.Fatal(err)
		}
		m := map[string]bool{}
		keyPaths(v, "", m)
		return m
	}
	want := paths([]byte(planExample))
	got := paths(mustJSON(t, darwinFull(), false))

	t.Run("every key in the PLAN example is present", func(t *testing.T) {
		for k := range want {
			if !got[k] {
				t.Errorf("output lacks %s", k)
			}
		}
	})

	t.Run("every key beyond the PLAN example is a documented addition", func(t *testing.T) {
		var extra []string
		for k := range got {
			if !want[k] {
				extra = append(extra, k)
			}
		}
		slices.Sort(extra)
		if !slices.Equal(extra, documentedAdditions) {
			t.Errorf("keys beyond the PLAN example = %v\nwant %v", extra, documentedAdditions)
		}
	})

	t.Run("temps is null and errors is {} when there are no sensors and no failures", func(t *testing.T) {
		b := mustJSON(t, darwinFull(), false)
		if !bytes.Contains(b, []byte(`"temps":null`)) || !bytes.Contains(b, []byte(`"errors":{}`)) {
			t.Errorf("got %s", b)
		}
	})
}
