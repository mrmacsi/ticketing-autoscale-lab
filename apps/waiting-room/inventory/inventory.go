// Package inventory polls GET /api/events/{id}/availability on a healthy app server (round robin)
// and feeds the total available tickets to the room. Any failure, or no healthy server, makes the
// inventory unknown: the room then keeps admitting by capacity and never declares sold out.
package inventory

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"time"

	"github.com/mrmacsi/ticketing-autoscale-lab/waiting-room/gcp"
	"github.com/mrmacsi/ticketing-autoscale-lab/waiting-room/queue"
)

// Room is the part of the waiting room the poller uses (*queue.Room and every queue.Store).
type Room interface {
	Config() queue.Config
	HealthyServers() []queue.Server
	SetInventory(eventID string, available, held int, appSoldOut bool, at time.Time)
	SetInventoryUnknown(eventID, reason string)
}

// Poller reads availability for the room's configured event.
type Poller struct {
	Room     Room
	Client   *http.Client
	Interval time.Duration
	Log      *slog.Logger

	next    int
	lastErr string
}

// Run polls every Interval until ctx is done.
func (p *Poller) Run(ctx context.Context) {
	t := time.NewTicker(p.Interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			p.Poll(ctx)
		}
	}
}

// Poll performs one observation (serial use only).
func (p *Poller) Poll(ctx context.Context) {
	cfg := p.Room.Config()
	if !cfg.Enabled || cfg.EventID == "" {
		return
	}
	servers := p.Room.HealthyServers()
	if len(servers) == 0 {
		p.unknown(cfg.EventID, "no healthy server")
		return
	}
	srv := servers[p.next%len(servers)]
	p.next++
	available, held, soldOut, err := p.fetch(ctx, srv.BaseURL, cfg.EventID)
	if err != nil {
		if ctx.Err() == nil {
			p.unknown(cfg.EventID, fmt.Sprintf("%s: %v", srv.Name, err))
		}
		return
	}
	if p.lastErr != "" && p.Log != nil {
		p.Log.Info("inventory recovered", "available", available)
	}
	p.lastErr = ""
	p.Room.SetInventory(cfg.EventID, available, held, soldOut, time.Now())
}

func (p *Poller) unknown(eventID, reason string) {
	if reason != p.lastErr && p.Log != nil {
		p.Log.Warn("inventory unknown", "reason", reason)
	}
	p.lastErr = reason
	p.Room.SetInventoryUnknown(eventID, reason)
}

// Availability is the part of the ticketing API response the room uses.
type Availability struct {
	Data *struct {
		EventID    string `json:"event_id"`
		SoldOut    bool   `json:"sold_out"`
		Categories []struct {
			Available *int `json:"available"`
			Held      *int `json:"held"`
		} `json:"categories"`
	} `json:"data"`
}

// Parse returns the total available tickets, the total held in checkouts (-1 when any category does
// not report it) and the app's sold_out flag.
func Parse(body []byte) (available, held int, soldOut bool, err error) {
	var a Availability
	if err := json.Unmarshal(body, &a); err != nil {
		return 0, -1, false, fmt.Errorf("malformed availability: %w", err)
	}
	if a.Data == nil || a.Data.Categories == nil {
		return 0, -1, false, fmt.Errorf("malformed availability: missing data.categories")
	}
	for _, c := range a.Data.Categories {
		if c.Available == nil {
			return 0, -1, false, fmt.Errorf("malformed availability: category without available")
		}
		available += max(*c.Available, 0)
		if c.Held == nil || held < 0 {
			held = -1
		} else {
			held += max(*c.Held, 0)
		}
	}
	return available, held, a.Data.SoldOut, nil
}

func (p *Poller) fetch(ctx context.Context, base, eventID string) (int, int, bool, error) {
	timeout := min(max(p.Interval, 500*time.Millisecond), 3*time.Second)
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		base+"/api/events/"+url.PathEscape(eventID)+"/availability", nil)
	if err != nil {
		return 0, -1, false, err
	}
	req.Header.Set("Accept", "application/json")
	body, err := gcp.Do(p.Client, req)
	if err != nil {
		return 0, -1, false, err
	}
	return Parse(body)
}
