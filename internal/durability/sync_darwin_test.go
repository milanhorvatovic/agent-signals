package durability

import (
	"os"
	"testing"
)

func TestFullFsyncSucceedsOnARegularFile(t *testing.T) {
	file, err := os.CreateTemp(t.TempDir(), "fullfsync")
	if err != nil {
		t.Fatalf("create temp: %v", err)
	}
	defer func() { _ = file.Close() }()

	if _, err := file.WriteString("{}\n"); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := syncFileData(file); err != nil {
		t.Fatalf("F_FULLFSYNC on a regular file: %v", err)
	}
}

// The runtime's own sync answers a refusal by retrying as an ordinary fsync,
// which would report host-loss durability the filesystem never gave; this
// adapter returns the refusal instead. A pipe is the portable way to make the
// kernel refuse, having no vnode to flush, and it refuses with EBADF rather
// than the ENOTSUP an unsupporting filesystem gives — so what this pins is
// that the error surfaces at all, not which mode Probe would report from it.
func TestFullFsyncReportsARefusal(t *testing.T) {
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	defer func() { _ = reader.Close() }()
	defer func() { _ = writer.Close() }()

	err = syncFileData(writer)
	if err == nil {
		t.Fatal("F_FULLFSYNC on a pipe reported success")
	}
	t.Logf("refusal surfaced as %v (unsupported: %v)", err, isUnsupported(err))
}
