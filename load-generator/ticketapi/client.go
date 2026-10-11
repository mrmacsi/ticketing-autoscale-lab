// Package ticketapi is a minimal HTTP client for the public ticketing API
// (docs/contracts.md §1). Every request goes to the single configured base
// URL; only path segments validated as opaque IDs are ever appended, and
// redirects are never followed, so the target host cannot change at runtime.
package ticketapi

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
	"regexp"
	"strings"
	"time"

	"github.com/mrmacsi/ticketing-autoscale-lab/load-generator/metrics"
)

const maxBodyBytes = 1 << 20

// Recorder receives one observation per completed request.
type Recorder interface {
	RecordRequest(metrics.Observation)
}

// Client talks to one ticketing API base URL.
type Client struct {
	base    *url.URL
	http    *http.Client
	timeout time.Duration
	rec     Recorder
}

// NewClient creates a client. httpClient should come from NewHTTPClient.
// timeout bounds every request (per-request deadline).
func NewClient(baseURL string, httpClient *http.Client, timeout time.Duration, rec Recorder) (*Client, error) {
	u, err := ParseBaseURL(baseURL)
	if err != nil {
		return nil, err
	}
	return &Client{base: u, http: httpClient, timeout: timeout, rec: rec}, nil
}

// ParseBaseURL validates a target base URL (http/https, host, no query,
// credentials or fragment).
func ParseBaseURL(raw string) (*url.URL, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return nil, fmt.Errorf("invalid target URL: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, fmt.Errorf("target URL must be http or https, got %q", u.Scheme)
	}
	if u.Hostname() == "" {
		return nil, errors.New("target URL has no host")
	}
	if u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return nil, errors.New("target URL must not contain credentials, query or fragment")
	}
	u.Path = strings.TrimRight(u.Path, "/")
	if u.Path == "" {
		u.Path = "/"
	}
	u.RawPath = ""
	return u, nil
}

