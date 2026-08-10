//go:build !race

package logging

import "time"

// enqueueWaitFreeThreshold: see the race-tagged variant of this file for why
// this differs under the race detector.
const enqueueWaitFreeThreshold = 50 * time.Millisecond
