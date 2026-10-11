package queue

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/mrmacsi/ticketing-autoscale-lab/waiting-room/token"
)

const ev = "11111111-2222-3333-4444-555555555555"

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) Now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *clock) Add(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

var signer = token.NewSigner("test-admin-token")

// newRoom returns an enabled room with `healthy` servers × perServer capacity.
func newRoom(t *testing.T, healthy, perServer int) (*Room, *clock) {
	t.Helper()
	c := &clock{t: time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)}
	r := New(signer, c.Now)
	r.Configure(Config{Enabled: true, EventID: ev, ActiveUsersPerServer: perServer, AdmissionTTLSeconds: 60, SoldOutGraceSeconds: 10})
	r.SetCapacity(CapacityUpdate{Healthy: healthy, Source: "static"})
	return r, c
}

func join(t *testing.T, r *Room, ref string) View {
	t.Helper()
	v, err := r.Join(ev, ref)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func status(t *testing.T, r *Room, id string) View {
	t.Helper()
	v, err := r.Status(context.Background(), id, 0)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func TestFIFOOrderAndPositions(t *testing.T) {
	r, c := newRoom(t, 0, 1)
	var ids []string
	for i := range 5 {
		v := join(t, r, fmt.Sprintf("c%d", i))
		if v.Status != StatusWaiting || v.Position != int64(i+1) || v.Waiting != i+1 {
			t.Fatalf("join %d: %+v", i, v)
		}
		if len(v.QueueID) != 28 || v.QueueID[:2] != "q_" {
			t.Fatalf("queue id %q", v.QueueID)
		}
		ids = append(ids, v.QueueID)
	}
	r.Tick()
	if s := r.Stats(); s.Active != 0 || s.Waiting != 5 {
		t.Fatalf("zero healthy servers must admit nobody: %+v", s)
	}
	// Leaving from the middle renumbers those behind.
	if !r.Release(ids[2], "abandoned") {
		t.Fatal("release waiting")
	}
	if p := status(t, r, ids[3]).Position; p != 3 {
		t.Fatalf("position after middle removal = %d, want 3", p)
	}
	ids = append(ids[:2], ids[3:]...)
	r.SetCapacity(CapacityUpdate{Healthy: 1})
	for i, id := range ids {
		c.Add(100 * time.Millisecond)
		r.Tick()
		v := status(t, r, id)
		if v.Status != StatusAdmitted || v.Token == "" {
			t.Fatalf("entry %d not admitted in order: %+v", i, v)
		}
		for j, later := range ids[i+1:] {
			if lv := status(t, r, later); lv.Status != StatusWaiting || lv.Position != int64(j+1) {
				t.Fatalf("after %d admissions entry %s: %+v", i+1, later, lv)
			}
		}
		p, err := signer.Verify(v.Token, c.Now())
		if err != nil || p.Q != id || p.E != ev || p.Exp != c.Now().Add(60*time.Second).Unix() {
			t.Fatalf("token %+v %v", p, err)
		}
		if !r.Release(id, "purchased") {
			t.Fatal("release admitted")
		}
	}
	s := r.Stats()
	if s.AdmittedTotal != 4 || s.ReleasedTotal != 4 || s.JoinedTotal != 5 || s.LeftWaitingTotal != 1 || s.Waiting != 0 {
		t.Fatalf("stats %+v", s)
	}
}

func TestCapacityGatingAndGrowth(t *testing.T) {
	r, _ := newRoom(t, 1, 3)
	for i := range 10 {
		join(t, r, fmt.Sprintf("c%d", i))
	}
	r.Tick()
	if s := r.Stats(); s.Active != 3 || s.Capacity != 3 || s.Waiting != 7 {
		t.Fatalf("1 server: %+v", s)
	}
	r.SetCapacity(CapacityUpdate{Healthy: 2})
	r.Tick()
	if s := r.Stats(); s.Active != 6 || s.Capacity != 6 || s.Waiting != 4 {
		t.Fatalf("2 servers: %+v", s)
	}
	// Capacity shrinking never evicts the admitted, it only stops admitting.
	r.SetCapacity(CapacityUpdate{Healthy: 0})
	r.Tick()
	if s := r.Stats(); s.Active != 6 || s.Waiting != 4 {
		t.Fatalf("0 servers: %+v", s)
	}
}

func TestReleaseFreesSlotAndIsIdempotent(t *testing.T) {
	r, _ := newRoom(t, 1, 1)
	a := join(t, r, "a")
	b := join(t, r, "b")
	r.Tick()
	if status(t, r, b.QueueID).Status != StatusWaiting {
		t.Fatal("b should wait")
	}
	if !r.Release(a.QueueID, "purchased") || !r.Release(a.QueueID, "purchased") {
		t.Fatal("release must report true, also when repeated")
	}
	if r.Release("q_UNKNOWN", "failed") {
		t.Fatal("unknown id must report false")
	}
	r.Tick()
	if status(t, r, b.QueueID).Status != StatusAdmitted {
		t.Fatal("b should be admitted after a released")
	}
	if s := r.Stats(); s.ReleasedTotal != 1 || s.ReleasedByOutcome["purchased"] != 1 {
		t.Fatalf("stats %+v", s)
	}
}

func TestTTLExpiryFreesSlot(t *testing.T) {
	r, c := newRoom(t, 1, 1)
	a := join(t, r, "a")
	b := join(t, r, "b")
	r.Tick()
	c.Add(59 * time.Second)
	r.Tick()
	if status(t, r, b.QueueID).Status != StatusWaiting {
		t.Fatal("b admitted before a's TTL")
	}
	c.Add(time.Second)
	r.Tick()
	if status(t, r, b.QueueID).Status != StatusAdmitted {
		t.Fatal("b should be admitted after a's TTL")
	}
	if s := r.Stats(); s.ExpiredTotal != 1 || s.Active != 1 {
		t.Fatalf("stats %+v", s)
	}
	if r.Release(a.QueueID, "purchased") {
		t.Fatal("releasing an expired entry frees nothing")
	}
	// The same customer may join again once their admission expired.
	if v := join(t, r, "a"); v.QueueID == a.QueueID || v.Status != StatusWaiting {
		t.Fatalf("rejoin after expiry: %+v", v)
	}
}

func TestIdempotentJoin(t *testing.T) {
	r, _ := newRoom(t, 1, 1)
	a1 := join(t, r, "alice")
	a2 := join(t, r, "alice")
	if a1.QueueID != a2.QueueID {
		t.Fatal("same customerRef while waiting must return the same entry")
	}
	r.Tick()
	a3 := join(t, r, "alice")
	if a3.QueueID != a1.QueueID || a3.Status != StatusAdmitted || a3.Token == "" {
		t.Fatalf("same customerRef while admitted: %+v", a3)
	}
	r.Release(a1.QueueID, "purchased")
	if a4 := join(t, r, "alice"); a4.QueueID == a1.QueueID {
		t.Fatal("after release a join creates a new entry")
	}
	x, y := join(t, r, ""), join(t, r, "")
	if x.QueueID == y.QueueID {
		t.Fatal("empty customerRef is never deduplicated")
	}
	if s := r.Stats(); s.JoinedTotal != 4 {
		t.Fatalf("joinedTotal = %d, want 4", s.JoinedTotal)
	}
}

func TestDisabledAndUnknownEvent(t *testing.T) {
	c := &clock{t: time.Unix(1_800_000_000, 0)}
	r := New(signer, c.Now)
	v, err := r.Join(ev, "x")
	if err != nil || v.Status != StatusOpen || v.Token != "" || v.Position != 0 {
		t.Fatalf("disabled join: %+v %v", v, err)
	}
	if s := r.Stats(); s.Enabled || s.JoinedTotal != 0 {
		t.Fatalf("disabled stats %+v", s)
	}
	r.Configure(Config{Enabled: true, EventID: ev, ActiveUsersPerServer: 25, AdmissionTTLSeconds: 60})
	if _, err := r.Join("99999999-2222-3333-4444-555555555555", "x"); !errors.Is(err, ErrUnknownEvent) {
		t.Fatalf("want ErrUnknownEvent, got %v", err)
	}
}

func TestConfigChangeClearsAndWakesWaiters(t *testing.T) {
	r, _ := newRoom(t, 0, 1)
	v := join(t, r, "a")
	done := make(chan error, 1)
	go func() {
		_, err := r.Status(context.Background(), v.QueueID, 20*time.Second)
		done <- err
	}()
	time.Sleep(20 * time.Millisecond)
	r.Configure(Config{Enabled: true, EventID: "22222222-2222-3333-4444-555555555555", ActiveUsersPerServer: 1, AdmissionTTLSeconds: 60})
	select {
	case err := <-done:
		if !errors.Is(err, ErrUnknownQueueID) {
			t.Fatalf("want ErrUnknownQueueID, got %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("waiter not woken by config change")
	}
	if s := r.Stats(); s.JoinedTotal != 0 || s.Waiting != 0 {
		t.Fatalf("not cleared: %+v", s)
	}
	// Same event, other settings: nothing is cleared.
	join2 := func() View { v, _ := r.Join("22222222-2222-3333-4444-555555555555", "b"); return v }()
	r.Configure(Config{Enabled: true, EventID: "22222222-2222-3333-4444-555555555555", ActiveUsersPerServer: 5, AdmissionTTLSeconds: 30})
	if _, err := r.Status(context.Background(), join2.QueueID, 0); err != nil {
		t.Fatalf("entry lost on same-event reconfigure: %v", err)
	}
	r.Reset()
	if s := r.Stats(); s.JoinedTotal != 0 || !s.Enabled || s.ActiveUsersPerServer != 5 {
		t.Fatalf("reset: %+v", s)
	}
}

func TestLongPollReturnsOnAdmission(t *testing.T) {
	r, _ := newRoom(t, 0, 1)
	v := join(t, r, "a")
	go func() {
		time.Sleep(50 * time.Millisecond)
		r.SetCapacity(CapacityUpdate{Healthy: 1})
		r.Tick()
	}()
	start := time.Now()
	got, err := r.Status(context.Background(), v.QueueID, 20*time.Second)
	if err != nil || got.Status != StatusAdmitted {
		t.Fatalf("%+v %v", got, err)
	}
	if el := time.Since(start); el > 2*time.Second {
		t.Fatalf("long poll took %v", el)
	}
}

func TestLongPollTimeoutAndCancel(t *testing.T) {
	r, _ := newRoom(t, 0, 1)
	join(t, r, "first")
	v := join(t, r, "a")
	start := time.Now()
	got, err := r.Status(context.Background(), v.QueueID, 150*time.Millisecond)
	if err != nil || got.Status != StatusWaiting || got.Position != 2 {
		t.Fatalf("%+v %v", got, err)
	}
	if el := time.Since(start); el < 140*time.Millisecond {
		t.Fatalf("returned early after %v", el)
	}

	before := runtime.NumGoroutine()
	var wg sync.WaitGroup
	ctx, cancel := context.WithCancel(context.Background())
	errs := make(chan error, 100)
	for range 100 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := r.Status(ctx, v.QueueID, 20*time.Second)
			errs <- err
		}()
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
	assertGoroutines(t, before)
}

func TestCloseWakesLongPolls(t *testing.T) {
	r, _ := newRoom(t, 0, 1)
	v := join(t, r, "a")
	go func() { time.Sleep(30 * time.Millisecond); r.Close() }()
	got, err := r.Status(context.Background(), v.QueueID, 20*time.Second)
	if err != nil || got.Status != StatusWaiting {
		t.Fatalf("%+v %v", got, err)
	}
}

func TestSoldOutGraceAndResume(t *testing.T) {
	r, c := newRoom(t, 1, 1)
	a := join(t, r, "a")
	b := join(t, r, "b")
	cc := join(t, r, "c")

	// Inventory unknown: admit by capacity.
	r.Tick()
	if status(t, r, a.QueueID).Status != StatusAdmitted {
		t.Fatal("a not admitted with unknown inventory")
	}
	r.Release(a.QueueID, "purchased")

	// available == 0: stop admitting, but not sold out before the grace period (10 s).
	r.SetInventory(ev, 0, -1, true, c.Now())
	c.Add(5 * time.Second)
	r.Tick()
	if v := status(t, r, b.QueueID); v.Status != StatusWaiting {
		t.Fatalf("b should keep waiting while available == 0: %+v", v)
	}
	// Tickets come back (an expired hold): admission resumes and the grace clock resets.
	r.SetInventory(ev, 1, -1, false, c.Now())
	r.Tick()
	if status(t, r, b.QueueID).Status != StatusAdmitted {
		t.Fatal("b should be admitted after tickets came back")
	}
	r.Release(b.QueueID, "purchased")

	r.SetInventory(ev, 0, -1, true, c.Now())
	c.Add(9 * time.Second)
	r.SetInventoryUnknown(ev, "boom") // unknown inventory never declares sold out
	c.Add(5 * time.Second)
	r.Tick()
	if r.Stats().SoldOut {
		t.Fatal("sold out declared while inventory unknown")
	}
	if status(t, r, cc.QueueID).Status != StatusAdmitted {
		t.Fatal("unknown inventory must admit by capacity")
	}
	r.Release(cc.QueueID, "sold_out")
	d := join(t, r, "d")
	e := join(t, r, "e")

	// Known zero for the full grace: declare sold out; everyone waiting is told.
	r.SetInventory(ev, 0, -1, true, c.Now())
	c.Add(10 * time.Second)
	waiter := make(chan View, 1)
	go func() { v, _ := r.Status(context.Background(), e.QueueID, 20*time.Second); waiter <- v }()
	time.Sleep(20 * time.Millisecond)
	r.Tick()
	select {
	case v := <-waiter:
		if v.Status != StatusSoldOut {
			t.Fatalf("long poll got %+v", v)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("long poll not woken by sold out")
	}
	if status(t, r, d.QueueID).Status != StatusSoldOut {
		t.Fatal("d should be sold_out")
	}
	f := join(t, r, "f")
	if f.Status != StatusSoldOut {
		t.Fatalf("join after sold out: %+v", f)
	}
	s := r.Stats()
	if !s.SoldOut || s.SoldOutTotal != 3 || s.Waiting != 0 || s.Inventory == nil || s.Inventory.Available != 0 {
		t.Fatalf("stats %+v", s)
	}
	// Tickets returning after the declaration do not reopen the room.
	r.SetInventory(ev, 5, -1, false, c.Now())
	r.Tick()
	if !r.Stats().SoldOut {
		t.Fatal("sold out must be sticky until reset")
	}
}

func TestSoldOutImmediatelyWhenNothingAvailableOrHeld(t *testing.T) {
	r, c := newRoom(t, 1, 5)
	a := join(t, r, "a")
	b := join(t, r, "b")
	// Holds outstanding: they may come back, so keep everyone waiting (no grace needed to decide).
	r.SetInventory(ev, 0, 3, true, c.Now())
	c.Add(time.Hour) // far beyond the grace: known holds keep the room open
	r.SetInventory(ev, 0, 3, true, c.Now())
	r.Tick()
	if s := r.Stats(); s.SoldOut || status(t, r, a.QueueID).Status != StatusWaiting {
		t.Fatalf("must keep waiting while tickets are held: %+v", s)
	}
	// Last hold bought: nothing available, nothing held → sold out at once.
	r.SetInventory(ev, 0, 0, true, c.Now())
	r.Tick()
	if status(t, r, a.QueueID).Status != StatusSoldOut || status(t, r, b.QueueID).Status != StatusSoldOut {
		t.Fatal("waiting customers must be told sold out immediately")
	}
	if s := r.Stats(); !s.SoldOut || s.Inventory.Held == nil || *s.Inventory.Held != 0 {
		t.Fatalf("stats %+v", s)
	}
}

func TestAdmitsNoMoreCustomersThanTicketsLeft(t *testing.T) {
	r, c := newRoom(t, 2, 25) // capacity 50
	var ids []string
	for i := range 40 {
		ids = append(ids, join(t, r, fmt.Sprintf("c%d", i)).QueueID)
	}
	// Two tickets came back from expired holds: admit two customers, not 40.
	r.SetInventory(ev, 2, 7, false, c.Now())
	r.Tick()
	r.Tick()
	if s := r.Stats(); s.Active != 2 || s.Waiting != 38 {
		t.Fatalf("budget not applied: active=%d waiting=%d", s.Active, s.Waiting)
	}
	if status(t, r, ids[0]).Status != StatusAdmitted || status(t, r, ids[1]).Status != StatusAdmitted || status(t, r, ids[2]).Status != StatusWaiting {
		t.Fatal("budget must still admit in FIFO order")
	}
	// Next observation: plenty of tickets again → capacity is the limit.
	r.SetInventory(ev, 1000, 0, false, c.Now())
	r.Tick()
	if s := r.Stats(); s.Active != 40 || s.Waiting != 0 {
		t.Fatalf("capacity should apply with plenty of tickets: active=%d waiting=%d", s.Active, s.Waiting)
	}
}

func TestStatsWaitAndRate(t *testing.T) {
	r, c := newRoom(t, 0, 10)
	for i := range 10 {
		join(t, r, fmt.Sprintf("c%d", i))
		c.Add(time.Second)
	}
	r.SetCapacity(CapacityUpdate{Healthy: 1})
	r.Tick()
	s := r.Stats()
	if s.WaitMs.Count != 10 || s.WaitMs.Max != 10000 || s.WaitMs.P50 != 5000 || s.WaitMs.P95 != 10000 {
		t.Fatalf("waitMs %+v", s.WaitMs)
	}
	if s.AdmissionRatePerSecond != 1 {
		t.Fatalf("rate %v, want 1 (10 admissions / 10 s)", s.AdmissionRatePerSecond)
	}
	r.SetCapacity(CapacityUpdate{Healthy: 0})
	w := join(t, r, "late")
	if v := status(t, r, w.QueueID); v.EstimatedWaitSeconds == nil || *v.EstimatedWaitSeconds != 1 {
		t.Fatalf("estimate %+v", v.EstimatedWaitSeconds)
	}
	c.Add(11 * time.Second)
	if v := status(t, r, w.QueueID); v.EstimatedWaitSeconds != nil {
		t.Fatalf("estimate without recent admissions should be null, got %d", *v.EstimatedWaitSeconds)
	}
	if s := r.Stats(); s.OldestWaitingSeconds != 11 {
		t.Fatalf("oldestWaitingSeconds %v", s.OldestWaitingSeconds)
	}
}

func waitOrFail(t *testing.T, wg *sync.WaitGroup, d time.Duration) {
	t.Helper()
	ch := make(chan struct{})
	go func() { wg.Wait(); close(ch) }()
	select {
	case <-ch:
	case <-time.After(d):
		t.Fatal("timed out waiting for goroutines")
	}
}

func assertGoroutines(t *testing.T, before int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for runtime.NumGoroutine() > before+2 {
		if time.Now().After(deadline) {
			t.Fatalf("goroutine leak: before %d, now %d", before, runtime.NumGoroutine())
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestPeopleWhoStopCheckingInLeaveTheLine(t *testing.T) {
	c := &clock{t: time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)}
	r := New(signer, c.Now)
	cfg := DefaultConfig() // waitingTimeoutSeconds 45
	cfg.Enabled, cfg.EventID = true, ev
	r.Configure(cfg)
	r.SetCapacity(CapacityUpdate{Healthy: 0, Source: "static"}) // nobody can be admitted yet
	var ids []string
	for i := range 6 {
		ids = append(ids, join(t, r, fmt.Sprintf("c%d", i)).QueueID)
	}
	// c1, c3 and c5 keep polling; the others close the page.
	for range 4 {
		c.Add(15 * time.Second)
		for _, i := range []int{1, 3, 5} {
			status(t, r, ids[i])
		}
		r.Tick()
	}
	s := r.Stats()
	if s.Waiting != 3 || s.AbandonedTotal != 3 {
		t.Fatalf("waiting=%d abandoned=%d", s.Waiting, s.AbandonedTotal)
	}
	for want, i := range []int{1, 3, 5} {
		if p := status(t, r, ids[i]).Position; p != int64(want+1) {
			t.Fatalf("c%d position %d, want %d", i, p, want+1)
		}
	}
	if _, err := r.Status(context.Background(), ids[0], 0); !errors.Is(err, ErrUnknownQueueID) {
		t.Fatalf("a dropped customer must get unknown_queue_id, got %v", err)
	}
	// Capacity appears: only the people still there are admitted.
	r.SetCapacity(CapacityUpdate{Healthy: 1, Source: "static"})
	r.Tick()
	if s := r.Stats(); s.Active != 3 || s.AdmittedTotal != 3 {
		t.Fatalf("admitted %d", s.AdmittedTotal)
	}
	// Coming back re-joins at the back of the line under a new id.
	if v := join(t, r, "c0"); v.QueueID == ids[0] {
		t.Fatal("expected a new place in line")
	}
}

func TestLongPollKeepsAWaiterPresent(t *testing.T) {
	c := &clock{t: time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)}
	r := New(signer, c.Now)
	cfg := DefaultConfig()
	cfg.Enabled, cfg.EventID = true, ev
	r.Configure(cfg)
	v := join(t, r, "patient")
	got := make(chan error, 1)
	go func() { _, err := r.Status(context.Background(), v.QueueID, 300*time.Millisecond); got <- err }()
	time.Sleep(50 * time.Millisecond)
	c.Add(40 * time.Second) // within the timeout of the poll's start
	r.Tick()
	if err := <-got; err != nil {
		t.Fatalf("poll: %v", err)
	}
	c.Add(30 * time.Second) // 30 s after the poll returned: still within 45 s
	r.Tick()
	if s := r.Stats(); s.Waiting != 1 || s.AbandonedTotal != 0 {
		t.Fatalf("waiting=%d abandoned=%d", s.Waiting, s.AbandonedTotal)
	}
}
