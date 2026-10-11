package queue

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/alicebob/miniredis/v2/server"
	redis "github.com/redis/go-redis/v9"
)

// Redis runs one script at a time, so a script whose cost grows with the line blocks every instance
// (measured in a 10,000-customer local run: 100+ ms scripts, lease renewals timing out, leadership
// flapping, stats frozen). These tests pin every script to O(batch) with a line far longer than a
// batch: they count the Redis commands each operation runs (commands called from Lua included) and
// refuse any whole-key read of a key whose size grows with the line.

const scaleLine = 12_000

// cmdRecorder records the commands miniredis executes while on.
type cmdRecorder struct {
	mu   sync.Mutex
	on   bool
	cmds [][]string
}

func (c *cmdRecorder) hook(_ *server.Peer, cmd string, args ...string) bool {
	c.mu.Lock()
	if c.on {
		c.cmds = append(c.cmds, append([]string{cmd}, args...))
	}
	c.mu.Unlock()
	return false
}

// measure runs f and returns the commands it caused.
func (c *cmdRecorder) measure(f func()) [][]string {
	c.mu.Lock()
	c.on, c.cmds = true, nil
	c.mu.Unlock()
	f()
	c.mu.Lock()
	defer c.mu.Unlock()
	c.on = false
	return c.cmds
}

// boundedKeys may be read whole: their size does not depend on the line.
var boundedKeys = map[string]bool{"wr:state": true, "wr:capacity": true, "wr:counters": true, "wr:rate": true, "wr:signals": true}

// worstCmds keeps the largest command count per operation (logged at the end of the test).
type cmdCounts struct {
	mu sync.Mutex
	m  map[string]int
}

func (w *cmdCounts) note(what string, n int) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.m == nil {
		w.m = map[string]int{}
	}
	w.m[what] = max(w.m[what], n)
}

var worstCmds cmdCounts

// checkBounded fails on more than maxCmds commands or on any read that could return O(line) items.
func checkBounded(t *testing.T, what string, cmds [][]string, maxCmds int) {
	t.Helper()
	worstCmds.note(what, len(cmds))
	if len(cmds) > maxCmds {
		t.Errorf("%s: %d Redis commands, want <= %d (O(batch))", what, len(cmds), maxCmds)
	}
	num := func(s string) int {
		n, err := strconv.Atoi(s)
		if err != nil {
			return 1 << 30
		}
		return n
	}
	for _, c := range cmds {
		bad := ""
		switch c[0] {
		case "ZRANGE", "ZREVRANGE", "LRANGE":
			if start, stop := num(c[2]), num(c[3]); stop < 0 || stop-start+1 > 2000 {
				bad = "unbounded range"
			}
		case "ZRANGEBYSCORE", "ZREVRANGEBYSCORE":
			i := slicesIndex(c, "LIMIT")
			if i < 0 || num(c[i+2]) > 500 {
				bad = "range by score without LIMIT <= 500"
			}
		case "ZPOPMIN", "ZPOPMAX":
			if len(c) > 2 && num(c[2]) > 500 {
				bad = "pop of more than a batch"
			}
		case "HGETALL", "HKEYS", "HVALS", "SMEMBERS", "ZRANGEBYLEX":
			if !boundedKeys[c[1]] {
				bad = "whole read of a key that grows with the line"
			}
		case "KEYS", "SCAN", "HSCAN", "ZSCAN", "SSCAN", "SORT":
			bad = "scan"
		}
		if bad != "" {
			t.Errorf("%s: %s: %s", what, bad, strings.Join(c, " "))
		}
	}
}

func slicesIndex(s []string, v string) int {
	for i, x := range s {
		if strings.EqualFold(x, v) {
			return i
		}
	}
	return -1
}

// seedLine writes n waiting customers straight into miniredis (the scripts would take minutes on
// miniredis, whose ZRANK sorts the whole set) and returns their ids in line order.
func seedLine(t *testing.T, m *miniredis.Miniredis, n int, joined time.Time) []string {
	t.Helper()
	ids := make([]string, n)
	for i := range n {
		id := fmt.Sprintf("q_SEED%022d", i)
		ids[i] = id
		ref := fmt.Sprintf("seed-%d", i)
		m.HSet("wr:entries", id, fmt.Sprintf("w,%d,0,%s", joined.UnixMilli(), ref))
		m.HSet("wr:customers", ref, id)
		m.ZAdd("wr:line", float64(i+1), id)
		m.ZAdd("wr:seen", float64(joined.UnixMilli()), id)
	}
	m.Set("wr:seq", strconv.Itoa(n))
	m.HSet("wr:counters", "joined", strconv.Itoa(n))
	return ids
}

