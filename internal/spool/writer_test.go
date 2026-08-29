package spool

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/milanhorvatovic/agent-signals/internal/durability"
)

func newRoot(t *testing.T) (Root, durability.Syncer) {
	t.Helper()

	root := Root(t.TempDir())
	syncer, err := durability.Probe(string(root))
	if err != nil {
		t.Fatalf("probe durability: %v", err)
	}

	return root, syncer
}

func openWriter(t *testing.T, root Root, source string, syncer durability.Syncer) *Writer {
	t.Helper()

	writer, err := Open(root, source, syncer)
	if err != nil {
		t.Fatalf("open writer for %s: %v", source, err)
	}
	t.Cleanup(func() { _ = writer.Close() })

	return writer
}

func readSpool(t *testing.T, root Root, source string) string {
	t.Helper()

	content, err := os.ReadFile(root.eventsPath(source))
	if err != nil {
		t.Fatalf("read spool: %v", err)
	}

	return string(content)
}

// seedSpool plants a spool file as a crashed writer would have left it.
func seedSpool(t *testing.T, root Root, source, content string) {
	t.Helper()

	if err := os.MkdirAll(root.eventsDir(), 0o755); err != nil {
		t.Fatalf("create events dir: %v", err)
	}
	if err := os.WriteFile(root.eventsPath(source), []byte(content), 0o644); err != nil {
		t.Fatalf("seed spool: %v", err)
	}
}

func TestAppendFramesEachRecordAsOneLine(t *testing.T) {
	root, syncer := newRoot(t)
	writer := openWriter(t, root, "pr-comments", syncer)

	for _, record := range []string{`{"id":"a"}`, `{"id":"b"}`} {
		if err := writer.Append([]byte(record)); err != nil {
			t.Fatalf("append %s: %v", record, err)
		}
	}

	const want = "{\"id\":\"a\"}\n{\"id\":\"b\"}\n"
	if got := readSpool(t, root, "pr-comments"); got != want {
		t.Errorf("spool holds %q, want %q", got, want)
	}
}

func TestAppendRejectsRecordsThatWouldBreakFraming(t *testing.T) {
	root, syncer := newRoot(t)
	writer := openWriter(t, root, "pr-comments", syncer)

	// Every separator a line-oriented reader may split on, not only the one
	// this file is framed with: a record carrying a raw U+2028 is one write
	// here and two records to a Unicode-aware consumer.
	rejected := map[string]string{
		"empty":               "",
		"embedded newline":    "{\"id\":\"a\"}\n{\"id\":\"b\"}",
		"trailing newline":    "{\"id\":\"a\"}\n",
		"carriage return":     "{\"id\":\"a\r\"}",
		"next line":           "{\"id\":\"a\u0085b\"}",
		"line separator":      "{\"id\":\"a\u2028b\"}",
		"paragraph separator": "{\"id\":\"a\u2029b\"}",
	}
	for name, record := range rejected {
		if err := writer.Append([]byte(record)); err == nil {
			t.Errorf("%s: accepted %q", name, record)
		}
	}

	if got := readSpool(t, root, "pr-comments"); got != "" {
		t.Errorf("rejected records reached the spool: %q", got)
	}
}

func TestOpenRejectsASecondWriterForTheSameSource(t *testing.T) {
	root, syncer := newRoot(t)
	openWriter(t, root, "pr-comments", syncer)

	_, err := Open(root, "pr-comments", syncer)
	if !errors.Is(err, ErrLocked) {
		t.Fatalf("second writer got %v, want ErrLocked", err)
	}
}

func TestOpenGuardsEachSourceIndependently(t *testing.T) {
	root, syncer := newRoot(t)
	openWriter(t, root, "pr-comments", syncer)
	openWriter(t, root, "ci-status", syncer)
}

// Deleting the lock file on release would let the next writer lock a fresh
// inode while a straggler still holds the old one.
func TestCloseReleasesTheGuardAndKeepsTheLockFile(t *testing.T) {
	root, syncer := newRoot(t)
	writer := openWriter(t, root, "pr-comments", syncer)

	if err := writer.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	if _, err := os.Stat(root.writerLockPath("pr-comments")); err != nil {
		t.Errorf("lock file gone after close: %v", err)
	}
	openWriter(t, root, "pr-comments", syncer)
}

func TestOpenRepairsATornTail(t *testing.T) {
	// A record longer than one scan chunk forces the backward scan to walk
	// more than a single buffer.
	longFragment := strings.Repeat("x", tailScanChunk*2)

	cases := map[string]struct {
		seeded string
		want   string
	}{
		"torn final record":        {seeded: "{\"id\":\"a\"}\n{\"id\":\"b\"", want: "{\"id\":\"a\"}\n"},
		"nothing ever completed":   {seeded: "{\"id\":\"a\"", want: ""},
		"complete file untouched":  {seeded: "{\"id\":\"a\"}\n", want: "{\"id\":\"a\"}\n"},
		"empty file untouched":     {seeded: "", want: ""},
		"fragment spans chunks":    {seeded: "{\"id\":\"a\"}\n" + longFragment, want: "{\"id\":\"a\"}\n"},
		"fragment is only newline": {seeded: "\n", want: "\n"},
	}

	for name, testCase := range cases {
		t.Run(name, func(t *testing.T) {
			root, syncer := newRoot(t)
			seedSpool(t, root, "pr-comments", testCase.seeded)

			writer := openWriter(t, root, "pr-comments", syncer)
			if got := readSpool(t, root, "pr-comments"); got != testCase.want {
				t.Fatalf("after repair spool holds %q, want %q", truncate(got), truncate(testCase.want))
			}

			if err := writer.Append([]byte(`{"id":"z"}`)); err != nil {
				t.Fatalf("append after repair: %v", err)
			}
			if got := readSpool(t, root, "pr-comments"); got != testCase.want+"{\"id\":\"z\"}\n" {
				t.Errorf("append after repair produced %q", truncate(got))
			}
		})
	}
}

