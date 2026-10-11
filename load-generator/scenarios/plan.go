package scenarios

import (
	"fmt"
	"hash/fnv"
	"time"

	"github.com/mrmacsi/ticketing-autoscale-lab/load-generator/config"
	"github.com/mrmacsi/ticketing-autoscale-lab/load-generator/internal/seedrand"
)

// Kind is a customer journey type.
type Kind string

// Journey kinds (keys of TrafficConfig.mix).
const (
	KindBrowse       Kind = "browse"
	KindAvailability Kind = "availability"
	KindReserve      Kind = "reserve"
	KindPurchase     Kind = "purchase"
)

// MaxPaymentRetries bounds how many times one journey retries a declined payment.
const MaxPaymentRetries = 2

// Plan holds every random decision for one arrival. It is drawn up front from
// an RNG seeded by (seed, arrival index), so the decisions are reproducible
// and independent of response timing or goroutine interleaving. Choices that
// depend on server state (which category has stock) use the pre-drawn
// CategoryPick value.
type Plan struct {
	Arrival      uint64
	Kind         Kind
	CustomerRef  string
	Quantity     int
	CategoryPick float64
	// ThinkTimes[0] precedes the 2nd request of a journey, [1] the 3rd, etc.
	ThinkTimes           [2]time.Duration
	AbandonCheckout      bool
	DuplicateReservation bool
	DuplicateOrder       bool
	DuplicatePayment     bool
	// RetryAfterDecline[i] decides whether decline number i is retried with a new key.
	RetryAfterDecline [MaxPaymentRetries]bool
	// SyntheticCPUMs > 0 adds GET /api/lab/cpu-work?ms=N at the start of the journey.
	SyntheticCPUMs int

	keyPrefix string
}

// IdempotencyKey derives the key for a journey step. The same step always
// gets the same key; a new payment attempt after a decline is a new step.
func (p Plan) IdempotencyKey(step string) string {
	return fmt.Sprintf("%s-a%d-%s", p.keyPrefix, p.Arrival, step)
}

// Planner draws Plans for a TrafficConfig.
type Planner struct {
	cfg       config.TrafficConfig
	keyPrefix string
}

// NewPlanner creates a planner. Idempotency keys are namespaced by the
// experiment id (hashed) and seed so a rerun with the same seed in a new
// experiment never collides with stored keys of an earlier run.
func NewPlanner(cfg config.TrafficConfig) *Planner {
	h := fnv.New32a()
	_, _ = h.Write([]byte(cfg.ExperimentID))
	return &Planner{cfg: cfg, keyPrefix: fmt.Sprintf("lg-%08x-s%d", h.Sum32(), cfg.Seed)}
}

// Plan returns the decisions for arrival n (0-based).
func (pl *Planner) Plan(n uint64) Plan {
	rng := seedrand.New(pl.cfg.Seed, seedrand.SaltArrival, n)
	b := pl.cfg.Behaviour
	p := Plan{
		Arrival:     n,
		CustomerRef: fmt.Sprintf("cust-%d-%d", pl.cfg.Seed, n),
		keyPrefix:   pl.keyPrefix,
	}
	// The draw order is fixed and every value is drawn for every journey kind,
	// so adding a decision for one kind never shifts another kind's values.
	p.Kind = pickKind(pl.cfg.Mix, rng.IntN(100))
	p.Quantity = skewedQuantity(rng.Float64(), b.MaxQuantity)
	p.CategoryPick = rng.Float64()
	for i := range p.ThinkTimes {
		p.ThinkTimes[i] = thinkTime(rng.Float64(), b.ThinkTimeMinMs, b.ThinkTimeMaxMs)
	}
	p.AbandonCheckout = rng.Float64() < b.AbandonCheckoutProbability
	p.DuplicateReservation = rng.Float64() < b.DuplicateSubmitProbability
	p.DuplicateOrder = rng.Float64() < b.DuplicateSubmitProbability
	p.DuplicatePayment = rng.Float64() < b.DuplicateSubmitProbability
	for i := range p.RetryAfterDecline {
		p.RetryAfterDecline[i] = rng.Float64() < b.RetryPaymentAfterDeclineProbability
	}
	if cpu := rng.Float64(); b.SyntheticCPUMs > 0 && cpu < b.SyntheticCPUProbability {
		p.SyntheticCPUMs = b.SyntheticCPUMs
	}
	return p
}

func pickKind(m config.Mix, u int) Kind {
	switch {
	case u < m.Browse:
		return KindBrowse
	case u < m.Browse+m.Availability:
		return KindAvailability
	case u < m.Browse+m.Availability+m.Reserve:
		return KindReserve
	default:
		return KindPurchase
	}
}

// skewedQuantity draws 1..maxQ from a truncated geometric distribution with
// ratio 1/2 (P(1)=~1/2, P(2)=~1/4, ...): most customers buy one or two tickets.
func skewedQuantity(u float64, maxQ int) int {
	maxQ = min(max(maxQ, 1), config.MaxReservationQuantity)
	total := 0.0
	for k := range maxQ {
		total += 1 / float64(int(1)<<k)
	}
	acc := 0.0
	for k := range maxQ {
		acc += 1 / float64(int(1)<<k) / total
		if u < acc {
			return k + 1
		}
	}
	return maxQ
}

func thinkTime(u float64, minMs, maxMs int) time.Duration {
	if maxMs < minMs {
		maxMs = minMs
	}
	ms := float64(minMs) + u*float64(maxMs-minMs)
	return time.Duration(ms * float64(time.Millisecond))
}
