// Package queue is the in-memory FIFO waiting room (docs/waiting-room.md sections 2 and 3).
//
// One Room is the single source of truth for order. Waiting customers long-poll their status; each
// entry has a channel that is closed exactly once when the entry stops waiting (admitted, sold out,
// removed), so a long poll is one blocked handler goroutine and a select, with no polling loops.
// Positions are O(1): every waiting entry stores a base position and the room counts how many
// entries have left the head of the line; only the rare removal from the middle renumbers.
package queue

import (
	"context"
	"crypto/rand"
	"encoding/base32"
	"errors"
	"math"
	"strings"
	"sync"
	"time"

	"github.com/mrmacsi/ticketing-autoscale-lab/waiting-room/token"
)

// TimeFormat is used for every timestamp in JSON (RFC 3339, milliseconds, UTC).
const TimeFormat = "2006-01-02T15:04:05.000Z"

// FormatTime renders t in TimeFormat.
func FormatTime(t time.Time) string { return t.UTC().Format(TimeFormat) }

// Public statuses.
const (
	StatusWaiting  = "waiting"
	StatusAdmitted = "admitted"
	StatusSoldOut  = "sold_out"
	StatusOpen     = "open"
)

// Release outcomes accepted by POST /queue/release.
var Outcomes = map[string]bool{"purchased": true, "sold_out": true, "abandoned": true, "failed": true}

var (
	ErrUnknownEvent   = errors.New("unknown_event")
	ErrUnknownQueueID = errors.New("unknown_queue_id")
	ErrClosed         = errors.New("room closed")
)

// CapacityStaleAfter is how long a capacity source error keeps the last known healthy count.
const CapacityStaleAfter = 30 * time.Second

// finishedRetention is how long released/expired/sold-out entries stay answerable by id.
const finishedRetention = time.Hour

type state uint8

const (
	stWaiting state = iota
	stAdmitted
	stReleased
	stExpired
	stSoldOut
)

// Config is the admin-controlled configuration (PUT /admin/config).
type Config struct {
	Enabled              bool   `json:"enabled"`
	EventID              string `json:"eventId"`
	ActiveUsersPerServer int    `json:"activeUsersPerServer"`
	AdmissionTTLSeconds  int    `json:"admissionTtlSeconds"`
	SoldOutGraceSeconds  int    `json:"soldOutGraceSeconds"`
	// AdmissionMode is "fixed" (capacity = healthy servers × activeUsersPerServer) or "adaptive"
	// (capacity = sum of per-server limits learned from load signals; activeUsersPerServer is the
	// starting limit of each new server).
	AdmissionMode string         `json:"admissionMode"`
	Adaptive      AdaptiveConfig `json:"adaptive"`
	// WaitingTimeoutSeconds drops people in line who have not checked in (joined or polled) for this
	// long: they closed the page. 0 disables. Must exceed the 25 s maximum long poll.
	WaitingTimeoutSeconds int `json:"waitingTimeoutSeconds"`
}

// DefaultConfig is the configuration at start: disabled, 25 per server, 60 s TTL, 125 s grace
// (the ticketing API's default reservation TTL of 120 s + 5 s).
func DefaultConfig() Config {
	return Config{ActiveUsersPerServer: 25, AdmissionTTLSeconds: 60, SoldOutGraceSeconds: 125,
		AdmissionMode: ModeFixed, Adaptive: DefaultAdaptiveConfig(), WaitingTimeoutSeconds: 45}
}

// Server is one app server the load balancer reports healthy.
type Server struct {
	Name    string `json:"name"`
	IP      string `json:"ip"`
	BaseURL string `json:"-"` // where the inventory poller reaches it, e.g. http://10.60.0.9:8080
}

// CapacityUpdate is pushed by the capacity monitor after each poll.
type CapacityUpdate struct {
	Healthy int
	Servers []Server
	Source  string
	Err     string // last error, "" when the latest poll succeeded
}

