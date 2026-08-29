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
	"runtime"
	"syscall"
)

// filesystem is what the platform can say about the volume holding the spool:
// a name for diagnostics, and whether it is a network filesystem, which the
// contract excludes because O_APPEND races there and locking is emulated.
type filesystem struct {
	name    string
	network bool
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

// Syncer performs the spool's durable writes at the strongest guarantee its
// directory was verified to support. Create one with Probe.
type Syncer struct {
	mode       Mode
	filesystem string
}

// Probe inspects dir and verifies the durability primitives against it, so an
// unsupported filesystem is discovered at startup rather than at the first
// event. dir must already exist.
func Probe(dir string) (Syncer, error) {
	volume, err := inspectFilesystem(dir)
	if err != nil {
		return Syncer{}, fmt.Errorf("inspect %s: %w", dir, err)
	}
	if volume.network {
		return Syncer{}, fmt.Errorf("%s is on %s: %w", dir, volume.name, ErrNetworkFilesystem)
	}

	mode, err := probeMode(dir)
	if err != nil {
		return Syncer{}, err
	}

	return Syncer{mode: mode, filesystem: volume.name}, nil
}

// probeMode answers the question the platform cannot be asked directly: it
// runs the full-barrier sync on a real file in the target directory and reads
// the refusal, if any, from the result.
func probeMode(dir string) (Mode, error) {
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

	switch err := syncFileData(file); {
	case err == nil:
		return HostLoss, nil
	case isUnsupported(err):
		return ProcessCrash, nil
	default:
		return "", fmt.Errorf("durability probe in %s: %w", dir, err)
	}
}

func isUnsupported(err error) bool {
	return errors.Is(err, syscall.ENOTSUP) ||
		errors.Is(err, syscall.EOPNOTSUPP) ||
		errors.Is(err, syscall.EINVAL) ||
		errors.Is(err, syscall.ENOSYS)
}

// Mode reports the guarantee this syncer verified.
func (s Syncer) Mode() Mode { return s.mode }

// Verified reports whether this syncer came from Probe. The zero Syncer
// carries no verified guarantee and refuses every sync.
func (s Syncer) Verified() bool { return s.mode != "" }

// Filesystem names the volume holding the spool, for diagnostics.
func (s Syncer) Filesystem() string { return s.filesystem }

// SyncFile flushes the file's data at the verified guarantee. It returns only
// after the platform reports the write is on stable storage.
func (s Syncer) SyncFile(file *os.File) error {
	switch s.mode {
	case HostLoss:
		return syncFileData(file)
	case ProcessCrash:
		return fsync(file)
	default:
		return ErrUnprobed
	}
}

// SyncDir flushes a directory's metadata, making a file created or renamed in
// it durable. Directory metadata uses the ordinary directory sync on both
// platforms; the full-barrier primitive covers file data.
func (s Syncer) SyncDir(dir string) error {
	if s.mode == "" {
		return ErrUnprobed
	}

	handle, err := os.Open(dir)
	if err != nil {
		return fmt.Errorf("open %s for sync: %w", dir, err)
	}
	defer func() { _ = handle.Close() }()

	if err := fsync(handle); err != nil {
		return fmt.Errorf("sync %s: %w", dir, err)
	}

	return nil
}

func fsync(file *os.File) error {
	err := syscall.Fsync(int(file.Fd()))
	runtime.KeepAlive(file)

	return err
}
