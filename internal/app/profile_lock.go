package app

import (
	"errors"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"time"

	"github.com/accessdurin/cli/internal/clierror"
	"github.com/accessdurin/cli/internal/contracts"
	"github.com/accessdurin/cli/internal/jsjson"
	"github.com/accessdurin/cli/internal/zod"
)

// DefaultLockPatience is how long WithProfileLock waits for another process by default.
const DefaultLockPatience = 90 * time.Second

var lockOwnerSchema = zod.Object(
	zod.F("pid", zod.Number().Int().Positive()),
	zod.F("nonce", zod.UUID()),
).Strict()

func acquireLock(path, owner string) (bool, error) {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		if errors.Is(err, fs.ErrExist) {
			return false, nil
		}
		return false, err
	}
	_, werr := f.WriteString(owner)
	cerr := f.Close()
	if werr != nil {
		return false, werr
	}
	return true, cerr
}

// processIsGone is process.kill(pid, 0) failing with ESRCH. Node refuses (throws a TypeError,
// which is not ESRCH) a pid that is not a 32-bit integer.
func processIsGone(pid float64) bool {
	if pid > math.MaxInt32 {
		return false
	}
	return processMissing(int(pid))
}

func assertLockOwner(path string) error {
	raw, err := readText(path)
	if err != nil {
		raw = ""
	}
	value, err := jsjson.ParseString(raw)
	if err != nil {
		value = jsjson.Null{}
	}
	out, zerr := zod.SafeParse(lockOwnerSchema, value)
	if zerr != nil {
		return nil
	}
	pid, _ := out.(*jsjson.Object).GetNumber("pid")
	if processIsGone(pid) {
		return clierror.New("A previous Durin process was interrupted. With Durin and its bridges stopped, remove the abandoned lock " + path + ", then retry with the same idempotency key.")
	}
	return nil
}

func removeOwnedLock(path, owner string) {
	if current, err := readText(path); err == nil && current == owner {
		_ = rmForce(path)
	}
}

func waitForLock(path, owner string, deadline time.Time) error {
	for {
		acquired, err := acquireLock(path, owner)
		if err != nil || acquired {
			return err
		}
		if err := assertLockOwner(path); err != nil {
			return err
		}
		if !time.Now().Before(deadline) {
			return clierror.New("This profile is busy in another Durin process. Finish that request, then retry with the same idempotency key.")
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// WithProfileLock runs work while holding <directory>/locks/<profileID>.lock, waiting up to
// patience for another process to release it. An abandoned lock (its process is gone) is
// never removed automatically, since another process could be racing to do the same.
func WithProfileLock[T any](directory, profileID string, work func() (T, error), patience time.Duration) (T, error) {
	var zero T
	locks := filepath.Join(directory, "locks")
	if err := os.MkdirAll(locks, 0o700); err != nil {
		return zero, err
	}
	if _, err := zod.Parse(contracts.ProfileIDSchema, jsjson.String(profileID)); err != nil {
		return zero, err
	}
	path := filepath.Join(locks, profileID+".lock")
	owner := jsjson.Stringify(jsjson.Obj(jsjson.KV("pid", jsjson.Int(int64(os.Getpid()))), jsjson.KV("nonce", jsjson.String(randomUUID()))))
	if err := waitForLock(path, owner, time.Now().Add(patience)); err != nil {
		return zero, err
	}
	defer removeOwnedLock(path, owner)
	return work()
}