// View is the Status object returned by join and status.
type View struct {
	QueueID              string `json:"queueId"`
	EventID              string `json:"eventId"`
	Status               string `json:"status"`
	Position             int64  `json:"position"`
	Waiting              int    `json:"waiting"`
	EstimatedWaitSeconds *int64 `json:"estimatedWaitSeconds"`
	Token                string `json:"token,omitempty"`
	TokenExpiresAt       string `json:"tokenExpiresAt,omitempty"`
	PollAfterMs          int    `json:"pollAfterMs"`
}

type entry struct {
	id          string
	eventID     string
	customerRef string
	basePos     int64 // position = basePos - room.popped while waiting
	joinedAt    time.Time
	lastSeen    time.Time // last join or status poll; a long poll in progress keeps it fresh
	state       state
	admittedAt  time.Time
	expiresAt   time.Time
	finishedAt  time.Time
	token       string
	tokenExp    int64
	done        chan struct{} // closed once when the entry stops waiting
}

func (e *entry) finish(st state, now time.Time) {
	e.state = st
	e.finishedAt = now
}

type inventory struct {
	available  int
	held       int // tickets in other customers' checkouts; -1 when the app does not report it
	appSoldOut bool
	observedAt time.Time
}

// Room is safe for concurrent use.
type Room struct {
	signer *token.Signer
	now    func() time.Time
	closed chan struct{}
	once   sync.Once

	mu         sync.Mutex
	cfg        Config
	entries    map[string]*entry
	byCustomer map[string]*entry // eventId + "\x00" + customerRef, while waiting or admitted
	line       []*entry          // waiting entries, line[head:] in FIFO order
	head       int
	popped     int64 // entries that have left the head of the line since the last clear
	active     map[string]*entry

	healthy   int
	servers   []Server
	limits    map[string]*serverLimit // latest load signals per healthy server name
	ctl       controller              // shared adaptive limit
	capSource string
	capErr    string

	inv       *inventory // nil while unknown
	invErr    string
	zeroSince time.Time // first observation of available == 0 in the current streak
	soldOut   bool
	// budget is how many more customers may be admitted before the next availability observation:
	// never more than there are tickets left, so a ticket returned by an expired hold does not let
	// a whole batch of customers in to compete for it.
	budget    int
	lastPrune time.Time

	joinedTotal, admittedTotal, releasedTotal, expiredTotal, soldOutTotal, leftWaitingTotal int64
	abandonedTotal                                                                          int64 // dropped from the line for not checking in
	lastSweep                                                                               time.Time
	releasedByOutcome                                                                       map[string]int64

	rate  rateWindow
	waits waitSamples
}

// New creates a room. now may be nil (time.Now).
func New(signer *token.Signer, now func() time.Time) *Room {
	if now == nil {
		now = time.Now
	}
	r := &Room{signer: signer, now: now, closed: make(chan struct{}), cfg: DefaultConfig()}
	r.clearLocked()
	return r
}

// Close wakes every long poll (used at shutdown).
func (r *Room) Close() { r.once.Do(func() { close(r.closed) }) }

// Run calls Tick every interval until ctx is done.
func (r *Room) Run(ctx context.Context, interval time.Duration) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			r.Tick()
		}
	}
}

// RunControl calls Control every interval until ctx is done.
func (r *Room) RunControl(ctx context.Context, interval time.Duration) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			r.Control()
		}
	}
}

// Config returns the current configuration.
func (r *Room) Config() Config {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.cfg
}

// Configure replaces the configuration. A different eventId, or enabled=false, clears the queue,
// the active set and the counters (waiters are woken and find their id gone).
func (r *Room) Configure(c Config) Config {
	r.mu.Lock()
	defer r.mu.Unlock()
	old := r.cfg
	if c.AdmissionMode == "" {
		c.AdmissionMode = ModeFixed
	}
	if c.Adaptive == (AdaptiveConfig{}) {
		c.Adaptive = DefaultAdaptiveConfig()
	}
	r.cfg = c
	if !c.Enabled || c.EventID != old.EventID {
		r.clearLocked()
	}
	return r.cfg
}

// Reset clears queue, active set and counters, keeping the configuration.
func (r *Room) Reset() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.clearLocked()
}

