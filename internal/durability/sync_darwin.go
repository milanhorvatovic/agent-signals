package durability

import (
	"os"
	"runtime"
	"syscall"
)

// syncFileData issues F_FULLFSYNC, the only macOS primitive that flushes the
// drive's own write cache; ordinary fsync returns as soon as the data reaches
// that cache and does not survive host loss. It applies to a directory handle
// as much as a file one, since the barrier is issued to the device. Unlike the runtime's file sync,
// a refusal is returned rather than retried as a weaker fsync, so Probe can
// report the downgrade instead of hiding it.
func syncFileData(file *os.File) error {
	_, _, errno := syscall.Syscall(syscall.SYS_FCNTL, file.Fd(), uintptr(syscall.F_FULLFSYNC), 0)
	runtime.KeepAlive(file)
	if errno != 0 {
		return errno
	}

	return nil
}

// mntLocal is MNT_LOCAL from the kernel's mount flags: the mount is served
// locally rather than over a network. Go's syscall package does not export it.
const mntLocal = 0x1000

// inspectFilesystem reads the mounted filesystem type by name, which macOS
// reports directly, and asks the kernel whether the mount is local. The flag
// is the authority: it covers network filesystems mounted through FUSE, which
// no list of names can enumerate.
func inspectFilesystem(dir string) (filesystem, error) {
	var stat syscall.Statfs_t
	if err := syscall.Statfs(dir, &stat); err != nil {
		return filesystem{}, err
	}

	name := make([]byte, 0, len(stat.Fstypename))
	for _, char := range stat.Fstypename {
		if char == 0 {
			break
		}
		name = append(name, byte(char))
	}

	local := stat.Flags&mntLocal != 0

	return filesystem{
		name:     string(name),
		network:  !local || networkFilesystems[string(name)],
		volatile: volatileFilesystems[string(name)],
	}, nil
}

// volatileFilesystems are the types macOS names as living in memory. The list
// closes only the case the kernel is willing to describe: a filesystem on a
// RAM-backed device — a disk image attached with ram:// and formatted APFS or
// HFS+ — reports the same type and the same local-mount flag as one on a
// physical disk, and statfs exposes nothing that separates them. Seeing
// through that would mean asking DiskArbitration, a framework dependency this
// package does without, so the host-loss claim assumes persistent backing and
// says so where the mode is defined.
var volatileFilesystems = map[string]bool{
	"tmpfs": true,
	"ramfs": true,
	"devfs": true,
}

var networkFilesystems = map[string]bool{
	"nfs":    true,
	"smbfs":  true,
	"afpfs":  true,
	"webdav": true,
	"ftp":    true,
}
