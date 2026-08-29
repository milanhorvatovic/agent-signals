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
// filesystem type as a number rather than a name and offers no local/remote
// flag to ask instead. An unrecognised type is named by its magic and treated
// as local, which is a deliberate trade: refusing every filesystem this table
// has not heard of would reject new local ones as readily as remote ones,
// while the exclusion the contract needs is documented rather than enforced.
// Types known to be network or volatile are listed exhaustively enough to
// catch what a developer machine actually mounts.
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
	0x9123683e: {name: "btrfs"},
	0x794c7630: {name: "overlay"},
	0xef53:     {name: "ext4"},
	0x58465342: {name: "xfs"},
	0x2fc12fc1: {name: "zfs"},
}
