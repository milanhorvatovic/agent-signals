package durability

import (
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

// inspectFilesystem maps the statfs magic number, since Linux reports the
// filesystem type as a number rather than a name. An unrecognised type is
// named by its magic and treated as local: only the types listed here are
// known to be network filesystems.
func inspectFilesystem(dir string) (filesystem, error) {
	var stat syscall.Statfs_t
	if err := syscall.Statfs(dir, &stat); err != nil {
		return filesystem{}, err
	}

	if known, ok := filesystemMagics[int64(stat.Type)]; ok {
		return known, nil
	}

	return filesystem{name: "0x" + strconv.FormatInt(int64(stat.Type), 16)}, nil
}

var filesystemMagics = map[int64]filesystem{
	0x6969:     {name: "nfs", network: true},
	0xff534d42: {name: "cifs", network: true},
	0x517b:     {name: "smbfs", network: true},
	0x5346414f: {name: "afs", network: true},
	0x9123683e: {name: "btrfs"},
	0x794c7630: {name: "overlay"},
	0xef53:     {name: "ext4"},
	0x58465342: {name: "xfs"},
	0x1021994:  {name: "tmpfs"},
	0x2fc12fc1: {name: "zfs"},
}
