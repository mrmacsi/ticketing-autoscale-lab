package engine

import (
	"context"
	"math"
	"sync"
	"testing"
	"time"

	"github.com/mrmacsi/ticketing-autoscale-lab/load-generator/config"
	"github.com/mrmacsi/ticketing-autoscale-lab/load-generator/internal/fakeapi"
	"github.com/mrmacsi/ticketing-autoscale-lab/load-generator/metrics"
	"github.com/mrmacsi/ticketing-autoscale-lab/load-generator/scenarios"
)

type memSink struct {
	mu      sync.Mutex
	buckets []metrics.Bucket
}

func (s *memSink) Write(b metrics.Bucket) {
	s.mu.Lock()
	s.buckets = append(s.buckets, b)
	s.mu.Unlock()
}
func (s *memSink) Close(context.Context) error { return nil }
func (s *memSink) all() []metrics.Bucket {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]metrics.Bucket(nil), s.buckets...)
}

var testLimits = config.Limits{MaxRate: 2000, MaxVUs: 2000, MaxDurationSeconds: 60}

func availabilityConfig(rate float64, seconds int) config.TrafficConfig {
	return config.TrafficConfig{
		ExperimentID: "exp_engine", EventID: "evt-1", Seed: 42, DurationSeconds: seconds, MaxVUs: 2000,
		RequestTimeoutMs: 10000,
		Pattern:          config.Pattern{Type: "constant", Rate: rate},
		Mix:              config.Mix{Availability: 100},
		Behaviour:        config.Behaviour{MaxQuantity: 1},
	}
}

func runEngine(t *testing.T, ctx context.Context, srv *fakeapi.Server, cfg config.TrafficConfig, limits config.Limits) (Result, []metrics.Bucket) {
	t.Helper()
	sink := &memSink{}
	res := Run(ctx, cfg, Options{TargetBaseURL: srv.URL, Limits: limits, Sink: sink, DrainGrace: 5 * time.Second})
	return res, sink.all()
}

// expectedArrivals replays the seeded schedule offline.
func expectedArrivals(t *testing.T, cfg config.TrafficConfig, limits config.Limits) int64 {
	t.Helper()
	pat, err := scenarios.NewPattern(cfg.Pattern, cfg.DurationSeconds, cfg.Seed, limits.MaxRate)
	if err != nil {
		t.Fatal(err)
	}
	s := scenarios.NewSchedule(pat, cfg.Seed)
	var n int64
	for {
		if _, ok := s.Next(); !ok {
			return n
		}
		n++
	}
}

func checkContiguous(t *testing.T, buckets []metrics.Bucket) {
	t.Helper()
	for i, b := range buckets {
		if b.Second != i {
			t.Fatalf("bucket %d has second %d; series must be contiguous", i, b.Second)
		}
	}
}

func TestAchievedRateMatchesRequested(t *testing.T) {
	srv := fakeapi.New(fakeapi.Options{})
	defer srv.Close()
	cfg := availabilityConfig(200, 3)
	res, buckets := runEngine(t, context.Background(), srv, cfg, testLimits)
	if res.Err != nil || res.Reason != ReasonCompleted {
		t.Fatalf("result = %+v", res)
	}
	s := res.Summary
	want := expectedArrivals(t, cfg, testLimits)
	if s.ArrivalsScheduled != want || s.ArrivalsStarted != want || s.ArrivalsDropped != 0 {
		t.Fatalf("scheduled=%d started=%d dropped=%d, want %d scheduled", s.ArrivalsScheduled, s.ArrivalsStarted, s.ArrivalsDropped, want)
	}
	if math.Abs(float64(want)-600) > 60 || math.Abs(s.ArrivalsRequested-600) > 1e-6 {
		t.Fatalf("requested=%v scheduled=%d, want ~600", s.ArrivalsRequested, want)
	}
	if s.Requests.Total != want || s.Requests.OK2xx != want || s.CompletedJourneys != want {
		t.Fatalf("requests=%+v completed=%d", s.Requests, s.CompletedJourneys)
	}
	if srv.Stats().Requests != want+1 { // +1 for the setup event fetch
		t.Fatalf("server saw %d requests, generator reported %d", srv.Stats().Requests, want)
	}
	checkContiguous(t, buckets)
	for _, b := range buckets[:3] {
		if b.TargetRate != 200 || b.ScheduledArrivals < 140 || b.ScheduledArrivals > 260 {
			t.Errorf("second %d: target=%v scheduled=%d", b.Second, b.TargetRate, b.ScheduledArrivals)
		}
		if b.SchedulerLagMsMax > 100 {
			t.Errorf("second %d: scheduler lag %vms", b.Second, b.SchedulerLagMsMax)
		}
	}
	if len(s.Instances) != 2 || s.Instances["app-a"].Requests == 0 || s.Instances["app-b"].Requests == 0 {
		t.Fatalf("instances = %+v", s.Instances)
	}
}

