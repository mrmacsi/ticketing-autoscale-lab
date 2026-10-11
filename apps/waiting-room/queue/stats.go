package queue

import (
	"math"
	"math/rand/v2"
	"slices"
	"time"
)

// Stats is the GET /admin/stats payload (docs/waiting-room.md section 3.4). Fields after
// capacityError are additions that the contract does not require.
type Stats struct {
	Enabled                bool           `json:"enabled"`
	EventID                *string        `json:"eventId"`
	SoldOut                bool           `json:"soldOut"`
	Waiting                int            `json:"waiting"`
	Active                 int            `json:"active"`
	Capacity               int            `json:"capacity"`
	HealthyServers         int            `json:"healthyServers"`
	Servers                []ServerView   `json:"servers"`
	ActiveUsersPerServer   int            `json:"activeUsersPerServer"`
	JoinedTotal            int64          `json:"joinedTotal"`
	AdmittedTotal          int64          `json:"admittedTotal"`
	ReleasedTotal          int64          `json:"releasedTotal"`
	ExpiredTotal           int64          `json:"expiredTotal"`
	SoldOutTotal           int64          `json:"soldOutTotal"`
	AdmissionRatePerSecond float64        `json:"admissionRatePerSecond"`
	WaitMs                 WaitMs         `json:"waitMs"`
	OldestWaitingSeconds   float64        `json:"oldestWaitingSeconds"`
	Inventory              *InventoryView `json:"inventory"`
	CapacitySource         string         `json:"capacitySource"`
	CapacityError          *string        `json:"capacityError"`
	UpdatedAt              string         `json:"updatedAt"`

	AdmissionTTLSeconds int              `json:"admissionTtlSeconds"`
	SoldOutGraceSeconds int              `json:"soldOutGraceSeconds"`
	LeftWaitingTotal    int64            `json:"leftWaitingTotal"` // released while still waiting
	ReleasedByOutcome   map[string]int64 `json:"releasedByOutcome"`
	InventoryError      *string          `json:"inventoryError"`
	InventoryZeroSince  *string          `json:"inventoryZeroSince"` // start of the current available == 0 streak

	AdmissionMode string         `json:"admissionMode"`
	Adaptive      AdaptiveConfig `json:"adaptive"`

	AbandonedTotal        int64 `json:"abandonedTotal"` // dropped from the line for not checking in
	WaitingTimeoutSeconds int   `json:"waitingTimeoutSeconds"`

	// High availability: which store holds the line, which instance answered, and which instance
	// runs admission (the leader). The memory store is always its own leader.
	Store          string  `json:"store"`
	Instance       string  `json:"instance"`
	Leader         bool    `json:"leader"`
	LeaderInstance *string `json:"leaderInstance"`
}

// WaitMs summarizes join → admitted durations of admitted customers.
type WaitMs struct {
	P50   int64 `json:"p50"`
	P95   int64 `json:"p95"`
	Max   int64 `json:"max"`
	Count int64 `json:"count"`
}

// InventoryView is the last availability observation.
type InventoryView struct {
	Available  int    `json:"available"`
	Held       *int   `json:"held"` // null when the app does not report holds
	SoldOut    bool   `json:"soldOut"`
	ObservedAt string `json:"observedAt"`
}