func TestRedisScriptsStayOBatchWithALongLine(t *testing.T) {
	m := miniredis.RunT(t)
	rec := &cmdRecorder{}
	m.Server().SetPreHook(rec.hook)
	c := newClock()
	r := newRedisRoom(t, m, RedisOptions{Now: c.Now, Instance: "solo", Recheck: time.Hour})
	cfg := DefaultConfig() // waitingTimeoutSeconds 45
	cfg.Enabled, cfg.EventID, cfg.ActiveUsersPerServer = true, ev, 25
	if _, err := r.Configure(cfg); err != nil {
		t.Fatal(err)
	}
	r.SetCapacity(CapacityUpdate{Healthy: 2, Source: "static"})
	ids := seedLine(t, m, scaleLine, c.Now())

	// Customer operations: a handful of commands each, whatever the line length.
	checkBounded(t, "join", rec.measure(func() { sJoin(t, r, "new-customer") }), 25)
	checkBounded(t, "status", rec.measure(func() {
		if v := sStatus(t, r, ids[scaleLine-1]); v.Position != scaleLine || v.Waiting != scaleLine+1 {
			t.Fatalf("status %+v", v)
		}
	}), 15)
	checkBounded(t, "release (waiting)", rec.measure(func() { sRelease(t, r, ids[5000], "abandoned") }), 20)
	checkBounded(t, "stats", rec.measure(func() { sStats(t, r) }), 30)
	checkBounded(t, "control", rec.measure(func() { r.Control() }), 30)

	// Admission: one tick admits at most the free capacity (50).
	cmds := rec.measure(r.Tick)
	checkBounded(t, "admission tick", cmds, 40)
	if st := sStats(t, r); st.Active != 50 || st.Waiting != scaleLine-50 {
		t.Fatalf("after one tick: active %d waiting %d", st.Active, st.Waiting)
	}

	// Everybody stops checking in: each tick forgets at most a batch; the line empties over ticks.
	c.Add(time.Minute)
	stale := int64(sStats(t, r).Waiting)
	sweeps := 0
	for sStats(t, r).Waiting > 0 {
		checkBounded(t, "abandonment tick", rec.measure(r.Tick), 40)
		if sweeps++; sweeps > scaleLine/500+5 {
			t.Fatalf("abandonment sweep does not make progress: waiting %d", sStats(t, r).Waiting)
		}
	}
	if st := sStats(t, r); st.AbandonedTotal != stale || st.AdmittedTotal != 50 {
		t.Fatalf("abandoned %d of %d, admitted %d (nobody who left may be admitted)", st.AbandonedTotal, stale, st.AdmittedTotal)
	}

	// Sold out with a long line: declaring is O(1) and tells everyone at once; the line drains in
	// batches on the following ticks.
	if err := r.Reset(); err != nil {
		t.Fatal(err)
	}
	ids = seedLine(t, m, scaleLine, c.Now())
	r.SetInventory(ev, 0, 0, true, c.Now())
	checkBounded(t, "sold-out tick", rec.measure(r.Tick), 40)
	if v := sStatus(t, r, ids[scaleLine-1]); v.Status != StatusSoldOut || v.Position != 0 || v.Waiting != 0 {
		t.Fatalf("last in line right after the declaration: %+v", v)
	}
	if ok := sRelease(t, r, ids[100], "abandoned"); ok {
		t.Fatal("releasing a sold-out entry frees nothing")
	}
	if st := sStats(t, r); !st.SoldOut || st.Waiting != 0 || st.SoldOutTotal != scaleLine || st.OldestWaitingSeconds != 0 {
		t.Fatalf("stats right after the declaration: soldOut=%v waiting=%d soldOutTotal=%d", st.SoldOut, st.Waiting, st.SoldOutTotal)
	}
	for drains := 0; m.Exists("wr:line"); drains++ {
		if drains > scaleLine/500+2 {
			t.Fatal("sold-out line does not drain")
		}
		checkBounded(t, "sold-out drain tick", rec.measure(r.Tick), 40)
	}
	if f, _ := m.HGet("wr:entries", ids[scaleLine-1]), ""; !strings.HasPrefix(f, "s,") {
		t.Fatalf("drained entry %q", f)
	}
	if st := sStats(t, r); st.SoldOutTotal != scaleLine {
		t.Fatalf("soldOutTotal changed while draining: %d", st.SoldOutTotal)
	}

	// An hour later the finished entries are pruned a batch at a time.
	c.Add(2 * time.Hour)
	for prunes := 0; m.Exists("wr:finished"); prunes++ {
		if prunes > scaleLine/500+2 {
			t.Fatal("pruning does not make progress")
		}
		checkBounded(t, "prune tick", rec.measure(r.Tick), 40)
	}

	// Reset/new event: a constant number of commands (UNLINK frees big keys in the background).
	seedLine(t, m, scaleLine, c.Now())
	checkBounded(t, "reset", rec.measure(func() {
		if err := r.Reset(); err != nil {
			t.Fatal(err)
		}
	}), 20)
	t.Logf("worst Redis commands per operation with %d in line: %v", scaleLine, worstCmds.m)
}

