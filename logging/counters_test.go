package logging

import (
	"fmt"
	"math"
	"sync"
	"sync/atomic"
	"testing"
)

// counterIdentityViolation returns a description of the first accounting
// identity the snapshot violates, or the empty string when both hold:
//
//	Enqueued == Delivered + Dropped + InFlight + QueueDepth   (Req 11.4)
//	sum(DroppedByReason) == Dropped                           (Req 11.5)
//
// It returns a string rather than failing directly because the concurrency test
// checks snapshots from goroutines other than the test goroutine, where
// t.Fatalf is not allowed.
func counterIdentityViolation(s Snapshot) string {
	if want := s.Delivered + s.Dropped + s.InFlight + s.QueueDepth; s.Enqueued != want {
		return fmt.Sprintf("Enqueued = %d, want Delivered+Dropped+InFlight+QueueDepth = %d (%+v)",
			s.Enqueued, want, s)
	}
	var sum uint64
	for _, n := range s.DroppedByReason {
		sum += n
	}
	if sum != s.Dropped {
		return fmt.Sprintf("sum(DroppedByReason) = %d, want Dropped = %d (%+v)", sum, s.Dropped, s)
	}
	return ""
}

// assertCounterIdentities fails the test when the snapshot breaks either
// identity. Callable only from the test goroutine.
func assertCounterIdentities(t *testing.T, step string, s Snapshot) {
	t.Helper()
	if v := counterIdentityViolation(s); v != "" {
		t.Fatalf("%s: %s", step, v)
	}
}

func TestCounterZeroValueSnapshotIsAllZeros(t *testing.T) {
	var c counters

	got := c.snapshot()
	if got != (Snapshot{}) {
		t.Fatalf("snapshot() on a never-incremented counters = %+v, want the zero Snapshot", got)
	}
	assertCounterIdentities(t, "zero value", got)

	// Repeated reads mutate nothing (Req 11.10).
	if again := c.snapshot(); again != got {
		t.Fatalf("second snapshot() = %+v, want %+v", again, got)
	}
}

// TestCounterAddSatHoldsAtMaxUint64 exercises the saturating add directly at
// its boundary: MaxUint64-1 plus 5 must hold at MaxUint64 rather than wrap to 3
// (Req 11.2).
func TestCounterAddSatHoldsAtMaxUint64(t *testing.T) {
	cases := []struct {
		name string
		seed uint64
		add  uint64
		want uint64
	}{
		{name: "no overflow", seed: 7, add: 5, want: 12},
		{name: "adding zero", seed: math.MaxUint64 - 1, add: 0, want: math.MaxUint64 - 1},
		{name: "exactly at the max", seed: math.MaxUint64 - 5, add: 5, want: math.MaxUint64},
		{name: "one short of the max plus five", seed: math.MaxUint64 - 1, add: 5, want: math.MaxUint64},
		{name: "already saturated", seed: math.MaxUint64, add: 1, want: math.MaxUint64},
		{name: "already saturated, huge addend", seed: math.MaxUint64, add: math.MaxUint64, want: math.MaxUint64},
		{name: "zero plus max", seed: 0, add: math.MaxUint64, want: math.MaxUint64},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dst := tc.seed
			addSat(&dst, tc.add)
			if dst != tc.want {
				t.Fatalf("addSat(%d, %d) = %d, want %d", tc.seed, tc.add, dst, tc.want)
			}
			if dst < tc.seed {
				t.Fatalf("addSat decreased the counter: %d -> %d", tc.seed, dst)
			}
		})
	}
}

func TestCounterSubFloorClampsAtZero(t *testing.T) {
	cases := []struct {
		a, b, want uint64
	}{
		{a: 10, b: 4, want: 6},
		{a: 4, b: 4, want: 0},
		{a: 4, b: 5, want: 0},
		{a: 0, b: math.MaxUint64, want: 0},
		{a: math.MaxUint64, b: 1, want: math.MaxUint64 - 1},
	}
	for _, tc := range cases {
		if got := subFloor(tc.a, tc.b); got != tc.want {
			t.Errorf("subFloor(%d, %d) = %d, want %d", tc.a, tc.b, got, tc.want)
		}
	}
}

