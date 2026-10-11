package signals

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mrmacsi/ticketing-autoscale-lab/waiting-room/queue"
	"github.com/mrmacsi/ticketing-autoscale-lab/waiting-room/token"
)

// Real nginx 1.30 stub_status output (GET /lab/load on the local stack with 64 concurrent requests
// on a 16-worker pool): 63 in flight besides the status request itself.
const stubStatus = "Active connections: 66 \nserver accepts handled requests\n 1200 1200 1290 \nReading: 0 Writing: 64 Waiting: 2 \n"

func TestParseLoad(t *testing.T) {
	inFlight, workers, err := ParseLoad([]byte(stubStatus), "16")
	if err != nil || inFlight != 63 || workers != 16 {
		t.Fatalf("%d %d %v", inFlight, workers, err)
	}
	if n, _, _ := ParseLoad([]byte("Reading: 0 Writing: 1 Waiting: 0"), "16"); n != 0 {
		t.Fatalf("only the status request in flight: %d", n)
	}
	if _, _, err := ParseLoad([]byte("nope"), "16"); err == nil {
		t.Error("missing Writing accepted")
	}
	if _, _, err := ParseLoad([]byte(stubStatus), ""); err == nil {
		t.Error("missing worker header accepted")
	}
}

func TestParseCPU(t *testing.T) {
	now := time.Date(2026, 10, 10, 19, 16, 44, 0, time.UTC)
	if c, _ := ParseCPU([]byte(`{"percent":74.2,"sampled_at":"2026-10-10T19:16:42Z"}`), now); c < 0.741 || c > 0.743 {
		t.Fatalf("cpu %v", c)
	}
	if c, _ := ParseCPU([]byte(`{"percent":74.2,"sampled_at":"2026-10-10T19:10:00Z"}`), now); c != -1 {
		t.Fatalf("stale sample must be unknown, got %v", c)
	}
	if c, _ := ParseCPU([]byte(`{}`), now); c != -1 {
		t.Fatalf("missing sample must be unknown, got %v", c)
	}
}

func TestPollerFeedsControllerAndSurvivesFailures(t *testing.T) {
	var cpuCalls atomic.Int64
	app := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/lab/load":
			w.Header().Set("X-Php-Workers", "32")
			w.Write([]byte("Active connections: 40 \nReading: 0 Writing: 37 Waiting: 3 \n")) // 36 in flight of 32 workers
		case "/lab/cpu":
			cpuCalls.Add(1)
			w.Write([]byte(`{"percent":55,"sampled_at":"` + time.Now().UTC().Format(time.RFC3339) + `"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer app.Close()
	broken := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden) // e.g. called through the load balancer
	}))
	defer broken.Close()

	room := queue.New(token.NewSigner("x"), nil)
	cfg := queue.DefaultConfig()
	cfg.Enabled, cfg.EventID, cfg.AdmissionMode = true, "11111111-2222-3333-4444-555555555555", queue.ModeAdaptive
	room.Configure(cfg)
	room.SetCapacity(queue.CapacityUpdate{Healthy: 2, Source: "static", Servers: []queue.Server{
		{Name: "ok", BaseURL: app.URL}, {Name: "broken", BaseURL: broken.URL}}})

	p := &Poller{Room: room, Client: app.Client(), Interval: time.Second}
	p.Poll(context.Background())
	views := map[string]queue.ServerView{}
	for _, v := range room.Stats().Servers {
		views[v.Name] = v
	}
	ok := views["ok"].Signals
	if ok == nil || ok.ActiveWorkers != 32 || ok.TotalWorkers != 32 || ok.ListenQueue != 4 || ok.Busy != 1 || ok.CPU == nil || *ok.CPU != 0.55 {
		t.Fatalf("ok signals %+v", ok)
	}
	if views["broken"].Signals != nil {
		t.Fatal("a server answering 403 must have no signals")
	}
	if cpuCalls.Load() != 1 {
		t.Fatalf("cpu calls %d", cpuCalls.Load())
	}
	// The shared limit is cut because one server is overloaded; the unreachable server still has no
	// signals of its own.
	room.Control()
	for _, v := range room.Stats().Servers {
		if v.Decision != queue.DecisionDecrease || v.Limit != 18 || v.Reason != "listen queue 4 on ok" {
			t.Fatalf("%s: %+v", v.Name, v)
		}
		if v.Name == "broken" && v.Signals != nil {
			t.Fatalf("broken server has signals: %+v", v.Signals)
		}
	}
}
