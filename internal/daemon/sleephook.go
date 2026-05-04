// Package daemon provides background helpers for the marunage daemon process.
// The sleep hook detects system wake-from-sleep events and notifies callers
// so they can compensate deadline-sensitive state (e.g. shift waiting_human
// updated_at by the sleep duration to avoid premature expiry).
package daemon

import (
	"context"
	"time"
)

// sleepThreshold is the minimum wall-clock vs monotonic divergence that
// qualifies as a sleep event. Sub-second divergences are clock drift, not
// sleep/resume.
const sleepThreshold = 2 * time.Second

// detectSleepFromDeltas is the pure testable core: given how much wall clock
// time and monotonic time advanced, return whether a sleep event occurred and
// its estimated duration. On all major OSes the monotonic clock pauses during
// system sleep while the wall clock keeps advancing; their difference equals
// the sleep duration.
func detectSleepFromDeltas(wallDelta, monoDelta time.Duration) (slept bool, duration time.Duration) {
	delta := wallDelta - monoDelta
	if delta < sleepThreshold {
		return false, 0
	}
	return true, delta
}

// detectSleep wraps detectSleepFromDeltas for real time.Time values.
// prev and cur must both come from time.Now() so they carry monotonic readings;
// Round(0) strips monotonic to isolate wall-clock differences.
func detectSleep(prev, cur time.Time) (slept bool, duration time.Duration) {
	mono := cur.Sub(prev)                   // uses monotonic — excludes sleep
	wall := cur.Round(0).Sub(prev.Round(0)) // wall clock — includes sleep
	return detectSleepFromDeltas(wall, mono)
}

// RunSleepHook starts a polling goroutine that calls onWake(sleepDuration)
// whenever the host resumes from sleep. It returns when ctx is cancelled.
// The implementation is OS-specific (see sleephook_darwin.go, sleephook_linux.go).
func RunSleepHook(ctx context.Context, onWake func(time.Duration)) {
	runSleepHook(ctx, time.Second, onWake)
}

// sleepDetectorFunc is the injectable detector type used by runSleepHookWith
// for testing. It takes the previous and current time and returns whether a
// sleep was detected and its estimated duration.
type sleepDetectorFunc func(prev, cur time.Time) (slept bool, duration time.Duration)

// runSleepHookWith is the testable core: it polls at interval, calls detector
// for each tick, and invokes onWake on a detected sleep event. Production code
// passes detectSleep; tests pass a fake detector.
func runSleepHookWith(ctx context.Context, interval time.Duration, detect sleepDetectorFunc, onWake func(time.Duration)) {
	prev := time.Now()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			if slept, duration := detect(prev, now); slept {
				onWake(duration)
			}
			prev = now
		}
	}
}
