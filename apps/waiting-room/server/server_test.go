package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mrmacsi/ticketing-autoscale-lab/waiting-room/queue"
	"github.com/mrmacsi/ticketing-autoscale-lab/waiting-room/token"
)

const (
	admin = "test-admin-token"
	ev    = "11111111-2222-3333-4444-555555555555"
)

type env struct {
	t    *testing.T
	room *queue.Room
	srv  *httptest.Server
	c    *http.Client
}

func newEnv(t *testing.T) *env {
	t.Helper()
	room := queue.New(token.NewSigner(admin), nil)
	srv := httptest.NewServer(New(room, admin, nil))
	t.Cleanup(func() { room.Close(); srv.Close() })
	return &env{t: t, room: room, srv: srv, c: &http.Client{Timeout: 30 * time.Second}}
}

type resp struct {
	code int
	hdr  http.Header
	body map[string]any
}

func (e *env) do(ctx context.Context, method, path, auth string, body any) resp {
	e.t.Helper()
	var rd io.Reader
	if body != nil {
		if s, ok := body.(string); ok {
			rd = strings.NewReader(s)
		} else {
			b, _ := json.Marshal(body)
			rd = bytes.NewReader(b)
		}
	}
	req, _ := http.NewRequestWithContext(ctx, method, e.srv.URL+path, rd)
	if auth != "" {
		req.Header.Set("Authorization", "Bearer "+auth)
	}
	res, err := e.c.Do(req)
	if err != nil {
		e.t.Fatalf("%s %s: %v", method, path, err)
	}
	defer res.Body.Close()
	var m map[string]any
	raw, _ := io.ReadAll(res.Body)
	if err := json.Unmarshal(raw, &m); err != nil {
		e.t.Fatalf("%s %s: non-JSON body %q", method, path, raw)
	}
	return resp{res.StatusCode, res.Header, m}
}

func (e *env) call(method, path, auth string, body any) resp {
	return e.do(context.Background(), method, path, auth, body)
}

func data(r resp) map[string]any { d, _ := r.body["data"].(map[string]any); return d }

func (e *env) enable(perServer int, healthy int) {
	e.t.Helper()
	r := e.call("PUT", "/admin/config", admin, map[string]any{
		"enabled": true, "eventId": ev, "activeUsersPerServer": perServer, "admissionTtlSeconds": 60, "soldOutGraceSeconds": 10,
	})
	if r.code != 200 {
		e.t.Fatalf("config: %d %v", r.code, r.body)
	}
	e.room.SetCapacity(queue.CapacityUpdate{Healthy: healthy, Source: "static"})
}

func TestCommonHeadersEverywhere(t *testing.T) {
	e := newEnv(t)
	for _, c := range []struct {
		method, path, auth string
		code               int
	}{
		{"GET", "/queue/health", "", 200},
		{"GET", "/nope", "", 404},
		{"GET", "/queue/join", "", 405},
		{"GET", "/queue/status/q_X", "", 404},
		{"GET", "/admin/stats", "", 401},
		{"GET", "/admin/stats", admin, 200},
	} {
		r := e.call(c.method, c.path, c.auth, nil)
		if r.code != c.code {
			t.Errorf("%s %s = %d, want %d", c.method, c.path, r.code, c.code)
		}
		if r.hdr.Get("X-Served-By") != "waiting-room" || r.hdr.Get("Cache-Control") != "no-store" ||
			r.hdr.Get("Content-Type") != "application/json" {
			t.Errorf("%s %s headers %v", c.method, c.path, r.hdr)
		}
		if c.code >= 400 && (r.body["error"] == nil || r.body["message"] == nil) {
			t.Errorf("%s %s error body %v", c.method, c.path, r.body)
		}
	}
	if r := e.call("GET", "/queue/health", "", nil); r.body["status"] != "ok" {
		t.Fatalf("health %v", r.body)
	}
}

func TestAdminAuth(t *testing.T) {
	e := newEnv(t)
	for _, auth := range []string{"", "wrong", admin + "x", strings.ToUpper(admin)} {
		for _, p := range []struct{ m, path string }{{"GET", "/admin/stats"}, {"POST", "/admin/reset"}, {"PUT", "/admin/config"}} {
			if r := e.call(p.m, p.path, auth, map[string]any{}); r.code != 401 || r.body["error"] != "unauthorized" {
				t.Errorf("%s %s with %q: %d %v", p.m, p.path, auth, r.code, r.body)
			}
		}
	}
	// Basic auth or a bare token is not accepted.
	req, _ := http.NewRequest("GET", e.srv.URL+"/admin/stats", nil)
	req.Header.Set("Authorization", admin)
	res, _ := e.c.Do(req)
	res.Body.Close()
	if res.StatusCode != 401 {
		t.Fatalf("bare token accepted: %d", res.StatusCode)
	}
	if r := e.call("POST", "/admin/reset", admin, nil); r.code != 200 {
		t.Fatalf("reset %d", r.code)
	}
}

