package observer

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// Purging keeps the capture within its size bound, oldest segments first, and deletes
// segments older than the age bound; a torn final record does not hide earlier ones.
func TestCapturePurgeAndDamage(t *testing.T) {
	dir := t.TempDir()
	key := bytes.Repeat([]byte{1}, 32)
	now := time.Now()
	c, err := OpenCapture(dir, key, 12<<20, 24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	c.now = func() time.Time { return now }
	record := bytes.Repeat([]byte("x"), 1<<20)
	for i := 0; i < 20; i++ { // 20 MiB in 8 MiB segments
		if err := c.Append(record); err != nil {
			t.Fatal(err)
		}
		now = now.Add(time.Millisecond)
	}
	segments, _ := Segments(dir)
	var total int64
	for _, s := range segments {
		total += s.Size
	}
	if total > 12<<20 || len(segments) < 3 {
		t.Fatalf("%d bytes in %d segments", total, len(segments))
	}
	newest := segments[len(segments)-1].Name
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	// Tear the newest segment's last record.
	path := filepath.Join(dir, newest)
	info, _ := os.Stat(path)
	if err := os.Truncate(path, info.Size()-10); err != nil {
		t.Fatal(err)
	}
	n := 0
	if _, err := ReadCapture(dir, key, func(r []byte) error { n++; return nil }); err != nil || n == 0 {
		t.Fatalf("read %d: %v", n, err)
	}
	// Age: everything older than a day goes, the open segment included.
	old := now.Add(-48 * time.Hour)
	for _, s := range segments {
		_ = os.Chtimes(filepath.Join(dir, s.Name), old, old)
	}
	if deleted, err := c.Purge(); err != nil || deleted != len(segments) {
		t.Fatalf("deleted %d of %d: %v", deleted, len(segments), err)
	}
	if _, err := OpenCapture(dir, []byte("short"), 0, 0); err == nil {
		t.Fatal("accepted a short key")
	}
}

func TestLoadKey(t *testing.T) {
	keys := &memoryKeys{}
	if _, err := LoadKey(keys, false); err == nil {
		t.Fatal("made up a key")
	}
	a, err := LoadKey(keys, true)
	if err != nil || len(a) != 32 {
		t.Fatal(err)
	}
	if b, _ := LoadKey(keys, true); !bytes.Equal(a, b) {
		t.Fatal("key changed")
	}
	keys.values[CaptureKeyName] = "bad"
	if _, err := LoadKey(keys, true); err == nil {
		t.Fatal("accepted an invalid stored key")
	}
}