// Stats returns a snapshot.
func (r *Room) Stats() Stats {
	now := r.now()
	r.mu.Lock()
	defer r.mu.Unlock()
	s := Stats{
		Enabled:                r.cfg.Enabled,
		SoldOut:                r.soldOut,
		Waiting:                len(r.line) - r.head,
		Active:                 len(r.active),
		Capacity:               r.capacityLocked(),
		HealthyServers:         r.healthy,
		Servers:                r.serverViewsLocked(),
		ActiveUsersPerServer:   r.cfg.ActiveUsersPerServer,
		JoinedTotal:            r.joinedTotal,
		AdmittedTotal:          r.admittedTotal,
		ReleasedTotal:          r.releasedTotal,
		ExpiredTotal:           r.expiredTotal,
		SoldOutTotal:           r.soldOutTotal,
		AdmissionRatePerSecond: math.Round(r.rate.perSecond(now)*10) / 10,
		WaitMs:                 r.waits.summary(),
		CapacitySource:         r.capSource,
		UpdatedAt:              FormatTime(now),
		AdmissionTTLSeconds:    r.cfg.AdmissionTTLSeconds,
		SoldOutGraceSeconds:    r.cfg.SoldOutGraceSeconds,
		LeftWaitingTotal:       r.leftWaitingTotal,
		ReleasedByOutcome:      make(map[string]int64, len(r.releasedByOutcome)),
		AdmissionMode:          r.cfg.AdmissionMode,
		AbandonedTotal:         r.abandonedTotal,
		WaitingTimeoutSeconds:  r.cfg.WaitingTimeoutSeconds,
		Adaptive:               r.cfg.Adaptive,
		Store:                  StoreMemory,
		Instance:               Hostname(),
		Leader:                 true,
	}
	host := Hostname()
	s.LeaderInstance = &host
	if r.cfg.EventID != "" {
		id := r.cfg.EventID
		s.EventID = &id
	}
	if r.head < len(r.line) {
		s.OldestWaitingSeconds = math.Round(now.Sub(r.line[r.head].joinedAt).Seconds()*10) / 10
	}
	if r.inv != nil {
		s.Inventory = &InventoryView{Available: r.inv.available, SoldOut: r.inv.appSoldOut, ObservedAt: FormatTime(r.inv.observedAt)}
		if r.inv.held >= 0 {
			h := r.inv.held
			s.Inventory.Held = &h
		}
	}
	if r.capErr != "" {
		e := r.capErr
		s.CapacityError = &e
	}
	if r.invErr != "" {
		e := r.invErr
		s.InventoryError = &e
	}
	if !r.zeroSince.IsZero() {
		z := FormatTime(r.zeroSince)
		s.InventoryZeroSince = &z
	}
	for k, v := range r.releasedByOutcome {
		s.ReleasedByOutcome[k] = v
	}
	return s
}

// rateWindow counts admissions in 100 ms slots over the last 10 s.
type rateWindow struct {
	slot  [100]int64 // slot id (unix ms / 100) each counter belongs to
	count [100]int64
}

func (w *rateWindow) add(now time.Time) {
	id := now.UnixMilli() / 100
	i := id % 100
	if w.slot[i] != id {
		w.slot[i], w.count[i] = id, 0
	}
	w.count[i]++
}

func (w *rateWindow) perSecond(now time.Time) float64 {
	cur := now.UnixMilli() / 100
	var n int64
	for i := range w.slot {
		if d := cur - w.slot[i]; d >= 0 && d < 100 {
			n += w.count[i]
		}
	}
	return float64(n) / 10
}

// waitSamples keeps every wait up to maxSamples, then a uniform reservoir; max and count are exact.
const maxSamples = 200_000

type waitSamples struct {
	samples []int64
	count   int64
	max     int64
}

func (w *waitSamples) add(ms int64) {
	w.count++
	w.max = max(w.max, ms)
	if len(w.samples) < maxSamples {
		w.samples = append(w.samples, ms)
		return
	}
	if j := rand.Int64N(w.count); j < maxSamples {
		w.samples[j] = ms
	}
}

func (w *waitSamples) summary() WaitMs {
	if len(w.samples) == 0 {
		return WaitMs{}
	}
	s := slices.Clone(w.samples)
	slices.Sort(s)
	return WaitMs{P50: nearestRank(s, 0.50), P95: nearestRank(s, 0.95), Max: w.max, Count: w.count}
}

func nearestRank(sorted []int64, p float64) int64 {
	i := int(math.Ceil(p*float64(len(sorted)))) - 1
	return sorted[min(max(i, 0), len(sorted)-1)]
}
