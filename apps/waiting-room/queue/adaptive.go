package queue

import (
	"fmt"
	"math"
	"strings"
	"time"
)

// Admission modes.
const (
	ModeFixed    = "fixed"
	ModeAdaptive = "adaptive"
)

// Adaptive-limit phases and decisions (docs/waiting-room.md section 2a).
const (
	PhaseFixed     = "fixed"
	PhaseSlowStart = "slow_start"
	PhaseSteady    = "steady"

	DecisionIncrease = "increase"
	DecisionDecrease = "decrease"
	DecisionHold     = "hold"
)

// DemandFill is the share of the capacity in use at or above which a limit may grow.
const DemandFill = 0.8

// MinInFlightPerActive is the share of admitted customers that must show up as requests in flight on
// the servers before the limit may grow. Admitted customers who never arrive (closed the page, or a
// broken path to the app) leave the servers idle; read as "headroom" that would inflate the limit
// (measured locally: 14 → 200 while sales stalled). A checkout journey keeps ~0.3–0.8 requests in
// flight per customer, so 5 % only rules out rooms that are full of no-shows.
const MinInFlightPerActive = 0.05

// SignalMaxAge is how old a server's load signals may be before the controller ignores them.
const SignalMaxAge = 3 * time.Second

// CPUMaxAge is how old the VM CPU sample may be before it is ignored (the sampler writes every 2 s).
const CPUMaxAge = 10 * time.Second

// AdaptiveConfig tunes the per-server limit controller.
type AdaptiveConfig struct {
	MinPerServer    int     `json:"minPerServer"`
	MaxPerServer    int     `json:"maxPerServer"`
	BusyHigh        float64 `json:"busyHigh"`        // workers busy at or above this ⇒ overloaded
	BusyLow         float64 `json:"busyLow"`         // below this (and CPU below cpuLow, no queue) ⇒ headroom
	CPUHigh         float64 `json:"cpuHigh"`         // VM CPU at or above this ⇒ overloaded
	CPULow          float64 `json:"cpuLow"`          //
	DecreaseFactor  float64 `json:"decreaseFactor"`  // multiplicative cut on overload
	SlowStartFactor float64 `json:"slowStartFactor"` // multiplicative growth until the first cut
	CooldownSeconds int     `json:"cooldownSeconds"` // minimum time between two cuts of the same server
}

// DefaultAdaptiveConfig returns the documented defaults.
func DefaultAdaptiveConfig() AdaptiveConfig {
	return AdaptiveConfig{MinPerServer: 5, MaxPerServer: 200, BusyHigh: 0.9, BusyLow: 0.7, CPUHigh: 0.9, CPULow: 0.8,
		DecreaseFactor: 0.75, SlowStartFactor: 1.25, CooldownSeconds: 3}
}

// Validate reports the first invalid field, or "".
func (a AdaptiveConfig) Validate() string {
	switch {
	case a.MinPerServer < 1 || a.MinPerServer > 1000:
		return "adaptive.minPerServer must be 1..1000."
	case a.MaxPerServer < a.MinPerServer || a.MaxPerServer > 1000:
		return "adaptive.maxPerServer must be minPerServer..1000."
	case a.BusyLow < 0 || a.BusyHigh > 1 || a.BusyLow >= a.BusyHigh:
		return "adaptive busy thresholds must satisfy 0 <= busyLow < busyHigh <= 1."
	case a.CPULow < 0 || a.CPUHigh > 1 || a.CPULow >= a.CPUHigh:
		return "adaptive CPU thresholds must satisfy 0 <= cpuLow < cpuHigh <= 1."
	case a.DecreaseFactor < 0.1 || a.DecreaseFactor > 0.99:
		return "adaptive.decreaseFactor must be 0.1..0.99."
	case a.SlowStartFactor < 1.01 || a.SlowStartFactor > 4:
		return "adaptive.slowStartFactor must be 1.01..4."
	case a.CooldownSeconds < 0 || a.CooldownSeconds > 60:
		return "adaptive.cooldownSeconds must be 0..60."
	}
	return ""
}

// Signals is one load observation of an app server (GET /lab/load and /lab/cpu).
type Signals struct {
	ActiveWorkers int       `json:"activeWorkers"`
	TotalWorkers  int       `json:"totalWorkers"`
	ListenQueue   int       `json:"listenQueue"`
	Busy          float64   `json:"busy"`         // active / total
	CPU           *float64  `json:"cpu"`          // 0..1, null when the sample is missing or older than CPUMaxAge
	CPUSampledAt  string    `json:"cpuSampledAt"` // as reported by the server
	ObservedAt    string    `json:"observedAt"`   //
	observedAt    time.Time `json:"-"`
}

