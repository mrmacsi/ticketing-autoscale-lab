package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	redis "github.com/redis/go-redis/v9"

	"github.com/mrmacsi/ticketing-autoscale-lab/waiting-room/queue"
	"github.com/mrmacsi/ticketing-autoscale-lab/waiting-room/token"
)

// Two queue servers behind (imaginary) round robin share one Redis: a customer may join on one,
// long-poll on the other, and the numbers are the same wherever stats are read.
func TestTwoServersShareOneRedis(t *testing.T) {
	m := miniredis.RunT(t)
	var rooms []*queue.RedisRoom
	var envs []*env
	for _, name := range []string{"wr-1", "wr-2"} {
		rdb := redis.NewClient(&redis.Options{Addr: m.Addr()})
		room, err := queue.NewRedis(context.Background(), rdb, token.NewSigner(admin),
			queue.RedisOptions{Instance: name, Elect: true, RenewEvery: 20 * time.Millisecond, Recheck: 5 * time.Second})
		if err != nil {
			t.Fatal(err)
		}
		srv := httptest.NewServer(New(room, admin, nil))
		t.Cleanup(func() { room.Close(); srv.Close(); rdb.Close() })
		rooms = append(rooms, room)
		envs = append(envs, &env{t: t, srv: srv, c: &http.Client{Timeout: 30 * time.Second}})
	}
	leader, follower := rooms[0], rooms[1]
	if !leader.Leading() || follower.Leading() {
		t.Fatal("wr-1 should lead")
	}

	// Configure through the follower.
	if r := envs[1].call("PUT", "/admin/config", admin, map[string]any{
		"enabled": true, "eventId": ev, "activeUsersPerServer": 1, "admissionTtlSeconds": 60,
	}); r.code != 200 {
		t.Fatalf("config %d %v", r.code, r.body)
	}
	var ids []string
	for i := range 6 {
		d := data(envs[i%2].call("POST", "/queue/join", "", map[string]any{"eventId": ev, "customerRef": fmt.Sprint("c", i)}))
		if d["status"] != "waiting" || d["position"] != float64(i+1) {
			t.Fatalf("join %d on wr-%d: %v", i, i%2+1, d)
		}
		ids = append(ids, d["queueId"].(string))
	}
	// Customer 0 joined on wr-1 and long-polls wr-2; wr-1 (leader) admits.
	go func() {
		time.Sleep(100 * time.Millisecond)
		leader.SetCapacity(queue.CapacityUpdate{Healthy: 2, Source: "static"})
		leader.Tick()
	}()
	start := time.Now()
	s := data(envs[1].call("GET", "/queue/status/"+ids[0]+"?waitSeconds=20", "", nil))
	if s["status"] != "admitted" || time.Since(start) > 2*time.Second {
		t.Fatalf("cross-instance long poll: %v after %v", s, time.Since(start))
	}
	if p, err := token.NewSigner(admin).Verify(s["token"].(string), time.Now()); err != nil || p.Q != ids[0] {
		t.Fatalf("token %v %v", p, err)
	}
	if r := envs[0].call("POST", "/queue/release", "", map[string]any{"queueId": ids[0], "outcome": "purchased"}); data(r)["released"] != true {
		t.Fatalf("release on the other server: %v", r.body)
	}

	statsOf := func(e *env) map[string]any {
		st := data(e.call("GET", "/admin/stats", admin, nil))
		for _, k := range []string{"instance", "leader", "updatedAt", "oldestWaitingSeconds"} {
			delete(st, k)
		}
		return st
	}
	a, b := statsOf(envs[0]), statsOf(envs[1])
	if !reflect.DeepEqual(a, b) {
		ja, _ := json.Marshal(a)
		jb, _ := json.Marshal(b)
		t.Fatalf("stats differ:\n%s\n%s", ja, jb)
	}
	if a["store"] != "redis" || a["leaderInstance"] != "wr-1" || a["waiting"] != float64(4) || a["admittedTotal"] != float64(2) {
		t.Fatalf("stats %v", a)
	}

	// Deploy/restart of the leader: it hands over; wr-2 leads and the line is intact.
	leader.Close()
	deadline := time.Now().Add(2 * time.Second)
	for !follower.Leading() {
		if time.Now().After(deadline) {
			t.Fatal("wr-2 did not take over")
		}
		time.Sleep(10 * time.Millisecond)
	}
	st := data(envs[1].call("GET", "/admin/stats", admin, nil))
	if st["leader"] != true || st["leaderInstance"] != "wr-2" || st["waiting"] != float64(4) || st["joinedTotal"] != float64(6) {
		t.Fatalf("after hand-over %v", st)
	}
	if d := data(envs[1].call("GET", "/queue/status/"+ids[5], "", nil)); d["position"] != float64(4) {
		t.Fatalf("position after hand-over %v", d)
	}
	follower.SetCapacity(queue.CapacityUpdate{Healthy: 3, Source: "static"})
	follower.Tick()
	if d := data(envs[1].call("GET", "/queue/status/"+ids[2], "", nil)); d["status"] != "admitted" {
		t.Fatalf("new leader does not admit: %v", d)
	}
}

