package metrics

import (
	"context"
	"errors"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestBucketIndexFormula(t *testing.T) {
	cases := []struct {
		ms   float64
		want int
	}{
		{0, 0}, {-3, 0}, {0.5, 0}, {1, 0},
		{1.05, 1},  // ln(1.05)/ln(1.1) = 0.51
		{2, 8},     // 7.27
		{10, 25},   // 24.16
		{100, 49},  // 48.32
		{1000, 73}, // 72.48
		{1e30, 250},
	}
	for _, c := range cases {
		if got := BucketIndex(c.ms); got != c.want {
			t.Errorf("BucketIndex(%v) = %d, want %d", c.ms, got, c.want)
		}
	}
	// The representative value is an upper bound within 10%.
	for _, ms := range []float64{1.3, 7, 42, 250, 999, 12345} {
		v := BucketValue(BucketIndex(ms))
		if v < ms || v > ms*1.1 {
			t.Errorf("BucketValue for %v = %v, want within [ms, 1.1*ms]", ms, v)
		}
	}
}

func TestHistogramPercentilesAndMerge(t *testing.T) {
	var a, b Histogram
	for range 90 {
		a.Observe(10)
	}
	for range 10 {
		b.Observe(1000)
	}
	if _, ok := a.Percentile(0.5); !ok {
		t.Fatal("expected percentile")
	}
	var empty Histogram
	if _, ok := empty.Percentile(0.5); ok {
		t.Fatal("empty histogram must not report a percentile")
	}
	a.Merge(&b)
	if a.Count() != 100 {
		t.Fatalf("count = %d", a.Count())
	}
	p50, _ := a.Percentile(0.50)
	p90, _ := a.Percentile(0.90)
	p95, _ := a.Percentile(0.95)
	p99, _ := a.Percentile(0.99)
	if p50 != BucketValue(25) || p90 != BucketValue(25) {
		t.Errorf("p50/p90 = %v/%v, want %v", p50, p90, BucketValue(25))
	}
	if p95 != BucketValue(73) || p99 != BucketValue(73) {
		t.Errorf("p95/p99 = %v/%v, want %v", p95, p99, BucketValue(73))
	}
	sparse := a.Sparse()
	if len(sparse) != 2 || sparse["25"] != 90 || sparse["73"] != 10 {
		t.Fatalf("sparse = %v", sparse)
	}
	back, err := HistogramFromSparse(sparse)
	if err != nil || back.Count() != 100 {
		t.Fatalf("round trip: %v %v", back, err)
	}
	if _, err := HistogramFromSparse(map[string]int64{"251": 1}); err == nil {
		t.Fatal("out-of-range bucket must fail")
	}
}

func TestClassifyStatus(t *testing.T) {
	cases := map[int]Class{
		200: ClassOK2xx, 201: ClassOK2xx, 409: ClassConflict409, 410: ClassGone410,
		402: ClassPayment402, 404: ClassClient4xx, 422: ClassClient4xx, 302: ClassClient4xx,
		500: ClassServer5xx, 503: ClassServer5xx,
	}
	for code, want := range cases {
		if got := ClassifyStatus(code); got != want {
			t.Errorf("ClassifyStatus(%d) = %v, want %v", code, got, want)
		}
	}
	for _, c := range []Class{ClassOK2xx, ClassConflict409, ClassGone410, ClassPayment402} {
		if c.IsError() {
			t.Errorf("class %v must not be an application error", c)
		}
	}
	for _, c := range []Class{ClassClient4xx, ClassServer5xx, ClassNetworkError, ClassTimeout} {
		if !c.IsError() {
			t.Errorf("class %v must be an application error", c)
		}
	}
}

type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) Now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *fakeClock) Set(d time.Duration, start time.Time) {
	c.mu.Lock()
	c.t = start.Add(d)
	c.mu.Unlock()
}

