package metrics

import "sync"

// Summary merges buckets into run totals with experiment-wide percentiles
// computed from the merged histogram (never by averaging per-second values).
type Summary struct {
	mu        sync.Mutex
	s         SummaryReport
	hist      Histogram
	queueHist Histogram
}

// SummaryReport is the JSON form of a Summary.
type SummaryReport struct {
	Seconds            int                        `json:"seconds"`
	ArrivalsRequested  float64                    `json:"arrivalsRequested"`
	ArrivalsScheduled  int64                      `json:"arrivalsScheduled"`
	ArrivalsStarted    int64                      `json:"arrivalsStarted"`
	ArrivalsDropped    int64                      `json:"arrivalsDropped"`
	AchievedVsRequest  *float64                   `json:"achievedVsRequestedPct"`
	CompletedJourneys  int64                      `json:"completedJourneys"`
	MaxActiveVUs       int64                      `json:"maxActiveVUs"`
	SchedulerLagMsMax  float64                    `json:"schedulerLagMsMax"`
	Requests           RequestCounts              `json:"requests"`
	ByEndpoint         map[Endpoint]EndpointStats `json:"byEndpoint"`
	Latency            LatencyStats               `json:"latency"`
	Instances          map[string]InstanceStats   `json:"instances"`
	Business           BusinessCounters           `json:"business"`
	Synthetic          SyntheticStats             `json:"synthetic"`
	PeakRequestsPerSec int64                      `json:"peakRequestsPerSecond"`
	Queue              QueueSummary               `json:"queue"`
}

// QueueSummary is the run total of the per-second queue objects.
type QueueSummary struct {
	Joined      int64 `json:"joined"`
	Admitted    int64 `json:"admitted"`
	SoldOut     int64 `json:"soldOut"`
	Open        int64 `json:"open"`
	Failed      int64 `json:"failed"`
	PeakWaiting int64 `json:"peakWaiting"`
	// WaitMs merges the per-second join -> admitted histograms.
	WaitMs   LatencyStats       `json:"waitMs"`
	Requests QueueRequestCounts `json:"requests"`
}

// NewSummary returns an empty summary.
func NewSummary() *Summary {
	return &Summary{s: SummaryReport{ByEndpoint: map[Endpoint]EndpointStats{}, Instances: map[string]InstanceStats{}}}
}

// Add merges one bucket.
func (m *Summary) Add(b Bucket) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s := &m.s
	s.Seconds = max(s.Seconds, b.Second+1)
	s.ArrivalsRequested += b.TargetRate
	s.ArrivalsScheduled += b.ScheduledArrivals
	s.ArrivalsStarted += b.StartedArrivals
	s.ArrivalsDropped += b.DroppedArrivals
	s.CompletedJourneys += b.CompletedJourneys
	s.MaxActiveVUs = max(s.MaxActiveVUs, b.MaxActiveVUs)
	s.SchedulerLagMsMax = max(s.SchedulerLagMsMax, b.SchedulerLagMsMax)
	s.Requests.Merge(b.Requests)
	s.PeakRequestsPerSec = max(s.PeakRequestsPerSec, b.Requests.Total)
	for e, v := range b.ByEndpoint {
		cur := s.ByEndpoint[e]
		cur.Count += v.Count
		cur.Errors += v.Errors
		s.ByEndpoint[e] = cur
	}
	for k, v := range b.Instances {
		cur := s.Instances[k]
		cur.Requests += v.Requests
		cur.Errors += v.Errors
		cur.LatencySumMs += v.LatencySumMs
		s.Instances[k] = cur
	}
	s.Latency.SumMs += b.Latency.SumMs
	s.Latency.MaxMs = max(s.Latency.MaxMs, b.Latency.MaxMs)
	if h, err := HistogramFromSparse(b.Latency.Hist); err == nil {
		m.hist.Merge(h)
	}
	s.Business.Merge(b.Business)
	s.Synthetic.Requests += b.Synthetic.Requests
	s.Synthetic.CPUMsRequested += b.Synthetic.CPUMsRequested
	q := &s.Queue
	q.Joined += b.Queue.Joined
	q.Admitted += b.Queue.Admitted
	q.SoldOut += b.Queue.SoldOut
	q.Open += b.Queue.Open
	q.Failed += b.Queue.Failed
	q.PeakWaiting = max(q.PeakWaiting, b.Queue.Waiting)
	q.WaitMs.SumMs += b.Queue.WaitMs.SumMs
	q.WaitMs.MaxMs = max(q.WaitMs.MaxMs, b.Queue.WaitMs.MaxMs)
	q.Requests.Merge(b.Queue.Requests)
	if h, err := HistogramFromSparse(b.Queue.WaitMs.Hist); err == nil {
		m.queueHist.Merge(h)
	}
}

// Report returns the merged totals.
func (m *Summary) Report() SummaryReport {
	m.mu.Lock()
	defer m.mu.Unlock()
	r := m.s
	r.ByEndpoint = make(map[Endpoint]EndpointStats, len(m.s.ByEndpoint))
	for k, v := range m.s.ByEndpoint {
		r.ByEndpoint[k] = v
	}
	r.Instances = make(map[string]InstanceStats, len(m.s.Instances))
	for k, v := range m.s.Instances {
		v.LatencySumMs = round3(v.LatencySumMs)
		r.Instances[k] = v
	}
	r.ArrivalsRequested = round3(r.ArrivalsRequested)
	fillLatency(&r.Latency, &m.hist)
	fillLatency(&r.Queue.WaitMs, &m.queueHist)
	if r.ArrivalsRequested > 0 {
		pct := round1(float64(r.ArrivalsStarted) / r.ArrivalsRequested * 100)
		r.AchievedVsRequest = &pct
	}
	return r
}
