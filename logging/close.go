package logging

import (
	"context"
	"errors"
	"log/slog"
	"time"
)

const (
	closeDrainBudget       = 5 * time.Second
	closeBudgetDiagMessage = "log events discarded when the log shipper close budget expired"
)

// Close stops the delivery loop after one final, bounded drain. sync.Once gives
// concurrent callers a single execution and blocks them until that execution
// has published closeResult. Calls made after it returns take sync.Once's atomic
// fast path and return the already-published result.
func (s *Shipper) Close() CloseResult {
	if s == nil {
		return CloseResult{}
	}

	s.closeOnce.Do(func() {
		s.lifecycleMu.Lock()
		// The fixed budget starts at the first Close invocation, not when an
		// already in-flight ordinary request finishes unwinding. Closing closeDone
		// publishes this deadline to the delivery goroutine.
		s.closeDeadline = s.deliveryClock().Now().Add(closeDrainBudget)
		s.signalCloseLocked()
		started := s.started.Load()
		if s.state.Load() == stateDisabled {
			// A disabled Shipper must perform no drain, request, or diagnostic.
			// Holding lifecycleMu makes this store atomic with enqueue's final
			// admission check.
			s.closed.Store(true)
			s.lifecycleMu.Unlock()
			return
		}
		s.lifecycleMu.Unlock()

		if !started {
			// Start is optional at the API boundary. If no delivery goroutine owns
			// the queue, the first Close caller performs the same final drain
			// synchronously. signalCloseLocked prevents a racing Start from
			// creating a second consumer.
			s.finalDrain(nil, nil)
			return
		}
		<-s.closeFinished
	})

	return s.closeResult
}

// finalDrain is the closeDone arm of the delivery loop. It sends each batch at
// most once, keeps admission open while draining, and seals admission only after
// observing an empty queue while holding lifecycleMu.
func (s *Shipper) finalDrain(pending []*Event, carry *Event) {
	result := CloseResult{}
	defer func() {
		s.closeResult = result
		s.finishClose()
	}()

	if s.state.Load() == stateDisabled {
		return
	}

	clock := s.deliveryClock()
	deadline := s.closeDeadline
	if deadline.IsZero() {
		// Defensive fallback for a directly invoked finalDrain in a package test;
		// Close always publishes the deadline before signaling the loop.
		deadline = clock.Now().Add(closeDrainBudget)
	}
	remainingBudget := deadline.Sub(clock.Now())
	if remainingBudget < 0 {
		remainingBudget = 0
	}
	budgetTimer := clock.After(remainingBudget)
	budgetExpired := make(chan struct{})
	stopBudget := make(chan struct{})
	budgetStopped := make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())

	// IngestClient's per-request timeout is a real context timer. This watcher
	// additionally cancels an in-progress request when an injected Clock reaches
	// the fixed close budget, which keeps fake-clock tests deterministic.
	go func() {
		defer close(budgetStopped)
		select {
		case <-budgetTimer:
			close(budgetExpired)
			cancel()
		case <-stopBudget:
		}
	}()
	defer func() {
		close(stopBudget)
		cancel()
		<-budgetStopped
	}()

	batchSize := s.batchSize()
	batch := pending
	batchBytes := pendingEventBytes(batch)

	for {
		if closeExpired(clock, deadline, budgetExpired) {
			discarded := s.discardCloseRemainder(batch)
			result.Discarded += uint64(discarded)
			if discarded > 0 {
				result.TimedOut = true
				s.diagCloseBudgetExpired(discarded)
			}
			return
		}

		if len(batch) == 0 {
			ev, sealed := s.dequeueOrSealClose()
			if sealed {
				return
			}
			batch = append(batch, ev)
			batchBytes = eventBytes(ev)
		}

		var carry *Event
		for len(batch) < batchSize {
			ev, ok := s.dequeue()
			if !ok {
				break
			}
			s.counters.addInFlight(1)
			if !s.fits(batch, batchBytes, ev) {
				carry = ev
				break
			}
			batch = append(batch, ev)
			batchBytes += eventBytes(ev)
		}

		remaining := deadline.Sub(clock.Now())
		if remaining <= 0 {
			if carry != nil {
				batch = append(batch, carry)
			}
			discarded := s.discardCloseRemainder(batch)
			result.Discarded += uint64(discarded)
			if discarded > 0 {
				result.TimedOut = true
				s.diagCloseBudgetExpired(discarded)
			}
			return
		}
		requestTimeout := remaining
		if requestTimeout > DefaultRequestTimeout {
			requestTimeout = DefaultRequestTimeout
		}

		attempt := s.transmitFinal(ctx, batch, requestTimeout)
		result.Flushed += uint64(attempt.flushed)
		result.Discarded += uint64(attempt.discarded)
		batch, batchBytes = nil, 0
		if carry != nil {
			batch = append(batch, carry)
			batchBytes = eventBytes(carry)
		}

		if attempt.disabled {
			if len(batch) > 0 {
				s.releaseInFlight(len(batch))
				s.counters.addDropped(reasonNonRetryable, uint64(len(batch)))
				result.Discarded += uint64(len(batch))
			}
			return
		}

		if closeExpired(clock, deadline, budgetExpired) {
			// A timeout caused by the close deadline is part of the expiry
			// discard count; a fast terminal response that happened to finish at
			// the boundary was already terminal and is not.
			expiredAttempt := 0
			if attempt.outcome.Err != nil && (isTimeout(attempt.outcome.Err) || errors.Is(attempt.outcome.Err, context.Canceled)) {
				expiredAttempt = attempt.discarded
			}
			discarded := s.discardCloseRemainder(batch)
			result.Discarded += uint64(discarded)
			if discarded+expiredAttempt > 0 {
				result.TimedOut = true
				s.diagCloseBudgetExpired(discarded + expiredAttempt)
			}
			return
		}
	}
}

