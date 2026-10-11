// Package engine runs one open-loop traffic experiment: it schedules
// arrivals from rate(t) as a seeded Poisson process, dispatches each arrival
// as an independent customer journey without waiting for earlier responses,
// and aggregates real HTTP outcomes into per-second buckets.
package engine

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/mrmacsi/ticketing-autoscale-lab/load-generator/config"
	"github.com/mrmacsi/ticketing-autoscale-lab/load-generator/metrics"
	"github.com/mrmacsi/ticketing-autoscale-lab/load-generator/queueapi"
	"github.com/mrmacsi/ticketing-autoscale-lab/load-generator/scenarios"
	"github.com/mrmacsi/ticketing-autoscale-lab/load-generator/ticketapi"
)

// Reason is the contract's generator.finishReason.
type Reason string

// Finish reasons.
const (
	ReasonCompleted     Reason = "completed"
	ReasonStopped       Reason = "stopped"
	ReasonMaxDuration   Reason = "max_duration"
	ReasonTotalArrivals Reason = "total_arrivals_reached"
	ReasonError         Reason = "error"
)

// StopCause is attached to a cancelled run context (context.WithCancelCause)
// to tell the engine why it was stopped.
type StopCause struct {
	Reason  Reason
	Message string
}

func (c *StopCause) Error() string { return fmt.Sprintf("%s: %s", c.Reason, c.Message) }

// DefaultDrainGrace bounds how long in-flight journeys may finish after
// scheduling ends naturally.
const DefaultDrainGrace = 30 * time.Second

// Options configure a run.
type Options struct {
	// TargetBaseURL is the only host traffic is sent to.
	TargetBaseURL string
	// QueueBaseURL is the waiting room base URL (default TargetBaseURL). It
	// is only used when the config enables the waiting room; the caller has
	// already checked it against the target allowlist.
	QueueBaseURL string
	Limits       config.Limits
	// Sink receives finalized buckets (may be nil).
	Sink metrics.Sink
	// DrainGrace overrides DefaultDrainGrace.
	DrainGrace time.Duration
	Logger     *slog.Logger
	// OnStarted is called once the event is fetched and scheduling begins.
	OnStarted func(start time.Time)
}

// Result describes a finished run.
type Result struct {
	Reason     Reason
	Message    string
	StartedAt  time.Time
	FinishedAt time.Time
	Config     config.TrafficConfig
	Notes      []string
	Summary    metrics.SummaryReport
	Err        error
}

// Run executes cfg until its duration elapses, totalArrivals is reached, or
// ctx is cancelled. Cancelling ctx is a hard stop: scheduling stops at once
// and in-flight requests are aborted.
func Run(ctx context.Context, cfg config.TrafficConfig, opts Options) Result {
	log := opts.Logger
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	fail := func(err error) Result {
		now := time.Now()
		return Result{Reason: ReasonError, Message: err.Error(), Err: err, StartedAt: now, FinishedAt: now, Config: cfg}
	}
	if err := cfg.Validate(); err != nil {
		return fail(fmt.Errorf("invalid traffic config: %w", err))
	}
	requestedDuration := cfg.DurationSeconds
	cfg, notes := cfg.Clamp(opts.Limits)
	for _, n := range notes {
		log.Warn("traffic config clamped", "note", n)
	}
	pattern, err := scenarios.NewPattern(cfg.Pattern, cfg.DurationSeconds, cfg.Seed, opts.Limits.MaxRate)
	if err != nil {
		return fail(err)
	}
	timeout := time.Duration(cfg.RequestTimeoutMs) * time.Millisecond
	httpClient := ticketapi.NewHTTPClient(cfg.MaxVUs, timeout)
	defer httpClient.CloseIdleConnections()

	setup, err := ticketapi.NewClient(opts.TargetBaseURL, httpClient, timeout, nil)
	if err != nil {
		return fail(err)
	}
	event, err := setup.FetchEvent(ctx, cfg.EventID)
	if err != nil {
		if ctx.Err() != nil {
			r := fail(err)
			r.Reason, r.Message = causeOf(ctx)
			return r
		}
		return fail(err)
	}

	r := &run{
		cfg:               cfg,
		requestedDuration: requestedDuration,
		pattern:           pattern,
		planner:           scenarios.NewPlanner(cfg),
		sem:               make(chan struct{}, cfg.MaxVUs),
		sink:              opts.Sink,
		summary:           metrics.NewSummary(),
		grace:             opts.DrainGrace,
		log:               log,
	}
	if r.grace <= 0 {
		r.grace = DefaultDrainGrace
	}
	res := r.execute(ctx, httpClient, timeout, event, opts)
	res.Notes = notes
	return res
}

