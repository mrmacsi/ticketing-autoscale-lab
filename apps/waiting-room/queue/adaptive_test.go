package queue

import (
	"fmt"
	"testing"
	"time"
)

// newAdaptiveRoom returns an enabled adaptive room with the named healthy servers.
func newAdaptiveRoom(t *testing.T, start int, names ...string) (*Room, *clock) {
	t.Helper()
	c := &clock{t: time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)}
	r := New(signer, c.Now)
	cfg := DefaultConfig()
	cfg.Enabled, cfg.EventID, cfg.ActiveUsersPerServer, cfg.AdmissionMode = true, ev, start, ModeAdaptive
	r.Configure(cfg)
	setServers(r, names...)
	return r, c
}

func setServers(r *Room, names ...string) {
	var srv []Server
	for _, n := range names {
		srv = append(srv, Server{Name: n, IP: "10.0.0." + n, BaseURL: "http://" + n})
	}
	r.SetCapacity(CapacityUpdate{Healthy: len(names), Servers: srv, Source: "static"})
}

// signal records a load observation: busy workers out of 32, listen queue, CPU percent (<0 unknown).
func signal(r *Room, c *clock, name string, active, queue int, cpuPct float64) {
	cpu := -1.0
	if cpuPct >= 0 {
		cpu = cpuPct / 100
	}
	r.SetSignals(name, NewSignals(active, 32, queue, cpu, "", c.Now()))
}

func limitOf(t *testing.T, r *Room, name string) ServerView {
	t.Helper()
	for _, s := range r.Stats().Servers {
		if s.Name == name {
			return s
		}
	}
	t.Fatalf("server %s not in stats", name)
	return ServerView{}
}

// step runs the 100 ms admission loop and then one controller step, as the room does in production.
func step(r *Room) {
	r.Tick()
	r.Control()
}

func fill(t *testing.T, r *Room, n int) {
	t.Helper()
	for i := range n {
		join(t, r, fmt.Sprintf("demand-%d-%d", time.Now().UnixNano(), i))
	}
}

func TestAdaptiveSlowStartThenAIMD(t *testing.T) {
	r, c := newAdaptiveRoom(t, 20, "a")
	fill(t, r, 500) // plenty of demand
	if s := r.Stats(); s.Capacity != 20 || s.AdmissionMode != ModeAdaptive {
		t.Fatalf("starting capacity %d mode %s", s.Capacity, s.AdmissionMode)
	}
	// Headroom: slow start multiplies by 1.25 per step (20 → 25 → 32).
	for _, want := range []int{25, 32} {
		c.Add(time.Second)
		signal(r, c, "a", 10, 0, 40)
		step(r)
		if v := limitOf(t, r, "a"); v.Limit != want || v.Phase != PhaseSlowStart || v.Decision != DecisionIncrease {
			t.Fatalf("slow start: %+v, want %d", v, want)
		}
	}
	// Requests queue for a worker: cut ×0.75 (32 → 24) and leave slow start.
	c.Add(time.Second)
	signal(r, c, "a", 32, 4, 70)
	step(r)
	v := limitOf(t, r, "a")
	if v.Limit != 24 || v.Phase != PhaseSteady || v.Decision != DecisionDecrease || v.Reason != "listen queue 4 on a" || v.Decreases != 1 {
		t.Fatalf("cut: %+v", v)
	}
	// Still overloaded 1 s later: hold (cooldown 3 s) instead of cutting again.
	c.Add(time.Second)
	signal(r, c, "a", 32, 2, 70)
	step(r)
	if v := limitOf(t, r, "a"); v.Limit != 24 || v.Decision != DecisionHold {
		t.Fatalf("cooldown: %+v", v)
	}
	// After the cooldown, a second cut.
	c.Add(3 * time.Second)
	signal(r, c, "a", 30, 0, 70) // busy 94 %
	step(r)
	if v := limitOf(t, r, "a"); v.Limit != 18 || v.Reason != "workers busy 94% on a" {
		t.Fatalf("second cut: %+v", v)
	}
	// Steady phase grows additively (+1) once customers finish and the room is full again.
	c.Add(time.Second)
	r.SetCapacity(CapacityUpdate{Healthy: 1, Source: "static", Servers: []Server{{Name: "a"}}})
	drain(r, 0)
	signal(r, c, "a", 12, 0, 50)
	step(r)
	if v := limitOf(t, r, "a"); v.Limit != 19 || v.Phase != PhaseSteady {
		t.Fatalf("additive increase: %+v", v)
	}
	// Between the thresholds (busy 75 %): hold.
	c.Add(time.Second)
	signal(r, c, "a", 24, 0, 50)
	step(r)
	if v := limitOf(t, r, "a"); v.Limit != 19 || v.Decision != DecisionHold {
		t.Fatalf("hold near limit: %+v", v)
	}
	// CPU alone can trigger a cut.
	c.Add(4 * time.Second)
	signal(r, c, "a", 10, 0, 95)
	step(r)
	if v := limitOf(t, r, "a"); v.Limit != 14 || v.Reason != "CPU 95% on a" {
		t.Fatalf("cpu cut: %+v", v)
	}
}

