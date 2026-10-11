package queue

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	mrand "math/rand/v2"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	redis "github.com/redis/go-redis/v9"

	"github.com/mrmacsi/ticketing-autoscale-lab/waiting-room/token"
)

// RedisOptions configures the redis store.
type RedisOptions struct {
	Prefix   string // key prefix, default "wr:"
	Instance string // this instance's name in stats and the leader lease, default the hostname
	// Elect runs leader election. Without it this instance always leads (one instance, or tests that
	// call Tick and Control themselves).
	Elect      bool
	LeaseTTL   time.Duration // leader lease, default 5 s
	RenewEvery time.Duration // lease renewal / acquisition attempts, default 1 s
	Recheck    time.Duration // long-poll fallback re-check if a wake-up message is missed, default 1 s
	OpTimeout  time.Duration // per Redis call, default 2 s
	Now        func() time.Time
	Log        *slog.Logger
}

// RedisRoom is the waiting room with its state in Redis, shared by any number of instances
// (docs/waiting-room.md, "High availability"). Safe for concurrent use.
type RedisRoom struct {
	rdb redis.UniversalClient
	// lease is a separate small connection pool for the leader lease, so renewals never queue behind
	// customer traffic in the main pool.
	lease     redis.UniversalClient
	ownLease  bool
	lastRenew atomic.Int64 // wall-clock Unix ms of the last successful acquire/renew
	signer    *token.Signer
	opt       RedisOptions
	now       func() time.Time
	log       *slog.Logger
	keys      []string
	lead      string // leader lease key
	chann     string // pub/sub channel
	fence     string // lease value when electing, "" otherwise

	leading atomic.Bool
	closed  chan struct{}
	once    sync.Once
	cancel  context.CancelFunc
	wg      sync.WaitGroup
	pubsub  *redis.PubSub

	wmu     sync.Mutex
	waiters map[string]*waiter

	cmu      sync.Mutex
	cfgCache Config

	emu    sync.Mutex
	errLog map[string]string
}

type waiter struct {
	ch chan struct{}
	n  int
}

// Key names, in the KEYS order every script expects.
var redisKeyNames = []string{"config", "seq", "line", "seen", "entries", "customers", "active", "finished",
	"counters", "state", "rate", "waits", "signals", "capacity", "capacity:names", "leader"}

// NewRedis connects the store to rdb, subscribes to wake-up messages and, with Elect, starts leader
// election. Call Close when done (it does not close rdb).
func NewRedis(ctx context.Context, rdb redis.UniversalClient, signer *token.Signer, opt RedisOptions) (*RedisRoom, error) {
	if opt.Prefix == "" {
		opt.Prefix = "wr:"
	}
	if opt.Instance == "" {
		opt.Instance = Hostname()
	}
	if opt.LeaseTTL <= 0 {
		opt.LeaseTTL = 5 * time.Second
	}
	if opt.RenewEvery <= 0 {
		opt.RenewEvery = time.Second
	}
	if opt.Recheck <= 0 {
		opt.Recheck = time.Second
	}
	if opt.OpTimeout <= 0 {
		opt.OpTimeout = 2 * time.Second
	}
	if opt.Now == nil {
		opt.Now = time.Now
	}
	if opt.Log == nil {
		opt.Log = slog.New(slog.DiscardHandler)
	}
	r := &RedisRoom{rdb: rdb, signer: signer, opt: opt, now: opt.Now, log: opt.Log, closed: make(chan struct{}),
		waiters: map[string]*waiter{}, cfgCache: DefaultConfig(), errLog: map[string]string{}}
	for _, k := range redisKeyNames {
		r.keys = append(r.keys, opt.Prefix+k)
	}
	r.lead = opt.Prefix + "leader"
	r.chann = opt.Prefix + "events"
	r.lease = rdb
	if c, ok := rdb.(*redis.Client); ok && opt.Elect {
		o := *c.Options()
		o.PoolSize, o.MinIdleConns = 2, 1
		r.lease, r.ownLease = redis.NewClient(&o), true
	}
	if opt.Elect {
		var b [4]byte
		_, _ = rand.Read(b[:])
		r.fence = opt.Instance + "/" + hex.EncodeToString(b[:])
	} else {
		r.leading.Store(true)
	}
	if err := rdb.Ping(ctx).Err(); err != nil {
		return nil, fmt.Errorf("redis: %w", err)
	}
	r.pubsub = rdb.Subscribe(ctx, r.chann)
	if _, err := r.pubsub.Receive(ctx); err != nil { // wait for the subscription to be confirmed
		_ = r.pubsub.Close()
		r.closeLease()
		return nil, fmt.Errorf("redis subscribe: %w", err)
	}
	bg, cancel := context.WithCancel(context.Background())
	r.cancel = cancel
	r.wg.Add(1)
	go r.listen()
	if opt.Elect {
		r.campaign(bg)
		r.wg.Add(1)
		go r.elect(bg)
	}
	return r, nil
}