func TestConfigValidation(t *testing.T) {
	e := newEnv(t)
	bad := []map[string]any{
		{"enabled": true},
		{"enabled": true, "eventId": "not-a-uuid"},
		{"enabled": true, "eventId": ev, "activeUsersPerServer": 0},
		{"enabled": true, "eventId": ev, "activeUsersPerServer": 1001},
		{"enabled": true, "eventId": ev, "admissionTtlSeconds": 9},
		{"enabled": true, "eventId": ev, "soldOutGraceSeconds": 3601},
	}
	for _, b := range bad {
		if r := e.call("PUT", "/admin/config", admin, b); r.code != 400 || r.body["error"] != "invalid_request" {
			t.Errorf("%v: %d %v", b, r.code, r.body)
		}
	}
	if r := e.call("PUT", "/admin/config", admin, "{"); r.code != 400 {
		t.Errorf("malformed JSON: %d", r.code)
	}
	r := e.call("PUT", "/admin/config", admin, map[string]any{"enabled": true, "eventId": strings.ToUpper(ev)})
	d := data(r)
	if r.code != 200 || d["eventId"] != ev || d["activeUsersPerServer"] != float64(25) || d["admissionTtlSeconds"] != float64(60) {
		t.Fatalf("partial config: %d %v", r.code, r.body)
	}
}

func TestHealthReportsVersion(t *testing.T) {
	room := queue.New(token.NewSigner(admin), nil)
	h := New(room, admin, nil)
	h.Version = "v-test"
	srv := httptest.NewServer(h)
	t.Cleanup(func() { room.Close(); srv.Close() })
	e := &env{t: t, room: room, srv: srv, c: &http.Client{Timeout: 5 * time.Second}}
	r := e.call("GET", "/queue/health", "", nil)
	if r.code != 200 || r.body["version"] != "v-test" || r.hdr.Get("X-App-Version") != "v-test" {
		t.Fatalf("health %d %v %v", r.code, r.body, r.hdr)
	}
}

func TestAdaptiveConfigOverHTTP(t *testing.T) {
	e := newEnv(t)
	for _, b := range []map[string]any{
		{"admissionMode": "magic"},
		{"adaptive": map[string]any{"minPerServer": 50, "maxPerServer": 10}},
		{"adaptive": map[string]any{"busyLow": 0.95}},
	} {
		if r := e.call("PUT", "/admin/config", admin, b); r.code != 400 {
			t.Errorf("%v accepted: %d %v", b, r.code, r.body)
		}
	}
	r := e.call("PUT", "/admin/config", admin, map[string]any{
		"enabled": true, "eventId": ev, "admissionMode": "adaptive", "adaptive": map[string]any{"maxPerServer": 120},
	})
	d := data(r)
	a, _ := d["adaptive"].(map[string]any)
	if r.code != 200 || d["admissionMode"] != "adaptive" || a["maxPerServer"] != float64(120) || a["busyHigh"] != 0.9 || a["minPerServer"] != float64(5) {
		t.Fatalf("adaptive config: %d %v", r.code, r.body)
	}
	e.room.SetCapacity(queue.CapacityUpdate{Healthy: 1, Source: "static", Servers: []queue.Server{{Name: "app-1", IP: "10.0.0.1"}}})
	st := data(e.call("GET", "/admin/stats", admin, nil))
	servers, _ := st["servers"].([]any)
	if st["admissionMode"] != "adaptive" || st["capacity"] != float64(25) || len(servers) != 1 {
		t.Fatalf("stats %v", st)
	}
	if s0 := servers[0].(map[string]any); s0["limit"] != float64(25) || s0["phase"] != "slow_start" || s0["signals"] != nil {
		t.Fatalf("server view %v", s0)
	}
}

