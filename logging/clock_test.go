package logging

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestFakeClockAdvanceMovesNowAndFiresAfter(t *testing.T) {
	c := newFakeClock()

	if got := c.Now(); !got.Equal(fakeClockEpoch) {
		t.Fatalf("Now() = %v, want %v", got, fakeClockEpoch)
	}

	ch := c.After(5 * time.Second)

	c.Advance(4 * time.Second)
	select {
	case at := <-ch:
		t.Fatalf("After fired early at %v", at)
	default:
	}
	if got, want := c.Now(), fakeClockEpoch.Add(4*time.Second); !got.Equal(want) {
		t.Fatalf("Now() = %v, want %v", got, want)
	}

	c.Advance(time.Second)
	select {
	case at := <-ch:
		if want := fakeClockEpoch.Add(5 * time.Second); !at.Equal(want) {
			t.Fatalf("After delivered %v, want %v", at, want)
		}
	default:
		t.Fatal("After did not fire once its deadline passed")
	}

	if got := c.PendingAfters(); got != 0 {
		t.Fatalf("PendingAfters() = %d, want 0", got)
	}
}

func TestFakeClockAfterFiresEveryElapsedDeadlineInOneAdvance(t *testing.T) {
	c := newFakeClock()

	first := c.After(time.Second)
	second := c.After(2 * time.Second)
	third := c.After(time.Minute)

	c.Advance(30 * time.Second)

	for i, ch := range []<-chan time.Time{first, second} {
		select {
		case <-ch:
		default:
			t.Fatalf("channel %d did not fire", i)
		}
	}
	select {
	case <-third:
		t.Fatal("channel with a later deadline fired")
	default:
	}
	if got := c.PendingAfters(); got != 1 {
		t.Fatalf("PendingAfters() = %d, want 1", got)
	}
}

func TestFakeClockAfterNonPositiveFiresImmediately(t *testing.T) {
	c := newFakeClock()

	select {
	case <-c.After(0):
	default:
		t.Fatal("After(0) did not fire immediately")
	}
	select {
	case <-c.After(-time.Second):
	default:
		t.Fatal("After(-1s) did not fire immediately")
	}
}

func TestFakeClockRecordsEveryAfterDuration(t *testing.T) {
	c := newFakeClock()

	want := []time.Duration{time.Second, 2 * time.Second, 4 * time.Second}
	for _, d := range want {
		c.After(d)
	}

	got := c.AfterDurations()
	if len(got) != len(want) {
		t.Fatalf("AfterDurations() = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("AfterDurations()[%d] = %v, want %v", i, got[i], want[i])
		}
	}
	if n := c.AfterCount(); n != len(want) {
		t.Fatalf("AfterCount() = %d, want %d", n, len(want))
	}

	// The accessor returns a copy, so a caller cannot corrupt the recorder.
	got[0] = time.Hour
	if again := c.AfterDurations(); again[0] != want[0] {
		t.Fatalf("AfterDurations() leaked its backing array: %v", again)
	}

	c.ResetAfterDurations()
	if n := c.AfterCount(); n != 0 {
		t.Fatalf("AfterCount() after reset = %d, want 0", n)
	}
}

func TestFakeClockAdvanceFiresElapsedTickers(t *testing.T) {
	c := newFakeClock()
	tk := c.NewTicker(5 * time.Second)
	defer tk.Stop()

	if got := c.TickerCount(); got != 1 {
		t.Fatalf("TickerCount() = %d, want 1", got)
	}

	c.Advance(4 * time.Second)
	select {
	case <-tk.C():
		t.Fatal("ticker fired before its interval elapsed")
	default:
	}

	c.Advance(time.Second)
	select {
	case at := <-tk.C():
		if want := fakeClockEpoch.Add(5 * time.Second); !at.Equal(want) {
			t.Fatalf("tick delivered %v, want %v", at, want)
		}
	default:
		t.Fatal("ticker did not fire once its interval elapsed")
	}

	// A second interval fires again, so flush ticks keep coming.
	c.Advance(5 * time.Second)
	select {
	case <-tk.C():
	default:
		t.Fatal("ticker did not fire on its second interval")
	}
}