func (r *Room) clearLocked() {
	for _, e := range r.entries {
		if e.state == stWaiting {
			close(e.done)
		}
	}
	r.entries = make(map[string]*entry)
	r.byCustomer = make(map[string]*entry)
	r.line, r.head, r.popped = nil, 0, 0
	r.active = make(map[string]*entry)
	r.inv, r.invErr, r.zeroSince, r.soldOut, r.budget = nil, "", time.Time{}, false, 0
	r.joinedTotal, r.admittedTotal, r.releasedTotal, r.expiredTotal, r.soldOutTotal, r.leftWaitingTotal = 0, 0, 0, 0, 0, 0
	r.abandonedTotal = 0
	r.releasedByOutcome = make(map[string]int64)
	r.limits = make(map[string]*serverLimit)
	r.ctl = controller{}
	r.rate = rateWindow{}
	r.waits = waitSamples{}
}

// SetCapacity records the latest capacity observation.
func (r *Room) SetCapacity(u CapacityUpdate) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.healthy = max(u.Healthy, 0)
	r.servers = append([]Server(nil), u.Servers...)
	// A server that left the load balancer's healthy set loses its learned limit; if it returns it
	// starts over in slow start.
	for name := range r.limits {
		if r.limitLocked(name) == nil {
			delete(r.limits, name)
		}
	}
	r.capSource = u.Source
	r.capErr = u.Err
}

// HealthyServers returns the servers of the latest capacity observation.
func (r *Room) HealthyServers() []Server {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]Server(nil), r.servers...)
}

// SetInventory records availability for eventID (ignored if the room moved to another event). held is
// the number of tickets in other customers' checkouts, or -1 when unknown.
func (r *Room) SetInventory(eventID string, available, held int, appSoldOut bool, at time.Time) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.cfg.Enabled || eventID != r.cfg.EventID {
		return
	}
	r.inv = &inventory{available: available, held: held, appSoldOut: appSoldOut, observedAt: at}
	r.invErr = ""
	r.budget = available
	if available > 0 {
		r.zeroSince = time.Time{}
	} else if r.zeroSince.IsZero() {
		r.zeroSince = at
	}
}

// SetInventoryUnknown marks inventory unknown: admission continues by capacity and sold out is not
// declared while it stays unknown.
func (r *Room) SetInventoryUnknown(eventID, reason string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if eventID != r.cfg.EventID {
		return
	}
	r.inv = nil
	r.invErr = reason
}

// Tick expires stale admissions, evaluates sold out and admits from the head of the line while
// active < capacity. Called every 100 ms by Run.
func (r *Room) Tick() {
	now := r.now()
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.cfg.Enabled {
		return
	}
	for id, e := range r.active {
		if !now.Before(e.expiresAt) {
			e.finish(stExpired, now)
			delete(r.active, id)
			r.dropCustomerLocked(e)
			r.expiredTotal++
		}
	}
	if r.cfg.WaitingTimeoutSeconds > 0 && now.Sub(r.lastSweep) >= time.Second {
		r.lastSweep = now
		r.sweepAbandonedLocked(now)
	}
	// Sold out is final when nothing is available and nothing is held (no hold can come back). When the
	// app does not report holds, wait until every hold must have resolved (the grace period).
	if !r.soldOut && r.inv != nil && r.inv.available == 0 && !r.zeroSince.IsZero() &&
		(r.inv.held == 0 || r.inv.held < 0 && now.Sub(r.zeroSince) >= time.Duration(r.cfg.SoldOutGraceSeconds)*time.Second) {
		r.declareSoldOutLocked(now)
	}
	if !r.soldOut && (r.inv == nil || r.budget > 0) {
		capacity := r.capacityLocked()
		for len(r.active) < capacity && r.head < len(r.line) && (r.inv == nil || r.budget > 0) {
			r.admitLocked(r.popLocked(), now)
			if r.inv != nil {
				r.budget--
			}
		}
	}
	if now.Sub(r.lastPrune) >= time.Minute {
		r.lastPrune = now
		for id, e := range r.entries {
			if e.state != stWaiting && e.state != stAdmitted && now.Sub(e.finishedAt) > finishedRetention {
				delete(r.entries, id)
			}
		}
	}
}