func TestAggregatorAttributionAndFlush(t *testing.T) {
	start := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
	clk := &fakeClock{t: start}
	var active atomic.Int64
	agg := NewAggregator(AggregatorOptions{
		Start: start, Now: clk.Now, ActiveVUs: active.Load,
		TargetRate: func(s int) float64 { return float64(10 * (s + 1)) },
	})

	clk.Set(200*time.Millisecond, start)
	agg.RecordArrival(true, 2*time.Millisecond, 1)
	agg.RecordArrival(false, 5*time.Millisecond, 1)
	agg.RecordRequest(Observation{Endpoint: EndpointReservation, Class: ClassOK2xx, LatencyMs: 20, HasResponse: true, Instance: "app-a"})
	agg.RecordRequest(Observation{Endpoint: EndpointReservation, Class: ClassConflict409, LatencyMs: 30, HasResponse: true, Instance: "app-b"})
	agg.RecordRequest(Observation{Endpoint: EndpointPayment, Class: ClassServer5xx, LatencyMs: 40, HasResponse: true, Instance: "app-a", Replayed: true})
	agg.RecordRequest(Observation{Endpoint: EndpointOrder, Class: ClassTimeout})
	agg.Count(BizReservationAttempted, 2)
	agg.Count(BizTicketsPurchased, 3)

	// Nothing is final before the second elapses.
	clk.Set(999*time.Millisecond, start)
	if got := agg.Flush(false); len(got) != 0 {
		t.Fatalf("flushed %d buckets before second 0 ended", len(got))
	}

	// A request that started in second 0 but completes in second 2 belongs to second 2.
	clk.Set(2300*time.Millisecond, start)
	agg.RecordRequest(Observation{Endpoint: EndpointBrowse, Class: ClassOK2xx, LatencyMs: 2100, HasResponse: true, Instance: "app-b"})
	active.Store(4)
	got := agg.Flush(false)
	if len(got) != 2 {
		t.Fatalf("want buckets 0 and 1 (empty), got %d", len(got))
	}
	b0, b1 := got[0], got[1]
	if b0.Second != 0 || b1.Second != 1 || !b1.TS.Equal(start.Add(time.Second)) {
		t.Fatalf("seconds/ts wrong: %+v %+v", b0.Second, b1.TS)
	}
	if b0.ScheduledArrivals != 2 || b0.StartedArrivals != 1 || b0.DroppedArrivals != 1 || b0.SchedulerLagMsMax != 5 {
		t.Errorf("arrivals = %+v", b0)
	}
	r := b0.Requests
	if r.Total != 4 || r.OK2xx != 1 || r.Conflict409 != 1 || r.Server5xx != 1 || r.Timeouts != 1 || r.Errors() != 2 {
		t.Errorf("requests = %+v", r)
	}
	if e := b0.ByEndpoint[EndpointReservation]; e.Count != 2 || e.Errors != 0 {
		t.Errorf("reservation endpoint = %+v", e)
	}
	if e := b0.ByEndpoint[EndpointOrder]; e.Count != 1 || e.Errors != 1 {
		t.Errorf("order endpoint = %+v", e)
	}
	if len(b1.ByEndpoint) != len(AllEndpoints) {
		t.Errorf("empty bucket must list every endpoint: %v", b1.ByEndpoint)
	}
	if b0.Latency.Count != 3 || b0.Latency.SumMs != 90 || b0.Latency.MaxMs != 40 {
		t.Errorf("latency = %+v (timeouts have no latency sample)", b0.Latency)
	}
	if b0.Latency.P50 == nil || *b0.Latency.P50 != round1(BucketValue(BucketIndex(30))) {
		t.Errorf("p50 = %v", b0.Latency.P50)
	}
	if a := b0.Instances["app-a"]; a.Requests != 2 || a.Errors != 1 || a.LatencySumMs != 60 {
		t.Errorf("app-a = %+v", a)
	}
	if bb := b0.Instances["app-b"]; bb.Requests != 1 || bb.Errors != 0 {
		t.Errorf("app-b = %+v", bb)
	}
	if b0.Business.IdempotentReplays != 1 || b0.Business.ReservationsAttempted != 2 || b0.Business.TicketsPurchased != 3 {
		t.Errorf("business = %+v", b0.Business)
	}
	if b0.TargetRate != 10 || b1.TargetRate != 20 {
		t.Errorf("target rates = %v, %v", b0.TargetRate, b1.TargetRate)
	}
	if b1.Latency.P50 != nil || b1.Requests.Total != 0 || b1.ActiveVUs != 4 {
		t.Errorf("empty bucket = %+v", b1)
	}

	final := agg.Flush(true)
	if len(final) != 1 || final[0].Second != 2 || final[0].Requests.Total != 1 || final[0].Instances["app-b"].Requests != 1 {
		t.Fatalf("final = %+v", final)
	}
	agg.RecordRequest(Observation{Endpoint: EndpointBrowse, Class: ClassOK2xx})
	if again := agg.Flush(true); again != nil {
		t.Fatal("records after the final flush must be ignored")
	}
}