type run struct {
	cfg               config.TrafficConfig
	requestedDuration int
	pattern           *scenarios.Pattern
	planner           *scenarios.Planner
	sem               chan struct{}
	active            atomic.Int64
	wg                sync.WaitGroup
	agg               *metrics.Aggregator
	sink              metrics.Sink
	summary           *metrics.Summary
	grace             time.Duration
	log               *slog.Logger
}

func (r *run) execute(ctx context.Context, hc *http.Client, timeout time.Duration, event ticketapi.Event, opts Options) Result {
	start := time.Now()
	durationSec := r.cfg.DurationSeconds
	r.agg = metrics.NewAggregator(metrics.AggregatorOptions{
		Start: start,
		TargetRate: func(s int) float64 {
			if s >= durationSec {
				return 0
			}
			return r.pattern.MeanRate(s)
		},
		ActiveVUs: r.active.Load,
	})
	client, err := ticketapi.NewClient(opts.TargetBaseURL, hc, timeout, r.agg)
	if err != nil {
		return Result{Reason: ReasonError, Message: err.Error(), Err: err, StartedAt: start, FinishedAt: start, Config: r.cfg}
	}
	exec := &scenarios.Executor{Client: client, Counter: r.agg, Event: event}
	if r.cfg.WaitingRoomEnabled() {
		queueURL := opts.QueueBaseURL
		if queueURL == "" {
			queueURL = opts.TargetBaseURL
		}
		// A separate transport: thousands of waiting customers each hold a
		// long poll, which must not compete with checkout connections.
		qhc := queueapi.NewHTTPClient(r.cfg.MaxVUs)
		defer qhc.CloseIdleConnections()
		qc, err := queueapi.NewClient(queueURL, qhc, r.agg)
		if err != nil {
			return Result{Reason: ReasonError, Message: err.Error(), Err: err, StartedAt: start, FinishedAt: start, Config: r.cfg}
		}
		exec.Queue, exec.QueueMetrics = qc, r.agg
	}

	flushDone := make(chan struct{})
	stopFlush := make(chan struct{})
	go r.flushLoop(start, stopFlush, flushDone)

	if opts.OnStarted != nil {
		opts.OnStarted(start)
	}
	r.log.Info("traffic started", "experimentId", r.cfg.ExperimentID, "pattern", r.cfg.Pattern.Type,
		"durationSeconds", durationSec, "maxVUs", r.cfg.MaxVUs, "peakRate", r.pattern.Peak(),
		"waitingRoom", r.cfg.WaitingRoomEnabled())

	// Journeys get their own context so they can be cut off after the drain
	// grace without cancelling the caller's context.
	journeyCtx, cancelJourneys := context.WithCancel(ctx)
	defer cancelJourneys()

	reason, message := r.schedule(ctx, journeyCtx, exec, start)
	switch reason {
	case "":
		// Natural end: keep the full duration, then let in-flight journeys finish.
		end := start.Add(time.Duration(durationSec) * time.Second)
		if sleepUntil(ctx, end) {
			r.drain(ctx, time.Now().Add(r.grace))
		}
		reason, message = ReasonCompleted, fmt.Sprintf("traffic pattern completed after %ds", durationSec)
		if r.requestedDuration > durationSec {
			reason, message = ReasonMaxDuration, fmt.Sprintf("stopped at max duration %ds (requested %ds)", durationSec, r.requestedDuration)
		}
		if ctx.Err() != nil {
			reason, message = causeOf(ctx)
		}
	case ReasonTotalArrivals:
		deadline := time.Now().Add(r.grace)
		if r.cfg.WaitingRoomEnabled() {
			// Queued customers may wait until the journey deadline of
			// durationSeconds + grace, even when the last arrival came early.
			deadline = later(deadline, start.Add(time.Duration(durationSec)*time.Second+r.grace))
		}
		r.drain(ctx, deadline)
		if ctx.Err() != nil {
			reason, message = causeOf(ctx)
		}
	}
	cancelJourneys()
	r.wg.Wait()

	close(stopFlush)
	<-flushDone
	for _, b := range r.agg.Flush(true) {
		r.emit(b)
	}
	finished := time.Now()
	r.log.Info("traffic finished", "experimentId", r.cfg.ExperimentID, "reason", reason, "message", message)
	return Result{
		Reason: reason, Message: message, StartedAt: start, FinishedAt: finished,
		Config: r.cfg, Summary: r.summary.Report(),
	}
}

