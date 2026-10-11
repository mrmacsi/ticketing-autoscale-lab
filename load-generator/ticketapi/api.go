package ticketapi

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strconv"

	"github.com/mrmacsi/ticketing-autoscale-lab/load-generator/metrics"
)

// Event is the subset of the Event type the generator needs.
type Event struct {
	ID         string     `json:"id"`
	Name       string     `json:"name"`
	Categories []Category `json:"categories"`
}

// Category of an event.
type Category struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	PriceCents int64  `json:"price_cents"`
	Capacity   int64  `json:"capacity"`
}

// Availability is GET /api/events/{id}/availability data.
type Availability struct {
	EventID    string                 `json:"event_id"`
	SoldOut    bool                   `json:"sold_out"`
	Categories []CategoryAvailability `json:"categories"`
}

// CategoryAvailability is one category's approximate remaining stock.
type CategoryAvailability struct {
	ID        string `json:"id"`
	Available int64  `json:"available"`
}

// Reservation is the subset of the Reservation type the generator needs.
type Reservation struct {
	ID       string `json:"id"`
	Status   string `json:"status"`
	Quantity int    `json:"quantity"`
}

// Order is the subset of the Order type the generator needs.
type Order struct {
	ID      string   `json:"id"`
	Status  string   `json:"status"`
	Tickets []Ticket `json:"tickets"`
}

// Ticket issued for a paid order.
type Ticket struct {
	ID string `json:"id"`
}

// ListEvents calls GET /api/events.
func (c *Client) ListEvents(ctx context.Context) Response {
	return c.Do(ctx, Request{Endpoint: metrics.EndpointBrowse, Method: http.MethodGet, Segments: []string{"api", "events"}})
}

// GetEvent calls GET /api/events/{id}.
func (c *Client) GetEvent(ctx context.Context, eventID string) Response {
	return c.Do(ctx, Request{Endpoint: metrics.EndpointBrowse, Method: http.MethodGet, Segments: []string{"api", "events", eventID}})
}

// GetAvailability calls GET /api/events/{id}/availability.
func (c *Client) GetAvailability(ctx context.Context, eventID string) Response {
	return c.Do(ctx, Request{Endpoint: metrics.EndpointAvailability, Method: http.MethodGet, Segments: []string{"api", "events", eventID, "availability"}})
}

// ReservationRequest builds POST /api/reservations.
func ReservationRequest(key, categoryID string, quantity int, customerRef string) Request {
	return Request{
		Endpoint: metrics.EndpointReservation, Method: http.MethodPost, Segments: []string{"api", "reservations"},
		IdempotencyKey: key,
		Body:           map[string]any{"category_id": categoryID, "quantity": quantity, "customer_ref": customerRef},
	}
}

// OrderRequest builds POST /api/orders.
func OrderRequest(key, reservationID, customerRef string) Request {
	return Request{
		Endpoint: metrics.EndpointOrder, Method: http.MethodPost, Segments: []string{"api", "orders"},
		IdempotencyKey: key,
		Body:           map[string]any{"reservation_id": reservationID, "customer_ref": customerRef},
	}
}

// PaymentRequest builds POST /api/orders/{id}/simulate-payment.
func PaymentRequest(key, orderID string) Request {
	return Request{
		Endpoint: metrics.EndpointPayment, Method: http.MethodPost, Segments: []string{"api", "orders", orderID, "simulate-payment"},
		IdempotencyKey: key,
		Body:           map[string]any{},
	}
}

// CPUWork calls the demo-only GET /api/lab/cpu-work?ms=N.
func (c *Client) CPUWork(ctx context.Context, ms int) Response {
	return c.Do(ctx, Request{
		Endpoint: metrics.EndpointCPUWork, Method: http.MethodGet, Segments: []string{"api", "lab", "cpu-work"},
		Query: url.Values{"ms": {strconv.Itoa(ms)}},
	})
}

// FetchEvent loads an event and its categories (run setup).
func (c *Client) FetchEvent(ctx context.Context, eventID string) (Event, error) {
	if !ValidID(eventID) {
		return Event{}, fmt.Errorf("invalid event id %q", eventID)
	}
	r := c.do(ctx, Request{Method: http.MethodGet, Segments: []string{"api", "events", eventID}})
	if r.Err != nil {
		return Event{}, fmt.Errorf("fetch event: %w", r.Err)
	}
	if !r.OK() {
		return Event{}, fmt.Errorf("fetch event: HTTP %d %s", r.Status, r.ErrorCode())
	}
	ev, err := DecodeData[Event](r)
	if err != nil {
		return Event{}, fmt.Errorf("decode event: %w", err)
	}
	valid := ev.Categories[:0]
	for _, cat := range ev.Categories {
		if ValidID(cat.ID) {
			valid = append(valid, cat)
		}
	}
	ev.Categories = valid
	if len(ev.Categories) == 0 {
		return Event{}, fmt.Errorf("event %s has no categories", eventID)
	}
	return ev, nil
}
