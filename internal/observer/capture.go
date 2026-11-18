package observer

import (
	"bufio"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// CaptureKeyName is the credential store entry holding the capture key.
const CaptureKeyName = "observer:capture-key"

// segmentMagic starts every capture segment, followed by an 8-byte key fingerprint.
const segmentMagic = "DURINCAP1\n"

// Segment rotation and purge defaults.
const (
	DefaultMaxBytes   = 512 << 20
	DefaultMaxAge     = 7 * 24 * time.Hour
	segmentMaxBytes   = 8 << 20
	segmentMaxAge     = time.Hour
	maxRecordBytes    = MaxFrame + 4096
	purgeEveryRecords = 512
)

// KeyStore is the part of the OS credential store the capture needs.
type KeyStore interface {
	Read(key string) (string, bool, error)
	Write(key, value string) error
	Remove(key string) error
}

// LoadKey returns the capture key from the credential store, creating it on first use when
// create is set. There is no plaintext fallback: without the store there is no capture.
func LoadKey(store KeyStore, create bool) ([]byte, error) {
	value, found, err := store.Read(CaptureKeyName)
	if err != nil {
		return nil, err
	}
	if found {
		key, err := base64.StdEncoding.DecodeString(value)
		if err != nil || len(key) != 32 {
			return nil, errors.New("observer: the stored capture key is invalid")
		}
		return key, nil
	}
	if !create {
		return nil, errors.New("observer: no capture key")
	}
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return nil, err
	}
	if err := store.Write(CaptureKeyName, base64.StdEncoding.EncodeToString(key)); err != nil {
		return nil, err
	}
	return key, nil
}

// Capture is an append-only, encrypted, bounded log of records split into segment files
// named by creation time, so that purging oldest-first is deleting files in name order.
//
// Each record is sealed on its own with AES-256-GCM (random nonce, the segment's name as
// additional data), so a damaged or truncated tail never hides earlier records.
type Capture struct {
	dir      string
	aead     cipher.AEAD
	keyID    [8]byte
	maxBytes int64
	maxAge   time.Duration
	now      func() time.Time

	mu          sync.Mutex
	file        *os.File
	writer      *bufio.Writer
	name        string
	size        int64
	opened      time.Time
	sincePurge  int
	closedBytes int64 // size of the other segments at the last purge
	segmentMax  int64
}

// OpenCapture opens the capture directory with key. maxBytes and maxAge bound what is kept.
func OpenCapture(dir string, key []byte, maxBytes int64, maxAge time.Duration) (*Capture, error) {
	if len(key) != 32 {
		return nil, errors.New("observer: capture key must be 32 bytes")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	if maxBytes <= 0 {
		maxBytes = DefaultMaxBytes
	}
	if maxAge <= 0 {
		maxAge = DefaultMaxAge
	}
	c := &Capture{dir: dir, aead: aead, maxBytes: maxBytes, maxAge: maxAge, now: time.Now, segmentMax: min(segmentMaxBytes, max(maxBytes/4, 64<<10))}
	sum := sha256.Sum256(append([]byte("durin capture key id\x00"), key...))
	copy(c.keyID[:], sum[:8])
	return c, nil
}

// Append seals and appends records (each one JSON document) and flushes them.
func (c *Capture) Append(records ...[]byte) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, record := range records {
		if len(record) > maxRecordBytes {
			continue
		}
		if err := c.ensureSegment(int64(len(record))); err != nil {
			return err
		}
		if c.file == nil { // the purge after opening removed it (a clock far ahead)
			if err := c.ensureSegment(int64(len(record))); err != nil || c.file == nil {
				return errors.Join(err, errors.New("observer: cannot keep a capture segment open"))
			}
		}
		nonce := make([]byte, c.aead.NonceSize())
		if _, err := rand.Read(nonce); err != nil {
			return err
		}
		sealed := c.aead.Seal(nonce, nonce, record, []byte(c.name))
		var header [4]byte
		binary.BigEndian.PutUint32(header[:], uint32(len(sealed)))
		if _, err := c.writer.Write(header[:]); err != nil {
			return err
		}
		if _, err := c.writer.Write(sealed); err != nil {
			return err
		}
		c.size += int64(4 + len(sealed))
		c.sincePurge++
	}
	if c.writer != nil {
		if err := c.writer.Flush(); err != nil {
			return err
		}
	}
	if c.sincePurge >= purgeEveryRecords || c.closedBytes+c.size > c.maxBytes {
		c.sincePurge = 0
		_, err := c.purgeLocked()
		return err
	}
	return nil
}