func TestSummaryMergesHistograms(t *testing.T) {
	s := NewSummary()
	var h1, h2 Histogram
	for range 99 {
		h1.Observe(5)
	}
	h2.Observe(5000)
	s.Add(Bucket{Second: 0, TargetRate: 10, ScheduledArrivals: 9, StartedArrivals: 9, Requests: RequestCounts{Total: 99, OK2xx: 99},
		Latency: LatencyStats{SumMs: 495, MaxMs: 5, Hist: h1.Sparse()}})
	s.Add(Bucket{Second: 1, TargetRate: 10, ScheduledArrivals: 11, StartedArrivals: 10, DroppedArrivals: 1, Requests: RequestCounts{Total: 1, Server5xx: 1},
		Latency: LatencyStats{SumMs: 5000, MaxMs: 5000, Hist: h2.Sparse()}})
	r := s.Report()
	if r.ArrivalsRequested != 20 || r.ArrivalsStarted != 19 || r.ArrivalsDropped != 1 || *r.AchievedVsRequest != 95 {
		t.Errorf("arrivals = %+v", r)
	}
	if r.Latency.Count != 100 || *r.Latency.P50 != round1(BucketValue(BucketIndex(5))) || *r.Latency.P99 != round1(BucketValue(BucketIndex(5))) {
		t.Errorf("merged latency = %+v p50=%v p99=%v", r.Latency, *r.Latency.P50, *r.Latency.P99)
	}
	if r.Latency.MaxMs != 5000 || r.Requests.Total != 100 || r.PeakRequestsPerSec != 99 || r.Seconds != 2 {
		t.Errorf("totals = %+v", r)
	}
}

