package durability

import (
	"os"
	"runtime"
	"syscall"
)

// syncFileData issues F_FULLFSYNC, the only macOS primitive that flushes the
// drive's own write cache; ordinary fsync returns as soon as the data reaches
// that cache and does not survive host loss. Unlike the runtime's file sync,
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

// inspectFilesystem reads the mounted filesystem type by name, which macOS
// reports directly.
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

	return filesystem{name: string(name), network: networkFilesystems[string(name)]}, nil
}

var networkFilesystems = map[string]bool{
	"nfs":    true,
	"smbfs":  true,
	"afpfs":  true,
	"webdav": true,
	"ftp":    true,
}
