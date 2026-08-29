// Package spool owns the on-disk event spool (event-contract.md §Spool and
// cursors). This file implements the append path: one writer per source,
// guarded by an advisory lock, appending newline-terminated records that are
// durable before the append is reported as accepted.
package spool

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"

	"github.com/milanhorvatovic/agent-signals/internal/contract"
	"github.com/milanhorvatovic/agent-signals/internal/durability"
)

// ErrLocked reports that another process already writes this source. The
// service runs at most one producer per source (event-contract.md §Spool and
// cursors), so a second writer is a configuration or supervision fault, not a
// condition to wait out.
var ErrLocked = errors.New("another writer holds the spool for this source")

var (
	errEmptyRecord     = errors.New("record is empty")
	errEmbeddedNewline = errors.New("record contains a newline, which would frame it as two events")
)

// tailScanChunk bounds the backward scan for the last newline. A torn record
// is at most one event long, so the scan almost always ends in the first
// chunk; the loop exists for the case where it does not.
const tailScanChunk = 64 * 1024

// Root is the spool state directory — the `.agent/` tree that holds events,
// cursors, locks, and checkpoints.
type Root string

func (r Root) eventsDir() string { return filepath.Join(string(r), "events") }

func (r Root) eventsPath(source string) string {
	return filepath.Join(r.eventsDir(), source+".jsonl")
}

func (r Root) writerLockDir() string { return filepath.Join(string(r), "locks", "writers") }

func (r Root) writerLockPath(source string) string {
	return filepath.Join(r.writerLockDir(), source+".lock")
}

// Writer appends events to one source spool.
type Writer struct {
	source string
	events *os.File
	// lock is held open for the writer's whole life, not because the handle
	// is used again but because the advisory lock lives on the descriptor: a
	// collected *os.File would release the single-writer guard when its
	// finalizer closed the descriptor, with nothing to notice.
	lock   *os.File
	syncer durability.Syncer
}

// Open takes the single-writer guard for source, repairs a torn tail left by
// an earlier crash, and returns a writer positioned to append. It fails with
// ErrLocked while another writer holds the source.
func Open(root Root, source string, syncer durability.Syncer) (*Writer, error) {
	// The source becomes two path components below; it is validated here so
	// no unvalidated value ever reaches the filesystem.
	if err := contract.ValidateSlug("source", source); err != nil {
		return nil, err
	}
	// An unprobed syncer would let the writer open and then fail every
	// append; the durability guarantee is settled before any state exists.
	if !syncer.Verified() {
		return nil, durability.ErrUnprobed
	}

	lock, err := acquireWriterLock(root, source)
	if err != nil {
		return nil, err
	}

	events, err := openEventsFile(root, source, syncer)
	if err != nil {
		_ = lock.Close()
		return nil, err
	}

	writer := &Writer{source: source, events: events, lock: lock, syncer: syncer}
	if err := writer.repairTail(); err != nil {
		_ = writer.Close()
		return nil, err
	}

	return writer, nil
}

func acquireWriterLock(root Root, source string) (*os.File, error) {
	if err := os.MkdirAll(root.writerLockDir(), 0o755); err != nil {
		return nil, fmt.Errorf("create writer lock directory: %w", err)
	}

	path := root.writerLockPath(source)
	lock, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}

	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = lock.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, fmt.Errorf("%s: %w", source, ErrLocked)
		}
		return nil, fmt.Errorf("lock %s: %w", path, err)
	}

	return lock, nil
}

func openEventsFile(root Root, source string, syncer durability.Syncer) (*os.File, error) {
	dir := root.eventsDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("create events directory: %w", err)
	}

	path := root.eventsPath(source)
	_, err := os.Stat(path)
	created := errors.Is(err, os.ErrNotExist)
	if err != nil && !created {
		return nil, fmt.Errorf("stat %s: %w", path, err)
	}

	events, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}

	// A new spool file's directory entry is only durable once the directory
	// itself is synced; without this a host loss could leave events written
	// to a file the directory never gained.
	if created {
		if err := syncer.SyncDir(dir); err != nil {
			_ = events.Close()
			return nil, err
		}
	}

	return events, nil
}

// Append writes record as one newline-terminated line and returns only after
// it is durably on disk, which is what lets the caller report the event as
// accepted (event-contract.md §Spool and cursors).
func (w *Writer) Append(record []byte) error {
	if len(record) == 0 {
		return errEmptyRecord
	}
	if bytes.IndexByte(record, '\n') >= 0 {
		return errEmbeddedNewline
	}

	// One write call: O_APPEND makes a single write atomic against the file's
	// end, so a concurrent reader sees either none of the line or all of it.
	line := make([]byte, 0, len(record)+1)
	line = append(line, record...)
	line = append(line, '\n')
	if _, err := w.events.Write(line); err != nil {
		return fmt.Errorf("append to %s: %w", w.source, err)
	}

	if err := w.syncer.SyncFile(w.events); err != nil {
		return fmt.Errorf("sync %s: %w", w.source, err)
	}

	return nil
}

// Close releases the single-writer guard. The lock file itself is never
// removed: a holder that unlinks it keeps its lock on the now-nameless inode
// while the next writer creates and locks a different one, and both then
// believe they are the only writer.
func (w *Writer) Close() error {
	return errors.Join(w.events.Close(), w.lock.Close())
}

// repairTail truncates a trailing fragment left by a crash mid-append, so the
// next append cannot fuse itself onto half a record (event-contract.md §Spool
// and cursors). The lost event was never checkpointed, so the watcher replays
// it.
func (w *Writer) repairTail() error {
	info, err := w.events.Stat()
	if err != nil {
		return fmt.Errorf("stat %s: %w", w.source, err)
	}
	size := info.Size()
	if size == 0 {
		return nil
	}

	last := make([]byte, 1)
	if _, err := w.events.ReadAt(last, size-1); err != nil {
		return fmt.Errorf("read tail of %s: %w", w.source, err)
	}
	if last[0] == '\n' {
		return nil
	}

	newline, err := lastNewlineOffset(w.events, size)
	if err != nil {
		return fmt.Errorf("scan tail of %s: %w", w.source, err)
	}
	if err := w.events.Truncate(newline + 1); err != nil {
		return fmt.Errorf("repair tail of %s: %w", w.source, err)
	}

	return w.syncer.SyncFile(w.events)
}

// lastNewlineOffset scans backward in bounded chunks and reports the offset of
// the final newline, or -1 when the file holds none.
func lastNewlineOffset(file *os.File, size int64) (int64, error) {
	buf := make([]byte, tailScanChunk)
	for end := size; end > 0; {
		start := max(end-int64(len(buf)), 0)
		chunk := buf[:end-start]
		if _, err := file.ReadAt(chunk, start); err != nil {
			return 0, err
		}
		if index := bytes.LastIndexByte(chunk, '\n'); index >= 0 {
			return start + int64(index), nil
		}
		end = start
	}

	return -1, nil
}