func TestJoinStatusReleaseFlow(t *testing.T) {
	e := newEnv(t)
	// Disabled: open, no token, nothing counted.
	r := e.call("POST", "/queue/join", "", map[string]any{"eventId": ev, "customerRef": "c1"})
	if d := data(r); r.code != 200 || d["status"] != "open" || d["token"] != nil {
		t.Fatalf("disabled join %d %v", r.code, r.body)
	}
	st := data(e.call("GET", "/admin/stats", admin, nil))
	if st["enabled"] != false {
		t.Fatalf("stats enabled %v", st)
	}

	e.enable(1, 0)
	for _, b := range []any{
		map[string]any{"eventId": "x"},
		map[string]any{"eventId": ev, "customerRef": strings.Repeat("a", 101)},
		"not json",
	} {
		if r := e.call("POST", "/queue/join", "", b); r.code != 400 || r.body["error"] != "invalid_request" {
			t.Errorf("join %v: %d %v", b, r.code, r.body)
		}
	}
	if r := e.call("POST", "/queue/join", "", map[string]any{"eventId": "99999999-2222-3333-4444-555555555555"}); r.code != 404 || r.body["error"] != "unknown_event" {
		t.Fatalf("other event: %d %v", r.code, r.body)
	}

	a := data(e.call("POST", "/queue/join", "", map[string]any{"eventId": ev, "customerRef": "a"}))
	b := data(e.call("POST", "/queue/join", "", map[string]any{"eventId": ev, "customerRef": "b"}))
	if a["status"] != "waiting" || a["position"] != float64(1) || b["position"] != float64(2) || b["waiting"] != float64(2) {
		t.Fatalf("join: %v %v", a, b)
	}
	if _, ok := a["estimatedWaitSeconds"]; !ok || a["estimatedWaitSeconds"] != nil {
		t.Fatalf("estimatedWaitSeconds should be present and null: %v", a)
	}
	if again := data(e.call("POST", "/queue/join", "", map[string]any{"eventId": ev, "customerRef": "a"})); again["queueId"] != a["queueId"] {
		t.Fatal("join retry is not idempotent")
	}
	aid := a["queueId"].(string)

	// Long poll returns on timeout with the current position...
	start := time.Now()
	s := data(e.call("GET", "/queue/status/"+aid+"?waitSeconds=0.3", "", nil))
	if s["status"] != "waiting" || time.Since(start) < 250*time.Millisecond {
		t.Fatalf("timeout poll %v after %v", s, time.Since(start))
	}
	// ...and as soon as the entry is admitted.
	go func() {
		time.Sleep(100 * time.Millisecond)
		e.room.SetCapacity(queue.CapacityUpdate{Healthy: 1, Source: "static"})
		e.room.Tick()
	}()
	start = time.Now()
	s = data(e.call("GET", "/queue/status/"+aid+"?waitSeconds=25", "", nil))
	if s["status"] != "admitted" || time.Since(start) > 3*time.Second {
		t.Fatalf("admission poll %v after %v", s, time.Since(start))
	}
	tok, _ := s["token"].(string)
	if p, err := token.NewSigner(admin).Verify(tok, time.Now()); err != nil || p.Q != aid || p.E != ev {
		t.Fatalf("token %q: %+v %v", tok, p, err)
	}
	if exp, err := time.Parse(queue.TimeFormat, s["tokenExpiresAt"].(string)); err != nil || exp.Before(time.Now().Add(55*time.Second)) {
		t.Fatalf("tokenExpiresAt %v %v", s["tokenExpiresAt"], err)
	}

	if r := e.call("GET", "/queue/status/q_UNKNOWN?waitSeconds=5", "", nil); r.code != 404 || r.body["error"] != "unknown_queue_id" {
		t.Fatalf("unknown id %d %v", r.code, r.body)
	}
	if r := e.call("GET", "/queue/status/"+aid+"?waitSeconds=abc", "", nil); r.code != 400 {
		t.Fatalf("bad waitSeconds %d", r.code)
	}
	if r := e.call("POST", "/queue/release", "", map[string]any{"queueId": aid, "outcome": "bogus"}); r.code != 400 {
		t.Fatalf("bad outcome %d", r.code)
	}
	for range 2 {
		if r := e.call("POST", "/queue/release", "", map[string]any{"queueId": aid, "outcome": "purchased"}); r.code != 200 || data(r)["released"] != true {
			t.Fatalf("release %d %v", r.code, r.body)
		}
	}
	if r := e.call("POST", "/queue/release", "", map[string]any{"queueId": "q_NOPE", "outcome": "failed"}); r.code != 200 || data(r)["released"] != false {
		t.Fatalf("release unknown %d %v", r.code, r.body)
	}

	st = data(e.call("GET", "/admin/stats", admin, nil))
	for _, k := range []string{"enabled", "eventId", "soldOut", "waiting", "active", "capacity", "healthyServers", "servers",
		"activeUsersPerServer", "joinedTotal", "admittedTotal", "releasedTotal", "expiredTotal", "soldOutTotal",
		"admissionRatePerSecond", "waitMs", "oldestWaitingSeconds", "inventory", "capacitySource", "capacityError", "updatedAt"} {
		if _, ok := st[k]; !ok {
			t.Errorf("stats missing %q", k)
		}
	}
	if st["joinedTotal"] != float64(2) || st["admittedTotal"] != float64(1) || st["releasedTotal"] != float64(1) || st["capacitySource"] != "static" {
		t.Fatalf("stats %v", st)
	}
}