// TestRedisLoadDrain is TestLoadDrain against a real Redis shared by two servers: 5,000 customers
// join through both, 2,000 long-poll alternately on either, the leader's 100 ms loop admits at
// capacity 200 while customers release. Opt-in: WR_REDIS_ADDR=localhost:16379 go test ./server -run RedisLoad -v
func TestRedisLoadDrain(t *testing.T) {
	addr := os.Getenv("WR_REDIS_ADDR")
	if addr == "" {
		t.Skip("set WR_REDIS_ADDR to run against a real Redis")
	}
	const customers, pollers = 5000, 2000
	prefix := fmt.Sprintf("wrtest:%d:", time.Now().UnixNano())
	var rooms []*queue.RedisRoom
	var urls []string
	tr := &http.Transport{MaxIdleConnsPerHost: pollers, MaxIdleConns: 2 * pollers}
	client := &http.Client{Timeout: 30 * time.Second, Transport: tr}
	defer tr.CloseIdleConnections()
	for _, name := range []string{"load-1", "load-2"} {
		rdb := redis.NewClient(&redis.Options{Addr: addr, PoolSize: 200})
		room, err := queue.NewRedis(context.Background(), rdb, token.NewSigner(admin),
			queue.RedisOptions{Prefix: prefix, Instance: name, Elect: true})
		if err != nil {
			t.Fatal(err)
		}
		srv := httptest.NewServer(New(room, admin, nil))
		t.Cleanup(func() {
			room.Close()
			srv.Close()
			keys, _ := rdb.Keys(context.Background(), prefix+"*").Result()
			if len(keys) > 0 {
				rdb.Del(context.Background(), keys...)
			}
			rdb.Close()
		})
		rooms = append(rooms, room)
		urls = append(urls, srv.URL)
	}
	call := func(i int, method, path string, body any) map[string]any {
		e := &env{t: t, c: client, srv: &httptest.Server{URL: urls[i%2]}}
		return data(e.call(method, path, admin, body))
	}
	call(1, "PUT", "/admin/config", map[string]any{"enabled": true, "eventId": ev, "activeUsersPerServer": 50, "admissionTtlSeconds": 60, "waitingTimeoutSeconds": 0})
	leader := rooms[0]
	leader.SetCapacity(queue.CapacityUpdate{Healthy: 4, Source: "static"})

	start := time.Now()
	ids := make([]string, customers)
	var jwg sync.WaitGroup
	for w := range 100 {
		jwg.Add(1)
		go func() {
			defer jwg.Done()
			for i := w; i < customers; i += 100 {
				ids[i], _ = call(i, "POST", "/queue/join", map[string]any{"eventId": ev, "customerRef": fmt.Sprintf("c%d", i)})["queueId"].(string)
			}
		}()
	}
	jwg.Wait()
	joined := time.Since(start)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	for _, r := range rooms {
		go r.Run(ctx, 100*time.Millisecond)
	}
	var admitted, polls atomic.Int64
	var pwg sync.WaitGroup
	for w := range pollers {
		pwg.Add(1)
		go func() {
			defer pwg.Done()
			for i := w; i < customers; i += pollers {
				for k := 0; ; k++ {
					polls.Add(1)
					d := call(i+k+1, "GET", "/queue/status/"+ids[i]+"?waitSeconds=25", nil) // the other server than the join, then alternate
					if d["status"] == "waiting" {
						continue
					}
					if d["status"] != "admitted" {
						t.Errorf("entry %d: %v", i, d)
						return
					}
					admitted.Add(1)
					call(i, "POST", "/queue/release", map[string]any{"queueId": ids[i], "outcome": "purchased"})
					break
				}
			}
		}()
	}
	done := make(chan struct{})
	go func() { pwg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(60 * time.Second):
		t.Fatalf("not drained in time: admitted %d", admitted.Load())
	}
	st, err := rooms[1].Stats()
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("redis %s: joins %v (%.0f/s), drain %v total, %d polls, waiting=%d admitted=%d released=%d waitP50=%dms p95=%dms rate=%.1f/s",
		addr, joined, customers/joined.Seconds(), time.Since(start), polls.Load(), st.Waiting, st.AdmittedTotal, st.ReleasedTotal, st.WaitMs.P50, st.WaitMs.P95, st.AdmissionRatePerSecond)
	if admitted.Load() != customers || st.AdmittedTotal != customers || st.ReleasedTotal != customers || st.Waiting != 0 || st.Active != 0 {
		t.Fatalf("admitted %d, stats %+v", admitted.Load(), st)
	}
}
