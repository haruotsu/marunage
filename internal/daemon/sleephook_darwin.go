// macOS sleep/resume hook implementation.
//
// Rather than requiring CGo IOPMLib bindings, we use Go's dual-clock
// time.Time: time.Now() carries both a monotonic reading (mach_absolute_time,
// which pauses during sleep) and a wall-clock reading (which advances during
// sleep). The difference between wall-elapsed and mono-elapsed across a
// fixed polling interval equals the sleep duration.

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
