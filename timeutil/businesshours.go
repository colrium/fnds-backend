// Package timeutil holds time arithmetic shared across domains.
//
// Business-hours maths lives here rather than in any one domain package because
// two callers need the same definition of a business hour: the assessment
// service, which uses it to place a retake cooldown, and the SLA job, which uses
// it to decide when a pending review has breached. Two implementations would
// drift, and a drift means an artisan is told a cooldown has elapsed while the
// job still considers the clock running.
package timeutil

import "time"

// isWeekend reports whether the day is outside the business week. Business hours
// here mean "any hour on a weekday": the pipeline's SLAs are stated in hours
// excluding weekends, not in office hours, so no time-of-day window applies.
func isWeekend(t time.Time) bool {
	wd := t.Weekday()
	return wd == time.Saturday || wd == time.Sunday
}

// startOfNextDay returns midnight at the start of the following day, in the
// receiver's own location so DST transitions are handled by the zone rules.
func startOfNextDay(t time.Time) time.Time {
	return time.Date(t.Year(), t.Month(), t.Day()+1, 0, 0, 0, 0, t.Location())
}

// AddBusinessHours advances from by d, counting only weekday time.
//
// A non-positive d returns from unchanged. If the result would land on a weekend
// it is pushed to the start of Monday, so a deadline is never set at a moment
// that BusinessHoursSince would never count toward.
func AddBusinessHours(from time.Time, d time.Duration) time.Time {
	if d <= 0 {
		return from
	}

	remaining := d
	current := from

	for remaining > 0 {
		if isWeekend(current) {
			current = startOfNextDay(current)
			continue
		}

		endOfDay := startOfNextDay(current)
		availableToday := endOfDay.Sub(current)
		if remaining <= availableToday {
			current = current.Add(remaining)
			remaining = 0
			continue
		}
		remaining -= availableToday
		current = endOfDay
	}

	// Landing exactly at midnight on a Saturday is possible when the remainder
	// consumes a Friday exactly; move to Monday so the deadline sits on a day
	// that counts.
	for isWeekend(current) {
		current = startOfNextDay(current)
	}
	return current
}

// BusinessHoursSince returns the weekday hours elapsed between from and to,
// excluding weekends. It is the inverse of AddBusinessHours and returns 0 when
// to is not after from.
func BusinessHoursSince(from, to time.Time) float64 {
	if !to.After(from) {
		return 0
	}

	var hours float64
	current := from
	for current.Before(to) {
		if isWeekend(current) {
			current = startOfNextDay(current)
			continue
		}

		end := startOfNextDay(current)
		if end.After(to) {
			end = to
		}
		hours += end.Sub(current).Hours()
		current = end
	}
	return hours
}
