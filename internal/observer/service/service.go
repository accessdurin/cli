// Package service installs durin's observer as a per-user OS service: a systemd user unit
// on Linux, a launchd agent on macOS and a logon scheduled task on Windows. Each restarts
// the observer when it crashes and runs the copied runtime executable, never a path inside
// a package cache.
package service

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode/utf16"
)

// Names of the service on each OS.
const (
	SystemdUnit  = "durin-observer.service"
	LaunchdLabel = "com.getdurin.observer"
	TaskName     = "Durin Observer"
)

// Runner runs an OS command and returns its combined output.
type Runner func(ctx context.Context, name string, args ...string) (string, error)

// ExecRunner runs commands for real.
func ExecRunner(ctx context.Context, name string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	err := cmd.Run()
	return out.String(), err
}

// Spec is what the service runs.
type Spec struct {
	Executable string // <config>/runtime/durin
	ConfigDir  string
	Home       string
	GOOS       string
	UID        int
	Run        Runner
}

// Arguments are the observer's command line after the executable.
func (s Spec) Arguments() []string { return []string{"observe", "--config-dir", s.ConfigDir} }

// Status is the service's state.
type Status struct {
	Manager   string `json:"manager"`
	Path      string `json:"path"`
	Installed bool   `json:"installed"`
	Running   bool   `json:"running"`
	Detail    string `json:"detail,omitempty"`
}

// Path is the service definition file.
func (s Spec) Path() string {
	switch s.GOOS {
	case "darwin":
		return filepath.Join(s.Home, "Library", "LaunchAgents", LaunchdLabel+".plist")
	case "windows":
		return filepath.Join(s.ConfigDir, "observer", "task.xml")
	}
	config := os.Getenv("XDG_CONFIG_HOME")
	if config == "" || !filepath.IsAbs(config) {
		config = filepath.Join(s.Home, ".config")
	}
	return filepath.Join(config, "systemd", "user", SystemdUnit)
}

// Manager is the OS service manager's name.
func (s Spec) Manager() string {
	switch s.GOOS {
	case "darwin":
		return "launchd"
	case "windows":
		return "task-scheduler"
	}
	return "systemd-user"
}

// Definition is the service definition file's content.
func (s Spec) Definition() string {
	switch s.GOOS {
	case "darwin":
		return s.launchdPlist()
	case "windows":
		return s.taskXML()
	}
	return s.systemdUnit()
}

