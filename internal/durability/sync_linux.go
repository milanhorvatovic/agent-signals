package durability

import (
	"fmt"
	"os"
	"strconv"
	"syscall"
)

// syncFileData issues fsync, Linux's durable file sync: it flushes file data
// and the metadata needed to read it back. Whether that survives host loss
// depends on the drive honouring the resulting cache flush, which is the same
// assumption every Linux database makes.
func syncFileData(file *os.File) error {
	return fsync(file)
}

// inspectFilesystem asks the mount table what the filesystem is called, since
// Linux offers no local/remote flag to ask directly and statfs reports only a
// magic number. A name can be classified or refused honestly; a number can
// only be matched against a list of numbers, which is how an unlisted network
// filesystem passes for a local one.
//
// The magic table remains as the fallback for the case where the mount table
// cannot be read — a chroot without /proc, mainly. There an unrecognised
// magic still counts as local, because refusing every filesystem a list of
// numbers has not heard of would reject new local ones as readily as remote
// ones, and a number carries nothing an error message could act on.
func inspectFilesystem(dir string) (filesystem, error) {
	if named, err := inspectMountTable(dir); err != nil || named.name != "" {
		return named, err
	}

	var stat syscall.Statfs_t
	if err := syscall.Statfs(dir, &stat); err != nil {
		return filesystem{}, err
	}

	// Statfs_t.Type is int64 on 64-bit kernels and int32 on 32-bit ones, where
	// a magic with the high bit set — CIFS, Btrfs, ramfs — arrives negative
	// and would sign-extend past every table key, classifying CIFS as an
	// unknown local filesystem. Narrowing to uint32 makes both widths agree;
	// every magic is a 32-bit value, so nothing is lost on the wider one.
	return filesystemForMagic(uint32(stat.Type)), nil
}

// inspectMountTable classifies by the name the kernel publishes for the mount
// holding this device. It reports an empty filesystem when the table is
// unreadable or holds no matching mount, leaving the caller on the magic
// fallback, and an error when the table names a filesystem no list here knows.
func inspectMountTable(dir string) (filesystem, error) {
	mountinfo, err := os.ReadFile("/proc/self/mountinfo")
	if err != nil {
		return filesystem{}, nil
	}

	// The mount table is keyed by the device a file reports, which statfs does
	// not carry — st_dev comes from stat(2).
	device, err := deviceOf(dir)
	if err != nil {
		return filesystem{}, err
	}

	major, minor := linuxDeviceNumbers(device)
	name := mountTypeForDevice(mountinfo, major, minor)
	if name == "" {
		return filesystem{}, nil
	}

	known, ok := classifyMountType(name)
	if !ok {
		return filesystem{}, fmt.Errorf("%w: %q", ErrUnknownFilesystem, name)
	}

	return known, nil
}

func filesystemForMagic(magic uint32) filesystem {
	if known, ok := filesystemMagics[magic]; ok {
		return known
	}

	return filesystem{name: "0x" + strconv.FormatUint(uint64(magic), 16)}
}

var filesystemMagics = map[uint32]filesystem{
	0x6969:     {name: "nfs", network: true},
	0xff534d42: {name: "cifs", network: true},
	0xfe534d42: {name: "smb2", network: true},
	0x517b:     {name: "smbfs", network: true},
	0x5346414f: {name: "afs", network: true},
	0x6b414653: {name: "afs", network: true},
	0x1021997:  {name: "9p", network: true},
	0xc36400:   {name: "ceph", network: true},
	0xbd00bd0:  {name: "lustre", network: true},
	0x7461636f: {name: "ocfs2", network: true},
	// FUSE is how sshfs, rclone, s3fs and their kind reach Linux, and the
	// magic is the same for every driver, so the subtype cannot be read from
	// statfs. Refused rather than guessed: sync semantics on a FUSE mount
	// belong to the driver, which is not a substrate for a durable spool even
	// when the driver is local.
	0x65735546: {name: "fuse", network: true},
	0x1021994:  {name: "tmpfs", volatile: true},
	0x858458f6: {name: "ramfs", volatile: true},
	// An overlay mount says nothing about what backs it: the upper layer may
	// be tmpfs, and statfs cannot see through to it. Capped at the weaker mode
	// because the persistence of what is written here cannot be established
	// from the mount alone.
	0x794c7630: {name: "overlay", volatile: true},
	0x9123683e: {name: "btrfs"},
	0xef53:     {name: "ext4"},
	0x58465342: {name: "xfs"},
	0x2fc12fc1: {name: "zfs"},
}
