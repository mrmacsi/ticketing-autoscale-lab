// Package signals reads each healthy app server's load every second for adaptive admission
// (docs/waiting-room.md section 2a): requests in flight from GET /lab/load (nginx stub_status, with the
// PHP worker count in the X-Php-Workers header) and VM CPU from GET /lab/cpu. Both are served by nginx
// without the PHP worker pool, so they answer in milliseconds while the pool is saturated.
package signals

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/mrmacsi/ticketing-autoscale-lab/waiting-room/gcp"
	"github.com/mrmacsi/ticketing-autoscale-lab/waiting-room/queue"
)

// Poller fetches signals from every healthy server.
type Poller struct {
	Room     *queue.Room
	Client   *http.Client
	Interval time.Duration
	Timeout  time.Duration // per request; default 800 ms
	Log      *slog.Logger
	Now      func() time.Time

	mu      sync.Mutex
	lastErr map[string]string
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

// Poll observes every healthy server once, in parallel.
func (p *Poller) Poll(ctx context.Context) {
	cfg := p.Room.Config()
	if !cfg.Enabled {
		return
	}
	var wg sync.WaitGroup
	for _, srv := range p.Room.HealthyServers() {
		if srv.BaseURL == "" {
			continue
		}
		wg.Add(1)
		go func(srv queue.Server) {
			defer wg.Done()
			sig, err := p.fetch(ctx, srv.BaseURL)
			p.report(srv.Name, err)
			if err == nil {
				p.Room.SetSignals(srv.Name, sig)
			}
		}(srv)
	}
	wg.Wait()
}

func (p *Poller) report(name string, err error) {
	if p.Log == nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.lastErr == nil {
		p.lastErr = map[string]string{}
	}
	msg := ""
	if err != nil {
		msg = err.Error()
	}
	if msg != p.lastErr[name] {
		if msg == "" {
			p.Log.Info("load signals recovered", "server", name)
		} else {
			p.Log.Warn("load signals unavailable", "server", name, "error", msg)
		}
		p.lastErr[name] = msg
	}
}

var writingRe = regexp.MustCompile(`Writing:\s*(\d+)`)

// ParseLoad reads nginx stub_status and the PHP worker count. In-flight requests are nginx's
// "Writing" (requests being processed, in PHP or waiting for a worker) minus the status request
// itself; those beyond the worker count are waiting for a worker.
func ParseLoad(body []byte, workersHeader string) (inFlight, workers int, err error) {
	m := writingRe.FindSubmatch(body)
	if m == nil {
		return 0, 0, fmt.Errorf("malformed stub_status: no Writing counter")
	}
	writing, _ := strconv.Atoi(string(m[1]))
	workers, err = strconv.Atoi(strings.TrimSpace(workersHeader))
	if err != nil || workers <= 0 {
		return 0, 0, fmt.Errorf("missing or invalid X-Php-Workers header %q", workersHeader)
	}
	return max(writing-1, 0), workers, nil
}

// CPUSample is GET /lab/cpu.
type CPUSample struct {
	Percent   *float64 `json:"percent"`
	SampledAt string   `json:"sampled_at"`
}

// ParseCPU returns VM CPU as a fraction 0..1, or -1 when missing or older than queue.CPUMaxAge.
func ParseCPU(body []byte, now time.Time) (float64, string) {
	var c CPUSample
	if json.Unmarshal(body, &c) != nil || c.Percent == nil {
		return -1, ""
	}
	at, err := time.Parse(time.RFC3339, c.SampledAt)
	if err != nil || now.Sub(at) > queue.CPUMaxAge {
		return -1, c.SampledAt
	}
	return min(max(*c.Percent/100, 0), 1), c.SampledAt
}

func (p *Poller) fetch(ctx context.Context, base string) (queue.Signals, error) {
	timeout := p.Timeout
	if timeout <= 0 {
		timeout = 800 * time.Millisecond
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	get := func(path string) ([]byte, http.Header, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+path, nil)
		if err != nil {
			return nil, nil, err
		}
		res, err := p.Client.Do(req)
		if err != nil {
			return nil, nil, err
		}
		defer res.Body.Close()
		body, err := io.ReadAll(io.LimitReader(res.Body, 64<<10))
		if err != nil {
			return nil, nil, err
		}
		if res.StatusCode != http.StatusOK {
			return nil, nil, fmt.Errorf("HTTP %d: %s", res.StatusCode, gcp.Excerpt(body))
		}
		return body, res.Header, nil
	}
	var cpuBody []byte
	var cpuErr error
	done := make(chan struct{})
	go func() { cpuBody, _, cpuErr = get("/lab/cpu"); close(done) }()
	loadBody, hdr, err := get("/lab/load")
	<-done
	if err != nil {
		return queue.Signals{}, fmt.Errorf("/lab/load: %w", err)
	}
	inFlight, workers, err := ParseLoad(loadBody, hdr.Get("X-Php-Workers"))
	if err != nil {
		return queue.Signals{}, err
	}
	now := time.Now()
	if p.Now != nil {
		now = p.Now()
	}
	cpu, sampledAt := -1.0, ""
	if cpuErr == nil {
		cpu, sampledAt = ParseCPU(cpuBody, now)
	}
	return queue.NewSignals(min(inFlight, workers), workers, max(inFlight-workers, 0), cpu, sampledAt, now), nil
}
