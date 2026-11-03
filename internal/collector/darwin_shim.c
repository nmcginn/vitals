//go:build darwin

// Excluded elsewhere so the untagged Go files in this package build on Linux.

#include "darwin_shim.h"

#include <CoreFoundation/CoreFoundation.h>
#include <IOKit/IOBSD.h>
#include <IOKit/IOKitLib.h>
#include <IOKit/storage/IOBlockStorageDriver.h>
#include <mach/mach.h>
#include <dlfcn.h>
#include <errno.h>
#include <net/if.h>
#include <net/if_mib.h>
#include <stdlib.h>
#include <string.h>
#include <sys/mount.h>
#include <sys/socket.h>
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

// IFMIB_IFDATA rather than NET_RT_IFLIST2 or getifaddrs: on macOS 26 those two
// give unprivileged callers byte counts wrapped at 2^32 and rounded to 1 KiB,
// while this gives the same 64-bit counts as netstat (decision 24). Indexes
// run from 1 to IFMIB_IFCOUNT, and a removed interface leaves a gap that
// answers ENOENT.
int vitals_net(struct vitals_iface *out, int max) {
	int count_mib[5] = {CTL_NET, PF_LINK, NETLINK_GENERIC, IFMIB_SYSTEM, IFMIB_IFCOUNT};
	int count = 0;
	size_t len = sizeof count;
	if (sysctl(count_mib, 5, &count, &len, NULL, 0) != 0) {
		return -1;
	}
	int n = 0;
	for (int i = 1; i <= count; i++) {
		int mib[6] = {CTL_NET, PF_LINK, NETLINK_GENERIC, IFMIB_IFDATA, i, IFDATA_GENERAL};
		struct ifmibdata md;
		len = sizeof md;
		if (sysctl(mib, 6, &md, &len, NULL, 0) != 0) {
			if (errno == ENOENT) {
				continue;
			}
			return -2;
		}
		if (n < max) {
			memcpy(out[n].name, md.ifmd_name, sizeof out[n].name);
			out[n].name[sizeof out[n].name - 1] = '\0';
			out[n].ibytes = md.ifmd_data.ifi_ibytes;
			out[n].obytes = md.ifmd_data.ifi_obytes;
			out[n].ipackets = md.ifmd_data.ifi_ipackets;
			out[n].opackets = md.ifmd_data.ifi_opackets;
		}
		n++;
	}
	return n;
}

static void copy_statfs(struct vitals_mount *out, const struct statfs *st) {
	out->flags = st->f_flags;
	out->bsize = st->f_bsize;
	out->blocks = st->f_blocks;
	out->bavail = st->f_bavail;
}

static void copy_str(char *dst, size_t size, const char *src) {
	strncpy(dst, src, size - 1);
	dst[size - 1] = '\0';
}

int vitals_mounts(struct vitals_mount *out, int max) {
	int n = getfsstat(NULL, 0, MNT_NOWAIT);
	if (n < 0) {
		return -1;
	}
	// Room for a few mounts that appear between the two calls; getfsstat
	// fills what fits and returns how many it wrote.
	int room = n + 4;
	struct statfs *buf = malloc((size_t)room * sizeof *buf);
	if (buf == NULL) {
		return -1;
	}
	n = getfsstat(buf, room * (int)sizeof *buf, MNT_NOWAIT);
	for (int i = 0; i < n && i < max; i++) {
		copy_str(out[i].mount, sizeof out[i].mount, buf[i].f_mntonname);
		copy_str(out[i].device, sizeof out[i].device, buf[i].f_mntfromname);
		copy_str(out[i].fs, sizeof out[i].fs, buf[i].f_fstypename);
		copy_statfs(&out[i], &buf[i]);
	}
	free(buf);
	return n;
}

int vitals_statfs(struct vitals_mount *m) {
	struct statfs st;
	if (statfs(m->mount, &st) != 0) {
		return -1;
	}
	copy_statfs(m, &st);
	return 0;
}