func (r *Room) popLocked() *entry {
	e := r.line[r.head]
	r.line[r.head] = nil
	r.head++
	r.popped++
	if r.head >= 1024 && r.head*2 >= len(r.line) {
		r.line = append(make([]*entry, 0, len(r.line)-r.head+64), r.line[r.head:]...)
		r.head = 0
	}
	return e
}

func (r *Room) admitLocked(e *entry, now time.Time) {
	ttl := time.Duration(r.cfg.AdmissionTTLSeconds) * time.Second
	e.state = stAdmitted
	e.admittedAt = now
	e.expiresAt = now.Add(ttl)
	e.tokenExp = now.Add(ttl).Unix()
	e.token = r.signer.Sign(token.Payload{Q: e.id, E: e.eventID, Exp: e.tokenExp})
	r.active[e.id] = e
	r.admittedTotal++
	r.rate.add(now)
	r.waits.add(now.Sub(e.joinedAt).Milliseconds())
	close(e.done)
}

func (r *Room) declareSoldOutLocked(now time.Time) {
	r.soldOut = true
	for r.head < len(r.line) {
		e := r.popLocked()
		e.finish(stSoldOut, now)
		r.dropCustomerLocked(e)
		r.soldOutTotal++
		close(e.done)
	}
	r.line, r.head = nil, 0
}

func (r *Room) dropCustomerLocked(e *entry) {
	if e.customerRef == "" {
		return
	}
	k := customerKey(e.eventID, e.customerRef)
	if r.byCustomer[k] == e {
		delete(r.byCustomer, k)
	}
}

func customerKey(eventID, ref string) string { return eventID + "\x00" + ref }

// Join adds a customer to the line (or returns their existing entry while it is waiting/admitted).
// eventID must already be validated; it is compared case-insensitively with the configured event.
func (r *Room) Join(eventID, customerRef string) (View, error) {
	id := NewQueueID()
	now := r.now()
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.cfg.Enabled {
		return View{QueueID: id, EventID: eventID, Status: StatusOpen, EstimatedWaitSeconds: zero()}, nil
	}
	if !strings.EqualFold(eventID, r.cfg.EventID) {
		return View{}, ErrUnknownEvent
	}
	eventID = r.cfg.EventID
	if customerRef != "" {
		if e := r.byCustomer[customerKey(eventID, customerRef)]; e != nil {
			return r.viewLocked(e, now), nil
		}
	}
	e := &entry{id: id, eventID: eventID, customerRef: customerRef, joinedAt: now, lastSeen: now, done: make(chan struct{})}
	r.entries[id] = e
	r.joinedTotal++
	if r.soldOut {
		e.finish(stSoldOut, now)
		close(e.done)
		r.soldOutTotal++
		return r.viewLocked(e, now), nil
	}
	e.basePos = r.popped + int64(len(r.line)-r.head) + 1
	r.line = append(r.line, e)
	if customerRef != "" {
		r.byCustomer[customerKey(eventID, customerRef)] = e
	}
	return r.viewLocked(e, now), nil
}

