package collector

import "regexp"

// Options are the collector's settings from flags. A nil regex means the
// built-in list; a user's regex replaces that list rather than adding to it.
type Options struct {
	DiskExclude *regexp.Regexp
	NetExclude  *regexp.Regexp
}

// defaultDiskExclude is PLAN's list, matched against the mount point and the
// fs type in turn: the kernel's pseudo filesystems, container layers, snap
// images, network filesystems (statfs blocks when the server is gone, and the
// sampler can't interrupt it), and on darwin devfs, autofs, nullfs, and the
// APFS volumes under /System/Volumes, which share the container that / already
// reports.
var defaultDiskExclude = regexp.MustCompile(`^(proc|sysfs|tmpfs|devtmpfs|devpts|ramfs|overlay|squashfs|cgroup.*|securityfs|pstore|bpf|tracefs|debugfs|configfs|fusectl|mqueue|hugetlbfs|autofs|binfmt_misc|rpc_pipefs|nsfs|efivarfs|selinuxfs|fuse\.lxcfs|fuse\.gvfsd-fuse|nfs|nfs4|cifs|smb3|fuse\.sshfs|9p|devfs|nullfs|/snap/.*|/System/Volumes/.*)$`)

// keepDisks drops excluded mounts, then keeps one mount per device: a bind
// mount, or a second btrfs subvolume, would otherwise show the same space
// twice. The shortest mount point stands for the device, as with df. The
// result is never nil, so filtering everything out gives "disks": [].
func keepDisks(ds []Disk, exclude *regexp.Regexp) []Disk {
	if exclude == nil {
		exclude = defaultDiskExclude
	}
	kept := []Disk{}
	at := map[string]int{}
	for _, d := range ds {
		if exclude.MatchString(d.Mount) || exclude.MatchString(d.FS) {
			continue
		}
		if i, ok := at[d.Device]; ok {
			if len(d.Mount) < len(kept[i].Mount) {
				kept[i] = d
			}
			continue
		}
		at[d.Device] = len(kept)
		kept = append(kept, d)
	}
	return kept
}

// defaultNetExclude is PLAN's list: loopback, container and VM plumbing, VPN
// tunnels, and Apple's peer-to-peer and internal interfaces.
var defaultNetExclude = regexp.MustCompile(`^(lo|lo0|veth.*|docker.*|br-.*|utun.*|awdl.*|llw.*|bridge.*|anpi.*)$`)

// keepIfaces drops excluded interfaces in place. The result is never nil, so
// an all-filtered machine reports "network": [] rather than a failed group.
func keepIfaces(ifs []Interface, exclude *regexp.Regexp) []Interface {
	if exclude == nil {
		exclude = defaultNetExclude
	}
	kept := ifs[:0]
	if kept == nil {
		kept = []Interface{}
	}
	for _, i := range ifs {
		if !exclude.MatchString(i.Name) {
			kept = append(kept, i)
		}
	}
	return kept
}
