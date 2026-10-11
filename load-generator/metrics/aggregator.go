package metrics

import (
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Observation is the outcome of one real HTTP request.
type Observation struct {
	Endpoint Endpoint
	Class    Class
	// LatencyMs is only meaningful when HasResponse is true.
	LatencyMs   float64
	HasResponse bool
	// Instance is the X-Served-By header ("" when absent or no response).
	Instance string
	// Replayed is true when the response carried Idempotent-Replayed: true.
	Replayed bool
}

// Biz identifies a business counter.
type Biz int

// Business counters.
const (
	BizReservationAttempted Biz = iota
	BizReservationSucceeded
	BizReservationRejected
	BizOrderCreated
	BizPaymentAttempted
	BizPurchaseCompleted
	BizPaymentDeclined
	BizPurchaseFailed
	BizExpiredDuringCheckout
	BizAbandoned
	BizDuplicateSubmission
	BizTicketsPurchased
)

// Aggregator accumulates observations into per-second buckets relative to
// the run start. Requests (and every other event) are attributed to the second
// in which they are recorded, i.e. when they completed. The second is computed
// under the lock, so once Flush has emitted second s no later record can land
// in it.
type Aggregator struct {
	mu         sync.Mutex
	start      time.Time
	now        func() time.Time
	targetRate func(second int) float64
	activeVUs  func() int64
	open       map[int]*acc
	next       int
	closed     bool
	// queueWaiting is the gauge of customers currently waiting in the queue.
	queueWaiting atomic.Int64
}

type acc struct {
	b         Bucket
	hist      Histogram
	queueHist Histogram
}

// AggregatorOptions configure NewAggregator.
type AggregatorOptions struct {
	// Start is the run start (second 0 begins here).
	Start time.Time
	// TargetRate returns the requested arrivals for a second.
	TargetRate func(second int) float64
	// ActiveVUs returns the number of journeys currently in flight.
	ActiveVUs func() int64
	// Now overrides the clock (tests).
	Now func() time.Time
}

// NewAggregator creates an aggregator.
func NewAggregator(o AggregatorOptions) *Aggregator {
	a := &Aggregator{start: o.Start, now: o.Now, targetRate: o.TargetRate, activeVUs: o.ActiveVUs, open: map[int]*acc{}}
	if a.now == nil {
		a.now = time.Now
	}
	if a.targetRate == nil {
		a.targetRate = func(int) float64 { return 0 }
	}
	if a.activeVUs == nil {
		a.activeVUs = func() int64 { return 0 }
	}
	return a
}

func (a *Aggregator) secondAt(t time.Time) int {
	d := t.Sub(a.start)
	if d < 0 {
		return 0
	}
	return int(d / time.Second)
}

// current returns the open bucket for "now". Caller holds the lock.
func (a *Aggregator) current() *acc {
	if a.closed {
		return nil
	}
	s := max(a.secondAt(a.now()), a.next)
	ac := a.open[s]
	if ac == nil {
		ac = &acc{}
		a.open[s] = ac
	}
	return ac
}

// RecordRequest records one completed HTTP request.
func (a *Aggregator) RecordRequest(o Observation) {
	a.mu.Lock()
	defer a.mu.Unlock()
	ac := a.current()
	if ac == nil {
		return
	}
	b := &ac.b
	b.Requests.add(o.Class)
	if b.ByEndpoint == nil {
		b.ByEndpoint = map[Endpoint]EndpointStats{}
	}
	es := b.ByEndpoint[o.Endpoint]
	es.Count++
	if o.Class.IsError() {
		es.Errors++
	}
	b.ByEndpoint[o.Endpoint] = es
	if o.HasResponse {
		ac.hist.Observe(o.LatencyMs)
		b.Latency.SumMs += o.LatencyMs
		b.Latency.MaxMs = max(b.Latency.MaxMs, o.LatencyMs)
		inst := SanitizeInstance(o.Instance)
		if b.Instances == nil {
			b.Instances = map[string]InstanceStats{}
		}
		is := b.Instances[inst]
		is.Requests++
		if o.Class.IsError() {
			is.Errors++
		}
		is.LatencySumMs += o.LatencyMs
		b.Instances[inst] = is
	}
	if o.Replayed {
		b.Business.IdempotentReplays++
	}
	if o.Endpoint == EndpointCPUWork {
		b.Synthetic.Requests++
	}
}

// RecordArrival records one scheduled arrival: started (got a VU) or dropped.
func (a *Aggregator) RecordArrival(started bool, lag time.Duration, active int64) {
	a.mu.Lock()
	defer a.mu.Unlock()
	ac := a.current()
	if ac == nil {
		return
	}
	b := &ac.b
	b.ScheduledArrivals++
	if started {
		b.StartedArrivals++
	} else {
		b.DroppedArrivals++
	}
	b.SchedulerLagMsMax = max(b.SchedulerLagMsMax, float64(lag)/float64(time.Millisecond))
	b.MaxActiveVUs = max(b.MaxActiveVUs, active)
}

// RecordJourneyCompleted records the end of a journey.
func (a *Aggregator) RecordJourneyCompleted() {
	a.mu.Lock()
	defer a.mu.Unlock()
	if ac := a.current(); ac != nil {
		ac.b.CompletedJourneys++
	}
}

// RecordSyntheticCPU records the CPU milliseconds requested from /api/lab/cpu-work.
func (a *Aggregator) RecordSyntheticCPU(ms int) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if ac := a.current(); ac != nil {
		ac.b.Synthetic.CPUMsRequested += int64(ms)
	}
}