// TestCounterEverySaturableCounterHoldsAtMax seeds each counter one short of
// MaxUint64, drives five more increments through the public counter methods, and
// asserts the counter holds at MaxUint64 without panicking (Req 11.1, 11.2).
func TestCounterEverySaturableCounterHoldsAtMax(t *testing.T) {
	cases := []struct {
		name string
		seed func(c *counters)
		bump func(c *counters)
		get  func(s Snapshot) uint64
	}{
		{
			name: "enqueued",
			seed: func(c *counters) { c.enqueued = math.MaxUint64 - 1 },
			bump: func(c *counters) { c.addAdmitted(5) },
			get:  func(s Snapshot) uint64 { return s.Enqueued },
		},
		{
			name: "enqueued via five single admissions",
			seed: func(c *counters) { c.enqueued = math.MaxUint64 - 1 },
			bump: func(c *counters) {
				for i := 0; i < 5; i++ {
					c.addAdmitted(1)
				}
			},
			get: func(s Snapshot) uint64 { return s.Enqueued },
		},
		{
			name: "delivered",
			seed: func(c *counters) { c.delivered = math.MaxUint64 - 1 },
			bump: func(c *counters) { c.addDelivered(5) },
			get:  func(s Snapshot) uint64 { return s.Delivered },
		},
		{
			name: "dropped",
			seed: func(c *counters) { c.dropped = math.MaxUint64 - 1 },
			bump: func(c *counters) { c.addDropped(reasonQueueFull, 5) },
			get:  func(s Snapshot) uint64 { return s.Dropped },
		},
		{
			name: "dropped by reason",
			seed: func(c *counters) { c.byReason[reasonAbandonedBatch] = math.MaxUint64 - 1 },
			bump: func(c *counters) { c.addDropped(reasonAbandonedBatch, 5) },
			get:  func(s Snapshot) uint64 { return s.DroppedByReason[reasonAbandonedBatch] },
		},
		{
			name: "conversion panics",
			seed: func(c *counters) { c.conversionPanics = math.MaxUint64 - 1 },
			bump: func(c *counters) {
				for i := 0; i < 5; i++ {
					c.addConversionPanic()
				}
			},
			get: func(s Snapshot) uint64 { return s.ConversionPanics },
		},
		{
			name: "retried batches",
			seed: func(c *counters) { c.retried = math.MaxUint64 - 1 },
			bump: func(c *counters) {
				for i := 0; i < 5; i++ {
					c.addRetriedBatch()
				}
			},
			get: func(s Snapshot) uint64 { return s.RetriedBatches },
		},
		{
			name: "abandoned batches",
			seed: func(c *counters) { c.abandoned = math.MaxUint64 - 1 },
			bump: func(c *counters) {
				for i := 0; i < 5; i++ {
					c.addAbandonedBatch()
				}
			},
			get: func(s Snapshot) uint64 { return s.AbandonedBatches },
		},
		{
			name: "in flight",
			seed: func(c *counters) { c.inFlight = math.MaxUint64 - 1 },
			bump: func(c *counters) { c.addInFlight(5) },
			get:  func(s Snapshot) uint64 { return s.InFlight },
		},
		{
			name: "in flight via MaxInt64 delta",
			seed: func(c *counters) { c.inFlight = math.MaxUint64 - 1 },
			bump: func(c *counters) { c.addInFlight(math.MaxInt64) },
			get:  func(s Snapshot) uint64 { return s.InFlight },
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var c counters
			tc.seed(&c)
			before := tc.get(c.snapshot())

			tc.bump(&c)

			got := tc.get(c.snapshot())
			if got != math.MaxUint64 {
				t.Fatalf("counter = %d, want math.MaxUint64 (%d)", got, uint64(math.MaxUint64))
			}
			if got < before {
				t.Fatalf("counter decreased across saturation: %d -> %d", before, got)
			}

			// Further increments keep completing and keep the counter pinned.
			tc.bump(&c)
			if got := tc.get(c.snapshot()); got != math.MaxUint64 {
				t.Fatalf("counter after a second saturated bump = %d, want math.MaxUint64", got)
			}
		})
	}
}

// TestCounterSaturatedDroppedKeepsPerReasonSum checks that saturating the total
// dropped counter does not leave the per-reason array behind: both saturate
// together, so a saturated snapshot still reports a per-reason sum that
// saturates to the same value (Req 11.2, 11.5).
func TestCounterSaturatedDroppedKeepsPerReasonSum(t *testing.T) {
	var c counters
	c.dropped = math.MaxUint64 - 1
	c.byReason[reasonNonRetryable] = math.MaxUint64 - 1

	c.addDropped(reasonNonRetryable, 5)

	s := c.snapshot()
	if s.Dropped != math.MaxUint64 {
		t.Fatalf("Dropped = %d, want math.MaxUint64", s.Dropped)
	}
	if s.DroppedByReason[reasonNonRetryable] != math.MaxUint64 {
		t.Fatalf("DroppedByReason[reasonNonRetryable] = %d, want math.MaxUint64",
			s.DroppedByReason[reasonNonRetryable])
	}
	// QueueDepth clamps rather than wrapping once a counter has saturated: the
	// arithmetic identity is unreachable in that state by design, but the
	// reported depth must stay plausible.
	if s.QueueDepth != 0 {
		t.Fatalf("QueueDepth = %d, want 0 once dropped saturated", s.QueueDepth)
	}
}

