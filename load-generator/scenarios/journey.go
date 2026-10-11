package scenarios

import (
	"context"
	"net/http"
	"strconv"
	"time"

	"github.com/mrmacsi/ticketing-autoscale-lab/load-generator/metrics"
	"github.com/mrmacsi/ticketing-autoscale-lab/load-generator/queueapi"
	"github.com/mrmacsi/ticketing-autoscale-lab/load-generator/ticketapi"
)

// Outcome is how a journey ended.
type Outcome string

// Journey outcomes.
const (
	OutcomeBrowsed           Outcome = "browsed"
	OutcomeCheckedAvailable  Outcome = "checked_availability"
	OutcomeReservedAbandoned Outcome = "reserved_abandoned"
	OutcomeSoldOut           Outcome = "sold_out"
	OutcomeAbandoned         Outcome = "abandoned"
	OutcomePurchased         Outcome = "purchased"
	OutcomeDeclined          Outcome = "declined"
	OutcomeExpired           Outcome = "expired"
	OutcomeFailed            Outcome = "failed"
	OutcomeAborted           Outcome = "aborted"
	// OutcomeSoldOutInQueue: the waiting room declared the event sold out
	// while the customer was waiting; no application request was sent.
	OutcomeSoldOutInQueue Outcome = "sold_out_in_queue"
	// OutcomeQueueFailed: the waiting room rejected the customer with a
	// non-retryable error (e.g. 400, 404 unknown_event).
	OutcomeQueueFailed Outcome = "queue_failed"
)

// Counter receives business counters (implemented by metrics.Aggregator).
type Counter interface {
	Count(metrics.Biz, int64)
	RecordSyntheticCPU(ms int)
}

// Executor runs journeys against the ticketing API.
type Executor struct {
	Client  *ticketapi.Client
	Counter Counter
	Event   ticketapi.Event

	// Queue, when set, sends reserve and purchase journeys through the
	// waiting room first (TrafficConfig.waitingRoom.enabled).
	Queue *queueapi.Client
	// QueueMetrics receives waiting-room observations (required with Queue).
	QueueMetrics QueueMetrics
	// QueueBackoff overrides queueapi.Backoff (tests).
	QueueBackoff func(retry int) time.Duration
	// QueueWaitSeconds overrides queueapi.DefaultWaitSeconds (tests).
	QueueWaitSeconds int
}

// Run executes the journey described by plan and returns its outcome. All
// waits and requests stop as soon as ctx is cancelled.
func (x *Executor) Run(ctx context.Context, p Plan) Outcome {
	if x.Queue != nil && (p.Kind == KindReserve || p.Kind == KindPurchase) {
		// The synthetic CPU request is sent after admission so a waiting
		// customer never touches an application server.
		return x.queuedCheckout(ctx, p)
	}
	if !x.syntheticCPU(ctx, p) {
		return OutcomeAborted
	}
	switch p.Kind {
	case KindBrowse:
		return x.browse(ctx, p)
	case KindAvailability:
		if x.Client.GetAvailability(ctx, x.Event.ID).Aborted {
			return OutcomeAborted
		}
		return OutcomeCheckedAvailable
	default:
		return x.checkout(ctx, p, "")
	}
}

// syntheticCPU sends the planned demo-only CPU request; false when aborted.
func (x *Executor) syntheticCPU(ctx context.Context, p Plan) bool {
	if p.SyntheticCPUMs <= 0 {
		return true
	}
	if x.Client.CPUWork(ctx, p.SyntheticCPUMs).Aborted {
		return false
	}
	x.Counter.RecordSyntheticCPU(p.SyntheticCPUMs)
	return true
}

func (x *Executor) browse(ctx context.Context, p Plan) Outcome {
	if x.Client.ListEvents(ctx).Aborted || !sleep(ctx, p.ThinkTimes[0]) {
		return OutcomeAborted
	}
	if x.Client.GetEvent(ctx, x.Event.ID).Aborted {
		return OutcomeAborted
	}
	return OutcomeBrowsed
}