func (r *RedisRoom) Kind() string  { return StoreRedis }
func (r *RedisRoom) Leading() bool { return r.leading.Load() }

// Instance is this instance's name.
func (r *RedisRoom) Instance() string { return r.opt.Instance }

// Close wakes every long poll on this instance, hands leadership over and stops background work.
func (r *RedisRoom) Close() {
	r.once.Do(func() {
		close(r.closed)
		r.cancel()
		if r.opt.Elect && r.leading.Swap(false) {
			ctx, cancel := context.WithTimeout(context.Background(), r.opt.OpTimeout)
			_ = luaResign.Run(ctx, r.lease, []string{r.lead}, r.fence).Err()
			cancel()
			r.log.Info("leadership released", "instance", r.opt.Instance)
		}
		_ = r.pubsub.Close()
		r.wg.Wait()
		r.closeLease()
	})
}

func (r *RedisRoom) closeLease() {
	if r.ownLease {
		_ = r.lease.Close()
	}
}

// crash stops background work without handing leadership over (tests: a killed instance).
func (r *RedisRoom) crash() {
	r.once.Do(func() {
		r.leading.Store(false)
		close(r.closed)
		r.cancel()
		_ = r.pubsub.Close()
		r.wg.Wait()
		r.closeLease()
	})
}

// ---- leader election ----

func (r *RedisRoom) elect(ctx context.Context) {
	defer r.wg.Done()
	t := time.NewTicker(r.opt.RenewEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			r.campaign(ctx)
		}
	}
}

// campaign renews the lease while leading, or tries to take it. One slow or failed renewal does not
// end leadership: the instance keeps leading while the lease it last renewed is certainly still
// valid (LeaseTTL - RenewEvery after that renewal), and every leader-side script is fenced anyway.
// It steps down only when Redis says the lease belongs to someone else, or when it could not renew
// for that long.
func (r *RedisRoom) campaign(ctx context.Context) {
	ctx, cancel := context.WithTimeout(ctx, min(r.opt.OpTimeout, r.opt.RenewEvery))
	defer cancel()
	if r.leading.Load() {
		n, err := luaRenew.Run(ctx, r.lease, []string{r.lead}, r.fence, r.opt.LeaseTTL.Milliseconds()).Int()
		r.noteErr("lease", err)
		switch {
		case err == nil && n == 1:
			r.lastRenew.Store(time.Now().UnixMilli())
			return
		case err != nil && time.Since(time.UnixMilli(r.lastRenew.Load())) < r.opt.LeaseTTL-r.opt.RenewEvery:
			return // unknown outcome, lease still valid: try again next interval
		}
		if r.leading.Swap(false) {
			r.log.Warn("leadership lost", "instance", r.opt.Instance, "err", err)
		}
		return
	}
	ok, err := r.lease.SetNX(ctx, r.lead, r.fence, r.opt.LeaseTTL).Result()
	r.noteErr("lease", err)
	if err == nil && ok {
		r.lastRenew.Store(time.Now().UnixMilli())
		r.leading.Store(true)
		r.log.Info("became leader", "instance", r.opt.Instance)
	}
}

// ---- wake-ups ----

func (r *RedisRoom) listen() {
	defer r.wg.Done()
	for msg := range r.pubsub.Channel() {
		switch p := msg.Payload; {
		case p == "*":
			r.wakeAll()
		case strings.HasPrefix(p, "w "):
			r.wake(strings.Fields(p[2:]))
		}
	}
	r.wakeAll()
}

