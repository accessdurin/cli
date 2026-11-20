package observer

import (
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Process is what the observer knows about a process.
type Process struct {
	PID   int
	PPID  int
	Exe   string
	Args  []string // the first few arguments
	Start uint64   // OS start time, to tell a reused pid apart (0 when unknown)
}

// ProcessInfo looks a process up in the OS.
type ProcessInfo interface {
	Process(pid int) (Process, bool)
}

// ProcessCache bounds and ages the OS lookups: entries live for ttl and at most max are
// kept; the oldest are evicted first.
type ProcessCache struct {
	info ProcessInfo
	ttl  time.Duration
	max  int
	now  func() time.Time

	mu      sync.Mutex
	entries map[int]processEntry
	learned map[int]learnedHarness // pid → harness, from hook events
}

type processEntry struct {
	p       Process
	ok      bool
	fetched time.Time
}

type learnedHarness struct {
	harness string
	at      time.Time
}

// NewProcessCache wraps info.
func NewProcessCache(info ProcessInfo, ttl time.Duration, max int) *ProcessCache {
	return &ProcessCache{info: info, ttl: ttl, max: max, now: time.Now, entries: map[int]processEntry{}, learned: map[int]learnedHarness{}}
}

// Get returns the process, from the cache when fresh.
func (c *ProcessCache) Get(pid int) (Process, bool) {
	if pid <= 0 || c.info == nil {
		return Process{}, false
	}
	now := c.now()
	c.mu.Lock()
	if e, ok := c.entries[pid]; ok && now.Sub(e.fetched) < c.ttl {
		c.mu.Unlock()
		return e.p, e.ok
	}
	c.mu.Unlock()
	p, ok := c.info.Process(pid)
	c.mu.Lock()
	c.entries[pid] = processEntry{p, ok, now}
	c.evictLocked(now)
	c.mu.Unlock()
	return p, ok
}

// Learn notes that pid runs harness, as a hook event from that harness says. Hooks run as a
// child of the harness (or of a shell it started), so the hook's parent and grandparent are
// recorded.
func (c *ProcessCache) Learn(pid int, harness string) {
	if pid <= 0 || harness == "" {
		return
	}
	c.mu.Lock()
	c.learned[pid] = learnedHarness{harness, c.now()}
	c.evictLocked(c.now())
	c.mu.Unlock()
}

func (c *ProcessCache) evictLocked(now time.Time) {
	for pid, e := range c.entries {
		if now.Sub(e.fetched) >= c.ttl {
			delete(c.entries, pid)
		}
	}
	for len(c.entries) > c.max {
		oldest, at := 0, now
		for pid, e := range c.entries {
			if !e.fetched.After(at) {
				oldest, at = pid, e.fetched
			}
		}
		delete(c.entries, oldest)
	}
	for pid, l := range c.learned {
		if now.Sub(l.at) >= 24*time.Hour {
			delete(c.learned, pid)
		}
	}
	for len(c.learned) > c.max {
		oldest, at := 0, now
		for pid, l := range c.learned {
			if !l.at.After(at) {
				oldest, at = pid, l.at
			}
		}
		delete(c.learned, oldest)
	}
}

// Size is the number of cached processes.
func (c *ProcessCache) Size() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.entries)
}

// Harness names the harness pid belongs to: one a hook event identified, or one recognized
// from the executable of the process or one of its ancestors.
func (c *ProcessCache) Harness(pid int) string {
	for depth := 0; depth < 8 && pid > 1; depth++ {
		c.mu.Lock()
		l, ok := c.learned[pid]
		c.mu.Unlock()
		if ok {
			return l.harness
		}
		p, ok := c.Get(pid)
		if !ok {
			return ""
		}
		if h := HarnessOf(p); h != "" {
			return h
		}
		pid = p.PPID
	}
	return ""
}

// harnessExecutables maps executable names (lowercase, without .exe) to harness ids.
var harnessExecutables = map[string]string{
	"claude": "claude", "codex": "codex", "cursor": "cursor", "cursor-agent": "cursor",
	"windsurf": "windsurf", "gemini": "gemini", "copilot": "copilot", "opencode": "opencode",
	"pi": "pi", "kiro": "kiro", "kiro-cli": "kiro",
}

// harnessPackages recognizes harnesses that run as scripts under node or bun.
var harnessPackages = map[string]string{
	"@anthropic-ai/claude-code": "claude", "@openai/codex": "codex", "@google/gemini-cli": "gemini",
	"@github/copilot": "copilot", "opencode-ai": "opencode", "@mariozechner/pi-coding-agent": "pi",
}

// HarnessOf recognizes a harness from a process's executable or script.
func HarnessOf(p Process) string {
	names := []string{p.Exe}
	if len(p.Args) > 0 {
		names = append(names, p.Args[0])
	}
	if len(p.Args) > 1 {
		names = append(names, p.Args[1])
	}
	for _, name := range names {
		if name == "" {
			continue
		}
		for pkg, h := range harnessPackages {
			if strings.Contains(filepath.ToSlash(name), "/"+pkg+"/") {
				return h
			}
		}
		base := strings.ToLower(strings.TrimSuffix(filepath.Base(filepath.ToSlash(strings.ReplaceAll(name, `\`, "/"))), ".exe"))
		if h, ok := harnessExecutables[base]; ok {
			return h
		}
		// macOS app bundles: /Applications/Cursor.app/Contents/... helpers.
		if i := strings.Index(name, ".app/"); i >= 0 {
			app := strings.ToLower(filepath.Base(name[:i]))
			if h, ok := harnessExecutables[app]; ok {
				return h
			}
		}
	}
	return ""
}
