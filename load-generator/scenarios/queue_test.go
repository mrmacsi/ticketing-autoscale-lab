package scenarios

import (
	"context"
	"net/http"
	"net/http/httptest"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/mrmacsi/ticketing-autoscale-lab/load-generator/internal/fakeapi"
	"github.com/mrmacsi/ticketing-autoscale-lab/load-generator/metrics"
	"github.com/mrmacsi/ticketing-autoscale-lab/load-generator/queueapi"
	"github.com/mrmacsi/ticketing-autoscale-lab/load-generator/ticketapi"
)

// queueFixture is a fake app with the fake waiting room on the same host and
// an executor whose app and queue clients report to one real Aggregator.
type queueFixture struct {
	srv   *fakeapi.Server
	queue *fakeapi.Queue
	agg   *metrics.Aggregator
	x     *Executor
	cnt   *counter
	// setupRequests is the app request count after the setup event fetch.
	setupRequests int64
}

func newQueueFixture(t *testing.T, capacity int, appOpts fakeapi.Options, timeout time.Duration) *queueFixture {
	t.Helper()
	q := fakeapi.NewQueue(fakeapi.QueueOptions{Capacity: capacity})
	appOpts.Queue = q
	appOpts.RequireQueueToken = true
	srv := fakeapi.New(appOpts)
	t.Cleanup(srv.Close)
	f := &queueFixture{srv: srv, queue: q}
	f.agg = metrics.NewAggregator(metrics.AggregatorOptions{Start: time.Now()})
	f.x = f.executor(t, srv.URL, timeout)
	f.setupRequests = srv.Stats().Requests
	return f
}

func (f *queueFixture) executor(t *testing.T, queueURL string, timeout time.Duration) *Executor {
	t.Helper()
	c, err := ticketapi.NewClient(f.srv.URL, ticketapi.NewHTTPClient(50, timeout), timeout, f.agg)
	if err != nil {
		t.Fatal(err)
	}
	ev, err := c.FetchEvent(context.Background(), "evt-1")
	if err != nil {
		t.Fatal(err)
	}
	qc, err := queueapi.NewClient(queueURL, queueapi.NewHTTPClient(50), f.agg)
	if err != nil {
		t.Fatal(err)
	}
	f.cnt = &counter{}
	return &Executor{Client: c, Counter: f.cnt, Event: ev, Queue: qc, QueueMetrics: f.agg}
}

// total merges every bucket the aggregator emits.
func (f *queueFixture) total() metrics.SummaryReport {
	s := metrics.NewSummary()
	for _, b := range f.agg.Flush(true) {
		s.Add(b)
	}
	return s.Report()
}

func (f *queueFixture) appRequests() int64 { return f.srv.Stats().Requests - f.setupRequests }

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestQueuedPurchaseSendsTokenOnEveryReservationAttemptAndReleases(t *testing.T) {
	// The first reservation response is lost (timeout -> retry with the same
	// key), then a planned duplicate submission follows: all three requests
	// must carry the admission token.
	f := newQueueFixture(t, 10, fakeapi.Options{LoseFirstResponse: map[string]time.Duration{"reservation": 2 * time.Second}}, 300*time.Millisecond)
	p := purchasePlan(baseConfig(1))
	p.DuplicateReservation = true
	if out := f.x.Run(context.Background(), p); out != OutcomePurchased {
		t.Fatalf("outcome = %s", out)
	}
	st := f.srv.Stats()
	if len(st.ReservationTokens) != 3 {
		t.Fatalf("reservation requests = %d, want 3", len(st.ReservationTokens))
	}
	for _, tok := range st.ReservationTokens {
		if tok == "" || tok != st.ReservationTokens[0] {
			t.Fatalf("tokens = %q", st.ReservationTokens)
		}
	}
	if ev, code := fakeapi.VerifyQueueToken(fakeapi.DefaultAdminToken, st.ReservationTokens[0], time.Now()); code != "" || ev != "evt-1" {
		t.Fatalf("token verify = %q %q", ev, code)
	}
	if st.QueueTokenRejected != 0 || st.PaidOrders != 1 {
		t.Fatalf("server stats = %+v", st)
	}
	qs := f.queue.Stats()
	if qs.Joins != 1 || qs.Admitted != 1 || qs.Released["purchased"] != 1 || f.queue.Active() != 0 {
		t.Fatalf("queue stats = %+v active=%d", qs, f.queue.Active())
	}
	sum := f.total()
	if q := sum.Queue; q.Joined != 1 || q.Admitted != 1 || q.WaitMs.Count != 1 || q.SoldOut != 0 || q.Failed != 0 {
		t.Fatalf("queue summary = %+v", q)
	}
}

