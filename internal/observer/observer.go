package observer

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/netip"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// ProviderTraffic is one detection of AI-provider traffic.
type ProviderTraffic struct {
	Provider   string         // provider id, e.g. "openai"
	Host       string         // the provider hostname that matched
	Source     string         // "socket" or a DNS source name
	Phase      string         // "open" or "close" for sockets, "query" for DNS
	PID        int            // 0 when unknown
	Executable string         // empty when unknown
	Harness    string         // empty when unknown
	Remote     netip.AddrPort // zero for DNS
	FirstSeen  time.Time
	LastSeen   time.Time
}

// ProviderTrafficHook is invoked for every detection. It is the extension point for
// capabilities built on detection (none yet); hooks run on the detector goroutine and must
// return quickly. They must not intercept, proxy or modify traffic.
type ProviderTrafficHook func(ctx context.Context, traffic ProviderTraffic)

// NoopProviderTrafficHook is the default hook: it does nothing.
func NoopProviderTrafficHook(context.Context, ProviderTraffic) {}

// Options configure an Observer. Zero values select the OS implementations.
type Options struct {
	ConfigDir string
	Address   string // default Address(ConfigDir)
	Config    Config
	Keys      KeyStore // the OS credential store
	Table     SocketTable
	Processes ProcessInfo
	Resolver  Resolver
	DNS       []DNSSource // every source this OS offers; Config.DNSSources selects
	Hooks     []ProviderTrafficHook
	Version   string
	Logf      func(format string, args ...any)

	pollInterval time.Duration // tests only; Config.PollInterval otherwise
}

// Observer is the always-on helper.
type Observer struct {
	opts      Options
	providers []Provider
	cache     *ResolutionCache
	procs     *ProcessCache
	queue     *queue
	started   time.Time
	dns       sourceStates

	captureMu     sync.Mutex
	capture       *Capture
	captureDetail string

	received, stored, dropped, rejected, detections atomic.Uint64
	open                                            atomic.Int64
	tableState                                      atomic.Value // string
}

// Concurrency and memory bounds.
const (
	maxConnections = 32       // hook connections read at once
	queueMaxBytes  = 32 << 20 // records waiting for the writer
	batchInterval  = 200 * time.Millisecond
	memoryLimit    = 96 << 20 // soft limit for the Go heap
	maxTracked     = 4096     // provider connections tracked between polls
)

// New returns an observer; Run starts it.
func New(opts Options) *Observer {
	if opts.Address == "" {
		opts.Address = Address(opts.ConfigDir)
	}
	if opts.Table == nil {
		opts.Table = NewSocketTable()
	}
	if opts.Processes == nil {
		opts.Processes = NewProcessInfo()
	}
	if opts.Logf == nil {
		opts.Logf = func(string, ...any) {}
	}
	if len(opts.Hooks) == 0 {
		opts.Hooks = []ProviderTrafficHook{NoopProviderTrafficHook}
	}
	o := &Observer{
		opts:      opts,
		providers: Providers,
		cache:     NewResolutionCache(Providers, opts.Resolver, 5*time.Minute, time.Hour),
		procs:     NewProcessCache(opts.Processes, 10*time.Minute, 1024),
		queue:     newQueue(queueMaxBytes),
		started:   time.Now(),
	}
	o.tableState.Store("starting")
	return o
}

// Run serves until ctx ends.
func (o *Observer) Run(ctx context.Context) error {
	debug.SetMemoryLimit(memoryLimit)
	l, err := listen(o.opts.Address)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var wg sync.WaitGroup
	wg.Add(3)
	go func() { defer wg.Done(); o.writer(ctx) }()
	go func() { defer wg.Done(); o.detector(ctx) }()
	go func() { defer wg.Done(); o.keyLoop(ctx) }()
	o.startDNS(ctx, &wg)
	go func() { <-ctx.Done(); l.Close() }()
	sem := make(chan struct{}, maxConnections)
	for {
		conn, err := l.Accept()
		if err != nil {
			if ctx.Err() != nil {
				break
			}
			if errors.Is(err, net.ErrClosed) {
				break
			}
			time.Sleep(50 * time.Millisecond)
			continue
		}
		sem <- struct{}{}
		go func() {
			defer func() { <-sem }()
			o.serve(conn)
		}()
	}
	cancel()
	wg.Wait()
	o.captureMu.Lock()
	if o.capture != nil {
		_ = o.capture.Close()
	}
	o.captureMu.Unlock()
	return nil
}