type finalAttemptResult struct {
	flushed   int
	discarded int
	disabled  bool
	outcome   SendOutcome
}

// transmitFinal performs exactly one attempt for a close-path batch and applies
// the same terminal classification/accounting as ordinary delivery, without any
// retry or backoff.
func (s *Shipper) transmitFinal(ctx context.Context, batch []*Event, timeout time.Duration) finalAttemptResult {
	if len(batch) == 0 {
		return finalAttemptResult{}
	}
	n := len(batch)

	if s.client == nil || s.state.Load() == stateDisabled {
		s.releaseInFlight(n)
		s.counters.addDropped(reasonAbandonedBatch, uint64(n))
		return finalAttemptResult{discarded: n, disabled: s.state.Load() == stateDisabled}
	}

	outcome := s.client.Send(ctx, batch, timeout)
	classified := classify(outcome)
	result := finalAttemptResult{outcome: outcome}

	// Every close-path outcome is terminal because retries are forbidden.
	s.releaseInFlight(n)
	if classified.creditsDelivered() {
		s.counters.addDelivered(uint64(n))
		result.flushed = n
	} else if classified.retriesBatch() {
		s.counters.addDropped(reasonAbandonedBatch, uint64(n))
		result.discarded = n
	} else {
		s.counters.addDropped(classified.dropReason(), uint64(n))
		result.discarded = n
	}

	discardedForDiagnostic := result.discarded
	switch classified.outcome {
	case outcomeSuspend:
		s.suspendTransmission()
	case outcomeDisable:
		drained := s.disableAfterTokenRejection()
		result.discarded += drained
		discardedForDiagnostic += drained
		result.disabled = true
	}
	s.diagOutcome(classified, outcome.Status, discardedForDiagnostic)
	return result
}

// dequeueOrSealClose resolves the final-empty race with enqueue. enqueue holds
// lifecycleMu from its closed check through its channel send, so an empty queue
// observed under this lock means every previously admitted event is already
// visible. Storing closed before releasing the lock makes every later enqueue a
// reasonAfterClose drop.
func (s *Shipper) dequeueOrSealClose() (*Event, bool) {
	s.lifecycleMu.Lock()
	defer s.lifecycleMu.Unlock()

	if ev, ok := s.dequeue(); ok {
		s.counters.addInFlight(1)
		return ev, false
	}
	s.closed.Store(true)
	return nil, true
}

// discardCloseRemainder seals admission, drains every resident event, and
// attributes the in-flight batch plus queued remainder to the close expiry.
func (s *Shipper) discardCloseRemainder(batch []*Event) int {
	s.lifecycleMu.Lock()
	s.closed.Store(true)
	queued := 0
	for {
		if _, ok := s.dequeue(); !ok {
			break
		}
		queued++
	}
	s.lifecycleMu.Unlock()

	inFlight := len(batch)
	if inFlight > 0 {
		s.releaseInFlight(inFlight)
	}
	discarded := inFlight + queued
	if discarded > 0 {
		s.counters.addDropped(reasonAbandonedBatch, uint64(discarded))
	}
	return discarded
}

func (s *Shipper) diagCloseBudgetExpired(discarded int) {
	containPanic(func() {
		s.diagf(slog.LevelError, closeBudgetDiagMessage,
			slog.Int("discarded_events", discarded),
		)
	})
}

func closeExpired(clock Clock, deadline time.Time, expired <-chan struct{}) bool {
	select {
	case <-expired:
		return true
	default:
		return !clock.Now().Before(deadline)
	}
}

// finishClose is the final publication step for a drain. The result is assigned
// before this is called, and closing closeFinished publishes it to every blocked
// Close caller.
func (s *Shipper) finishClose() {
	s.lifecycleMu.Lock()
	s.closed.Store(true)
	s.lifecycleMu.Unlock()

	select {
	case <-s.closeFinished:
	default:
		close(s.closeFinished)
	}
}

func (s *Shipper) signalCloseLocked() {
	if s.closeCancel != nil {
		s.closeCancel()
	}
	select {
	case <-s.closeDone:
	default:
		close(s.closeDone)
	}
}
