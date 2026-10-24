//go:build darwin

// Excluded elsewhere so the untagged Go files in this package build on Linux.

#include "darwin_shim.h"

#include <sys/sysctl.h>
#include <sys/time.h>

static void str(const char *name, char *buf, size_t size) {
	size_t len = size;
	if (sysctlbyname(name, buf, &len, NULL, 0) != 0) {
		buf[0] = '\0';
	}
	// C.GoString reads to the first NUL, so one must be inside the buffer
	// whatever the sysctl handler copied out.
	buf[size - 1] = '\0';
}

void vitals_host_static(struct vitals_host_static *out) {
	str("kern.osproductversion", out->os_version, sizeof out->os_version);
	str("kern.osrelease", out->kernel, sizeof out->kernel);
	str("hw.machine", out->machine, sizeof out->machine);
}

int vitals_host(struct vitals_host *out) {
	struct timeval tv;
	size_t len = sizeof tv;
	if (sysctlbyname("kern.boottime", &tv, &len, NULL, 0) != 0) {
		return -1;
	}
	struct loadavg la;
	len = sizeof la;
	if (sysctlbyname("vm.loadavg", &la, &len, NULL, 0) != 0) {
		return -2;
	}
	out->boot_sec = tv.tv_sec;
	out->boot_usec = tv.tv_usec;
	for (int i = 0; i < 3; i++) {
		out->ldavg[i] = la.ldavg[i];
	}
	out->fscale = la.fscale;
	return 0;
}
