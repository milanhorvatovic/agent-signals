package durability

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestProbeVerifiesTheHostLossPrimitive(t *testing.T) {
	dir := t.TempDir()

	syncer, err := Probe(dir)
	if err != nil {
		t.Fatalf("probe: %v", err)
	}

	t.Logf("filesystem %s reports %s durability", syncer.Filesystem(), syncer.Mode())
	if syncer.Mode() != HostLoss {
		t.Errorf("local filesystem %s downgraded to %s: the platform adapter cannot verify the full-barrier sync here", syncer.Filesystem(), syncer.Mode())
	}
	if syncer.Filesystem() == "" {
		t.Error("filesystem name is empty; diagnostics cannot name the volume")
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
