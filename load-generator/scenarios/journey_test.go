package scenarios

import (
	"context"
	"math"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mrmacsi/ticketing-autoscale-lab/load-generator/config"
	"github.com/mrmacsi/ticketing-autoscale-lab/load-generator/internal/fakeapi"
	"github.com/mrmacsi/ticketing-autoscale-lab/load-generator/metrics"
	"github.com/mrmacsi/ticketing-autoscale-lab/load-generator/ticketapi"
)

func baseConfig(seed int64) config.TrafficConfig {
	return config.TrafficConfig{
		ExperimentID: "exp_test", EventID: "evt-1", Seed: seed, DurationSeconds: 10,
		Pattern: config.Pattern{Type: "constant", Rate: 10},
		Mix:     config.Mix{Browse: 40, Availability: 20, Reserve: 25, Purchase: 15},
		Behaviour: config.Behaviour{
			MaxQuantity: 4, ThinkTimeMinMs: 200, ThinkTimeMaxMs: 1500,
			AbandonCheckoutProbability: 0.1, DuplicateSubmitProbability: 0.05,
			RetryPaymentAfterDeclineProbability: 0.5, SyntheticCPUMs: 50, SyntheticCPUProbability: 0.2,
		},
	}
}

func TestPlansAreReproducible(t *testing.T) {
	a, b, c := NewPlanner(baseConfig(42)), NewPlanner(baseConfig(42)), NewPlanner(baseConfig(43))
	differ := 0
	for n := range uint64(500) {
		pa, pb, pc := a.Plan(n), b.Plan(n), c.Plan(n)
		if !reflect.DeepEqual(pa, pb) {
			t.Fatalf("arrival %d: same seed gave different plans", n)
		}
		if pa.IdempotencyKey("res") != pb.IdempotencyKey("res") {
			t.Fatal("idempotency keys must be deterministic")
		}
		if pa.Kind != pc.Kind || pa.Quantity != pc.Quantity || pa.ThinkTimes != pc.ThinkTimes {
			differ++
		}
	}
	if differ < 250 {
		t.Fatalf("different seeds should change most plans, only %d differ", differ)
	}
	p := a.Plan(7)
	if p.CustomerRef != "cust-42-7" {
		t.Fatalf("customer ref = %q", p.CustomerRef)
	}
	if p.IdempotencyKey("pay0") == p.IdempotencyKey("pay1") || !strings.HasSuffix(p.IdempotencyKey("res"), "-a7-res") {
		t.Fatalf("keys: %q %q", p.IdempotencyKey("pay0"), p.IdempotencyKey("res"))
	}
	other := baseConfig(42)
	other.ExperimentID = "exp_other"
	if NewPlanner(other).Plan(7).IdempotencyKey("res") == p.IdempotencyKey("res") {
		t.Fatal("keys must be namespaced by experiment")
	}
}

func TestBehaviourMixMatchesPercentages(t *testing.T) {
	cfg := baseConfig(9)
	pl := NewPlanner(cfg)
	const n = 40000
	kinds := map[Kind]int{}
	qty := map[int]int{}
	var abandon, dup, cpu int
	var think time.Duration
	for i := range uint64(n) {
		p := pl.Plan(i)
		kinds[p.Kind]++
		qty[p.Quantity]++
		if p.AbandonCheckout {
			abandon++
		}
		if p.DuplicateReservation {
			dup++
		}
		if p.SyntheticCPUMs > 0 {
			cpu++
		}
		for _, d := range p.ThinkTimes {
			if d < 200*time.Millisecond || d > 1500*time.Millisecond {
				t.Fatalf("think time %s out of range", d)
			}
			think += d
		}
	}
	frac := func(c int) float64 { return float64(c) / n }
	for kind, pct := range map[Kind]int{KindBrowse: 40, KindAvailability: 20, KindReserve: 25, KindPurchase: 15} {
		if math.Abs(frac(kinds[kind])-float64(pct)/100) > 0.01 {
			t.Errorf("%s = %.3f, want %.2f", kind, frac(kinds[kind]), float64(pct)/100)
		}
	}
	if math.Abs(frac(abandon)-0.1) > 0.01 || math.Abs(frac(dup)-0.05) > 0.01 || math.Abs(frac(cpu)-0.2) > 0.01 {
		t.Errorf("abandon=%.3f dup=%.3f cpu=%.3f", frac(abandon), frac(dup), frac(cpu))
	}
	if mean := think / (2 * n); mean < 800*time.Millisecond || mean > 900*time.Millisecond {
		t.Errorf("mean think time = %s, want ~850ms", mean)
	}
	for q := range qty {
		if q < 1 || q > 4 {
			t.Fatalf("quantity %d out of range", q)
		}
	}
	// Skewed to small quantities: 8/15, 4/15, 2/15, 1/15.
	if !(qty[1] > qty[2] && qty[2] > qty[3] && qty[3] > qty[4]) || math.Abs(frac(qty[1])-8.0/15) > 0.01 {
		t.Errorf("quantity distribution = %v", qty)
	}
}