// TestCounterAccountingIdentityAcrossEveryPath walks the design's counting
// discipline table one call at a time and asserts, after every single counter
// call, that the full snapshot matches the expected values and that both
// accounting identities hold (Req 11.4, 11.5).
//
// Within a terminal path the in-flight release precedes the terminal increment.
// That order is what keeps the identity exact at every instant: releasing first
// moves the events back into the derived queue depth for one moment, whereas
// recording the terminal outcome first would leave the events counted twice
// (once as delivered or dropped, once as in flight) until the release landed.
func TestCounterAccountingIdentityAcrossEveryPath(t *testing.T) {
	var c counters

	assertCounterIdentities(t, "before any increment", c.snapshot())

	steps := []struct {
		name string
		op   func(c *counters)
		want Snapshot
	}{
		// Path: conversion panics (Req 8.5). Admission is counted before
		// conversion, so the panic drop balances an admission that already exists.
		{
			name: "conversion panic path: admit the record",
			op:   func(c *counters) { c.addAdmitted(1) },
			want: Snapshot{Enqueued: 1, QueueDepth: 1},
		},
		{
			name: "conversion panic path: record the recovered panic",
			op:   func(c *counters) { c.addConversionPanic() },
			want: Snapshot{Enqueued: 1, ConversionPanics: 1, QueueDepth: 1},
		},
		{
			name: "conversion panic path: attribute the drop",
			op:   func(c *counters) { c.addDropped(reasonConversionPanic, 1) },
			want: Snapshot{
				Enqueued: 1, Dropped: 1, ConversionPanics: 1,
				DroppedByReason: [numDropReasons]uint64{reasonConversionPanic: 1},
			},
		},

		// Path: queue full (Req 6.3).
		{
			name: "queue full path: admit the record",
			op:   func(c *counters) { c.addAdmitted(1) },
			want: Snapshot{
				Enqueued: 2, Dropped: 1, ConversionPanics: 1, QueueDepth: 1,
				DroppedByReason: [numDropReasons]uint64{reasonConversionPanic: 1},
			},
		},
		{
			name: "queue full path: attribute the drop",
			op:   func(c *counters) { c.addDropped(reasonQueueFull, 1) },
			want: Snapshot{
				Enqueued: 2, Dropped: 2, ConversionPanics: 1,
				DroppedByReason: [numDropReasons]uint64{reasonConversionPanic: 1, reasonQueueFull: 1},
			},
		},

		// Path: enqueue after close (Req 9.11).
		{
			name: "after close path: admit the record",
			op:   func(c *counters) { c.addAdmitted(1) },
			want: Snapshot{
				Enqueued: 3, Dropped: 2, ConversionPanics: 1, QueueDepth: 1,
				DroppedByReason: [numDropReasons]uint64{reasonConversionPanic: 1, reasonQueueFull: 1},
			},
		},
		{
			name: "after close path: attribute the drop",
			op:   func(c *counters) { c.addDropped(reasonAfterClose, 1) },
			want: Snapshot{
				Enqueued: 3, Dropped: 3, ConversionPanics: 1,
				DroppedByReason: [numDropReasons]uint64{
					reasonConversionPanic: 1, reasonQueueFull: 1, reasonAfterClose: 1,
				},
			},
		},

		// Path: the event sits in the channel, so it shows up as queue depth.
		{
			name: "queued path: admit the record",
			op:   func(c *counters) { c.addAdmitted(1) },
			want: Snapshot{
				Enqueued: 4, Dropped: 3, ConversionPanics: 1, QueueDepth: 1,
				DroppedByReason: [numDropReasons]uint64{
					reasonConversionPanic: 1, reasonQueueFull: 1, reasonAfterClose: 1,
				},
			},
		},

		// Path: dequeued into the pending batch, so depth becomes in flight.
		{
			name: "dequeued path: move the event in flight",
			op:   func(c *counters) { c.addInFlight(1) },
			want: Snapshot{
				Enqueued: 4, Dropped: 3, ConversionPanics: 1, InFlight: 1,
				DroppedByReason: [numDropReasons]uint64{
					reasonConversionPanic: 1, reasonQueueFull: 1, reasonAfterClose: 1,
				},
			},
		},

		// Path: delivered 202 (Req 7.1).
		{
			name: "delivered path: release the in-flight event",
			op:   func(c *counters) { c.addInFlight(-1) },
			want: Snapshot{
				Enqueued: 4, Dropped: 3, ConversionPanics: 1, QueueDepth: 1,
				DroppedByReason: [numDropReasons]uint64{
					reasonConversionPanic: 1, reasonQueueFull: 1, reasonAfterClose: 1,
				},
			},
		},
		{
			name: "delivered path: record the delivery",
			op:   func(c *counters) { c.addDelivered(1) },
			want: Snapshot{
				Enqueued: 4, Delivered: 1, Dropped: 3, ConversionPanics: 1,
				DroppedByReason: [numDropReasons]uint64{
					reasonConversionPanic: 1, reasonQueueFull: 1, reasonAfterClose: 1,
				},
			},
		},

		// Path: batch abandoned after 5 transmission attempts (Req 7.7), two events.
		{
			name: "abandoned path: admit two records",
			op:   func(c *counters) { c.addAdmitted(2) },
			want: Snapshot{
				Enqueued: 6, Delivered: 1, Dropped: 3, ConversionPanics: 1, QueueDepth: 2,
				DroppedByReason: [numDropReasons]uint64{
					reasonConversionPanic: 1, reasonQueueFull: 1, reasonAfterClose: 1,
				},
			},
		},
		{
			name: "abandoned path: move both events in flight",
			op:   func(c *counters) { c.addInFlight(2) },
			want: Snapshot{
				Enqueued: 6, Delivered: 1, Dropped: 3, ConversionPanics: 1, InFlight: 2,
				DroppedByReason: [numDropReasons]uint64{
					reasonConversionPanic: 1, reasonQueueFull: 1, reasonAfterClose: 1,
				},
			},
		},
		{
			name: "abandoned path: four retransmissions after the first attempt",
			op: func(c *counters) {
				for i := 0; i < 4; i++ {
					c.addRetriedBatch()
				}
			},
			want: Snapshot{
				Enqueued: 6, Delivered: 1, Dropped: 3, ConversionPanics: 1,
				RetriedBatches: 4, InFlight: 2,
				DroppedByReason: [numDropReasons]uint64{
					reasonConversionPanic: 1, reasonQueueFull: 1, reasonAfterClose: 1,
				},
			},
		},
		{
			name: "abandoned path: abandon the batch",
			op:   func(c *counters) { c.addAbandonedBatch() },
			want: Snapshot{
				Enqueued: 6, Delivered: 1, Dropped: 3, ConversionPanics: 1,
				RetriedBatches: 4, AbandonedBatches: 1, InFlight: 2,
				DroppedByReason: [numDropReasons]uint64{
					reasonConversionPanic: 1, reasonQueueFull: 1, reasonAfterClose: 1,
				},
			},
		},
		{
			name: "abandoned path: release both in-flight events",
			op:   func(c *counters) { c.addInFlight(-2) },
			want: Snapshot{
				Enqueued: 6, Delivered: 1, Dropped: 3, ConversionPanics: 1,
				RetriedBatches: 4, AbandonedBatches: 1, QueueDepth: 2,
				DroppedByReason: [numDropReasons]uint64{
					reasonConversionPanic: 1, reasonQueueFull: 1, reasonAfterClose: 1,
				},
			},
		},
		{
			name: "abandoned path: attribute both drops",
			op:   func(c *counters) { c.addDropped(reasonAbandonedBatch, 2) },
			want: Snapshot{
				Enqueued: 6, Delivered: 1, Dropped: 5, ConversionPanics: 1,
				RetriedBatches: 4, AbandonedBatches: 1,
				DroppedByReason: [numDropReasons]uint64{
					reasonConversionPanic: 1, reasonQueueFull: 1, reasonAfterClose: 1,
					reasonAbandonedBatch: 2,
				},
			},
		},

		// Path: non-retryable ingest status (Req 7.11, 7.15).
		{
			name: "non-retryable path: admit the record",
			op:   func(c *counters) { c.addAdmitted(1) },
			want: Snapshot{
				Enqueued: 7, Delivered: 1, Dropped: 5, ConversionPanics: 1,
				RetriedBatches: 4, AbandonedBatches: 1, QueueDepth: 1,
				DroppedByReason: [numDropReasons]uint64{
					reasonConversionPanic: 1, reasonQueueFull: 1, reasonAfterClose: 1,
					reasonAbandonedBatch: 2,
				},
			},
		},
		{
			name: "non-retryable path: move the event in flight",
			op:   func(c *counters) { c.addInFlight(1) },
			want: Snapshot{
				Enqueued: 7, Delivered: 1, Dropped: 5, ConversionPanics: 1,
				RetriedBatches: 4, AbandonedBatches: 1, InFlight: 1,
				DroppedByReason: [numDropReasons]uint64{
					reasonConversionPanic: 1, reasonQueueFull: 1, reasonAfterClose: 1,
					reasonAbandonedBatch: 2,
				},
			},
		},
		{
			name: "non-retryable path: release the in-flight event",
			op:   func(c *counters) { c.addInFlight(-1) },
			want: Snapshot{
				Enqueued: 7, Delivered: 1, Dropped: 5, ConversionPanics: 1,
				RetriedBatches: 4, AbandonedBatches: 1, QueueDepth: 1,
				DroppedByReason: [numDropReasons]uint64{
					reasonConversionPanic: 1, reasonQueueFull: 1, reasonAfterClose: 1,
					reasonAbandonedBatch: 2,
				},
			},
		},
		{
			name: "non-retryable path: attribute the drop",
			op:   func(c *counters) { c.addDropped(reasonNonRetryable, 1) },
			want: Snapshot{
				Enqueued: 7, Delivered: 1, Dropped: 6, ConversionPanics: 1,
				RetriedBatches: 4, AbandonedBatches: 1,
				DroppedByReason: [numDropReasons]uint64{
					reasonConversionPanic: 1, reasonQueueFull: 1, reasonAfterClose: 1,
					reasonAbandonedBatch: 2, reasonNonRetryable: 1,
				},
			},
		},

		// Path: discarded when the close budget expired (Req 9.7), three events.
		{
			name: "close budget path: admit three records",
			op:   func(c *counters) { c.addAdmitted(3) },
			want: Snapshot{
				Enqueued: 10, Delivered: 1, Dropped: 6, ConversionPanics: 1,
				RetriedBatches: 4, AbandonedBatches: 1, QueueDepth: 3,
				DroppedByReason: [numDropReasons]uint64{
					reasonConversionPanic: 1, reasonQueueFull: 1, reasonAfterClose: 1,
					reasonAbandonedBatch: 2, reasonNonRetryable: 1,
				},
			},
		},
		{
			name: "close budget path: move all three events in flight",
			op:   func(c *counters) { c.addInFlight(3) },
			want: Snapshot{
				Enqueued: 10, Delivered: 1, Dropped: 6, ConversionPanics: 1,
				RetriedBatches: 4, AbandonedBatches: 1, InFlight: 3,
				DroppedByReason: [numDropReasons]uint64{
					reasonConversionPanic: 1, reasonQueueFull: 1, reasonAfterClose: 1,
					reasonAbandonedBatch: 2, reasonNonRetryable: 1,
				},
			},
		},
		{
			name: "close budget path: abandon the batch",
			op:   func(c *counters) { c.addAbandonedBatch() },
			want: Snapshot{
				Enqueued: 10, Delivered: 1, Dropped: 6, ConversionPanics: 1,
				RetriedBatches: 4, AbandonedBatches: 2, InFlight: 3,
				DroppedByReason: [numDropReasons]uint64{
					reasonConversionPanic: 1, reasonQueueFull: 1, reasonAfterClose: 1,
					reasonAbandonedBatch: 2, reasonNonRetryable: 1,
				},
			},
		},
		{
			name: "close budget path: release all three in-flight events",
			op:   func(c *counters) { c.addInFlight(-3) },
			want: Snapshot{
				Enqueued: 10, Delivered: 1, Dropped: 6, ConversionPanics: 1,
				RetriedBatches: 4, AbandonedBatches: 2, QueueDepth: 3,
				DroppedByReason: [numDropReasons]uint64{
					reasonConversionPanic: 1, reasonQueueFull: 1, reasonAfterClose: 1,
					reasonAbandonedBatch: 2, reasonNonRetryable: 1,
				},
			},
		},
		{
			name: "close budget path: attribute all three drops",
			op:   func(c *counters) { c.addDropped(reasonAbandonedBatch, 3) },
			want: Snapshot{
				Enqueued: 10, Delivered: 1, Dropped: 9, ConversionPanics: 1,
				RetriedBatches: 4, AbandonedBatches: 2,
				DroppedByReason: [numDropReasons]uint64{
					reasonConversionPanic: 1, reasonQueueFull: 1, reasonAfterClose: 1,
					reasonAbandonedBatch: 5, reasonNonRetryable: 1,
				},
			},
		},
	}

	for i, step := range steps {
		step.op(&c)
		got := c.snapshot()
		if got != step.want {
			t.Fatalf("step %d (%s):\n got %+v\nwant %+v", i, step.name, got, step.want)
		}
		assertCounterIdentities(t, fmt.Sprintf("step %d (%s)", i, step.name), got)
	}

	final := c.snapshot()
	if final.Enqueued != final.Delivered+final.Dropped {
		t.Fatalf("after every path reached a terminal state, Enqueued = %d, want Delivered+Dropped = %d",
			final.Enqueued, final.Delivered+final.Dropped)
	}
	if final.InFlight != 0 || final.QueueDepth != 0 {
		t.Fatalf("InFlight = %d, QueueDepth = %d, want 0 and 0", final.InFlight, final.QueueDepth)
	}
}