func TestOpenLoopKeepsRateWithSlowServer(t *testing.T) {
	srv := fakeapi.New(fakeapi.Options{Delay: 500 * time.Millisecond})
	defer srv.Close()
	cfg := availabilityConfig(200, 3)
	start := time.Now()
	res, buckets := runEngine(t, context.Background(), srv, cfg, testLimits)
	s := res.Summary
	want := expectedArrivals(t, cfg, testLimits)
	// A closed-loop generator would slow down here; the open-loop scheduler
	// emits exactly the same seeded schedule as against a fast server.
	if s.ArrivalsScheduled != want || s.ArrivalsStarted != want || s.ArrivalsDropped != 0 {
		t.Fatalf("scheduled=%d started=%d dropped=%d, want %d", s.ArrivalsScheduled, s.ArrivalsStarted, s.ArrivalsDropped, want)
	}
	if s.MaxActiveVUs < 70 {
		t.Fatalf("max active VUs = %d; ~100 concurrent journeys expected at 200/s x 0.5s", s.MaxActiveVUs)
	}
	if s.Requests.Total != want || *s.Latency.P50 < 500 {
		t.Fatalf("requests=%d p50=%v", s.Requests.Total, *s.Latency.P50)
	}
	if el := time.Since(start); el > 5*time.Second {
		t.Fatalf("run took %s", el)
	}
	for _, b := range buckets[:3] {
		if b.ScheduledArrivals < 140 || b.ScheduledArrivals > 260 || b.SchedulerLagMsMax > 100 {
			t.Errorf("second %d: scheduled=%d lag=%v", b.Second, b.ScheduledArrivals, b.SchedulerLagMsMax)
		}
	}
	// Requests are attributed to their completion second: the drain second
	// after the pattern ends has no arrivals but receives the late responses.
	if len(buckets) < 4 || buckets[3].ScheduledArrivals != 0 || buckets[3].Requests.Total < 50 {
		t.Errorf("drain bucket = %+v", buckets[len(buckets)-1])
	}
}

func TestSaturationCountsDroppedArrivals(t *testing.T) {
	srv := fakeapi.New(fakeapi.Options{Delay: 500 * time.Millisecond})
	defer srv.Close()
	cfg := availabilityConfig(100, 2)
	cfg.MaxVUs = 5
	res, _ := runEngine(t, context.Background(), srv, cfg, testLimits)
	s := res.Summary
	if s.ArrivalsScheduled != s.ArrivalsStarted+s.ArrivalsDropped {
		t.Fatalf("scheduled %d != started %d + dropped %d", s.ArrivalsScheduled, s.ArrivalsStarted, s.ArrivalsDropped)
	}
	if s.ArrivalsDropped < 150 || s.ArrivalsStarted > 30 || s.MaxActiveVUs > 5 {
		t.Fatalf("started=%d dropped=%d maxActive=%d", s.ArrivalsStarted, s.ArrivalsDropped, s.MaxActiveVUs)
	}
	if srv.Stats().MaxConcurrent > 5 {
		t.Fatalf("server saw %d concurrent requests with maxVUs=5", srv.Stats().MaxConcurrent)
	}
	if *s.AchievedVsRequest > 20 {
		t.Fatalf("achieved = %v%%", *s.AchievedVsRequest)
	}
}