// Count adds n to a business counter.
func (a *Aggregator) Count(c Biz, n int64) {
	a.mu.Lock()
	defer a.mu.Unlock()
	ac := a.current()
	if ac == nil {
		return
	}
	bc := &ac.b.Business
	switch c {
	case BizReservationAttempted:
		bc.ReservationsAttempted += n
	case BizReservationSucceeded:
		bc.ReservationsSucceeded += n
	case BizReservationRejected:
		bc.ReservationsRejected += n
	case BizOrderCreated:
		bc.OrdersCreated += n
	case BizPaymentAttempted:
		bc.PaymentsAttempted += n
	case BizPurchaseCompleted:
		bc.PurchasesCompleted += n
	case BizPaymentDeclined:
		bc.PaymentsDeclined += n
	case BizPurchaseFailed:
		bc.PurchasesFailed += n
	case BizExpiredDuringCheckout:
		bc.ExpiredDuringCheckout += n
	case BizAbandoned:
		bc.Abandoned += n
	case BizDuplicateSubmission:
		bc.DuplicateSubmissions += n
	case BizTicketsPurchased:
		bc.TicketsPurchased += n
	}
}

// RecordQueueRequest records one completed waiting-room HTTP request. It is
// kept apart from RecordRequest so queue calls never show up in the
// application request counts, latency, byEndpoint or instances.
func (a *Aggregator) RecordQueueRequest(ok bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	ac := a.current()
	if ac == nil {
		return
	}
	r := &ac.b.Queue.Requests
	r.Total++
	if ok {
		r.OK++
	} else {
		r.Errors++
	}
}

// CountQueue adds one to a waiting-room counter.
func (a *Aggregator) CountQueue(e QueueEvent) {
	a.mu.Lock()
	defer a.mu.Unlock()
	ac := a.current()
	if ac == nil {
		return
	}
	q := &ac.b.Queue
	switch e {
	case QueueJoined:
		q.Joined++
	case QueueSoldOut:
		q.SoldOut++
	case QueueOpen:
		q.Open++
	case QueueFailed:
		q.Failed++
	}
}

// RecordQueueAdmitted records one admission after waiting waitMs since join.
func (a *Aggregator) RecordQueueAdmitted(waitMs float64) {
	a.mu.Lock()
	defer a.mu.Unlock()
	ac := a.current()
	if ac == nil {
		return
	}
	q := &ac.b.Queue
	q.Admitted++
	ac.queueHist.Observe(waitMs)
	q.WaitMs.SumMs += waitMs
	q.WaitMs.MaxMs = max(q.WaitMs.MaxMs, waitMs)
}

// AddQueueWaiting moves the waiting gauge (+1 on joining the line, -1 on
// leaving it for any reason).
func (a *Aggregator) AddQueueWaiting(delta int64) { a.queueWaiting.Add(delta) }

// QueueWaiting returns the current waiting gauge.
func (a *Aggregator) QueueWaiting() int64 { return a.queueWaiting.Load() }

// Flush returns every bucket for seconds that have fully elapsed (including
// empty seconds, so consumers see a contiguous series). With final=true the
// current partial second is emitted too and the aggregator stops accepting
// records.
func (a *Aggregator) Flush(final bool) []Bucket {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closed {
		return nil
	}
	upto := a.secondAt(a.now()) // exclusive
	if final {
		upto++
		for s := range a.open {
			upto = max(upto, s+1)
		}
		a.closed = true
	}
	var out []Bucket
	active := a.activeVUs()
	waiting := a.queueWaiting.Load()
	for s := a.next; s < upto; s++ {
		ac := a.open[s]
		delete(a.open, s)
		if ac == nil {
			ac = &acc{}
		}
		b := a.finish(s, ac, active)
		b.Queue.Waiting = waiting
		out = append(out, b)
	}
	a.next = max(a.next, upto)
	return out
}

func (a *Aggregator) finish(s int, ac *acc, active int64) Bucket {
	b := ac.b
	b.Second = s
	b.TS = a.start.Add(time.Duration(s) * time.Second).UTC()
	b.TargetRate = round3(a.targetRate(s))
	b.ActiveVUs = active
	b.MaxActiveVUs = max(b.MaxActiveVUs, active)
	b.SchedulerLagMsMax = round3(b.SchedulerLagMsMax)
	byEndpoint := make(map[Endpoint]EndpointStats, len(AllEndpoints))
	for _, e := range AllEndpoints {
		byEndpoint[e] = b.ByEndpoint[e]
	}
	b.ByEndpoint = byEndpoint
	if b.Instances == nil {
		b.Instances = map[string]InstanceStats{}
	}
	for k, v := range b.Instances {
		v.LatencySumMs = round3(v.LatencySumMs)
		b.Instances[k] = v
	}
	fillLatency(&b.Latency, &ac.hist)
	fillLatency(&b.Queue.WaitMs, &ac.queueHist)
	return b
}

// SanitizeInstance makes an X-Served-By value safe to use as a map key.
func SanitizeInstance(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return "unknown"
	}
	var sb strings.Builder
	for _, r := range s {
		if sb.Len() >= 63 {
			break
		}
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			sb.WriteRune(r)
		default:
			sb.WriteRune('_')
		}
	}
	return sb.String()
}