func (c *Capture) ensureSegment(next int64) error {
	now := c.now()
	if c.file != nil && c.size+next < c.segmentMax && now.Sub(c.opened) < segmentMaxAge {
		return nil
	}
	if err := c.closeLocked(); err != nil {
		return err
	}
	name := fmt.Sprintf("%020d.seg", now.UnixNano())
	f, err := os.OpenFile(filepath.Join(c.dir, name), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	header := append([]byte(segmentMagic), c.keyID[:]...)
	if _, err := f.Write(header); err != nil {
		f.Close()
		return err
	}
	c.file, c.writer, c.name, c.size, c.opened = f, bufio.NewWriterSize(f, 64<<10), name, int64(len(header)), now
	_, err = c.purgeLocked()
	return err
}

func (c *Capture) closeLocked() error {
	if c.file == nil {
		return nil
	}
	err := c.writer.Flush()
	if serr := c.file.Sync(); err == nil {
		err = serr
	}
	if cerr := c.file.Close(); err == nil {
		err = cerr
	}
	c.file, c.writer = nil, nil
	return err
}

// Close flushes and closes the current segment.
func (c *Capture) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.closeLocked()
}

// SegmentInfo describes one segment file.
type SegmentInfo struct {
	Name     string
	Size     int64
	Modified time.Time
}

// Segments lists the capture's segment files, oldest first.
func Segments(dir string) ([]SegmentInfo, error) {
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []SegmentInfo
	for _, e := range entries {
		if !e.Type().IsRegular() || !strings.HasSuffix(e.Name(), ".seg") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		out = append(out, SegmentInfo{Name: e.Name(), Size: info.Size(), Modified: info.ModTime()})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// Purge deletes segments, oldest first, until the capture fits maxBytes, and every segment
// last written before maxAge ago. The segment being written is kept unless it is too old.
// It returns how many segments it deleted.
func (c *Capture) Purge() (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.purgeLocked()
}

func (c *Capture) purgeLocked() (int, error) {
	segments, err := Segments(c.dir)
	if err != nil {
		return 0, err
	}
	var total int64
	for _, s := range segments {
		total += s.Size
	}
	cutoff := c.now().Add(-c.maxAge)
	deleted := 0
	for _, s := range segments {
		current := s.Name == c.name && c.file != nil
		modified := s.Modified
		if current && c.opened.After(modified) {
			modified = c.opened
		}
		tooOld := modified.Before(cutoff)
		if current && !tooOld {
			continue
		}
		if total <= c.maxBytes && !tooOld {
			continue
		}
		if current {
			if err := c.closeLocked(); err != nil {
				return deleted, err
			}
		}
		if err := os.Remove(filepath.Join(c.dir, s.Name)); err != nil && !errors.Is(err, os.ErrNotExist) {
			return deleted, err
		}
		total -= s.Size
		deleted++
	}
	c.closedBytes = total
	if c.file != nil {
		for _, s := range segments {
			if s.Name == c.name {
				c.closedBytes -= s.Size
			}
		}
	}
	return deleted, nil
}

// ReadCapture decrypts every record in dir, oldest first, calling fn for each. Segments
// sealed with another key (for example after the key was reset) are skipped and counted.
func ReadCapture(dir string, key []byte, fn func(record []byte) error) (skipped int, err error) {
	c, err := OpenCapture(dir, key, 0, 0)
	if err != nil {
		return 0, err
	}
	segments, err := Segments(dir)
	if err != nil {
		return 0, err
	}
	for _, s := range segments {
		ok, err := c.readSegment(s.Name, fn)
		if err != nil {
			return skipped, err
		}
		if !ok {
			skipped++
		}
	}
	return skipped, nil
}

func (c *Capture) readSegment(name string, fn func([]byte) error) (bool, error) {
	f, err := os.Open(filepath.Join(c.dir, name))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return true, nil
		}
		return false, err
	}
	defer f.Close()
	r := bufio.NewReader(f)
	header := make([]byte, len(segmentMagic)+8)
	if _, err := io.ReadFull(r, header); err != nil || string(header[:len(segmentMagic)]) != segmentMagic || string(header[len(segmentMagic):]) != string(c.keyID[:]) {
		return false, nil
	}
	for {
		var size [4]byte
		if _, err := io.ReadFull(r, size[:]); err != nil {
			return true, nil // end of segment, or a torn final record
		}
		n := binary.BigEndian.Uint32(size[:])
		if n > maxRecordBytes+64 || int(n) < c.aead.NonceSize() {
			return true, nil
		}
		sealed := make([]byte, n)
		if _, err := io.ReadFull(r, sealed); err != nil {
			return true, nil
		}
		nonce := sealed[:c.aead.NonceSize()]
		plain, err := c.aead.Open(nil, nonce, sealed[c.aead.NonceSize():], []byte(name))
		if err != nil {
			continue
		}
		if err := fn(plain); err != nil {
			return true, err
		}
	}
}

// PurgeAll deletes every segment in dir.
func PurgeAll(dir string) (int, error) {
	segments, err := Segments(dir)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, s := range segments {
		if err := os.Remove(filepath.Join(dir, s.Name)); err == nil {
			n++
		}
	}
	return n, nil
}