func (r *RedisRoom) register(id string) *waiter {
	r.wmu.Lock()
	defer r.wmu.Unlock()
	w := r.waiters[id]
	if w == nil {
		w = &waiter{ch: make(chan struct{})}
		r.waiters[id] = w
	}
	w.n++
	return w
}

func (r *RedisRoom) unregister(id string, w *waiter) {
	r.wmu.Lock()
	defer r.wmu.Unlock()
	w.n--
	if w.n == 0 && r.waiters[id] == w {
		delete(r.waiters, id)
	}
}

func (r *RedisRoom) wake(ids []string) {
	r.wmu.Lock()
	defer r.wmu.Unlock()
	for _, id := range ids {
		if w := r.waiters[id]; w != nil {
			close(w.ch)
			delete(r.waiters, id)
		}
	}
}

func (r *RedisRoom) wakeAll() {
	r.wmu.Lock()
	defer r.wmu.Unlock()
	for id, w := range r.waiters {
		close(w.ch)
		delete(r.waiters, id)
	}
}

// ---- helpers ----

func (r *RedisRoom) ctx(parent context.Context) (context.Context, context.CancelFunc) {
	if parent == nil {
		parent = context.Background()
	}
	return context.WithTimeout(parent, r.opt.OpTimeout)
}

// run executes a script with the standard KEYS and ARGV[1..2].
func (r *RedisRoom) run(ctx context.Context, s *redis.Script, fenced bool, args ...any) *redis.Cmd {
	fence := ""
	if fenced {
		fence = r.fence
	}
	return s.Run(ctx, r.rdb, r.keys, append([]any{r.chann, fence}, args...)...)
}

// noteErr logs an operation's error when it changes (and its recovery), so a Redis outage logs once.
func (r *RedisRoom) noteErr(op string, err error) {
	msg := ""
	if err != nil {
		msg = err.Error()
	}
	r.emu.Lock()
	prev := r.errLog[op]
	r.errLog[op] = msg
	r.emu.Unlock()
	switch {
	case msg == prev:
	case msg == "":
		r.log.Info("redis recovered", "op", op)
	default:
		r.log.Warn("redis error", "op", op, "err", msg)
	}
}

func slot(t time.Time) int64 { return t.UnixMilli() / 100 }

func asInt(v any) int64 {
	switch x := v.(type) {
	case int64:
		return x
	case string:
		n, _ := strconv.ParseInt(x, 10, 64)
		return n
	}
	return 0
}

func asString(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case int64:
		return strconv.FormatInt(x, 10)
	}
	return ""
}

func asMap(v any) map[string]string {
	a, _ := v.([]any)
	m := make(map[string]string, len(a)/2)
	for i := 0; i+1 < len(a); i += 2 {
		m[asString(a[i])] = asString(a[i+1])
	}
	return m
}

func msTime(ms int64) time.Time {
	if ms == 0 {
		return time.Time{}
	}
	return time.UnixMilli(ms).UTC()
}

func ms(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.UnixMilli()
}

// toView renders a script's {id, state, position, waiting, expMs, recent admissions, eventId}.
func (r *RedisRoom) toView(a []any) View {
	id, st, ev := asString(a[0]), asString(a[1]), asString(a[6])
	v := View{QueueID: id, EventID: ev, Waiting: int(asInt(a[3])), EstimatedWaitSeconds: zero()}
	switch st {
	case "w":
		v.Status = StatusWaiting
		v.Position = asInt(a[2])
		v.PollAfterMs = 1000
		v.EstimatedWaitSeconds = nil
		if rate := float64(asInt(a[5])) / 10; rate > 0 {
			s := int64(math.Ceil(float64(v.Position) / rate))
			v.EstimatedWaitSeconds = &s
		}
	case "a", "r", "x":
		exp := asInt(a[4]) / 1000
		v.Status = StatusAdmitted
		v.Token = r.signer.Sign(token.Payload{Q: id, E: ev, Exp: exp})
		v.TokenExpiresAt = FormatTime(time.Unix(exp, 0))
	case "s":
		v.Status = StatusSoldOut
	}
	return v
}

// ---- configuration ----