// TestCounterDroppedByReasonSumEqualsDroppedForEveryReason drops a distinct
// count for every defined reason and checks the Req 11.5 sum equality after each
// one.
func TestCounterDroppedByReasonSumEqualsDroppedForEveryReason(t *testing.T) {
	var c counters

	counts := map[DropReason]uint64{
		reasonQueueFull:       3,
		reasonAbandonedBatch:  11,
		reasonNonRetryable:    7,
		reasonConversionPanic: 2,
		reasonAfterClose:      5,
	}
	if len(counts) != int(numDropReasons) {
		t.Fatalf("test covers %d reasons, but numDropReasons = %d", len(counts), numDropReasons)
	}

	var total uint64
	for reason := DropReason(0); reason < numDropReasons; reason++ {
		n := counts[reason]
		c.addAdmitted(n)
		c.addDropped(reason, n)
		total += n

		s := c.snapshot()
		assertCounterIdentities(t, fmt.Sprintf("after dropping %d for reason %d", n, reason), s)
		if s.Dropped != total {
			t.Fatalf("Dropped = %d, want %d", s.Dropped, total)
		}
		if s.DroppedByReason[reason] != n {
			t.Fatalf("DroppedByReason[%d] = %d, want %d", reason, s.DroppedByReason[reason], n)
		}
	}

	s := c.snapshot()
	for reason, want := range counts {
		if got := s.DroppedByReason[reason]; got != want {
			t.Errorf("DroppedByReason[%d] = %d, want %d", reason, got, want)
		}
	}
	if s.QueueDepth != 0 {
		t.Errorf("QueueDepth = %d, want 0", s.QueueDepth)
	}
}