func TestDurationIsEnforced(t *testing.T) {
	srv := fakeapi.New(fakeapi.Options{})
	defer srv.Close()
	start := time.Now()
	res, buckets := runEngine(t, context.Background(), srv, availabilityConfig(50, 2), testLimits)
	el := time.Since(start)
	if res.Reason != ReasonCompleted || el < 2*time.Second || el > 2700*time.Millisecond {
		t.Fatalf("reason=%s elapsed=%s", res.Reason, el)
	}
	checkContiguous(t, buckets)
	if n := len(buckets); n < 2 || n > 3 {
		t.Fatalf("buckets = %d", n)
	}
	for _, b := range buckets[2:] {
		if b.TargetRate != 0 || b.ScheduledArrivals != 0 {
			t.Fatalf("arrivals after the duration: %+v", b)
		}
	}
}

func TestMaxDurationClampsRun(t *testing.T) {
	srv := fakeapi.New(fakeapi.Options{})
	defer srv.Close()
	limits := testLimits
	limits.MaxDurationSeconds = 1
	start := time.Now()
	res, _ := runEngine(t, context.Background(), srv, availabilityConfig(50, 30), limits)
	if el := time.Since(start); res.Reason != ReasonMaxDuration || el > 1700*time.Millisecond {
		t.Fatalf("reason=%s elapsed=%s notes=%v", res.Reason, el, res.Notes)
	}
	if len(res.Notes) == 0 {
		t.Fatal("expected a clamp note")
	}
}

func TestCancelIsImmediate(t *testing.T) {
	srv := fakeapi.New(fakeapi.Options{Delay: 10 * time.Second, FastEventFetch: true})
	defer srv.Close()
	ctx, cancel := context.WithCancelCause(context.Background())
	defer cancel(nil)
	var cancelledAt time.Time
	started := make(chan struct{})
	go func() {
		<-started
		time.Sleep(time.Second)
		cancelledAt = time.Now()
		cancel(&StopCause{Reason: ReasonStopped, Message: "stopped by test"})
	}()
	sink := &memSink{}
	res := Run(ctx, availabilityConfig(100, 30), Options{
		TargetBaseURL: srv.URL, Limits: testLimits, Sink: sink,
		OnStarted: func(time.Time) { close(started) },
	})
	stopLatency := time.Since(cancelledAt)
	if res.Reason != ReasonStopped || res.Message != "stopped by test" {
		t.Fatalf("reason = %s (%s)", res.Reason, res.Message)
	}
	if stopLatency > time.Second {
		t.Fatalf("stop took %s with requests in flight", stopLatency)
	}
	s := res.Summary
	if s.ArrivalsStarted < 50 || s.Requests.Total != 0 {
		t.Fatalf("started=%d requests=%d: in-flight requests must be aborted, not recorded", s.ArrivalsStarted, s.Requests.Total)
	}
	// Scheduling stopped at the cancel: nothing after ~1s.
	if s.ArrivalsScheduled > 200 {
		t.Fatalf("scheduled %d arrivals after a 1s run at 100/s", s.ArrivalsScheduled)
	}
}

func TestTotalArrivalsCap(t *testing.T) {
	srv := fakeapi.New(fakeapi.Options{})
	defer srv.Close()
	cfg := availabilityConfig(200, 30)
	cfg.TotalArrivals = 50
	start := time.Now()
	res, _ := runEngine(t, context.Background(), srv, cfg, testLimits)
	if res.Reason != ReasonTotalArrivals || res.Summary.ArrivalsScheduled != 50 || res.Summary.CompletedJourneys != 50 {
		t.Fatalf("result = %s scheduled=%d completed=%d", res.Reason, res.Summary.ArrivalsScheduled, res.Summary.CompletedJourneys)
	}
	if el := time.Since(start); el > 2*time.Second {
		t.Fatalf("took %s", el)
	}
}

