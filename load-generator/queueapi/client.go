// Package queueapi is a minimal HTTP client for the waiting room's public API
// (docs/waiting-room.md §3.1). It uses its own transport, sized for thousands
// of concurrent long polls, and reports every request to a Recorder that is
// kept apart from the application request metrics: queue calls never appear
// in a bucket's requests, latency, byEndpoint or instances.
package queueapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/mrmacsi/ticketing-autoscale-lab/load-generator/ticketapi"
)

// Queue statuses.
const (
	StatusWaiting  = "waiting"
	StatusAdmitted = "admitted"
	StatusSoldOut  = "sold_out"
	StatusOpen     = "open"
)

// Release outcomes.
const (
	ReleasePurchased = "purchased"
	ReleaseSoldOut   = "sold_out"
	ReleaseAbandoned = "abandoned"
	ReleaseFailed    = "failed"
)

// TokenHeader carries the admission token on POST /api/reservations.
const TokenHeader = "X-Queue-Token"

// DefaultWaitSeconds is the long-poll duration of a status request.
const DefaultWaitSeconds = 15

const (
	maxBodyBytes = 64 << 10
	// pollSlack is added to waitSeconds for the per-request deadline.
	pollSlack = 10 * time.Second
	// JoinTimeout bounds a join request.
	JoinTimeout = 10 * time.Second
	// ReleaseTimeout bounds a release request (best effort).
	ReleaseTimeout = 5 * time.Second
)

// Status is the status object returned by join and status.
type Status struct {
	QueueID              string   `json:"queueId"`
	EventID              string   `json:"eventId"`
	Status               string   `json:"status"`
	Position             int64    `json:"position"`
	Waiting              int64    `json:"waiting"`
	EstimatedWaitSeconds *float64 `json:"estimatedWaitSeconds"`
	Token                string   `json:"token,omitempty"`
	TokenExpiresAt       string   `json:"tokenExpiresAt,omitempty"`
	PollAfterMs          int64    `json:"pollAfterMs"`
}

// Recorder receives one call per completed queue request (aborted requests
// are not reported). ok is true for a 2xx response.
type Recorder interface {
	RecordQueueRequest(ok bool)
}

// Error is a failed queue request.
type Error struct {
	// Status is the HTTP status (0 when no response was received).
	Status int
	// Code is the "error" field of the body, if any.
	Code string
	Err  error
}

func (e *Error) Error() string {
	if e.Status == 0 {
		return fmt.Sprintf("queue request failed: %v", e.Err)
	}
	if e.Code != "" {
		return fmt.Sprintf("queue request: HTTP %d %s", e.Status, e.Code)
	}
	return fmt.Sprintf("queue request: HTTP %d", e.Status)
}

func (e *Error) Unwrap() error { return e.Err }

// Retryable reports a transient failure (network error, timeout, 5xx, 429)
// worth retrying with backoff.
func (e *Error) Retryable() bool {
	return e.Status == 0 || e.Status >= 500 || e.Status == http.StatusTooManyRequests
}

// IsRetryable reports whether err is a retryable *Error.
func IsRetryable(err error) bool {
	var qe *Error
	return errors.As(err, &qe) && qe.Retryable()
}

// IsUnknownQueueID reports a 404 unknown_queue_id (the queue lost the entry,
// e.g. after a restart; the customer must join again).
func IsUnknownQueueID(err error) bool {
	var qe *Error
	return errors.As(err, &qe) && qe.Status == http.StatusNotFound && qe.Code == "unknown_queue_id"
}

// Client talks to one waiting room base URL.
type Client struct {
	base *url.URL
	http *http.Client
	rec  Recorder
}

// NewClient creates a client. httpClient should come from NewHTTPClient.
func NewClient(baseURL string, httpClient *http.Client, rec Recorder) (*Client, error) {
	u, err := ticketapi.ParseBaseURL(baseURL)
	if err != nil {
		return nil, fmt.Errorf("queue base URL: %w", err)
	}
	return &Client{base: u, http: httpClient, rec: rec}, nil
}

