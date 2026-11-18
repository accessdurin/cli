package app

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

var macApps = map[string]string{
	"codex":    "Codex.app",
	"cursor":   "Cursor.app",
	"windsurf": "Windsurf.app",
}

// currentPlatform is process.platform.
func currentPlatform() string {
	if runtime.GOOS == "windows" {
		return "win32"
	}
	return runtime.GOOS
}

func appInstalled(key, platform string) bool {
	app, ok := macApps[key]
	if platform != "darwin" || !ok {
		return false
	}
	for _, directory := range []string{"/Applications", filepath.Join(Homedir(), "Applications")} {
		if _, err := os.Stat(filepath.Join(directory, app)); err == nil {
			return true
		}
	}
	return false
}

func executableInstalled(key string, env Env, platform string) bool {
	extensions := []string{""}
	delimiter := ":"
	if platform == "win32" {
		extensions = []string{"", ".exe", ".cmd", ".bat"}
		delimiter = ";"
	}
	path, _ := env.Lookup("PATH")
	for _, directory := range strings.Split(path, delimiter) {
		if directory == "" {
			continue
		}
		for _, extension := range extensions {
			if executable(filepath.Join(directory, key+extension)) {
				return true
			}
		}
	}
	return false
}

// AgentInstalled reports whether the harness's executable is on PATH or, on macOS, its app is
// installed (platform is a process.platform name).
func AgentInstalled(key string, env Env, platform string) bool {
	return executableInstalled(key, env, platform) || appInstalled(key, platform)
}
