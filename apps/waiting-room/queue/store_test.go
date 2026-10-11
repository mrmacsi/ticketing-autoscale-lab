package queue

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	redis "github.com/redis/go-redis/v9"
)

// The behavioural tests of the room, run against both stores (table-driven over a store factory).
// The memory room keeps its own original tests in room_test.go and adaptive_test.go as well.

type storeFactory struct {
	name string
	new  func(t *testing.T, c *clock) Store
}

var factories = []storeFactory{
	{"memory", func(t *testing.T, c *clock) Store { return Memory(New(signer, c.Now)) }},
	{"redis", func(t *testing.T, c *clock) Store {
		m := miniredis.RunT(t)
		return newRedisRoom(t, m, RedisOptions{Now: c.Now, Instance: "solo"})
	}},
}

func newRedisRoom(t *testing.T, m *miniredis.Miniredis, opt RedisOptions) *RedisRoom {
	t.Helper()
	rdb := redis.NewClient(&redis.Options{Addr: m.Addr()})
	r, err := NewRedis(context.Background(), rdb, signer, opt)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { r.Close(); rdb.Close() })
	return r
}

func forEachStore(t *testing.T, f func(t *testing.T, mk func(c *clock) Store)) {
	for _, fc := range factories {
		t.Run(fc.name, func(t *testing.T) { f(t, func(c *clock) Store { return fc.new(t, c) }) })
	}
}

func newClock() *clock { return &clock{t: time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)} }

// enabled returns an enabled store with healthy × perServer capacity.
func enabled(t *testing.T, mk func(*clock) Store, healthy, perServer int) (Store, *clock) {
	t.Helper()
	c := newClock()
	s := mk(c)
	if _, err := s.Configure(Config{Enabled: true, EventID: ev, ActiveUsersPerServer: perServer, AdmissionTTLSeconds: 60, SoldOutGraceSeconds: 10}); err != nil {
		t.Fatal(err)
	}
	s.SetCapacity(CapacityUpdate{Healthy: healthy, Source: "static"})
	return s, c
}

