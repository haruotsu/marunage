//go:build !darwin && !windows

// Linux/unix sleep/resume hook implementation.
//
// CLOCK_MONOTONIC (used by Go's monotonic reading) pauses during suspend on
// Linux, so the same dual-clock comparison used on darwin detects sleep/resume
// without CGo or systemd inhibitor locks.

package daemon

import (
	"context"
	"time"
)

// runSleepHook polls every interval and calls onWake when a sleep-resume
// event is detected using the monotonic-clock divergence method.
func runSleepHook(ctx context.Context, interval time.Duration, onWake func(time.Duration)) {
	runSleepHookWith(ctx, interval, detectSleep, onWake)
}