// serve reads one message. Events are queued without waiting for storage.
func (o *Observer) serve(conn net.Conn) {
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	r := bufio.NewReaderSize(conn, 32<<10)
	data, err := ReadFrame(r)
	if err != nil {
		o.rejected.Add(1)
		return
	}
	var message Message
	if err := json.Unmarshal(data, &message); err != nil {
		o.rejected.Add(1)
		return
	}
	switch message.Type {
	case TypeStatus:
		out, _ := json.Marshal(o.Stats())
		_ = WriteFrame(conn, out)
	case TypePurge:
		out, _ := json.Marshal(o.purgeAll())
		_ = WriteFrame(conn, out)
	case TypeEvent:
		o.received.Add(1)
		o.learn(message)
		if message.Time.IsZero() {
			message.Time = time.Now()
		}
		record, _ := json.Marshal(hookRecord{Kind: "hook", Time: message.Time.UTC(), Harness: message.Harness, Event: message.Event, PID: message.PID, PPID: message.PPID, Payload: message.Payload})
		o.enqueue(record)
	default:
		o.rejected.Add(1)
	}
}

type hookRecord struct {
	Kind    string          `json:"kind"`
	Time    time.Time       `json:"ts"`
	Harness string          `json:"harness"`
	Event   string          `json:"event"`
	PID     int             `json:"pid,omitempty"`
	PPID    int             `json:"ppid,omitempty"`
	Payload json.RawMessage `json:"payload,omitempty"`
}

var shells = map[string]bool{"sh": true, "bash": true, "zsh": true, "dash": true, "fish": true, "pwsh": true, "powershell": true, "cmd": true, "node": true}

