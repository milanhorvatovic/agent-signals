// Package durability is the platform adapter for the spool's durability
// primitives (event-contract.md §Spool and cursors): the file-data sync that
// must survive sudden host loss, the directory-metadata sync that makes a
// create or rename durable, and the startup capability check that decides
// which guarantee this installation may honestly advertise.
//
// The runtime's own file sync is deliberately not the host-loss primitive.
// On macOS it issues F_FULLFSYNC but falls back to ordinary fsync when the
// filesystem answers ENOTSUP, which is the silent downgrade the contract
// forbids: the guarantee has to be verified and reported, not assumed.
package durability

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"syscall"
)

// filesystem is what the platform can say about the volume holding the spool:
// a name for diagnostics, whether it is a network filesystem, which the
// contract excludes because O_APPEND races there and locking is emulated, and
// whether its contents are volatile, which caps the guarantee it can give
// however well its sync call reports success.
type filesystem struct {
	name     string
	network  bool
	volatile bool
}

// Mode is the durability guarantee a probed directory can honour.
type Mode string

const (
	// HostLoss survives sudden host power loss: the platform's full-barrier
	// file sync is available on this filesystem and was verified at probe
	// time. This is the contract's durability target.
	HostLoss Mode = "host-loss"
	// ProcessCrash survives process death only. The filesystem refused the
	// full-barrier primitive, so writes reported as durable may still be
	// sitting in a drive cache. Callers advertise this weaker guarantee
	// explicitly rather than claiming the stronger one.
	ProcessCrash Mode = "process-crash"
)

// ErrNetworkFilesystem rejects the installation outright: O_APPEND races and
// emulated locking make network filesystems unsupported, not merely weaker.
var ErrNetworkFilesystem = errors.New("spool state on a network filesystem is unsupported")

// ErrUnprobed guards the zero Syncer, whose empty mode would otherwise
// silently degrade every sync to the weaker primitive.
var ErrUnprobed = errors.New("syncer was not created by Probe")

// ErrForeignFilesystem reports a file on a filesystem this syncer never
// probed, whose durability and locking behaviour are therefore unknown.
var ErrForeignFilesystem = errors.New("file is not on the probed filesystem")

// Syncer performs the spool's durable writes at the strongest guarantee its
// directory was verified to support. Create one with Probe.
type Syncer struct {
	dir        string
	device     uint64
	mode       Mode
	filesystem string
}

// Probe inspects dir and verifies the durability primitives against it, so an
// unsupported filesystem is discovered at startup rather than at the first
// event. dir must already exist.
func Probe(dir string) (Syncer, error) {
	absolute, err := filepath.Abs(dir)
	if err != nil {
		return Syncer{}, fmt.Errorf("resolve %s: %w", dir, err)
	}

	volume, err := inspectFilesystem(absolute)
	if err != nil {
		return Syncer{}, fmt.Errorf("inspect %s: %w", absolute, err)
	}
	if volume.network {
		return Syncer{}, fmt.Errorf("%s is on %s: %w", absolute, volume.name, ErrNetworkFilesystem)
	}

	device, err := deviceOf(absolute)
	if err != nil {
		return Syncer{}, fmt.Errorf("identify the filesystem of %s: %w", absolute, err)
	}

	mode, err := probeMode(absolute, volume)
	if err != nil {
		return Syncer{}, err
	}

	return Syncer{dir: absolute, device: device, mode: mode, filesystem: volume.name}, nil
}

// VerifyFile reports whether file sits on the filesystem this syncer probed.
// A path can leave that filesystem without leaving the directory — a symlinked
// component, a nested mount — and the guarantee this syncer advertises was
// measured on one filesystem only.
func (s Syncer) VerifyFile(file *os.File) error {
	if !s.Verified() {
		return ErrUnprobed
	}

	info, err := file.Stat()
	if err != nil {
		return fmt.Errorf("stat %s: %w", file.Name(), err)
	}
	device, err := deviceOfInfo(info)
	if err != nil {
		return err
	}
	if device != s.device {
		return fmt.Errorf("%w: %s is on device %d, %s was probed on %d", ErrForeignFilesystem, file.Name(), device, s.dir, s.device)
	}

	return nil
}

func deviceOf(path string) (uint64, error) {
	info, err := os.Stat(path)
	if err != nil {
		return 0, err
	}

	return deviceOfInfo(info)
}

