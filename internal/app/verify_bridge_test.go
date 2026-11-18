package app

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMain(m *testing.M) {
	// The test binary doubles as the saved bridge that VerifySavedBridge starts.
	if len(os.Args) > 2 && os.Args[1] == "mcp" && os.Args[2] == "serve" {
		os.Exit(fakeBridge())
	}
	os.Exit(m.Run())
}

// fakeBridge serves an empty catalog over stdio, but only with exactly the environment setup
// promises the bridge: what it needs, and nothing unrelated.
func fakeBridge() int {
	want := map[string]string{
		"DO_NOT_TRACK": "1", "NODE_ENV": "test",
		"DBUS_SESSION_BUS_ADDRESS": "unix:path=/run/user/1000/bus", "XDG_RUNTIME_DIR": "/run/user/1000",
	}
	for key, value := range want {
		if os.Getenv(key) != value {
			return 1
		}
	}
	if _, leaked := os.LookupEnv("DURIN_UNRELATED_SECRET"); leaked {
		return 1
	}
	replies := map[string]any{
		"initialize": map[string]any{"protocolVersion": "2025-11-25", "serverInfo": map[string]any{"name": "durin", "version": "1"}, "capabilities": map[string]any{"tools": map[string]any{}}},
		"tools/list": map[string]any{"tools": []any{}},
	}
	scanner := bufio.NewScanner(os.Stdin)
	for scanner.Scan() {
		var request struct {
			ID     any    `json:"id"`
			Method string `json:"method"`
		}
		if json.Unmarshal(scanner.Bytes(), &request) != nil {
			continue
		}
		if result, ok := replies[request.Method]; ok {
			line, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": request.ID, "result": result})
			os.Stdout.Write(append(line, '\n'))
		}
	}
	return 0
}

func TestVerifySavedBridgeLaunchesTheExactSavedCommand(t *testing.T) {
	dir := tempDir(t)
	t.Setenv("DO_NOT_TRACK", "1")
	t.Setenv("NODE_ENV", "test")
	// The bridge reads the OS credential store; on Linux that needs the session bus.
	t.Setenv("DBUS_SESSION_BUS_ADDRESS", "unix:path=/run/user/1000/bus")
	t.Setenv("XDG_RUNTIME_DIR", "/run/user/1000")
	t.Setenv("DURIN_UNRELATED_SECRET", "not inherited")
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	agent := AgentsFor(dir, EnvMap{})["claude"]
	config := BridgeConfiguration{ExecutablePath: executable, ConfigDirectory: dir, ProfileID: strings.Repeat("a", 64)}
	if err := WriteBridgeConfig(agent, config); err != nil {
		t.Fatal(err)
	}
	if count, err := VerifySavedBridge(context.Background(), agent, config); err != nil || count != 0 {
		t.Fatalf("%d %v", count, err)
	}
	other := config
	other.ProfileID = strings.Repeat("b", 64)
	_, err = VerifySavedBridge(context.Background(), agent, other)
	wantCliError(t, err, "The saved agent configuration is missing its organization bridge.")
	missing := AgentsFor(filepath.Join(dir, "elsewhere"), EnvMap{})["claude"]
	_, err = VerifySavedBridge(context.Background(), missing, config)
	wantCliError(t, err, "missing its organization bridge")
}