func sJoin(t *testing.T, s Store, ref string) View {
	t.Helper()
	v, err := s.Join(ev, ref)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func sStatus(t *testing.T, s Store, id string) View {
	t.Helper()
	v, err := s.Status(context.Background(), id, 0)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func sStats(t *testing.T, s Store) Stats {
	t.Helper()
	st, err := s.Stats()
	if err != nil {
		t.Fatal(err)
	}
	return st
}

func sRelease(t *testing.T, s Store, id, outcome string) bool {
	t.Helper()
	ok, err := s.Release(id, outcome)
	if err != nil {
		t.Fatal(err)
	}
	return ok
}

func TestStoreFIFOOrderAndPositions(t *testing.T) {
	forEachStore(t, func(t *testing.T, mk func(*clock) Store) {
		s, c := enabled(t, mk, 0, 1)
		var ids []string
		for i := range 5 {
			v := sJoin(t, s, fmt.Sprintf("c%d", i))
			if v.Status != StatusWaiting || v.Position != int64(i+1) || v.Waiting != i+1 || v.EventID != ev {
				t.Fatalf("join %d: %+v", i, v)
			}
			ids = append(ids, v.QueueID)
		}
		s.Tick()
		if st := sStats(t, s); st.Active != 0 || st.Waiting != 5 {
			t.Fatalf("zero healthy servers must admit nobody: %+v", st)
		}
		if !sRelease(t, s, ids[2], "abandoned") {
			t.Fatal("release waiting")
		}
		if p := sStatus(t, s, ids[3]).Position; p != 3 {
			t.Fatalf("position after middle removal = %d, want 3", p)
		}
		ids = append(ids[:2], ids[3:]...)
		s.SetCapacity(CapacityUpdate{Healthy: 1})
		for i, id := range ids {
			c.Add(100 * time.Millisecond)
			s.Tick()
			v := sStatus(t, s, id)
			if v.Status != StatusAdmitted || v.Token == "" {
				t.Fatalf("entry %d not admitted in order: %+v", i, v)
			}
			for j, later := range ids[i+1:] {
				if lv := sStatus(t, s, later); lv.Status != StatusWaiting || lv.Position != int64(j+1) {
					t.Fatalf("after %d admissions entry %s: %+v", i+1, later, lv)
				}
			}
			p, err := signer.Verify(v.Token, c.Now())
			if err != nil || p.Q != id || p.E != ev || p.Exp != c.Now().Add(60*time.Second).Unix() {
				t.Fatalf("token %+v %v", p, err)
			}
			if v.TokenExpiresAt != FormatTime(time.Unix(c.Now().Add(60*time.Second).Unix(), 0)) {
				t.Fatalf("tokenExpiresAt %s", v.TokenExpiresAt)
			}
			if !sRelease(t, s, id, "purchased") {
				t.Fatal("release admitted")
			}
		}
		st := sStats(t, s)
		if st.AdmittedTotal != 4 || st.ReleasedTotal != 4 || st.JoinedTotal != 5 || st.LeftWaitingTotal != 1 || st.Waiting != 0 ||
			st.ReleasedByOutcome["purchased"] != 4 {
			t.Fatalf("stats %+v", st)
		}
	})
}

func TestStoreCapacityGatingAndGrowth(t *testing.T) {
	forEachStore(t, func(t *testing.T, mk func(*clock) Store) {
		s, _ := enabled(t, mk, 1, 3)
		for i := range 10 {
			sJoin(t, s, fmt.Sprintf("c%d", i))
		}
		s.Tick()
		if st := sStats(t, s); st.Active != 3 || st.Capacity != 3 || st.Waiting != 7 {
			t.Fatalf("1 server: %+v", st)
		}
		s.SetCapacity(CapacityUpdate{Healthy: 2})
		s.Tick()
		if st := sStats(t, s); st.Active != 6 || st.Capacity != 6 || st.Waiting != 4 {
			t.Fatalf("2 servers: %+v", st)
		}
		s.SetCapacity(CapacityUpdate{Healthy: 0})
		s.Tick()
		if st := sStats(t, s); st.Active != 6 || st.Waiting != 4 {
			t.Fatalf("0 servers: %+v", st)
		}
	})
}

func TestStoreReleaseTTLAndIdempotentJoin(t *testing.T) {
	forEachStore(t, func(t *testing.T, mk func(*clock) Store) {
		s, c := enabled(t, mk, 1, 1)
		a := sJoin(t, s, "a")
		if again := sJoin(t, s, "a"); again.QueueID != a.QueueID {
			t.Fatal("same customerRef while waiting must return the same entry")
		}
		b := sJoin(t, s, "b")
		s.Tick()
		if a2 := sJoin(t, s, "a"); a2.QueueID != a.QueueID || a2.Status != StatusAdmitted || a2.Token == "" {
			t.Fatalf("same customerRef while admitted: %+v", a2)
		}
		// TTL: b waits until a's admission expires.
		c.Add(59 * time.Second)
		s.Tick()
		if sStatus(t, s, b.QueueID).Status != StatusWaiting {
			t.Fatal("b admitted before a's TTL")
		}
		c.Add(time.Second)
		s.Tick()
		if sStatus(t, s, b.QueueID).Status != StatusAdmitted {
			t.Fatal("b should be admitted after a's TTL")
		}
		if st := sStats(t, s); st.ExpiredTotal != 1 || st.Active != 1 {
			t.Fatalf("stats %+v", st)
		}
		if sRelease(t, s, a.QueueID, "purchased") {
			t.Fatal("releasing an expired entry frees nothing")
		}
		if v := sStatus(t, s, a.QueueID); v.Status != StatusAdmitted || v.Token == "" {
			t.Fatalf("an expired entry still answers admitted with its token: %+v", v)
		}
		if v := sJoin(t, s, "a"); v.QueueID == a.QueueID || v.Status != StatusWaiting {
			t.Fatalf("rejoin after expiry: %+v", v)
		}
		// Release is idempotent; unknown ids report false.
		if !sRelease(t, s, b.QueueID, "purchased") || !sRelease(t, s, b.QueueID, "purchased") {
			t.Fatal("release must report true, also when repeated")
		}
		if sRelease(t, s, "q_UNKNOWN", "failed") {
			t.Fatal("unknown id must report false")
		}
		x, y := sJoin(t, s, ""), sJoin(t, s, "")
		if x.QueueID == y.QueueID {
			t.Fatal("empty customerRef is never deduplicated")
		}
		if st := sStats(t, s); st.JoinedTotal != 5 || st.ReleasedTotal != 1 {
			t.Fatalf("joined %d released %d", st.JoinedTotal, st.ReleasedTotal)
		}
	})
}

func TestStoreDisabledUnknownEventAndClear(t *testing.T) {
	forEachStore(t, func(t *testing.T, mk func(*clock) Store) {
		c := newClock()
		s := mk(c)
		v, err := s.Join(ev, "x")
		if err != nil || v.Status != StatusOpen || v.Token != "" || v.Position != 0 {
			t.Fatalf("disabled join: %+v %v", v, err)
		}
		if st := sStats(t, s); st.Enabled || st.JoinedTotal != 0 || st.Store != s.Kind() || !st.Leader {
			t.Fatalf("disabled stats %+v", st)
		}
		if _, err := s.Configure(Config{Enabled: true, EventID: ev, ActiveUsersPerServer: 25, AdmissionTTLSeconds: 60}); err != nil {
			t.Fatal(err)
		}
		if cfg := s.Config(); cfg.AdmissionMode != ModeFixed || cfg.Adaptive != DefaultAdaptiveConfig() || !cfg.Enabled {
			t.Fatalf("normalized config %+v", cfg)
		}
		if _, err := s.Join("99999999-2222-3333-4444-555555555555", "x"); !errors.Is(err, ErrUnknownEvent) {
			t.Fatalf("want ErrUnknownEvent, got %v", err)
		}
		w := sJoin(t, s, "a")
		done := make(chan error, 1)
		go func() { _, err := s.Status(context.Background(), w.QueueID, 20*time.Second); done <- err }()
		time.Sleep(30 * time.Millisecond)
		other := "22222222-2222-3333-4444-555555555555"
		if _, err := s.Configure(Config{Enabled: true, EventID: other, ActiveUsersPerServer: 1, AdmissionTTLSeconds: 60}); err != nil {
			t.Fatal(err)
		}
		select {
		case err := <-done:
			if !errors.Is(err, ErrUnknownQueueID) {
				t.Fatalf("want ErrUnknownQueueID, got %v", err)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("waiter not woken by config change")
		}
		if st := sStats(t, s); st.JoinedTotal != 0 || st.Waiting != 0 {
			t.Fatalf("not cleared: %+v", st)
		}
		b, _ := s.Join(other, "b")
		if _, err := s.Configure(Config{Enabled: true, EventID: other, ActiveUsersPerServer: 5, AdmissionTTLSeconds: 30}); err != nil {
			t.Fatal(err)
		}
		if _, err := s.Status(context.Background(), b.QueueID, 0); err != nil {
			t.Fatalf("entry lost on same-event reconfigure: %v", err)
		}
		if err := s.Reset(); err != nil {
			t.Fatal(err)
		}
		if st := sStats(t, s); st.JoinedTotal != 0 || !st.Enabled || st.ActiveUsersPerServer != 5 {
			t.Fatalf("reset: %+v", st)
		}
		if _, err := s.Status(context.Background(), b.QueueID, 0); !errors.Is(err, ErrUnknownQueueID) {
			t.Fatalf("reset must forget entries: %v", err)
		}
	})
}

func TestStoreLongPoll(t *testing.T) {
	forEachStore(t, func(t *testing.T, mk func(*clock) Store) {
		s, _ := enabled(t, mk, 0, 1)
		first := sJoin(t, s, "first")
		v := sJoin(t, s, "a")
		start := time.Now()
		got, err := s.Status(context.Background(), v.QueueID, 150*time.Millisecond)
		if err != nil || got.Status != StatusWaiting || got.Position != 2 || time.Since(start) < 140*time.Millisecond {
			t.Fatalf("timeout poll %+v %v after %v", got, err, time.Since(start))
		}
		// Cancel ends every poll with ctx.Err().
		ctx, cancel := context.WithCancel(context.Background())
		var wg sync.WaitGroup
		errs := make(chan error, 20)
		for range 20 {
			wg.Add(1)
			go func() { defer wg.Done(); _, err := s.Status(ctx, v.QueueID, 20*time.Second); errs <- err }()
		}
		time.Sleep(30 * time.Millisecond)
		cancel()
		waitOrFail(t, &wg, 2*time.Second)
		close(errs)
		for err := range errs {
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("want context.Canceled, got %v", err)
			}
		}
		// Admission wakes the poll at once.
		go func() {
			time.Sleep(50 * time.Millisecond)
			s.SetCapacity(CapacityUpdate{Healthy: 1})
			s.Tick()
		}()
		start = time.Now()
		got, err = s.Status(context.Background(), first.QueueID, 20*time.Second)
		if err != nil || got.Status != StatusAdmitted || time.Since(start) > 900*time.Millisecond {
			t.Fatalf("admission poll %+v %v after %v", got, err, time.Since(start))
		}
		// Close answers open polls with the current status.
		go func() { time.Sleep(30 * time.Millisecond); s.Close() }()
		got, err = s.Status(context.Background(), v.QueueID, 20*time.Second)
		if err != nil || got.Status != StatusWaiting {
			t.Fatalf("close: %+v %v", got, err)
		}
	})
}

func TestStoreSoldOutAndInventoryBudget(t *testing.T) {
	forEachStore(t, func(t *testing.T, mk func(*clock) Store) {
		s, c := enabled(t, mk, 1, 1)
		a, b, cc := sJoin(t, s, "a"), sJoin(t, s, "b"), sJoin(t, s, "c")
		s.Tick()
		if sStatus(t, s, a.QueueID).Status != StatusAdmitted {
			t.Fatal("a not admitted with unknown inventory")
		}
		sRelease(t, s, a.QueueID, "purchased")
		s.SetInventory(ev, 0, -1, true, c.Now())
		c.Add(5 * time.Second)
		s.Tick()
		if v := sStatus(t, s, b.QueueID); v.Status != StatusWaiting {
			t.Fatalf("b should keep waiting while available == 0: %+v", v)
		}
		s.SetInventory(ev, 1, -1, false, c.Now())
		s.Tick()
		if sStatus(t, s, b.QueueID).Status != StatusAdmitted {
			t.Fatal("b should be admitted after tickets came back")
		}
		sRelease(t, s, b.QueueID, "purchased")
		s.SetInventory(ev, 0, -1, true, c.Now())
		c.Add(9 * time.Second)
		s.SetInventoryUnknown(ev, "boom")
		c.Add(5 * time.Second)
		s.Tick()
		if st := sStats(t, s); st.SoldOut || st.Inventory != nil || st.InventoryError == nil || *st.InventoryError != "boom" {
			t.Fatalf("sold out declared while inventory unknown: %+v", st)
		}
		if sStatus(t, s, cc.QueueID).Status != StatusAdmitted {
			t.Fatal("unknown inventory must admit by capacity")
		}
		sRelease(t, s, cc.QueueID, "sold_out")
		d, e := sJoin(t, s, "d"), sJoin(t, s, "e")
		s.SetInventory(ev, 0, -1, true, c.Now())
		c.Add(10 * time.Second)
		waiter := make(chan View, 1)
		go func() { v, _ := s.Status(context.Background(), e.QueueID, 20*time.Second); waiter <- v }()
		time.Sleep(30 * time.Millisecond)
		s.Tick()
		select {
		case v := <-waiter:
			if v.Status != StatusSoldOut {
				t.Fatalf("long poll got %+v", v)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("long poll not woken by sold out")
		}
		if sStatus(t, s, d.QueueID).Status != StatusSoldOut {
			t.Fatal("d should be sold_out")
		}
		if f := sJoin(t, s, "f"); f.Status != StatusSoldOut {
			t.Fatalf("join after sold out: %+v", f)
		}
		st := sStats(t, s)
		if !st.SoldOut || st.SoldOutTotal != 3 || st.Waiting != 0 || st.Inventory == nil || st.Inventory.Available != 0 ||
			st.Inventory.Held != nil || !st.Inventory.SoldOut || st.InventoryZeroSince == nil {
			t.Fatalf("stats %+v", st)
		}
		s.SetInventory(ev, 5, -1, false, c.Now())
		s.Tick()
		if !sStats(t, s).SoldOut {
			t.Fatal("sold out must be sticky until reset")
		}

		// Budget: never admit more customers per observation than tickets left.
		s2, c2 := enabled(t, mk, 2, 25)
		var ids []string
		for i := range 40 {
			ids = append(ids, sJoin(t, s2, fmt.Sprintf("c%d", i)).QueueID)
		}
		s2.SetInventory(ev, 2, 7, false, c2.Now())
		s2.Tick()
		s2.Tick()
		if st := sStats(t, s2); st.Active != 2 || st.Waiting != 38 || *st.Inventory.Held != 7 {
			t.Fatalf("budget not applied: %+v", st)
		}
		if sStatus(t, s2, ids[1]).Status != StatusAdmitted || sStatus(t, s2, ids[2]).Status != StatusWaiting {
			t.Fatal("budget must still admit in FIFO order")
		}
		// Nothing available and nothing held: sold out at once.
		s2.SetInventory(ev, 0, 0, true, c2.Now())
		s2.Tick()
		if sStatus(t, s2, ids[2]).Status != StatusSoldOut || sStats(t, s2).SoldOutTotal != 38 {
			t.Fatal("waiting customers must be told sold out immediately")
		}
	})
}

func TestStoreStatsWaitAndRate(t *testing.T) {
	forEachStore(t, func(t *testing.T, mk func(*clock) Store) {
		s, c := enabled(t, mk, 0, 10)
		for i := range 10 {
			sJoin(t, s, fmt.Sprintf("c%d", i))
			c.Add(time.Second)
		}
		s.SetCapacity(CapacityUpdate{Healthy: 1})
		s.Tick()
		st := sStats(t, s)
		if st.WaitMs.Count != 10 || st.WaitMs.Max != 10000 || st.WaitMs.P50 != 5000 || st.WaitMs.P95 != 10000 {
			t.Fatalf("waitMs %+v", st.WaitMs)
		}
		if st.AdmissionRatePerSecond != 1 {
			t.Fatalf("rate %v, want 1", st.AdmissionRatePerSecond)
		}
		s.SetCapacity(CapacityUpdate{Healthy: 0})
		w := sJoin(t, s, "late")
		if v := sStatus(t, s, w.QueueID); v.EstimatedWaitSeconds == nil || *v.EstimatedWaitSeconds != 1 {
			t.Fatalf("estimate %+v", v.EstimatedWaitSeconds)
		}
		c.Add(11 * time.Second)
		if v := sStatus(t, s, w.QueueID); v.EstimatedWaitSeconds != nil {
			t.Fatalf("estimate without recent admissions should be null, got %d", *v.EstimatedWaitSeconds)
		}
		if st := sStats(t, s); st.OldestWaitingSeconds != 11 {
			t.Fatalf("oldestWaitingSeconds %v", st.OldestWaitingSeconds)
		}
	})
}

func TestStoreAbandonment(t *testing.T) {
	forEachStore(t, func(t *testing.T, mk func(*clock) Store) {
		c := newClock()
		s := mk(c)
		cfg := DefaultConfig()
		cfg.Enabled, cfg.EventID = true, ev
		if _, err := s.Configure(cfg); err != nil {
			t.Fatal(err)
		}
		s.SetCapacity(CapacityUpdate{Healthy: 0, Source: "static"})
		var ids []string
		for i := range 6 {
			ids = append(ids, sJoin(t, s, fmt.Sprintf("c%d", i)).QueueID)
		}
		for range 4 {
			c.Add(15 * time.Second)
			for _, i := range []int{1, 3, 5} {
				sStatus(t, s, ids[i])
			}
			s.Tick()
		}
		if st := sStats(t, s); st.Waiting != 3 || st.AbandonedTotal != 3 {
			t.Fatalf("waiting=%d abandoned=%d", st.Waiting, st.AbandonedTotal)
		}
		for want, i := range []int{1, 3, 5} {
			if p := sStatus(t, s, ids[i]).Position; p != int64(want+1) {
				t.Fatalf("c%d position %d, want %d", i, p, want+1)
			}
		}
		if _, err := s.Status(context.Background(), ids[0], 0); !errors.Is(err, ErrUnknownQueueID) {
			t.Fatalf("a dropped customer must get unknown_queue_id, got %v", err)
		}
		s.SetCapacity(CapacityUpdate{Healthy: 1, Source: "static"})
		s.Tick()
		if st := sStats(t, s); st.Active != 3 || st.AdmittedTotal != 3 {
			t.Fatalf("admitted %d", st.AdmittedTotal)
		}
		if v := sJoin(t, s, "c0"); v.QueueID == ids[0] {
			t.Fatal("expected a new place in line")
		}
		// A long poll in progress keeps a waiter present.
		p := sJoin(t, s, "patient")
		got := make(chan error, 1)
		go func() { _, err := s.Status(context.Background(), p.QueueID, 300*time.Millisecond); got <- err }()
		time.Sleep(50 * time.Millisecond)
		c.Add(40 * time.Second)
		s.Tick()
		if err := <-got; err != nil {
			t.Fatalf("poll: %v", err)
		}
		c.Add(30 * time.Second)
		s.Tick()
		if _, err := s.Status(context.Background(), p.QueueID, 0); err != nil {
			t.Fatalf("patient waiter dropped: %v", err)
		}
	})
}

// sDrain releases admitted customers (from ids) until at most keep remain admitted.
func sDrain(t *testing.T, s Store, ids []string, released map[string]bool, keep int) {
	t.Helper()
	var adm []string
	for _, id := range ids {
		if !released[id] && sStatus(t, s, id).Status == StatusAdmitted {
			adm = append(adm, id)
		}
	}
	for i := 0; i < len(adm)-keep; i++ {
		sRelease(t, s, adm[i], "purchased")
		released[adm[i]] = true
	}
}

func TestStoreAdaptiveController(t *testing.T) {
	forEachStore(t, func(t *testing.T, mk func(*clock) Store) {
		c := newClock()
		s := mk(c)
		cfg := DefaultConfig()
		cfg.Enabled, cfg.EventID, cfg.ActiveUsersPerServer, cfg.AdmissionMode = true, ev, 20, ModeAdaptive
		if _, err := s.Configure(cfg); err != nil {
			t.Fatal(err)
		}
		servers := []Server{{Name: "app-a", IP: "10.0.0.1", BaseURL: "http://a"}, {Name: "app-b", IP: "10.0.0.2", BaseURL: "http://b"}}
		s.SetCapacity(CapacityUpdate{Healthy: 2, Servers: servers, Source: "static"})
		if hs := s.HealthyServers(); len(hs) != 2 || hs[1].BaseURL != "http://b" {
			t.Fatalf("healthy servers %+v", hs)
		}
		sig := func(name string, active, queue int, cpuPct float64) {
			s.SetSignals(name, NewSignals(active, 32, queue, cpuPct/100, "", c.Now()))
		}
		view := func(name string) ServerView {
			for _, v := range sStats(t, s).Servers {
				if v.Name == name {
					return v
				}
			}
			t.Fatalf("no server %s", name)
			return ServerView{}
		}
		var ids []string
		for i := range 300 {
			ids = append(ids, sJoin(t, s, fmt.Sprintf("d%d", i)).QueueID)
		}
		released := map[string]bool{}
		if st := sStats(t, s); st.Capacity != 40 || st.Servers[0].Phase != PhaseSlowStart || st.Servers[0].Reason != "starting value" {
			t.Fatalf("start: %+v", st)
		}
		step := func() { s.Tick(); s.Control() }
		c.Add(time.Second)
		sig("app-a", 5, 0, 40)
		sig("app-b", 6, 0, 45)
		step()
		if v := view("app-a"); v.Limit != 25 || v.Decision != DecisionIncrease || v.Signals == nil || v.Signals.ActiveWorkers != 5 {
			t.Fatalf("grow together: %+v", v)
		}
		c.Add(time.Second)
		sig("app-a", 5, 0, 40)
		sig("app-b", 32, 3, 95)
		step()
		if v := view("app-a"); v.Limit != 18 || v.Reason != "listen queue 3 on b" || v.Phase != PhaseSteady || v.Decreases != 1 || v.DecisionAt == nil {
			t.Fatalf("busiest server rules: %+v", v)
		}
		c.Add(time.Second)
		sig("app-a", 5, 0, 40)
		sig("app-b", 32, 3, 95)
		step()
		if v := view("app-a"); v.Limit != 18 || v.Decision != DecisionHold {
			t.Fatalf("cooldown: %+v", v)
		}
		// A server without fresh signals blocks growth.
		c.Add(5 * time.Second)
		sig("app-a", 2, 0, 20)
		sDrain(t, s, ids, released, 0)
		step()
		if v := view("app-a"); v.Limit != 18 || v.Reason != "no signal from b" {
			t.Fatalf("missing signal: %+v", v)
		}
		// A server that leaves drops its signals; capacity follows.
		s.SetCapacity(CapacityUpdate{Healthy: 1, Servers: servers[:1], Source: "static"})
		if st := sStats(t, s); st.Capacity != 18 || len(st.Servers) != 1 {
			t.Fatalf("leave: %+v", st)
		}
		s.SetSignals("app-b", NewSignals(1, 32, 0, 0.1, "", c.Now())) // ignored: not healthy
		s.SetCapacity(CapacityUpdate{Healthy: 2, Servers: servers, Source: "static"})
		if v := view("app-b"); v.Signals != nil || v.Limit != 18 {
			t.Fatalf("rejoined server keeps no stale signals: %+v", v)
		}
	})
}

// ---- Redis: several instances sharing one Redis ----

func pair(t *testing.T) (*miniredis.Miniredis, *RedisRoom, *RedisRoom, *clock) {
	t.Helper()
	m := miniredis.RunT(t)
	c := newClock()
	opt := RedisOptions{Now: c.Now, Elect: true, RenewEvery: 20 * time.Millisecond, LeaseTTL: 5 * time.Second, Recheck: 10 * time.Second}
	opt.Instance = "a"
	a := newRedisRoom(t, m, opt)
	opt.Instance = "b"
	b := newRedisRoom(t, m, opt)
	if !a.Leading() || b.Leading() {
		t.Fatalf("first instance must lead: a=%v b=%v", a.Leading(), b.Leading())
	}
	return m, a, b, c
}

func eventually(t *testing.T, d time.Duration, what string, f func() bool) {
	t.Helper()
	deadline := time.Now().Add(d)
	for !f() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out: %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestRedisTwoInstancesShareTheLine(t *testing.T) {
	_, a, b, _ := pair(t)
	// Configured through the follower, applied everywhere.
	if _, err := b.Configure(Config{Enabled: true, EventID: ev, ActiveUsersPerServer: 1, AdmissionTTLSeconds: 60}); err != nil {
		t.Fatal(err)
	}
	if !a.Config().Enabled {
		t.Fatal("config not shared")
	}
	x := sJoin(t, a, "x")
	y := sJoin(t, b, "y")
	if y.Position != 2 || sStatus(t, a, y.QueueID).Position != 2 || sStatus(t, b, x.QueueID).Position != 1 {
		t.Fatal("positions differ between instances")
	}
	// The follower's leader-side calls do nothing.
	b.SetCapacity(CapacityUpdate{Healthy: 5})
	b.Tick()
	if st := sStats(t, b); st.HealthyServers != 0 || st.Active != 0 {
		t.Fatalf("follower acted as leader: %+v", st)
	}
	// Long poll on B, admission by A's tick: woken by pub/sub (Recheck is 10 s, so not by polling).
	got := make(chan View, 1)
	go func() { v, _ := b.Status(context.Background(), x.QueueID, 20*time.Second); got <- v }()
	time.Sleep(30 * time.Millisecond)
	start := time.Now()
	a.SetCapacity(CapacityUpdate{Healthy: 1, Source: "static"})
	a.Tick()
	select {
	case v := <-got:
		if v.Status != StatusAdmitted || v.Token == "" {
			t.Fatalf("B saw %+v", v)
		}
		if el := time.Since(start); el > time.Second {
			t.Fatalf("wake-up took %v", el)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("long poll on B not woken by A's admission")
	}
	if va, vb := sStatus(t, a, x.QueueID), sStatus(t, b, x.QueueID); va.Token != vb.Token {
		t.Fatal("both instances must render the same token")
	}
	// Release on B frees the slot for A's next tick.
	sRelease(t, b, x.QueueID, "purchased")
	a.Tick()
	if sStatus(t, b, y.QueueID).Status != StatusAdmitted {
		t.Fatal("y not admitted after x released on the other instance")
	}
	sa, sb := sStats(t, a), sStats(t, b)
	if sa.JoinedTotal != 2 || sa.AdmittedTotal != sb.AdmittedTotal || sa.ReleasedTotal != 1 || sb.Active != 1 {
		t.Fatalf("stats differ: %+v / %+v", sa, sb)
	}
	if !sa.Leader || sb.Leader || sa.Instance != "a" || sb.Instance != "b" || sb.LeaderInstance == nil || *sb.LeaderInstance != "a" || sb.Store != StoreRedis {
		t.Fatalf("HA fields: a=%v/%s b=%v/%s leader=%v", sa.Leader, sa.Instance, sb.Leader, sb.Instance, sb.LeaderInstance)
	}
}

func TestRedisLeaderFailover(t *testing.T) {
	m, a, b, c := pair(t)
	if _, err := a.Configure(Config{Enabled: true, EventID: ev, ActiveUsersPerServer: 2, AdmissionTTLSeconds: 60}); err != nil {
		t.Fatal(err)
	}
	a.SetCapacity(CapacityUpdate{Healthy: 0, Source: "static"})
	var ids []string
	for i := range 10 {
		on := a
		if i%2 == 1 {
			on = b
		}
		ids = append(ids, sJoin(t, on, fmt.Sprintf("c%d", i)).QueueID)
	}
	// A long poll on B survives the leader's crash.
	got := make(chan View, 1)
	go func() { v, _ := b.Status(context.Background(), ids[0], 20*time.Second); got <- v }()

	a.crash() // killed: no hand-over, the lease must expire
	time.Sleep(60 * time.Millisecond)
	if b.Leading() {
		t.Fatal("B took over before the lease expired")
	}
	m.FastForward(5 * time.Second)
	eventually(t, 2*time.Second, "B becomes leader", b.Leading)
	st := sStats(t, b)
	if st.Waiting != 10 || st.JoinedTotal != 10 || *st.LeaderInstance != "b" || !st.Leader {
		t.Fatalf("line lost in failover: %+v", st)
	}
	for i, id := range ids {
		if p := sStatus(t, b, id).Position; p != int64(i+1) {
			t.Fatalf("position of %d after failover = %d", i, p)
		}
	}
	// The new leader admits in the original FIFO order.
	b.SetCapacity(CapacityUpdate{Healthy: 1, Source: "static"})
	c.Add(100 * time.Millisecond)
	b.Tick()
	select {
	case v := <-got:
		if v.Status != StatusAdmitted {
			t.Fatalf("poll after failover: %+v", v)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("long poll not woken by the new leader")
	}
	for i, id := range ids {
		want := StatusWaiting
		if i < 2 {
			want = StatusAdmitted
		}
		if v := sStatus(t, b, id); v.Status != want {
			t.Fatalf("entry %d: %s, want %s", i, v.Status, want)
		}
	}
	// The old leader's fenced tick (as if it resumed after a pause) is refused.
	a.leading.Store(true)
	a.SetCapacity(CapacityUpdate{Healthy: 50})
	a.Tick()
	if st := sStats(t, b); st.Active != 2 || st.HealthyServers != 1 {
		t.Fatalf("stale leader acted: %+v", st)
	}
}

func TestRedisGracefulHandOver(t *testing.T) {
	_, a, b, _ := pair(t)
	a.Close()
	start := time.Now()
	eventually(t, time.Second, "B takes over after A resigns", b.Leading)
	t.Logf("hand-over took %v", time.Since(start))
}

// Three instances tick concurrently without election (every one believes it leads): the atomic
// admission script still admits each customer exactly once and never exceeds the capacity.
func TestRedisNoDoubleAdmission(t *testing.T) {
	m := miniredis.RunT(t)
	c := newClock()
	var rooms []*RedisRoom
	for i := range 3 {
		rooms = append(rooms, newRedisRoom(t, m, RedisOptions{Now: c.Now, Instance: fmt.Sprint("r", i)}))
	}
	if _, err := rooms[0].Configure(Config{Enabled: true, EventID: ev, ActiveUsersPerServer: 10, AdmissionTTLSeconds: 600}); err != nil {
		t.Fatal(err)
	}
	rooms[0].SetCapacity(CapacityUpdate{Healthy: 2, Source: "static"})
	const n = 120
	ids := make([]string, n)
	for i := range n {
		ids[i] = sJoin(t, rooms[i%3], fmt.Sprintf("c%d", i)).QueueID
	}
	var mu sync.Mutex
	released := map[string]bool{}
	var wg sync.WaitGroup
	for w := range 3 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r := rooms[w]
			for range 40 {
				r.Tick()
				st, err := r.Stats()
				if err != nil {
					t.Error(err)
					return
				}
				if st.Active > st.Capacity {
					t.Errorf("active %d > capacity %d", st.Active, st.Capacity)
				}
				// Release a few admitted customers so admission keeps going.
				for i := w; i < n; i += 3 {
					v, err := r.Status(context.Background(), ids[i], 0)
					if err == nil && v.Status == StatusAdmitted {
						mu.Lock()
						done := released[ids[i]]
						released[ids[i]] = true
						mu.Unlock()
						if !done {
							if _, err := r.Release(ids[i], "purchased"); err != nil {
								t.Error(err)
							}
						}
					}
				}
			}
		}()
	}
	wg.Wait()
	st := sStats(t, rooms[0])
	if st.AdmittedTotal < int64(len(released)) || st.AdmittedTotal < 40 {
		t.Fatalf("admitted %d, released %d, active %d", st.AdmittedTotal, len(released), st.Active)
	}
	if st.AdmittedTotal+int64(st.Waiting) != n || st.WaitMs.Count != st.AdmittedTotal {
		t.Fatalf("every customer is admitted at most once: admitted %d + waiting %d != %d (waits %d)", st.AdmittedTotal, st.Waiting, n, st.WaitMs.Count)
	}
	// FIFO across instances: everyone admitted joined before everyone still waiting.
	lastAdmitted, firstWaiting := -1, n
	for i, id := range ids {
		if sStatus(t, rooms[1], id).Status == StatusAdmitted {
			lastAdmitted = i
		} else if firstWaiting == n {
			firstWaiting = i
		}
	}
	if lastAdmitted > firstWaiting {
		t.Fatalf("FIFO broken: admitted #%d while #%d still waits", lastAdmitted, firstWaiting)
	}
}

func TestRedisOnlyOneLeader(t *testing.T) {
	m := miniredis.RunT(t)
	var rooms []*RedisRoom
	for i := range 3 {
		rooms = append(rooms, newRedisRoom(t, m, RedisOptions{Elect: true, Instance: fmt.Sprint("r", i), RenewEvery: 10 * time.Millisecond}))
	}
	for range 20 {
		n := 0
		for _, r := range rooms {
			if r.Leading() {
				n++
			}
		}
		if n != 1 {
			t.Fatalf("%d leaders", n)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestRedisResetRemovesTheEventKeys(t *testing.T) {
	m := miniredis.RunT(t)
	c := newClock()
	r := newRedisRoom(t, m, RedisOptions{Now: c.Now, Instance: "solo"})
	cfg := DefaultConfig()
	cfg.Enabled, cfg.EventID, cfg.AdmissionMode = true, ev, ModeAdaptive
	if _, err := r.Configure(cfg); err != nil {
		t.Fatal(err)
	}
	r.SetCapacity(CapacityUpdate{Healthy: 1, Servers: []Server{{Name: "app"}}, Source: "static"})
	r.SetSignals("app", NewSignals(1, 32, 0, 0.1, "", c.Now()))
	r.SetInventory(ev, 10, 0, false, c.Now())
	for i := range 40 {
		sJoin(t, r, fmt.Sprint("c", i))
	}
	r.Tick()
	r.Control()
	x := sJoin(t, r, "x")
	sRelease(t, r, x.QueueID, "abandoned")
	if len(m.Keys()) < 12 {
		t.Fatalf("expected the room's keys, got %v", m.Keys())
	}
	kept := map[string]bool{"wr:config": true, "wr:capacity": true, "wr:capacity:names": true}
	check := func(when string) {
		t.Helper()
		for _, k := range m.Keys() {
			if !kept[k] {
				t.Fatalf("%s: key %s left behind (all: %v)", when, k, m.Keys())
			}
		}
	}
	if err := r.Reset(); err != nil {
		t.Fatal(err)
	}
	check("reset")
	for i := range 5 {
		sJoin(t, r, fmt.Sprint("c", i))
	}
	r.Tick()
	cfg.EventID = "22222222-2222-3333-4444-555555555555"
	if _, err := r.Configure(cfg); err != nil {
		t.Fatal(err)
	}
	check("new event")
	if st := sStats(t, r); st.JoinedTotal != 0 || st.Waiting != 0 || st.Active != 0 || st.Inventory != nil {
		t.Fatalf("stats after clear %+v", st)
	}
}