// schedule emits arrivals until the pattern ends (returns ""), the arrival
// cap is reached, or ctx is cancelled. It never waits for responses.
func (r *run) schedule(ctx, journeyCtx context.Context, exec *scenarios.Executor, start time.Time) (Reason, string) {
	sched := scenarios.NewSchedule(r.pattern, r.cfg.Seed)
	timer := time.NewTimer(time.Hour)
	defer timer.Stop()
	var n uint64
	for {
		at, ok := sched.Next()
		if !ok {
			return "", ""
		}
		due := start.Add(time.Duration(at * float64(time.Second)))
		if wait := time.Until(due); wait > 0 {
			timer.Reset(wait)
			select {
			case <-ctx.Done():
				return causeOf(ctx)
			case <-timer.C:
			}
		} else if ctx.Err() != nil {
			return causeOf(ctx)
		}
		r.dispatch(journeyCtx, exec, n, time.Since(due))
		n++
		if r.cfg.TotalArrivals > 0 && n >= uint64(r.cfg.TotalArrivals) {
			return ReasonTotalArrivals, fmt.Sprintf("totalArrivals %d reached", r.cfg.TotalArrivals)
		}
	}
}

// dispatch starts one journey if a VU slot is free (non-blocking); otherwise
// the arrival is counted as dropped, which signals generator saturation.
func (r *run) dispatch(ctx context.Context, exec *scenarios.Executor, n uint64, lag time.Duration) {
	select {
	case r.sem <- struct{}{}:
	default:
		r.agg.RecordArrival(false, lag, r.active.Load())
		return
	}
	r.agg.RecordArrival(true, lag, r.active.Add(1))
	r.wg.Add(1)
	go func() {
		defer func() {
			r.active.Add(-1)
			<-r.sem
			r.wg.Done()
		}()
		if exec.Run(ctx, r.planner.Plan(n)) != scenarios.OutcomeAborted {
			r.agg.RecordJourneyCompleted()
		}
	}()
}

// drain waits for in-flight journeys until deadline (or until ctx is
// cancelled); the caller then aborts whatever is still running.
func (r *run) drain(ctx context.Context, deadline time.Time) {
	done := make(chan struct{})
	go func() {
		r.wg.Wait()
		close(done)
	}()
	t := time.NewTimer(time.Until(deadline))
	defer t.Stop()
	select {
	case <-done:
	case <-t.C:
		r.log.Warn("drain deadline elapsed; aborting in-flight journeys", "active", r.active.Load(), "queueWaiting", r.agg.QueueWaiting())
	case <-ctx.Done():
	}
}

// flushLoop emits each bucket shortly after its second has elapsed.
func (r *run) flushLoop(start time.Time, stop <-chan struct{}, done chan<- struct{}) {
	defer close(done)
	const settle = 20 * time.Millisecond
	t := time.NewTimer(time.Until(start.Add(time.Second + settle)))
	defer t.Stop()
	for {
		select {
		case <-stop:
			return
		case <-t.C:
		}
		for _, b := range r.agg.Flush(false) {
			r.emit(b)
		}
		elapsed := time.Since(start)
		next := start.Add(elapsed.Truncate(time.Second) + time.Second + settle)
		t.Reset(time.Until(next))
	}
}

func (r *run) emit(b metrics.Bucket) {
	r.summary.Add(b)
	if r.sink != nil {
		r.sink.Write(b)
	}
}

func later(a, b time.Time) time.Time {
	if b.After(a) {
		return b
	}
	return a
}

func causeOf(ctx context.Context) (Reason, string) {
	var sc *StopCause
	if errors.As(context.Cause(ctx), &sc) {
		return sc.Reason, sc.Message
	}
	return ReasonStopped, "stopped"
}

func sleepUntil(ctx context.Context, t time.Time) bool {
	d := time.Until(t)
	if d <= 0 {
		return ctx.Err() == nil
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-timer.C:
		return true
	case <-ctx.Done():
		return false
	}
}
