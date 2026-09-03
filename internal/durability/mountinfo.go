//go:build linux

package durability

import (
	"bytes"
	"strconv"
	"strings"
)

// This file reads Linux's mount table, which is the only place that names the
// filesystem holding a path: statfs reports a magic number, and a number can
// only be matched against a list of numbers somebody remembered to write down.

// mountTypeForDevice returns the filesystem type of the mount whose device
// matches, or the empty string when the table holds no such mount.
//
// Matching on the device rather than the mount point is what keeps this
// simple: the caller already has the device from the directory it probed, so
// there is no longest-prefix search, no symlink resolution, and no ambiguity
// from bind mounts, which repeat one device under several paths and always
// with the same type. It also sidesteps the octal escapes the kernel writes
// into mount-point fields, since no field this reads can contain a space.
//
// The format is one mount per line (proc(5)):
//
//	36 35 98:0 /mnt1 /mnt2 rw,noatime shared:1 - ext3 /dev/root rw
//	                                            ^ type follows the lone dash
//
// The optional fields before the dash are variable in number, so the type is
// found by scanning for the separator rather than by a fixed index.
func mountTypeForDevice(mountinfo []byte, major, minor uint32) string {
	want := strconv.FormatUint(uint64(major), 10) + ":" + strconv.FormatUint(uint64(minor), 10)

	for _, line := range bytes.Split(mountinfo, []byte{'\n'}) {
		fields := strings.Fields(string(line))
		if len(fields) < 8 || fields[2] != want {
			continue
		}
		for index := 6; index < len(fields)-1; index++ {
			if fields[index] == "-" {
				return fields[index+1]
			}
		}
	}

	return ""
}

// linuxDeviceNumbers splits a Linux st_dev into the major and minor numbers
// the mount table prints. The encoding spreads both across the word, so the
// low bits alone would mismatch every device numbered above 255.
func linuxDeviceNumbers(device uint64) (major, minor uint32) {
	major = uint32((device>>8)&0xfff) | uint32((device>>32)&^uint64(0xfff))
	minor = uint32(device&0xff) | uint32((device>>12)&^uint64(0xff))

	return major, minor
}

// classifyMountType answers for a filesystem named by the kernel. Reporting
// false means the name is absent from every list below, which the caller
// treats as a refusal rather than a guess: unlike a magic number, a name the
// kernel gave us and this table does not know is genuinely unusual, and the
// spool cannot state what append or locking semantics it would get there.
func classifyMountType(name string) (filesystem, bool) {
	// Every FUSE driver reports its own name here, so unlike the shared magic
	// this can say which one — but the semantics still belong to a userspace
	// driver rather than the kernel, so the refusal stands and merely becomes
	// legible.
	if name == "fuse" || name == "fuseblk" || strings.HasPrefix(name, "fuse.") {
		return filesystem{name: name, network: true}, true
	}

	known, ok := mountTypes[name]
	if !ok {
		return filesystem{name: name}, false
	}
	known.name = name

	return known, true
}

// mountTypes classifies the filesystems a machine running this service
// plausibly mounts. Local entries carry no flags.
var mountTypes = map[string]filesystem{
	"nfs":       {network: true},
	"nfs4":      {network: true},
	"cifs":      {network: true},
	"smb3":      {network: true},
	"smbfs":     {network: true},
	"9p":        {network: true},
	"ceph":      {network: true},
	"lustre":    {network: true},
	"ocfs2":     {network: true},
	"afs":       {network: true},
	"glusterfs": {network: true},
	"davfs":     {network: true},
	"beegfs":    {network: true},
	"orangefs":  {network: true},
	"tmpfs":     {volatile: true},
	"ramfs":     {volatile: true},
	"overlay":   {volatile: true},
	"ext2":      {},
	"ext3":      {},
	"ext4":      {},
	"xfs":       {},
	"btrfs":     {},
	"zfs":       {},
	"f2fs":      {},
	"bcachefs":  {},
	"jfs":       {},
	"nilfs2":    {},
	"reiserfs":  {},
	"erofs":     {},
	"squashfs":  {},
	"udf":       {},
	"ubifs":     {},
	"vfat":      {},
	"exfat":     {},
	"ntfs":      {},
	"ntfs3":     {},
	"hfsplus":   {},
}