func TestLongPollClientDisconnectDoesNotLeak(t *testing.T) {
	e := newEnv(t)
	e.enable(1, 0)
	id := data(e.call("POST", "/queue/join", "", map[string]any{"eventId": ev}))["queueId"].(string)
	e.c.CloseIdleConnections()
	before := runtime.NumGoroutine()

	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	for range 50 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			req, _ := http.NewRequestWithContext(ctx, "GET", e.srv.URL+"/queue/status/"+id+"?waitSeconds=25", nil)
			if res, err := e.c.Do(req); err == nil {
				res.Body.Close()
			}
		}()
	}
	time.Sleep(100 * time.Millisecond)
	cancel()
	wg.Wait()
	e.c.CloseIdleConnections()
	deadline := time.Now().Add(3 * time.Second)
	for runtime.NumGoroutine() > before+3 {
		if time.Now().After(deadline) {
			buf := make([]byte, 1<<16)
			t.Fatalf("goroutines before %d now %d\n%s", before, runtime.NumGoroutine(), buf[:runtime.Stack(buf, true)])
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// TestLoadDrain: 5,000 customers join, 2,000 long-poll concurrently over HTTP, the real 100 ms
// admission loop drains the line at capacity 200 while customers release their slots.
func TestLoadDrain(t *testing.T) {
	if testing.Short() {
		t.Skip("load test")
	}
	const customers, pollers = 5000, 2000
	e := newEnv(t)
	e.c = &http.Client{Timeout: 30 * time.Second, Transport: &http.Transport{MaxIdleConnsPerHost: pollers, MaxIdleConns: pollers}}
	defer e.c.CloseIdleConnections()
	e.enable(50, 4)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	start := time.Now()
	ids := make([]string, customers)
	var jwg sync.WaitGroup
	for w := range 100 {
		jwg.Add(1)
		go func() {
			defer jwg.Done()
			for i := w; i < customers; i += 100 {
				r := e.call("POST", "/queue/join", "", map[string]any{"eventId": ev, "customerRef": fmt.Sprintf("c%d", i)})
				ids[i], _ = data(r)["queueId"].(string)
			}
		}()
	}
	jwg.Wait()
	if s := e.room.Stats(); s.Waiting != customers {
		t.Fatalf("waiting %d after joins", s.Waiting)
	}
	joined := time.Since(start)

	go e.room.Run(ctx, 100*time.Millisecond)
	var admitted, polls atomic.Int64
	var pwg sync.WaitGroup
	for w := range pollers {
		pwg.Add(1)
		go func() {
			defer pwg.Done()
			for i := w; i < customers; i += pollers {
				for {
					polls.Add(1)
					d := data(e.call("GET", "/queue/status/"+ids[i]+"?waitSeconds=25", "", nil))
					if d["status"] == "waiting" {
						continue
					}
					if d["status"] != "admitted" {
						t.Errorf("entry %d: %v", i, d)
						return
					}
					admitted.Add(1)
					e.call("POST", "/queue/release", "", map[string]any{"queueId": ids[i], "outcome": "purchased"})
					break
				}
			}
		}()
	}
	done := make(chan struct{})
	go func() { pwg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatalf("not drained in time: admitted %d, stats %+v", admitted.Load(), e.room.Stats())
	}
	s := e.room.Stats()
	t.Logf("joins %v, drain %v total, %d polls, stats waiting=%d admitted=%d released=%d waitP50=%dms p95=%dms rate=%.1f/s",
		joined, time.Since(start), polls.Load(), s.Waiting, s.AdmittedTotal, s.ReleasedTotal, s.WaitMs.P50, s.WaitMs.P95, s.AdmissionRatePerSecond)
	if admitted.Load() != customers || s.AdmittedTotal != customers || s.ReleasedTotal != customers || s.Waiting != 0 || s.Active != 0 {
		t.Fatalf("admitted %d, stats %+v", admitted.Load(), s)
	}
	if time.Since(start) > 10*time.Second {
		t.Fatalf("took %v", time.Since(start))
	}
}
