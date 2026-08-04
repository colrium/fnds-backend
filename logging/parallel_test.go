package logging

import (
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strconv"
	"sync/atomic"
	"testing"
	"time"
)

const parallelShipperCount = 8

type parallelAcceptedEndpoint struct {
	*httptest.Server
	requests atomic.Int64
}

func parallelNewAcceptedEndpoint(t *testing.T) *parallelAcceptedEndpoint {
	t.Helper()
	endpoint := &parallelAcceptedEndpoint{}
	endpoint.Server = httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		endpoint.requests.Add(1)
		response.WriteHeader(http.StatusAccepted)
	}))
	t.Cleanup(endpoint.Close)
	return endpoint
}

func parallelConfig(endpoint string, queueSize int, clock Clock) Config {
	return Config{
		Enabled:     true,
		IngestURL:   endpoint,
		SourceToken: "parallel-source-token-" + strconv.Itoa(queueSize),
		ServiceName: "parallel-isolation",
		Environment: "test",
		MinLevel:    slog.LevelError,
		BatchSize:   queueSize,
		FlushEvery:  time.Minute,
		QueueSize:   queueSize,
		DiagWriter:  io.Discard,
		Clock:       clock,
		Jitter:      FixedJitter(0),
	}
}

func parallelStableGoroutineCount() int {
	previous := runtime.NumGoroutine()
	stableSamples := 0
	for attempt := 0; attempt < 100; attempt++ {
		time.Sleep(time.Millisecond)
		current := runtime.NumGoroutine()
		if current == previous {
			stableSamples++
			if stableSamples == 3 {
				return current
			}
		} else {
			stableSamples = 0
		}
		previous = current
	}
	return previous
}

type parallelCountingListener struct {
	net.Listener
	accepted atomic.Int64
}

func (l *parallelCountingListener) Accept() (net.Conn, error) {
	connection, err := l.Listener.Accept()
	if err == nil {
		l.accepted.Add(1)
	}
	return connection, err
}

// TestParallelShippersKeepIndependentAccounting constructs and drives eight
// independently configured Shippers in t.Parallel subtests. Unique queue and
// drop counts make any package-level shared state visible as cross-test counter
// contamination.
func TestParallelShippersKeepIndependentAccounting(t *testing.T) {
	for index := 0; index < parallelShipperCount; index++ {
		index := index
		t.Run("shipper-"+strconv.Itoa(index), func(t *testing.T) {
			t.Parallel()

			queueSize := 3 + index
			wantDropped := uint64(index + 1)
			endpoint := parallelNewAcceptedEndpoint(t)
			clock := newFakeClock()
			shipper := NewShipper(parallelConfig(endpoint.URL, queueSize, clock))
			logger := slog.New(NewFanoutHandler(
				slog.NewJSONHandler(io.Discard, nil),
				shipper.Handler(),
			))

			// Fill before Start so the accepted and dropped counts are exact and
			// independent of scheduler timing.
			for event := 0; event < queueSize+int(wantDropped); event++ {
				logger.Error("parallel-isolation",
					slog.Int("shipper", index),
					slog.Int("event", event),
				)
			}

			shipper.Start()
			if !clock.WaitTickers(2, deliveryWaitTimeout) {
				t.Fatalf("parallel Shipper %d did not register delivery and health tickers", index)
			}
			flush := shipper.Flush(2 * time.Second)
			if !flush.Drained {
				t.Fatalf("parallel Shipper %d did not drain: result=%+v snapshot=%+v", index, flush, shipper.Snapshot())
			}
			closeResult := shipper.Close()
			if closeResult.TimedOut {
				t.Fatalf("parallel Shipper %d close timed out: %+v", index, closeResult)
			}

			snapshot := shipper.Snapshot()
			if snapshot.Enqueued != uint64(queueSize)+wantDropped {
				t.Errorf("parallel Shipper %d enqueued = %d, want %d", index, snapshot.Enqueued, uint64(queueSize)+wantDropped)
			}
			if snapshot.Delivered != uint64(queueSize) {
				t.Errorf("parallel Shipper %d delivered = %d, want %d", index, snapshot.Delivered, queueSize)
			}
			if snapshot.Dropped != wantDropped {
				t.Errorf("parallel Shipper %d dropped = %d, want %d", index, snapshot.Dropped, wantDropped)
			}
			if snapshot.DroppedByReason[reasonQueueFull] != wantDropped {
				t.Errorf("parallel Shipper %d queue-full drops = %d, want %d", index, snapshot.DroppedByReason[reasonQueueFull], wantDropped)
			}
			if snapshot.QueueDepth != 0 || snapshot.InFlight != 0 {
				t.Errorf("parallel Shipper %d retained work after flush: %+v", index, snapshot)
			}
			if got := endpoint.requests.Load(); got != 1 {
				t.Errorf("parallel Shipper %d endpoint requests = %d, want one independent batch", index, got)
			}
		})
	}
}

// TestDisabledEmptyTokenStartsNoGoroutineAndMakesNoConnection validates the
// empty-token startup path against a live counting listener. The listener's
// Serve goroutine is included in both stable goroutine measurements; any growth
// after Start therefore belongs to the disabled Shipper path under test.
func TestDisabledEmptyTokenStartsNoGoroutineAndMakesNoConnection(t *testing.T) {
	baseListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen on loopback: %v", err)
	}
	listener := &parallelCountingListener{Listener: baseListener}
	server := &http.Server{}
	serveDone := make(chan error, 1)
	go func() { serveDone <- server.Serve(listener) }()
	t.Cleanup(func() {
		_ = server.Close()
		<-serveDone
	})

	baseline := parallelStableGoroutineCount()
	cfg, diagnostics := ResolveConfig(RawConfig{
		SourceToken: "   ",
		IngestURL:   "https://" + listener.Addr().String() + "/logs",
		QueueSize:   "8",
	})
	if cfg.Enabled {
		t.Fatal("empty source token resolved to an enabled Shipper")
	}
	if len(diagnostics) != 1 || diagnostics[0].Level != slog.LevelWarn {
		t.Fatalf("empty source token diagnostics = %+v, want one WARN", diagnostics)
	}
	cfg.DiagWriter = io.Discard
	shipper := NewShipper(cfg)
	shipper.Start()
	shipper.Start()

	logger := slog.New(NewFanoutHandler(slog.NewJSONHandler(io.Discard, nil), shipper.Handler()))
	for event := 0; event < 16; event++ {
		logger.Error("disabled-empty-token", slog.Int("event", event))
	}
	shipper.Close()

	// A connection about to be opened gets a bounded scheduling window, while
	// staying well below the suite's 50 ms maximum sleep.
	time.Sleep(20 * time.Millisecond)
	after := parallelStableGoroutineCount()
	if after > baseline {
		t.Errorf("stable goroutine count grew from %d to %d on disabled startup", baseline, after)
	}
	if got := listener.accepted.Load(); got != 0 {
		t.Errorf("disabled startup opened %d outbound connection(s), want zero", got)
	}
	if snapshot := shipper.Snapshot(); snapshot != (Snapshot{}) {
		t.Errorf("disabled startup snapshot = %+v, want all zero counters", snapshot)
	}
}
