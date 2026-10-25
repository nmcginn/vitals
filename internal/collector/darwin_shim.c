//go:build darwin

// Excluded elsewhere so the untagged Go files in this package build on Linux.

#include "darwin_shim.h"

#include <CoreFoundation/CoreFoundation.h>
#include <IOKit/IOKitLib.h>
#include <mach/mach.h>
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

static int32_t i32(const char *name) {
	int32_t v = 0;
	size_t len = sizeof v;
	if (sysctlbyname(name, &v, &len, NULL, 0) != 0) {
		return 0;
	}
	return v;
}

// hw.pagesize is an int on some kernels and a quad on others; starting from 0,
// a 4-byte copy into the low half of a little-endian uint64_t is the same value.
static int u64(const char *name, uint64_t *out) {
	*out = 0;
	size_t len = sizeof *out;
	return sysctlbyname(name, out, &len, NULL, 0);
}

void vitals_host_static(struct vitals_host_static *out) {
	str("kern.osproductversion", out->os_version, sizeof out->os_version);
	str("kern.osrelease", out->kernel, sizeof out->kernel);
	str("hw.machine", out->machine, sizeof out->machine);
	str("machdep.cpu.brand_string", out->cpu_model, sizeof out->cpu_model);
	out->nperflevels = i32("hw.nperflevels");
	out->perflevel_logical[0] = i32("hw.perflevel0.logicalcpu");
	out->perflevel_logical[1] = i32("hw.perflevel1.logicalcpu");
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

int vitals_cpu_load(struct vitals_cpu_load *out, int max) {
	// Each mach_host_self() adds a reference to the send right, and a
	// reference count that only grows overflows after weeks of 2s ticks.
	mach_port_t host = mach_host_self();
	natural_t ncpu = 0;
	processor_info_array_t info = NULL;
	mach_msg_type_number_t count = 0;
	kern_return_t kr = host_processor_info(host, PROCESSOR_CPU_LOAD_INFO, &ncpu, &info, &count);
	mach_port_deallocate(mach_task_self(), host);
	if (kr != KERN_SUCCESS) {
		return -1;
	}
	processor_cpu_load_info_t load = (processor_cpu_load_info_t)info;
	for (natural_t i = 0; i < ncpu && i < (natural_t)max; i++) {
		for (int s = 0; s < CPU_STATE_MAX; s++) {
			out[i].ticks[s] = load[i].cpu_ticks[s];
		}
	}
	// The kernel mapped this array into our address space; it's ours to free.
	vm_deallocate(mach_task_self(), (vm_address_t)info, count * sizeof *info);
	return (int)ncpu;
}

int vitals_cpu_nodes(struct vitals_cpu_node *out, int max) {
	io_registry_entry_t cpus = IORegistryEntryFromPath(kIOMainPortDefault, "IODeviceTree:/cpus");
	if (cpus == MACH_PORT_NULL) {
		return -1;
	}
	io_iterator_t it = 0;
	kern_return_t kr = IORegistryEntryGetChildIterator(cpus, kIODeviceTreePlane, &it);
	IOObjectRelease(cpus);
	if (kr != KERN_SUCCESS) {
		return -1;
	}
	int n = 0;
	io_registry_entry_t cpu;
	while ((cpu = IOIteratorNext(it)) != 0) {
		if (n < max) {
			out[n].logical_id = -1;
			out[n].cluster_type = 0;
			// The type checks keep a property of an unexpected type from being
			// read as the wrong struct.
			CFTypeRef id = IORegistryEntryCreateCFProperty(cpu, CFSTR("logical-cpu-id"), kCFAllocatorDefault, 0);
			if (id != NULL) {
				if (CFGetTypeID(id) == CFNumberGetTypeID()) {
					CFNumberGetValue((CFNumberRef)id, kCFNumberSInt32Type, &out[n].logical_id);
				}
				CFRelease(id);
			}
			CFTypeRef kind = IORegistryEntryCreateCFProperty(cpu, CFSTR("cluster-type"), kCFAllocatorDefault, 0);
			if (kind != NULL) {
				if (CFGetTypeID(kind) == CFDataGetTypeID() && CFDataGetLength((CFDataRef)kind) > 0) {
					out[n].cluster_type = (char)CFDataGetBytePtr((CFDataRef)kind)[0];
				}
				CFRelease(kind);
			}
		}
		n++;
		IOObjectRelease(cpu);
	}
	IOObjectRelease(it);
	return n;
}

int vitals_memory(struct vitals_memory *out) {
	if (u64("hw.memsize", &out->memsize) != 0) {
		return -1;
	}
	if (u64("hw.pagesize", &out->pagesize) != 0) {
		return -2;
	}
	// As in vitals_cpu_load, the host port reference is dropped every tick.
	mach_port_t host = mach_host_self();
	vm_statistics64_data_t vm;
	mach_msg_type_number_t count = HOST_VM_INFO64_COUNT;
	kern_return_t kr = host_statistics64(host, HOST_VM_INFO64, (host_info64_t)&vm, &count);
	mach_port_deallocate(mach_task_self(), host);
	if (kr != KERN_SUCCESS) {
		return -3;
	}
	out->active_count = vm.active_count;
	out->inactive_count = vm.inactive_count;
	out->wire_count = vm.wire_count;
	out->purgeable_count = vm.purgeable_count;
	out->compressor_page_count = vm.compressor_page_count;
	out->internal_page_count = vm.internal_page_count;
	out->external_page_count = vm.external_page_count;

	struct xsw_usage swap;
	size_t len = sizeof swap;
	if (sysctlbyname("vm.swapusage", &swap, &len, NULL, 0) != 0) {
		return -4;
	}
	out->swap_total = swap.xsu_total;
	out->swap_used = swap.xsu_used;

	int level = 0;
	len = sizeof level;
	out->pressure_ok = sysctlbyname("kern.memorystatus_vm_pressure_level", &level, &len, NULL, 0) == 0;
	out->pressure_level = level;
	return 0;
}
