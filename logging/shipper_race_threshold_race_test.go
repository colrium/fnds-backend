//go:build race

package logging

import "time"

// enqueueWaitFreeThreshold is looser under the race detector: its per-access
// instrumentation adds overhead to every channel send, so the same wait-free
// behavior costs more wall-clock time without indicating a real regression.
const enqueueWaitFreeThreshold = 250 * time.Millisecond
