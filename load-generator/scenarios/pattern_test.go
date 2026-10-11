package scenarios

import (
	"math"
	"reflect"
	"testing"

	"github.com/mrmacsi/ticketing-autoscale-lab/load-generator/config"
)

func mustPattern(t *testing.T, p config.Pattern, d int, seed int64, maxRate float64) *Pattern {
	t.Helper()
	pat, err := NewPattern(p, d, seed, maxRate)
	if err != nil {
		t.Fatal(err)
	}
	return pat
}

func near(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

func TestPatternRates(t *testing.T) {
	type pt struct{ t, want float64 }
	cases := []struct {
		name string
		p    config.Pattern
		pts  []pt
		peak float64
	}{
		{"constant", config.Pattern{Type: "constant", Rate: 20},
			[]pt{{0, 20}, {50, 20}, {99.9, 20}, {100, 0}, {-1, 0}}, 20},
		{"ramp_up", config.Pattern{Type: "ramp_up", StartRate: 5, EndRate: 105},
			[]pt{{0, 5}, {50, 55}, {75, 80}}, 105},
		{"ramp_down", config.Pattern{Type: "ramp_down", StartRate: 100, EndRate: 0},
			[]pt{{0, 100}, {25, 75}, {90, 10}}, 100},
		{"spike", config.Pattern{Type: "spike", BaseRate: 5, PeakRate: 200, SpikeAtSeconds: 30, SpikeDurationSeconds: 20},
			[]pt{{0, 5}, {29.99, 5}, {30, 200}, {49.99, 200}, {50, 5}, {99, 5}}, 200},
		{"custom", config.Pattern{Type: "custom", Points: []config.Point{{AtSeconds: 60, Rate: 10}, {AtSeconds: 0, Rate: 0}, {AtSeconds: 80, Rate: 50}}},
			[]pt{{0, 0}, {30, 5}, {60, 10}, {70, 30}, {80, 50}, {95, 50}}, 50},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			pat := mustPattern(t, c.p, 100, 1, 0)
			for _, p := range c.pts {
				if got := pat.Rate(p.t); !near(got, p.want) {
					t.Errorf("rate(%v) = %v, want %v", p.t, got, p.want)
				}
			}
			if pat.Peak() != c.peak {
				t.Errorf("peak = %v, want %v", pat.Peak(), c.peak)
			}
		})
	}
}

func TestPatternClampedToMaxRate(t *testing.T) {
	pat := mustPattern(t, config.Pattern{Type: "spike", BaseRate: 10, PeakRate: 2000, SpikeAtSeconds: 5, SpikeDurationSeconds: 5}, 20, 1, 500)
	if pat.Rate(6) != 500 || pat.Peak() != 500 || pat.Rate(1) != 10 {
		t.Fatalf("rate(6)=%v peak=%v rate(1)=%v", pat.Rate(6), pat.Peak(), pat.Rate(1))
	}
}

func TestMeanRateIntegratesTheSecond(t *testing.T) {
	pat := mustPattern(t, config.Pattern{Type: "ramp_up", StartRate: 0, EndRate: 100}, 100, 1, 0)
	if got := pat.MeanRate(10); !near(got, 10.5) {
		t.Fatalf("mean rate over [10,11) = %v, want 10.5", got)
	}
	if pat.MeanRate(100) != 0 {
		t.Fatal("no rate after the duration")
	}
}

func TestRandomBurstsDerivedFromSeed(t *testing.T) {
	p := config.Pattern{Type: "random_bursts", BaseRate: 5, PeakRate: 100, BurstCount: 5, BurstDurationSeconds: 10}
	a := mustPattern(t, p, 300, 42, 0)
	b := mustPattern(t, p, 300, 42, 0)
	c := mustPattern(t, p, 300, 43, 0)
	if !reflect.DeepEqual(a.Bursts(), b.Bursts()) {
		t.Fatal("same seed must give the same bursts")
	}
	if reflect.DeepEqual(a.Bursts(), c.Bursts()) {
		t.Fatal("different seeds should give different bursts")
	}
	bursts := a.Bursts()
	if len(bursts) != 5 {
		t.Fatalf("bursts = %v", bursts)
	}
	for i, w := range bursts {
		if !near(w.End-w.Start, 10) || w.Start < 0 || w.End > 300 {
			t.Errorf("burst %d = %+v", i, w)
		}
		if i > 0 && w.Start < bursts[i-1].End {
			t.Errorf("bursts overlap: %+v %+v", bursts[i-1], w)
		}
		if a.Rate(w.Start+1) != 100 {
			t.Errorf("rate inside burst %d = %v", i, a.Rate(w.Start+1))
		}
		if a.Rate(w.End+0.01) != 5 && (i == len(bursts)-1 || bursts[i+1].Start > w.End+0.01) {
			t.Errorf("rate after burst %d = %v", i, a.Rate(w.End+0.01))
		}
	}
}

func scheduleTimes(p *Pattern, seed int64) []float64 {
	s := NewSchedule(p, seed)
	var out []float64
	for {
		at, ok := s.Next()
		if !ok {
			return out
		}
		out = append(out, at)
	}
}

func TestScheduleIsSeededPoisson(t *testing.T) {
	pat := mustPattern(t, config.Pattern{Type: "constant", Rate: 200}, 50, 7, 0)
	a := scheduleTimes(pat, 7)
	b := scheduleTimes(pat, 7)
	c := scheduleTimes(pat, 8)
	if !reflect.DeepEqual(a, b) {
		t.Fatal("same seed must produce the same schedule")
	}
	if reflect.DeepEqual(a, c) {
		t.Fatal("different seeds must produce different schedules")
	}
	// Expected 10000 arrivals; Poisson sd = 100, so 3% is a > 3 sigma bound.
	if n := float64(len(a)); math.Abs(n-10000) > 300 {
		t.Fatalf("arrivals = %v, want ~10000", n)
	}
	// Exponential inter-arrival times: mean 1/200 s and coefficient of variation ~1.
	var sum, sumSq float64
	for i := 1; i < len(a); i++ {
		d := a[i] - a[i-1]
		if d < 0 {
			t.Fatal("schedule must be monotonic")
		}
		sum += d
		sumSq += d * d
	}
	n := float64(len(a) - 1)
	mean := sum / n
	cv := math.Sqrt(sumSq/n-mean*mean) / mean
	if math.Abs(mean-0.005) > 0.0003 || math.Abs(cv-1) > 0.05 {
		t.Fatalf("inter-arrival mean=%v cv=%v", mean, cv)
	}
}

func TestScheduleFollowsNonhomogeneousRate(t *testing.T) {
	pat := mustPattern(t, config.Pattern{Type: "ramp_up", StartRate: 0, EndRate: 400}, 100, 3, 0)
	times := scheduleTimes(pat, 3)
	firstHalf := 0
	for _, at := range times {
		if at < 50 {
			firstHalf++
		}
	}
	// Integral over [0,50) = 5000, over [50,100) = 15000.
	if math.Abs(float64(firstHalf)-5000) > 300 || math.Abs(float64(len(times)-firstHalf)-15000) > 500 {
		t.Fatalf("first half %d, second half %d", firstHalf, len(times)-firstHalf)
	}
}

func TestScheduleZeroRate(t *testing.T) {
	pat := mustPattern(t, config.Pattern{Type: "constant", Rate: 0}, 10, 1, 0)
	if len(scheduleTimes(pat, 1)) != 0 {
		t.Fatal("zero rate must schedule nothing")
	}
}
