package render

import (
	"math"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

var nan = math.NaN()

// points parses a path into its subpaths' y coordinates, so tests can talk
// about runs and heights rather than exact strings.
func points(t *testing.T, d string) [][]float64 {
	t.Helper()
	var runs [][]float64
	for _, sub := range strings.Split(d, "M")[1:] {
		var ys []float64
		for _, pt := range strings.Split(strings.TrimSuffix(sub, "h0"), "L") {
			xy := strings.Fields(pt)
			if len(xy) != 2 {
				t.Fatalf("malformed point %q in %q", pt, d)
			}
			y, err := strconv.ParseFloat(xy[1], 64)
			if err != nil {
				t.Fatal(err)
			}
			ys = append(ys, y)
		}
		runs = append(runs, ys)
	}
	return runs
}

func TestSparkPath(t *testing.T) {
	bottom, top := float64(sparkH-sparkPad), float64(sparkPad)

	t.Run("an empty history draws nothing", func(t *testing.T) {
		if d := sparkPath(nil, 100); d != "" {
			t.Errorf("sparkPath(nil) = %q, want \"\"", d)
		}
	})

	t.Run("a history of unknowns draws nothing", func(t *testing.T) {
		if d := sparkPath(series{nan, nan}, 100); d != "" {
			t.Errorf("got %q, want \"\"", d)
		}
	})

	t.Run("a single point is a dot at the right edge", func(t *testing.T) {
		want := "M" + strconv.Itoa(sparkW) + " " + strconv.FormatFloat((top+bottom)/2, 'f', -1, 64) + "h0"
		if d := sparkPath(series{50}, 100); d != want {
			t.Errorf("got %q, want %q", d, want)
		}
	})

	t.Run("a flat line keeps one height from edge to edge", func(t *testing.T) {
		d := sparkPath(series{30, 30, 30, 30}, 100)
		if !strings.HasPrefix(d, "M0 ") || !strings.Contains(d, "L"+strconv.Itoa(sparkW)+" ") {
			t.Errorf("line doesn't span the box: %q", d)
		}
		runs := points(t, d)
		for _, y := range runs[0] {
			if y != runs[0][0] {
				t.Errorf("flat line moves: %q", d)
			}
		}
	})

	t.Run("all zeros with no peak is a flat line along the bottom", func(t *testing.T) {
		for _, y := range points(t, sparkPath(series{0, 0, 0}, 0))[0] {
			if y != bottom {
				t.Errorf("y = %v, want %v", y, bottom)
			}
		}
	})

	t.Run("an unknown value lifts the pen, and a lone point between gaps is a dot", func(t *testing.T) {
		d := sparkPath(series{10, 20, nan, 30, nan, nan, 40, 50}, 100)
		if runs := points(t, d); len(runs) != 3 || len(runs[0]) != 2 || len(runs[1]) != 1 || len(runs[2]) != 2 {
			t.Errorf("runs = %v from %q, want 2, 1, and 2 points", runs, d)
		}
		if strings.Count(d, "h0") != 1 {
			t.Errorf("want exactly the lone point drawn as a dot: %q", d)
		}
	})

	t.Run("a leading unknown, as on the first sample, starts the line later", func(t *testing.T) {
		d := sparkPath(series{nan, 10, 20}, 100)
		if !strings.HasPrefix(d, "M60 ") {
			t.Errorf("got %q, want the line to start at the middle point", d)
		}
	})

	t.Run("values outside 0 to top stay inside the box", func(t *testing.T) {
		ys := points(t, sparkPath(series{-5, 150}, 100))[0]
		if ys[0] != bottom || ys[1] != top {
			t.Errorf("ys = %v, want %v and %v", ys, bottom, top)
		}
	})

	t.Run("coordinates have at most one decimal place", func(t *testing.T) {
		d := sparkPath(series{1, 2, 3, 4, 5, 6, 7}, 7)
		if m := regexp.MustCompile(`\.\d\d`).FindString(d); m != "" {
			t.Errorf("found %q in %q", m, d)
		}
	})
}

// downsampleOf downsamples a series held in a slice.
func downsampleOf(s series) series {
	return downsample(len(s), func(i int) float64 { return s[i] })
}

func TestDownsample(t *testing.T) {
	t.Run("a short history is drawn point for point", func(t *testing.T) {
		s := series{1, 2, 3}
		if got := downsampleOf(s); len(got) != 3 || got[0] != 1 || got[1] != 2 || got[2] != 3 {
			t.Errorf("got %v, want [1 2 3]", got)
		}
	})

	t.Run("450 samples become sparkPoints buckets, each the peak of its samples", func(t *testing.T) {
		s := make(series, 450)
		for i := range s {
			s[i] = float64(i % 2 * 10) // 0, 10, 0, 10, …
		}
		got := downsampleOf(s)
		if len(got) != sparkPoints {
			t.Fatalf("len = %d, want %d", len(got), sparkPoints)
		}
		for i, v := range got {
			if v != 10 {
				t.Errorf("bucket %d = %v, want the peak, 10", i, v)
			}
		}
	})

	t.Run("a single-sample spike keeps its full height", func(t *testing.T) {
		s := make(series, 450)
		s[200] = 100 // one 2s spike in 15 minutes of idle
		if got := peak(downsampleOf(s)); got != 100 {
			t.Errorf("tallest point = %v, want 100", got)
		}
	})

	t.Run("a bucket with nothing known stays a gap, and a partly known bucket takes its known peak", func(t *testing.T) {
		s := make(series, 2*sparkPoints)
		for i := range s {
			s[i] = 50
		}
		s[0], s[1] = nan, nan // bucket 0
		s[2] = nan            // half of bucket 1
		got := downsampleOf(s)
		if !math.IsNaN(got[0]) || got[1] != 50 {
			t.Errorf("buckets 0 and 1 = %v, %v; want NaN and 50", got[0], got[1])
		}
	})
}

func TestPeak(t *testing.T) {
	t.Run("the largest known value across every series", func(t *testing.T) {
		if got := peak(series{1, nan, 3}, series{nan, 7}); got != 7 {
			t.Errorf("peak = %v, want 7", got)
		}
	})
	t.Run("nothing known is 0", func(t *testing.T) {
		if got := peak(series{nan}, nil); got != 0 {
			t.Errorf("peak = %v, want 0", got)
		}
	})
}