// deviceOfInfo reads the device the file lives on. Two paths share a device
// exactly when they share a filesystem, which is the question every caller
// here is really asking.
func deviceOfInfo(info os.FileInfo) (uint64, error) {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, fmt.Errorf("filesystem identity is unavailable for %s", info.Name())
	}

	return uint64(stat.Dev), nil
}

// probeMode answers the question the platform cannot be asked directly: it
// runs the full-barrier sync on a real file in the target directory and reads
// the refusal, if any, from the result.
func probeMode(dir string, volume filesystem) (Mode, error) {
	file, err := os.CreateTemp(dir, ".durability-probe-*")
	if err != nil {
		return "", fmt.Errorf("create durability probe in %s: %w", dir, err)
	}
	defer func() {
		_ = file.Close()
		_ = os.Remove(file.Name())
	}()

	if _, err := file.Write([]byte{'\n'}); err != nil {
		return "", fmt.Errorf("write durability probe: %w", err)
	}

	// A volatile filesystem holds the write in memory, so the full-barrier
	// sync would report success and still lose everything on host loss. Its
	// ceiling is the weaker guarantee no matter what the primitive answers.
	if volume.volatile {
		return verifyProcessCrash(file, dir)
	}

	switch err := syncFileData(file); {
	case err == nil:
	case isUnsupported(err):
		return verifyProcessCrash(file, dir)
	default:
		return "", fmt.Errorf("durability probe in %s: %w", dir, err)
	}

	// Metadata commits stand alone: a new spool file's directory entry and,
	// later, a cursor rename have no file-data barrier behind them to carry
	// them to the platter. The directory primitive is therefore verified too,
	// and when only one of the two is available the syncer reports the weaker
	// mode for both rather than a guarantee that holds for half its calls.
	handle, err := os.Open(dir)
	if err != nil {
		return "", fmt.Errorf("open %s for the directory probe: %w", dir, err)
	}
	defer func() { _ = handle.Close() }()

	switch err := syncFileData(handle); {
	case err == nil:
		return HostLoss, nil
	case isUnsupported(err):
		return verifyProcessCrash(file, dir)
	default:
		return "", fmt.Errorf("directory durability probe in %s: %w", dir, err)
	}
}

// verifyProcessCrash runs the weaker primitive rather than assuming it works.
// Every mode a Syncer reports is one it has actually executed here.
func verifyProcessCrash(file *os.File, dir string) (Mode, error) {
	if err := fsync(file); err != nil {
		return "", fmt.Errorf("ordinary sync probe in %s: %w", dir, err)
	}

	return ProcessCrash, nil
}

func isUnsupported(err error) bool {
	return errors.Is(err, syscall.ENOTSUP) ||
		errors.Is(err, syscall.EOPNOTSUPP) ||
		errors.Is(err, syscall.EINVAL) ||
		errors.Is(err, syscall.ENOSYS)
}

// Mode reports the guarantee this syncer verified.
func (s Syncer) Mode() Mode { return s.mode }

// Dir reports the directory this syncer was probed against. Durability was
// verified there and on no other filesystem.
func (s Syncer) Dir() string { return s.dir }

// Verified reports whether this syncer came from Probe. The zero Syncer
// carries no verified guarantee and refuses every sync.
func (s Syncer) Verified() bool { return s.mode != "" }

// Filesystem names the volume holding the spool, for diagnostics.
func (s Syncer) Filesystem() string { return s.filesystem }

// SyncFile flushes the file's data at the verified guarantee. It returns only
// after the platform reports the write is on stable storage.
func (s Syncer) SyncFile(file *os.File) error {
	if !s.Verified() {
		return ErrUnprobed
	}

	return s.sync(file)
}

// SyncDir flushes a directory's metadata, making a file created or renamed in
// it durable. It uses the same primitive as SyncFile: a metadata commit that
// nothing else follows would otherwise sit in the drive's cache while being
// reported as durable.
func (s Syncer) SyncDir(dir string) error {
	if !s.Verified() {
		return ErrUnprobed
	}

	handle, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer func() { _ = handle.Close() }()

	if err := s.sync(handle); err != nil {
		return fmt.Errorf("sync %s: %w", dir, err)
	}

	return nil
}

// sync applies the verified primitive to an open file or directory. Both were
// exercised at probe time, so the mode holds for either.
func (s Syncer) sync(file *os.File) error {
	if s.mode == HostLoss {
		return syncFileData(file)
	}

	return fsync(file)
}

func fsync(file *os.File) error {
	err := syscall.Fsync(int(file.Fd()))
	runtime.KeepAlive(file)

	return err
}
