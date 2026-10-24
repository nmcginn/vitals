// Package render turns snapshots into bytes, one pure function per format. No
// clock, no store, no net/http: same input, same bytes.
package render

import (
	"encoding/json"

	"github.com/nmcginn/vitals/internal/sampler"
)

// JSON renders a snapshot in the shape PLAN documents. The field names live in
// the snapshot's struct tags; testdata/*.json pins them. pretty indents by two
// spaces for ?pretty=1. Output ends with a newline either way, for curl.
func JSON(s *sampler.Snapshot, pretty bool) ([]byte, error) {
	return marshal(s, pretty)
}

// History renders snapshots, oldest first, as a JSON array of the same shape.
// No snapshots is [], not null.
func History(h []*sampler.Snapshot, pretty bool) ([]byte, error) {
	if h == nil {
		h = []*sampler.Snapshot{}
	}
	return marshal(h, pretty)
}

func marshal(v any, pretty bool) ([]byte, error) {
	var b []byte
	var err error
	if pretty {
		b, err = json.MarshalIndent(v, "", "  ")
	} else {
		b, err = json.Marshal(v)
	}
	if err != nil {
		return nil, err
	}
	return append(b, '\n'), nil
}
