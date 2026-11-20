package observer

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/netip"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

// DNSQuery is a resolver lookup of a provider hostname seen by a DNS source.
type DNSQuery struct {
	Source string
	Host   string
	Addrs  []netip.Addr
	PID    int // 0 when the source does not say
	Time   time.Time
}

// DNSSource observes resolver queries. Sources are opt-in and best-effort: most need
// privileges or OS settings a user may not have, and Run then returns an error describing
// what is missing. A source never asks for elevation.
type DNSSource interface {
	Name() string
	Description() string
	Run(ctx context.Context, emit func(DNSQuery)) error
}

// SourceState is a DNS source's status.
type SourceState struct {
	Source string `json:"source"`
	State  string `json:"state"` // disabled | running | unavailable | unsupported
	Detail string `json:"detail,omitempty"`
}

// commandSource runs a long-lived command and parses its output line by line.
type commandSource struct {
	name, description string
	argv              []string
	parse             func(line []byte, providers []Provider) []DNSQuery
	providers         []Provider
	run               func(ctx context.Context, argv []string) (io.ReadCloser, func() error, error)
}

func (s commandSource) Name() string        { return s.name }
func (s commandSource) Description() string { return s.description }

func startCommand(ctx context.Context, argv []string) (io.ReadCloser, func() error, error) {
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	var stderr bytes.Buffer
	cmd.Stderr = &limitedBuffer{b: &stderr, max: 4096}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, nil, err
	}
	wait := func() error {
		err := cmd.Wait()
		if err != nil && stderr.Len() > 0 {
			return fmt.Errorf("%v: %s", err, strings.TrimSpace(stderr.String()))
		}
		return err
	}
	return stdout, wait, nil
}

func (s commandSource) Run(ctx context.Context, emit func(DNSQuery)) error {
	run := s.run
	if run == nil {
		run = startCommand
	}
	out, wait, err := run(ctx, s.argv)
	if err != nil {
		return err
	}
	scanner := bufio.NewScanner(out)
	scanner.Buffer(make([]byte, 0, 64<<10), 1<<20)
	for scanner.Scan() {
		for _, q := range s.parse(scanner.Bytes(), s.providers) {
			q.Source = s.name
			if q.Time.IsZero() {
				q.Time = time.Now()
			}
			emit(q)
		}
	}
	out.Close()
	err = wait()
	if ctx.Err() != nil {
		return nil
	}
	if err == nil {
		err = errors.New("the source stopped")
	}
	return err
}

type limitedBuffer struct {
	b   *bytes.Buffer
	max int
}

func (l *limitedBuffer) Write(p []byte) (int, error) {
	if room := l.max - l.b.Len(); room > 0 {
		l.b.Write(p[:min(room, len(p))])
	}
	return len(p), nil
}

// ParseResolvedMonitor parses one JSON line of `resolvectl monitor --json=short`: provider
// names anywhere in the message, with the A/AAAA addresses it carries.
func ParseResolvedMonitor(line []byte, providers []Provider) []DNSQuery {
	var message any
	if json.Unmarshal(line, &message) != nil {
		return nil
	}
	var names []string
	var addrs []netip.Addr
	var walk func(v any, key string)
	walk = func(v any, key string) {
		switch v := v.(type) {
		case map[string]any:
			for k, child := range v {
				walk(child, k)
			}
		case []any:
			if key == "address" && (len(v) == 4 || len(v) == 16) {
				raw := make([]byte, len(v))
				for i, b := range v {
					f, ok := b.(float64)
					if !ok || f < 0 || f > 255 {
						raw = nil
						break
					}
					raw[i] = byte(f)
				}
				if a, ok := netip.AddrFromSlice(raw); ok && raw != nil {
					addrs = append(addrs, a.Unmap())
				}
				return
			}
			for _, child := range v {
				walk(child, key)
			}
		case string:
			if key == "name" {
				names = append(names, v)
			}
		}
	}
	walk(message, "")
	var out []DNSQuery
	seen := map[string]bool{}
	for _, n := range names {
		if p, ok := providerHost(providers, n); ok {
			host := strings.TrimSuffix(strings.ToLower(n), ".")
			if !seen[host] {
				seen[host] = true
				_ = p
				out = append(out, DNSQuery{Host: host, Addrs: addrs})
			}
		}
	}
	return out
}

var unifiedLogPID = regexp.MustCompile(`PID\[(\d+)\]`)

// ParseUnifiedLog parses one `log stream --style ndjson` line from mDNSResponder: provider
// names in the event message (the log redacts names as <private> unless private data is
// enabled, in which case nothing matches).
func ParseUnifiedLog(line []byte, providers []Provider) []DNSQuery {
	var entry struct {
		EventMessage string `json:"eventMessage"`
		Timestamp    string `json:"timestamp"`
	}
	if json.Unmarshal(line, &entry) != nil || entry.EventMessage == "" {
		return nil
	}
	hosts := HostsIn(providers, entry.EventMessage)
	if len(hosts) == 0 {
		return nil
	}
	pid := 0
	if m := unifiedLogPID.FindStringSubmatch(entry.EventMessage); m != nil {
		pid, _ = strconv.Atoi(m[1])
	}
	var addrs []netip.Addr
	for _, field := range strings.FieldsFunc(entry.EventMessage, func(r rune) bool { return r == ' ' || r == ',' || r == '(' || r == ')' }) {
		if a, err := netip.ParseAddr(field); err == nil {
			addrs = append(addrs, a.Unmap())
		}
	}
	var out []DNSQuery
	for _, h := range hosts {
		out = append(out, DNSQuery{Host: h, Addrs: addrs, PID: pid})
	}
	return out
}