func truncate(value string) string {
	const limit = 60
	if len(value) <= limit {
		return value
	}

	return value[:limit] + "..."
}

// A syncer carries the durability mode and the network-filesystem verdict of
// the directory it probed; accepting one probed elsewhere would attest a
// filesystem this spool never touches.
func TestOpenRejectsASyncerProbedElsewhere(t *testing.T) {
	root, _ := newRoot(t)
	elsewhere, err := durability.Probe(t.TempDir())
	if err != nil {
		t.Fatalf("probe: %v", err)
	}

	if _, err := Open(root, "pr-comments", elsewhere); !errors.Is(err, ErrSyncerRoot) {
		t.Fatalf("open with a foreign syncer returned %v, want ErrSyncerRoot", err)
	}
}

// A failed write can leave a fragment, and the next append would fuse onto it.
func TestAppendRefusesEverythingAfterAFailedWrite(t *testing.T) {
	root, syncer := newRoot(t)
	writer := openWriter(t, root, "pr-comments", syncer)

	if err := writer.Append([]byte(`{"id":"a"}`)); err != nil {
		t.Fatalf("first append: %v", err)
	}
	// Closing the descriptor underneath is the cheapest way to make the next
	// write fail the way a full disk would.
	if err := writer.events.Close(); err != nil {
		t.Fatalf("close events file: %v", err)
	}

	if err := writer.Append([]byte(`{"id":"b"}`)); err == nil {
		t.Fatal("append succeeded on a closed file")
	}
	if err := writer.Append([]byte(`{"id":"c"}`)); !errors.Is(err, ErrBroken) {
		t.Fatalf("append after a failed write returned %v, want ErrBroken", err)
	}

	if got := readSpool(t, root, "pr-comments"); got != "{\"id\":\"a\"}\n" {
		t.Errorf("spool holds %q after the failed appends", got)
	}
}

// A failed sync leaves a framed but undurable record, and on Linux a later
// sync can succeed after the kernel has dropped the dirty page.
func TestAppendRefusesEverythingAfterAFailedSync(t *testing.T) {
	root, syncer := newRoot(t)
	writer := openWriter(t, root, "pr-comments", syncer)

	// An unprobed syncer refuses every sync, which stands in for a filesystem
	// that fails one without needing a filesystem that does.
	writer.syncer = durability.Syncer{}
	if err := writer.Append([]byte(`{"id":"a"}`)); err == nil {
		t.Fatal("append reported success without a durable sync")
	}
	if err := writer.Append([]byte(`{"id":"b"}`)); !errors.Is(err, ErrBroken) {
		t.Fatalf("append after a failed sync returned %v, want ErrBroken", err)
	}

	if got := readSpool(t, root, "pr-comments"); got != "{\"id\":\"a\"}\n" {
		t.Errorf("spool holds %q; the refused append should not have landed", got)
	}
}

func TestOpenRefusesAnUnprobedSyncer(t *testing.T) {
	root, _ := newRoot(t)

	var unprobed durability.Syncer
	if _, err := Open(root, "pr-comments", unprobed); !errors.Is(err, durability.ErrUnprobed) {
		t.Fatalf("open with an unprobed syncer returned %v, want ErrUnprobed", err)
	}
}

// A symlink at either state path would move the spool or its guard onto a
// filesystem whose durability and locking were never verified.
func TestOpenRejectsSymlinkedState(t *testing.T) {
	elsewhere := t.TempDir()

	cases := map[string]func(root Root) string{
		"spool file": func(root Root) string { return root.eventsPath("pr-comments") },
		"lock file":  func(root Root) string { return root.writerLockPath("pr-comments") },
	}

	for name, pathOf := range cases {
		t.Run(name, func(t *testing.T) {
			root, syncer := newRoot(t)
			if err := os.MkdirAll(root.eventsDir(), 0o755); err != nil {
				t.Fatalf("create events dir: %v", err)
			}
			if err := os.MkdirAll(root.writerLockDir(), 0o755); err != nil {
				t.Fatalf("create lock dir: %v", err)
			}

			target := filepath.Join(elsewhere, name+".target")
			if err := os.WriteFile(target, nil, 0o644); err != nil {
				t.Fatalf("create target: %v", err)
			}
			if err := os.Symlink(target, pathOf(root)); err != nil {
				t.Fatalf("symlink: %v", err)
			}

			_, err := Open(root, "pr-comments", syncer)
			if err == nil {
				t.Fatalf("opened a spool whose %s is a symlink", name)
			}
			t.Logf("%s symlink refused: %v", name, err)
		})
	}
}

func TestOpenRejectsASourceThatIsNotASlug(t *testing.T) {
	root, syncer := newRoot(t)

	for _, source := range []string{"", "../escape", "PR-Comments", "pr comments"} {
		if _, err := Open(root, source, syncer); err == nil {
			t.Errorf("source %q accepted", source)
		}
	}

	if _, err := os.Stat(filepath.Join(string(root), "events")); err == nil {
		t.Error("a rejected source created state directories")
	}
}