func TestFakeClockStoppedTickerNeverFires(t *testing.T) {
	c := newFakeClock()
	tk := c.NewTicker(time.Second)

	tk.Stop()
	tk.Stop() // idempotent

	if got := c.TickerCount(); got != 0 {
		t.Fatalf("TickerCount() after Stop = %d, want 0", got)
	}

	c.Advance(time.Minute)
	select {
	case <-tk.C():
		t.Fatal("stopped ticker fired")
	default:
	}
}

func TestFakeClockWaitAftersObservesConcurrentRegistration(t *testing.T) {
	c := newFakeClock()

	go func() { c.After(time.Second) }()

	if !c.WaitAfters(1, 50*time.Millisecond) {
		t.Fatal("WaitAfters did not observe the After call")
	}
	if c.WaitAfters(2, 5*time.Millisecond) {
		t.Fatal("WaitAfters reported a count that was never reached")
	}
}

func TestSystemClockUsesRealTime(t *testing.T) {
	c := SystemClock()

	if c.Now().IsZero() {
		t.Fatal("SystemClock().Now() returned the zero time")
	}

	select {
	case <-c.After(time.Millisecond):
	case <-time.After(time.Second):
		t.Fatal("SystemClock().After did not fire")
	}

	tk := c.NewTicker(time.Millisecond)
	defer tk.Stop()
	select {
	case <-tk.C():
	case <-time.After(time.Second):
		t.Fatal("SystemClock().NewTicker did not tick")
	}
}

func TestRandJitterStaysInUnitInterval(t *testing.T) {
	j := RandJitter(1)
	for i := 0; i < 1000; i++ {
		f := j.Fraction()
		if f < 0 || f >= 1 {
			t.Fatalf("Fraction() = %v, want [0, 1)", f)
		}
	}

	// The same seed yields the same sequence, so backoff tests are reproducible.
	a, b := RandJitter(42), RandJitter(42)
	for i := 0; i < 10; i++ {
		if x, y := a.Fraction(), b.Fraction(); x != y {
			t.Fatalf("seeded sequences diverged at %d: %v != %v", i, x, y)
		}
	}
}

func TestFixedJitterClampsIntoUnitInterval(t *testing.T) {
	cases := []struct {
		in   float64
		want float64
	}{
		{in: 0, want: 0},
		{in: 0.5, want: 0.5},
		{in: -1, want: 0},
		{in: 1, want: maxJitterFraction},
		{in: 42, want: maxJitterFraction},
	}
	for _, tc := range cases {
		if got := FixedJitter(tc.in).Fraction(); got != tc.want {
			t.Errorf("FixedJitter(%v).Fraction() = %v, want %v", tc.in, got, tc.want)
		}
	}
	if got := FixedJitter(nan()).Fraction(); got != 0 {
		t.Errorf("FixedJitter(NaN).Fraction() = %v, want 0", got)
	}
}

func nan() float64 {
	var zero float64
	return zero / zero
}

// timePackageExemptions lists non-test files allowed to reference the time
// package directly, with the reason. Req 12.2 constrains the durations the
// Shipper derives — flush intervals, backoff waits, rate-limit windows — so a
// use that drives none of them is exempt.
var timePackageExemptions = map[string]string{
	"clock.go":   "defines SystemClock, the single permitted wrapper",
	"context.go": "package-init fallback seed for the request-id nonce, drives no Shipper duration",
}

// TestOnlyClockTouchesTheTimePackageDirectly guards the injection rule: apart
// from the documented exemptions, no non-test file in this package may call
// time.Now, time.After, or time.NewTicker (Req 12.2).
func TestOnlyClockTouchesTheTimePackageDirectly(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read package directory: %v", err)
	}

	banned := []string{"time.Now(", "time.After(", "time.NewTicker("}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || filepath.Ext(name) != ".go" || strings.HasSuffix(name, "_test.go") {
			continue
		}
		if _, exempt := timePackageExemptions[name]; exempt {
			continue
		}
		src, err := os.ReadFile(name)
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		for _, call := range banned {
			if strings.Contains(string(src), call) {
				t.Errorf("%s calls %s; use the injected Clock instead", name, call)
			}
		}
	}
}
