package scenarios

import (
	"context"
	"time"

	"github.com/mrmacsi/ticketing-autoscale-lab/load-generator/metrics"
	"github.com/mrmacsi/ticketing-autoscale-lab/load-generator/queueapi"
	"github.com/mrmacsi/ticketing-autoscale-lab/load-generator/ticketapi"
)

// QueueMetrics receives waiting-room observations (implemented by
// metrics.Aggregator). Queue HTTP requests are reported by the queueapi
// client itself through queueapi.Recorder.
type QueueMetrics interface {
	CountQueue(metrics.QueueEvent)
	RecordQueueAdmitted(waitMs float64)
	AddQueueWaiting(delta int64)
}

// minPollInterval guards against a status endpoint that answers "waiting"
// immediately instead of long-polling.
const minPollInterval = 200 * time.Millisecond

// maxPollAfter caps the server's pollAfterMs hint.
const maxPollAfter = 5 * time.Second

// queuedCheckout is a reserve or purchase journey behind the waiting room:
// join -> long-poll while waiting -> sold_out ends the journey; admitted
// runs checkout with the admission token; open runs checkout without one.
// The queue slot is always released afterwards with the checkout outcome,
// unless the journey was aborted (hard stop or run deadline).
func (x *Executor) queuedCheckout(ctx context.Context, p Plan) Outcome {
	st, out, proceed := x.waitInQueue(ctx, p)
	if !proceed {
		return out
	}
	token := ""
	if st.Status == queueapi.StatusAdmitted {
		token = st.Token
	}
	out = OutcomeAborted
	if x.syntheticCPU(ctx, p) {
		out = x.checkout(ctx, p, token)
	}
	if out != OutcomeAborted {
		x.release(ctx, st.QueueID, releaseOutcome(out))
	}
	return out
}

// waitInQueue joins the queue and waits for a decision. proceed is true when
// the customer may start checkout (admitted or open); otherwise out is the
// journey outcome. Transient queue failures (network, timeout, 5xx, 429) are
// retried with queueapi.Backoff until ctx is done; a 404 unknown_queue_id
// (the queue lost the entry) re-joins.
func (x *Executor) waitInQueue(ctx context.Context, p Plan) (st queueapi.Status, out Outcome, proceed bool) {
	m := x.QueueMetrics
	backoff := x.QueueBackoff
	if backoff == nil {
		backoff = queueapi.Backoff
	}
	waitSeconds := x.QueueWaitSeconds
	if waitSeconds <= 0 {
		waitSeconds = queueapi.DefaultWaitSeconds
	}

	waiting := false
	setWaiting := func(w bool) {
		if w == waiting {
			return
		}
		waiting = w
		if w {
			m.AddQueueWaiting(1)
		} else {
			m.AddQueueWaiting(-1)
		}
	}
	defer setWaiting(false)

	var (
		joined   bool
		joinedAt time.Time
		needJoin = true
		failures int
	)
	for {
		var (
			next queueapi.Status
			err  error
		)
		sent := time.Now()
		if needJoin {
			next, err = x.Queue.Join(ctx, x.Event.ID, p.CustomerRef)
		} else {
			next, err = x.Queue.Status(ctx, st.QueueID, waitSeconds)
		}
		if err != nil {
			if ctx.Err() != nil {
				return st, OutcomeAborted, false
			}
			switch {
			case !needJoin && queueapi.IsUnknownQueueID(err):
				needJoin = true
			case !queueapi.IsRetryable(err):
				m.CountQueue(metrics.QueueFailed)
				return st, OutcomeQueueFailed, false
			}
			failures++
			if !sleep(ctx, backoff(failures)) {
				return st, OutcomeAborted, false
			}
			continue
		}
		failures = 0
		if next.QueueID == "" && !needJoin {
			next.QueueID = st.QueueID
		}
		if needJoin {
			needJoin = false
			if !joined {
				joined, joinedAt = true, sent
				m.CountQueue(metrics.QueueJoined)
			}
		}
		st = next
		switch st.Status {
		case queueapi.StatusWaiting:
			if !ticketapi.ValidID(st.QueueID) {
				m.CountQueue(metrics.QueueFailed)
				return st, OutcomeQueueFailed, false
			}
			setWaiting(true)
			if el := time.Since(sent); el < minPollInterval {
				pause := max(minPollInterval-el, min(time.Duration(st.PollAfterMs)*time.Millisecond, maxPollAfter))
				if !sleep(ctx, pause) {
					return st, OutcomeAborted, false
				}
			}
		case queueapi.StatusAdmitted:
			setWaiting(false)
			m.RecordQueueAdmitted(float64(time.Since(joinedAt).Microseconds()) / 1000)
			return st, "", true
		case queueapi.StatusOpen:
			setWaiting(false)
			m.CountQueue(metrics.QueueOpen)
			return st, "", true
		case queueapi.StatusSoldOut:
			setWaiting(false)
			m.CountQueue(metrics.QueueSoldOut)
			x.release(ctx, st.QueueID, queueapi.ReleaseSoldOut)
			return st, OutcomeSoldOutInQueue, false
		default:
			setWaiting(false)
			m.CountQueue(metrics.QueueFailed)
			return st, OutcomeQueueFailed, false
		}
	}
}

// release frees the customer's queue slot (single attempt, short timeout).
// Errors are ignored here; the queue client counts them in queue.requests.
func (x *Executor) release(ctx context.Context, queueID, outcome string) {
	if !ticketapi.ValidID(queueID) || ctx.Err() != nil {
		return
	}
	_ = x.Queue.Release(ctx, queueID, outcome)
}

// releaseOutcome maps a checkout outcome to a queue release outcome.
func releaseOutcome(o Outcome) string {
	switch o {
	case OutcomePurchased:
		return queueapi.ReleasePurchased
	case OutcomeSoldOut:
		return queueapi.ReleaseSoldOut
	case OutcomeFailed:
		return queueapi.ReleaseFailed
	default:
		// reserved_abandoned, abandoned, declined, expired: the customer
		// left without buying.
		return queueapi.ReleaseAbandoned
	}
}
