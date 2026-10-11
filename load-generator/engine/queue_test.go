package engine

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/mrmacsi/ticketing-autoscale-lab/load-generator/config"
	"github.com/mrmacsi/ticketing-autoscale-lab/load-generator/internal/fakeapi"
)

func waitingRoomConfig(rate float64, seconds int) config.TrafficConfig {
	return config.TrafficConfig{
		ExperimentID: "exp_queue", EventID: "evt-1", Seed: 7, DurationSeconds: seconds, MaxVUs: 500,
		RequestTimeoutMs: 5000,
		Pattern:          config.Pattern{Type: "constant", Rate: rate},
		Mix:              config.Mix{Purchase: 100},
		Behaviour:        config.Behaviour{MaxQuantity: 1, ThinkTimeMinMs: 10, ThinkTimeMaxMs: 20},
		WaitingRoom:      &config.WaitingRoomConfig{Enabled: true},
	}
}

func TestWaitingRoomRunAccounting(t *testing.T) {
	q := fakeapi.NewQueue(fakeapi.QueueOptions{Capacity: 1})
	srv := fakeapi.New(fakeapi.Options{Queue: q, RequireQueueToken: true, Delay: 20 * time.Millisecond, FastEventFetch: true})
	defer srv.Close()
	res, buckets := runEngine(t, context.Background(), srv, waitingRoomConfig(15, 2), testLimits)
	if res.Err != nil || res.Reason != ReasonCompleted {
		t.Fatalf("result = %+v", res)
	}
	checkContiguous(t, buckets)

	var started, appReqs, queueReqs, joined, admitted, purchases, peakWaiting int64
	for _, b := range buckets {
		started += b.StartedArrivals
		appReqs += b.Requests.Total
		queueReqs += b.Queue.Requests.Total
		joined += b.Queue.Joined
		admitted += b.Queue.Admitted
		purchases += b.Business.PurchasesCompleted
		peakWaiting = max(peakWaiting, b.Queue.Waiting)
		if _, ok := b.Instances["waiting-room"]; ok {
			t.Fatal("queue responses counted as an app instance")
		}
	}
	st, qs := srv.Stats(), q.Stats()
	if started == 0 || joined != started || admitted != started || purchases != started {
		t.Fatalf("started=%d joined=%d admitted=%d purchases=%d", started, joined, admitted, purchases)
	}
	if appReqs != st.Requests-1 { // minus the setup event fetch
		t.Fatalf("app requests in buckets = %d, server = %d", appReqs, st.Requests-1)
	}
	if queueReqs != qs.Requests || st.QueueTokenRejected != 0 || qs.Released["purchased"] != started {
		t.Fatalf("queue requests = %d server %d, rejected=%d released=%v", queueReqs, qs.Requests, st.QueueTokenRejected, qs.Released)
	}
	if peakWaiting == 0 {
		t.Fatal("with capacity 1 some customers must have waited")
	}
	if last := buckets[len(buckets)-1]; last.Queue.Waiting != 0 {
		t.Fatalf("final waiting gauge = %d", last.Queue.Waiting)
	}
	sq := res.Summary.Queue
	if sq.Joined != joined || sq.Admitted != admitted || sq.PeakWaiting != peakWaiting || sq.WaitMs.Count != admitted || sq.Requests.Total != queueReqs {
		t.Fatalf("summary queue = %+v", sq)
	}

	// Wire shape: camelCase fields in a "queue" object, waitMs like latency.
	raw, _ := json.Marshal(buckets[0])
	var doc map[string]any
	_ = json.Unmarshal(raw, &doc)
	qd, ok := doc["queue"].(map[string]any)
	if !ok {
		t.Fatalf("bucket JSON has no queue object: %s", raw)
	}
	for _, k := range []string{"joined", "admitted", "soldOut", "open", "failed", "waiting", "waitMs", "requests"} {
		if _, ok := qd[k]; !ok {
			t.Errorf("queue.%s missing", k)
		}
	}
	wm := qd["waitMs"].(map[string]any)
	for _, k := range []string{"count", "sumMs", "maxMs", "p50", "p95", "p99", "hist"} {
		if _, ok := wm[k]; !ok {
			t.Errorf("queue.waitMs.%s missing", k)
		}
	}
	rq := qd["requests"].(map[string]any)
	for _, k := range []string{"total", "ok", "errors"} {
		if _, ok := rq[k]; !ok {
			t.Errorf("queue.requests.%s missing", k)
		}
	}
}

func TestWaitingRoomDeadlineIsDurationPlusGrace(t *testing.T) {
	// Nobody is ever admitted. The arrival cap is hit at once, but queued
	// customers may wait until durationSeconds + grace, then are aborted.
	q := fakeapi.NewQueue(fakeapi.QueueOptions{Capacity: 0})
	srv := fakeapi.New(fakeapi.Options{Queue: q, RequireQueueToken: true})
	defer srv.Close()
	cfg := waitingRoomConfig(100, 2)
	cfg.TotalArrivals = 3
	sink := &memSink{}
	start := time.Now()
	res := Run(context.Background(), cfg, Options{TargetBaseURL: srv.URL, QueueBaseURL: srv.URL, Limits: testLimits, Sink: sink, DrainGrace: time.Second})
	el := time.Since(start)
	if res.Reason != ReasonTotalArrivals {
		t.Fatalf("reason = %s", res.Reason)
	}
	if el < 2900*time.Millisecond || el > 4500*time.Millisecond {
		t.Fatalf("run took %s, want ~3 s (duration 2 s + grace 1 s)", el)
	}
	if res.Summary.Queue.Joined != 3 || res.Summary.Queue.PeakWaiting != 3 || res.Summary.Requests.Total != 0 {
		t.Fatalf("summary = %+v requests=%+v", res.Summary.Queue, res.Summary.Requests)
	}
	all := sink.all()
	if last := all[len(all)-1]; last.Queue.Waiting != 0 {
		t.Fatalf("final waiting gauge = %d", last.Queue.Waiting)
	}
}

func TestWaitingRoomHardStopIsPrompt(t *testing.T) {
	q := fakeapi.NewQueue(fakeapi.QueueOptions{Capacity: 0})
	srv := fakeapi.New(fakeapi.Options{Queue: q})
	defer srv.Close()
	ctx, cancel := context.WithCancelCause(context.Background())
	defer cancel(nil)
	var stoppedAt time.Time
	go func() {
		deadline := time.Now().Add(5 * time.Second)
		for q.Waiting() < 10 && time.Now().Before(deadline) {
			time.Sleep(10 * time.Millisecond)
		}
		stoppedAt = time.Now()
		cancel(&StopCause{Reason: ReasonStopped, Message: "stop"})
	}()
	res := Run(ctx, waitingRoomConfig(50, 30), Options{TargetBaseURL: srv.URL, Limits: testLimits, Sink: &memSink{}})
	if res.Reason != ReasonStopped {
		t.Fatalf("reason = %s", res.Reason)
	}
	if lat := time.Since(stoppedAt); lat > time.Second {
		t.Fatalf("stop took %s with customers long-polling", lat)
	}
}

func TestWaitingRoomBadQueueURLFails(t *testing.T) {
	srv := fakeapi.New(fakeapi.Options{})
	defer srv.Close()
	res := Run(context.Background(), waitingRoomConfig(5, 1), Options{TargetBaseURL: srv.URL, QueueBaseURL: "ftp://x", Limits: testLimits})
	if res.Reason != ReasonError || !strings.Contains(res.Message, "queue base URL") {
		t.Fatalf("result = %+v", res)
	}
}
