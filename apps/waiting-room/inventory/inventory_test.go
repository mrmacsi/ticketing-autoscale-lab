package inventory

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mrmacsi/ticketing-autoscale-lab/waiting-room/queue"
	"github.com/mrmacsi/ticketing-autoscale-lab/waiting-room/token"
)

const ev = "11111111-2222-3333-4444-555555555555"

func TestPoll(t *testing.T) {
	var body atomic.Value
	body.Store(`{"data":{"event_id":"` + ev + `","sold_out":false,"categories":[{"id":"c1","available":5},{"id":"c2","available":7}],"as_of":"x"}}`)
	app := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/events/"+ev+"/availability" {
			http.NotFound(w, r)
			return
		}
		io.WriteString(w, body.Load().(string))
	}))
	defer app.Close()

	room := queue.New(token.NewSigner("k"), nil)
	p := &Poller{Room: room, Client: app.Client(), Interval: time.Second}
	p.Poll(context.Background()) // disabled: no-op
	if room.Stats().Inventory != nil {
		t.Fatal("disabled room polled")
	}
	room.Configure(queue.Config{Enabled: true, EventID: ev, ActiveUsersPerServer: 1, AdmissionTTLSeconds: 60})

	p.Poll(context.Background())
	if s := room.Stats(); s.Inventory != nil || s.InventoryError == nil || *s.InventoryError != "no healthy server" {
		t.Fatalf("no servers: %+v", s)
	}

	room.SetCapacity(queue.CapacityUpdate{Healthy: 1, Servers: []queue.Server{{Name: "app", IP: "127.0.0.1", BaseURL: app.URL}}})
	p.Poll(context.Background())
	if s := room.Stats(); s.Inventory == nil || s.Inventory.Available != 12 || s.Inventory.SoldOut || s.InventoryError != nil {
		t.Fatalf("available: %+v", s.Inventory)
	}

	body.Store(`{"data":{"event_id":"` + ev + `","sold_out":true,"categories":[{"available":0},{"available":0}]}}`)
	p.Poll(context.Background())
	if s := room.Stats(); s.Inventory == nil || s.Inventory.Available != 0 || !s.Inventory.SoldOut || s.InventoryZeroSince == nil {
		t.Fatalf("zero: %+v", s)
	}

	body.Store(`<html>502</html>`)
	p.Poll(context.Background())
	if s := room.Stats(); s.Inventory != nil || s.InventoryError == nil {
		t.Fatalf("error should make inventory unknown: %+v", s)
	}
}

func TestParse(t *testing.T) {
	for _, bad := range []string{`{}`, `{"data":{}}`, `{"data":{"categories":[{"id":"x"}]}}`, `nope`} {
		if _, _, _, err := Parse([]byte(bad)); err == nil {
			t.Errorf("%s: expected error", bad)
		}
	}
	n, _, so, err := Parse([]byte(`{"data":{"sold_out":true,"categories":[]}}`))
	if err != nil || n != 0 || !so {
		t.Fatalf("%d %v %v", n, so, err)
	}
	n, h, _, err := Parse([]byte(`{"data":{"sold_out":false,"categories":[{"available":3,"held":4},{"available":2,"held":1}]}}`))
	if err != nil || n != 5 || h != 5 {
		t.Fatalf("with holds: %d %d %v", n, h, err)
	}
	// An app that does not report holds (or reports them for only some categories) gives held = -1.
	_, h, _, err = Parse([]byte(`{"data":{"sold_out":false,"categories":[{"available":3},{"available":2,"held":1}]}}`))
	if err != nil || h != -1 {
		t.Fatalf("missing holds: %d %v", h, err)
	}
}