// Config returns the configuration stored in Redis (the last value read if Redis is unreachable).
func (r *RedisRoom) Config() Config {
	ctx, cancel := r.ctx(nil)
	defer cancel()
	raw, err := r.rdb.HGet(ctx, r.keys[0], "json").Result()
	r.cmu.Lock()
	defer r.cmu.Unlock()
	switch {
	case errors.Is(err, redis.Nil):
		r.cfgCache = DefaultConfig()
	case err != nil:
		r.noteErr("config", err)
	default:
		var c Config
		if json.Unmarshal([]byte(raw), &c) == nil {
			r.cfgCache = c
		}
	}
	return r.cfgCache
}

// Configure stores the configuration for every instance. A different eventId, or enabled=false,
// clears the line, the active set and the counters (waiters everywhere are woken and find their id gone).
func (r *RedisRoom) Configure(c Config) (Config, error) {
	if c.AdmissionMode == "" {
		c.AdmissionMode = ModeFixed
	}
	if c.Adaptive == (AdaptiveConfig{}) {
		c.Adaptive = DefaultAdaptiveConfig()
	}
	raw, err := json.Marshal(c)
	if err != nil {
		return Config{}, err
	}
	enabled := "0"
	if c.Enabled {
		enabled = "1"
	}
	ctx, cancel := r.ctx(nil)
	defer cancel()
	if err := r.run(ctx, luaConfigure, false, string(raw), enabled, c.EventID, c.ActiveUsersPerServer,
		c.AdmissionTTLSeconds, c.SoldOutGraceSeconds, c.AdmissionMode, c.WaitingTimeoutSeconds, startLimit(c)).Err(); err != nil {
		return Config{}, err
	}
	r.cmu.Lock()
	r.cfgCache = c
	r.cmu.Unlock()
	return c, nil
}

// Reset clears queue, active set and counters, keeping the configuration.
func (r *RedisRoom) Reset() error {
	ctx, cancel := r.ctx(nil)
	defer cancel()
	return r.run(ctx, luaReset, false).Err()
}

// ---- customers ----

// Join adds a customer to the line (or returns their existing entry while it is waiting/admitted).
func (r *RedisRoom) Join(eventID, customerRef string) (View, error) {
	id := NewQueueID()
	now := r.now()
	ctx, cancel := r.ctx(nil)
	defer cancel()
	res, err := r.run(ctx, luaJoin, false, ms(now), id, strings.ToLower(eventID), customerRef, slot(now)).Slice()
	if err != nil {
		return View{}, err
	}
	switch asString(res[0]) {
	case "open":
		return View{QueueID: id, EventID: eventID, Status: StatusOpen, EstimatedWaitSeconds: zero()}, nil
	case "unknown_event":
		return View{}, ErrUnknownEvent
	}
	return r.toView(res), nil
}

// status reads an entry; touch records a check-in (keeps a waiting customer in line).
func (r *RedisRoom) status(ctx context.Context, id string, touch bool) (View, string, error) {
	now := r.now()
	t := "0"
	if touch {
		t = "1"
	}
	cctx, cancel := r.ctx(ctx)
	defer cancel()
	res, err := r.run(cctx, luaStatus, false, ms(now), id, t, slot(now)).Slice()
	if err != nil {
		if ctx.Err() != nil {
			return View{}, "", ctx.Err()
		}
		return View{}, "", err
	}
	if asString(res[0]) == "unknown" {
		return View{}, "", ErrUnknownQueueID
	}
	return r.toView(res), asString(res[1]), nil
}

// Status returns the entry's status. With wait > 0 and the entry waiting, it blocks until a wake-up
// message for the entry arrives (published by whichever instance changed it), a periodic re-check
// finds it no longer waiting, wait elapses, ctx is done (returns ctx.Err()) or the room closes.
func (r *RedisRoom) Status(ctx context.Context, id string, wait time.Duration) (View, error) {
	v, st, err := r.status(ctx, id, true)
	if err != nil || st != "w" || wait <= 0 {
		return v, err
	}
	w := r.register(id)
	defer r.unregister(id, w)
	// Re-check after registering so a wake-up published in between is not missed.
	if v, st, err := r.status(ctx, id, false); err != nil || st != "w" {
		return v, err
	}
	timer := time.NewTimer(wait)
	defer timer.Stop()
	recheck := time.NewTicker(r.opt.Recheck)
	defer recheck.Stop()
loop:
	for {
		select {
		case <-w.ch:
			break loop
		case <-timer.C:
			break loop
		case <-r.closed:
			break loop
		case <-ctx.Done():
			return View{}, ctx.Err()
		case <-recheck.C:
			if _, st, err := r.status(ctx, id, false); err != nil || st != "w" {
				break loop
			}
		}
	}
	v, _, err = r.status(ctx, id, true)
	return v, err
}

