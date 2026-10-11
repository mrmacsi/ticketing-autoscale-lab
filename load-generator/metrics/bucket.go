package metrics

import (
	"math"
	"time"
)

// Endpoint groups requests in byEndpoint.
type Endpoint string

// Endpoints from the contract.
const (
	EndpointBrowse       Endpoint = "browse"
	EndpointAvailability Endpoint = "availability"
	EndpointReservation  Endpoint = "reservation"
	EndpointOrder        Endpoint = "order"
	EndpointPayment      Endpoint = "payment"
	EndpointCPUWork      Endpoint = "cpu_work"
)

// AllEndpoints lists every endpoint key; buckets always contain all of them.
var AllEndpoints = []Endpoint{EndpointBrowse, EndpointAvailability, EndpointReservation, EndpointOrder, EndpointPayment, EndpointCPUWork}

// Class is the outcome class of one HTTP request.
type Class int

// Request classes.
const (
	ClassOK2xx Class = iota
	ClassConflict409
	ClassGone410
	ClassPayment402
	ClassClient4xx // any other non-2xx/5xx status (unexpected 4xx, 1xx/3xx)
	ClassServer5xx
	ClassNetworkError
	ClassTimeout
)

// ClassifyStatus maps an HTTP status code to its class.
func ClassifyStatus(code int) Class {
	switch {
	case code >= 200 && code < 300:
		return ClassOK2xx
	case code == 409:
		return ClassConflict409
	case code == 410:
		return ClassGone410
	case code == 402:
		return ClassPayment402
	case code >= 500:
		return ClassServer5xx
	default:
		return ClassClient4xx
	}
}

// IsError reports whether the class is an application error. 409/410/402 are
// expected business outcomes, not errors.
func (c Class) IsError() bool {
	switch c {
	case ClassClient4xx, ClassServer5xx, ClassNetworkError, ClassTimeout:
		return true
	}
	return false
}

// Bucket is one experiments/{id}/traffic/{second} document.
type Bucket struct {
	Second            int                        `json:"second"`
	TS                time.Time                  `json:"ts"`
	TargetRate        float64                    `json:"targetRate"`
	ScheduledArrivals int64                      `json:"scheduledArrivals"`
	StartedArrivals   int64                      `json:"startedArrivals"`
	DroppedArrivals   int64                      `json:"droppedArrivals"`
	CompletedJourneys int64                      `json:"completedJourneys"`
	ActiveVUs         int64                      `json:"activeVUs"`
	MaxActiveVUs      int64                      `json:"maxActiveVUs"`
	SchedulerLagMsMax float64                    `json:"schedulerLagMsMax"`
	Requests          RequestCounts              `json:"requests"`
	ByEndpoint        map[Endpoint]EndpointStats `json:"byEndpoint"`
	Latency           LatencyStats               `json:"latency"`
	Instances         map[string]InstanceStats   `json:"instances"`
	Business          BusinessCounters           `json:"business"`
	Synthetic         SyntheticStats             `json:"synthetic"`
	// Queue holds waiting-room activity. Queue requests are counted only
	// here, never in Requests, Latency, ByEndpoint or Instances.
	Queue QueueStats `json:"queue"`
}

// QueueStats is the per-second waiting-room object (docs/waiting-room.md §3.6).
type QueueStats struct {
	Joined   int64 `json:"joined"`
	Admitted int64 `json:"admitted"`
	SoldOut  int64 `json:"soldOut"`
	Open     int64 `json:"open"`
	Failed   int64 `json:"failed"`
	// Waiting is a gauge: customers waiting in the queue when the bucket closed.
	Waiting int64 `json:"waiting"`
	// WaitMs is the join -> admitted time of customers admitted in this
	// second, in the same format as Latency.
	WaitMs   LatencyStats       `json:"waitMs"`
	Requests QueueRequestCounts `json:"requests"`
}

// QueueRequestCounts counts HTTP requests to the waiting room. ok = 2xx;
// errors = every other response, network error or timeout.
type QueueRequestCounts struct {
	Total  int64 `json:"total"`
	OK     int64 `json:"ok"`
	Errors int64 `json:"errors"`
}

// Merge adds o into r.
func (r *QueueRequestCounts) Merge(o QueueRequestCounts) {
	r.Total += o.Total
	r.OK += o.OK
	r.Errors += o.Errors
}

// QueueEvent identifies a waiting-room counter.
type QueueEvent int

// Waiting-room counters. Admissions are recorded with RecordQueueAdmitted.
const (
	QueueJoined QueueEvent = iota
	QueueSoldOut
	QueueOpen
	QueueFailed
)

// RequestCounts counts requests by outcome class.
type RequestCounts struct {
	Total         int64 `json:"total"`
	OK2xx         int64 `json:"ok2xx"`
	Conflict409   int64 `json:"conflict409"`
	Gone410       int64 `json:"gone410"`
	Payment402    int64 `json:"payment402"`
	Client4xx     int64 `json:"client4xx"`
	Server5xx     int64 `json:"server5xx"`
	NetworkErrors int64 `json:"networkErrors"`
	Timeouts      int64 `json:"timeouts"`
}

