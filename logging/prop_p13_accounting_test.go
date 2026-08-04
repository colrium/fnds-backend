package logging

import (
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"sync"
	"testing"
	"time"
)

type p13Input struct {
	Workers   int
	PerWorker int
}

func p13IdentityError(s Snapshot) error {
	if s.Enqueued != s.Delivered+s.Dropped+s.InFlight+s.QueueDepth {
		return fmt.Errorf("enqueued %d != delivered %d + dropped %d + in_flight %d + queue_depth %d",
			s.Enqueued, s.Delivered, s.Dropped, s.InFlight, s.QueueDepth)
	}
	var reasons uint64
	for _, count := range s.DroppedByReason {
		reasons += count
	}
	if reasons != s.Dropped {
		return fmt.Errorf("per-reason drops sum to %d, want dropped total %d", reasons, s.Dropped)
	}
	return nil
}

func p13CounterDecrease(previous, current Snapshot) error {
	pairs := []struct {
		name string
		old  uint64
		new  uint64
	}{
		{"enqueued", previous.Enqueued, current.Enqueued},
		{"delivered", previous.Delivered, current.Delivered},
		{"dropped", previous.Dropped, current.Dropped},
		{"conversion_panics", previous.ConversionPanics, current.ConversionPanics},
		{"retried_batches", previous.RetriedBatches, current.RetriedBatches},
		{"abandoned_batches", previous.AbandonedBatches, current.AbandonedBatches},
	}
	for i := range previous.DroppedByReason {
		pairs = append(pairs, struct {
			name string
			old  uint64
			new  uint64
		}{fmt.Sprintf("dropped_reason_%d", i), previous.DroppedByReason[i], current.DroppedByReason[i]})
	}
	for _, pair := range pairs {
		if pair.new < pair.old {
			return fmt.Errorf("counter %s decreased from %d to %d", pair.name, pair.old, pair.new)
		}
	}
	return nil
}

// Feature: betterstack-error-logging, Property 13: Snapshot accounting is exact
// **Validates: Requirements 11.1, 11.4, 11.5, 11.12**
func TestProperty13_SnapshotAccountingIsExact(t *testing.T) {
	r, seed := pbtRand(t)
	plans := make([]p13Input, pbtIterations)
	for i := range plans {
		plans[i] = p13Input{Workers: 1 + r.Intn(3), PerWorker: 1 + r.Intn(3)}
	}

	server := newDeliveryServer(t, http.StatusAccepted)
	clock := newFakeClock()
	s := NewShipper(Config{
		Enabled:     true,
		IngestURL:   server.URL,
		SourceToken: "p13-source-token",
		ServiceName: defaultServiceName,
		Environment: productionEnvironment,
		MinLevel:    slog.LevelError,
		BatchSize:   8,
		FlushEvery:  time.Minute,
		QueueSize:   32,
		DiagWriter:  io.Discard,
		Clock:       clock,
		Jitter:      FixedJitter(0),
	})
	s.Start()
	if !clock.WaitTickers(2, deliveryWaitTimeout) {
		t.Fatalf("delivery and health tickers were not registered")
	}
	logger := deliveryLogger(s)

	stopSnapshots := make(chan struct{})
	violations := make(chan error, 1)
	var snapshots sync.WaitGroup
	snapshots.Add(1)
	go func() {
		defer snapshots.Done()
		previous := s.Snapshot()
		for {
			current := s.Snapshot()
			if err := p13IdentityError(current); err != nil {
				select {
				case violations <- err:
				default:
				}
				return
			}
			if err := p13CounterDecrease(previous, current); err != nil {
				select {
				case violations <- err:
				default:
				}
				return
			}
			previous = current
			select {
			case <-stopSnapshots:
				return
			default:
			}
		}
	}()

	start := make(chan struct{})
	var producers sync.WaitGroup
	for iteration, plan := range plans {
		for worker := 0; worker < plan.Workers; worker++ {
			producers.Add(1)
			go func(iteration, worker, count int) {
				defer producers.Done()
				<-start
				for event := 0; event < count; event++ {
					logger.Error("p13-accounting",
						slog.Int("iteration", iteration),
						slog.Int("worker", worker),
						slog.Int("event", event),
					)
				}
			}(iteration, worker, plan.PerWorker)
		}
	}
	close(start)
	producers.Wait()

	flush := s.Flush(10 * time.Second)
	if !flush.Drained {
		close(stopSnapshots)
		snapshots.Wait()
		pbtFatalf(t, seed, -1, plans, "Flush left accounting work unsettled: %+v; snapshot %+v", flush, s.Snapshot())
	}
	closeResult := s.Close()
	close(stopSnapshots)
	snapshots.Wait()

	select {
	case err := <-violations:
		pbtFatalf(t, seed, -1, plans, "a concurrent snapshot violated Property 13: %v", err)
	default:
	}
	final := s.Snapshot()
	if err := p13IdentityError(final); err != nil {
		pbtFatalf(t, seed, -1, plans, "final snapshot violated Property 13: %v (%+v)", err, final)
	}
	if final.Enqueued == 0 || final.Delivered+final.Dropped != final.Enqueued {
		pbtFailf(t, seed, -1, plans, "final terminal accounting = %+v, want every admission delivered or dropped", final)
	}
	if closeResult.TimedOut {
		pbtFailf(t, seed, -1, plans, "Close unexpectedly timed out after Flush: %+v", closeResult)
	}
}
