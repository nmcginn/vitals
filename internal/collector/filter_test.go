package collector

import (
	"regexp"
	"slices"
	"testing"
)

func TestKeepIfaces(t *testing.T) {
	ifaces := func(names ...string) []Interface {
		ifs := make([]Interface, len(names))
		for i, n := range names {
			ifs[i] = Interface{Name: n, RxBytes: uint64(i)}
		}
		return ifs
	}
	names := func(ifs []Interface) []string {
		n := []string{}
		for _, i := range ifs {
			n = append(n, i.Name)
		}
		return n
	}

	for _, name := range []string{"lo", "lo0", "veth3f2a1b0", "docker0", "br-5e1f0c2d9a7b", "utun4", "awdl0", "llw0", "bridge0", "anpi1"} {
		t.Run("the default list excludes "+name, func(t *testing.T) {
			if got := keepIfaces(ifaces(name), nil); len(got) != 0 {
				t.Errorf("kept %v", names(got))
			}
		})
	}

	// "xlo0" and "lo1" check that the default patterns are anchored and that
	// "lo" isn't a prefix match.
	for _, name := range []string{"en0", "eth0", "tailscale0", "wg-home", "enp3s0", "xlo0", "lo1"} {
		t.Run("the default list keeps "+name, func(t *testing.T) {
			if got := keepIfaces(ifaces(name), nil); len(got) != 1 {
				t.Errorf("dropped %s", name)
			}
		})
	}

	t.Run("a user's regex replaces the defaults: ^en1$ drops en1 and keeps lo0 and utun0", func(t *testing.T) {
		got := keepIfaces(ifaces("lo0", "en0", "en1", "utun0", "en10"), regexp.MustCompile(`^en1$`))
		if want := []string{"lo0", "en0", "utun0", "en10"}; !slices.Equal(names(got), want) {
			t.Errorf("kept %v, want %v", names(got), want)
		}
	})

	t.Run("kept interfaces keep their order and counters", func(t *testing.T) {
		got := keepIfaces(ifaces("lo0", "en0", "utun0", "en1"), nil)
		want := []Interface{{Name: "en0", RxBytes: 1}, {Name: "en1", RxBytes: 3}}
		if !slices.Equal(got, want) {
			t.Errorf("kept %+v, want %+v", got, want)
		}
	})

	t.Run("no interfaces at all is an empty list, not nil", func(t *testing.T) {
		if got := keepIfaces(nil, nil); got == nil || len(got) != 0 {
			t.Errorf("got %#v, want an empty, non-nil slice", got)
		}
	})
}

func TestKeepDisks(t *testing.T) {
	mounts := func(ds []Disk) []string {
		m := []string{}
		for _, d := range ds {
			m = append(m, d.Mount)
		}
		return m
	}

	for _, d := range []Disk{
		{Mount: "/snap/core22/1612", Device: "/dev/loop0", FS: "squashfs"},
		{Mount: "/snap/firefox/5091", Device: "/dev/loop1", FS: "ext4"},
		{Mount: "/var/lib/docker/overlay2/x/merged", Device: "overlay", FS: "overlay"},
		{Mount: "/run", Device: "tmpfs", FS: "tmpfs"},
		{Mount: "/sys/fs/cgroup", Device: "cgroup2", FS: "cgroup2"},
		{Mount: "/sys/fs/cgroup/cpu", Device: "cgroup", FS: "cgroup"},
		{Mount: "/proc", Device: "proc", FS: "proc"},
		{Mount: "/dev", Device: "devfs", FS: "devfs"},
		{Mount: "/System/Volumes/Data/home", Device: "map auto_home", FS: "autofs"},
		{Mount: "/System/Volumes/Data", Device: "/dev/disk3s5", FS: "apfs"},
		{Mount: "/System/Volumes/Update/mnt1", Device: "/dev/disk3s1", FS: "apfs"},
		{Mount: "/private/var/folders/x", Device: "/private/var/folders/y", FS: "nullfs"},
	} {
		t.Run("the default list excludes "+d.Mount+" ("+d.FS+")", func(t *testing.T) {
			if got := keepDisks([]Disk{d}, nil); len(got) != 0 {
				t.Errorf("kept %v", mounts(got))
			}
		})
	}

	// "/System/VolumesX" and "/snapshots" check that the mount patterns are
	// anchored prefixes of a whole path segment.
	for _, d := range []Disk{
		{Mount: "/", Device: "/dev/disk3s1s1", FS: "apfs"},
		{Mount: "/", Device: "/dev/nvme0n1p2", FS: "ext4"},
		{Mount: "/Volumes/Backup", Device: "/dev/disk5s1", FS: "apfs"},
		{Mount: "/boot/efi", Device: "/dev/nvme0n1p1", FS: "vfat"},
		{Mount: "/mnt/nas", Device: "//nas/share", FS: "smbfs"},
		{Mount: "/srv", Device: "rpool/srv", FS: "zfs"},
		{Mount: "/System/VolumesX", Device: "/dev/disk6s1", FS: "apfs"},
		{Mount: "/snapshots", Device: "/dev/sdc1", FS: "btrfs"},
	} {
		t.Run("the default list keeps "+d.Mount+" ("+d.FS+")", func(t *testing.T) {
			if got := keepDisks([]Disk{d}, nil); len(got) != 1 {
				t.Errorf("dropped %s", d.Mount)
			}
		})
	}

	t.Run("a user's regex replaces the defaults and matches the mount point or the fs type", func(t *testing.T) {
		ds := []Disk{
			{Mount: "/", Device: "a", FS: "ext4"}, {Mount: "/run", Device: "b", FS: "tmpfs"},
			{Mount: "/boot/efi", Device: "c", FS: "vfat"}, {Mount: "/mnt/x", Device: "d", FS: "xfs"},
		}
		got := keepDisks(ds, regexp.MustCompile(`^(vfat|/mnt/.*)$`))
		if want := []string{"/", "/run"}; !slices.Equal(mounts(got), want) {
			t.Errorf("kept %v, want %v", mounts(got), want)
		}
	})

	t.Run("mounts of one device are listed once, under the shortest mount point, where the first one was", func(t *testing.T) {
		ds := []Disk{
			{Mount: "/srv/exports/media", Device: "/dev/sda1", FS: "ext4"},
			{Mount: "/", Device: "/dev/sdb2", FS: "btrfs"},
			{Mount: "/media", Device: "/dev/sda1", FS: "ext4"},
			{Mount: "/home", Device: "/dev/sdb2", FS: "btrfs"},
			{Mount: "/media/again", Device: "/dev/sda1", FS: "ext4"},
		}
		if got, want := mounts(keepDisks(ds, nil)), []string{"/media", "/"}; !slices.Equal(got, want) {
			t.Errorf("kept %v, want %v", got, want)
		}
	})

	t.Run("no mounts at all is an empty list, not nil", func(t *testing.T) {
		if got := keepDisks(nil, nil); got == nil || len(got) != 0 {
			t.Errorf("got %#v, want an empty, non-nil slice", got)
		}
	})
}