// learn attributes the hook's parent (and, through a shell, its grandparent) to the harness
// that sent the event.
func (o *Observer) learn(m Message) {
	if !harnessName(m.Harness) || m.PPID <= 0 {
		return
	}
	o.procs.Learn(m.PPID, m.Harness)
	if p, ok := o.procs.Get(m.PPID); ok && p.PPID > 1 {
		base := strings.ToLower(strings.TrimSuffix(filepath.Base(strings.ReplaceAll(p.Exe, `\`, "/")), ".exe"))
		if shells[base] {
			o.procs.Learn(p.PPID, m.Harness)
		}
	}
}

func harnessName(s string) bool {
	if s == "" || len(s) > 64 {
		return false
	}
	for _, r := range s {
		if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-') {
			return false
		}
	}
	return true
}

func (o *Observer) enqueue(record []byte) {
	if !o.queue.push(record) {
		o.dropped.Add(1)
	}
}

// writer batches queued records into the capture.
func (o *Observer) writer(ctx context.Context) {
	ticker := time.NewTicker(batchInterval)
	defer ticker.Stop()
	purge := time.NewTicker(10 * time.Minute)
	defer purge.Stop()
	for {
		select {
		case <-ctx.Done():
			o.flush()
			return
		case <-o.queue.ready:
		case <-ticker.C:
		case <-purge.C:
			o.captureMu.Lock()
			if o.capture != nil {
				_, _ = o.capture.Purge()
			}
			o.captureMu.Unlock()
			continue
		}
		o.flush()
	}
}

func (o *Observer) flush() {
	batch := o.queue.drain()
	if len(batch) == 0 {
		return
	}
	o.captureMu.Lock()
	capture := o.capture
	o.captureMu.Unlock()
	if capture == nil {
		o.dropped.Add(uint64(len(batch)))
		return
	}
	if err := capture.Append(batch...); err != nil {
		o.dropped.Add(uint64(len(batch)))
		o.opts.Logf("capture write failed: %v", err)
		return
	}
	o.stored.Add(uint64(len(batch)))
}

func (o *Observer) purgeAll() PurgeResult {
	o.captureMu.Lock()
	defer o.captureMu.Unlock()
	if o.capture != nil {
		if err := o.capture.Close(); err != nil {
			return PurgeResult{Error: err.Error()}
		}
	}
	n, err := PurgeAll(CaptureDirectory(o.opts.ConfigDir))
	if err != nil {
		return PurgeResult{Deleted: n, Error: err.Error()}
	}
	return PurgeResult{Deleted: n}
}

// keyLoop opens the capture once the credential store hands out the key, retrying while it
// is locked or unavailable.
func (o *Observer) keyLoop(ctx context.Context) {
	for {
		if o.openCapture() {
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(time.Minute):
		}
	}
}

func (o *Observer) openCapture() bool {
	if o.opts.Keys == nil {
		o.setCaptureDetail("no credential store")
		return false
	}
	key, err := LoadKey(o.opts.Keys, true)
	if err != nil {
		o.setCaptureDetail("credential store unavailable: " + err.Error())
		return false
	}
	c, err := OpenCapture(CaptureDirectory(o.opts.ConfigDir), key, o.opts.Config.MaxBytes(), o.opts.Config.MaxAge())
	if err != nil {
		o.setCaptureDetail(err.Error())
		return false
	}
	_, _ = c.Purge()
	o.captureMu.Lock()
	o.capture, o.captureDetail = c, ""
	o.captureMu.Unlock()
	return true
}

func (o *Observer) setCaptureDetail(detail string) {
	o.captureMu.Lock()
	o.captureDetail = detail
	o.captureMu.Unlock()
}

// tracked is a provider connection seen in the socket table.
type tracked struct {
	traffic ProviderTraffic
	seen    time.Time
}

// detector polls the socket table for connections to provider addresses.
func (o *Observer) detector(ctx context.Context) {
	seen := map[[2]netip.AddrPort]*tracked{}
	interval := o.opts.Config.PollInterval()
	if o.opts.pollInterval > 0 {
		interval = o.opts.pollInterval
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		o.poll(ctx, seen)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (o *Observer) poll(ctx context.Context, seen map[[2]netip.AddrPort]*tracked) {
	o.cache.Refresh(ctx)
	conns, err := o.opts.Table.Connections()
	if err != nil {
		o.tableState.Store("unavailable: " + err.Error())
		return
	}
	o.tableState.Store("ok")
	now := time.Now()
	var matched []Conn
	for _, c := range conns {
		if _, ok := o.cache.Lookup(c.Remote.Addr()); ok {
			matched = append(matched, c)
		}
	}
	if resolver, ok := o.opts.Table.(PIDResolver); ok && len(matched) > 0 {
		matched = resolver.ResolvePIDs(matched)
	}
	for _, c := range matched {
		key := [2]netip.AddrPort{c.Local, c.Remote}
		if t, ok := seen[key]; ok {
			t.seen = now
			continue
		}
		if len(seen) >= maxTracked {
			continue
		}
		m, _ := o.cache.Lookup(c.Remote.Addr())
		traffic := ProviderTraffic{Provider: m.Provider.ID, Host: m.Host, Source: "socket", Phase: "open", PID: c.PID, Remote: c.Remote, FirstSeen: now, LastSeen: now}
		o.attribute(&traffic)
		seen[key] = &tracked{traffic: traffic, seen: now}
		o.open.Add(1)
		o.detected(ctx, traffic, c.Local, c.State)
	}
	for key, t := range seen {
		if t.seen.Equal(now) {
			continue
		}
		delete(seen, key)
		o.open.Add(-1)
		closed := t.traffic
		closed.Phase, closed.LastSeen = "close", t.seen
		o.detected(ctx, closed, key[0], "")
	}
}

func (o *Observer) attribute(t *ProviderTraffic) {
	if t.PID <= 0 {
		return
	}
	if p, ok := o.procs.Get(t.PID); ok {
		t.Executable = p.Exe
	}
	t.Harness = o.procs.Harness(t.PID)
}

type detectionRecord struct {
	Kind       string    `json:"kind"`
	Time       time.Time `json:"ts"`
	Phase      string    `json:"phase"`
	Source     string    `json:"source"`
	Provider   string    `json:"provider"`
	Host       string    `json:"host"`
	Remote     string    `json:"remote,omitempty"`
	Local      string    `json:"local,omitempty"`
	State      string    `json:"state,omitempty"`
	Addrs      []string  `json:"addrs,omitempty"`
	PID        int       `json:"pid,omitempty"`
	Executable string    `json:"exe,omitempty"`
	Harness    string    `json:"harness,omitempty"`
	FirstSeen  time.Time `json:"firstSeen"`
	DurationMs int64     `json:"durationMs,omitempty"`
}

func (o *Observer) detected(ctx context.Context, t ProviderTraffic, local netip.AddrPort, state string, addrs ...netip.Addr) {
	o.detections.Add(1)
	kind := "connection"
	if t.Source != "socket" {
		kind = "dns"
	}
	record := detectionRecord{Kind: kind, Time: time.Now().UTC(), Phase: t.Phase, Source: t.Source, Provider: t.Provider, Host: t.Host, State: state, PID: t.PID, Executable: t.Executable, Harness: t.Harness, FirstSeen: t.FirstSeen.UTC()}
	if t.Remote.IsValid() {
		record.Remote = t.Remote.String()
	}
	if local.IsValid() {
		record.Local = local.String()
	}
	for _, a := range addrs {
		record.Addrs = append(record.Addrs, a.String())
	}
	if t.Phase == "close" {
		record.DurationMs = t.LastSeen.Sub(t.FirstSeen).Milliseconds()
	}
	data, _ := json.Marshal(record)
	o.enqueue(data)
	for _, hook := range o.opts.Hooks {
		hook(ctx, t)
	}
}

func (o *Observer) startDNS(ctx context.Context, wg *sync.WaitGroup) {
	enabled := map[string]bool{}
	for _, name := range o.opts.Config.DNSSources {
		enabled[name] = true
	}
	for _, source := range o.opts.DNS {
		if !enabled[source.Name()] {
			o.dns.set(SourceState{Source: source.Name(), State: "disabled", Detail: source.Description()})
			continue
		}
		delete(enabled, source.Name())
		wg.Add(1)
		go func(source DNSSource) {
			defer wg.Done()
			o.runDNS(ctx, source)
		}(source)
	}
	for name := range enabled {
		o.dns.set(SourceState{Source: name, State: "unsupported", Detail: "not available on " + runtime.GOOS})
	}
}

// runDNS runs a source, restarting it with backoff; after repeated failures it is reported
// unavailable and retried rarely.
func (o *Observer) runDNS(ctx context.Context, source DNSSource) {
	backoff := 10 * time.Second
	for ctx.Err() == nil {
		o.dns.set(SourceState{Source: source.Name(), State: "running"})
		err := source.Run(ctx, func(q DNSQuery) { o.dnsQuery(ctx, q) })
		if ctx.Err() != nil {
			return
		}
		detail := "stopped"
		if err != nil {
			detail = err.Error()
		}
		o.dns.set(SourceState{Source: source.Name(), State: "unavailable", Detail: detail})
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		backoff = min(backoff*2, 30*time.Minute)
	}
}

func (o *Observer) dnsQuery(ctx context.Context, q DNSQuery) {
	p, ok := providerHost(o.providers, q.Host)
	if !ok {
		return
	}
	o.cache.Learn(q.Host, q.Addrs)
	t := ProviderTraffic{Provider: p.ID, Host: q.Host, Source: q.Source, Phase: "query", PID: q.PID, FirstSeen: q.Time, LastSeen: q.Time}
	o.attribute(&t)
	o.detected(ctx, t, netip.AddrPort{}, "", q.Addrs...)
}

// Stats is what `durin observer status` reports about a running observer.
type Stats struct {
	PID             int           `json:"pid"`
	Version         string        `json:"version,omitempty"`
	StartedAt       time.Time     `json:"startedAt"`
	RSSBytes        uint64        `json:"rssBytes"`
	PeakRSSBytes    uint64        `json:"peakRssBytes"`
	HeapBytes       uint64        `json:"heapBytes"`
	Goroutines      int           `json:"goroutines"`
	Received        uint64        `json:"eventsReceived"`
	Stored          uint64        `json:"recordsStored"`
	Dropped         uint64        `json:"recordsDropped"`
	Rejected        uint64        `json:"messagesRejected"`
	Detections      uint64        `json:"detections"`
	OpenConnections int64         `json:"openProviderConnections"`
	CachedAddresses int           `json:"cachedProviderAddresses"`
	CachedProcesses int           `json:"cachedProcesses"`
	SocketTable     string        `json:"socketTable"`
	Capture         CaptureStats  `json:"capture"`
	DNS             []SourceState `json:"dnsSources"`
}

// CaptureStats describes the encrypted capture.
type CaptureStats struct {
	Available   bool   `json:"available"`
	Detail      string `json:"detail,omitempty"`
	Bytes       int64  `json:"bytes"`
	Segments    int    `json:"segments"`
	MaxBytes    int64  `json:"maxBytes"`
	MaxAgeHours int    `json:"maxAgeHours"`
}

// Stats reports the observer's state.
func (o *Observer) Stats() Stats {
	var mem runtime.MemStats
	runtime.ReadMemStats(&mem)
	rss, peak := processRSS()
	s := Stats{
		PID: osGetpid(), Version: o.opts.Version, StartedAt: o.started.UTC(),
		RSSBytes: rss, PeakRSSBytes: peak, HeapBytes: mem.HeapAlloc, Goroutines: runtime.NumGoroutine(),
		Received: o.received.Load(), Stored: o.stored.Load(), Dropped: o.dropped.Load(), Rejected: o.rejected.Load(),
		Detections: o.detections.Load(), OpenConnections: o.open.Load(),
		CachedAddresses: o.cache.Size(), CachedProcesses: o.procs.Size(),
		SocketTable: o.tableState.Load().(string), DNS: o.dns.list(),
	}
	s.Capture = CaptureSummary(o.opts.ConfigDir, o.opts.Config)
	o.captureMu.Lock()
	s.Capture.Available, s.Capture.Detail = o.capture != nil, o.captureDetail
	o.captureMu.Unlock()
	return s
}

// CaptureSummary measures the capture directory.
func CaptureSummary(configDir string, config Config) CaptureStats {
	s := CaptureStats{MaxBytes: config.MaxBytes(), MaxAgeHours: int(config.MaxAge() / time.Hour)}
	segments, _ := Segments(CaptureDirectory(configDir))
	for _, seg := range segments {
		s.Bytes += seg.Size
	}
	s.Segments = len(segments)
	return s
}

// queue holds records for the writer, bounded by total size.
type queue struct {
	mu    sync.Mutex
	items [][]byte
	bytes int
	max   int
	ready chan struct{}
}

func newQueue(max int) *queue { return &queue{max: max, ready: make(chan struct{}, 1)} }

func (q *queue) push(item []byte) bool {
	q.mu.Lock()
	if q.bytes+len(item) > q.max {
		q.mu.Unlock()
		return false
	}
	q.items = append(q.items, item)
	q.bytes += len(item)
	full := len(q.items) >= 256
	q.mu.Unlock()
	if full {
		select {
		case q.ready <- struct{}{}:
		default:
		}
	}
	return true
}

func (q *queue) drain() [][]byte {
	q.mu.Lock()
	defer q.mu.Unlock()
	items := q.items
	q.items, q.bytes = nil, 0
	return items
}
