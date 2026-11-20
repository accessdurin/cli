package observer

import (
	"context"
	"io"
	"net/netip"
	"os"
	"strings"
	"testing"
	"time"
)

func TestSocketTableParsers(t *testing.T) {
	procNet := `  sl  local_address rem_address   st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode
   0: 0100007F:1F90 00000000:0000 0A 00000000:00000000 00:00000000 00000000  1000        0 111 1 0000000000000000 100 0 0 10 0
   1: 0200000A:C350 0A0233C6:01BB 01 00000000:00000000 00:00000000 00000000  1000        0 222 1 0000000000000000 20 4 30 10 -1
   2: 0200000A:C351 0A0233C6:01BB 02 00000000:00000000 00:00000000 00000000  1000        0 333 1 0000000000000000 20 4 30 10 -1
`
	conns, err := ParseProcNetTCP(strings.NewReader(procNet))
	if err != nil || len(conns) != 2 || conns[0].Remote.String() != "198.51.2.10:443" || conns[0].Local.String() != "10.0.0.2:50000" || conns[0].Inode != 222 || conns[1].State != "syn-sent" {
		t.Fatalf("%+v %v", conns, err)
	}
	v6 := "header\n   0: 00000000000000000000000001000000:1F90 0000000000000000FFFF00000A0233C6:01BB 01 0:0 0:0 0 1000 0 444 1\n"
	conns, _ = ParseProcNetTCP(strings.NewReader(v6))
	if len(conns) != 1 || conns[0].Local.Addr().String() != "::1" || conns[0].Remote.String() != "198.51.2.10:443" {
		t.Fatalf("v6 %+v", conns)
	}

	lsof := "p501\ncnode\nf23\nn10.0.0.2:50000->198.51.2.10:443\nTST=ESTABLISHED\nTQR=0\nf24\nn*:8080\nTST=LISTEN\nf25\nn[::1]:5000->[2001:db8::1]:443\nTST=SYN_SENT\np502\nf3\nn10.0.0.2:1->10.0.0.3:2\nTST=CLOSE_WAIT\n"
	conns = ParseLsof(strings.NewReader(lsof))
	if len(conns) != 2 || conns[0].PID != 501 || conns[0].Remote.String() != "198.51.2.10:443" || conns[1].State != "syn-sent" || conns[1].Remote.Addr().String() != "2001:db8::1" {
		t.Fatalf("lsof %+v", conns)
	}

	row := func(state uint32, local, remote [4]byte, lport, rport uint16, pid uint32) []byte {
		b := make([]byte, 24)
		putLE(b[0:], state)
		copy(b[4:8], local[:])
		b[8], b[9] = byte(lport>>8), byte(lport)
		copy(b[12:16], remote[:])
		b[16], b[17] = byte(rport>>8), byte(rport)
		putLE(b[20:], pid)
		return b
	}
	buf := make([]byte, 4)
	putLE(buf, 2)
	buf = append(buf, row(5, [4]byte{10, 0, 0, 2}, [4]byte{198, 51, 2, 10}, 50000, 443, 4242)...)
	buf = append(buf, row(2, [4]byte{0, 0, 0, 0}, [4]byte{}, 80, 0, 4)...)
	conns = ParseTCPTableOwnerPID(buf)
	if len(conns) != 1 || conns[0].PID != 4242 || conns[0].Remote.String() != "198.51.2.10:443" || conns[0].Local.Port() != 50000 {
		t.Fatalf("windows %+v", conns)
	}
	buf6 := make([]byte, 4+56)
	putLE(buf6, 1)
	r := buf6[4:]
	copy(r[24:40], netip.MustParseAddr("2001:db8::1").AsSlice())
	r[44], r[45] = 0x01, 0xbb
	putLE(r[48:], 3)
	putLE(r[52:], 99)
	conns = ParseTCP6TableOwnerPID(buf6)
	if len(conns) != 1 || conns[0].Remote.String() != "[2001:db8::1]:443" || conns[0].State != "syn-sent" || conns[0].PID != 99 {
		t.Fatalf("windows v6 %+v", conns)
	}
}

func putLE(b []byte, v uint32) {
	b[0], b[1], b[2], b[3] = byte(v), byte(v>>8), byte(v>>16), byte(v>>24)
}

