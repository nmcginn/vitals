//go:build darwin

// Excluded elsewhere so the untagged Go files in this package build on Linux.

#include "darwin_shim.h"

#include <sys/sysctl.h>

int vitals_hw_machine(char *buf, size_t len) {
	return sysctlbyname("hw.machine", buf, &len, NULL, 0) == 0 ? 0 : -1;
}
