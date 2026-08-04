package logging

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"runtime"
	"sync"
	"testing"
	"time"
)

type p10Input struct {
	Callers   int
	Events    int
	BatchSize int
}

type p10Server struct {
	*httptest.Server
	mu       sync.Mutex
	requests int
	gate     chan struct{}
	arrived  chan struct{}
}

func p10NewServer(t *testing.T) *p10Server {
	t.Helper()
	server := &p10Server{arrived: make(chan struct{}, 1)}
	server.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		server.mu.Lock()
		server.requests++
		gate := server.gate
		server.mu.Unlock()
		select {
		case server.arrived <- struct{}{}:
		default:
		}
		if gate != nil {
			<-gate
		}
		w.WriteHeader(http.StatusAccepted)
	}))
	t.Cleanup(func() {
		server.release()
		server.Close()
	})
	return server
}

func (s *p10Server) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.requests
}

func (s *p10Server) block() {
	s.mu.Lock()
	s.gate = make(chan struct{})
	s.mu.Unlock()
	select {
	case <-s.arrived:
	default:
	}
}

func (s *p10Server) waitArrival(timeout time.Duration) bool {
	select {
	case <-s.arrived:
		return true
	case <-time.After(timeout):
		return false
	}
}

func (s *p10Server) release() {
	s.mu.Lock()
	gate := s.gate
	s.gate = nil
	s.mu.Unlock()
	if gate != nil {
		close(gate)
	}
}

func p10NewShipper(serverURL string, input p10Input) *Shipper {
	return NewShipper(Config{
		Enabled:     true,
		IngestURL:   serverURL,
		SourceToken: "p10-source-token",
		ServiceName: defaultServiceName,
		Environment: productionEnvironment,
		MinLevel:    slog.LevelError,
		BatchSize:   input.BatchSize,
		FlushEvery:  time.Minute,
		QueueSize:   input.Events + 1,
		DiagWriter:  io.Discard,
		Clock:       newFakeClock(),
		Jitter:      FixedJitter(0),
	})
}

func p10Queue(s *Shipper, count int) {
	logger := deliveryLogger(s)
	for i := 0; i < count; i++ {
		logger.Error("p10-close-event", slog.Int("index", i))
	}
}

// Feature: betterstack-error-logging, Property 10: Close is idempotent
// **Validates: Requirements 9.8, 9.9**
func TestProperty10_CloseIsIdempotent(t *testing.T) {
	r, seed := pbtRand(t)
	server := p10NewServer(t)
	type observation struct {
		result   CloseResult
		requests int
	}
	// The single-close oracle depends only on event count and batch size. Cache
	// each observed combination so the property keeps 100 randomized concurrent
	// cases without paying for duplicate baseline HTTP drains.
	baselines := make(map[[2]int]observation)

	for iteration := 0; iteration < pbtIterations; iteration++ {
		input := p10Input{
			Callers:   2 + r.Intn(7),
			Events:    1 + r.Intn(9),
			BatchSize: 1 + r.Intn(5),
		}

		key := [2]int{input.Events, input.BatchSize}
		baseline, ok := baselines[key]
		if !ok {
			single := p10NewShipper(server.URL, input)
			p10Queue(single, input.Events)
			beforeBaseline := server.count()
			baseline = observation{result: single.Close(), requests: server.count() - beforeBaseline}
			baselines[key] = baseline
		}
		wantResult := baseline.result
		baselineRequests := baseline.requests
		if wantResult != (CloseResult{Flushed: uint64(input.Events)}) {
			pbtFailf(t, seed, iteration, input, "single Close result = %+v, want Flushed=%d", wantResult, input.Events)
		}

		concurrent := p10NewShipper(server.URL, input)
		p10Queue(concurrent, input.Events)
		server.block()
		beforeConcurrent := server.count()

		start := make(chan struct{})
		entered := make(chan struct{}, input.Callers)
		results := make(chan CloseResult, input.Callers)
		for caller := 0; caller < input.Callers; caller++ {
			go func() {
				<-start
				entered <- struct{}{}
				results <- concurrent.Close()
			}()
		}
		close(start)
		for caller := 0; caller < input.Callers; caller++ {
			<-entered
		}
		if !server.waitArrival(deliveryWaitTimeout) {
			server.release()
			pbtFatalf(t, seed, iteration, input, "concurrent Close never began its drain")
		}
		// Give every released caller an opportunity to enter sync.Once while the
		// sole drain is blocked on its first response.
		for caller := 0; caller < input.Callers; caller++ {
			runtime.Gosched()
		}
		server.release()

		for caller := 0; caller < input.Callers; caller++ {
			select {
			case got := <-results:
				if got != wantResult {
					pbtFailf(t, seed, iteration, input, "concurrent caller %d result = %+v, want the single-close result %+v", caller, got, wantResult)
				}
			case <-time.After(deliveryWaitTimeout):
				pbtFatalf(t, seed, iteration, input, "concurrent caller %d did not return", caller)
			}
		}

		concurrentRequests := server.count() - beforeConcurrent
		if concurrentRequests != baselineRequests {
			pbtFailf(t, seed, iteration, input, "concurrent Close made %d requests, want the single-close count %d", concurrentRequests, baselineRequests)
		}
		if got := concurrent.Snapshot(); got.Delivered != uint64(input.Events) || got.Dropped != 0 || got.QueueDepth != 0 || got.InFlight != 0 {
			pbtFailf(t, seed, iteration, input, "exactly-once drain snapshot = %+v, want %d delivered and no remainder", got, input.Events)
		}

		beforeFastPath := server.count()
		fastStart := time.Now()
		fastResult := concurrent.Close()
		fastElapsed := time.Since(fastStart)
		if fastResult != wantResult {
			pbtFailf(t, seed, iteration, input, "post-return Close result = %+v, want %+v", fastResult, wantResult)
		}
		if fastElapsed >= time.Millisecond {
			pbtFailf(t, seed, iteration, input, "post-return Close took %s, want under 1ms", fastElapsed)
		}
		if got := server.count(); got != beforeFastPath {
			pbtFailf(t, seed, iteration, input, "post-return Close added %d requests, want 0", got-beforeFastPath)
		}
	}
}