// windowsDNSEvent is the part of a Microsoft-Windows-DNS-Client/Operational event read.
type windowsDNSEvent struct {
	System struct {
		RecordID  uint64 `xml:"EventRecordID"`
		Execution struct {
			PID int `xml:"ProcessID,attr"`
		} `xml:"Execution"`
	} `xml:"System"`
	Data []struct {
		Name  string `xml:"Name,attr"`
		Value string `xml:",chardata"`
	} `xml:"EventData>Data"`
}

// ParseWindowsDNSEvents parses `wevtutil qe ... /f:xml` output (a sequence of <Event>
// elements), returning the provider queries in events newer than after and the highest
// record id seen.
func ParseWindowsDNSEvents(data []byte, providers []Provider, after uint64) ([]DNSQuery, uint64) {
	decoder := xml.NewDecoder(bytes.NewReader(data))
	var out []DNSQuery
	highest := after
	for {
		token, err := decoder.Token()
		if err != nil {
			break
		}
		start, ok := token.(xml.StartElement)
		if !ok || start.Name.Local != "Event" {
			continue
		}
		var event windowsDNSEvent
		if decoder.DecodeElement(&event, &start) != nil {
			continue
		}
		if event.System.RecordID <= after {
			continue
		}
		highest = max(highest, event.System.RecordID)
		var name, results string
		for _, d := range event.Data {
			switch d.Name {
			case "QueryName":
				name = d.Value
			case "QueryResults":
				results = d.Value
			}
		}
		if _, ok := providerHost(providers, name); !ok {
			continue
		}
		var addrs []netip.Addr
		for _, r := range strings.Split(results, ";") {
			if a, err := netip.ParseAddr(strings.TrimSpace(r)); err == nil {
				addrs = append(addrs, a.Unmap())
			}
		}
		out = append(out, DNSQuery{Host: strings.TrimSuffix(strings.ToLower(name), "."), Addrs: addrs, PID: event.System.Execution.PID})
	}
	return out, highest
}

// windowsEventSource polls the DNS-Client operational log, which Windows keeps disabled
// until an administrator enables it.
type windowsEventSource struct {
	providers []Provider
	interval  time.Duration
	query     func(ctx context.Context) ([]byte, error)
}

func (windowsEventSource) Name() string { return "windows-dns-client" }
func (windowsEventSource) Description() string {
	return "Microsoft-Windows-DNS-Client/Operational event log (an administrator must enable the log)"
}

func (s windowsEventSource) Run(ctx context.Context, emit func(DNSQuery)) error {
	query := s.query
	if query == nil {
		query = func(ctx context.Context) ([]byte, error) {
			return exec.CommandContext(ctx, "wevtutil", "qe", "Microsoft-Windows-DNS-Client/Operational", "/q:*[System[(EventID=3008)]]", "/c:200", "/rd:true", "/f:xml").Output()
		}
	}
	var after uint64
	first := true
	ticker := time.NewTicker(s.interval)
	defer ticker.Stop()
	for {
		data, err := query(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return fmt.Errorf("cannot read the DNS-Client log: %w", err)
		}
		queries, highest := ParseWindowsDNSEvents(data, s.providers, after)
		if !first {
			for _, q := range queries {
				q.Source = s.Name()
				q.Time = time.Now()
				emit(q)
			}
		}
		first, after = false, highest
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}

// DNSSources are the sources this OS offers.
func DNSSources(goos string, providers []Provider) []DNSSource {
	switch goos {
	case "linux":
		return []DNSSource{commandSource{name: "systemd-resolved", description: "resolvectl monitor (systemd-resolved; usually needs root)", argv: []string{"resolvectl", "monitor", "--json=short"}, parse: ParseResolvedMonitor, providers: providers}}
	case "darwin":
		return []DNSSource{commandSource{name: "unified-log", description: "mDNSResponder in the unified log (names are redacted unless private logging is enabled)", argv: []string{"/usr/bin/log", "stream", "--style", "ndjson", "--predicate", `process == "mDNSResponder"`}, parse: ParseUnifiedLog, providers: providers}}
	case "windows":
		return []DNSSource{windowsEventSource{providers: providers, interval: 5 * time.Second}}
	}
	return nil
}

// sourceStates tracks the DNS sources' states for status.
type sourceStates struct {
	mu     sync.Mutex
	states map[string]SourceState
}

func (s *sourceStates) set(state SourceState) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.states == nil {
		s.states = map[string]SourceState{}
	}
	s.states[state.Source] = state
}

func (s *sourceStates) list() []SourceState {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]SourceState, 0, len(s.states))
	for _, st := range s.states {
		out = append(out, st)
	}
	sortStates(out)
	return out
}
