package daemon

import (
	"context"
	"testing"
	"time"
)

// Test list for PR-300 sleep hook.
//
//  SH-1. detectSleepFromDeltas returns false when wall == mono (no sleep)
//  SH-2. detectSleepFromDeltas returns true with correct duration when wall >> mono
//  SH-3. detectSleepFromDeltas treats sub-threshold divergence as drift
//  SH-4. runSleepHookWith calls onWake with the sleep duration when detector
//         reports a wake event, and stops when ctx is cancelled

// SH-1

func TestDetectSleepFromDeltas_EqualDeltasNoSleep(t *testing.T) {
	t.Parallel()
	slept, _ := detectSleepFromDeltas(time.Second, time.Second)
	if slept {
		t.Errorf("detectSleepFromDeltas(1s, 1s) = true; want false (no divergence)")
	}
}

// SH-2

func TestDetectSleepFromDeltas_LargeWallMono_ReportsWake(t *testing.T) {
	t.Parallel()
	wall := 30*time.Minute + time.Second
	mono := time.Second
	slept, duration := detectSleepFromDeltas(wall, mono)
	if !slept {
		t.Fatalf("detectSleepFromDeltas(30m+1s wall, 1s mono) = false; want true")
	}
	want := 30 * time.Minute
	if duration < want-time.Second || duration > want+time.Second {
		t.Errorf("sleep duration = %v; want ~30m", duration)
	}
}

// SH-3

func TestDetectSleepFromDeltas_SubThresholdIgnored(t *testing.T) {
	t.Parallel()
	slept, _ := detectSleepFromDeltas(1500*time.Millisecond, time.Second)
	if slept {
		t.Errorf("detectSleepFromDeltas(1500ms wall, 1s mono) = true; want false (below %v threshold)", sleepThreshold)
	}
}

// SH-4: inject a fake detector into runSleepHookWith.

func TestRunSleepHook_CallsOnWakeWhenSleepDetected(t *testing.T) {
	t.Parallel()

	calls := 0
	detector := func(prev, cur time.Time) (bool, time.Duration) {
		calls++
		if calls == 1 {
			return true, 30 * time.Second
		}
		return false, 0
	}

	var woken []time.Duration
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})

	go func() {
		defer close(done)
		runSleepHookWith(ctx, time.Millisecond, detector, func(d time.Duration) {
			woken = append(woken, d)
			cancel()
		})
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("runSleepHookWith did not return within 2s")
	}

	if len(woken) != 1 || woken[0] != 30*time.Second {
		t.Errorf("woken = %v; want [30s]", woken)
	}
}