// systemdQuote quotes a word for a unit file's ExecStart (and escapes specifiers).
func systemdQuote(s string) string {
	s = strings.NewReplacer(`\`, `\\`, `"`, `\"`, "%", "%%", "$", "$$").Replace(s)
	return `"` + s + `"`
}

func (s Spec) systemdUnit() string {
	words := []string{systemdQuote(s.Executable)}
	for _, a := range s.Arguments() {
		words = append(words, systemdQuote(a))
	}
	return "# Managed by Durin CLI. Remove with: durin observer remove\n" +
		"[Unit]\nDescription=Durin observer (local hook capture and AI-provider detection)\n\n" +
		"[Service]\nType=simple\nExecStart=" + strings.Join(words, " ") + "\n" +
		"Restart=always\nRestartSec=2\nNice=10\nIOSchedulingClass=idle\nMemoryMax=256M\n\n" +
		"[Install]\nWantedBy=default.target\n"
}

func xmlEscape(s string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;", "'", "&apos;").Replace(s)
}

func (s Spec) launchdPlist() string {
	var args strings.Builder
	for _, a := range append([]string{s.Executable}, s.Arguments()...) {
		args.WriteString("    <string>" + xmlEscape(a) + "</string>\n")
	}
	return `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<!-- Managed by Durin CLI. Remove with: durin observer remove -->
<plist version="1.0">
<dict>
  <key>Label</key>
  <string>` + LaunchdLabel + `</string>
  <key>ProgramArguments</key>
  <array>
` + args.String() + `  </array>
  <key>RunAtLoad</key>
  <true/>
  <key>KeepAlive</key>
  <dict>
    <key>SuccessfulExit</key>
    <false/>
  </dict>
  <key>ThrottleInterval</key>
  <integer>5</integer>
  <key>ProcessType</key>
  <string>Background</string>
  <key>LowPriorityIO</key>
  <true/>
  <key>Nice</key>
  <integer>10</integer>
</dict>
</plist>
`
}

// windowsQuote quotes an argument for CreateProcess command-line parsing.
func windowsQuote(s string) string {
	if s != "" && !strings.ContainsAny(s, " \t\"") {
		return s
	}
	var b strings.Builder
	b.WriteByte('"')
	slashes := 0
	for _, r := range s {
		switch r {
		case '\\':
			slashes++
			continue
		case '"':
			b.WriteString(strings.Repeat(`\`, 2*slashes+1))
		default:
			b.WriteString(strings.Repeat(`\`, slashes))
		}
		slashes = 0
		b.WriteRune(r)
	}
	b.WriteString(strings.Repeat(`\`, 2*slashes))
	b.WriteByte('"')
	return b.String()
}

// taskXML runs the observer at logon for this user, restarts it when it fails and never
// stops it for running long. The observer also supervises its own worker on Windows, since
// Task Scheduler only restarts a task whose launch fails.
func (s Spec) taskXML() string {
	var args []string
	for _, a := range append(s.Arguments(), "--supervise") {
		args = append(args, windowsQuote(a))
	}
	return `<?xml version="1.0" encoding="UTF-16"?>
<!-- Managed by Durin CLI. Remove with: durin observer remove -->
<Task version="1.2" xmlns="http://schemas.microsoft.com/windows/2004/02/mit/task">
  <RegistrationInfo>
    <Description>Durin observer (local hook capture and AI-provider detection)</Description>
  </RegistrationInfo>
  <Triggers>
    <LogonTrigger>
      <Enabled>true</Enabled>
    </LogonTrigger>
  </Triggers>
  <Principals>
    <Principal id="Author">
      <LogonType>InteractiveToken</LogonType>
      <RunLevel>LeastPrivilege</RunLevel>
    </Principal>
  </Principals>
  <Settings>
    <MultipleInstancesPolicy>IgnoreNew</MultipleInstancesPolicy>
    <DisallowStartIfOnBatteries>false</DisallowStartIfOnBatteries>
    <StopIfGoingOnBatteries>false</StopIfGoingOnBatteries>
    <ExecutionTimeLimit>PT0S</ExecutionTimeLimit>
    <Hidden>true</Hidden>
    <Priority>7</Priority>
    <RestartOnFailure>
      <Interval>PT1M</Interval>
      <Count>999</Count>
    </RestartOnFailure>
  </Settings>
  <Actions Context="Author">
    <Exec>
      <Command>` + xmlEscape(windowsQuote(s.Executable)) + `</Command>
      <Arguments>` + xmlEscape(strings.Join(args, " ")) + `</Arguments>
    </Exec>
  </Actions>
</Task>
`
}

// utf16File encodes a Task Scheduler definition as UTF-16LE with a byte order mark, the
// encoding its XML declaration names.
func utf16File(s string) string {
	units := utf16.Encode([]rune(s))
	out := make([]byte, 2+2*len(units))
	out[0], out[1] = 0xff, 0xfe
	for i, u := range units {
		out[2+2*i], out[3+2*i] = byte(u), byte(u>>8)
	}
	return string(out)
}

func writeDefinition(path, content string) (changed bool, err error) {
	if before, err := os.ReadFile(path); err == nil && string(before) == content {
		return false, nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return false, err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(content), 0o600); err != nil {
		return false, err
	}
	return true, os.Rename(tmp, path)
}

// Install writes the definition and (re)starts the service, so that an updated runtime is
// picked up. It never asks for elevation: everything is per user.
func (s Spec) Install(ctx context.Context) error {
	path := s.Path()
	content := s.Definition()
	if s.GOOS == "windows" {
		content = utf16File(content)
	}
	if _, err := writeDefinition(path, content); err != nil {
		return err
	}
	run := s.Run
	switch s.GOOS {
	case "darwin":
		target := "gui/" + strconv.Itoa(s.UID)
		_, _ = run(ctx, "launchctl", "bootout", target+"/"+LaunchdLabel)
		if out, err := run(ctx, "launchctl", "bootstrap", target, path); err != nil {
			return commandError("launchctl bootstrap", out, err)
		}
	case "windows":
		_, _ = run(ctx, "schtasks", "/End", "/TN", TaskName)
		if out, err := run(ctx, "schtasks", "/Create", "/TN", TaskName, "/XML", path, "/F"); err != nil {
			return commandError("schtasks /Create", out, err)
		}
		if out, err := run(ctx, "schtasks", "/Run", "/TN", TaskName); err != nil {
			return commandError("schtasks /Run", out, err)
		}
	default:
		if out, err := run(ctx, "systemctl", "--user", "daemon-reload"); err != nil {
			return commandError("systemctl --user daemon-reload", out, err)
		}
		if out, err := run(ctx, "systemctl", "--user", "enable", SystemdUnit); err != nil {
			return commandError("systemctl --user enable", out, err)
		}
		if out, err := run(ctx, "systemctl", "--user", "restart", SystemdUnit); err != nil {
			return commandError("systemctl --user restart", out, err)
		}
	}
	return nil
}

// Remove stops and unregisters the service and deletes its definition.
func (s Spec) Remove(ctx context.Context) error {
	run := s.Run
	path := s.Path()
	var firstErr error
	switch s.GOOS {
	case "darwin":
		_, _ = run(ctx, "launchctl", "bootout", "gui/"+strconv.Itoa(s.UID)+"/"+LaunchdLabel)
	case "windows":
		_, _ = run(ctx, "schtasks", "/End", "/TN", TaskName)
		if out, err := run(ctx, "schtasks", "/Delete", "/TN", TaskName, "/F"); err != nil && !strings.Contains(strings.ToLower(out), "cannot find") {
			firstErr = commandError("schtasks /Delete", out, err)
		}
	default:
		_, _ = run(ctx, "systemctl", "--user", "disable", "--now", SystemdUnit)
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) && firstErr == nil {
		firstErr = err
	}
	if s.GOOS != "darwin" && s.GOOS != "windows" {
		_, _ = run(ctx, "systemctl", "--user", "daemon-reload")
	}
	return firstErr
}

// Status asks the OS manager whether the service is installed and running.
func (s Spec) Status(ctx context.Context) Status {
	st := Status{Manager: s.Manager(), Path: s.Path()}
	if _, err := os.Stat(st.Path); err == nil {
		st.Installed = true
	}
	run := s.Run
	switch s.GOOS {
	case "darwin":
		out, err := run(ctx, "launchctl", "print", "gui/"+strconv.Itoa(s.UID)+"/"+LaunchdLabel)
		st.Running = err == nil && strings.Contains(out, "state = running")
		if err != nil && st.Installed {
			st.Detail = "not loaded"
		}
	case "windows":
		out, err := run(ctx, "schtasks", "/Query", "/TN", TaskName, "/FO", "LIST")
		st.Running = err == nil && strings.Contains(out, "Running")
		if err != nil && st.Installed {
			st.Detail = "not registered"
		}
	default:
		out, err := run(ctx, "systemctl", "--user", "is-active", SystemdUnit)
		state := strings.TrimSpace(out)
		st.Running = err == nil && state == "active"
		if !st.Running && st.Installed {
			if state == "" && err != nil {
				state = err.Error()
			}
			st.Detail = state
		}
	}
	return st
}

func commandError(what, out string, err error) error {
	out = strings.TrimSpace(out)
	if len(out) > 300 {
		out = out[:300]
	}
	if out == "" {
		return fmt.Errorf("%s: %v", what, err)
	}
	return fmt.Errorf("%s: %v: %s", what, err, out)
}
