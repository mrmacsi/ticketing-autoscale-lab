// Package fakeapi is an in-memory ticketing API used by tests. It follows the
// contract's endpoints, status codes and headers (X-Served-By alternating
// between instances, Idempotent-Replayed on replays) with strict inventory
// accounting, so tests can compare generator counters with server truth.
package fakeapi

import (
	"encoding/json"
	"fmt"
	"maps"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Options configure the fake server.
type Options struct {
	EventID    string
	Categories map[string]int64 // category id -> capacity
	Instances  []string         // X-Served-By values, rotated per request
	// Delay is added to every request (honours client cancellation).
	Delay time.Duration
	// FastEventFetch exempts GET /api/events/{id} from Delay so a run's setup
	// fetch is quick while journeys stay slow.
	FastEventFetch bool
	// Decline decides whether payment attempt number n (1-based, per order) is declined.
	Decline func(orderSeq int64, attempt int) bool
	// LoseFirstResponse makes the first request for each idempotency key of
	// the given path kind ("payment", "order", "reservation") apply its
	// effect but stall past the client timeout, simulating a lost response.
	LoseFirstResponse map[string]time.Duration
	// Queue, when set, serves /queue/* on the same host (like the load
	// balancer's path rule). Queue requests are not counted in Stats.
	Queue *Queue
	// RequireQueueToken enforces X-Queue-Token on POST /api/reservations
	// (waiting_room_required), verified with QueueAdminToken.
	RequireQueueToken bool
	QueueAdminToken   string
}

// Stats is server-side truth.
type Stats struct {
	Requests             int64
	ReservationsCreated  int64
	Rejected409          int64
	OrdersCreated        int64
	PaidOrders           int64
	TicketsIssued        int64
	Declines             int64
	Replays              int64
	PaymentKeysPerOrder  map[string][]string
	MaxConcurrent        int64
	ByInstance           map[string]int64
	ReservedPerCategory  map[string]int64
	CPUWorkRequests      int64
	RequestsWithoutIdKey int64
	// ReservationTokens is the X-Queue-Token of every POST /api/reservations
	// ("" when absent), in arrival order.
	ReservationTokens []string
	// QueueTokenRejected counts reservations refused with 403 queue_token_*.
	QueueTokenRejected int64
}

// Server is the fake API.
type Server struct {
	*httptest.Server
	opts Options

	served   atomic.Int64
	inflight atomic.Int64
	maxIn    atomic.Int64

	mu           sync.Mutex
	available    map[string]int64
	reservations map[string]*reservation
	orders       map[string]*order
	orderByRes   map[string]string
	idem         map[string]stored
	lost         map[string]bool
	stats        Stats
	seq          int64
}

type reservation struct {
	ID, CategoryID, Status string
	Quantity               int64
}

type order struct {
	ID, ReservationID, Status string
	Seq                       int64
	Attempts                  int
	Tickets                   []map[string]string
}

type stored struct {
	status      int
	body        []byte
	fingerprint string
}

// New starts the fake server.
func New(opts Options) *Server {
	if opts.EventID == "" {
		opts.EventID = "evt-1"
	}
	if len(opts.Categories) == 0 {
		opts.Categories = map[string]int64{"cat-a": 1000}
	}
	if len(opts.Instances) == 0 {
		opts.Instances = []string{"app-a", "app-b"}
	}
	if opts.QueueAdminToken == "" {
		opts.QueueAdminToken = DefaultAdminToken
	}
	s := &Server{
		opts:         opts,
		available:    map[string]int64{},
		reservations: map[string]*reservation{},
		orders:       map[string]*order{},
		orderByRes:   map[string]string{},
		idem:         map[string]stored{},
		lost:         map[string]bool{},
		stats: Stats{
			PaymentKeysPerOrder: map[string][]string{},
			ByInstance:          map[string]int64{},
			ReservedPerCategory: map[string]int64{},
		},
	}
	for id, c := range opts.Categories {
		s.available[id] = c
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/events", s.listEvents)
	mux.HandleFunc("GET /api/events/{id}", s.getEvent)
	mux.HandleFunc("GET /api/events/{id}/availability", s.availability)
	mux.HandleFunc("POST /api/reservations", s.createReservation)
	mux.HandleFunc("POST /api/orders", s.createOrder)
	mux.HandleFunc("POST /api/orders/{id}/simulate-payment", s.pay)
	mux.HandleFunc("GET /api/lab/cpu-work", s.cpuWork)
	app := s.wrap(mux)
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if opts.Queue != nil && strings.HasPrefix(r.URL.Path, "/queue/") {
			opts.Queue.ServeHTTP(w, r)
			return
		}
		app.ServeHTTP(w, r)
	}))
	return s
}