// TestFlashSale sends 1000 purchase arrivals at an event with 100 tickets and
// checks the generator reports exactly what the server confirmed.
func TestFlashSale(t *testing.T) {
	srv := fakeapi.New(fakeapi.Options{Categories: map[string]int64{"cat-a": 60, "cat-b": 40}})
	defer srv.Close()
	cfg := config.TrafficConfig{
		ExperimentID: "exp_flash", EventID: "evt-1", Seed: 7, DurationSeconds: 10, MaxVUs: 1000,
		TotalArrivals: 1000, RequestTimeoutMs: 10000,
		Pattern:   config.Pattern{Type: "constant", Rate: 1000},
		Mix:       config.Mix{Purchase: 100},
		Behaviour: config.Behaviour{MaxQuantity: 4, DuplicateSubmitProbability: 0.2},
	}
	res, buckets := runEngine(t, context.Background(), srv, cfg, testLimits)
	if res.Reason != ReasonTotalArrivals {
		t.Fatalf("reason = %s (%s)", res.Reason, res.Message)
	}
	st := srv.Stats()
	b := res.Summary.Business
	if res.Summary.ArrivalsStarted != 1000 || b.ReservationsAttempted != 1000 {
		t.Fatalf("started=%d attempted=%d", res.Summary.ArrivalsStarted, b.ReservationsAttempted)
	}
	if b.PurchasesCompleted != st.PaidOrders || b.TicketsPurchased != st.TicketsIssued {
		t.Fatalf("generator purchases=%d tickets=%d, server paid=%d tickets=%d", b.PurchasesCompleted, b.TicketsPurchased, st.PaidOrders, st.TicketsIssued)
	}
	if b.ReservationsSucceeded != st.ReservationsCreated || b.ReservationsRejected != st.Rejected409 {
		t.Fatalf("reservations ok=%d rejected=%d, server created=%d rejected=%d", b.ReservationsSucceeded, b.ReservationsRejected, st.ReservationsCreated, st.Rejected409)
	}
	if st.TicketsIssued > 100 || st.TicketsIssued < 90 || b.ReservationsSucceeded+b.ReservationsRejected != 1000 {
		t.Fatalf("tickets issued = %d, business = %+v", st.TicketsIssued, b)
	}
	if b.DuplicateSubmissions == 0 || b.IdempotentReplays != b.DuplicateSubmissions {
		t.Fatalf("duplicates=%d replays=%d", b.DuplicateSubmissions, b.IdempotentReplays)
	}
	if res.Summary.Requests.Errors() != 0 || res.Summary.Requests.Conflict409 < b.ReservationsRejected {
		t.Fatalf("requests = %+v", res.Summary.Requests)
	}
	var perSecondTickets int64
	for _, bk := range buckets {
		perSecondTickets += bk.Business.TicketsPurchased
	}
	if perSecondTickets != st.TicketsIssued {
		t.Fatalf("bucket tickets sum %d != server %d", perSecondTickets, st.TicketsIssued)
	}
}

func TestUnreachableTargetFails(t *testing.T) {
	res := Run(context.Background(), availabilityConfig(10, 1), Options{TargetBaseURL: "http://127.0.0.1:1", Limits: testLimits})
	if res.Reason != ReasonError || res.Err == nil {
		t.Fatalf("result = %+v", res)
	}
}

func TestInvalidConfigFails(t *testing.T) {
	cfg := availabilityConfig(10, 1)
	cfg.Mix = config.Mix{Browse: 50}
	res := Run(context.Background(), cfg, Options{TargetBaseURL: "http://127.0.0.1:1", Limits: testLimits})
	if res.Reason != ReasonError || res.Err == nil {
		t.Fatalf("result = %+v", res)
	}
}
