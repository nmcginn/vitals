package collector

import "regexp"

// Options are the collector's settings from flags. A nil regex means the
// built-in list; a user's regex replaces that list rather than adding to it.
type Options struct {
	NetExclude *regexp.Regexp
}

// defaultNetExclude is PLAN's list: loopback, container and VM plumbing, VPN
// tunnels, and Apple's peer-to-peer and internal interfaces.
var defaultNetExclude = regexp.MustCompile(`^(lo|lo0|veth.*|docker.*|br-.*|utun.*|awdl.*|llw.*|bridge.*|anpi.*)$`)

// keepIfaces drops excluded interfaces in place. The result is never nil, so
// an all-filtered machine reports "network": [] rather than a failed group.
func keepIfaces(ifs []Interface, exclude *regexp.Regexp) []Interface {
	if exclude == nil {
		exclude = defaultNetExclude
	}
	kept := ifs[:0]
	if kept == nil {
		kept = []Interface{}
	}
	for _, i := range ifs {
		if !exclude.MatchString(i.Name) {
			kept = append(kept, i)
		}
	}
	return kept
}