// NewSignals builds a Signals value; cpu < 0 means unknown.
func NewSignals(active, total, listenQueue int, cpu float64, cpuSampledAt string, at time.Time) Signals {
	s := Signals{ActiveWorkers: active, TotalWorkers: total, ListenQueue: listenQueue, CPUSampledAt: cpuSampledAt,
		ObservedAt: FormatTime(at), observedAt: at}
	if total > 0 {
		s.Busy = math.Round(float64(active)/float64(total)*1000) / 1000
	}
	if cpu >= 0 {
		c := math.Round(cpu*1000) / 1000
		s.CPU = &c
	}
	return s
}

// serverLimit holds the latest load signals of one healthy app server.
type serverLimit struct {
	signals *Signals
}

// controller is the shared adaptive limit. The load balancer spreads every customer's requests
// evenly over the healthy servers, whatever limit each one has, so a separate limit per server drifts
// for the wrong reasons (measured: identical VMs ended at 7 and 39). One limit per server, decided by
// the busiest server, matches how the load actually lands; a new server joins at the learned value
// instead of slow-starting (which overshot to p95 2 s on a cold VM).
type controller struct {
	limit        int // 0 until first used: the configured starting value
	phase        string
	decision     string
	reason       string
	decisionAt   time.Time
	lastDecrease time.Time
	decreases    int64
}

// ServerView is one entry of Stats.Servers. Limit, phase, decision, reason and decreases are the
// shared controller's (the same on every server); signals are the server's own.
type ServerView struct {
	Name       string   `json:"name"`
	IP         string   `json:"ip"`
	Limit      int      `json:"limit"`
	Phase      string   `json:"phase"`
	Decision   string   `json:"decision"`
	Reason     string   `json:"reason"`
	DecisionAt *string  `json:"decisionAt"`
	Decreases  int64    `json:"decreases"`
	Signals    *Signals `json:"signals"`
}

// SetSignals records a load observation for the named server (ignored for servers the LB does not
// report healthy).
func (r *Room) SetSignals(name string, s Signals) {
	r.mu.Lock()
	defer r.mu.Unlock()
	st := r.limitLocked(name)
	if st == nil {
		return
	}
	st.signals = &s
}

// limitLocked returns the signal slot of a healthy server.
func (r *Room) limitLocked(name string) *serverLimit {
	healthy := false
	for _, s := range r.servers {
		if s.Name == name {
			healthy = true
			break
		}
	}
	if !healthy {
		return nil
	}
	st := r.limits[name]
	if st == nil {
		st = &serverLimit{}
		r.limits[name] = st
	}
	return st
}

func startLimit(cfg Config) int {
	a := cfg.Adaptive
	return min(max(cfg.ActiveUsersPerServer, a.MinPerServer), a.MaxPerServer)
}

// shared is the current limit per healthy server in adaptive mode (initialized on first use).
func (c *controller) shared(cfg Config) int {
	if c.limit == 0 {
		c.limit, c.phase, c.decision, c.reason = startLimit(cfg), PhaseSlowStart, DecisionHold, "starting value"
	}
	return c.limit
}

// capacityFor is the number of customers that may be in checkout at once.
func capacityFor(cfg Config, healthy int, c *controller) int {
	if cfg.AdmissionMode != ModeAdaptive {
		return healthy * cfg.ActiveUsersPerServer
	}
	return healthy * c.shared(cfg)
}

// signalLookup returns the latest load signals of a healthy server, or nil.
type signalLookup func(name string) *Signals

func (r *Room) sharedLimitLocked() int { return r.ctl.shared(r.cfg) }

// capacityLocked is the number of customers that may be in checkout at once.
func (r *Room) capacityLocked() int { return capacityFor(r.cfg, r.healthy, &r.ctl) }

func (r *Room) signalsLocked(name string) *Signals {
	if st := r.limits[name]; st != nil {
		return st.signals
	}
	return nil
}

// worst is the most loaded value of each signal across the healthy servers with fresh signals.
type worst struct {
	queue, queueAt   int
	busy, cpu        float64
	busyAt, cpuAt    int
	fresh, missingAt int // number of servers with fresh signals; index of one without (or -1)
	inFlight         int // requests in flight (running or waiting for a worker) summed over fresh servers
}

func worstOf(servers []Server, sigOf signalLookup, now time.Time) worst {
	w := worst{cpu: -1, queueAt: -1, busyAt: -1, cpuAt: -1, missingAt: -1}
	for i, srv := range servers {
		sig := sigOf(srv.Name)
		if sig == nil || now.Sub(sig.observedAt) > SignalMaxAge {
			w.missingAt = i
			continue
		}
		w.fresh++
		w.inFlight += sig.ActiveWorkers + sig.ListenQueue
		if sig.ListenQueue > w.queue || w.queueAt < 0 {
			w.queue, w.queueAt = sig.ListenQueue, i
		}
		if sig.Busy >= w.busy {
			w.busy, w.busyAt = sig.Busy, i
		}
		if sig.CPU != nil && *sig.CPU >= w.cpu {
			w.cpu, w.cpuAt = *sig.CPU, i
		}
	}
	return w
}

