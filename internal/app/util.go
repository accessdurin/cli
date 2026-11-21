// Package app ports the TypeScript CLI's modules (src/*.ts), one Go file per module, so the
// two can be compared side by side. Shared helpers that reproduce Node's built-ins (fs
// errors, path.isAbsolute, crypto.randomUUID, Date#toISOString, ...) live in this file.
package app

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"math"
	"math/big"
	"net/http"
	"os"
	"os/user"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/accessdurin/cli/internal/jsjson"
)

// randomUUID is crypto.randomUUID(): a random RFC 9562 version 4 UUID.
func randomUUID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// isoString is Date#toISOString(): UTC with millisecond precision.
func isoString(t time.Time) string { return t.UTC().Format("2006-01-02T15:04:05.000Z") }

// textDigest is sha256 hex of a string's UTF-8 bytes (src/managed-files.ts).
func textDigest(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

// missing reports a Node ENOENT: the path, or a directory on it, does not exist.
func missing(err error) bool {
	return errors.Is(err, fs.ErrNotExist) && (runtime.GOOS == "windows" || !errors.Is(err, syscall.ENOTDIR))
}

// readText is readFile(path, 'utf8'): invalid UTF-8 becomes U+FFFD.
func readText(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return jsjson.DecodeUTF8(data), nil
}

// rmForce is fs.rm(path, {force: true}): a missing path is fine, a directory is an error.
func rmForce(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		if missing(err) {
			return nil
		}
		return err
	}
	if info.IsDir() {
		return &fs.PathError{Op: "rm", Path: path, Err: syscall.EISDIR}
	}
	if err := os.Remove(path); err != nil && !missing(err) {
		return err
	}
	return nil
}

// writeExclusive is writeFile(path, value, {mode, flag: 'wx'}).
func writeExclusive(path string, value []byte, mode os.FileMode) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return err
	}
	if _, err := f.Write(value); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// isAbsolute is Node's path.isAbsolute for the given platform's path flavor.
func isAbsolute(p string, windows bool) bool {
	if !windows {
		return strings.HasPrefix(p, "/")
	}
	if p == "" {
		return false
	}
	separator := func(c byte) bool { return c == '/' || c == '\\' }
	if separator(p[0]) {
		return true
	}
	letter := (p[0] >= 'A' && p[0] <= 'Z') || (p[0] >= 'a' && p[0] <= 'z')
	return letter && len(p) > 2 && p[1] == ':' && separator(p[2])
}

// isAbsoluteHere is path.isAbsolute on this platform.
func isAbsoluteHere(p string) bool { return isAbsolute(p, runtime.GOOS == "windows") }

// Env reads environment variables as Node's process.env does: a value that is not valid
// UTF-8 has each invalid sequence replaced with U+FFFD.
type Env interface {
	Lookup(key string) (string, bool)
}

// EnvMap is an Env backed by a map, for tests.
type EnvMap map[string]string

// Lookup implements Env.
func (m EnvMap) Lookup(key string) (string, bool) { v, ok := m[key]; return v, ok }

type processEnv struct{}

func (processEnv) Lookup(key string) (string, bool) {
	v, ok := os.LookupEnv(key)
	return jsjson.DecodeUTF8([]byte(v)), ok
}

// ProcessEnv is process.env.
var ProcessEnv Env = processEnv{}

// Homedir is os.homedir(): $HOME (%USERPROFILE% on Windows) when set, else the account's
// home directory.
func Homedir() string {
	key := "HOME"
	if runtime.GOOS == "windows" {
		key = "USERPROFILE"
	}
	if v, ok := ProcessEnv.Lookup(key); ok && v != "" {
		return v
	}
	if u, err := user.Current(); err == nil {
		return u.HomeDir
	}
	return ""
}

// resolvePath is path.resolve(p): absolute against the working directory, cleaned.
func resolvePath(p string) string {
	abs, err := filepath.Abs(p)
	if err != nil {
		return filepath.Clean(p)
	}
	return abs
}