// NewHTTPClient returns a client with one shared, tuned transport sized for
// maxConns concurrent virtual users. Proxies from the environment are
// ignored and redirects are not followed.
func NewHTTPClient(maxConns int, responseHeaderTimeout time.Duration) *http.Client {
	dialer := &net.Dialer{Timeout: 5 * time.Second, KeepAlive: 30 * time.Second}
	tr := &http.Transport{
		Proxy:                 nil,
		DialContext:           dialer.DialContext,
		MaxIdleConns:          maxConns,
		MaxIdleConnsPerHost:   maxConns,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   5 * time.Second,
		ResponseHeaderTimeout: responseHeaderTimeout,
		ExpectContinueTimeout: time.Second,
		DisableCompression:    true,
	}
	return &http.Client{
		Transport:     tr,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}

var idPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

// ValidID reports whether s is safe to use as a single path segment.
func ValidID(s string) bool { return idPattern.MatchString(s) }

// Request describes one API call.
type Request struct {
	Endpoint       metrics.Endpoint
	Method         string
	Segments       []string // path segments appended to the base path; IDs must pass ValidID
	Query          url.Values
	Body           any
	IdempotencyKey string
	// QueueToken, when set, is sent as X-Queue-Token (waiting-room admission).
	QueueToken string
}

// Response is the outcome of one API call.
type Response struct {
	Status   int
	Class    metrics.Class
	Body     []byte
	Replayed bool
	ServedBy string
	Latency  time.Duration
	Err      error
	// Aborted means the run itself was cancelled; such requests are not recorded.
	Aborted bool
}

// OK reports a 2xx response.
func (r Response) OK() bool { return r.Err == nil && r.Status >= 200 && r.Status < 300 }

// Unknown reports an outcome where the server may or may not have applied the
// request (timeout, connection failure, or a gateway error), which is when an
// idempotent retry with the same key is appropriate.
func (r Response) Unknown() bool {
	if r.Aborted {
		return false
	}
	switch r.Class {
	case metrics.ClassTimeout, metrics.ClassNetworkError:
		return true
	}
	return r.Status == http.StatusBadGateway || r.Status == http.StatusServiceUnavailable || r.Status == http.StatusGatewayTimeout
}

// ErrorCode returns the "error" field of an error body, if any.
func (r Response) ErrorCode() string {
	var e struct {
		Error string `json:"error"`
	}
	if json.Unmarshal(r.Body, &e) != nil {
		return ""
	}
	return e.Error
}

// Do performs the request against the base URL and records the observation.
func (c *Client) Do(ctx context.Context, req Request) Response {
	resp := c.do(ctx, req)
	if !resp.Aborted && c.rec != nil {
		c.rec.RecordRequest(metrics.Observation{
			Endpoint:    req.Endpoint,
			Class:       resp.Class,
			LatencyMs:   float64(resp.Latency.Microseconds()) / 1000,
			HasResponse: resp.Status != 0,
			Instance:    resp.ServedBy,
			Replayed:    resp.Replayed,
		})
	}
	return resp
}

func (c *Client) do(parent context.Context, req Request) Response {
	for _, s := range req.Segments {
		if s == "" || s == "." || s == ".." || strings.ContainsAny(s, "/?#%\\") {
			return Response{Class: metrics.ClassClient4xx, Err: fmt.Errorf("unsafe path segment %q", s)}
		}
	}
	u := c.base.JoinPath(req.Segments...)
	u.RawQuery = req.Query.Encode()

	var body io.Reader
	if req.Body != nil {
		buf, err := json.Marshal(req.Body)
		if err != nil {
			return Response{Class: metrics.ClassClient4xx, Err: err}
		}
		body = bytes.NewReader(buf)
	}
	ctx, cancel := context.WithTimeout(parent, c.timeout)
	defer cancel()
	hreq, err := http.NewRequestWithContext(ctx, req.Method, u.String(), body)
	if err != nil {
		return Response{Class: metrics.ClassClient4xx, Err: err}
	}
	hreq.Header.Set("Accept", "application/json")
	hreq.Header.Set("User-Agent", "ticketing-loadgen/1")
	if body != nil {
		hreq.Header.Set("Content-Type", "application/json")
	}
	if req.IdempotencyKey != "" {
		hreq.Header.Set("Idempotency-Key", req.IdempotencyKey)
	}
	if req.QueueToken != "" {
		hreq.Header.Set("X-Queue-Token", req.QueueToken)
	}

	start := time.Now()
	hresp, err := c.http.Do(hreq)
	if err != nil {
		return c.failure(parent, ctx, err, time.Since(start))
	}
	defer hresp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(hresp.Body, maxBodyBytes))
	if err == nil {
		// Drain any remainder so the connection can be reused.
		_, _ = io.Copy(io.Discard, hresp.Body)
	}
	lat := time.Since(start)
	if err != nil {
		return c.failure(parent, ctx, err, lat)
	}
	return Response{
		Status:   hresp.StatusCode,
		Class:    metrics.ClassifyStatus(hresp.StatusCode),
		Body:     data,
		Replayed: strings.EqualFold(hresp.Header.Get("Idempotent-Replayed"), "true"),
		ServedBy: hresp.Header.Get("X-Served-By"),
		Latency:  lat,
	}
}

func (c *Client) failure(parent, reqCtx context.Context, err error, lat time.Duration) Response {
	if parent.Err() != nil {
		return Response{Err: err, Aborted: true, Latency: lat}
	}
	var ne net.Error
	if errors.Is(reqCtx.Err(), context.DeadlineExceeded) || errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &ne) && ne.Timeout()) {
		return Response{Class: metrics.ClassTimeout, Err: err, Latency: lat}
	}
	return Response{Class: metrics.ClassNetworkError, Err: err, Latency: lat}
}

// DecodeData decodes the {"data": ...} envelope.
func DecodeData[T any](r Response) (T, error) {
	var env struct {
		Data T `json:"data"`
	}
	err := json.Unmarshal(r.Body, &env)
	return env.Data, err
}