// Control runs one step of the shared adaptive controller (docs/waiting-room.md section 2a). It is a
// no-op in fixed mode. Called every second by RunControl.
func (r *Room) Control() {
	now := r.now()
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.cfg.Enabled || r.cfg.AdmissionMode != ModeAdaptive {
		return
	}
	r.ctl.control(r.cfg, r.servers, r.signalsLocked, len(r.active), r.healthy, now)
}

// control runs one controller step and records the decision (adaptive mode, room enabled).
func (c *controller) control(cfg Config, servers []Server, sigOf signalLookup, active, healthy int, now time.Time) {
	c.shared(cfg)
	decision, reason := c.step(cfg, servers, sigOf, active, healthy, now)
	c.decision, c.reason, c.decisionAt = decision, reason, now
}

func shortName(servers []Server, i int) string {
	n := servers[i].Name
	if j := strings.LastIndex(n, "-"); j >= 0 && j < len(n)-1 {
		return n[j+1:]
	}
	return n
}

func (c *controller) step(cfg Config, servers []Server, sigOf signalLookup, active, healthy int, now time.Time) (string, string) {
	a := cfg.Adaptive
	w := worstOf(servers, sigOf, now)
	if w.fresh == 0 {
		return DecisionHold, "no signal"
	}
	var overload string
	switch {
	case w.queue > 0:
		overload = fmt.Sprintf("listen queue %d on %s", w.queue, shortName(servers, w.queueAt))
	case w.busy >= a.BusyHigh:
		overload = fmt.Sprintf("workers busy %.0f%% on %s", w.busy*100, shortName(servers, w.busyAt))
	case w.cpu >= a.CPUHigh:
		overload = fmt.Sprintf("CPU %.0f%% on %s", w.cpu*100, shortName(servers, w.cpuAt))
	}
	if overload != "" {
		if !c.lastDecrease.IsZero() && now.Sub(c.lastDecrease) < time.Duration(a.CooldownSeconds)*time.Second {
			return DecisionHold, overload + " (cooling down after a cut)"
		}
		c.limit = max(a.MinPerServer, int(math.Floor(float64(c.limit)*a.DecreaseFactor)))
		c.phase, c.lastDecrease = PhaseSteady, now
		c.decreases++
		return DecisionDecrease, overload
	}
	// Grow only while the capacity itself is what holds customers back: the room is (nearly) full.
	// People waiting is not enough: near a sell-out they wait for tickets, not for capacity.
	capacity := capacityFor(cfg, healthy, c)
	demand := capacity > 0 && float64(active) >= DemandFill*float64(capacity)
	headroom := w.queue == 0 && w.busy < a.BusyLow && w.cpu < a.CPULow
	switch {
	case w.missingAt >= 0:
		return DecisionHold, "no signal from " + shortName(servers, w.missingAt)
	case !headroom:
		return DecisionHold, "near the limit"
	case !demand:
		return DecisionHold, "capacity not in use"
	case float64(w.inFlight) < math.Ceil(MinInFlightPerActive*float64(active)):
		return DecisionHold, fmt.Sprintf("admitted customers not arriving (%d in flight for %d admitted)", w.inFlight, active)
	case c.limit >= a.MaxPerServer:
		return DecisionHold, "at maximum"
	}
	if c.phase == PhaseSlowStart {
		c.limit = min(a.MaxPerServer, max(c.limit+1, int(math.Ceil(float64(c.limit)*a.SlowStartFactor))))
	} else {
		c.limit = min(a.MaxPerServer, c.limit+1)
	}
	return DecisionIncrease, "headroom"
}

// serverViewsLocked builds Stats.Servers.
func (r *Room) serverViewsLocked() []ServerView {
	return serverViews(r.cfg, r.servers, &r.ctl, r.signalsLocked)
}

func serverViews(cfg Config, servers []Server, c *controller, sigOf signalLookup) []ServerView {
	out := make([]ServerView, 0, len(servers))
	adaptive := cfg.AdmissionMode == ModeAdaptive
	for _, s := range servers {
		v := ServerView{Name: s.Name, IP: s.IP, Limit: cfg.ActiveUsersPerServer, Phase: PhaseFixed, Decision: DecisionHold, Reason: "fixed mode"}
		if adaptive {
			v.Limit, v.Phase, v.Decision, v.Reason, v.Decreases = c.shared(cfg), c.phase, c.decision, c.reason, c.decreases
			if !c.decisionAt.IsZero() {
				t := FormatTime(c.decisionAt)
				v.DecisionAt = &t
			}
		}
		if sig := sigOf(s.Name); sig != nil {
			cp := *sig
			v.Signals = &cp
		}
		out = append(out, v)
	}
	return out
}
