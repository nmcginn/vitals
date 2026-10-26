package render

import (
	"fmt"
	"math"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/nmcginn/vitals/internal/sampler"
)

var testBuild = Build{Version: "v1.0.0", Commit: "abc1234"}

func TestPromGolden(t *testing.T) {
	for _, tc := range []struct {
		name, file string
		snap       *sampler.Snapshot
	}{
		{"a full darwin snapshot", "darwin_full.prom", darwinFull()},
		{"a first-sample snapshot with every rate unknown", "darwin_first.prom", darwinFirst()},
		{"a linux snapshot with temps, a failed group, and awkward disks", "linux_partial.prom", linuxPartial()},
		{"a darwin snapshot with failed groups", "darwin_errors.prom", darwinErrors()},
	} {
		t.Run(tc.name+" matches its golden file", func(t *testing.T) {
			golden(t, tc.file, Prom(tc.snap, testBuild))
		})
	}
}

// fixtures are every fixed snapshot, for properties that hold of all of them.
func fixtures() map[string]*sampler.Snapshot {
	return map[string]*sampler.Snapshot{
		"darwin full": darwinFull(), "darwin first": darwinFirst(),
		"linux partial": linuxPartial(), "darwin errors": darwinErrors(),
		"escapes": awkwardLabels(),
	}
}

// awkwardLabels has a hostname and a mount point with every character the
// exposition format makes a label value escape.
func awkwardLabels() *sampler.Snapshot {
	s := linuxPartial()
	s.Host.Hostname = "box \"one\"\\two\nthree"
	s.Disks[1].Mount = `/srv/"quoted" \ path`
	return s
}

func TestPromGrammar(t *testing.T) {
	for name, s := range fixtures() {
		t.Run(name+" is valid exposition format 0.0.4", func(t *testing.T) {
			for _, p := range checkExposition(Prom(s, testBuild)) {
				t.Error(p)
			}
		})
	}

	t.Run("a build with quotes and backslashes in its version is still valid", func(t *testing.T) {
		for _, p := range checkExposition(Prom(darwinFull(), Build{Version: `v1 "rc" \ 2`, Commit: ""})) {
			t.Error(p)
		}
	})
}

func TestPromSpecNames(t *testing.T) {
	// The four series the spec fixes, by name and the label it names.
	samples := parseSamples(t, Prom(darwinFull(), testBuild))
	for _, want := range []struct{ name, label, value string }{
		{"vitals_cpu_usage_ratio", "cpu", "total"},
		{"vitals_memory_available_bytes", "", ""},
		{"vitals_filesystem_used_bytes", "mount", "/"},
		{"vitals_network_receive_bytes_per_second", "iface", "en0"},
	} {
		t.Run(fmt.Sprintf("%s{%s=%q} is present", want.name, want.label, want.value), func(t *testing.T) {
			if !slices.ContainsFunc(samples, func(s promSample) bool {
				return s.name == want.name && (want.label == "" || s.labels[want.label] == want.value)
			}) {
				t.Errorf("no %s sample with %s=%q", want.name, want.label, want.value)
			}
		})
	}
}