func TestQueuedReserveReleasesAsAbandonedAndOrdersAreNotGated(t *testing.T) {
	f := newQueueFixture(t, 10, fakeapi.Options{}, 2*time.Second)
	p := purchasePlan(baseConfig(1))
	p.Kind = KindReserve
	if out := f.x.Run(context.Background(), p); out != OutcomeReservedAbandoned {
		t.Fatalf("outcome = %s", out)
	}
	if got := f.queue.Stats().Released; got["abandoned"] != 1 {
		t.Fatalf("released = %v", got)
	}
}

func TestWithoutTokenTheAppRefusesReservation(t *testing.T) {
	// Sanity check of the fake's enforcement: a non-queued checkout fails.
	f := newQueueFixture(t, 10, fakeapi.Options{}, 2*time.Second)
	f.x.Queue = nil
	if out := f.x.Run(context.Background(), purchasePlan(baseConfig(1))); out != OutcomeFailed {
		t.Fatalf("outcome = %s", out)
	}
	if f.srv.Stats().QueueTokenRejected != 1 {
		t.Fatal("reservation without token must be rejected with 403")
	}
}

func TestOpenRoomChecksOutWithoutToken(t *testing.T) {
	q := fakeapi.NewQueue(fakeapi.QueueOptions{Disabled: true})
	srv := fakeapi.New(fakeapi.Options{Queue: q})
	defer srv.Close()
	f := &queueFixture{srv: srv, queue: q, agg: metrics.NewAggregator(metrics.AggregatorOptions{Start: time.Now()})}
	f.x = f.executor(t, srv.URL, 2*time.Second)
	if out := f.x.Run(context.Background(), purchasePlan(baseConfig(1))); out != OutcomePurchased {
		t.Fatalf("outcome = %s", out)
	}
	if toks := srv.Stats().ReservationTokens; len(toks) != 1 || toks[0] != "" {
		t.Fatalf("open room must not send a token: %q", toks)
	}
	if q := f.total().Queue; q.Open != 1 || q.Joined != 1 || q.Admitted != 0 || q.WaitMs.Count != 0 {
		t.Fatalf("queue summary = %+v", q)
	}
}