func TestAsyncSinkRetriesFailedWrites(t *testing.T) {
	var mu sync.Mutex
	attempts := map[int]int{}
	written := map[int]bool{}
	sink := NewAsyncSink(func(_ context.Context, b Bucket) error {
		mu.Lock()
		defer mu.Unlock()
		attempts[b.Second]++
		if attempts[b.Second] < 3 {
			return errors.New("unavailable")
		}
		written[b.Second] = true
		return nil
	}, AsyncSinkOptions{BaseBackoff: time.Millisecond, MaxBackoff: 5 * time.Millisecond})
	for i := range 5 {
		sink.Write(Bucket{Second: i})
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := sink.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if len(written) != 5 {
		t.Fatalf("written = %v", written)
	}
	if w, d, f := sink.Stats(); w != 5 || d != 0 || f != 0 {
		t.Fatalf("stats = %d %d %d", w, d, f)
	}
}

func TestAsyncSinkNeverBlocksAndBoundsQueue(t *testing.T) {
	release := make(chan struct{})
	sink := NewAsyncSink(func(ctx context.Context, _ Bucket) error {
		select {
		case <-release:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}, AsyncSinkOptions{Capacity: 10, WriteTimeout: time.Hour})
	start := time.Now()
	for i := range 10000 {
		sink.Write(Bucket{Second: i})
	}
	if el := time.Since(start); el > 500*time.Millisecond {
		t.Fatalf("Write blocked for %s", el)
	}
	if _, dropped, _ := sink.Stats(); dropped < 9000 {
		t.Fatalf("dropped = %d, want overflow to drop old buckets", dropped)
	}
	close(release)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := sink.Close(ctx); err == nil {
		t.Fatal("Close must report dropped buckets")
	}
}

func TestAsyncSinkCloseHonoursDeadline(t *testing.T) {
	sink := NewAsyncSink(func(context.Context, Bucket) error { return errors.New("down") },
		AsyncSinkOptions{BaseBackoff: time.Hour, MaxAttempts: 100})
	sink.Write(Bucket{})
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	if err := sink.Close(ctx); err == nil {
		t.Fatal("expected unwritten bucket error")
	}
	if el := time.Since(start); el > time.Second {
		t.Fatalf("Close took %s", el)
	}
}

func TestAggregatorQueueStats(t *testing.T) {
	start := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	clk := &fakeClock{t: start}
	agg := NewAggregator(AggregatorOptions{Start: start, Now: clk.Now})

	clk.Set(100*time.Millisecond, start)
	for range 3 {
		agg.CountQueue(QueueJoined)
		agg.AddQueueWaiting(1)
	}
	agg.RecordQueueRequest(true)
	agg.RecordQueueRequest(true)
	agg.RecordQueueRequest(false)
	agg.AddQueueWaiting(-1)
	agg.RecordQueueAdmitted(8000)
	agg.CountQueue(QueueSoldOut)
	agg.CountQueue(QueueOpen)
	agg.CountQueue(QueueFailed)

	clk.Set(1500*time.Millisecond, start)
	b := agg.Flush(false)[0]
	q := b.Queue
	if q.Joined != 3 || q.Admitted != 1 || q.SoldOut != 1 || q.Open != 1 || q.Failed != 1 || q.Waiting != 2 {
		t.Fatalf("queue = %+v", q)
	}
	if q.Requests != (QueueRequestCounts{Total: 3, OK: 2, Errors: 1}) {
		t.Fatalf("queue requests = %+v", q.Requests)
	}
	if q.WaitMs.Count != 1 || q.WaitMs.SumMs != 8000 || q.WaitMs.MaxMs != 8000 || *q.WaitMs.P50 != round1(BucketValue(BucketIndex(8000))) ||
		q.WaitMs.Hist[strconv.Itoa(BucketIndex(8000))] != 1 {
		t.Fatalf("waitMs = %+v", q.WaitMs)
	}
	// Queue traffic never shows up as application traffic.
	if b.Requests.Total != 0 || b.Latency.Count != 0 || len(b.Instances) != 0 {
		t.Fatalf("app metrics polluted: %+v %+v %+v", b.Requests, b.Latency, b.Instances)
	}
	for _, e := range b.ByEndpoint {
		if e.Count != 0 {
			t.Fatalf("byEndpoint polluted: %+v", b.ByEndpoint)
		}
	}

	agg.AddQueueWaiting(-2)
	clk.Set(2500*time.Millisecond, start)
	b1 := agg.Flush(false)[0]
	if b1.Queue.Waiting != 0 || b1.Queue.WaitMs.P50 != nil || b1.Queue.WaitMs.Hist == nil {
		t.Fatalf("empty queue bucket = %+v", b1.Queue)
	}

	s := NewSummary()
	s.Add(b)
	s.Add(b1)
	r := s.Report().Queue
	if r.Joined != 3 || r.PeakWaiting != 2 || r.WaitMs.Count != 1 || r.Requests.Total != 3 || r.SoldOut != 1 {
		t.Fatalf("summary queue = %+v", r)
	}
}