// drain releases admitted customers until at most keep remain (they finished checkout).
func drain(r *Room, keep int) {
	r.mu.Lock()
	var ids []string
	for id := range r.active {
		ids = append(ids, id)
	}
	r.mu.Unlock()
	for i := 0; i < len(ids)-keep; i++ {
		r.Release(ids[i], "purchased")
	}
}

func TestLimitDoesNotGrowWhileAdmissionIsBlockedByInventory(t *testing.T) {
	r, c := newAdaptiveRoom(t, 20, "a")
	fill(t, r, 300)
	r.SetInventory(ev, 0, 40, true, c.Now()) // all tickets sold or held: nobody can be admitted
	for range 10 {
		c.Add(time.Second)
		signal(r, c, "a", 1, 0, 10) // idle server, people waiting
		r.Tick()
		r.Control()
	}
	if v := limitOf(t, r, "a"); v.Limit != 20 || v.Reason != "capacity not in use" {
		t.Fatalf("limit inflated while blocked by inventory: %+v", v)
	}
}

func TestAdaptiveGuards(t *testing.T) {
	r, c := newAdaptiveRoom(t, 20, "a")
	// No demand: an idle server does not inflate the limit.
	signal(r, c, "a", 0, 0, 5)
	step(r)
	if v := limitOf(t, r, "a"); v.Limit != 20 || v.Reason != "capacity not in use" {
		t.Fatalf("idle growth: %+v", v)
	}
	fill(t, r, 100)
	// Stale signals: hold.
	c.Add(5 * time.Second)
	step(r)
	if v := limitOf(t, r, "a"); v.Limit != 20 || v.Reason != "no signal" {
		t.Fatalf("stale: %+v", v)
	}
	// Unknown CPU does not block growth when the other signals show headroom.
	signal(r, c, "a", 5, 0, -1)
	step(r)
	if v := limitOf(t, r, "a"); v.Limit != 25 {
		t.Fatalf("unknown cpu: %+v", v)
	}
	// Bounds: never below min (5), never above max.
	cfg := r.Config()
	cfg.Adaptive.MaxPerServer = 30
	r.Configure(cfg)
	for range 5 {
		c.Add(time.Second)
		signal(r, c, "a", 5, 0, 10)
		drain(r, 0)
		fill(t, r, 40)
		step(r)
	}
	if v := limitOf(t, r, "a"); v.Limit != 30 || v.Reason != "at maximum" {
		t.Fatalf("max bound: %+v", v)
	}
	for range 12 {
		c.Add(4 * time.Second)
		signal(r, c, "a", 32, 9, 99)
		step(r)
	}
	if v := limitOf(t, r, "a"); v.Limit != 5 {
		t.Fatalf("min bound: %+v", v)
	}
}