// fetchInit is the part of fetch's RequestInit the CLI sets. Headers keep their order.
type fetchInit struct {
	Method  string
	Header  [][2]string
	Body    *string       // nil for no body
	Timeout time.Duration // AbortSignal.timeout(Timeout); 0 for none
}

// cancelOnClose releases a request's timeout once its body is closed.
type cancelOnClose struct {
	io.ReadCloser
	cancel context.CancelFunc
}

func (b cancelOnClose) Close() error {
	err := b.ReadCloser.Close()
	b.cancel()
	return err
}

// fetch is fetch(url, {...init, redirect: 'error'}) on client (nil for http.DefaultClient).
// Its timeout covers reading the body, as an AbortSignal does; closing the body releases it.
func fetch(ctx context.Context, client *http.Client, url string, init fetchInit) (*http.Response, error) {
	if client == nil {
		client = http.DefaultClient
	}
	noRedirect := *client
	noRedirect.CheckRedirect = func(*http.Request, []*http.Request) error { return ErrRedirect }
	cancel := context.CancelFunc(func() {})
	if init.Timeout > 0 {
		ctx, cancel = context.WithTimeout(ctx, init.Timeout)
	}
	var body io.Reader
	if init.Body != nil {
		body = strings.NewReader(*init.Body)
	}
	method := init.Method
	if method == "" {
		method = http.MethodGet
	}
	req, err := http.NewRequestWithContext(ctx, method, url, body)
	if err != nil {
		cancel()
		return nil, err
	}
	for _, h := range init.Header {
		req.Header.Set(h[0], h[1])
	}
	resp, err := noRedirect.Do(req)
	if err != nil {
		cancel()
		return nil, err
	}
	resp.Body = cancelOnClose{resp.Body, cancel}
	return resp, nil
}

// responseOK is Response#ok.
func responseOK(resp *http.Response) bool { return resp.StatusCode >= 200 && resp.StatusCode <= 299 }

// responseJSON is Response#json(): the whole body, decoded as UTF-8 without a BOM, parsed as
// JSON. It closes the body.
func responseJSON(resp *http.Response) (jsjson.Value, error) {
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	return jsjson.ParseString(jsjson.DecodeUTF8StripBOM(data))
}

// jsWhiteSpace is JavaScript's WhiteSpace or LineTerminator, which String.prototype.trim and
// Number() strip.
func jsWhiteSpace(c rune) bool {
	switch c {
	case 0x09, 0x0A, 0x0B, 0x0C, 0x0D, 0x20, 0xA0, 0x1680, 0x2028, 0x2029, 0x202F, 0x205F, 0x3000, 0xFEFF:
		return true
	}
	return c >= 0x2000 && c <= 0x200A
}

// jsTrim is String.prototype.trim.
func jsTrim(s string) string { return strings.TrimFunc(s, jsWhiteSpace) }

var (
	jsDecimal = regexp.MustCompile(`^[+-]?(?:Infinity|(?:\d+\.?\d*|\.\d+)(?:[eE][+-]?\d+)?)$`)
	jsRadix   = regexp.MustCompile(`^0(?:[xX][\da-fA-F]+|[oO][0-7]+|[bB][01]+)$`)
)

// jsNumber is Number(string): NaN unless the trimmed string is empty (0), a decimal literal,
// Infinity, or a 0x/0o/0b integer.
func jsNumber(s string) float64 {
	s = jsTrim(s)
	switch {
	case s == "":
		return 0
	case jsRadix.MatchString(s):
		base := map[byte]int{'x': 16, 'X': 16, 'o': 8, 'O': 8, 'b': 2, 'B': 2}[s[1]]
		n, _ := new(big.Int).SetString(s[2:], base)
		f, _ := new(big.Float).SetInt(n).Float64()
		return f
	case !jsDecimal.MatchString(s):
		return math.NaN()
	case strings.HasSuffix(s, "Infinity"):
		if s[0] == '-' {
			return math.Inf(-1)
		}
		return math.Inf(1)
	}
	f, _ := strconv.ParseFloat(s, 64) // out of range is ±Inf or ±0, as in JavaScript
	return f
}
