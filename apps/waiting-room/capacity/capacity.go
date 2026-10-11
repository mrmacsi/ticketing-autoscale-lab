// Package capacity tells the waiting room how many app servers the load balancer can route to.
//
//   - gce: POST compute/v1/projects/{p}/global/backendServices/{bs}/getHealth for the app instance
//     group and count healthStatus[].healthState == "HEALTHY".
//   - static: a list of base URLs; each is healthy while GET {base}/health answers 200 (local lab).
package capacity

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"path"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/mrmacsi/ticketing-autoscale-lab/waiting-room/gcp"
	"github.com/mrmacsi/ticketing-autoscale-lab/waiting-room/queue"
)

// Source lists the currently healthy app servers.
type Source interface {
	Name() string
	Healthy(ctx context.Context) ([]queue.Server, error)
}

// GCE reads backend health from the Compute Engine API.
type GCE struct {
	Project        string
	BackendService string
	Group          string // instance group URL
	AppPort        int
	Endpoint       string // default https://compute.googleapis.com/compute/v1
	Tokens         gcp.TokenSource
	Client         *http.Client
}

func (g *GCE) Name() string { return "gce" }

func (g *GCE) Healthy(ctx context.Context) ([]queue.Server, error) {
	endpoint := g.Endpoint
	if endpoint == "" {
		endpoint = "https://compute.googleapis.com/compute/v1"
	}
	tok, err := g.Tokens.Token(ctx)
	if err != nil {
		return nil, err
	}
	body, _ := json.Marshal(map[string]string{"group": g.Group})
	u := fmt.Sprintf("%s/projects/%s/global/backendServices/%s/getHealth", endpoint,
		url.PathEscape(g.Project), url.PathEscape(g.BackendService))
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Content-Type", "application/json")
	resp, err := gcp.Do(g.Client, req)
	if err != nil {
		return nil, fmt.Errorf("getHealth: %w", err)
	}
	return ParseGetHealth(resp, g.AppPort)
}

// ParseGetHealth extracts the HEALTHY backends from a backendServices.getHealth response.
// An empty group answers without healthStatus, which is zero healthy servers, not an error.
func ParseGetHealth(body []byte, appPort int) ([]queue.Server, error) {
	var r struct {
		HealthStatus []struct {
			IPAddress   string `json:"ipAddress"`
			Instance    string `json:"instance"`
			HealthState string `json:"healthState"`
		} `json:"healthStatus"`
	}
	if err := json.Unmarshal(body, &r); err != nil {
		return nil, fmt.Errorf("getHealth: malformed response: %w", err)
	}
	servers := []queue.Server{}
	for _, h := range r.HealthStatus {
		if h.HealthState != "HEALTHY" || h.IPAddress == "" {
			continue
		}
		servers = append(servers, queue.Server{
			Name:    path.Base(h.Instance),
			IP:      h.IPAddress,
			BaseURL: "http://" + net.JoinHostPort(h.IPAddress, strconv.Itoa(appPort)),
		})
	}
	sort.Slice(servers, func(i, j int) bool { return servers[i].Name < servers[j].Name })
	return servers, nil
}

// Static probes a fixed list of base URLs.
type Static struct {
	BaseURLs []string
	Client   *http.Client
}

func (s *Static) Name() string { return "static" }

func (s *Static) Healthy(ctx context.Context) ([]queue.Server, error) {
	ok := make([]bool, len(s.BaseURLs))
	var wg sync.WaitGroup
	for i, base := range s.BaseURLs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/health", nil)
			if err != nil {
				return
			}
			resp, err := s.Client.Do(req)
			if err != nil {
				return
			}
			resp.Body.Close()
			ok[i] = resp.StatusCode == http.StatusOK
		}()
	}
	wg.Wait()
	servers := []queue.Server{}
	for i, base := range s.BaseURLs {
		if ok[i] {
			u, _ := url.Parse(base)
			servers = append(servers, queue.Server{Name: u.Host, IP: u.Hostname(), BaseURL: base})
		}
	}
	return servers, nil
}

// ParseStaticBackends splits STATIC_BACKENDS into normalized base URLs.
func ParseStaticBackends(list string) ([]string, error) {
	var out []string
	for _, item := range strings.Split(list, ",") {
		item = strings.TrimRight(strings.TrimSpace(item), "/")
		if item == "" {
			continue
		}
		u, err := url.Parse(item)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return nil, fmt.Errorf("STATIC_BACKENDS: %q is not an http(s) base URL", item)
		}
		out = append(out, item)
	}
	if len(out) == 0 {
		return nil, errors.New("STATIC_BACKENDS is empty")
	}
	return out, nil
}

// Monitor polls a Source and pushes updates. On errors it keeps the last known healthy servers for
// StaleAfter (default 30 s) after the last success, then reports zero.
type Monitor struct {
	Source     Source
	Apply      func(queue.CapacityUpdate)
	Interval   time.Duration
	Timeout    time.Duration // per poll, default 5 s
	StaleAfter time.Duration
	Now        func() time.Time
	Log        *slog.Logger

	lastOK      time.Time
	last        []queue.Server
	lastErr     string
	lastHealthy int
}

// Run polls immediately and then every Interval until ctx is done.
func (m *Monitor) Run(ctx context.Context) {
	m.Poll(ctx)
	t := time.NewTicker(m.Interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			m.Poll(ctx)
		}
	}
}

// Poll performs one observation. Not safe for concurrent use (Run calls it serially).
func (m *Monitor) Poll(ctx context.Context) {
	now := time.Now
	if m.Now != nil {
		now = m.Now
	}
	timeout, stale := m.Timeout, m.StaleAfter
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	if stale <= 0 {
		stale = queue.CapacityStaleAfter
	}
	pctx, cancel := context.WithTimeout(ctx, timeout)
	servers, err := m.Source.Healthy(pctx)
	cancel()
	if ctx.Err() != nil {
		return
	}
	u := queue.CapacityUpdate{Source: m.Source.Name()}
	if err == nil {
		m.lastOK, m.last = now(), servers
		u.Healthy, u.Servers = len(servers), servers
	} else {
		u.Err = err.Error()
		if !m.lastOK.IsZero() && now().Sub(m.lastOK) <= stale {
			u.Healthy, u.Servers = len(m.last), m.last
		}
	}
	m.logChange(u)
	m.Apply(u)
}

func (m *Monitor) logChange(u queue.CapacityUpdate) {
	if m.Log == nil {
		return
	}
	if u.Healthy != m.lastHealthy {
		names := make([]string, len(u.Servers))
		for i, s := range u.Servers {
			names[i] = s.Name
		}
		m.Log.Info("healthy servers changed", "from", m.lastHealthy, "to", u.Healthy, "servers", names)
	}
	if u.Err != m.lastErr {
		if u.Err != "" {
			m.Log.Warn("capacity source error", "source", u.Source, "err", u.Err, "healthyKept", u.Healthy)
		} else {
			m.Log.Info("capacity source recovered", "source", u.Source)
		}
	}
	m.lastHealthy, m.lastErr = u.Healthy, u.Err
}
