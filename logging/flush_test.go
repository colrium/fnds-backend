package logging

import (
	"net/http"
	"testing"
	"time"
)

func TestClampFlushDeadline(t *testing.T) {
	tests := []struct {
		name string
		in   time.Duration
		want time.Duration
	}{
		{name: "negative clamps to minimum", in: -time.Second, want: minFlushDeadline},
		{name: "zero clamps to minimum", in: 0, want: minFlushDeadline},
		{name: "minimum is retained", in: minFlushDeadline, want: minFlushDeadline},
		{name: "interior value is retained", in: 5 * time.Second, want: 5 * time.Second},
		{name: "maximum is retained", in: maxFlushDeadline, want: maxFlushDeadline},
		{name: "above maximum clamps to maximum", in: maxFlushDeadline + time.Second, want: maxFlushDeadline},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := clampFlushDeadline(tt.in); got != tt.want {
				t.Errorf("clampFlushDeadline(%s) = %s, want %s", tt.in, got, tt.want)
			}
		})
	}
}

func TestFlushDrainsQueuedEventsReportsDeltasAndLeavesShipperUsable(t *testing.T) {
	server := newSDServer(t, http.StatusAccepted).withGate(t)
	clock := newFakeClock()
	s := startDeliveryShipper(t, deliveryConfig(server.URL, 20, 16, clock), clock)
	logger := deliveryLogger(s)

	// Put one event on the wire and hold its response open. Events emitted after
	// this point stay resident in the Event_Queue until Flush reaches the sole
	// delivery goroutine.
	deliveryEmit(logger, "before-flush", 1)
	if !waitFor(func() bool { return s.Snapshot().InFlight == 1 }, deliveryWaitTimeout) {
		t.Fatalf("InFlight = %d, want the first event pending before the flush tick", s.Snapshot().InFlight)
	}
	clock.Advance(defaultFlushSeconds * time.Second)
	if !server.waitRequests(1) {
		t.Fatalf("the initial Delivery_Batch did not reach the endpoint within %s", deliveryWaitTimeout)
	}

	const queued = 5
	deliveryEmit(logger, "flush", queued)
	if got := s.Snapshot().QueueDepth; got != queued {
		t.Fatalf("QueueDepth = %d while the first request is blocked, want %d", got, queued)
	}

	resultCh := make(chan FlushResult, 1)
	go func() { resultCh <- s.Flush(10 * time.Second) }()
	if !clock.WaitAfters(1, deliveryWaitTimeout) {
		t.Fatalf("Flush did not register its deadline on the injected clock within %s", deliveryWaitTimeout)
	}

	server.releaseGate()
	var result FlushResult
	select {
	case result = <-resultCh:
	case <-time.After(deliveryWaitTimeout):
		t.Fatalf("Flush did not return within %s after the endpoint was released", deliveryWaitTimeout)
	}

	if result != (FlushResult{Delivered: queued, Drained: true}) {
		t.Errorf("Flush result = %+v, want Delivered=%d Dropped=0 Drained=true", result, queued)
	}
	if got := server.requests(); got != 2 {
		t.Errorf("endpoint requests = %d, want the initial request plus one flushed batch", got)
	}
	assertCounterIdentities(t, "after Flush drained queued events", s.Snapshot())

	// Flush is not Close: a later event is accepted and can be synchronously
	// flushed by the same delivery goroutine.
	deliveryEmit(logger, "after-flush", 1)
	second := s.Flush(10 * time.Second)
	if second != (FlushResult{Delivered: 1, Drained: true}) {
		t.Errorf("second Flush result = %+v, want Delivered=1 Dropped=0 Drained=true", second)
	}
	if s.closed.Load() {
		t.Error("Flush marked the Shipper closed")
	}
	assertCounterIdentities(t, "after using the Shipper following Flush", s.Snapshot())
}

func TestFlushReportsDroppedEvents(t *testing.T) {
	server := newDeliveryServer(t, http.StatusRequestEntityTooLarge)
	clock := newFakeClock()
	s := startDeliveryShipper(t, deliveryConfig(server.URL, 20, 8, clock), clock)

	const records = 3
	deliveryEmit(deliveryLogger(s), "drop", records)
	if !waitFor(func() bool { return s.Snapshot().InFlight == records }, deliveryWaitTimeout) {
		t.Fatalf("InFlight = %d, want %d pending events before Flush", s.Snapshot().InFlight, records)
	}

	result := s.Flush(10 * time.Second)
	if result != (FlushResult{Dropped: records, Drained: true}) {
		t.Errorf("Flush result = %+v, want Delivered=0 Dropped=%d Drained=true", result, records)
	}
	if got := s.Snapshot().DroppedByReason[reasonNonRetryable]; got != records {
		t.Errorf("DroppedByReason[reasonNonRetryable] = %d, want %d", got, records)
	}
	assertCounterIdentities(t, "after Flush reached a dropped terminal state", s.Snapshot())
}

func TestFlushReturnsWhenItsInjectedClockDeadlineElapses(t *testing.T) {
	server := newSDServer(t, http.StatusAccepted).withGate(t)
	clock := newFakeClock()
	s := startDeliveryShipper(t, deliveryConfig(server.URL, 20, 8, clock), clock)

	deliveryEmit(deliveryLogger(s), "deadline", 1)
	if !waitFor(func() bool { return s.Snapshot().InFlight == 1 }, deliveryWaitTimeout) {
		t.Fatalf("InFlight = %d, want 1 before starting the blocked request", s.Snapshot().InFlight)
	}
	clock.Advance(defaultFlushSeconds * time.Second)
	if !server.waitRequests(1) {
		t.Fatalf("the blocked Delivery_Batch did not reach the endpoint within %s", deliveryWaitTimeout)
	}

	resultCh := make(chan FlushResult, 1)
	go func() { resultCh <- s.Flush(5 * time.Millisecond) }()
	if !clock.WaitAfters(1, deliveryWaitTimeout) {
		t.Fatalf("Flush did not register its caller deadline within %s", deliveryWaitTimeout)
	}
	clock.Advance(5 * time.Millisecond)

	select {
	case result := <-resultCh:
		if result != (FlushResult{}) {
			t.Errorf("timed-out Flush result = %+v, want zero counts and Drained=false", result)
		}
	case <-time.After(deliveryWaitTimeout):
		t.Fatalf("Flush did not return after its injected-clock deadline elapsed")
	}

	server.releaseGate()
	if !waitFor(func() bool { return s.Snapshot().Delivered == 1 }, deliveryWaitTimeout) {
		t.Fatalf("the Shipper did not remain usable after Flush timed out: snapshot %+v", s.Snapshot())
	}
	assertCounterIdentities(t, "after a timed-out Flush left delivery running", s.Snapshot())
}
