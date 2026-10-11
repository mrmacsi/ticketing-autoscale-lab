package fakeapi

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
)

// DefaultAdminToken signs admission tokens when no token is configured.
const DefaultAdminToken = "lab-admin-token"

// QueueOptions configure the fake waiting room (docs/waiting-room.md §3.1).
type QueueOptions struct {
	EventID string
	// Capacity is the number of admitted customers that may be active at once.
	Capacity int
	// Disabled answers every join with status "open".
	Disabled bool
	// AdminToken derives the token signing key (default DefaultAdminToken).
	AdminToken string
	// AdmissionTTL sets the token expiry (default 60 s).
	AdmissionTTL time.Duration
}

// QueueStats is the fake waiting room's server-side truth.
type QueueStats struct {
	Requests       int64
	StatusRequests int64
	Joins          int64 // new entries created
	Admitted       int64
	Released       map[string]int64 // outcome -> count (known ids only)
	JoinOrder      []string         // customerRefs in join order
	AdmissionOrder []string         // customerRefs in admission order
	MaxWaiting     int
	Injected       int64 // injected failures served
}

// Queue is an in-memory waiting room: strict FIFO admission while the number
// of active (admitted, unreleased) customers is below Capacity, long-polled
// status and HMAC-signed admission tokens. Admission is evaluated
// synchronously on every state change instead of on a 100 ms tick.
type Queue struct {
	opts    QueueOptions
	key     []byte
	handler http.Handler

	mu       sync.Mutex
	capacity int
	soldOut  bool
	entries  map[string]*qentry
	byRef    map[string]*qentry
	line     []*qentry
	active   map[string]*qentry
	failN    int
	failCode int
	stats    QueueStats
}

type qentry struct {
	id, ref, status, token string
	joinedAt               time.Time
	expires                time.Time
	changed                chan struct{}
}

// NewQueue creates a waiting room handler (mount at /queue/ or serve alone).
func NewQueue(opts QueueOptions) *Queue {
	if opts.EventID == "" {
		opts.EventID = "evt-1"
	}
	if opts.AdminToken == "" {
		opts.AdminToken = DefaultAdminToken
	}
	if opts.AdmissionTTL <= 0 {
		opts.AdmissionTTL = 60 * time.Second
	}
	q := &Queue{
		opts:     opts,
		key:      QueueSigningKey(opts.AdminToken),
		capacity: opts.Capacity,
		entries:  map[string]*qentry{},
		byRef:    map[string]*qentry{},
		active:   map[string]*qentry{},
		stats:    QueueStats{Released: map[string]int64{}},
	}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /queue/join", q.join)
	mux.HandleFunc("GET /queue/status/{id}", q.status)
	mux.HandleFunc("POST /queue/release", q.release)
	mux.HandleFunc("GET /queue/health", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, 200, map[string]string{"status": "ok"})
	})
	q.handler = mux
	return q
}

// ServeHTTP implements http.Handler.
func (q *Queue) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("X-Served-By", "waiting-room")
	w.Header().Set("Cache-Control", "no-store")
	q.mu.Lock()
	q.stats.Requests++
	if q.failN > 0 && r.URL.Path != "/queue/health" {
		q.failN--
		q.stats.Injected++
		code := q.failCode
		q.mu.Unlock()
		writeJSON(w, code, errBody("injected_failure", "injected"))
		return
	}
	q.mu.Unlock()
	q.handler.ServeHTTP(w, r)
}

// FailNext makes the next n queue requests answer with HTTP status code.
func (q *Queue) FailNext(n, code int) {
	q.mu.Lock()
	q.failN, q.failCode = n, code
	q.mu.Unlock()
}

// SetCapacity changes the active-customer capacity and admits accordingly.
func (q *Queue) SetCapacity(n int) {
	q.mu.Lock()
	q.capacity = n
	q.admitLocked()
	q.mu.Unlock()
}

// DeclareSoldOut tells every waiting customer "sold_out" and stops admitting.
func (q *Queue) DeclareSoldOut() {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.soldOut = true
	for _, e := range q.line {
		q.setStatusLocked(e, "sold_out")
	}
	q.line = nil
}