func TestPromValues(t *testing.T) {
	t.Run("unknown rates are omitted on the first sample", func(t *testing.T) {
		b := string(Prom(darwinFirst(), testBuild))
		for _, absent := range []string{"_per_second", "vitals_cpu_usage_ratio"} {
			if strings.Contains(b, absent) {
				t.Errorf("first sample has %s, want it omitted:\n%s", absent, b)
			}
		}
		if !strings.Contains(b, `vitals_network_receive_bytes_total{iface="en0"} 98234111`) {
			t.Error("first sample lost the network counters, which need no previous sample")
		}
	})

	t.Run("ratios are within 0 to 1 and no value is NaN or infinite", func(t *testing.T) {
		for name, s := range fixtures() {
			for _, smp := range parseSamples(t, Prom(s, testBuild)) {
				if strings.HasSuffix(smp.name, "_ratio") && (smp.value < 0 || smp.value > 1) {
					t.Errorf("%s: %s = %v, outside 0 to 1", name, smp.name, smp.value)
				}
				if math.IsInf(smp.value, 0) {
					t.Errorf("%s: %s is infinite", name, smp.name)
				}
			}
		}
	})

	t.Run("a percentage becomes a ratio", func(t *testing.T) {
		if !strings.Contains(string(Prom(linuxPartial(), testBuild)), `vitals_cpu_usage_ratio{cpu="total"} 1`+"\n") {
			t.Error("100% CPU is not a ratio of 1")
		}
	})

	t.Run("label values escape quotes, backslashes, and newlines", func(t *testing.T) {
		b := string(Prom(awkwardLabels(), testBuild))
		for _, want := range []string{
			`hostname="box \"one\"\\two\nthree"`,
			`mount="/srv/\"quoted\" \\ path"`,
		} {
			if !strings.Contains(b, want) {
				t.Errorf("output lacks %s:\n%s", want, b)
			}
		}
	})

	t.Run("network counters past 2^32 bytes are written exactly", func(t *testing.T) {
		s := darwinFull()
		s.Network[0].RxTotalBytes = 1<<53 + 1 // a float64 would round this
		if !strings.Contains(string(Prom(s, testBuild)), `vitals_network_receive_bytes_total{iface="en0"} 9007199254740993`) {
			t.Error("the counter was not written as an exact integer")
		}
	})

	t.Run("mounts on one device report its rates once, under the device", func(t *testing.T) {
		s := linuxPartial()
		boot := s.Disks[0]
		boot.Mount, boot.Device = "/boot", "/dev/nvme0n1p1"
		s.Disks = append(s.Disks, boot)
		samples := parseSamples(t, Prom(s, testBuild))
		var reads []string
		for _, smp := range samples {
			if smp.name == "vitals_disk_read_bytes_per_second" {
				reads = append(reads, smp.labels["device"])
			}
		}
		if !slices.Equal(reads, []string{"nvme0n1"}) {
			t.Errorf("read rates for devices %q, want just nvme0n1", reads)
		}
	})

	t.Run("a mount with no physical device has size but no rates", func(t *testing.T) {
		b := string(Prom(linuxPartial(), testBuild))
		if !strings.Contains(b, `vitals_filesystem_size_bytes{mount="/mnt/empty",device="/dev/loop9",fs="ext4"} 0`) {
			t.Error("the zero-size mount is missing")
		}
		if n := strings.Count(b, "vitals_disk_read_bytes_per_second{"); n != 1 {
			t.Errorf("%d disk read rates, want 1: the share and the empty mount have no device", n)
		}
	})

	t.Run("each group's error is 1 when it failed and 0 when it didn't", func(t *testing.T) {
		got := map[string]float64{}
		for _, smp := range parseSamples(t, Prom(darwinErrors(), testBuild)) {
			if smp.name == "vitals_collector_error" {
				got[smp.labels["group"]] = smp.value
			}
		}
		want := map[string]float64{"host": 0, "cpu": 1, "memory": 1, "disks": 1, "network": 0, "temps": 0}
		if fmt.Sprint(got) != fmt.Sprint(want) {
			t.Errorf("collector errors = %v, want %v", got, want)
		}
	})

	t.Run("exactly one memory pressure level is 1, and an unknown level is omitted", func(t *testing.T) {
		levels := map[string]float64{}
		for _, smp := range parseSamples(t, Prom(darwinFull(), testBuild)) {
			if smp.name == "vitals_memory_pressure" {
				levels[smp.labels["level"]] = smp.value
			}
		}
		if want := map[string]float64{"normal": 1, "warn": 0, "critical": 0}; fmt.Sprint(levels) != fmt.Sprint(want) {
			t.Errorf("pressure levels = %v, want %v", levels, want)
		}
		s := darwinFull()
		s.Memory.Darwin.Pressure = nil
		if b := string(Prom(s, testBuild)); strings.Contains(b, "vitals_memory_pressure") {
			t.Error("an unknown pressure level was reported")
		}
	})

	t.Run("no sensors and no darwin fields leave those families out entirely", func(t *testing.T) {
		b := string(Prom(darwinFull(), testBuild))
		if strings.Contains(b, "vitals_temperature_celsius") {
			t.Error("darwin has temperature lines without sensors")
		}
		b = string(Prom(linuxPartial(), testBuild))
		for _, absent := range []string{"vitals_memory_wired_bytes", "vitals_cpu_core_info", "vitals_memory_pressure", "vitals_network_"} {
			if strings.Contains(b, absent) {
				t.Errorf("linux output has %s", absent)
			}
		}
	})
}

