package app

import (
	"context"
	"time"

	"github.com/accessdurin/cli/internal/ports"
)

type pollingClock struct{}

func (pollingClock) Now() time.Time { return time.Now() }

func (pollingClock) Sleep(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// NewPollingClock is pollingClock(): the real clock.
func NewPollingClock() ports.PollingClock { return pollingClock{} }
