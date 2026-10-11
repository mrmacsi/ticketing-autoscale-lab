// Package metrics aggregates real HTTP observations into the per-second
// traffic buckets defined in docs/contracts.md and provides sinks for them.
package metrics

import (
	"fmt"
	"math"
	"strconv"
)

// HistogramBuckets is the number of latency buckets (indices 0..250).
const HistogramBuckets = 251

var logBase = math.Log(1.1)

// BucketIndex maps a latency in milliseconds to its histogram bucket:
// i = ceil(ln(ms)/ln(1.1)) clamped to [0, 250]; ms <= 1 maps to 0.
func BucketIndex(ms float64) int {
	if ms <= 1 || math.IsNaN(ms) {
		return 0
	}
	i := int(math.Ceil(math.Log(ms) / logBase))
	return min(max(i, 0), HistogramBuckets-1)
}

// BucketValue is the representative (upper bound) latency of bucket i: 1.1^i ms.
func BucketValue(i int) float64 { return math.Pow(1.1, float64(i)) }

// Histogram is a fixed-layout, mergeable latency histogram.
type Histogram struct {
	counts [HistogramBuckets]int64
	total  int64
}

// Observe records one latency sample.
func (h *Histogram) Observe(ms float64) {
	h.counts[BucketIndex(ms)]++
	h.total++
}

// Add adds n samples to bucket i.
func (h *Histogram) Add(i int, n int64) {
	if i < 0 || i >= HistogramBuckets || n <= 0 {
		return
	}
	h.counts[i] += n
	h.total += n
}

// Merge adds all samples of o into h.
func (h *Histogram) Merge(o *Histogram) {
	for i, c := range o.counts {
		h.counts[i] += c
	}
	h.total += o.total
}

// Count is the number of samples.
func (h *Histogram) Count() int64 { return h.total }

// Percentile returns the representative value of the bucket holding the
// q-quantile (0 < q <= 1) using the nearest-rank method; ok is false when the
// histogram is empty.
func (h *Histogram) Percentile(q float64) (value float64, ok bool) {
	if h.total == 0 {
		return 0, false
	}
	rank := int64(math.Ceil(q * float64(h.total)))
	rank = min(max(rank, 1), h.total)
	var cum int64
	for i, c := range h.counts {
		cum += c
		if cum >= rank {
			return BucketValue(i), true
		}
	}
	return BucketValue(HistogramBuckets - 1), true
}

// Sparse returns the non-empty buckets keyed by decimal index (the wire form).
func (h *Histogram) Sparse() map[string]int64 {
	out := make(map[string]int64)
	for i, c := range h.counts {
		if c > 0 {
			out[strconv.Itoa(i)] = c
		}
	}
	return out
}

// HistogramFromSparse parses the wire form back into a Histogram.
func HistogramFromSparse(m map[string]int64) (*Histogram, error) {
	h := &Histogram{}
	for k, v := range m {
		i, err := strconv.Atoi(k)
		if err != nil || i < 0 || i >= HistogramBuckets {
			return nil, fmt.Errorf("invalid histogram bucket %q", k)
		}
		h.Add(i, v)
	}
	return h, nil
}