type counter struct {
	mu  sync.Mutex
	biz map[metrics.Biz]int64
	cpu int
}

func (c *counter) Count(b metrics.Biz, n int64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.biz == nil {
		c.biz = map[metrics.Biz]int64{}
	}
	c.biz[b] += n
}

func (c *counter) RecordSyntheticCPU(ms int) { c.mu.Lock(); c.cpu += ms; c.mu.Unlock() }

type recorder struct {
	mu  sync.Mutex
	obs []metrics.Observation
}

func (r *recorder) RecordRequest(o metrics.Observation) {
	r.mu.Lock()
	r.obs = append(r.obs, o)
	r.mu.Unlock()
}

func newExecutor(t *testing.T, srv *fakeapi.Server, timeout time.Duration) (*Executor, *counter, *recorder) {
	t.Helper()
	rec := &recorder{}
	c, err := ticketapi.NewClient(srv.URL, ticketapi.NewHTTPClient(10, timeout), timeout, rec)
	if err != nil {
		t.Fatal(err)
	}
	ev, err := c.FetchEvent(context.Background(), "evt-1")
	if err != nil {
		t.Fatal(err)
	}
	cnt := &counter{}
	return &Executor{Client: c, Counter: cnt, Event: ev}, cnt, rec
}

func purchasePlan(cfg config.TrafficConfig) Plan {
	p := NewPlanner(cfg).Plan(1)
	p.Kind = KindPurchase
	p.ThinkTimes = [2]time.Duration{}
	p.Quantity = 2
	p.AbandonCheckout = false
	p.SyntheticCPUMs = 0
	p.DuplicateReservation, p.DuplicateOrder, p.DuplicatePayment = false, false, false
	return p
}

func TestPurchaseWithDuplicateSubmissionsCountsOnce(t *testing.T) {
	srv := fakeapi.New(fakeapi.Options{})
	defer srv.Close()
	x, cnt, rec := newExecutor(t, srv, 2*time.Second)
	p := purchasePlan(baseConfig(1))
	p.DuplicateReservation, p.DuplicateOrder, p.DuplicatePayment = true, true, true
	if out := x.Run(context.Background(), p); out != OutcomePurchased {
		t.Fatalf("outcome = %s", out)
	}
	st := srv.Stats()
	if st.ReservationsCreated != 1 || st.OrdersCreated != 1 || st.PaidOrders != 1 || st.TicketsIssued != 2 || st.Replays != 3 {
		t.Fatalf("server stats = %+v", st)
	}
	if cnt.biz[metrics.BizDuplicateSubmission] != 3 || cnt.biz[metrics.BizPurchaseCompleted] != 1 || cnt.biz[metrics.BizTicketsPurchased] != 2 {
		t.Fatalf("counters = %v", cnt.biz)
	}
	replays := 0
	instances := map[string]bool{}
	for _, o := range rec.obs {
		if o.Replayed {
			replays++
		}
		instances[o.Instance] = true
	}
	if replays != 3 || !instances["app-a"] || !instances["app-b"] {
		t.Fatalf("replays=%d instances=%v", replays, instances)
	}
}

func TestPaymentRetryAfterDeclineUsesNewKey(t *testing.T) {
	srv := fakeapi.New(fakeapi.Options{Decline: func(_ int64, attempt int) bool { return attempt == 1 }})
	defer srv.Close()
	x, cnt, _ := newExecutor(t, srv, 2*time.Second)
	p := purchasePlan(baseConfig(1))
	p.RetryAfterDecline = [MaxPaymentRetries]bool{true, false}
	if out := x.Run(context.Background(), p); out != OutcomePurchased {
		t.Fatalf("outcome = %s", out)
	}
	for _, keys := range srv.Stats().PaymentKeysPerOrder {
		if len(keys) != 2 || keys[0] == keys[1] {
			t.Fatalf("payment keys = %v, want two distinct keys", keys)
		}
	}
	if cnt.biz[metrics.BizPaymentDeclined] != 1 || cnt.biz[metrics.BizPaymentAttempted] != 2 || cnt.biz[metrics.BizPurchaseCompleted] != 1 {
		t.Fatalf("counters = %v", cnt.biz)
	}

	// Without a retry decision the journey ends declined.
	p2 := purchasePlan(baseConfig(2))
	p2.RetryAfterDecline = [MaxPaymentRetries]bool{}
	if out := x.Run(context.Background(), p2); out != OutcomeDeclined {
		t.Fatalf("outcome = %s", out)
	}
}