static int i64(CFDictionaryRef dict, CFStringRef key, uint64_t *out) {
	CFTypeRef v = CFDictionaryGetValue(dict, key);
	if (v == NULL || CFGetTypeID(v) != CFNumberGetTypeID()) {
		return -1;
	}
	int64_t n = 0;
	CFNumberGetValue((CFNumberRef)v, kCFNumberSInt64Type, &n);
	*out = (uint64_t)n;
	return 0;
}

// The driver's own counters, and the BSD name of the whole-disk IOMedia it
// publishes (media, the entry just below it).
static int copy_disk_io(io_registry_entry_t driver, io_registry_entry_t media, struct vitals_disk_io *out) {
	out->device[0] = '\0';
	CFTypeRef name = IORegistryEntryCreateCFProperty(media, CFSTR(kIOBSDNameKey), kCFAllocatorDefault, 0);
	if (name != NULL) {
		if (CFGetTypeID(name) != CFStringGetTypeID() ||
		    !CFStringGetCString((CFStringRef)name, out->device, sizeof out->device, kCFStringEncodingUTF8)) {
			out->device[0] = '\0';
		}
		CFRelease(name);
	}
	CFTypeRef stats = IORegistryEntryCreateCFProperty(driver, CFSTR(kIOBlockStorageDriverStatisticsKey), kCFAllocatorDefault, 0);
	if (stats == NULL) {
		return -3;
	}
	int rc = -3;
	if (CFGetTypeID(stats) == CFDictionaryGetTypeID() &&
	    i64((CFDictionaryRef)stats, CFSTR(kIOBlockStorageDriverStatisticsBytesReadKey), &out->read_bytes) == 0 &&
	    i64((CFDictionaryRef)stats, CFSTR(kIOBlockStorageDriverStatisticsBytesWrittenKey), &out->write_bytes) == 0) {
		rc = 0;
	}
	CFRelease(stats);
	return rc;
}

// An APFS volume's IOMedia sits under its container, which sits on a
// partition of the physical disk, so the walk up the service plane passes
// those to reach the disk's IOBlockStorageDriver (decision 7).
int vitals_disk_io(const char *bsd_name, struct vitals_disk_io *out) {
	// IOServiceGetMatchingService consumes the matching dictionary.
	io_registry_entry_t child = IOServiceGetMatchingService(kIOMainPortDefault, IOBSDNameMatching(kIOMainPortDefault, 0, bsd_name));
	if (child == 0) {
		return -1;
	}
	int rc = -2;
	io_registry_entry_t parent = 0;
	while (IORegistryEntryGetParentEntry(child, kIOServicePlane, &parent) == KERN_SUCCESS) {
		if (IOObjectConformsTo(parent, kIOBlockStorageDriverClass)) {
			rc = copy_disk_io(parent, child, out);
			IOObjectRelease(parent);
			break;
		}
		IOObjectRelease(child);
		child = parent;
	}
	IOObjectRelease(child);
	return rc;
}

static void copy_gpu(io_registry_entry_t entry, struct vitals_gpu *out) {
	out->has_stats = 0;
	out->has_util = 0;
	out->util = 0;
	CFTypeRef stats = IORegistryEntryCreateCFProperty(entry, CFSTR("PerformanceStatistics"), kCFAllocatorDefault, 0);
	if (stats == NULL) {
		return;
	}
	if (CFGetTypeID(stats) == CFDictionaryGetTypeID()) {
		out->has_stats = 1;
		CFTypeRef v = CFDictionaryGetValue((CFDictionaryRef)stats, CFSTR("Device Utilization %"));
		if (v != NULL && CFGetTypeID(v) == CFNumberGetTypeID()) {
			CFNumberGetValue((CFNumberRef)v, kCFNumberDoubleType, &out->util);
			out->has_util = 1;
		}
	}
	CFRelease(stats);
}

