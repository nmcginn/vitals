#ifndef VITALS_DARWIN_SHIM_H
#define VITALS_DARWIN_SHIM_H

#include <stdint.h>

// Read once at startup. Each string is "" when its sysctl failed.
struct vitals_host_static {
	char os_version[32]; // kern.osproductversion, e.g. "26.1"
	char kernel[32];     // kern.osrelease, e.g. "25.1.0"
	char machine[32];    // hw.machine, e.g. "arm64"
};

// The host group's gauges, copied from kern.boottime and vm.loadavg.
struct vitals_host {
	int64_t boot_sec;
	int64_t boot_usec;
	uint32_t ldavg[3]; // fixed point: divide by fscale
	int64_t fscale;
};

void vitals_host_static(struct vitals_host_static *out);

// Returns 0 on success, -1 if kern.boottime failed, -2 if vm.loadavg failed.
int vitals_host(struct vitals_host *out);

#endif
