package observer

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"time"
)

// Config is <config>/observer/config.json. Every field is optional.
type Config struct {
	// CaptureMaxBytes bounds the encrypted capture (default 512 MiB).
	CaptureMaxBytes int64 `json:"captureMaxBytes,omitempty"`
	// CaptureMaxAgeHours is how long captured records are kept (default 168, seven days).
	CaptureMaxAgeHours int `json:"captureMaxAgeHours,omitempty"`
	// PollIntervalSeconds is how often the socket table is read (default 5).
	PollIntervalSeconds int `json:"pollIntervalSeconds,omitempty"`
	// DNSSources opts in to DNS sources by name (see `durin observer status`).
	DNSSources []string `json:"dnsSources,omitempty"`
}

// MaxBytes is the effective capture size bound.
func (c Config) MaxBytes() int64 {
	if c.CaptureMaxBytes > 0 {
		return c.CaptureMaxBytes
	}
	return DefaultMaxBytes
}

// MaxAge is the effective capture age bound.
func (c Config) MaxAge() time.Duration {
	if c.CaptureMaxAgeHours > 0 {
		return time.Duration(c.CaptureMaxAgeHours) * time.Hour
	}
	return DefaultMaxAge
}

// PollInterval is the effective socket table interval.
func (c Config) PollInterval() time.Duration {
	if c.PollIntervalSeconds > 0 {
		return time.Duration(c.PollIntervalSeconds) * time.Second
	}
	return 5 * time.Second
}

// Directory is <config>/observer.
func Directory(configDir string) string { return filepath.Join(configDir, "observer") }

// CaptureDirectory is <config>/observer/capture.
func CaptureDirectory(configDir string) string {
	return filepath.Join(Directory(configDir), "capture")
}

// LoadConfig reads the observer configuration; a missing file is the defaults.
func LoadConfig(configDir string) (Config, error) {
	var c Config
	data, err := os.ReadFile(filepath.Join(Directory(configDir), "config.json"))
	if errors.Is(err, os.ErrNotExist) {
		return c, nil
	}
	if err != nil {
		return c, err
	}
	if err := json.Unmarshal(data, &c); err != nil {
		return Config{}, errors.New("observer: config.json is not valid: " + err.Error())
	}
	return c, nil
}

func sortStates(states []SourceState) {
	sort.Slice(states, func(i, j int) bool { return states[i].Source < states[j].Source })
}