// The driver publishes its counters on the accelerator's own registry entry,
// which any user can read (decision 46).
int vitals_gpus(struct vitals_gpu *out, int max) {
	io_iterator_t iter = 0;
	// IOServiceGetMatchingServices consumes the matching dictionary.
	if (IOServiceGetMatchingServices(kIOMainPortDefault, IOServiceMatching("IOAccelerator"), &iter) != KERN_SUCCESS) {
		return -1;
	}
	int n = 0;
	io_registry_entry_t entry;
	while ((entry = IOIteratorNext(iter)) != 0) {
		if (n < max) {
			copy_gpu(entry, &out[n]);
		}
		IOObjectRelease(entry);
		n++;
	}
	IOObjectRelease(iter);
	return n;
}

// The private IOHID event system, the one route to the SoC's temperature
// sensors without root (decision 44). No header declares these, so the
// prototypes are written out here, every object as a CFTypeRef. They are
// resolved once with dlsym from the IOKit already linked, so a macOS that
// drops one fails the temps group instead of the whole binary at launch.
typedef CFTypeRef (*hid_client_create_fn)(CFAllocatorRef);
typedef int (*hid_client_set_matching_fn)(CFTypeRef client, CFDictionaryRef matching);
typedef CFArrayRef (*hid_client_copy_services_fn)(CFTypeRef client);
typedef CFTypeRef (*hid_service_copy_property_fn)(CFTypeRef service, CFStringRef key);
typedef CFTypeRef (*hid_service_copy_event_fn)(CFTypeRef service, int64_t type, int32_t options, int64_t timestamp);
typedef double (*hid_event_get_float_fn)(CFTypeRef event, uint32_t field);

// kIOHIDEventTypeTemperature, and its value field, IOHIDEventFieldBase(type).
#define HID_EVENT_TEMPERATURE 15
#define HID_FIELD_TEMPERATURE ((uint32_t)HID_EVENT_TEMPERATURE << 16)
// kHIDPage_AppleVendor and kHIDUsage_AppleVendor_TemperatureSensor.
#define HID_PAGE_APPLE_VENDOR 0xff00
#define HID_USAGE_TEMPERATURE 5

static struct {
	hid_client_set_matching_fn set_matching;
	hid_client_copy_services_fn copy_services;
	hid_service_copy_property_fn copy_property;
	hid_service_copy_event_fn copy_event;
	hid_event_get_float_fn get_float;
	CFTypeRef client;
} hid;

// ISO C has no cast from dlsym's void * to a function pointer; copying the
// bytes is the form POSIX blesses.
static int resolve(void *lib, const char *name, void *fn, size_t size, char *missing, int missing_size) {
	void *sym = dlsym(lib, name);
	if (sym == NULL) {
		copy_str(missing, (size_t)missing_size, name);
		return -2;
	}
	memcpy(fn, &sym, size);
	return 0;
}

