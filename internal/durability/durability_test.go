package durability

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestProbeReportsAVerifiedMode(t *testing.T) {
	dir := t.TempDir()

	syncer, err := Probe(dir)
	if err != nil {
		t.Fatalf("probe: %v", err)
	}

	t.Logf("filesystem %s reports %s durability", syncer.Filesystem(), syncer.Mode())
	switch syncer.Mode() {
	case HostLoss, ProcessCrash:
	default:
		t.Fatalf("probe returned %q, which is not a verified mode", syncer.Mode())
	}

	// Both modes are legitimate results, so the general assertion is weak.
	// These filesystems make it specific: a downgrade on one that supports the
	// full-barrier sync means the adapter failed to run it, and host-loss
	// durability on a volatile one is a guarantee that cannot be kept.
	switch syncer.Filesystem() {
	case "apfs", "ext4", "xfs", "btrfs", "zfs":
		if syncer.Mode() != HostLoss {
			t.Errorf("%s downgraded to %s despite supporting the full-barrier sync", syncer.Filesystem(), syncer.Mode())
		}
	case "tmpfs", "ramfs":
		if syncer.Mode() != ProcessCrash {
			t.Errorf("volatile filesystem %s advertised %s", syncer.Filesystem(), syncer.Mode())
		}
	}

	if syncer.Filesystem() == "" {
		t.Error("filesystem name is empty; diagnostics cannot name the volume")
	}
	if syncer.Dir() != dir {
		t.Errorf("syncer reports %s as the probed directory, want %s", syncer.Dir(), dir)
	}
}

// A path can leave the probed filesystem without leaving the directory, so
// the check is on the opened file rather than on its name.
func TestVerifyFileRejectsAnotherFilesystem(t *testing.T) {
	dir := t.TempDir()
	syncer, err := Probe(dir)
	if err != nil {
		t.Fatalf("probe: %v", err)
	}

	local, err := os.CreateTemp(dir, "local")
	if err != nil {
		t.Fatalf("create temp: %v", err)
	}
	defer func() { _ = local.Close() }()

	if err := syncer.VerifyFile(local); err != nil {
		t.Errorf("file inside the probed directory rejected: %v", err)
	}

	// The device node lives on the kernel's own filesystem, never on the one
	// holding a temporary directory.
	foreign, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatalf("open %s: %v", os.DevNull, err)
	}
	defer func() { _ = foreign.Close() }()

	if err := syncer.VerifyFile(foreign); !errors.Is(err, ErrForeignFilesystem) {
		t.Errorf("VerifyFile on %s returned %v, want ErrForeignFilesystem", os.DevNull, err)
	}
}

func TestProbeLeavesNothingBehind(t *testing.T) {
	dir := t.TempDir()

	if _, err := Probe(dir); err != nil {
		t.Fatalf("probe: %v", err)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read dir: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("probe left %d entries behind: %v", len(entries), entries)
	}
}

func TestProbeFailsOnAMissingDirectory(t *testing.T) {
	if _, err := Probe(filepath.Join(t.TempDir(), "absent")); err == nil {
		t.Fatal("probing a missing directory succeeded")
	}
}

// The zero Syncer has no verified mode, so honouring it would mean syncing at
// an unknown guarantee — the one outcome the adapter exists to prevent.
func TestUnprobedSyncerRefusesToSync(t *testing.T) {
	dir := t.TempDir()
	file, err := os.CreateTemp(dir, "unprobed")
	if err != nil {
		t.Fatalf("create temp: %v", err)
	}
	defer func() { _ = file.Close() }()

	var unprobed Syncer
	if err := unprobed.SyncFile(file); !errors.Is(err, ErrUnprobed) {
		t.Errorf("SyncFile on an unprobed syncer returned %v, want ErrUnprobed", err)
	}
	if err := unprobed.SyncDir(dir); !errors.Is(err, ErrUnprobed) {
		t.Errorf("SyncDir on an unprobed syncer returned %v, want ErrUnprobed", err)
	}
}

func TestSyncFileAndSyncDirSucceedOnALocalFilesystem(t *testing.T) {
	dir := t.TempDir()
	syncer, err := Probe(dir)
	if err != nil {
		t.Fatalf("probe: %v", err)
	}

	path := filepath.Join(dir, "events.jsonl")
	file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_RDWR, 0o644)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer func() { _ = file.Close() }()

	if _, err := file.WriteString("{}\n"); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := syncer.SyncFile(file); err != nil {
		t.Errorf("SyncFile: %v", err)
	}
	if err := syncer.SyncDir(dir); err != nil {
		t.Errorf("SyncDir: %v", err)
	}
}
