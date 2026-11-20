package service

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"unicode/utf16"
)

type recorder struct {
	calls  []string
	output map[string]string
	fail   map[string]bool
}

func (r *recorder) run(_ context.Context, name string, args ...string) (string, error) {
	call := strings.Join(append([]string{name}, args...), " ")
	r.calls = append(r.calls, call)
	for prefix := range r.fail {
		if strings.HasPrefix(call, prefix) {
			return "boom", errors.New("exit status 1")
		}
	}
	for prefix, out := range r.output {
		if strings.HasPrefix(call, prefix) {
			return out, nil
		}
	}
	return "", nil
}

// Each OS's definition runs the copied runtime with its config directory, restarts on
// crash, and quotes paths so that spaces and metacharacters stay one argument.
func TestDefinitionsAndLifecycle(t *testing.T) {
	home := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", "")
	exe := home + `/my "durin" 100%/runtime/durin`
	cases := map[string][]string{
		"linux":   {`ExecStart="` + home + `/my \"durin\" 100%%/runtime/durin" "observe" "--config-dir"`, "Restart=always", "MemoryMax=256M", "WantedBy=default.target"},
		"darwin":  {"<string>" + home + "/my &quot;durin&quot; 100%/runtime/durin</string>", "<key>KeepAlive</key>", "<key>SuccessfulExit</key>\n    <false/>", "<string>observe</string>"},
		"windows": {`<Command>&quot;` + home + `/my \&quot;durin\&quot; 100%/runtime/durin&quot;</Command>`, "<RestartOnFailure>", "--supervise", "<LogonTrigger>", "LeastPrivilege"},
	}
	for goos, want := range cases {
		r := &recorder{output: map[string]string{"systemctl --user is-active": "active\n", "launchctl print": "state = running\n", "schtasks /Query": "Status: Running\n"}}
		spec := Spec{Executable: exe, ConfigDir: home + "/cfg", Home: home, GOOS: goos, UID: 501, Run: r.run}
		definition := spec.Definition()
		for _, w := range want {
			if !strings.Contains(definition, w) {
				t.Errorf("%s definition lacks %q:\n%s", goos, w, definition)
			}
		}
		if err := spec.Install(context.Background()); err != nil {
			t.Fatal(err)
		}
		data, err := os.ReadFile(spec.Path())
		if err != nil {
			t.Fatal(err)
		}
		if goos == "windows" {
			units := make([]uint16, (len(data)-2)/2)
			for i := range units {
				units[i] = uint16(data[2+2*i]) | uint16(data[3+2*i])<<8
			}
			if data[0] != 0xff || data[1] != 0xfe || string(utf16.Decode(units)) != definition {
				t.Fatal("task definition is not UTF-16LE with a BOM")
			}
		} else if string(data) != definition {
			t.Fatal("definition not written")
		}
		if st := spec.Status(context.Background()); !st.Installed || !st.Running {
			t.Fatalf("%s status %+v", goos, st)
		}
		if err := spec.Remove(context.Background()); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(spec.Path()); !os.IsNotExist(err) {
			t.Fatalf("%s definition left behind", goos)
		}
		joined := strings.Join(r.calls, "\n")
		for _, w := range map[string][]string{
			"linux":   {"systemctl --user daemon-reload", "systemctl --user enable durin-observer.service", "systemctl --user restart durin-observer.service", "systemctl --user disable --now durin-observer.service"},
			"darwin":  {"launchctl bootstrap gui/501 " + spec.Path(), "launchctl bootout gui/501/com.getdurin.observer"},
			"windows": {"schtasks /Create /TN Durin Observer /XML " + spec.Path() + " /F", "schtasks /Run /TN Durin Observer", "schtasks /Delete /TN Durin Observer /F"},
		}[goos] {
			if !strings.Contains(joined, w) {
				t.Errorf("%s: no %q in\n%s", goos, w, joined)
			}
		}
	}
	// A manager that refuses (no user session) is an error for setup to report, not a crash.
	r := &recorder{fail: map[string]bool{"systemctl": true}}
	spec := Spec{Executable: "/x/durin", ConfigDir: home + "/cfg2", Home: home, GOOS: "linux", Run: r.run}
	if err := spec.Install(context.Background()); err == nil || !strings.Contains(err.Error(), "daemon-reload") || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("%v", err)
	}
	if st := spec.Status(context.Background()); !st.Installed || st.Running || st.Detail == "" {
		t.Fatalf("%+v", st)
	}
	if got := windowsQuote(`C:\a b\c\`); got != `"C:\a b\c\\"` {
		t.Fatalf("quote %s", got)
	}
}
