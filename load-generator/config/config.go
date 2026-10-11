// Package config defines the TrafficConfig contract (docs/contracts.md §2),
// its validation, and clamping to the agent's hard limits.
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
)

// Pattern types.
const (
	PatternConstant     = "constant"
	PatternRampUp       = "ramp_up"
	PatternRampDown     = "ramp_down"
	PatternSpike        = "spike"
	PatternRandomBursts = "random_bursts"
	PatternCustom       = "custom"
)

// TrafficConfig is the generator/command.config document.
type TrafficConfig struct {
	ExperimentID     string    `json:"experimentId"`
	EventID          string    `json:"eventId"`
	Seed             int64     `json:"seed"`
	DurationSeconds  int       `json:"durationSeconds"`
	MaxVUs           int       `json:"maxVUs"`
	TotalArrivals    int64     `json:"totalArrivals"`
	RequestTimeoutMs int       `json:"requestTimeoutMs"`
	Pattern          Pattern   `json:"pattern"`
	Mix              Mix       `json:"mix"`
	Behaviour        Behaviour `json:"behaviour"`
	// WaitingRoom routes reserve and purchase journeys through the waiting
	// room (docs/waiting-room.md §3.6). Absent means disabled.
	WaitingRoom *WaitingRoomConfig `json:"waitingRoom,omitempty"`
}

// WaitingRoomConfig is TrafficConfig.waitingRoom.
type WaitingRoomConfig struct {
	Enabled bool `json:"enabled"`
}

// WaitingRoomEnabled reports whether checkout journeys queue first.
func (c TrafficConfig) WaitingRoomEnabled() bool {
	return c.WaitingRoom != nil && c.WaitingRoom.Enabled
}

// Pattern describes rate(t). Only the fields relevant to Type are used.
type Pattern struct {
	Type                 string  `json:"type"`
	Rate                 float64 `json:"rate,omitempty"`
	StartRate            float64 `json:"startRate,omitempty"`
	EndRate              float64 `json:"endRate,omitempty"`
	BaseRate             float64 `json:"baseRate,omitempty"`
	PeakRate             float64 `json:"peakRate,omitempty"`
	SpikeAtSeconds       float64 `json:"spikeAtSeconds,omitempty"`
	SpikeDurationSeconds float64 `json:"spikeDurationSeconds,omitempty"`
	BurstCount           int     `json:"burstCount,omitempty"`
	BurstDurationSeconds float64 `json:"burstDurationSeconds,omitempty"`
	Points               []Point `json:"points,omitempty"`
}

// Point is one vertex of a custom piecewise-linear pattern.
type Point struct {
	AtSeconds float64 `json:"atSeconds"`
	Rate      float64 `json:"rate"`
}

// Mix holds integer journey percentages that must sum to 100.
type Mix struct {
	Browse       int `json:"browse"`
	Availability int `json:"availability"`
	Reserve      int `json:"reserve"`
	Purchase     int `json:"purchase"`
}

// Behaviour tunes customer journeys.
type Behaviour struct {
	MaxQuantity                         int     `json:"maxQuantity"`
	ThinkTimeMinMs                      int     `json:"thinkTimeMinMs"`
	ThinkTimeMaxMs                      int     `json:"thinkTimeMaxMs"`
	AbandonCheckoutProbability          float64 `json:"abandonCheckoutProbability"`
	DuplicateSubmitProbability          float64 `json:"duplicateSubmitProbability"`
	RetryPaymentAfterDeclineProbability float64 `json:"retryPaymentAfterDeclineProbability"`
	SyntheticCPUMs                      int     `json:"syntheticCpuMs"`
	SyntheticCPUProbability             float64 `json:"syntheticCpuProbability"`
}

// Limits are the agent's hard caps (env MAX_RATE, MAX_VUS, MAX_DURATION_SECONDS).
type Limits struct {
	MaxRate            float64 `json:"maxRate"`
	MaxVUs             int     `json:"maxVUs"`
	MaxDurationSeconds int     `json:"maxDurationSeconds"`
}

// DefaultLimits mirrors the documented env defaults.
func DefaultLimits() Limits {
	return Limits{MaxRate: 500, MaxVUs: 1_000_000, MaxDurationSeconds: 1800}
}

const (
	// MaxReservationQuantity is the API's upper bound for a reservation quantity.
	MaxReservationQuantity  = 8
	defaultRequestTimeoutMs = 10000
	minRequestTimeoutMs     = 100
	maxRequestTimeoutMs     = 120000
)