// NewHTTPClient returns a client with a transport of its own, separate from
// the application transport, sized for maxConns concurrent long polls: idle
// connections are kept per host, there is no per-host connection cap and no
// response-header timeout (each request carries its own deadline). Proxies
// are ignored and redirects are not followed.
func NewHTTPClient(maxConns int) *http.Client {
	maxConns = max(maxConns, 64)
	dialer := &net.Dialer{Timeout: 5 * time.Second, KeepAlive: 30 * time.Second}
	tr := &http.Transport{
		Proxy:                 nil,
		DialContext:           dialer.DialContext,
		MaxIdleConns:          maxConns,
		MaxIdleConnsPerHost:   maxConns,
		MaxConnsPerHost:       0,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   5 * time.Second,
		ExpectContinueTimeout: time.Second,
		DisableCompression:    true,
	}
	return &http.Client{
		Transport:     tr,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}

// Join calls POST /queue/join.
func (c *Client) Join(ctx context.Context, eventID, customerRef string) (Status, error) {
	var st Status
	err := c.call(ctx, JoinTimeout, http.MethodPost, []string{"queue", "join"}, nil,
		map[string]string{"eventId": eventID, "customerRef": customerRef}, &st)
	return st, err
}

// Status calls GET /queue/status/{queueId}?waitSeconds=N (long poll). The
// request deadline is waitSeconds + 10 s.
func (c *Client) Status(ctx context.Context, queueID string, waitSeconds int) (Status, error) {
	var st Status
	if !ticketapi.ValidID(queueID) {
		return st, &Error{Status: http.StatusBadRequest, Code: "invalid_queue_id", Err: fmt.Errorf("unsafe queue id %q", queueID)}
	}
	waitSeconds = min(max(waitSeconds, 0), 25)
	q := url.Values{"waitSeconds": {strconv.Itoa(waitSeconds)}}
	err := c.call(ctx, time.Duration(waitSeconds)*time.Second+pollSlack, http.MethodGet,
		[]string{"queue", "status", queueID}, q, nil, &st)
	return st, err
}

// Release calls POST /queue/release (single attempt, ReleaseTimeout).
func (c *Client) Release(ctx context.Context, queueID, outcome string) error {
	var out struct {
		Released bool `json:"released"`
	}
	return c.call(ctx, ReleaseTimeout, http.MethodPost, []string{"queue", "release"}, nil,
		map[string]string{"queueId": queueID, "outcome": outcome}, &out)
}

func (c *Client) call(parent context.Context, timeout time.Duration, method string, segments []string, query url.Values, body any, out any) error {
	u := c.base.JoinPath(segments...)
	u.RawQuery = query.Encode()
	var rd io.Reader
	if body != nil {
		buf, err := json.Marshal(body)
		if err != nil {
			return &Error{Status: http.StatusBadRequest, Err: err}
		}
		rd = bytes.NewReader(buf)
	}
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, method, u.String(), rd)
	if err != nil {
		return &Error{Status: http.StatusBadRequest, Err: err}
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "ticketing-loadgen/1")
	if rd != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		if parent.Err() != nil {
			return parent.Err() // aborted: not recorded
		}
		c.record(false)
		return &Error{Err: err}
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes))
	if err == nil {
		_, _ = io.Copy(io.Discard, resp.Body)
	}
	if err != nil {
		if parent.Err() != nil {
			return parent.Err()
		}
		c.record(false)
		return &Error{Err: err}
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		c.record(false)
		var e struct {
			Error string `json:"error"`
		}
		_ = json.Unmarshal(data, &e)
		return &Error{Status: resp.StatusCode, Code: e.Error}
	}
	env := struct {
		Data any `json:"data"`
	}{Data: out}
	if err := json.Unmarshal(data, &env); err != nil {
		// A 2xx with an unreadable body is treated as a transient gateway fault.
		c.record(false)
		return &Error{Err: fmt.Errorf("decode queue response: %w", err)}
	}
	c.record(true)
	return nil
}

func (c *Client) record(ok bool) {
	if c.rec != nil {
		c.rec.RecordQueueRequest(ok)
	}
}

// Backoff returns the delay before retry number n (1-based) of a failed
// queue request: 1 s, 2 s, 4 s, then 5 s.
func Backoff(n int) time.Duration {
	if n < 1 {
		n = 1
	}
	if n > 3 {
		return 5 * time.Second
	}
	return min(time.Second<<(n-1), 5*time.Second)
}