func TestPromGrammarChecker(t *testing.T) {
	// The checker is what stands in for promtool (decision 8), so it must
	// catch what promtool would.
	for _, tc := range []struct{ name, in string }{
		{"a sample without HELP and TYPE", "a 1\n"},
		{"TYPE before HELP", "# TYPE a gauge\n# HELP a x\na 1\n"},
		{"a family declared twice", "# HELP a x\n# TYPE a gauge\na 1\n# HELP a x\n# TYPE a gauge\na 2\n"},
		{"a sample under another family's TYPE", "# HELP a x\n# TYPE a gauge\nb 1\n"},
		{"a duplicate label set", "# HELP a x\n# TYPE a gauge\na{x=\"1\"} 1\na{x=\"1\"} 2\n"},
		{"a bad metric name", "# HELP 1a x\n# TYPE 1a gauge\n1a 1\n"},
		{"a bad label name", "# HELP a x\n# TYPE a gauge\na{x-y=\"1\"} 1\n"},
		{"an unknown escape", "# HELP a x\n# TYPE a gauge\na{x=\"\\t\"} 1\n"},
		{"an unescaped quote", "# HELP a x\n# TYPE a gauge\na{x=\"a\"b\"} 1\n"},
		{"a NaN", "# HELP a x\n# TYPE a gauge\na NaN\n"},
		{"an unknown type", "# HELP a x\n# TYPE a meter\na 1\n"},
		{"a counter not ending in _total", "# HELP a x\n# TYPE a counter\na 1\n"},
		{"no final newline", "# HELP a x\n# TYPE a gauge\na 1"},
	} {
		t.Run("rejects "+tc.name, func(t *testing.T) {
			if len(checkExposition([]byte(tc.in))) == 0 {
				t.Errorf("accepted:\n%s", tc.in)
			}
		})
	}
	t.Run("accepts a valid family with escapes", func(t *testing.T) {
		in := "# HELP a_total x\n# TYPE a_total counter\na_total{x=\"q\\\"b\\\\n\\n\",y=\"\"} 1.5e+06\na_total 2\n"
		for _, p := range checkExposition([]byte(in)) {
			t.Error(p)
		}
	})
}