// TestCounterInvalidDropReasonKeepsIdentities checks that a reason outside the
// defined range neither panics with an out-of-range index nor skips the
// increment, which would break the Req 11.4 and 11.5 identities.
func TestCounterInvalidDropReasonKeepsIdentities(t *testing.T) {
	invalid := []DropReason{
		DropReason(-1),
		DropReason(math.MinInt32),
		numDropReasons,
		numDropReasons + 1,
		DropReason(99),
	}

	var c counters
	var total uint64
	for _, reason := range invalid {
		c.addAdmitted(2)
		c.addDropped(reason, 2)
		total += 2

		s := c.snapshot()
		assertCounterIdentities(t, fmt.Sprintf("after an invalid reason %d", reason), s)
		if s.Dropped != total {
			t.Fatalf("Dropped after invalid reason %d = %d, want %d", reason, s.Dropped, total)
		}
	}

	// Every invalid reason is attributed to reasonNonRetryable, so the whole
	// total lands in that bucket and nothing is lost.
	s := c.snapshot()
	if got := s.DroppedByReason[reasonNonRetryable]; got != total {
		t.Fatalf("DroppedByReason[reasonNonRetryable] = %d, want %d", got, total)
	}
}

// TestCounterInFlightNegativeDeltaClampsAtZero covers the negative-delta path of
// addInFlight, including math.MinInt64, whose magnitude cannot be computed by
// negation in int64 space.
func TestCounterInFlightNegativeDeltaClampsAtZero(t *testing.T) {
	cases := []struct {
		name  string
		seed  uint64
		delta int64
		want  uint64
	}{
		{name: "partial release", seed: 5, delta: -2, want: 3},
		{name: "exact release", seed: 5, delta: -5, want: 0},
		{name: "over-release clamps", seed: 3, delta: -5, want: 0},
		{name: "release from zero clamps", seed: 0, delta: -1, want: 0},
		{name: "zero delta is a no-op", seed: 4, delta: 0, want: 4},
		{name: "MinInt64 from zero clamps", seed: 0, delta: math.MinInt64, want: 0},
		{name: "MinInt64 from a small count clamps", seed: 10, delta: math.MinInt64, want: 0},
		{
			// The exact result pins the MinInt64 magnitude at 2^63: MaxUint64
			// minus 2^63 is MaxInt64. An off-by-one in the magnitude
			// computation would show up here.
			name:  "MinInt64 magnitude is exactly 2^63",
			seed:  math.MaxUint64,
			delta: math.MinInt64,
			want:  math.MaxInt64,
		},
		{
			name:  "MinInt64 plus one magnitude is exactly 2^63-1",
			seed:  math.MaxUint64,
			delta: math.MinInt64 + 1,
			want:  math.MaxInt64 + 1,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var c counters
			c.inFlight = tc.seed

			c.addInFlight(tc.delta)

			if got := c.snapshot().InFlight; got != tc.want {
				t.Fatalf("InFlight = %d, want %d", got, tc.want)
			}
		})
	}
}