// Stats returns a snapshot of server-side counters.
func (s *Server) Stats() Stats {
	s.mu.Lock()
	defer s.mu.Unlock()
	st := s.stats
	st.Requests = s.served.Load()
	st.MaxConcurrent = s.maxIn.Load()
	st.PaymentKeysPerOrder = map[string][]string{}
	for k, v := range s.stats.PaymentKeysPerOrder {
		st.PaymentKeysPerOrder[k] = append([]string(nil), v...)
	}
	st.ByInstance = map[string]int64{}
	for k, v := range s.stats.ByInstance {
		st.ByInstance[k] = v
	}
	st.ReservationTokens = slices.Clone(s.stats.ReservationTokens)
	return st
}

func (s *Server) wrap(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := s.served.Add(1)
		cur := s.inflight.Add(1)
		defer s.inflight.Add(-1)
		for {
			m := s.maxIn.Load()
			if cur <= m || s.maxIn.CompareAndSwap(m, cur) {
				break
			}
		}
		inst := s.opts.Instances[int(n-1)%len(s.opts.Instances)]
		s.mu.Lock()
		s.stats.ByInstance[inst]++
		s.mu.Unlock()
		w.Header().Set("X-Served-By", inst)
		w.Header().Set("X-App-Version", "test")
		if s.opts.Delay > 0 && !(s.opts.FastEventFetch && r.Method == http.MethodGet && strings.Count(r.URL.Path, "/") == 3) {
			select {
			case <-time.After(s.opts.Delay):
			case <-r.Context().Done():
				return
			}
		}
		h.ServeHTTP(w, r)
	})
}

func writeJSON(w http.ResponseWriter, status int, v any) []byte {
	body, _ := json.Marshal(v)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(body)
	return body
}

func errBody(code, msg string) map[string]string {
	return map[string]string{"error": code, "message": msg}
}

func (s *Server) event() map[string]any {
	cats := []map[string]any{}
	for _, id := range sortedKeys(s.opts.Categories) {
		cats = append(cats, map[string]any{"id": id, "event_id": s.opts.EventID, "name": id, "price_cents": 4500, "capacity": s.opts.Categories[id]})
	}
	return map[string]any{"id": s.opts.EventID, "name": "Lab", "categories": cats}
}

func (s *Server) listEvents(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, 200, map[string]any{"data": []any{s.event()}})
}

func (s *Server) getEvent(w http.ResponseWriter, r *http.Request) {
	if r.PathValue("id") != s.opts.EventID {
		writeJSON(w, 404, errBody("not_found", "no event"))
		return
	}
	writeJSON(w, 200, map[string]any{"data": s.event()})
}

func (s *Server) availability(w http.ResponseWriter, r *http.Request) {
	if r.PathValue("id") != s.opts.EventID {
		writeJSON(w, 404, errBody("not_found", "no event"))
		return
	}
	s.mu.Lock()
	cats := []map[string]any{}
	total := int64(0)
	for _, id := range sortedKeys(s.opts.Categories) {
		cats = append(cats, map[string]any{"id": id, "name": id, "price_cents": 4500, "capacity": s.opts.Categories[id], "available": s.available[id]})
		total += s.available[id]
	}
	s.mu.Unlock()
	writeJSON(w, 200, map[string]any{"data": map[string]any{"event_id": s.opts.EventID, "sold_out": total == 0, "categories": cats, "as_of": time.Now().UTC()}})
}

