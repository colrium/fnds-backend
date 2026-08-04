// Package logging forwards error-severity log records to Better Stack.
//
// The package is stdlib-only by design: it must add no third-party module
// requirement to go.mod (Req 12.6).
package logging

import (
	"math"
	"math/rand"
	"sync"
	"time"
)

// Ticker abstracts *time.Ticker so flush intervals can be driven by a fake
// clock in tests (Req 12.2).
type Ticker interface {
	// C returns the channel on which ticks are delivered.
	C() <-chan time.Time
	// Stop releases the ticker. It never closes the tick channel.
	Stop()
}

// Clock is the single source of time for the Shipper. Every flush interval,
// retry backoff wait, close budget, and rate-limit window is derived from an
// injected Clock, so advancing a fake clock rather than wall-clock time drives
// the whole delivery state machine (Req 12.2).
//
// Log_Event timestamps do not come from here: they come from slog.Record.Time,
// which slog sets at the call site.
type Clock interface {
	Now() time.Time
	NewTicker(d time.Duration) Ticker
	After(d time.Duration) <-chan time.Time
}

// Jitter is the randomness source for retry backoff (Req 7.5).
type Jitter interface {
	// Fraction returns a value in [0, 1).
	Fraction() float64
}

// SystemClock returns the Clock backed by the time package.
//
// This file is the only place in the logging package that may call time.Now,
// time.After, or time.NewTicker (Req 12.2).
func SystemClock() Clock { return systemClock{} }

type systemClock struct{}

func (systemClock) Now() time.Time { return time.Now() }

// NewTicker panics for a non-positive interval, matching time.NewTicker.
// Callers get their interval from ResolveConfig, which clamps it to a positive
// range before construction.
func (systemClock) NewTicker(d time.Duration) Ticker {
	return systemTicker{ticker: time.NewTicker(d)}
}

func (systemClock) After(d time.Duration) <-chan time.Time { return time.After(d) }

type systemTicker struct{ ticker *time.Ticker }

func (t systemTicker) C() <-chan time.Time { return t.ticker.C }

func (t systemTicker) Stop() { t.ticker.Stop() }

// RandJitter returns a Jitter drawn from math/rand seeded with seed. The
// returned Jitter is safe for concurrent use, so a snapshot reader and the
// delivery goroutine can never race on the generator.
func RandJitter(seed int64) Jitter {
	return &randJitter{rng: rand.New(rand.NewSource(seed))}
}

type randJitter struct {
	mu  sync.Mutex
	rng *rand.Rand
}

func (j *randJitter) Fraction() float64 {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.rng.Float64()
}

// maxJitterFraction is the largest float64 strictly below 1, so that a clamped
// FixedJitter still honours the [0, 1) contract.
var maxJitterFraction = math.Nextafter(1, 0)

// FixedJitter returns a Jitter that always reports f, for deterministic backoff
// assertions in tests. Values outside [0, 1), including NaN, are clamped into
// range rather than rejected, so a Jitter is always usable.
func FixedJitter(f float64) Jitter {
	switch {
	case math.IsNaN(f), f < 0:
		f = 0
	case f > maxJitterFraction:
		f = maxJitterFraction
	}
	return fixedJitter(f)
}

type fixedJitter float64

func (j fixedJitter) Fraction() float64 { return float64(j) }
