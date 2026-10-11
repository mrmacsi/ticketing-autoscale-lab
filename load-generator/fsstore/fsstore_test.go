package fsstore

import (
	"testing"
	"time"

	"github.com/mrmacsi/ticketing-autoscale-lab/load-generator/controller"
	"github.com/mrmacsi/ticketing-autoscale-lab/load-generator/metrics"
)

func TestDecodeCommand(t *testing.T) {
	issued := time.Now().UTC()
	cmd := DecodeCommand(map[string]any{
		"seq": int64(17), "action": "start", "experimentId": "exp_1", "issuedAt": issued, "issuedBy": "u@x",
		"config": map[string]any{
			"experimentId": "exp_1", "eventId": "e1", "seed": int64(42), "durationSeconds": float64(600),
			"pattern":     map[string]any{"type": "constant", "rate": 37.5},
			"mix":         map[string]any{"browse": int64(100)},
			"behaviour":   map[string]any{"maxQuantity": int64(4), "abandonCheckoutProbability": int64(0)},
			"futureField": "ignored",
			"waitingRoom": map[string]any{"enabled": true},
		},
	})
	if cmd.DecodeErr != nil || cmd.Seq != 17 || cmd.Action != "start" || !cmd.IssuedAt.Equal(issued) || cmd.IssuedBy != "u@x" {
		t.Fatalf("cmd = %+v", cmd)
	}
	c := cmd.Config
	if !c.WaitingRoomEnabled() {
		t.Fatalf("waitingRoom = %+v", c.WaitingRoom)
	}
	if c.DurationSeconds != 600 || c.Seed != 42 || c.Pattern.Rate != 37.5 || c.Mix.Browse != 100 || c.Behaviour.MaxQuantity != 4 {
		t.Fatalf("config = %+v", c)
	}
	bad := DecodeCommand(map[string]any{"seq": int64(2), "action": "start", "config": map[string]any{"durationSeconds": 1.5}})
	if bad.DecodeErr == nil || bad.Seq != 2 {
		t.Fatalf("expected decode error: %+v", bad)
	}
}

func TestBucketDocumentTypes(t *testing.T) {
	ts := time.Date(2026, 10, 9, 15, 30, 42, 0, time.UTC)
	p50 := 31.4
	m, err := BucketDocument(metrics.Bucket{
		Second: 42, TS: ts, TargetRate: 37.5, ScheduledArrivals: 38,
		Latency:   metrics.LatencyStats{Count: 2, SumMs: 10.5, P50: &p50, Hist: map[string]int64{"36": 2}},
		Instances: map[string]metrics.InstanceStats{"app-x7k2": {Requests: 2}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if m["ts"] != ts || m["second"] != int64(42) || m["scheduledArrivals"] != int64(38) || m["targetRate"] != 37.5 {
		t.Fatalf("doc = %#v", m)
	}
	lat := m["latency"].(map[string]any)
	if lat["p95"] != nil || lat["p50"] != 31.4 || lat["hist"].(map[string]any)["36"] != int64(2) {
		t.Fatalf("latency = %#v", lat)
	}
	if m["instances"].(map[string]any)["app-x7k2"].(map[string]any)["requests"] != int64(2) {
		t.Fatalf("instances = %#v", m["instances"])
	}
	if _, ok := m["requests"].(map[string]any)["conflict409"]; !ok {
		t.Fatal("contract field missing")
	}
}

func TestBucketDocumentQueue(t *testing.T) {
	p50 := 8140.6
	m, err := BucketDocument(metrics.Bucket{Queue: metrics.QueueStats{
		Joined: 40, Admitted: 25, Waiting: 1830,
		WaitMs:   metrics.LatencyStats{Count: 25, SumMs: 1.5e5, MaxMs: 9000, P50: &p50, Hist: map[string]int64{"95": 25}},
		Requests: metrics.QueueRequestCounts{Total: 130, OK: 129, Errors: 1},
	}})
	if err != nil {
		t.Fatal(err)
	}
	q := m["queue"].(map[string]any)
	if q["joined"] != int64(40) || q["admitted"] != int64(25) || q["soldOut"] != int64(0) || q["open"] != int64(0) ||
		q["failed"] != int64(0) || q["waiting"] != int64(1830) {
		t.Fatalf("queue = %#v", q)
	}
	w := q["waitMs"].(map[string]any)
	if w["count"] != int64(25) || w["p50"] != 8140.6 || w["p95"] != nil || w["hist"].(map[string]any)["95"] != int64(25) {
		t.Fatalf("waitMs = %#v", w)
	}
	r := q["requests"].(map[string]any)
	if r["total"] != int64(130) || r["ok"] != int64(129) || r["errors"] != int64(1) {
		t.Fatalf("requests = %#v", r)
	}
}

func TestGeneratorAndStatusMapping(t *testing.T) {
	now := time.Now().UTC()
	g := controller.Generator{Owner: "a", LeaseExpiresAt: now, State: "running", StartedAt: &now}
	m := EncodeGenerator(g)
	if m["finishedAt"] != nil || m["finishReason"] != nil {
		t.Fatalf("running generator must have null finish fields: %#v", m)
	}
	back := DecodeGenerator(m)
	if back.Owner != "a" || !back.StartedAt.Equal(now) || back.FinishedAt != nil {
		t.Fatalf("round trip = %+v", back)
	}
	st := EncodeStatus(controller.Status{AgentID: "a", State: controller.StateIdle, LastCommandSeq: 3})
	if st["experimentId"] != nil || st["runStartedAt"] != nil || st["lastCommandSeq"] != int64(3) {
		t.Fatalf("status = %#v", st)
	}
}