int vitals_temps_open(char *missing, int size) {
	missing[0] = '\0';
	void *lib = dlopen("/System/Library/Frameworks/IOKit.framework/IOKit", RTLD_LAZY | RTLD_LOCAL);
	if (lib == NULL) {
		return -1;
	}
	hid_client_create_fn create = NULL;
	if (resolve(lib, "IOHIDEventSystemClientCreate", &create, sizeof create, missing, size) != 0 ||
	    resolve(lib, "IOHIDEventSystemClientSetMatching", &hid.set_matching, sizeof hid.set_matching, missing, size) != 0 ||
	    resolve(lib, "IOHIDEventSystemClientCopyServices", &hid.copy_services, sizeof hid.copy_services, missing, size) != 0 ||
	    resolve(lib, "IOHIDServiceClientCopyProperty", &hid.copy_property, sizeof hid.copy_property, missing, size) != 0 ||
	    resolve(lib, "IOHIDServiceClientCopyEvent", &hid.copy_event, sizeof hid.copy_event, missing, size) != 0 ||
	    resolve(lib, "IOHIDEventGetFloatValue", &hid.get_float, sizeof hid.get_float, missing, size) != 0) {
		return -2;
	}
	// The handle stays open: the function pointers live as long as it does.
	CFTypeRef client = create(kCFAllocatorDefault);
	if (client == NULL) {
		return -3;
	}
	int page = HID_PAGE_APPLE_VENDOR, usage = HID_USAGE_TEMPERATURE;
	CFNumberRef page_num = CFNumberCreate(kCFAllocatorDefault, kCFNumberIntType, &page);
	CFNumberRef usage_num = CFNumberCreate(kCFAllocatorDefault, kCFNumberIntType, &usage);
	CFDictionaryRef matching = NULL;
	if (page_num != NULL && usage_num != NULL) {
		const void *keys[2] = {CFSTR("PrimaryUsagePage"), CFSTR("PrimaryUsage")};
		const void *values[2] = {page_num, usage_num};
		matching = CFDictionaryCreate(kCFAllocatorDefault, keys, values, 2, &kCFTypeDictionaryKeyCallBacks, &kCFTypeDictionaryValueCallBacks);
	}
	if (page_num != NULL) {
		CFRelease(page_num);
	}
	if (usage_num != NULL) {
		CFRelease(usage_num);
	}
	if (matching == NULL) {
		CFRelease(client);
		return -3;
	}
	hid.set_matching(client, matching);
	CFRelease(matching);
	hid.client = client;
	return 0;
}

static void copy_temp(CFTypeRef service, struct vitals_temp *out) {
	out->name[0] = '\0';
	out->has_event = 0;
	out->celsius = 0;
	CFTypeRef name = hid.copy_property(service, CFSTR("Product"));
	if (name != NULL) {
		if (CFGetTypeID(name) != CFStringGetTypeID() ||
		    !CFStringGetCString((CFStringRef)name, out->name, sizeof out->name, kCFStringEncodingUTF8)) {
			out->name[0] = '\0';
		}
		CFRelease(name);
	}
	CFTypeRef event = hid.copy_event(service, HID_EVENT_TEMPERATURE, 0, 0);
	if (event != NULL) {
		out->celsius = hid.get_float(event, HID_FIELD_TEMPERATURE);
		out->has_event = 1;
		CFRelease(event);
	}
}

int vitals_temps(struct vitals_temp *out, int max) {
	CFArrayRef services = hid.copy_services(hid.client);
	if (services == NULL) {
		return 0;
	}
	int n = (int)CFArrayGetCount(services);
	for (int i = 0; i < n && i < max; i++) {
		copy_temp(CFArrayGetValueAtIndex(services, i), &out[i]);
	}
	CFRelease(services);
	return n;
}

// Private IOReport, the one route to the SoC's energy counters without root
// (decision 44). No header declares it, so the prototypes are written out
// here as macmon declares them, every object as a CFTypeRef or dictionary. It
// is resolved once with dlsym, never linked, so a macOS that drops a function
// fails the power group instead of the whole binary at launch.
typedef CFDictionaryRef (*ior_copy_channels_in_group_fn)(CFStringRef group, CFStringRef subgroup, uint64_t a, uint64_t b, uint64_t c);
typedef CFTypeRef (*ior_create_subscription_fn)(const void *a, CFMutableDictionaryRef channels, CFMutableDictionaryRef *subscribed, uint64_t id, CFTypeRef b);
typedef CFDictionaryRef (*ior_create_samples_fn)(CFTypeRef subscription, CFMutableDictionaryRef channels, CFTypeRef a);
typedef CFStringRef (*ior_channel_string_fn)(CFDictionaryRef channel);
typedef int64_t (*ior_simple_get_integer_fn)(CFDictionaryRef channel, int32_t index);

static struct {
	ior_create_samples_fn create_samples;
	ior_channel_string_fn get_group;
	ior_channel_string_fn get_name;
	ior_channel_string_fn get_unit;
	ior_simple_get_integer_fn get_integer;
	CFTypeRef subscription;
	CFMutableDictionaryRef channels;
} ior;

