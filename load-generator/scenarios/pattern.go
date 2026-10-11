// Package scenarios turns a TrafficConfig into an arrival process (rate(t) and
// a seeded nonhomogeneous Poisson schedule) and into customer journeys.
package scenarios

import (
	"fmt"
	"math"
	"sort"

	"github.com/mrmacsi/ticketing-autoscale-lab/load-generator/config"
	"github.com/mrmacsi/ticketing-autoscale-lab/load-generator/internal/seedrand"
)

// Interval is a half-open time window [Start, End) in seconds from run start.
type Interval struct {
	Start, End float64
}

// Pattern evaluates the requested arrival rate rate(t) in arrivals/second,
// already clamped to the agent's maximum rate.
type Pattern struct {
	duration float64
	maxRate  float64
	peak     float64
	fn       func(t float64) float64
	bursts   []Interval
}

// NewPattern builds rate(t) for a run of durationSeconds. maxRate <= 0 means
// unclamped. The seed only matters for random_bursts.
func NewPattern(p config.Pattern, durationSeconds int, seed int64, maxRate float64) (*Pattern, error) {
	if durationSeconds <= 0 {
		return nil, fmt.Errorf("duration must be > 0")
	}
	d := float64(durationSeconds)
	pat := &Pattern{duration: d, maxRate: maxRate}
	switch p.Type {
	case config.PatternConstant:
		r := p.Rate
		pat.fn = func(float64) float64 { return r }
		pat.peak = r
	case config.PatternRampUp, config.PatternRampDown:
		start, end := p.StartRate, p.EndRate
		pat.fn = func(t float64) float64 { return start + (end-start)*t/d }
		pat.peak = math.Max(start, end)
	case config.PatternSpike:
		spike := Interval{Start: p.SpikeAtSeconds, End: p.SpikeAtSeconds + p.SpikeDurationSeconds}
		pat.fn = stepFunc(p.BaseRate, p.PeakRate, []Interval{spike})
		pat.peak = math.Max(p.BaseRate, p.PeakRate)
	case config.PatternRandomBursts:
		pat.bursts = burstWindows(p.BurstCount, p.BurstDurationSeconds, d, seed)
		pat.fn = stepFunc(p.BaseRate, p.PeakRate, pat.bursts)
		pat.peak = math.Max(p.BaseRate, p.PeakRate)
	case config.PatternCustom:
		if len(p.Points) == 0 {
			return nil, fmt.Errorf("custom pattern needs points")
		}
		pts := append([]config.Point(nil), p.Points...)
		sort.SliceStable(pts, func(i, j int) bool { return pts[i].AtSeconds < pts[j].AtSeconds })
		pat.fn = piecewiseLinear(pts)
		for _, pt := range pts {
			pat.peak = math.Max(pat.peak, pt.Rate)
		}
	default:
		return nil, fmt.Errorf("unknown pattern type %q", p.Type)
	}
	if maxRate > 0 {
		pat.peak = math.Min(pat.peak, maxRate)
	}
	return pat, nil
}

// Rate returns rate(t); zero outside [0, duration).
func (p *Pattern) Rate(t float64) float64 {
	if t < 0 || t >= p.duration {
		return 0
	}
	r := p.fn(t)
	if r < 0 || math.IsNaN(r) {
		return 0
	}
	if p.maxRate > 0 && r > p.maxRate {
		return p.maxRate
	}
	return r
}

// Peak is an upper bound of Rate over the run (used for Poisson thinning).
func (p *Pattern) Peak() float64 { return p.peak }

// Duration of the run in seconds.
func (p *Pattern) Duration() float64 { return p.duration }

// Bursts returns the seed-derived burst windows (random_bursts only).
func (p *Pattern) Bursts() []Interval { return append([]Interval(nil), p.bursts...) }

// MeanRate is the average requested rate over second s, i.e. the expected
// number of arrivals in that second. Summing it over a run gives the total
// requested arrivals.
func (p *Pattern) MeanRate(s int) float64 {
	const steps = 20
	sum := 0.0
	for i := range steps {
		sum += p.Rate(float64(s) + (float64(i)+0.5)/steps)
	}
	return sum / steps
}

func stepFunc(base, peak float64, windows []Interval) func(float64) float64 {
	return func(t float64) float64 {
		for _, w := range windows {
			if t >= w.Start && t < w.End {
				return peak
			}
		}
		return base
	}
}

// burstWindows spreads count bursts over the run: the duration is divided into
// equal slots and each burst is placed at a seeded random offset inside its
// slot, so bursts are irregular but never overlap (unless a burst is longer
// than its slot).
func burstWindows(count int, burst, duration float64, seed int64) []Interval {
	if count <= 0 || burst <= 0 {
		return nil
	}
	rng := seedrand.New(seed, seedrand.SaltBursts, 0)
	slot := duration / float64(count)
	out := make([]Interval, 0, count)
	for i := range count {
		start := float64(i) * slot
		if slack := slot - burst; slack > 0 {
			start += rng.Float64() * slack
		}
		out = append(out, Interval{Start: start, End: math.Min(start+burst, duration)})
	}
	return out
}

func piecewiseLinear(pts []config.Point) func(float64) float64 {
	return func(t float64) float64 {
		if t <= pts[0].AtSeconds {
			return pts[0].Rate
		}
		for i := 1; i < len(pts); i++ {
			a, b := pts[i-1], pts[i]
			if t < b.AtSeconds {
				span := b.AtSeconds - a.AtSeconds
				if span <= 0 {
					return b.Rate
				}
				return a.Rate + (b.Rate-a.Rate)*(t-a.AtSeconds)/span
			}
		}
		return pts[len(pts)-1].Rate
	}
}