func (r *RequestCounts) add(c Class) {
	r.Total++
	switch c {
	case ClassOK2xx:
		r.OK2xx++
	case ClassConflict409:
		r.Conflict409++
	case ClassGone410:
		r.Gone410++
	case ClassPayment402:
		r.Payment402++
	case ClassClient4xx:
		r.Client4xx++
	case ClassServer5xx:
		r.Server5xx++
	case ClassNetworkError:
		r.NetworkErrors++
	case ClassTimeout:
		r.Timeouts++
	}
}

// Merge adds o into r.
func (r *RequestCounts) Merge(o RequestCounts) {
	r.Total += o.Total
	r.OK2xx += o.OK2xx
	r.Conflict409 += o.Conflict409
	r.Gone410 += o.Gone410
	r.Payment402 += o.Payment402
	r.Client4xx += o.Client4xx
	r.Server5xx += o.Server5xx
	r.NetworkErrors += o.NetworkErrors
	r.Timeouts += o.Timeouts
}

// Errors is the application error count (5xx + network + timeouts + unexpected 4xx).
func (r RequestCounts) Errors() int64 {
	return r.Server5xx + r.NetworkErrors + r.Timeouts + r.Client4xx
}

// EndpointStats counts requests per endpoint.
type EndpointStats struct {
	Count  int64 `json:"count"`
	Errors int64 `json:"errors"`
}

// LatencyStats summarises the latency of requests that received an HTTP response.
type LatencyStats struct {
	Count int64            `json:"count"`
	SumMs float64          `json:"sumMs"`
	MaxMs float64          `json:"maxMs"`
	P50   *float64         `json:"p50"`
	P95   *float64         `json:"p95"`
	P99   *float64         `json:"p99"`
	Hist  map[string]int64 `json:"hist"`
}

// InstanceStats are keyed by the X-Served-By response header.
type InstanceStats struct {
	Requests     int64   `json:"requests"`
	Errors       int64   `json:"errors"`
	LatencySumMs float64 `json:"latencySumMs"`
}

// BusinessCounters track journey outcomes derived from API responses.
type BusinessCounters struct {
	ReservationsAttempted int64 `json:"reservationsAttempted"`
	ReservationsSucceeded int64 `json:"reservationsSucceeded"`
	ReservationsRejected  int64 `json:"reservationsRejected"`
	OrdersCreated         int64 `json:"ordersCreated"`
	PaymentsAttempted     int64 `json:"paymentsAttempted"`
	PurchasesCompleted    int64 `json:"purchasesCompleted"`
	PaymentsDeclined      int64 `json:"paymentsDeclined"`
	PurchasesFailed       int64 `json:"purchasesFailed"`
	ExpiredDuringCheckout int64 `json:"expiredDuringCheckout"`
	Abandoned             int64 `json:"abandoned"`
	DuplicateSubmissions  int64 `json:"duplicateSubmissions"`
	IdempotentReplays     int64 `json:"idempotentReplays"`
	TicketsPurchased      int64 `json:"ticketsPurchased"`
}

// Merge adds o into b.
func (b *BusinessCounters) Merge(o BusinessCounters) {
	b.ReservationsAttempted += o.ReservationsAttempted
	b.ReservationsSucceeded += o.ReservationsSucceeded
	b.ReservationsRejected += o.ReservationsRejected
	b.OrdersCreated += o.OrdersCreated
	b.PaymentsAttempted += o.PaymentsAttempted
	b.PurchasesCompleted += o.PurchasesCompleted
	b.PaymentsDeclined += o.PaymentsDeclined
	b.PurchasesFailed += o.PurchasesFailed
	b.ExpiredDuringCheckout += o.ExpiredDuringCheckout
	b.Abandoned += o.Abandoned
	b.DuplicateSubmissions += o.DuplicateSubmissions
	b.IdempotentReplays += o.IdempotentReplays
	b.TicketsPurchased += o.TicketsPurchased
}

// SyntheticStats count demo-only CPU work requests.
type SyntheticStats struct {
	Requests       int64 `json:"requests"`
	CPUMsRequested int64 `json:"cpuMsRequested"`
}

// fillLatency sets the histogram-derived fields of l from h.
func fillLatency(l *LatencyStats, h *Histogram) {
	l.Count = h.Count()
	l.SumMs = round3(l.SumMs)
	l.MaxMs = round3(l.MaxMs)
	l.P50, l.P95, l.P99 = percentiles(h)
	l.Hist = h.Sparse()
}

// percentiles fills p50/p95/p99 from h (nil when empty), rounded to 0.1 ms.
func percentiles(h *Histogram) (p50, p95, p99 *float64) {
	get := func(q float64) *float64 {
		v, ok := h.Percentile(q)
		if !ok {
			return nil
		}
		v = round1(v)
		return &v
	}
	return get(0.50), get(0.95), get(0.99)
}

func round1(v float64) float64 { return math.Round(v*10) / 10 }

func round3(v float64) float64 { return math.Round(v*1000) / 1000 }