// Reset forgets every entry, like a queue VM restart: waiting long polls end
// and later status calls answer 404 unknown_queue_id.
func (q *Queue) Reset() {
	q.mu.Lock()
	defer q.mu.Unlock()
	for _, e := range q.entries {
		close(e.changed)
		e.changed = make(chan struct{})
	}
	q.entries = map[string]*qentry{}
	q.byRef = map[string]*qentry{}
	q.active = map[string]*qentry{}
	q.line = nil
	q.soldOut = false
}

// Waiting returns the number of customers in line.
func (q *Queue) Waiting() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return len(q.line)
}

// Active returns the number of admitted, unreleased customers.
func (q *Queue) Active() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return len(q.active)
}

// Stats returns a snapshot of server-side counters.
func (q *Queue) Stats() QueueStats {
	q.mu.Lock()
	defer q.mu.Unlock()
	st := q.stats
	st.Released = map[string]int64{}
	for k, v := range q.stats.Released {
		st.Released[k] = v
	}
	st.JoinOrder = slices.Clone(q.stats.JoinOrder)
	st.AdmissionOrder = slices.Clone(q.stats.AdmissionOrder)
	return st
}

func (q *Queue) setStatusLocked(e *qentry, status string) {
	e.status = status
	close(e.changed)
	e.changed = make(chan struct{})
}

func (q *Queue) admitLocked() {
	for len(q.line) > 0 && len(q.active) < q.capacity && !q.soldOut {
		e := q.line[0]
		q.line = q.line[1:]
		e.expires = time.Now().Add(q.opts.AdmissionTTL)
		e.token = SignQueueToken(q.opts.AdminToken, e.id, q.opts.EventID, e.expires)
		q.active[e.id] = e
		q.stats.Admitted++
		q.stats.AdmissionOrder = append(q.stats.AdmissionOrder, e.ref)
		q.setStatusLocked(e, "admitted")
	}
}

func (q *Queue) view(e *qentry) map[string]any {
	v := map[string]any{
		"queueId": e.id, "eventId": q.opts.EventID, "status": e.status,
		"position": 0, "waiting": len(q.line), "estimatedWaitSeconds": nil, "pollAfterMs": 0,
	}
	if e.status == "waiting" {
		v["position"] = slices.Index(q.line, e) + 1
	}
	if e.status == "admitted" {
		v["token"] = e.token
		v["tokenExpiresAt"] = e.expires.UTC().Format("2006-01-02T15:04:05.000Z")
	}
	return v
}

func newQueueID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return "q_" + hex.EncodeToString(b[:])
}