// TestCounterQueueDepthClampsInsteadOfWrapping drives the derived depth into
// states where naive uint64 subtraction would wrap to a value near MaxUint64.
// Such states are unreachable under the counting discipline, but the clamp is
// what keeps a reported depth plausible if a future change breaks the discipline
// or a counter saturates.
func TestCounterQueueDepthClampsInsteadOfWrapping(t *testing.T) {
	cases := []struct {
		name string
		seed func(c *counters)
	}{
		{name: "delivered exceeds enqueued", seed: func(c *counters) { c.enqueued, c.delivered = 1, 5 }},
		{name: "dropped without admission", seed: func(c *counters) { c.dropped = 3 }},
		{name: "in flight without admission", seed: func(c *counters) { c.inFlight = 3 }},
		{
			name: "terminal plus in flight exceeds enqueued",
			seed: func(c *counters) { c.enqueued, c.delivered, c.dropped, c.inFlight = 10, 4, 4, 7 },
		},
		{
			name: "delivered saturated",
			seed: func(c *counters) { c.enqueued, c.delivered = 10, math.MaxUint64 },
		},
		{
			name: "every subtrahend saturated, which would overflow a naive sum",
			seed: func(c *counters) {
				c.enqueued = math.MaxUint64
				c.delivered, c.dropped, c.inFlight = math.MaxUint64, math.MaxUint64, math.MaxUint64
				c.byReason[reasonQueueFull] = math.MaxUint64
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var c counters
			tc.seed(&c)

			if got := c.snapshot().QueueDepth; got != 0 {
				t.Fatalf("QueueDepth = %d, want 0 (a wrapped subtraction would report a value near %d)",
					got, uint64(math.MaxUint64))
			}
		})
	}
}