func TestProviderMatchingIsExact(t *testing.T) {
	cases := map[string][]string{
		"query api.openai.com. type A":               {"api.openai.com"},
		"QUERY API.ANTHROPIC.COM AAAA":               {"api.anthropic.com"},
		"fetch https://openrouter.ai/api/v1":         {"openrouter.ai"},
		"evilapi.openai.com api.openai.com.evil.net": nil,
		"api.openai.com-x notopenrouter.ai":          nil,
		"eu.api.openai.com":                          {"eu.api.openai.com"},
	}
	for text, want := range cases {
		if got := HostsIn(Providers, text); strings.Join(got, ",") != strings.Join(want, ",") {
			t.Errorf("%q: %v, want %v", text, got, want)
		}
	}
	for host, ok := range map[string]bool{"api.openai.com.": true, "API.Anthropic.com": true, "x.api.openai.com": false, "openai.com": false} {
		if _, got := providerHost(Providers, host); got != ok {
			t.Errorf("%s: %v", host, got)
		}
	}
}

func TestDNSParsers(t *testing.T) {
	resolved := `{"state":"success","question":[{"class":1,"type":1,"name":"api.anthropic.com"}],"answer":[{"rr":{"key":{"class":1,"type":1,"name":"api.anthropic.com"},"address":[160,79,104,10]},"ifindex":2}]}`
	qs := ParseResolvedMonitor([]byte(resolved), Providers)
	if len(qs) != 1 || qs[0].Host != "api.anthropic.com" || len(qs[0].Addrs) != 1 || qs[0].Addrs[0].String() != "160.79.104.10" {
		t.Fatalf("resolved %+v", qs)
	}
	if qs := ParseResolvedMonitor([]byte(`{"question":[{"name":"example.com"}]}`), Providers); len(qs) != 0 {
		t.Fatalf("matched %+v", qs)
	}
	unified := `{"timestamp":"2026-11-18 10:00:00.000000+0000","eventMessage":"[R123] getaddrinfo start -- flags: 0x0, hostname: api.openai.com, PID[4242](Codex)"}`
	qs = ParseUnifiedLog([]byte(unified), Providers)
	if len(qs) != 1 || qs[0].PID != 4242 || qs[0].Host != "api.openai.com" {
		t.Fatalf("unified %+v", qs)
	}
	if qs := ParseUnifiedLog([]byte(`{"eventMessage":"hostname: <private>"}`), Providers); len(qs) != 0 {
		t.Fatal("matched a redacted name")
	}
	events := `<Event xmlns="http://schemas.microsoft.com/win/2004/08/events/event"><System><EventRecordID>10</EventRecordID><Execution ProcessID="300" ThreadID="1"/></System><EventData><Data Name="QueryName">openrouter.ai</Data><Data Name="QueryResults">::ffff:104.18.2.115;104.18.3.115;</Data></EventData></Event>
<Event xmlns="http://schemas.microsoft.com/win/2004/08/events/event"><System><EventRecordID>11</EventRecordID><Execution ProcessID="301"/></System><EventData><Data Name="QueryName">example.com</Data></EventData></Event>`
	qs, highest := ParseWindowsDNSEvents([]byte(events), Providers, 0)
	if len(qs) != 1 || highest != 11 || qs[0].PID != 300 || len(qs[0].Addrs) != 2 || qs[0].Addrs[0].String() != "104.18.2.115" {
		t.Fatalf("windows %+v %d", qs, highest)
	}
	if qs, _ := ParseWindowsDNSEvents([]byte(events), Providers, 11); len(qs) != 0 {
		t.Fatal("re-read old events")
	}
}