func TestSharedLimitFollowsTheBusiestServer(t *testing.T) {
	r, c := newAdaptiveRoom(t, 20, "app-a", "app-b")
	fill(t, r, 500)
	// Both have headroom: the shared limit grows and capacity = 2 × limit.
	c.Add(time.Second)
	signal(r, c, "app-a", 5, 0, 40)
	signal(r, c, "app-b", 6, 0, 45)
	step(r)
	if s := r.Stats(); s.Capacity != 50 || s.Servers[0].Limit != 25 || s.Servers[1].Limit != 25 {
		t.Fatalf("grow together: capacity %d %+v", s.Capacity, s.Servers)
	}
	// One server is overloaded (the LB gives both the same share): the shared limit is cut, and the
	// reason names the busy server.
	c.Add(time.Second)
	signal(r, c, "app-a", 5, 0, 40)
	signal(r, c, "app-b", 32, 3, 95)
	step(r)
	if v := limitOf(t, r, "app-a"); v.Limit != 18 || v.Reason != "listen queue 3 on b" {
		t.Fatalf("busiest server rules: %+v", v)
	}
	// A server without fresh signals blocks growth (it might be the one in trouble).
	c.Add(5 * time.Second)
	signal(r, c, "app-a", 2, 0, 20)
	drain(r, 0)
	fill(t, r, 100)
	step(r)
	if v := limitOf(t, r, "app-a"); v.Limit != 18 || v.Reason != "no signal from b" {
		t.Fatalf("missing signal: %+v", v)
	}
	// A new server joins at the learned limit: capacity grows by 18 at once, no slow start overshoot.
	r.SetCapacity(CapacityUpdate{Healthy: 3, Source: "static", Servers: []Server{{Name: "app-a"}, {Name: "app-b"}, {Name: "app-c"}}})
	if s := r.Stats(); s.Capacity != 54 || s.Servers[2].Limit != 18 || s.Servers[2].Phase != PhaseSteady {
		t.Fatalf("join at learned limit: capacity %d %+v", s.Capacity, s.Servers[2])
	}
	// A server that leaves takes its share of the capacity with it; the learned limit stays.
	r.SetCapacity(CapacityUpdate{Healthy: 1, Source: "static", Servers: []Server{{Name: "app-a"}}})
	if s := r.Stats(); s.Capacity != 18 || len(s.Servers) != 1 {
		t.Fatalf("leave: capacity %d", s.Capacity)
	}
	// A cut never evicts anyone: it only stops admissions until customers finish.
	r.Tick()
	before := r.Stats()
	if before.Active < before.Capacity {
		t.Fatalf("expected a full room: %+v", before.Active)
	}
	c.Add(4 * time.Second)
	signal(r, c, "app-a", 32, 2, 95)
	step(r)
	after := r.Stats()
	if after.Capacity != 13 || after.Active != before.Active || after.AdmittedTotal != before.AdmittedTotal {
		t.Fatalf("cut evicted or admitted: before %d/%d after %d/%d", before.Active, before.Capacity, after.Active, after.Capacity)
	}
}

func TestFixedModeUnchangedAndShowsSignals(t *testing.T) {
	r, c := newRoom(t, 2, 25)
	setServers(r, "a", "b")
	signal(r, c, "a", 31, 3, 90)
	r.Control() // no-op in fixed mode
	s := r.Stats()
	if s.Capacity != 50 || s.AdmissionMode != ModeFixed {
		t.Fatalf("fixed capacity %d mode %q", s.Capacity, s.AdmissionMode)
	}
	if v := limitOf(t, r, "a"); v.Limit != 25 || v.Phase != PhaseFixed || v.Signals == nil || v.Signals.ListenQueue != 3 {
		t.Fatalf("fixed view %+v", v)
	}
}

func TestAdaptiveConfigValidation(t *testing.T) {
	ok := DefaultAdaptiveConfig()
	if msg := ok.Validate(); msg != "" {
		t.Fatal(msg)
	}
	for name, mut := range map[string]func(*AdaptiveConfig){
		"max<min":    func(a *AdaptiveConfig) { a.MaxPerServer = 2; a.MinPerServer = 5 },
		"busy order": func(a *AdaptiveConfig) { a.BusyLow = 0.95 },
		"cpu order":  func(a *AdaptiveConfig) { a.CPULow = 0.95 },
		"decrease":   func(a *AdaptiveConfig) { a.DecreaseFactor = 1 },
		"slow start": func(a *AdaptiveConfig) { a.SlowStartFactor = 1 },
		"cooldown":   func(a *AdaptiveConfig) { a.CooldownSeconds = 61 },
	} {
		a := DefaultAdaptiveConfig()
		mut(&a)
		if a.Validate() == "" {
			t.Errorf("%s accepted", name)
		}
	}
}