int vitals_power_open(char *missing, int size) {
	missing[0] = '\0';
	void *lib = dlopen("/usr/lib/libIOReport.dylib", RTLD_LAZY | RTLD_LOCAL);
	if (lib == NULL) {
		return -1;
	}
	ior_copy_channels_in_group_fn copy_channels = NULL;
	ior_create_subscription_fn create_subscription = NULL;
	if (resolve(lib, "IOReportCopyChannelsInGroup", &copy_channels, sizeof copy_channels, missing, size) != 0 ||
	    resolve(lib, "IOReportCreateSubscription", &create_subscription, sizeof create_subscription, missing, size) != 0 ||
	    resolve(lib, "IOReportCreateSamples", &ior.create_samples, sizeof ior.create_samples, missing, size) != 0 ||
	    resolve(lib, "IOReportChannelGetGroup", &ior.get_group, sizeof ior.get_group, missing, size) != 0 ||
	    resolve(lib, "IOReportChannelGetChannelName", &ior.get_name, sizeof ior.get_name, missing, size) != 0 ||
	    resolve(lib, "IOReportChannelGetUnitLabel", &ior.get_unit, sizeof ior.get_unit, missing, size) != 0 ||
	    resolve(lib, "IOReportSimpleGetIntegerValue", &ior.get_integer, sizeof ior.get_integer, missing, size) != 0) {
		return -2;
	}
	// The handle stays open: the function pointers live as long as it does.
	CFDictionaryRef found = copy_channels(CFSTR("Energy Model"), NULL, 0, 0, 0);
	if (found == NULL) {
		return -3;
	}
	// The subscription wants a mutable dictionary, and each sample is taken
	// against it, so the copy is kept for the life of the process.
	CFMutableDictionaryRef channels = CFDictionaryCreateMutableCopy(kCFAllocatorDefault, CFDictionaryGetCount(found), found);
	CFRelease(found);
	if (channels == NULL) {
		return -4;
	}
	// subscribed is the subscription's own copy of the channels. Like macmon,
	// nothing reads it, and it is created once, so it is left alone rather
	// than released on a guess about who owns it.
	CFMutableDictionaryRef subscribed = NULL;
	CFTypeRef subscription = create_subscription(NULL, channels, &subscribed, 0, NULL);
	if (subscription == NULL) {
		CFRelease(channels);
		return -4;
	}
	ior.subscription = subscription;
	ior.channels = channels;
	return 0;
}

// The channel accessors follow the get rule, so their strings aren't released.
static void copy_cfstr(CFStringRef s, char *buf, size_t size) {
	if (s == NULL || CFGetTypeID(s) != CFStringGetTypeID() ||
	    !CFStringGetCString(s, buf, (CFIndex)size, kCFStringEncodingUTF8)) {
		buf[0] = '\0';
	}
}

int vitals_power(struct vitals_energy *out, int max) {
	CFDictionaryRef sample = ior.create_samples(ior.subscription, ior.channels, NULL);
	if (sample == NULL) {
		return -1;
	}
	CFTypeRef items = CFDictionaryGetValue(sample, CFSTR("IOReportChannels"));
	if (items == NULL || CFGetTypeID(items) != CFArrayGetTypeID()) {
		CFRelease(sample);
		return -1;
	}
	int n = (int)CFArrayGetCount((CFArrayRef)items);
	for (int i = 0; i < n && i < max; i++) {
		CFDictionaryRef channel = (CFDictionaryRef)CFArrayGetValueAtIndex((CFArrayRef)items, i);
		copy_cfstr(ior.get_group(channel), out[i].group, sizeof out[i].group);
		copy_cfstr(ior.get_name(channel), out[i].name, sizeof out[i].name);
		copy_cfstr(ior.get_unit(channel), out[i].unit, sizeof out[i].unit);
		out[i].value = ior.get_integer(channel, 0);
	}
	CFRelease(sample);
	return n;
}