func TestTimeoutRetriesOnceWithSameKey(t *testing.T) {
	srv := fakeapi.New(fakeapi.Options{LoseFirstResponse: map[string]time.Duration{"payment": 2 * time.Second}})
	defer srv.Close()
	x, cnt, rec := newExecutor(t, srv, 300*time.Millisecond)
	if out := x.Run(context.Background(), purchasePlan(baseConfig(1))); out != OutcomePurchased {
		t.Fatalf("outcome = %s", out)
	}
	st := srv.Stats()
	if st.PaidOrders != 1 || st.TicketsIssued != 2 {
		t.Fatalf("server stats = %+v", st)
	}
	for _, keys := range st.PaymentKeysPerOrder {
		if len(keys) != 2 || keys[0] != keys[1] {
			t.Fatalf("payment keys = %v, want the same key twice", keys)
		}
	}
	var timeouts, replays int
	for _, o := range rec.obs {
		if o.Class == metrics.ClassTimeout {
			timeouts++
		}
		if o.Replayed {
			replays++
		}
	}
	if timeouts != 1 || replays != 1 || cnt.biz[metrics.BizPaymentAttempted] != 1 {
		t.Fatalf("timeouts=%d replays=%d counters=%v", timeouts, replays, cnt.biz)
	}
}

func TestSoldOutAndCategoryChoice(t *testing.T) {
	srv := fakeapi.New(fakeapi.Options{Categories: map[string]int64{"cat-a": 0, "cat-b": 3}})
	defer srv.Close()
	x, cnt, _ := newExecutor(t, srv, 2*time.Second)
	p := purchasePlan(baseConfig(1))
	p.Kind = KindReserve
	p.Quantity = 4
	// Availability says only cat-b has stock, so the customer picks it and
	// lowers the quantity to what is left.
	if out := x.Run(context.Background(), p); out != OutcomeReservedAbandoned {
		t.Fatalf("outcome = %s", out)
	}
	if got := srv.Stats().ReservedPerCategory; got["cat-b"] != 3 || got["cat-a"] != 0 {
		t.Fatalf("reserved = %v", got)
	}
	// Everything is now held: the next customer is rejected with 409.
	next := purchasePlan(baseConfig(2))
	next.Kind = KindReserve
	if out := x.Run(context.Background(), next); out != OutcomeSoldOut {
		t.Fatalf("outcome = %s", out)
	}
	if cnt.biz[metrics.BizReservationRejected] != 1 || cnt.biz[metrics.BizAbandoned] != 1 {
		t.Fatalf("counters = %v", cnt.biz)
	}
}

func TestBrowseAndSyntheticCPU(t *testing.T) {
	srv := fakeapi.New(fakeapi.Options{})
	defer srv.Close()
	x, cnt, rec := newExecutor(t, srv, 2*time.Second)
	p := purchasePlan(baseConfig(1))
	p.Kind = KindBrowse
	p.SyntheticCPUMs = 25
	if out := x.Run(context.Background(), p); out != OutcomeBrowsed {
		t.Fatalf("outcome = %s", out)
	}
	if len(rec.obs) != 3 || rec.obs[0].Endpoint != metrics.EndpointCPUWork || rec.obs[1].Endpoint != metrics.EndpointBrowse || cnt.cpu != 25 {
		t.Fatalf("observations = %+v cpu=%d", rec.obs, cnt.cpu)
	}
}

func TestJourneyAbortsOnCancel(t *testing.T) {
	srv := fakeapi.New(fakeapi.Options{Delay: 5 * time.Second})
	defer srv.Close()
	rec := &recorder{}
	c, _ := ticketapi.NewClient(srv.URL, ticketapi.NewHTTPClient(10, 10*time.Second), 10*time.Second, rec)
	x := &Executor{Client: c, Counter: &counter{}, Event: ticketapi.Event{ID: "evt-1", Categories: []ticketapi.Category{{ID: "cat-a"}}}}
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	start := time.Now()
	if out := x.Run(ctx, purchasePlan(baseConfig(1))); out != OutcomeAborted {
		t.Fatalf("outcome = %s", out)
	}
	if el := time.Since(start); el > time.Second {
		t.Fatalf("abort took %s", el)
	}
	if len(rec.obs) != 0 {
		t.Fatalf("aborted requests must not be recorded: %+v", rec.obs)
	}
}
