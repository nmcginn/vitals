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

// More interfaces than any Mac has, even with a dozen VPN tunnels.
#define VITALS_MAX_IFACES 128

// One interface's name and 64-bit counters, from its IFMIB_IFDATA record.
struct vitals_iface {
	char name[16];
	uint64_t ibytes;
	uint64_t obytes;
	uint64_t ipackets;
	uint64_t opackets;
};

// More mounts than a Mac has, counting every /System/Volumes volume.
#define VITALS_MAX_MOUNTS 64

// One mount's statfs record: names, and sizes in bsize blocks.
struct vitals_mount {
	char mount[1024];  // f_mntonname
	char device[1024]; // f_mntfromname, e.g. "/dev/disk3s1s1"
	char fs[16];       // f_fstypename, e.g. "apfs"
	uint32_t flags;    // f_flags; MNT_LOCAL is set for local filesystems
	uint32_t bsize;
	uint64_t blocks;
	uint64_t bavail;
};

// The byte counters of the physical disk behind a BSD device.
struct vitals_disk_io {
	char device[32]; // the whole disk's BSD name, e.g. "disk0"
	uint64_t read_bytes;
	uint64_t write_bytes;
};

// More GPUs than any Mac has; an Apple Silicon Mac has one IOAccelerator.
#define VITALS_MAX_GPUS 8

// One IOAccelerator's utilization, from its "PerformanceStatistics" registry
// property.
struct vitals_gpu {
	int32_t has_stats; // 0 when the entry has no PerformanceStatistics dictionary
	int32_t has_util;  // 0 when the dictionary has no "Device Utilization %" number
	double util;       // "Device Utilization %", when has_util is 1
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

// Copies up to max interfaces' counters into out. Returns the number of
// interfaces, which may exceed max, -1 if the IFMIB_IFCOUNT sysctl failed, or
// -2 if an interface's IFMIB_IFDATA sysctl failed other than for a gap.
int vitals_net(struct vitals_iface *out, int max);

// Copies up to max mounts from getfsstat(MNT_NOWAIT), whose sizes are the
// kernel's cached figures, into out. Returns the number of mounts, which may
// exceed max, or -1 if getfsstat failed.
int vitals_mounts(struct vitals_mount *out, int max);

// Refreshes m's sizes with statfs(m->mount). Returns 0, or -1 if statfs failed,
// leaving m as it was.
int vitals_statfs(struct vitals_mount *m);

// Finds the IOBlockStorageDriver above the BSD device bsd_name ("disk3s1s1")
// and copies its byte counters. Returns 0, -1 if no IOMedia has that name, -2
// if no IOBlockStorageDriver is above it, or -3 if the driver has no
// "Statistics" byte counters, as on some virtual disks.
int vitals_disk_io(const char *bsd_name, struct vitals_disk_io *out);

// Copies up to max IOAccelerator services' utilization into out. Returns the
// number of services, which may exceed max, or -1 if matching them failed.
int vitals_gpus(struct vitals_gpu *out, int max);

#endif
