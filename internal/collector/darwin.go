//go:build darwin

package collector

/*
#cgo LDFLAGS: -framework IOKit -framework CoreFoundation
#include "darwin_shim.h"
*/
import "C"

import (
	"errors"
	"unsafe"
)

// Machine returns the hw.machine sysctl. M0 uses it to prove the cgo link line
// (IOKit and CoreFoundation included) before any real collector depends on it.
func Machine() (string, error) {
	var buf [64]C.char
	if C.vitals_hw_machine(&buf[0], C.size_t(len(buf))) != 0 {
		return "", errors.New("reading sysctl hw.machine failed; is this macOS?")
	}
	return C.GoString((*C.char)(unsafe.Pointer(&buf[0]))), nil
}