// Parse decodes JSON into a TrafficConfig, rejecting unknown fields so typos
// in hand-written local configs surface early.
func Parse(data []byte) (TrafficConfig, error) {
	var c TrafficConfig
	dec := json.NewDecoder(strings.NewReader(string(data)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&c); err != nil {
		return c, fmt.Errorf("parse traffic config: %w", err)
	}
	return c, nil
}

// Validate reports every structural problem with the config.
func (c TrafficConfig) Validate() error {
	var errs []error
	add := func(format string, args ...any) { errs = append(errs, fmt.Errorf(format, args...)) }

	if strings.TrimSpace(c.EventID) == "" {
		add("eventId is required")
	}
	if c.DurationSeconds <= 0 {
		add("durationSeconds must be > 0")
	}
	if c.MaxVUs < 0 {
		add("maxVUs must be >= 0")
	}
	if c.TotalArrivals < 0 {
		add("totalArrivals must be >= 0")
	}
	if c.RequestTimeoutMs < 0 {
		add("requestTimeoutMs must be >= 0")
	}
	if err := c.Pattern.validate(); err != nil {
		errs = append(errs, err)
	}
	m := c.Mix
	if m.Browse < 0 || m.Availability < 0 || m.Reserve < 0 || m.Purchase < 0 {
		add("mix percentages must be >= 0")
	} else if sum := m.Browse + m.Availability + m.Reserve + m.Purchase; sum != 100 {
		add("mix percentages must sum to 100 (got %d)", sum)
	}
	b := c.Behaviour
	if b.ThinkTimeMinMs < 0 || b.ThinkTimeMaxMs < 0 {
		add("think times must be >= 0")
	}
	for name, p := range map[string]float64{
		"abandonCheckoutProbability":          b.AbandonCheckoutProbability,
		"duplicateSubmitProbability":          b.DuplicateSubmitProbability,
		"retryPaymentAfterDeclineProbability": b.RetryPaymentAfterDeclineProbability,
		"syntheticCpuProbability":             b.SyntheticCPUProbability,
	} {
		if p < 0 || p > 1 {
			add("%s must be within [0,1]", name)
		}
	}
	if b.SyntheticCPUMs < 0 {
		add("syntheticCpuMs must be >= 0")
	}
	sort.Slice(errs, func(i, j int) bool { return errs[i].Error() < errs[j].Error() })
	return errors.Join(errs...)
}

func (p Pattern) validate() error {
	neg := func(vs ...float64) bool {
		for _, v := range vs {
			if v < 0 {
				return true
			}
		}
		return false
	}
	switch p.Type {
	case PatternConstant:
		if neg(p.Rate) {
			return errors.New("pattern.rate must be >= 0")
		}
	case PatternRampUp, PatternRampDown:
		if neg(p.StartRate, p.EndRate) {
			return errors.New("pattern.startRate/endRate must be >= 0")
		}
	case PatternSpike:
		if neg(p.BaseRate, p.PeakRate, p.SpikeAtSeconds, p.SpikeDurationSeconds) {
			return errors.New("spike pattern values must be >= 0")
		}
	case PatternRandomBursts:
		if neg(p.BaseRate, p.PeakRate, p.BurstDurationSeconds) || p.BurstCount < 0 {
			return errors.New("random_bursts pattern values must be >= 0")
		}
	case PatternCustom:
		if len(p.Points) == 0 {
			return errors.New("custom pattern needs at least one point")
		}
		for _, pt := range p.Points {
			if neg(pt.AtSeconds, pt.Rate) {
				return errors.New("custom pattern points must be >= 0")
			}
		}
	default:
		return fmt.Errorf("unknown pattern.type %q", p.Type)
	}
	return nil
}

// Clamp applies defaults and the agent's hard limits, returning the effective
// config and a human-readable note per adjustment. Rate clamping is applied
// to rate(t) by the scenarios package; here we only record it.
func (c TrafficConfig) Clamp(l Limits) (TrafficConfig, []string) {
	var notes []string
	if l.MaxDurationSeconds > 0 && c.DurationSeconds > l.MaxDurationSeconds {
		notes = append(notes, fmt.Sprintf("durationSeconds %d clamped to %d", c.DurationSeconds, l.MaxDurationSeconds))
		c.DurationSeconds = l.MaxDurationSeconds
	}
	switch {
	case c.MaxVUs == 0:
		c.MaxVUs = l.MaxVUs
	case l.MaxVUs > 0 && c.MaxVUs > l.MaxVUs:
		notes = append(notes, fmt.Sprintf("maxVUs %d clamped to %d", c.MaxVUs, l.MaxVUs))
		c.MaxVUs = l.MaxVUs
	}
	if c.MaxVUs < 1 {
		c.MaxVUs = 1
	}
	switch {
	case c.RequestTimeoutMs == 0:
		c.RequestTimeoutMs = defaultRequestTimeoutMs
	case c.RequestTimeoutMs < minRequestTimeoutMs:
		c.RequestTimeoutMs = minRequestTimeoutMs
	case c.RequestTimeoutMs > maxRequestTimeoutMs:
		c.RequestTimeoutMs = maxRequestTimeoutMs
	}
	b := &c.Behaviour
	if b.MaxQuantity < 1 {
		b.MaxQuantity = 1
	}
	if b.MaxQuantity > MaxReservationQuantity {
		b.MaxQuantity = MaxReservationQuantity
	}
	if b.ThinkTimeMaxMs < b.ThinkTimeMinMs {
		b.ThinkTimeMaxMs = b.ThinkTimeMinMs
	}
	if peak := c.Pattern.PeakDeclaredRate(); l.MaxRate > 0 && peak > l.MaxRate {
		notes = append(notes, fmt.Sprintf("pattern peak rate %.1f/s clamped to %.1f/s", peak, l.MaxRate))
	}
	return c, notes
}

// PeakDeclaredRate is the highest rate mentioned by the pattern parameters.
func (p Pattern) PeakDeclaredRate() float64 {
	peak := 0.0
	for _, v := range []float64{p.Rate, p.StartRate, p.EndRate, p.BaseRate, p.PeakRate} {
		peak = max(peak, v)
	}
	for _, pt := range p.Points {
		peak = max(peak, pt.Rate)
	}
	return peak
}
