package logging

import (
	"sync"
	"time"
)

// fakeClockEpoch is the deterministic base time of every fakeClock, so
// timestamp assertions never depend on wall-clock time.
var fakeClockEpoch = time.Date(2024, time.January, 1, 0, 0, 0, 0, time.UTC)

// fakeClock is the Clock used by every test in this package. Time only moves
// when a test calls Advance; nothing in it observes wall-clock time.
//
// Advance fires, in one call:
//   - every channel returned by After whose deadline has passed, and
//   - every ticker whose interval has elapsed since its last tick.
//
// It also records every duration passed to After, in call order, so backoff
// schedules can be asserted directly (property P16, Req 7.4, 7.5).
//
// All sends are non-blocking on buffered channels, so Advance never blocks on a
// goroutine that is not currently receiving. Ticker sends use a one-slot buffer
// and drop when full, matching time.Ticker semantics.
type fakeClock struct {
	mu             sync.Mutex
	now            time.Time
	pending        []*fakeAfter
	afterDurations []time.Duration
	tickers        []*fakeTicker
}

type fakeAfter struct {
	deadline time.Time
	ch       chan time.Time
}

type fakeTicker struct {
	clock    *fakeClock
	interval time.Duration
	next     time.Time
	ch       chan time.Time
	stopped  bool
}

// newFakeClock returns a fakeClock positioned at fakeClockEpoch.
func newFakeClock() *fakeClock { return newFakeClockAt(fakeClockEpoch) }

// newFakeClockAt returns a fakeClock positioned at t.
func newFakeClockAt(t time.Time) *fakeClock { return &fakeClock{now: t} }

var _ Clock = (*fakeClock)(nil)

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

// After records d and returns a channel that receives once the clock has been
// advanced to or past now+d. A non-positive duration fires immediately, as
// time.After does.
func (c *fakeClock) After(d time.Duration) <-chan time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.afterDurations = append(c.afterDurations, d)
	ch := make(chan time.Time, 1)
	if d <= 0 {
		ch <- c.now
		return ch
	}
	c.pending = append(c.pending, &fakeAfter{deadline: c.now.Add(d), ch: ch})
	return ch
}

// NewTicker returns a Ticker that ticks each time Advance crosses a multiple of
// d from the moment of construction.
func (c *fakeClock) NewTicker(d time.Duration) Ticker {
	if d <= 0 {
		panic("fakeClock.NewTicker: non-positive interval")
	}
	c.mu.Lock()
	defer c.mu.Unlock()

	t := &fakeTicker{
		clock:    c,
		interval: d,
		next:     c.now.Add(d),
		ch:       make(chan time.Time, 1),
	}
	c.tickers = append(c.tickers, t)
	return t
}

// Advance moves the clock forward by d, firing every elapsed After deadline and
// every elapsed ticker interval. A non-positive d fires whatever is already due
// without moving time.
func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	if d > 0 {
		c.now = c.now.Add(d)
	}
	now := c.now

	kept := c.pending[:0]
	var due []*fakeAfter
	for _, a := range c.pending {
		if a.deadline.After(now) {
			kept = append(kept, a)
			continue
		}
		due = append(due, a)
	}
	for i := len(kept); i < len(c.pending); i++ {
		c.pending[i] = nil
	}
	c.pending = kept

	type tick struct {
		ch chan time.Time
		at time.Time
	}
	var ticks []tick
	for _, t := range c.tickers {
		if t.stopped {
			continue
		}
		for !t.next.After(now) {
			ticks = append(ticks, tick{ch: t.ch, at: t.next})
			t.next = t.next.Add(t.interval)
		}
	}
	c.mu.Unlock()

	for _, a := range due {
		select {
		case a.ch <- now:
		default:
		}
	}
	for _, t := range ticks {
		select {
		case t.ch <- t.at:
		default: // a tick is dropped when the buffer is full, as time.Ticker does
		}
	}
}

// AfterDurations returns a copy of every duration passed to After, in call
// order. Retry-delay assertions read the backoff schedule from here.
func (c *fakeClock) AfterDurations() []time.Duration {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]time.Duration, len(c.afterDurations))
	copy(out, c.afterDurations)
	return out
}

// AfterCount reports how many times After has been called.
func (c *fakeClock) AfterCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.afterDurations)
}

// ResetAfterDurations clears the recorded After durations, leaving pending
// deadlines intact, so a test can isolate one phase of a schedule.
func (c *fakeClock) ResetAfterDurations() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.afterDurations = nil
}

// PendingAfters reports how many After channels have not yet fired.
func (c *fakeClock) PendingAfters() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.pending)
}

// TickerCount reports how many tickers are currently running.
func (c *fakeClock) TickerCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	n := 0
	for _, t := range c.tickers {
		if !t.stopped {
			n++
		}
	}
	return n
}

// waitPollStep is the poll interval of the Wait helpers. It keeps every real
// sleep far below the 50 ms per-test ceiling (Req 12.2).
const waitPollStep = time.Millisecond

// WaitAfters blocks until After has been called at least n times or the timeout
// elapses, and reports whether the count was reached. Tests use it to
// synchronise with a delivery goroutine before advancing the clock, instead of
// sleeping for a guessed duration.
func (c *fakeClock) WaitAfters(n int, timeout time.Duration) bool {
	return waitFor(func() bool { return c.AfterCount() >= n }, timeout)
}

// WaitTickers blocks until at least n tickers are running or the timeout
// elapses, and reports whether the count was reached.
func (c *fakeClock) WaitTickers(n int, timeout time.Duration) bool {
	return waitFor(func() bool { return c.TickerCount() >= n }, timeout)
}

// waitFor polls cond a bounded number of times so it needs no wall-clock
// reading of its own.
func waitFor(cond func() bool, timeout time.Duration) bool {
	steps := int(timeout/waitPollStep) + 1
	for i := 0; i < steps; i++ {
		if cond() {
			return true
		}
		time.Sleep(waitPollStep)
	}
	return cond()
}

func (t *fakeTicker) C() <-chan time.Time { return t.ch }

// Stop deregisters the ticker so later Advance calls ignore it. Stop is
// idempotent and never closes the tick channel.
func (t *fakeTicker) Stop() {
	t.clock.mu.Lock()
	defer t.clock.mu.Unlock()
	if t.stopped {
		return
	}
	t.stopped = true
	for i, other := range t.clock.tickers {
		if other == t {
			t.clock.tickers = append(t.clock.tickers[:i], t.clock.tickers[i+1:]...)
			break
		}
	}
}
