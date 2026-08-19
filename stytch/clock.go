package stytch

import "time"

// Clock is the single source of time for the Stytch adapter. Every timeout,
// retry backoff jitter, and expiry check is derived from an injected Clock, so
// advancing a fake clock rather than wall-clock time drives the adapter in tests.
type Clock interface {
	Now() time.Time
}

// SystemClock returns a Clock backed by the time package.
func SystemClock() Clock { return systemClock{} }

type systemClock struct{}

func (systemClock) Now() time.Time { return time.Now() }
