//go:build linux

package collector

import "os"

// New returns the collector for this OS.
func New() Collector { return newProcfs(os.DirFS("/")) }