var (
	metricName = regexp.MustCompile(`^[a-zA-Z_:][a-zA-Z0-9_:]*`)
	labelName  = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_]*`)
	promTypes  = []string{"counter", "gauge", "histogram", "summary", "untyped"}
)

type promSample struct {
	name   string
	labels map[string]string
	value  float64
}

// checkExposition returns every way b breaks the exposition format 0.0.4
// rules promtool enforces: HELP then TYPE before a family's samples, each
// family once and contiguous, valid names, label values with only the three
// escapes, unique series, and parseable values. It also holds vitals to its
// own rules: no NaN, and counters end in _total.
func checkExposition(b []byte) []string {
	var problems []string
	bad := func(line int, format string, args ...any) {
		problems = append(problems, fmt.Sprintf("line %d: ", line)+fmt.Sprintf(format, args...))
	}
	if len(b) == 0 || b[len(b)-1] != '\n' {
		problems = append(problems, "output does not end in a newline")
	}
	lines := strings.Split(strings.TrimSuffix(string(b), "\n"), "\n")
	declared := map[string]bool{}
	series := map[string]bool{}
	var help, typ string // the family the HELP and TYPE just seen belong to
	for i, line := range lines {
		n := i + 1
		switch {
		case strings.HasPrefix(line, "# HELP "):
			name, _, _ := strings.Cut(strings.TrimPrefix(line, "# HELP "), " ")
			if metricName.FindString(name) != name {
				bad(n, "invalid metric name %q", name)
			}
			if declared[name] {
				bad(n, "family %s declared twice", name)
			}
			declared[name], help, typ = true, name, ""
		case strings.HasPrefix(line, "# TYPE "):
			name, t, _ := strings.Cut(strings.TrimPrefix(line, "# TYPE "), " ")
			if name != help {
				bad(n, "TYPE for %s does not follow its HELP", name)
			}
			if !slices.Contains(promTypes, t) {
				bad(n, "unknown type %q", t)
			}
			if t == "counter" && !strings.HasSuffix(name, "_total") {
				bad(n, "counter %s does not end in _total", name)
			}
			typ = name
		case strings.HasPrefix(line, "#"):
			bad(n, "unexpected comment %q", line)
		default:
			s, key, err := parseSample(line)
			if err != nil {
				bad(n, "%v in %q", err, line)
				continue
			}
			if s.name != typ {
				bad(n, "sample %s is not under its own HELP and TYPE", s.name)
			}
			if series[key] {
				bad(n, "duplicate series %s", key)
			}
			series[key] = true
		}
	}
	return problems
}

// parseSample parses one sample line. key identifies the series: the name and
// its labels in sorted order.
func parseSample(line string) (s promSample, key string, err error) {
	s.name = metricName.FindString(line)
	if s.name == "" {
		return s, "", fmt.Errorf("invalid metric name")
	}
	rest := line[len(s.name):]
	s.labels = map[string]string{}
	if strings.HasPrefix(rest, "{") {
		rest = rest[1:]
		for !strings.HasPrefix(rest, "}") {
			name := labelName.FindString(rest)
			if name == "" || strings.HasPrefix(name, "__") {
				return s, "", fmt.Errorf("invalid label name")
			}
			if _, dup := s.labels[name]; dup {
				return s, "", fmt.Errorf("label %s repeated", name)
			}
			rest = rest[len(name):]
			if !strings.HasPrefix(rest, `="`) {
				return s, "", fmt.Errorf("label %s has no quoted value", name)
			}
			rest = rest[2:]
			var v strings.Builder
			for {
				if rest == "" {
					return s, "", fmt.Errorf("unterminated label value")
				}
				c := rest[0]
				rest = rest[1:]
				if c == '"' {
					break
				}
				if c == '\\' {
					if rest == "" {
						return s, "", fmt.Errorf("unterminated escape")
					}
					switch rest[0] {
					case '\\':
						v.WriteByte('\\')
					case '"':
						v.WriteByte('"')
					case 'n':
						v.WriteByte('\n')
					default:
						return s, "", fmt.Errorf("unknown escape \\%c", rest[0])
					}
					rest = rest[1:]
					continue
				}
				v.WriteByte(c)
			}
			s.labels[name] = v.String()
			if strings.HasPrefix(rest, ",") {
				rest = rest[1:]
			} else if !strings.HasPrefix(rest, "}") {
				return s, "", fmt.Errorf("expected , or } after label %s", name)
			}
		}
		rest = rest[1:]
	}
	if !strings.HasPrefix(rest, " ") {
		return s, "", fmt.Errorf("no space before the value")
	}
	value := rest[1:]
	if s.value, err = strconv.ParseFloat(value, 64); err != nil {
		return s, "", fmt.Errorf("value %q is not a number", value)
	}
	if math.IsNaN(s.value) {
		return s, "", fmt.Errorf("NaN value")
	}
	names := make([]string, 0, len(s.labels))
	for k := range s.labels {
		names = append(names, k)
	}
	slices.Sort(names)
	key = s.name
	for _, k := range names {
		key += fmt.Sprintf(",%s=%q", k, s.labels[k])
	}
	return s, key, nil
}

// parseSamples parses b's sample lines, failing the test on a bad one.
func parseSamples(t *testing.T, b []byte) []promSample {
	t.Helper()
	var out []promSample
	for _, line := range strings.Split(strings.TrimSuffix(string(b), "\n"), "\n") {
		if strings.HasPrefix(line, "#") {
			continue
		}
		s, _, err := parseSample(line)
		if err != nil {
			t.Fatalf("%v in %q", err, line)
		}
		out = append(out, s)
	}
	return out
}
