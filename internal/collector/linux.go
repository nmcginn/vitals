//go:build linux

package collector

import (
	"os"
	"syscall"
)

// New returns the collector for this OS.
func New(opts Options) Collector { return newProcfs(os.DirFS("/"), statfs, opts) }

// statfs counts in fragments, as statvfs and df do; older kernels leave the
// fragment size 0, and then it's the block size.
func statfs(mount string) (total, avail uint64, err error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(mount, &st); err != nil {
		return 0, 0, err
	}
	size := uint64(st.Frsize)
	if size == 0 {
		size = uint64(st.Bsize)
	}
	return st.Blocks * size, st.Bavail * size, nil
}
