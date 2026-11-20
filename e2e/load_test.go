package e2e

import (
	"context"
	"fmt"
	"math/rand"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/accessdurin/cli/internal/observer"
)

// TestObserverLoad measures the observer under sustained load: resident memory, CPU while
// idle and loaded, enqueue latency and end-to-end hook latency with the observer up and
// down. It is a measurement, not a regression check, so it only runs on request:
//
//	DURIN_LOAD=1 e2e/run.sh -run TestObserverLoad -v
//
// DURIN_LOAD_SECONDS sets the load phase (default 60).
func TestObserverLoad(t *testing.T) {
	if os.Getenv("DURIN_LOAD") != "1" {
		t.Skip("set DURIN_LOAD=1 to measure the observer")
	}
	requireKeyring(t)
	seconds := 60
	if v, err := strconv.Atoi(os.Getenv("DURIN_LOAD_SECONDS")); err == nil && v > 0 {
		seconds = v
	}
	durin := durinCommand(t)
	root, _ := os.MkdirTemp("", "load")
	defer os.RemoveAll(root)
	config := filepath.Join(root, "config")
	_ = os.MkdirAll(filepath.Join(config, "observer"), 0o700)
	_ = os.WriteFile(filepath.Join(config, "observer", "config.json"), []byte(`{"pollIntervalSeconds":1}`), 0o600)
	env := append(os.Environ(), "DO_NOT_TRACK=1", "DURIN_OBSERVER_RESOLVE=api.openai.com=127.0.0.2,api.anthropic.com=127.0.0.3")
	address := observer.Address(config)

	hook := func() time.Duration {
		cmd := exec.Command(durin[0], append(durin[1:], "hooks", "run", "--harness", "claude", "--config-dir", config, "--event", "PostToolUse")...)
		cmd.Env = env
		cmd.Stdin = strings.NewReader(`{"session_id":"s","tool_name":"Bash","tool_input":{"command":"ls"},"tool_response":{"stdout":"a\nb"}}`)
		start := time.Now()
		if err := cmd.Run(); err != nil {
			t.Fatal(err)
		}
		return time.Since(start)
	}
	hookLatency := func(n int) string {
		var d []time.Duration
		for range n {
			d = append(d, hook())
		}
		return percentiles(d)
	}
	down := hookLatency(100)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cmd := exec.CommandContext(ctx, durin[0], append(durin[1:], "observe", "--config-dir", config)...)
	cmd.Env = env
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	pid := cmd.Process.Pid
	for deadline := time.Now().Add(10 * time.Second); ; time.Sleep(20 * time.Millisecond) {
		if c, err := net.Dial("unix", address); err == nil {
			c.Close()
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("observer did not start")
		}
	}
	time.Sleep(time.Second)
	idleStartRSS, _ := rss(pid)
	idle := cpuPercent(pid, 10*time.Second)
	up := hookLatency(100)

	// Provider connections for the detector: listeners on two pinned addresses and 50 live
	// client connections, re-opened continuously during the load phase.
	var listeners []net.Listener
	for _, a := range []string{"127.0.0.2:0", "127.0.0.3:0"} {
		l, err := net.Listen("tcp", a)
		if err != nil {
			t.Skip("needs 127.0.0.2/127.0.0.3 loopback")
		}
		listeners = append(listeners, l)
		go func() {
			for {
				c, err := l.Accept()
				if err != nil {
					return
				}
				go func() { b := make([]byte, 1); _, _ = c.Read(b); c.Close() }()
			}
		}()
	}
	defer func() {
		for _, l := range listeners {
			l.Close()
		}
	}()

	var sent, failed atomic.Int64
	var mu sync.Mutex
	var enqueue []time.Duration
	stop := make(chan struct{})
	var wg sync.WaitGroup
	payload := func(r *rand.Rand) []byte {
		size := 1 << 10
		switch n := r.Intn(1000); {
		case n == 0:
			size = 1 << 20 // occasional 1 MiB tool output
		case n < 50:
			size = 64 << 10
		}
		return []byte(`{"tool_response":"` + strings.Repeat("x", size) + `"}`)
	}
	const sessions = 32
	for i := range sessions {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			r := rand.New(rand.NewSource(int64(i)))
			for {
				select {
				case <-stop:
					return
				default:
				}
				p := payload(r)
				sctx, scancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
				start := time.Now()
				err := observer.Send(sctx, address, observer.Message{Harness: "claude", Event: "PostToolUse", PID: os.Getpid(), PPID: os.Getppid(), Payload: p})
				elapsed := time.Since(start)
				scancel()
				if err != nil {
					failed.Add(1)
				} else {
					sent.Add(1)
				}
				mu.Lock()
				if len(enqueue) < 200000 {
					enqueue = append(enqueue, elapsed)
				}
				mu.Unlock()
				time.Sleep(time.Duration(r.Intn(4)) * time.Millisecond)
			}
		}(i)
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		var conns []net.Conn
		defer func() {
			for _, c := range conns {
				c.Close()
			}
		}()
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			case <-time.After(100 * time.Millisecond):
			}
			if len(conns) >= 50 {
				conns[0].Close()
				conns = conns[1:]
			}
			if c, err := net.Dial("tcp", listeners[i%2].Addr().String()); err == nil {
				conns = append(conns, c)
			}
		}
	}()
	var peak uint64
	samplerDone := make(chan struct{})
	go func() {
		defer close(samplerDone)
		for {
			select {
			case <-stop:
				return
			case <-time.After(250 * time.Millisecond):
			}
			if r, _ := rss(pid); r > peak {
				peak = r
			}
		}
	}()
	loaded := cpuPercentDuring(pid, time.Duration(seconds)*time.Second)
	close(stop)
	wg.Wait()
	<-samplerDone
	time.Sleep(2 * time.Second)
	afterRSS, hwm := rss(pid)
	settle := cpuPercent(pid, 10*time.Second)
	sctx, scancel := context.WithTimeout(context.Background(), 2*time.Second)
	stats, err := observer.Query(sctx, address)
	scancel()
	if err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	enq := percentiles(enqueue)
	mu.Unlock()
	report := fmt.Sprintf(`observer load test (%ds, %d concurrent sessions, 1 KiB/64 KiB/1 MiB payload mix, 50 rolling provider connections)
  idle: RSS %.1f MiB, CPU %.2f%% (10 s)
  load: %d events sent (%.0f/s), %d send failures; CPU %.1f%%; RSS peak %.1f MiB (sampled), VmHWM %.1f MiB
  after load: RSS %.1f MiB, CPU %.2f%% (10 s)
  observer: received %d, stored %d, dropped %d, detections %d, capture %.1f MiB in %d segments
  enqueue latency (dial+write, client side): %s
  hook latency, durin hooks run --event PostToolUse (process start to exit), observer down: %s
  hook latency, observer up (idle): %s
`, seconds, sessions, float64(idleStartRSS)/(1<<20), idle,
		sent.Load(), float64(sent.Load())/float64(seconds), failed.Load(), loaded, float64(peak)/(1<<20), float64(hwm)/(1<<20),
		float64(afterRSS)/(1<<20), settle,
		stats.Received, stats.Stored, stats.Dropped, stats.Detections, float64(stats.Capture.Bytes)/(1<<20), stats.Capture.Segments,
		enq, down, up)
	t.Log("\n" + report)
	if out := os.Getenv("DURIN_LOAD_REPORT"); out != "" {
		_ = os.WriteFile(out, []byte(report), 0o644)
	}
	if hwm >= 200<<20 {
		t.Errorf("peak RSS %d bytes exceeds 200 MB", hwm)
	}
	_ = cmd.Process.Signal(os.Interrupt)
	_ = cmd.Wait()
}