func TestCustomersWaitWhenFullAndAreAdmittedFIFO(t *testing.T) {
	f := newQueueFixture(t, 0, fakeapi.Options{Delay: 20 * time.Millisecond}, 2*time.Second)
	const n = 6
	outs := make([]Outcome, n)
	var wg sync.WaitGroup
	for i := range n {
		p := purchasePlan(baseConfig(int64(i + 1)))
		wg.Add(1)
		go func() {
			defer wg.Done()
			outs[i] = f.x.Run(context.Background(), p)
		}()
		// Start the next customer only once this one is in line, so the join
		// order is known.
		waitFor(t, "customer in line", func() bool { return f.queue.Waiting() == i+1 })
	}
	waitFor(t, "waiting gauge", func() bool { return f.agg.QueueWaiting() == n })
	if f.appRequests() != 0 {
		t.Fatalf("waiting customers sent %d app requests", f.appRequests())
	}

	f.queue.SetCapacity(2)
	wg.Wait()
	for i, o := range outs {
		if o != OutcomePurchased {
			t.Fatalf("customer %d outcome = %s", i, o)
		}
	}
	qs := f.queue.Stats()
	if !slices.Equal(qs.AdmissionOrder, qs.JoinOrder) || len(qs.JoinOrder) != n {
		t.Fatalf("admission order %v != join order %v", qs.AdmissionOrder, qs.JoinOrder)
	}
	if qs.Released["purchased"] != n || f.queue.Active() != 0 {
		t.Fatalf("released = %v active = %d", qs.Released, f.queue.Active())
	}
	if g := f.agg.QueueWaiting(); g != 0 {
		t.Fatalf("waiting gauge = %d after everyone left the line", g)
	}
	sum := f.total()
	if q := sum.Queue; q.Joined != n || q.Admitted != n || q.WaitMs.Count != n || q.PeakWaiting != 0 {
		// PeakWaiting is 0 because the only flush happened after the line emptied.
		t.Fatalf("queue summary = %+v", q)
	}
	if sum.Queue.WaitMs.P50 == nil || *sum.Queue.WaitMs.P50 <= 0 {
		t.Fatalf("waitMs = %+v", sum.Queue.WaitMs)
	}
}

func TestSoldOutInQueueEndsJourneyWithoutTouchingTheApp(t *testing.T) {
	f := newQueueFixture(t, 0, fakeapi.Options{}, 2*time.Second)
	p := purchasePlan(baseConfig(1))
	p.SyntheticCPUMs = 25 // must not be sent either
	done := make(chan Outcome, 1)
	go func() { done <- f.x.Run(context.Background(), p) }()
	waitFor(t, "customer in line", func() bool { return f.queue.Waiting() == 1 })
	f.queue.DeclareSoldOut()
	select {
	case out := <-done:
		if out != OutcomeSoldOutInQueue {
			t.Fatalf("outcome = %s", out)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("sold_out did not end the long poll")
	}
	if n := f.appRequests(); n != 0 {
		t.Fatalf("app requests = %d, want 0", n)
	}
	if f.cnt.cpu != 0 || f.cnt.biz[metrics.BizReservationAttempted] != 0 {
		t.Fatalf("counters = %v cpu=%d", f.cnt.biz, f.cnt.cpu)
	}
	if got := f.queue.Stats().Released; got["sold_out"] != 1 {
		t.Fatalf("released = %v", got)
	}
	sum := f.total()
	if q := sum.Queue; q.SoldOut != 1 || q.Joined != 1 || q.Admitted != 0 {
		t.Fatalf("queue summary = %+v", q)
	}
	if sum.Requests.Total != 0 {
		t.Fatalf("app requests in metrics = %+v", sum.Requests)
	}
	// A customer arriving after the room is sold out is told at once.
	if out := f.x.Run(context.Background(), purchasePlan(baseConfig(2))); out != OutcomeSoldOutInQueue {
		t.Fatalf("late customer outcome = %s", out)
	}
}

func TestQueueRequestsAreExcludedFromAppMetrics(t *testing.T) {
	f := newQueueFixture(t, 1, fakeapi.Options{}, 2*time.Second)
	var wg sync.WaitGroup
	for i := range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if out := f.x.Run(context.Background(), purchasePlan(baseConfig(int64(i+1)))); out != OutcomePurchased {
				t.Errorf("outcome = %s", out)
			}
		}()
	}
	wg.Wait()
	sum := f.total()
	app := f.appRequests()
	qs := f.queue.Stats()
	if sum.Requests.Total != app || sum.Latency.Count != app {
		t.Fatalf("app requests: metrics %d / latency %d, server %d", sum.Requests.Total, sum.Latency.Count, app)
	}
	var byEndpoint int64
	for _, e := range sum.ByEndpoint {
		byEndpoint += e.Count
	}
	var byInstance int64
	for inst, v := range sum.Instances {
		if inst == "waiting-room" {
			t.Fatal("queue responses must not appear in instances")
		}
		byInstance += v.Requests
	}
	if byEndpoint != app || byInstance != app {
		t.Fatalf("byEndpoint=%d byInstance=%d app=%d", byEndpoint, byInstance, app)
	}
	if q := sum.Queue.Requests; q.Total != qs.Requests || q.OK != qs.Requests || q.Errors != 0 || q.Total < 4*2 {
		t.Fatalf("queue requests = %+v, server saw %d", q, qs.Requests)
	}
}

