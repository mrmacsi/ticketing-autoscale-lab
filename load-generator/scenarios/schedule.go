package scenarios

import (
	"math/rand/v2"

	"github.com/mrmacsi/ticketing-autoscale-lab/load-generator/internal/seedrand"
)

// Schedule produces arrival times (seconds from run start) of a
// nonhomogeneous Poisson process with intensity Pattern.Rate, using
// Lewis-Shedler thinning: candidates are drawn from a homogeneous process at
// the peak rate and accepted with probability rate(t)/peak. The sequence is a
// pure function of (pattern, seed).
type Schedule struct {
	pattern *Pattern
	rng     *rand.Rand
	lambda  float64
	t       float64
	done    bool
}

// NewSchedule creates the arrival schedule for a pattern.
func NewSchedule(p *Pattern, seed int64) *Schedule {
	return &Schedule{pattern: p, rng: seedrand.New(seed, seedrand.SaltSchedule, 0), lambda: p.Peak()}
}

// Next returns the next arrival time, or false once the run duration is over.
func (s *Schedule) Next() (float64, bool) {
	if s.done || s.lambda <= 0 {
		s.done = true
		return 0, false
	}
	for {
		s.t += s.rng.ExpFloat64() / s.lambda
		if s.t >= s.pattern.Duration() {
			s.done = true
			return 0, false
		}
		if s.rng.Float64()*s.lambda < s.pattern.Rate(s.t) {
			return s.t, true
		}
	}
}