func percentiles(d []time.Duration) string {
	if len(d) == 0 {
		return "no samples"
	}
	sort.Slice(d, func(i, j int) bool { return d[i] < d[j] })
	at := func(p float64) time.Duration { return d[min(len(d)-1, int(p*float64(len(d))))] }
	return fmt.Sprintf("p50 %v, p95 %v, p99 %v, max %v (n=%d)", at(0.5).Round(10*time.Microsecond), at(0.95).Round(10*time.Microsecond), at(0.99).Round(10*time.Microsecond), d[len(d)-1].Round(10*time.Microsecond), len(d))
}

// rss reads VmRSS and VmHWM of pid.
func rss(pid int) (current, peak uint64) {
	data, _ := os.ReadFile(fmt.Sprintf("/proc/%d/status", pid))
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		kb, _ := strconv.ParseUint(fields[1], 10, 64)
		switch fields[0] {
		case "VmRSS:":
			current = kb << 10
		case "VmHWM:":
			peak = kb << 10
		}
	}
	return current, peak
}

func cpuTicks(pid int) uint64 {
	data, _ := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	s := string(data)
	fields := strings.Fields(s[strings.LastIndexByte(s, ')')+1:])
	if len(fields) < 13 {
		return 0
	}
	u, _ := strconv.ParseUint(fields[11], 10, 64)
	k, _ := strconv.ParseUint(fields[12], 10, 64)
	return u + k
}

// cpuPercent is the share of one CPU pid used over d (clock ticks are 1/100 s on Linux).
func cpuPercent(pid int, d time.Duration) float64 {
	return cpuPercentDuring(pid, d)
}

func cpuPercentDuring(pid int, d time.Duration) float64 {
	before := cpuTicks(pid)
	start := time.Now()
	time.Sleep(d)
	return float64(cpuTicks(pid)-before) / 100 / time.Since(start).Seconds() * 100
}
