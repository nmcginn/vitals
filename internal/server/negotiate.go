package server

import (
	"errors"
	"strconv"
	"strings"
)

// format is a negotiated output format. Its value is what ?format= takes.
type format string

const (
	formatHTML format = "html"
	formatJSON format = "json"
	formatText format = "text"
	formatProm format = "prom"
)

// offered is every format, in the order the ties of decision 36 fall back to
// after the endpoint's default: someone asking for text/* from a JSON endpoint
// is more likely a terminal than a browser.
var offered = []format{formatText, formatHTML, formatJSON, formatProm}

// errNotAcceptable is the 406 body: what failed, and what would work instead.
var errNotAcceptable = errors.New("no supported format matches the request.\n" +
	"Ask with ?format=html, json, text, or prom, or send an Accept header naming\n" +
	"text/html, application/json, text/plain, or, for Prometheus,\n" +
	"text/plain; version=0.0.4 or application/openmetrics-text")

// negotiate picks the format for a negotiable endpoint, first match wins
// (PLAN): ?format=, then Accept with its q-values, then the endpoint's default.
// An empty ?format= is no format, like an empty ?since=.
//
// Each format takes the q of the most specific Accept range that matches it,
// so "text/html;q=0, */*" rules out HTML however broad the rest is. The
// highest q wins; a tie goes to the range listed first, and within one
// wildcard range to the default, then to the order in offered (decision 36).
func negotiate(query, accept string, def format) (format, error) {
	if query != "" {
		for _, f := range offered {
			if format(query) == f {
				return f, nil
			}
		}
		return "", errNotAcceptable
	}
	ranges := parseAccept(accept)
	if len(ranges) == 0 {
		return def, nil
	}
	type pick struct {
		f   format
		q   float64
		pos int // the winning range's place in the header
	}
	var best *pick
	for _, f := range append([]format{def}, offered...) {
		r, ok := mostSpecific(ranges, f)
		if !ok || r.q == 0 {
			continue
		}
		// Strictly better only, so the default and then offered's order keep
		// a tie.
		if best == nil || r.q > best.q || r.q == best.q && r.pos < best.pos {
			best = &pick{f, r.q, r.pos}
		}
	}
	if best == nil {
		return "", errNotAcceptable
	}
	return best.f, nil
}

// mediaRange is one entry of an Accept header, lowercased.
type mediaRange struct {
	typ, sub string
	version  string // text/plain's version parameter, which marks Prometheus
	q        float64
	pos      int
}

// parseAccept splits an Accept header into its ranges. A malformed range, or
// one with a q that isn't a number from 0 to 1, is dropped rather than
// guessed at; if nothing is left, the endpoint's default applies.
func parseAccept(h string) []mediaRange {
	var out []mediaRange
	for i, part := range strings.Split(h, ",") {
		params := strings.Split(part, ";")
		typ, sub, ok := strings.Cut(strings.ToLower(strings.TrimSpace(params[0])), "/")
		if !ok || typ == "" || sub == "" || typ == "*" && sub != "*" {
			continue
		}
		r := mediaRange{typ: typ, sub: sub, q: 1, pos: i}
		for _, p := range params[1:] {
			k, v, _ := strings.Cut(p, "=")
			k, v = strings.ToLower(strings.TrimSpace(k)), strings.Trim(strings.TrimSpace(v), `"`)
			switch k {
			case "q":
				q, err := strconv.ParseFloat(v, 64)
				if err != nil || q < 0 || q > 1 {
					ok = false
				}
				r.q = q
			case "version":
				r.version = v
			}
		}
		if ok {
			out = append(out, r)
		}
	}
	return out
}

// mostSpecific is the range that decides f's q: an exact type beats type/*,
// which beats */*, and the first of equals wins. Prometheus is reached only by
// naming it, never through a wildcard, and plain text/plain never reaches it.
func mostSpecific(ranges []mediaRange, f format) (mediaRange, bool) {
	var best mediaRange
	rank := 0
	for _, r := range ranges {
		var n int
		switch {
		case r.typ == "*":
			n = 1
		case r.sub == "*":
			if f.mediaType() == r.typ {
				n = 2
			}
		case f.matches(r):
			n = 3
		}
		if f == formatProm && n < 3 {
			n = 0
		}
		if n > rank {
			best, rank = r, n
		}
	}
	return best, rank > 0
}

// mediaType is the top-level type of f's Content-Type.
func (f format) mediaType() string {
	if f == formatJSON {
		return "application"
	}
	return "text"
}

// matches reports whether an exact (non-wildcard) range names f.
func (f format) matches(r mediaRange) bool {
	switch f {
	case formatHTML:
		return r.typ == "text" && r.sub == "html"
	case formatJSON:
		return r.typ == "application" && r.sub == "json"
	case formatText:
		return r.typ == "text" && r.sub == "plain" && r.version != "0.0.4"
	case formatProm:
		return r.typ == "text" && r.sub == "plain" && r.version == "0.0.4" ||
			r.typ == "application" && r.sub == "openmetrics-text"
	}
	return false
}