func TestQueueFailuresAreRetriedWithBackoff(t *testing.T) {
	f := newQueueFixture(t, 10, fakeapi.Options{}, 2*time.Second)
	var mu sync.Mutex
	var retries []int
	f.x.QueueBackoff = func(n int) time.Duration {
		mu.Lock()
		retries = append(retries, n)
		mu.Unlock()
		return 5 * time.Millisecond
	}
	f.queue.FailNext(3, http.StatusServiceUnavailable)
	if out := f.x.Run(context.Background(), purchasePlan(baseConfig(1))); out != OutcomePurchased {
		t.Fatalf("outcome = %s", out)
	}
	if !slices.Equal(retries, []int{1, 2, 3}) {
		t.Fatalf("backoff calls = %v", retries)
	}
	q := f.total().Queue
	if q.Joined != 1 || q.Admitted != 1 || q.Requests.Errors != 3 || q.Failed != 0 {
		t.Fatalf("queue summary = %+v", q)
	}
}

func TestQueueBackoffSchedule(t *testing.T) {
	want := []time.Duration{time.Second, 2 * time.Second, 4 * time.Second, 5 * time.Second, 5 * time.Second, 5 * time.Second}
	for i, w := range want {
		if got := queueapi.Backoff(i + 1); got != w {
			t.Errorf("Backoff(%d) = %s, want %s", i+1, got, w)
		}
	}
}

func TestQueueRetryUsesRealBackoffDelays(t *testing.T) {
	f := newQueueFixture(t, 10, fakeapi.Options{}, 2*time.Second)
	f.queue.FailNext(2, http.StatusBadGateway)
	start := time.Now()
	if out := f.x.Run(context.Background(), purchasePlan(baseConfig(1))); out != OutcomePurchased {
		t.Fatalf("outcome = %s", out)
	}
	if el := time.Since(start); el < 3*time.Second || el > 5*time.Second {
		t.Fatalf("two retries took %s, want 1 s + 2 s", el)
	}
}

func TestUnreachableQueueRetriesUntilDeadline(t *testing.T) {
	f := newQueueFixture(t, 10, fakeapi.Options{}, 2*time.Second)
	dead := httptest.NewServer(http.NotFoundHandler())
	deadURL := dead.URL
	dead.Close() // connection refused from now on
	x := f.executor(t, deadURL, 2*time.Second)
	f.setupRequests = f.srv.Stats().Requests
	var calls int
	x.QueueBackoff = func(n int) time.Duration { calls = n; return 20 * time.Millisecond }
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	if out := x.Run(ctx, purchasePlan(baseConfig(1))); out != OutcomeAborted {
		t.Fatalf("outcome = %s", out)
	}
	if calls < 3 {
		t.Fatalf("only %d retries before the deadline", calls)
	}
	q := f.total().Queue
	if q.Failed != 0 || q.Joined != 0 || q.Requests.Errors < 3 {
		t.Fatalf("queue summary = %+v", q)
	}
	if f.appRequests() != 0 {
		t.Fatal("no app request may be sent without a queue decision")
	}
}

func TestNonRetryableQueueErrorFailsJourney(t *testing.T) {
	q := fakeapi.NewQueue(fakeapi.QueueOptions{EventID: "another-event", Capacity: 10})
	srv := fakeapi.New(fakeapi.Options{Queue: q, RequireQueueToken: true})
	defer srv.Close()
	f := &queueFixture{srv: srv, queue: q, agg: metrics.NewAggregator(metrics.AggregatorOptions{Start: time.Now()})}
	f.x = f.executor(t, srv.URL, 2*time.Second)
	f.setupRequests = srv.Stats().Requests
	if out := f.x.Run(context.Background(), purchasePlan(baseConfig(1))); out != OutcomeQueueFailed {
		t.Fatalf("outcome = %s", out)
	}
	if qq := f.total().Queue; qq.Failed != 1 || qq.Requests.Errors != 1 || qq.Joined != 0 {
		t.Fatalf("queue summary = %+v", qq)
	}
	if f.appRequests() != 0 {
		t.Fatal("app must not be touched")
	}
}