func (q *Queue) join(w http.ResponseWriter, r *http.Request) {
	var body struct {
		EventID     string `json:"eventId"`
		CustomerRef string `json:"customerRef"`
	}
	if json.NewDecoder(r.Body).Decode(&body) != nil || body.EventID == "" || body.CustomerRef == "" || len(body.CustomerRef) > 100 {
		writeJSON(w, 400, errBody("invalid_request", "bad join"))
		return
	}
	if body.EventID != q.opts.EventID {
		writeJSON(w, 404, errBody("unknown_event", "room is for another event"))
		return
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	if e, ok := q.byRef[body.CustomerRef]; ok && (e.status == "waiting" || e.status == "admitted") {
		writeJSON(w, 200, map[string]any{"data": q.view(e)})
		return
	}
	e := &qentry{id: newQueueID(), ref: body.CustomerRef, joinedAt: time.Now(), changed: make(chan struct{})}
	q.entries[e.id] = e
	q.byRef[e.ref] = e
	q.stats.Joins++
	q.stats.JoinOrder = append(q.stats.JoinOrder, e.ref)
	switch {
	case q.opts.Disabled:
		e.status = "open"
	case q.soldOut:
		e.status = "sold_out"
	default:
		e.status = "waiting"
		q.line = append(q.line, e)
		q.admitLocked()
		q.stats.MaxWaiting = max(q.stats.MaxWaiting, len(q.line))
	}
	writeJSON(w, 200, map[string]any{"data": q.view(e)})
}

func (q *Queue) status(w http.ResponseWriter, r *http.Request) {
	wait, _ := strconv.Atoi(r.URL.Query().Get("waitSeconds"))
	wait = min(max(wait, 0), 25)
	q.mu.Lock()
	q.stats.StatusRequests++
	e, ok := q.entries[r.PathValue("id")]
	if !ok {
		q.mu.Unlock()
		writeJSON(w, 404, errBody("unknown_queue_id", "no such entry"))
		return
	}
	if e.status == "waiting" && wait > 0 {
		ch := e.changed
		q.mu.Unlock()
		t := time.NewTimer(time.Duration(wait) * time.Second)
		select {
		case <-ch:
		case <-t.C:
		case <-r.Context().Done():
			t.Stop()
			return
		}
		t.Stop()
		q.mu.Lock()
		if q.entries[e.id] != e {
			q.mu.Unlock()
			writeJSON(w, 404, errBody("unknown_queue_id", "no such entry"))
			return
		}
	}
	v := q.view(e)
	q.mu.Unlock()
	writeJSON(w, 200, map[string]any{"data": v})
}

func (q *Queue) release(w http.ResponseWriter, r *http.Request) {
	var body struct {
		QueueID string `json:"queueId"`
		Outcome string `json:"outcome"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	switch body.Outcome {
	case "purchased", "sold_out", "abandoned", "failed":
	default:
		writeJSON(w, 400, errBody("invalid_request", "bad outcome"))
		return
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	e, ok := q.entries[body.QueueID]
	if !ok {
		writeJSON(w, 200, map[string]any{"data": map[string]any{"released": false}})
		return
	}
	q.stats.Released[body.Outcome]++
	delete(q.active, e.id)
	if i := slices.Index(q.line, e); i >= 0 {
		q.line = slices.Delete(q.line, i, i+1)
	}
	if e.status == "waiting" || e.status == "admitted" {
		q.setStatusLocked(e, "released")
	}
	q.admitLocked()
	writeJSON(w, 200, map[string]any{"data": map[string]any{"released": true}})
}

// QueueSigningKey derives the token signing key:
// HMAC_SHA256(key = adminToken, message = "waiting-room-token-v1").
func QueueSigningKey(adminToken string) []byte {
	mac := hmac.New(sha256.New, []byte(adminToken))
	mac.Write([]byte("waiting-room-token-v1"))
	return mac.Sum(nil)
}

var b64 = base64.RawURLEncoding

type tokenPayload struct {
	Q   string `json:"q"`
	E   string `json:"e"`
	Exp int64  `json:"exp"`
}

// SignQueueToken builds "v1." + b64url(payload) + "." + b64url(HMAC(key, "v1." + b64url(payload))).
func SignQueueToken(adminToken, queueID, eventID string, exp time.Time) string {
	payload, _ := json.Marshal(tokenPayload{Q: queueID, E: eventID, Exp: exp.Unix()})
	signed := "v1." + b64.EncodeToString(payload)
	mac := hmac.New(sha256.New, QueueSigningKey(adminToken))
	mac.Write([]byte(signed))
	return signed + "." + b64.EncodeToString(mac.Sum(nil))
}

// VerifyQueueToken checks a token the way the ticketing API does (§3.3) and
// returns its event id, or the error code queue_token_invalid /
// queue_token_expired.
func VerifyQueueToken(adminToken, token string, now time.Time) (eventID, errCode string) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 || parts[0] != "v1" {
		return "", "queue_token_invalid"
	}
	sig, err := b64.DecodeString(parts[2])
	if err != nil {
		return "", "queue_token_invalid"
	}
	mac := hmac.New(sha256.New, QueueSigningKey(adminToken))
	mac.Write([]byte(parts[0] + "." + parts[1]))
	if !hmac.Equal(sig, mac.Sum(nil)) {
		return "", "queue_token_invalid"
	}
	raw, err := b64.DecodeString(parts[1])
	var p tokenPayload
	if err != nil || json.Unmarshal(raw, &p) != nil || p.Q == "" || p.E == "" {
		return "", "queue_token_invalid"
	}
	if p.Exp <= now.Unix() {
		return p.E, "queue_token_expired"
	}
	return p.E, ""
}