// A command source reports a failing command (missing privileges) as unavailable rather
// than silently doing nothing, and the observer shows it in status.
func TestCommandSourceFailureIsReported(t *testing.T) {
	failing := commandSource{name: "fake", argv: []string{"fake"}, parse: ParseResolvedMonitor, providers: Providers,
		run: func(context.Context, []string) (io.ReadCloser, func() error, error) {
			return io.NopCloser(strings.NewReader(`{"question":[{"name":"api.openai.com"}]}` + "\n")), func() error { return os.ErrPermission }, nil
		}}
	var got []DNSQuery
	err := failing.Run(context.Background(), func(q DNSQuery) { got = append(got, q) })
	if err == nil || len(got) != 1 || got[0].Source != "fake" {
		t.Fatalf("%v %+v", err, got)
	}
	dir := shortDir(t)
	o := New(Options{ConfigDir: dir, Keys: &memoryKeys{}, Table: &fakeTable{}, Processes: fakeProcs{}, Resolver: fakeResolver{},
		DNS: []DNSSource{failing, commandSource{name: "off", description: "an off source"}}, Config: Config{DNSSources: []string{"fake", "nonexistent"}}})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go o.Run(ctx)
	waitFor(t, "the source state", func() bool {
		for _, s := range o.Stats().DNS {
			if s.Source == "fake" && s.State == "unavailable" {
				return true
			}
		}
		return false
	})
	states := map[string]string{}
	for _, s := range o.Stats().DNS {
		states[s.Source] = s.State
	}
	if states["off"] != "disabled" || states["nonexistent"] != "unsupported" || o.Stats().Detections != 1 {
		t.Fatalf("%v %d", states, o.Stats().Detections)
	}
	if _, ok := o.cache.Lookup(netip.MustParseAddr("192.0.2.1")); ok {
		t.Fatal("learned an address that was not in the answer")
	}
}

func TestCachesAgeAndBound(t *testing.T) {
	now := time.Unix(0, 0)
	resolver := fakeResolver{"api.openai.com": {netip.MustParseAddr("::ffff:192.0.2.1")}}
	c := NewResolutionCache(Providers, resolver, time.Minute, time.Hour)
	c.now = func() time.Time { return now }
	c.Refresh(context.Background())
	if m, ok := c.Lookup(netip.MustParseAddr("192.0.2.1")); !ok || m.Provider.ID != "openai" {
		t.Fatal("not resolved")
	}
	delete(resolver, "api.openai.com")
	now = now.Add(30 * time.Minute)
	c.Refresh(context.Background())
	if _, ok := c.Lookup(netip.MustParseAddr("192.0.2.1")); !ok {
		t.Fatal("a failed refresh dropped a recent answer")
	}
	now = now.Add(2 * time.Hour)
	c.Refresh(context.Background())
	if _, ok := c.Lookup(netip.MustParseAddr("192.0.2.1")); ok || c.Size() != 0 {
		t.Fatal("a stale answer was kept")
	}
	c.Learn("api.anthropic.com", []netip.Addr{netip.MustParseAddr("192.0.2.9")})
	c.Learn("example.com", []netip.Addr{netip.MustParseAddr("192.0.2.8")})
	if _, ok := c.Lookup(netip.MustParseAddr("192.0.2.9")); !ok || c.Size() != 1 {
		t.Fatal("learned answers")
	}

	procs := fakeProcs{}
	for pid := 2; pid < 20; pid++ {
		procs[pid] = Process{PID: pid, PPID: pid - 1, Exe: "/bin/x"}
	}
	procs[2] = Process{PID: 2, PPID: 1, Exe: `C:\Users\u\AppData\Local\Programs\cursor\Cursor.exe`}
	pc := NewProcessCache(procs, time.Minute, 4)
	if h := pc.Harness(5); h != "cursor" {
		t.Fatalf("ancestor harness %q", h)
	}
	if pc.Size() > 4 {
		t.Fatalf("cache holds %d", pc.Size())
	}
	if h := pc.Harness(19); h != "" {
		t.Fatalf("searched too deep: %q", h)
	}
	for _, c := range []struct {
		p    Process
		want string
	}{
		{Process{Exe: "/Applications/Kiro.app/Contents/MacOS/Electron"}, "kiro"},
		{Process{Exe: "/usr/bin/bun", Args: []string{"bun", "/x/@mariozechner/pi-coding-agent/dist/cli.js"}}, "pi"},
		{Process{Exe: "/usr/local/bin/kiro-cli"}, "kiro"},
		{Process{Exe: "/usr/bin/curl"}, ""},
	} {
		if got := HarnessOf(c.p); got != c.want {
			t.Errorf("%+v: %q, want %q", c.p, got, c.want)
		}
	}
}