func TestLostQueueEntryRejoins(t *testing.T) {
	f := newQueueFixture(t, 0, fakeapi.Options{}, 2*time.Second)
	f.x.QueueBackoff = func(int) time.Duration { return 5 * time.Millisecond }
	done := make(chan Outcome, 1)
	go func() { done <- f.x.Run(context.Background(), purchasePlan(baseConfig(1))) }()
	waitFor(t, "customer in line", func() bool { return f.queue.Waiting() == 1 })
	f.queue.Reset() // queue VM restart: the entry is gone
	waitFor(t, "customer re-joined", func() bool { return f.queue.Waiting() == 1 })
	f.queue.SetCapacity(1)
	if out := <-done; out != OutcomePurchased {
		t.Fatalf("outcome = %s", out)
	}
	if st := f.queue.Stats(); st.Joins != 2 {
		t.Fatalf("joins = %d, want 2", st.Joins)
	}
	if q := f.total().Queue; q.Joined != 1 || q.Admitted != 1 {
		t.Fatalf("a re-join is the same customer: %+v", q)
	}
	if f.agg.QueueWaiting() != 0 {
		t.Fatal("waiting gauge must return to 0")
	}
}

func TestHardStopAbortsWaitingCustomersPromptly(t *testing.T) {
	f := newQueueFixture(t, 0, fakeapi.Options{}, 2*time.Second)
	const n = 25
	ctx, cancel := context.WithCancel(context.Background())
	outs := make(chan Outcome, n)
	for i := range n {
		go func() { outs <- f.x.Run(ctx, purchasePlan(baseConfig(int64(i+1)))) }()
	}
	waitFor(t, "all customers waiting", func() bool { return f.agg.QueueWaiting() == n })
	start := time.Now()
	cancel()
	for range n {
		select {
		case o := <-outs:
			if o != OutcomeAborted {
				t.Fatalf("outcome = %s", o)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("waiting customer was not aborted promptly")
		}
	}
	if el := time.Since(start); el > time.Second {
		t.Fatalf("abort took %s", el)
	}
	if g := f.agg.QueueWaiting(); g != 0 {
		t.Fatalf("waiting gauge = %d after abort", g)
	}
	if f.appRequests() != 0 {
		t.Fatal("app must not be touched")
	}
}

func TestReleaseOutcomeMapping(t *testing.T) {
	for o, want := range map[Outcome]string{
		OutcomePurchased: "purchased", OutcomeSoldOut: "sold_out", OutcomeFailed: "failed",
		OutcomeAbandoned: "abandoned", OutcomeReservedAbandoned: "abandoned", OutcomeDeclined: "abandoned", OutcomeExpired: "abandoned",
	} {
		if got := releaseOutcome(o); got != want {
			t.Errorf("releaseOutcome(%s) = %s, want %s", o, got, want)
		}
	}
}

func TestBrowseAndAvailabilityNeverQueue(t *testing.T) {
	f := newQueueFixture(t, 0, fakeapi.Options{}, 2*time.Second)
	for _, k := range []Kind{KindBrowse, KindAvailability} {
		p := purchasePlan(baseConfig(1))
		p.Kind = k
		if out := f.x.Run(context.Background(), p); out == OutcomeAborted || out == OutcomeQueueFailed {
			t.Fatalf("%s outcome = %s", k, out)
		}
	}
	if st := f.queue.Stats(); st.Requests != 0 {
		t.Fatalf("queue requests = %d", st.Requests)
	}
}