// Status returns the entry's status. With wait > 0 and the entry waiting, it blocks until the entry
// stops waiting, wait elapses, ctx is done (returns ctx.Err()) or the room closes.
func (r *Room) Status(ctx context.Context, id string, wait time.Duration) (View, error) {
	r.mu.Lock()
	e := r.entries[id]
	if e == nil {
		r.mu.Unlock()
		return View{}, ErrUnknownQueueID
	}
	e.lastSeen = r.now()
	if e.state != stWaiting || wait <= 0 {
		v := r.viewLocked(e, r.now())
		r.mu.Unlock()
		return v, nil
	}
	done := e.done
	r.mu.Unlock()

	t := time.NewTimer(wait)
	defer t.Stop()
	select {
	case <-done:
	case <-t.C:
	case <-r.closed:
	case <-ctx.Done():
		return View{}, ctx.Err()
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	if e = r.entries[id]; e == nil {
		return View{}, ErrUnknownQueueID
	}
	e.lastSeen = r.now()
	return r.viewLocked(e, r.now()), nil
}

// Release frees an admitted customer's slot, or removes a waiting customer from the line.
// It reports whether something was (or had already been) released; unknown ids report false.
func (r *Room) Release(id, outcome string) bool {
	now := r.now()
	r.mu.Lock()
	defer r.mu.Unlock()
	e := r.entries[id]
	if e == nil {
		return false
	}
	switch e.state {
	case stAdmitted:
		e.finish(stReleased, now)
		delete(r.active, id)
		r.dropCustomerLocked(e)
		r.releasedTotal++
		r.releasedByOutcome[outcome]++
		return true
	case stWaiting:
		r.removeWaitingLocked(e)
		delete(r.entries, id)
		r.dropCustomerLocked(e)
		r.leftWaitingTotal++
		close(e.done)
		return true
	case stReleased:
		return true
	default:
		return false
	}
}

// sweepAbandonedLocked drops people in line who stopped checking in (closed the page) so they are
// never admitted and do not count as waiting. Their id is forgotten: if they come back they get
// unknown_queue_id and re-join at the back. One pass, renumbering the rest of the line.
func (r *Room) sweepAbandonedLocked(now time.Time) {
	timeout := time.Duration(r.cfg.WaitingTimeoutSeconds) * time.Second
	kept := r.line[r.head:r.head]
	dropped := 0
	for _, e := range r.line[r.head:] {
		if now.Sub(e.lastSeen) > timeout {
			e.finish(stReleased, now)
			close(e.done)
			r.dropCustomerLocked(e)
			delete(r.entries, e.id)
			dropped++
			continue
		}
		kept = append(kept, e)
	}
	if dropped == 0 {
		return
	}
	r.abandonedTotal += int64(dropped)
	for i := len(kept) + r.head; i < len(r.line); i++ {
		r.line[i] = nil
	}
	r.line = r.line[:r.head+len(kept)]
	for i, e := range kept {
		e.basePos = r.popped + int64(i) + 1
	}
}

func (r *Room) removeWaitingLocked(e *entry) {
	i := r.head + int(e.basePos-r.popped) - 1
	if i < r.head || i >= len(r.line) || r.line[i] != e {
		// Should not happen; fall back to a scan so the invariant self-heals.
		for i = r.head; i < len(r.line) && r.line[i] != e; i++ {
		}
		if i == len(r.line) {
			return
		}
	}
	copy(r.line[i:], r.line[i+1:])
	r.line[len(r.line)-1] = nil
	r.line = r.line[:len(r.line)-1]
	for _, later := range r.line[i:] {
		later.basePos--
	}
}

func (r *Room) viewLocked(e *entry, now time.Time) View {
	v := View{QueueID: e.id, EventID: e.eventID, Waiting: len(r.line) - r.head, EstimatedWaitSeconds: zero()}
	switch e.state {
	case stWaiting:
		v.Status = StatusWaiting
		v.Position = e.basePos - r.popped
		v.PollAfterMs = 1000
		v.EstimatedWaitSeconds = nil
		if rate := r.rate.perSecond(now); rate > 0 {
			s := int64(math.Ceil(float64(v.Position) / rate))
			v.EstimatedWaitSeconds = &s
		}
	case stAdmitted, stReleased, stExpired:
		v.Status = StatusAdmitted
		v.Token = e.token
		v.TokenExpiresAt = FormatTime(time.Unix(e.tokenExp, 0))
	case stSoldOut:
		v.Status = StatusSoldOut
	}
	return v
}

func zero() *int64 { z := int64(0); return &z }

var idEncoding = base32.StdEncoding.WithPadding(base32.NoPadding)

// NewQueueID returns "q_" + 26 base32 characters of 128 random bits.
func NewQueueID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err) // crypto/rand never fails on supported platforms
	}
	return "q_" + idEncoding.EncodeToString(b[:])
}
