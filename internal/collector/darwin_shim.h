#ifndef VITALS_DARWIN_SHIM_H
#define VITALS_DARWIN_SHIM_H

#include <stdint.h>

// Read once at startup. Each string is "" and each number 0 when its sysctl
// failed.
struct vitals_host_static {
	char os_version[32];          // kern.osproductversion, e.g. "26.1"
	char kernel[32];              // kern.osrelease, e.g. "25.1.0"
	char machine[32];             // hw.machine, e.g. "arm64"
	char cpu_model[64];           // machdep.cpu.brand_string, e.g. "Apple M4"
	int32_t nperflevels;          // hw.nperflevels
	int32_t perflevel_logical[2]; // hw.perflevel0.logicalcpu, hw.perflevel1.logicalcpu
};

// More logical CPUs than any Mac has; the buffers below are this long.
#define VITALS_MAX_CPUS 256

// One core's cumulative ticks from host_processor_info, indexed by
// CPU_STATE_USER, CPU_STATE_SYSTEM, CPU_STATE_IDLE, and CPU_STATE_NICE.
struct vitals_cpu_load {
	uint32_t ticks[4];
};

// One child of IODeviceTree:/cpus.
struct vitals_cpu_node {
	int32_t logical_id; // "logical-cpu-id", or -1 when missing
	char cluster_type;  // first byte of "cluster-type" ('P' or 'E'), or 0 when missing
};

// The host group's gauges, copied from kern.boottime and vm.loadavg.
struct vitals_host {
	int64_t boot_sec;
	int64_t boot_usec;
	uint32_t ldavg[3]; // fixed point: divide by fscale
	int64_t fscale;
};

// The memory group: hw.memsize, hw.pagesize, the host_statistics64
// (HOST_VM_INFO64) page counts that Activity Monitor's figures are built from,
// vm.swapusage, and kern.memorystatus_vm_pressure_level.
struct vitals_memory {
	uint64_t memsize;
	uint64_t pagesize;
	uint32_t active_count;
	uint32_t inactive_count;
	uint32_t wire_count;
	uint32_t purgeable_count;
	uint32_t compressor_page_count;
	uint32_t internal_page_count;
	uint32_t external_page_count;
	uint64_t swap_total;
	uint64_t swap_used;
	int32_t pressure_level;
	int32_t pressure_ok; // 0 when the pressure sysctl failed
};

void vitals_host_static(struct vitals_host_static *out);

// Returns 0 on success, -1 if kern.boottime failed, -2 if vm.loadavg failed.
int vitals_host(struct vitals_host *out);

// Copies up to max cores' tick counters into out. Returns the number of cores
// the kernel reported, which may exceed max, or -1 if host_processor_info
// failed.
int vitals_cpu_load(struct vitals_cpu_load *out, int max);

// Copies up to max children of IODeviceTree:/cpus into out. Returns the number
// of children, which may exceed max, or -1 if the registry has no /cpus.
int vitals_cpu_nodes(struct vitals_cpu_node *out, int max);

// Returns 0 on success, -1 if hw.memsize failed, -2 if hw.pagesize failed, -3
// if host_statistics64 failed, or -4 if vm.swapusage failed. A failed pressure
// sysctl is not a failure here: it sets pressure_ok to 0.
int vitals_memory(struct vitals_memory *out);

#endif