// idempotent wraps a POST handler with key replay semantics. The handler
// runs under s.mu and returns status + body.
func (s *Server) idempotent(kind string, w http.ResponseWriter, r *http.Request, fn func(body map[string]any) (int, any)) {
	key := r.Header.Get("Idempotency-Key")
	var body map[string]any
	_ = json.NewDecoder(r.Body).Decode(&body)
	if key == "" {
		s.mu.Lock()
		s.stats.RequestsWithoutIdKey++
		s.mu.Unlock()
		writeJSON(w, 422, errBody("idempotency_key_required", "missing key"))
		return
	}
	fp := r.URL.Path + fmt.Sprint(body)
	scoped := kind + ":" + key
	s.mu.Lock()
	if prev, ok := s.idem[scoped]; ok {
		s.stats.Replays++
		s.mu.Unlock()
		if prev.fingerprint != fp {
			writeJSON(w, 422, errBody("idempotency_key_reused", "different payload"))
			return
		}
		status := prev.status
		if status == 201 {
			status = 200
		}
		w.Header().Set("Idempotent-Replayed", "true")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write(prev.body)
		return
	}
	status, v := fn(body)
	buf, _ := json.Marshal(v)
	s.idem[scoped] = stored{status: status, body: buf, fingerprint: fp}
	stall := time.Duration(0)
	if d, ok := s.opts.LoseFirstResponse[kind]; ok && !s.lost[scoped] {
		s.lost[scoped] = true
		stall = d
	}
	s.mu.Unlock()
	if stall > 0 {
		select {
		case <-time.After(stall):
		case <-r.Context().Done():
			return
		}
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(buf)
}

func (s *Server) nextID(prefix string) string {
	s.seq++
	return prefix + "-" + strconv.FormatInt(s.seq, 10)
}

func (s *Server) createReservation(w http.ResponseWriter, r *http.Request) {
	token := r.Header.Get("X-Queue-Token")
	s.mu.Lock()
	s.stats.ReservationTokens = append(s.stats.ReservationTokens, token)
	s.mu.Unlock()
	if s.opts.RequireQueueToken {
		// Checked before idempotency replay and any inventory work (§3.3).
		code := "queue_token_required"
		if token != "" {
			var event string
			event, code = VerifyQueueToken(s.opts.QueueAdminToken, token, time.Now())
			if code == "" && event != s.opts.EventID {
				code = "queue_token_wrong_event"
			}
		}
		if code != "" {
			s.mu.Lock()
			s.stats.QueueTokenRejected++
			s.mu.Unlock()
			writeJSON(w, 403, errBody(code, "waiting room admission required"))
			return
		}
	}
	s.idempotent("reservation", w, r, func(b map[string]any) (int, any) {
		cat, _ := b["category_id"].(string)
		qf, _ := b["quantity"].(float64)
		q := int64(qf)
		if _, ok := s.opts.Categories[cat]; !ok || q < 1 || q > 8 {
			return 422, errBody("validation_failed", "bad request")
		}
		if s.available[cat] < q {
			s.stats.Rejected409++
			code := "insufficient_inventory"
			if s.available[cat] == 0 {
				code = "sold_out"
			}
			return 409, errBody(code, "not enough tickets")
		}
		s.available[cat] -= q
		res := &reservation{ID: s.nextID("res"), CategoryID: cat, Quantity: q, Status: "active"}
		s.reservations[res.ID] = res
		s.stats.ReservationsCreated++
		s.stats.ReservedPerCategory[cat] += q
		return 201, map[string]any{"data": map[string]any{"id": res.ID, "category_id": cat, "quantity": q, "status": res.Status}}
	})
}

func (s *Server) createOrder(w http.ResponseWriter, r *http.Request) {
	s.idempotent("order", w, r, func(b map[string]any) (int, any) {
		resID, _ := b["reservation_id"].(string)
		res, ok := s.reservations[resID]
		if !ok || res.Status != "active" {
			return 409, errBody("reservation_not_active", "reservation not active")
		}
		if id, ok := s.orderByRes[resID]; ok {
			return 200, map[string]any{"data": s.orderView(s.orders[id])}
		}
		s.stats.OrdersCreated++
		o := &order{ID: s.nextID("ord"), ReservationID: resID, Status: "pending", Seq: s.stats.OrdersCreated}
		s.orders[o.ID] = o
		s.orderByRes[resID] = o.ID
		return 201, map[string]any{"data": s.orderView(o)}
	})
}

func (s *Server) pay(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	s.mu.Lock()
	s.stats.PaymentKeysPerOrder[id] = append(s.stats.PaymentKeysPerOrder[id], r.Header.Get("Idempotency-Key"))
	s.mu.Unlock()
	s.idempotent("payment", w, r, func(map[string]any) (int, any) {
		o, ok := s.orders[id]
		if !ok {
			return 404, errBody("not_found", "no order")
		}
		if o.Status == "paid" {
			return 200, map[string]any{"data": s.orderView(o)}
		}
		o.Attempts++
		if s.opts.Decline != nil && s.opts.Decline(o.Seq, o.Attempts) {
			o.Status = "payment_failed"
			s.stats.Declines++
			return 402, map[string]any{"error": "payment_declined", "message": "declined", "data": s.orderView(o)}
		}
		res := s.reservations[o.ReservationID]
		res.Status = "consumed"
		o.Status = "paid"
		for i := range res.Quantity {
			o.Tickets = append(o.Tickets, map[string]string{"id": fmt.Sprintf("%s-t%d", o.ID, i)})
		}
		s.stats.PaidOrders++
		s.stats.TicketsIssued += res.Quantity
		return 200, map[string]any{"data": s.orderView(o)}
	})
}

func (s *Server) cpuWork(w http.ResponseWriter, r *http.Request) {
	ms, _ := strconv.Atoi(r.URL.Query().Get("ms"))
	s.mu.Lock()
	s.stats.CPUWorkRequests++
	s.mu.Unlock()
	writeJSON(w, 200, map[string]any{"data": map[string]any{"requested_ms": ms, "performed_ms": ms}})
}

func (s *Server) orderView(o *order) map[string]any {
	tickets := o.Tickets
	if tickets == nil {
		tickets = []map[string]string{}
	}
	return map[string]any{"id": o.ID, "reservation_id": o.ReservationID, "status": o.Status, "payment_attempts": o.Attempts, "tickets": tickets}
}

func sortedKeys(m map[string]int64) []string { return slices.Sorted(maps.Keys(m)) }