// TestCounterCumulativeCountersNeverDecrease drives a long mixed sequence and
// asserts no cumulative counter ever decreases (Req 11.1). InFlight and
// QueueDepth are excluded deliberately: they are gauges, not cumulative
// counters, and Req 11.3 defines both as current values.
func TestCounterCumulativeCountersNeverDecrease(t *testing.T) {
	var c counters

	ops := []struct {
		name string
		op   func(c *counters)
	}{
		{name: "admit 4", op: func(c *counters) { c.addAdmitted(4) }},
		{name: "in flight +4", op: func(c *counters) { c.addInFlight(4) }},
		{name: "retry", op: func(c *counters) { c.addRetriedBatch() }},
		{name: "in flight -1", op: func(c *counters) { c.addInFlight(-1) }},
		{name: "deliver 1", op: func(c *counters) { c.addDelivered(1) }},
		{name: "in flight -1", op: func(c *counters) { c.addInFlight(-1) }},
		{name: "drop non-retryable", op: func(c *counters) { c.addDropped(reasonNonRetryable, 1) }},
		{name: "abandon batch", op: func(c *counters) { c.addAbandonedBatch() }},
		{name: "in flight -2", op: func(c *counters) { c.addInFlight(-2) }},
		{name: "drop abandoned 2", op: func(c *counters) { c.addDropped(reasonAbandonedBatch, 2) }},
		{name: "in flight -5 clamps", op: func(c *counters) { c.addInFlight(-5) }},
		{name: "admit 1", op: func(c *counters) { c.addAdmitted(1) }},
		{name: "conversion panic", op: func(c *counters) { c.addConversionPanic() }},
		{name: "drop conversion panic", op: func(c *counters) { c.addDropped(reasonConversionPanic, 1) }},
		{name: "admit 1", op: func(c *counters) { c.addAdmitted(1) }},
		{name: "drop queue full", op: func(c *counters) { c.addDropped(reasonQueueFull, 1) }},
		{name: "admit 1", op: func(c *counters) { c.addAdmitted(1) }},
		{name: "drop after close", op: func(c *counters) { c.addDropped(reasonAfterClose, 1) }},
		{name: "no-op in flight", op: func(c *counters) { c.addInFlight(0) }},
		{name: "admit 0", op: func(c *counters) { c.addAdmitted(0) }},
	}

	cumulative := func(s Snapshot) []uint64 {
		out := []uint64{
			s.Enqueued, s.Delivered, s.Dropped,
			s.ConversionPanics, s.RetriedBatches, s.AbandonedBatches,
		}
		return append(out, s.DroppedByReason[:]...)
	}
	names := []string{
		"Enqueued", "Delivered", "Dropped",
		"ConversionPanics", "RetriedBatches", "AbandonedBatches",
		"DroppedByReason[queueFull]", "DroppedByReason[abandonedBatch]",
		"DroppedByReason[nonRetryable]", "DroppedByReason[conversionPanic]",
		"DroppedByReason[afterClose]",
	}

	prev := cumulative(c.snapshot())
	if len(prev) != len(names) {
		t.Fatalf("cumulative() returned %d values, but %d are named", len(prev), len(names))
	}
	for _, step := range ops {
		step.op(&c)
		s := c.snapshot()
		assertCounterIdentities(t, "after "+step.name, s)

		next := cumulative(s)
		for i := range next {
			if next[i] < prev[i] {
				t.Fatalf("after %s, %s decreased: %d -> %d", step.name, names[i], prev[i], next[i])
			}
		}
		prev = next
	}
}

