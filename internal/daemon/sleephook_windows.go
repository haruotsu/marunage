// Windows sleep/resume hook: same dual-clock approach as darwin/linux.

package daemon

import (
	"context"
	"time"
)

func runSleepHook(ctx context.Context, interval time.Duration, onWake func(time.Duration)) {
	runSleepHookWith(ctx, interval, detectSleep, onWake)
}
