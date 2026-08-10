package timeutil

import (
	"testing"
	"time"
)

// Fixed reference points. 2026-08-10 is a Monday.
var (
	monday   = time.Date(2026, 8, 10, 9, 0, 0, 0, time.UTC)
	friday   = time.Date(2026, 8, 14, 9, 0, 0, 0, time.UTC)
	saturday = time.Date(2026, 8, 15, 9, 0, 0, 0, time.UTC)
)

func TestAddBusinessHoursWithinOneWeekday(t *testing.T) {
	got := AddBusinessHours(monday, 5*time.Hour)
	want := time.Date(2026, 8, 10, 14, 0, 0, 0, time.UTC)
	if !got.Equal(want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

// The rule that matters: a Friday afternoon deadline must skip the weekend.
func TestAddBusinessHoursSkipsWeekend(t *testing.T) {
	// Friday 09:00 + 48h of weekday time = 15h Friday, 24h Monday, 9h Tuesday.
	got := AddBusinessHours(friday, 48*time.Hour)
	want := time.Date(2026, 8, 18, 9, 0, 0, 0, time.UTC) // Tuesday
	if !got.Equal(want) {
		t.Errorf("got %v (%s), want %v (%s)", got, got.Weekday(), want, want.Weekday())
	}
}

// Starting inside a weekend must not consume weekend time.
func TestAddBusinessHoursStartingOnWeekend(t *testing.T) {
	got := AddBusinessHours(saturday, 2*time.Hour)
	want := time.Date(2026, 8, 17, 2, 0, 0, 0, time.UTC) // Monday 02:00
	if !got.Equal(want) {
		t.Errorf("got %v (%s), want %v (%s)", got, got.Weekday(), want, want.Weekday())
	}
}

func TestAddBusinessHoursNeverLandsOnWeekend(t *testing.T) {
	// Walk a range of durations from a Friday; none may land on a weekend, since
	// BusinessHoursSince would never count such a moment.
	for h := 1; h <= 200; h++ {
		got := AddBusinessHours(friday, time.Duration(h)*time.Hour)
		if isWeekend(got) {
			t.Fatalf("+%dh landed on %s (%v)", h, got.Weekday(), got)
		}
	}
}

func TestAddBusinessHoursNonPositiveIsIdentity(t *testing.T) {
	for _, d := range []time.Duration{0, -time.Hour} {
		if got := AddBusinessHours(monday, d); !got.Equal(monday) {
			t.Errorf("AddBusinessHours(monday, %v) = %v, want unchanged", d, got)
		}
	}
}

func TestBusinessHoursSinceExcludesWeekend(t *testing.T) {
	// Friday 09:00 -> Monday 09:00 is 72 wall-clock hours, but only 24 weekday
	// hours: 15h of Friday (09:00-24:00) plus 9h of Monday (00:00-09:00). The 48
	// hours of Saturday and Sunday are excluded.
	got := BusinessHoursSince(friday, time.Date(2026, 8, 17, 9, 0, 0, 0, time.UTC))
	if want := 24.0; got != want {
		t.Errorf("got %v hours, want %v", got, want)
	}
}

func TestBusinessHoursSinceZeroWhenNotAfter(t *testing.T) {
	if got := BusinessHoursSince(monday, monday); got != 0 {
		t.Errorf("equal times gave %v, want 0", got)
	}
	if got := BusinessHoursSince(friday, monday); got != 0 {
		t.Errorf("reversed range gave %v, want 0", got)
	}
}

func TestBusinessHoursSinceEntirelyWithinWeekend(t *testing.T) {
	sunday := time.Date(2026, 8, 16, 12, 0, 0, 0, time.UTC)
	if got := BusinessHoursSince(saturday, sunday); got != 0 {
		t.Errorf("weekend-only range gave %v, want 0", got)
	}
}

// The two functions are inverses; if they disagree, a cooldown could read as
// elapsed to one caller and pending to the other. This is the drift the shared
// package exists to prevent.
func TestAddAndSinceAreInverses(t *testing.T) {
	starts := []time.Time{
		monday,
		friday,
		saturday,
		time.Date(2026, 8, 14, 23, 0, 0, 0, time.UTC), // late Friday
	}
	for _, start := range starts {
		for _, h := range []int{1, 8, 24, 47, 48, 100} {
			d := time.Duration(h) * time.Hour
			deadline := AddBusinessHours(start, d)
			got := BusinessHoursSince(start, deadline)
			// Allow a small epsilon for float accumulation.
			if diff := got - float64(h); diff > 1e-6 || diff < -1e-6 {
				t.Errorf("start=%v (%s) +%dh: BusinessHoursSince=%v, want %d",
					start, start.Weekday(), h, got, h)
			}
		}
	}
}