// Release frees an admitted customer's slot, or removes a waiting customer from the line.
func (r *RedisRoom) Release(id, outcome string) (bool, error) {
	ctx, cancel := r.ctx(nil)
	defer cancel()
	n, err := r.run(ctx, luaRelease, false, ms(r.now()), id, outcome).Int()
	return n == 1, err
}

// ---- leader inputs and steps ----

// storedServer keeps the base URL that Server leaves out of JSON (the leader's pollers need it).
type storedServer struct {
	Name    string `json:"name"`
	IP      string `json:"ip"`
	BaseURL string `json:"baseUrl"`
}

func encodeServers(s []Server) string {
	out := make([]storedServer, len(s))
	for i, v := range s {
		out[i] = storedServer(v)
	}
	b, _ := json.Marshal(out)
	return string(b)
}

func decodeServers(raw string) []Server {
	var in []storedServer
	_ = json.Unmarshal([]byte(raw), &in)
	out := make([]Server, len(in))
	for i, v := range in {
		out[i] = Server(v)
	}
	return out
}

// SetCapacity records the latest capacity observation (leader only).
func (r *RedisRoom) SetCapacity(u CapacityUpdate) {
	if !r.Leading() {
		return
	}
	ctx, cancel := r.ctx(nil)
	defer cancel()
	args := []any{max(u.Healthy, 0), u.Source, u.Err, encodeServers(u.Servers)}
	for _, s := range u.Servers {
		args = append(args, s.Name)
	}
	r.noteErr("capacity", r.run(ctx, luaSetCapacity, true, args...).Err())
}

// HealthyServers returns the servers of the latest capacity observation.
func (r *RedisRoom) HealthyServers() []Server {
	ctx, cancel := r.ctx(nil)
	defer cancel()
	raw, err := r.rdb.HGet(ctx, r.keys[13], "servers").Result()
	if err != nil {
		if !errors.Is(err, redis.Nil) {
			r.noteErr("servers", err)
		}
		return nil
	}
	return decodeServers(raw)
}

// storedSignals keeps the observation time that Signals leaves out of JSON.
type storedSignals struct {
	Signals
	At int64 `json:"at"`
}

// SetSignals records a load observation for a healthy server (leader only).
func (r *RedisRoom) SetSignals(name string, s Signals) {
	if !r.Leading() {
		return
	}
	b, _ := json.Marshal(storedSignals{Signals: s, At: ms(s.observedAt)})
	ctx, cancel := r.ctx(nil)
	defer cancel()
	r.noteErr("signals", r.run(ctx, luaSetSignals, true, name, string(b)).Err())
}

func decodeSignals(raw string) *Signals {
	var s storedSignals
	if json.Unmarshal([]byte(raw), &s) != nil {
		return nil
	}
	s.Signals.observedAt = msTime(s.At)
	return &s.Signals
}

// SetInventory records availability for eventID (leader only; ignored if the room moved on).
func (r *RedisRoom) SetInventory(eventID string, available, held int, appSoldOut bool, at time.Time) {
	if !r.Leading() {
		return
	}
	so := "0"
	if appSoldOut {
		so = "1"
	}
	ctx, cancel := r.ctx(nil)
	defer cancel()
	r.noteErr("inventory", r.run(ctx, luaSetInventory, true, eventID, available, held, so, ms(at)).Err())
}

// SetInventoryUnknown marks inventory unknown (leader only).
func (r *RedisRoom) SetInventoryUnknown(eventID, reason string) {
	if !r.Leading() {
		return
	}
	ctx, cancel := r.ctx(nil)
	defer cancel()
	r.noteErr("inventory", r.run(ctx, luaSetInventoryUnknown, true, eventID, reason).Err())
}

