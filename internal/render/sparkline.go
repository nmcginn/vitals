package render

import (
	"math"
	"strconv"
)

// Every sparkline draws into the same fixed viewBox, which the stylesheet
// stretches to the card's width. A path has at most sparkPoints points, so 450
// samples of history cost the same bytes as 120: without that, eight
// interfaces' rx and tx lines alone would blow the 50 KB page budget.
const (
	sparkW      = 120
	sparkH      = 32
	sparkPad    = 1 // keeps a line at 0 or at the top inside the box
	sparkPoints = 120
)

// A series is one value per snapshot, oldest first. NaN is a value that
// can't be known (a first-sample rate, a failed group), drawn as a gap.
type series []float64

// downsample keeps the peak of each run of consecutive values, in at most
// sparkPoints buckets, so a short spike still reaches its full height (decision
// 33). Unknowns are skipped, and a bucket with nothing known stays a gap.
func downsample(s series) series {
	if len(s) <= sparkPoints {
		return s
	}
	out := make(series, sparkPoints)
	for i := range out {
		lo, hi := i*len(s)/sparkPoints, (i+1)*len(s)/sparkPoints
		out[i] = math.NaN()
		for _, v := range s[lo:hi] {
			if !math.IsNaN(v) && (math.IsNaN(out[i]) || v > out[i]) {
				out[i] = v
			}
		}
	}
	return out
}

// peak is the largest known value across the series, the top of a shared
// scale. It is 0 when nothing is known or everything is 0.
func peak(all ...series) float64 {
	top := 0.0
	for _, s := range all {
		for _, v := range s {
			if v > top {
				top = v
			}
		}
	}
	return top
}

// sparkPath draws s as an SVG path, scaled so that top is the top of the box.
// Points are spread evenly across the width, the newest at the right edge. A
// gap lifts the pen, and a point with gaps on both sides becomes a dot (a
// zero-length segment, which round line caps draw). No known values gives "".
func sparkPath(s series, top float64) string {
	if top <= 0 {
		top = 1 // all zeros: a flat line along the bottom
	}
	var b []byte
	run := 0 // points in the current unbroken run
	for i, v := range s {
		if math.IsNaN(v) {
			if run == 1 {
				b = append(b, "h0"...)
			}
			run = 0
			continue
		}
		x := float64(sparkW)
		if len(s) > 1 {
			x = float64(i) * sparkW / float64(len(s)-1)
		}
		y := sparkPad + (1-min(max(v/top, 0), 1))*(sparkH-2*sparkPad)
		if run == 0 {
			b = append(b, 'M')
		} else {
			b = append(b, 'L')
		}
		b = appendCoord(b, x)
		b = append(b, ' ')
		b = appendCoord(b, y)
		run++
	}
	if run == 1 {
		b = append(b, "h0"...)
	}
	return string(b)
}

// appendCoord writes a coordinate to one decimal place, which is finer than a
// pixel at any size the dashboard draws, without trailing zeros.
func appendCoord(b []byte, v float64) []byte {
	return strconv.AppendFloat(b, math.Round(v*10)/10, 'f', -1, 64)
}