// TestCounterConcurrentSnapshotsSatisfyTheAccountingIdentity drives admissions,
// dequeues, deliveries, and drops from many goroutines while other goroutines
// snapshot continuously, asserting both identities on every snapshot observed and
// exact final totals. It is the groundwork for the Req 11.12 race-detector
// requirement, so it must stay clean under -race.
func TestCounterConcurrentSnapshotsSatisfyTheAccountingIdentity(t *testing.T) {
	const (
		producers        = 8
		perProducer      = 500
		deliveryWorkers  = 4
		observerRoutines = 4
		queueCapacity    = 16
		total            = producers * perProducer
	)

	c := &counters{}
	queue := make(chan int, queueCapacity)
	done := make(chan struct{})

	var wantDelivered, wantDropped, wantQueueFull, wantNonRetryable atomic.Uint64
	var observed atomic.Uint64

	var observers sync.WaitGroup
	for i := 0; i < observerRoutines; i++ {
		observers.Add(1)
		go func() {
			defer observers.Done()
			for {
				select {
				case <-done:
					// One final read after the writers finished.
					if v := counterIdentityViolation(c.snapshot()); v != "" {
						t.Errorf("final concurrent snapshot: %s", v)
					}
					return
				default:
					s := c.snapshot()
					observed.Add(1)
					if v := counterIdentityViolation(s); v != "" {
						t.Errorf("concurrent snapshot: %s", v)
						return
					}
				}
			}
		}()
	}

	var workers sync.WaitGroup
	for w := 0; w < deliveryWorkers; w++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for ev := range queue {
				// Dequeued into a batch: depth becomes in flight.
				c.addInFlight(1)
				// Release before recording the terminal outcome, so no event is
				// ever counted in two buckets at once.
				c.addInFlight(-1)
				if ev%4 == 3 {
					c.addDropped(reasonNonRetryable, 1)
					wantDropped.Add(1)
					wantNonRetryable.Add(1)
					continue
				}
				c.addDelivered(1)
				wantDelivered.Add(1)
			}
		}()
	}

	var admitters sync.WaitGroup
	for p := 0; p < producers; p++ {
		p := p
		admitters.Add(1)
		go func() {
			defer admitters.Done()
			for i := 0; i < perProducer; i++ {
				ev := p*perProducer + i
				c.addAdmitted(1)
				select {
				case queue <- ev:
				default:
					// Queue at capacity: the admission is balanced by a drop
					// before the event ever reaches the channel (Req 6.3).
					c.addDropped(reasonQueueFull, 1)
					wantDropped.Add(1)
					wantQueueFull.Add(1)
				}
			}
		}()
	}

	admitters.Wait()
	close(queue)
	workers.Wait()
	close(done)
	observers.Wait()

	if observed.Load() == 0 {
		t.Fatal("no concurrent snapshots were observed, so the identity was never exercised under contention")
	}

	s := c.snapshot()
	assertCounterIdentities(t, "after every goroutine finished", s)
	if s.Enqueued != total {
		t.Errorf("Enqueued = %d, want %d", s.Enqueued, total)
	}
	if got, want := s.Delivered, wantDelivered.Load(); got != want {
		t.Errorf("Delivered = %d, want %d", got, want)
	}
	if got, want := s.Dropped, wantDropped.Load(); got != want {
		t.Errorf("Dropped = %d, want %d", got, want)
	}
	if got, want := s.DroppedByReason[reasonQueueFull], wantQueueFull.Load(); got != want {
		t.Errorf("DroppedByReason[reasonQueueFull] = %d, want %d", got, want)
	}
	if got, want := s.DroppedByReason[reasonNonRetryable], wantNonRetryable.Load(); got != want {
		t.Errorf("DroppedByReason[reasonNonRetryable] = %d, want %d", got, want)
	}
	if s.Delivered+s.Dropped != total {
		t.Errorf("Delivered+Dropped = %d, want %d", s.Delivered+s.Dropped, total)
	}
	if s.InFlight != 0 {
		t.Errorf("InFlight = %d, want 0", s.InFlight)
	}
	if s.QueueDepth != 0 {
		t.Errorf("QueueDepth = %d, want 0", s.QueueDepth)
	}
}