// Tick runs one admission step in one atomic script (leader only).
func (r *RedisRoom) Tick() {
	if !r.Leading() {
		return
	}
	now := r.now()
	ctx, cancel := r.ctx(nil)
	defer cancel()
	res, err := r.run(ctx, luaTick, true, ms(now), slot(now), 1+mrand.Int64N(2147483646)).Slice()
	r.noteErr("tick", err)
	if err == nil && asInt(res[0]) == -1 && r.opt.Elect && r.leading.Swap(false) {
		r.log.Warn("leadership lost (lease held by another instance)", "instance", r.opt.Instance)
	}
}

// Run calls Tick every interval until ctx is done (Tick does nothing while following).
func (r *RedisRoom) Run(ctx context.Context, interval time.Duration) { every(ctx, interval, r.Tick) }

// RunControl calls Control every interval until ctx is done.
func (r *RedisRoom) RunControl(ctx context.Context, interval time.Duration) {
	every(ctx, interval, r.Control)
}

func every(ctx context.Context, interval time.Duration, f func()) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			f()
		}
	}
}

// snapshot is everything Stats and Control read, taken atomically.
type snapshot struct {
	cfg           Config
	gen           string
	st, cap, cnt  map[string]string
	waiting       int
	active        int
	oldestJoined  int64
	rate          map[string]string
	sig           map[string]string
	waits         []any
	leader        string
	servers       []Server
	healthy       int
	ctl           controller
	signalsByName map[string]*Signals
}

func (r *RedisRoom) snapshot(withWaits bool) (*snapshot, error) {
	ctx, cancel := r.ctx(nil)
	defer cancel()
	w := "0"
	if withWaits {
		w = "1"
	}
	res, err := r.run(ctx, luaSnapshot, false, w).Slice()
	if err != nil {
		return nil, err
	}
	s := &snapshot{cfg: DefaultConfig(), gen: asString(res[1]), st: asMap(res[2]), cap: asMap(res[3]), cnt: asMap(res[4]),
		waiting: int(asInt(res[5])), active: int(asInt(res[6])), oldestJoined: asInt(res[7]), rate: asMap(res[8]),
		sig: asMap(res[9]), leader: asString(res[11])}
	s.waits, _ = res[10].([]any)
	if raw := asString(res[0]); raw != "" {
		_ = json.Unmarshal([]byte(raw), &s.cfg)
	}
	s.servers = decodeServers(s.cap["servers"])
	s.healthy = int(asInt(s.cap["healthy"]))
	s.signalsByName = map[string]*Signals{}
	for name, raw := range s.sig {
		if sig := decodeSignals(raw); sig != nil {
			s.signalsByName[name] = sig
		}
	}
	n := func(k string) int64 { return asInt(s.st[k]) }
	s.ctl = controller{limit: int(n("ctlLimit")), phase: s.st["ctlPhase"], decision: s.st["ctlDecision"], reason: s.st["ctlReason"],
		decisionAt: msTime(n("ctlDecisionAt")), lastDecrease: msTime(n("ctlLastDecrease")), decreases: n("ctlDecreases")}
	return s, nil
}

func (s *snapshot) signal(name string) *Signals { return s.signalsByName[name] }

// Control runs one step of the shared adaptive controller (leader only).
func (r *RedisRoom) Control() {
	if !r.Leading() {
		return
	}
	now := r.now()
	s, err := r.snapshot(false)
	r.noteErr("control", err)
	if err != nil || !s.cfg.Enabled || s.cfg.AdmissionMode != ModeAdaptive {
		return
	}
	c := s.ctl
	c.control(s.cfg, s.servers, s.signal, s.active, s.healthy, now)
	ctx, cancel := r.ctx(nil)
	defer cancel()
	err = r.run(ctx, luaSetController, true, s.gen,
		"ctlLimit", c.limit, "ctlPhase", c.phase, "ctlDecision", c.decision, "ctlReason", c.reason,
		"ctlDecisionAt", ms(c.decisionAt), "ctlLastDecrease", ms(c.lastDecrease), "ctlDecreases", c.decreases).Err()
	r.noteErr("control", err)
}

