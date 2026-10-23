#ifndef VITALS_DARWIN_SHIM_H
#define VITALS_DARWIN_SHIM_H

#include <stddef.h>

// Copies the hw.machine sysctl (e.g. "arm64") into buf. Returns 0 on success, -1 on failure.
int vitals_hw_machine(char *buf, size_t len);

#endif