// TestRedisScriptTimesOnRealRedis is the same scenario against a real Redis (opt-in), measuring
// wall-clock time per call with 20,000 people in line. WR_REDIS_ADDR=localhost:16379
func TestRedisScriptTimesOnRealRedis(t *testing.T) {
	addr := os.Getenv("WR_REDIS_ADDR")
	if addr == "" {
		t.Skip("set WR_REDIS_ADDR to run against a real Redis")
	}
	const n = 20_000
	prefix := fmt.Sprintf("wrscale:%d:", time.Now().UnixNano())
	rdb := redis.NewClient(&redis.Options{Addr: addr, PoolSize: 64})
	defer rdb.Close()
	defer func() {
		keys, _ := rdb.Keys(context.Background(), prefix+"*").Result()
		if len(keys) > 0 {
			rdb.Unlink(context.Background(), keys...)
		}
	}()
	c := newClock()
	r, err := NewRedis(context.Background(), rdb, signer, RedisOptions{Prefix: prefix, Now: c.Now, Instance: "scale"})
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	cfg := DefaultConfig()
	cfg.Enabled, cfg.EventID, cfg.ActiveUsersPerServer = true, ev, 25
	if _, err := r.Configure(cfg); err != nil {
		t.Fatal(err)
	}
	r.SetCapacity(CapacityUpdate{Healthy: 2, Source: "static"})
	seed := func() []string {
		ids := make([]string, n)
		var wg sync.WaitGroup
		for w := range 32 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for i := w; i < n; i += 32 {
					v, err := r.Join(ev, fmt.Sprint("c", i))
					if err != nil {
						t.Error(err)
						return
					}
					ids[i] = v.QueueID
				}
			}()
		}
		wg.Wait()
		return ids
	}
	worst := map[string]time.Duration{}
	timed := func(what string, f func()) {
		start := time.Now()
		f()
		worst[what] = max(worst[what], time.Since(start))
	}
	ids := seed()
	for i := range 200 {
		timed("status", func() { sStatus(t, r, ids[(i*97)%n]) })
		timed("join", func() { sJoin(t, r, fmt.Sprint("x", i)) })
	}
	for range 20 {
		timed("stats", func() { sStats(t, r) })
		timed("admission tick", r.Tick)
	}
	c.Add(time.Minute)
	for sStats(t, r).Waiting > 0 {
		timed("abandonment tick", r.Tick)
	}
	if err := r.Reset(); err != nil {
		t.Fatal(err)
	}
	seed()
	r.SetInventory(ev, 0, 0, true, c.Now())
	for i := 0; i < n/500+5; i++ {
		timed("sold-out tick", r.Tick)
	}
	timed("reset", func() { _ = r.Reset() })
	for what, d := range worst {
		t.Logf("%-17s worst %v", what, d)
		if d > 25*time.Millisecond {
			t.Errorf("%s took %v with %d in line", what, d, n)
		}
	}
}