// ---- stats ----

// Stats returns a snapshot; every instance reports the same numbers.
func (r *RedisRoom) Stats() (Stats, error) {
	now := r.now()
	s, err := r.snapshot(true)
	if err != nil {
		return Stats{}, err
	}
	cfg := s.cfg
	ctl := s.ctl
	cnt := func(k string) int64 { return asInt(s.cnt[k]) }
	st := func(k string) int64 { return asInt(s.st[k]) }
	out := Stats{
		Enabled:               cfg.Enabled,
		SoldOut:               s.st["soldOut"] == "1",
		Waiting:               s.waiting,
		Active:                s.active,
		Capacity:              capacityFor(cfg, s.healthy, &ctl),
		HealthyServers:        s.healthy,
		Servers:               serverViews(cfg, s.servers, &ctl, s.signal),
		ActiveUsersPerServer:  cfg.ActiveUsersPerServer,
		JoinedTotal:           cnt("joined"),
		AdmittedTotal:         cnt("admitted"),
		ReleasedTotal:         cnt("released"),
		ExpiredTotal:          cnt("expired"),
		SoldOutTotal:          cnt("soldOut"),
		CapacitySource:        s.cap["source"],
		UpdatedAt:             FormatTime(now),
		AdmissionTTLSeconds:   cfg.AdmissionTTLSeconds,
		SoldOutGraceSeconds:   cfg.SoldOutGraceSeconds,
		LeftWaitingTotal:      cnt("leftWaiting"),
		ReleasedByOutcome:     map[string]int64{},
		AdmissionMode:         cfg.AdmissionMode,
		AbandonedTotal:        cnt("abandoned"),
		WaitingTimeoutSeconds: cfg.WaitingTimeoutSeconds,
		Adaptive:              cfg.Adaptive,
		Store:                 StoreRedis,
		Instance:              r.opt.Instance,
	}
	if cfg.EventID != "" {
		id := cfg.EventID
		out.EventID = &id
	}
	// Admission rate over the last 10 s (100 ms slots), as in the memory room.
	cur := slot(now)
	var n int64
	for k, v := range s.rate {
		if d := cur - asInt(k); d >= 0 && d < 100 {
			n += asInt(v)
		}
	}
	out.AdmissionRatePerSecond = math.Round(float64(n)/10*10) / 10
	if len(s.waits) > 0 {
		samples := make([]int64, len(s.waits))
		for i, v := range s.waits {
			samples[i] = asInt(v)
		}
		slices.Sort(samples)
		out.WaitMs = WaitMs{P50: nearestRank(samples, 0.50), P95: nearestRank(samples, 0.95), Max: cnt("waitMax"), Count: cnt("waitCount")}
	}
	if s.waiting > 0 && s.oldestJoined > 0 {
		out.OldestWaitingSeconds = math.Round(now.Sub(time.UnixMilli(s.oldestJoined)).Seconds()*10) / 10
	}
	if s.st["invKnown"] == "1" {
		out.Inventory = &InventoryView{Available: int(st("invAvail")), SoldOut: s.st["invAppSoldOut"] == "1",
			ObservedAt: FormatTime(time.UnixMilli(st("invObservedAt")))}
		if h := int(st("invHeld")); h >= 0 {
			out.Inventory.Held = &h
		}
	}
	if e := s.cap["err"]; e != "" {
		out.CapacityError = &e
	}
	if e := s.st["invErr"]; e != "" {
		out.InventoryError = &e
	}
	if z := st("zeroSince"); z != 0 {
		t := FormatTime(time.UnixMilli(z))
		out.InventoryZeroSince = &t
	}
	for k, v := range s.cnt {
		if o, ok := strings.CutPrefix(k, "rel:"); ok {
			out.ReleasedByOutcome[o] = asInt(v)
		}
	}
	if r.opt.Elect {
		if s.leader != "" {
			name, _, _ := strings.Cut(s.leader, "/")
			out.LeaderInstance = &name
		}
		out.Leader = s.leader == r.fence
	} else {
		name := r.opt.Instance
		out.LeaderInstance, out.Leader = &name, true
	}
	return out, nil
}