// checkout implements the reserve and purchase journeys. A non-empty
// queueToken is sent as X-Queue-Token on every POST /api/reservations
// attempt (including the unknown-outcome retry and duplicate submissions).
func (x *Executor) checkout(ctx context.Context, p Plan, queueToken string) Outcome {
	purchase := p.Kind == KindPurchase
	fail := func() Outcome {
		if purchase {
			x.Counter.Count(metrics.BizPurchaseFailed, 1)
		}
		return OutcomeFailed
	}

	av := x.Client.GetAvailability(ctx, x.Event.ID)
	if av.Aborted {
		return OutcomeAborted
	}
	var avail *ticketapi.Availability
	if av.OK() {
		if a, err := ticketapi.DecodeData[ticketapi.Availability](av); err == nil {
			avail = &a
		}
	}
	categoryID, qty := pickCategory(x.Event, avail, p.CategoryPick, p.Quantity)

	x.Counter.Count(metrics.BizReservationAttempted, 1)
	resReq := ticketapi.ReservationRequest(p.IdempotencyKey("res"), categoryID, qty, p.CustomerRef)
	resReq.QueueToken = queueToken
	rr := x.post(ctx, resReq, p.DuplicateReservation)
	switch {
	case rr.Aborted:
		return OutcomeAborted
	case rr.Status == http.StatusConflict:
		x.Counter.Count(metrics.BizReservationRejected, 1)
		return OutcomeSoldOut
	case !rr.OK():
		return fail()
	}
	res, err := ticketapi.DecodeData[ticketapi.Reservation](rr)
	if err != nil || !ticketapi.ValidID(res.ID) {
		return fail()
	}
	x.Counter.Count(metrics.BizReservationSucceeded, 1)
	if !purchase {
		x.Counter.Count(metrics.BizAbandoned, 1)
		return OutcomeReservedAbandoned
	}

	if !sleep(ctx, p.ThinkTimes[0]) {
		return OutcomeAborted
	}
	or := x.post(ctx, ticketapi.OrderRequest(p.IdempotencyKey("ord"), res.ID, p.CustomerRef), p.DuplicateOrder)
	switch {
	case or.Aborted:
		return OutcomeAborted
	case or.Status == http.StatusGone || or.Status == http.StatusConflict:
		// 410 reservation_expired / 409 reservation_not_active: the hold lapsed.
		x.Counter.Count(metrics.BizExpiredDuringCheckout, 1)
		return OutcomeExpired
	case !or.OK():
		return fail()
	}
	order, err := ticketapi.DecodeData[ticketapi.Order](or)
	if err != nil || !ticketapi.ValidID(order.ID) {
		return fail()
	}
	x.Counter.Count(metrics.BizOrderCreated, 1)
	if p.AbandonCheckout {
		x.Counter.Count(metrics.BizAbandoned, 1)
		return OutcomeAbandoned
	}

	if !sleep(ctx, p.ThinkTimes[1]) {
		return OutcomeAborted
	}
	for attempt := 0; ; attempt++ {
		x.Counter.Count(metrics.BizPaymentAttempted, 1)
		key := p.IdempotencyKey("pay" + strconv.Itoa(attempt))
		pr := x.post(ctx, ticketapi.PaymentRequest(key, order.ID), attempt == 0 && p.DuplicatePayment)
		switch {
		case pr.Aborted:
			return OutcomeAborted
		case pr.Status == http.StatusPaymentRequired:
			x.Counter.Count(metrics.BizPaymentDeclined, 1)
			if attempt < len(p.RetryAfterDecline) && p.RetryAfterDecline[attempt] {
				continue
			}
			return OutcomeDeclined
		case pr.Status == http.StatusGone:
			x.Counter.Count(metrics.BizExpiredDuringCheckout, 1)
			return OutcomeExpired
		case !pr.OK():
			return fail()
		}
		paid, err := ticketapi.DecodeData[ticketapi.Order](pr)
		if err != nil || paid.Status != "paid" {
			return fail()
		}
		tickets := int64(len(paid.Tickets))
		if tickets == 0 {
			tickets = int64(max(res.Quantity, 0))
		}
		x.Counter.Count(metrics.BizPurchaseCompleted, 1)
		x.Counter.Count(metrics.BizTicketsPurchased, tickets)
		return OutcomePurchased
	}
}

// post sends an idempotent POST. An unknown outcome (timeout, connection
// error, gateway error) is retried once with the same Idempotency-Key. A
// planned duplicate submission re-sends the identical request afterwards, as
// a double-clicking customer would; the server must replay the first result.
func (x *Executor) post(ctx context.Context, req ticketapi.Request, duplicate bool) ticketapi.Response {
	resp := x.Client.Do(ctx, req)
	if resp.Unknown() {
		resp = x.Client.Do(ctx, req)
	}
	if resp.Aborted || !duplicate {
		return resp
	}
	x.Counter.Count(metrics.BizDuplicateSubmission, 1)
	dup := x.Client.Do(ctx, req)
	if dup.Aborted {
		return dup
	}
	if resp.Unknown() && !dup.Unknown() {
		return dup
	}
	return resp
}

// pickCategory chooses among categories with known remaining stock, falling
// back to all event categories when availability is unknown or exhausted
// (customers still try; availability is only approximate).
func pickCategory(ev ticketapi.Event, avail *ticketapi.Availability, u float64, qty int) (string, int) {
	if avail != nil {
		var inStock []ticketapi.CategoryAvailability
		for _, c := range avail.Categories {
			if c.Available > 0 && ticketapi.ValidID(c.ID) {
				inStock = append(inStock, c)
			}
		}
		if len(inStock) > 0 {
			c := inStock[index(u, len(inStock))]
			return c.ID, int(min(int64(qty), c.Available))
		}
	}
	return ev.Categories[index(u, len(ev.Categories))].ID, qty
}

func index(u float64, n int) int { return min(int(u*float64(n)), n-1) }

func sleep(ctx context.Context, d time.Duration) bool {
	if d <= 0 {
		return ctx.Err() == nil
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return true
	case <-ctx.Done():
		return false
	}
}
