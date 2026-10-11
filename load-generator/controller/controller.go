package controller

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/mrmacsi/ticketing-autoscale-lab/load-generator/config"
	"github.com/mrmacsi/ticketing-autoscale-lab/load-generator/engine"
)

// Runner executes one traffic run. It must return promptly once ctx is
// cancelled and should report the StopCause attached to ctx.
type Runner interface {
	Run(ctx context.Context, cfg config.TrafficConfig) engine.Result
}

// RunnerFunc adapts a function to Runner.
type RunnerFunc func(ctx context.Context, cfg config.TrafficConfig) engine.Result

// Run calls f.
func (f RunnerFunc) Run(ctx context.Context, cfg config.TrafficConfig) engine.Result {
	return f(ctx, cfg)
}

// Options configure a Controller.
type Options struct {
	AgentID       string
	Version       string
	TargetBaseURL string
	Limits        config.Limits

	HeartbeatInterval  time.Duration // default 2s
	LeaseRenewInterval time.Duration // default 5s
	LeaseDuration      time.Duration // default 15s
	StaleAfter         time.Duration // start commands older than this are ignored; default 120s
	// MaxDuration is the independent hard-stop timer; defaults to Limits.MaxDurationSeconds.
	MaxDuration     time.Duration
	ShutdownTimeout time.Duration // default 10s
	Logger          *slog.Logger
	Now             func() time.Time
}

// RestartMessage is written to an experiment whose run was interrupted by an
// agent restart.
const RestartMessage = "generator restarted during run; traffic was not resumed"

var experimentIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)

// Controller is the long-running agent loop.
type Controller struct {
	store  Store
	runner Runner
	opts   Options
	log    *slog.Logger

	mu     sync.Mutex
	status Status
	kick   chan struct{}

	// Owned by the event loop goroutine.
	lastSeq int64
	cur     *activeRun
}

type activeRun struct {
	experimentID string
	ctx          context.Context
	cancel       context.CancelCauseFunc
	done         chan engine.Result
	hardStop     *time.Timer
	stopLease    context.CancelFunc
}

// New creates a controller.
func New(store Store, runner Runner, opts Options) *Controller {
	if opts.HeartbeatInterval <= 0 {
		opts.HeartbeatInterval = 2 * time.Second
	}
	if opts.LeaseRenewInterval <= 0 {
		opts.LeaseRenewInterval = 5 * time.Second
	}
	if opts.LeaseDuration <= 0 {
		opts.LeaseDuration = 3 * opts.LeaseRenewInterval
	}
	if opts.StaleAfter <= 0 {
		opts.StaleAfter = 120 * time.Second
	}
	if opts.MaxDuration <= 0 {
		opts.MaxDuration = time.Duration(opts.Limits.MaxDurationSeconds) * time.Second
	}
	if opts.ShutdownTimeout <= 0 {
		opts.ShutdownTimeout = 10 * time.Second
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.Logger == nil {
		opts.Logger = slog.New(slog.DiscardHandler)
	}
	return &Controller{
		store: store, runner: runner, opts: opts, log: opts.Logger,
		kick: make(chan struct{}, 1),
		status: Status{
			AgentID: opts.AgentID, State: StateIdle, Version: opts.Version,
			Limits: opts.Limits, TargetBaseURL: opts.TargetBaseURL,
		},
	}
}

// Status returns a copy of the current status (for tests and logs).
func (c *Controller) Status() Status {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.status
}

func (c *Controller) updateStatus(fn func(*Status)) {
	c.mu.Lock()
	fn(&c.status)
	c.mu.Unlock()
	select {
	case c.kick <- struct{}{}:
	default:
	}
}

func (c *Controller) note(format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	c.log.Info(msg)
	c.updateStatus(func(s *Status) { s.Message = msg })
}

// Run recovers from a previous incarnation, then processes commands until
// ctx is cancelled. A run in progress at shutdown is finished with reason
// "error" rather than left dangling.
func (c *Controller) Run(ctx context.Context) error {
	if err := c.recoverPrevious(ctx); err != nil {
		return err
	}

	hbCtx, stopHeartbeat := context.WithCancel(context.Background())
	hbDone := make(chan struct{})
	go func() {
		defer close(hbDone)
		c.heartbeatLoop(hbCtx)
	}()
	defer func() {
		stopHeartbeat()
		<-hbDone
		c.writeStatus(context.Background())
	}()

	cmds := make(chan Command)
	go c.watchLoop(ctx, cmds)

	for {
		var runDone chan engine.Result
		if c.cur != nil {
			runDone = c.cur.done
		}
		select {
		case <-ctx.Done():
			c.shutdown()
			return nil
		case cmd := <-cmds:
			c.handle(cmd)
		case res := <-runDone:
			c.finishRun(res)
		}
	}
}

// recoverPrevious reads generator/status left by an earlier agent process.
// If this host's previous agent was mid-run, the experiment is marked
// finished with an error; traffic is never silently resumed.
func (c *Controller) recoverPrevious(ctx context.Context) error {
	var prev Status
	var ok bool
	backoff := time.Second
	for {
		var err error
		prev, ok, err = c.store.LoadStatus(ctx)
		if err == nil {
			break
		}
		c.log.Warn("cannot read generator/status; retrying", "err", err)
		if !sleepCtx(ctx, backoff) {
			return ctx.Err()
		}
		backoff = min(backoff*2, 30*time.Second)
	}
	if !ok {
		c.note("agent started")
		return nil
	}
	c.lastSeq = prev.LastCommandSeq
	c.updateStatus(func(s *Status) { s.LastCommandSeq = prev.LastCommandSeq })

	wasRunning := (prev.State == StateRunning || prev.State == StateStopping) && prev.ExperimentID != ""
	if !wasRunning {
		c.note("agent started")
		return nil
	}
	if !sameHost(prev.AgentID, c.opts.AgentID) {
		c.log.Warn("generator/status shows a run by a different host's agent; not touching its experiment",
			"previousAgent", prev.AgentID, "experimentId", prev.ExperimentID)
		c.note("agent started; previous status belonged to %s", prev.AgentID)
		return nil
	}
	uctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	err := c.store.UpdateGenerator(uctx, prev.ExperimentID, finish(prev.AgentID, c.opts.Now(), string(engine.ReasonError), RestartMessage))
	switch {
	case err == nil:
		c.note("agent restarted; experiment %s marked finished (error): %s", prev.ExperimentID, RestartMessage)
	case errors.Is(err, ErrAlreadyFinished), errors.Is(err, ErrLeaseLost), errors.Is(err, ErrExperimentNotFound):
		c.note("agent restarted; experiment %s needed no recovery (%v)", prev.ExperimentID, err)
	default:
		c.log.Error("failed to mark interrupted experiment", "experimentId", prev.ExperimentID, "err", err)
		c.note("agent restarted; could not mark experiment %s finished: %v", prev.ExperimentID, err)
	}
	return nil
}

// sameHost compares agent IDs of the form "<host>/boot-<uuid>".
func sameHost(a, b string) bool {
	host := func(id string) string {
		h, _, _ := strings.Cut(id, "/boot-")
		return h
	}
	return a != "" && host(a) == host(b)
}

func (c *Controller) watchLoop(ctx context.Context, out chan<- Command) {
	backoff := time.Second
	for ctx.Err() == nil {
		var received atomic.Bool
		err := c.store.WatchCommands(ctx, func(cmd Command) {
			received.Store(true)
			select {
			case out <- cmd:
			case <-ctx.Done():
			}
		})
		if ctx.Err() != nil {
			return
		}
		if received.Load() {
			backoff = time.Second
		}
		c.log.Warn("command listener stopped; re-subscribing", "err", err, "backoff", backoff)
		if !sleepCtx(ctx, backoff) {
			return
		}
		backoff = min(backoff*2, 30*time.Second)
	}
}

func (c *Controller) heartbeatLoop(ctx context.Context) {
	t := time.NewTicker(c.opts.HeartbeatInterval)
	defer t.Stop()
	c.writeStatus(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		case <-c.kick:
		}
		c.writeStatus(ctx)
	}
}

func (c *Controller) writeStatus(ctx context.Context) {
	c.mu.Lock()
	c.status.HeartbeatAt = c.opts.Now()
	st := c.status
	c.mu.Unlock()
	wctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if err := c.store.WriteStatus(wctx, st); err != nil {
		c.log.Warn("heartbeat write failed", "err", err)
	}
}

func (c *Controller) handle(cmd Command) {
	if cmd.Seq <= c.lastSeq {
		return // already handled (or older); snapshot listeners redeliver the current doc
	}
	c.lastSeq = cmd.Seq
	c.updateStatus(func(s *Status) { s.LastCommandSeq = cmd.Seq })
	c.log.Info("command received", "seq", cmd.Seq, "action", cmd.Action, "experimentId", cmd.ExperimentID, "issuedBy", cmd.IssuedBy)

	if cmd.DecodeErr != nil {
		c.note("ignored malformed command seq %d: %v", cmd.Seq, cmd.DecodeErr)
		if cmd.Action == ActionStart && c.cur == nil {
			c.rejectStart(cmd.ExperimentID, fmt.Sprintf("malformed start command: %v", cmd.DecodeErr))
		}
		return
	}
	switch cmd.Action {
	case ActionStart:
		c.handleStart(cmd)
	case ActionStop:
		c.handleStop(cmd)
	default:
		c.note("ignored command seq %d with unknown action %q", cmd.Seq, cmd.Action)
	}
}

func (c *Controller) handleStop(cmd Command) {
	if c.cur == nil {
		c.note("stop (seq %d) ignored: no run in progress", cmd.Seq)
		return
	}
	if cmd.ExperimentID != "" && cmd.ExperimentID != c.cur.experimentID {
		c.note("stop (seq %d) for %s ignored: running %s", cmd.Seq, cmd.ExperimentID, c.cur.experimentID)
		return
	}
	who := cmd.IssuedBy
	if who == "" {
		who = "control-api"
	}
	c.cur.cancel(&engine.StopCause{Reason: engine.ReasonStopped, Message: "stopped by " + who})
	c.updateStatus(func(s *Status) {
		s.State = StateStopping
		s.Message = fmt.Sprintf("stopping experiment %s (seq %d)", c.cur.experimentID, cmd.Seq)
	})
}

func (c *Controller) handleStart(cmd Command) {
	now := c.opts.Now()
	if c.cur != nil {
		if cmd.ExperimentID == c.cur.experimentID {
			c.note("duplicate start (seq %d) for running experiment %s ignored", cmd.Seq, cmd.ExperimentID)
			return
		}
		msg := fmt.Sprintf("start (seq %d) for %s rejected: already running %s", cmd.Seq, cmd.ExperimentID, c.cur.experimentID)
		c.note("%s", msg)
		c.rejectStart(cmd.ExperimentID, msg)
		return
	}
	if cmd.IssuedAt.IsZero() || now.Sub(cmd.IssuedAt) > c.opts.StaleAfter {
		msg := fmt.Sprintf("stale start (seq %d) for %s ignored: issued %s ago (limit %s)",
			cmd.Seq, cmd.ExperimentID, now.Sub(cmd.IssuedAt).Round(time.Second), c.opts.StaleAfter)
		if cmd.IssuedAt.IsZero() {
			msg = fmt.Sprintf("start (seq %d) for %s ignored: missing issuedAt", cmd.Seq, cmd.ExperimentID)
		}
		c.note("%s", msg)
		c.rejectStart(cmd.ExperimentID, msg)
		return
	}
	cfg, err := c.startConfig(cmd)
	if err != nil {
		msg := fmt.Sprintf("start (seq %d) for %s rejected: %v", cmd.Seq, cmd.ExperimentID, err)
		c.note("%s", msg)
		c.rejectStart(cmd.ExperimentID, msg)
		return
	}

	cctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	err = c.store.UpdateGenerator(cctx, cfg.ExperimentID, claim(c.opts.AgentID, now, c.opts.LeaseDuration))
	cancel()
	if err != nil {
		c.note("start (seq %d) for %s not started: claim failed: %v", cmd.Seq, cfg.ExperimentID, err)
		return
	}
	c.startRun(cfg, now)
}

func (c *Controller) startConfig(cmd Command) (config.TrafficConfig, error) {
	if !experimentIDPattern.MatchString(cmd.ExperimentID) {
		return config.TrafficConfig{}, fmt.Errorf("invalid experimentId %q", cmd.ExperimentID)
	}
	if cmd.Config == nil {
		return config.TrafficConfig{}, errors.New("missing config")
	}
	cfg := *cmd.Config
	if cfg.ExperimentID == "" {
		cfg.ExperimentID = cmd.ExperimentID
	}
	if cfg.ExperimentID != cmd.ExperimentID {
		return cfg, fmt.Errorf("config.experimentId %q does not match command experimentId", cfg.ExperimentID)
	}
	if err := cfg.Validate(); err != nil {
		return cfg, fmt.Errorf("invalid config: %s", strings.ReplaceAll(err.Error(), "\n", "; "))
	}
	return cfg, nil
}

// rejectStart best-effort records a refused start on an untouched experiment.
func (c *Controller) rejectStart(experimentID, msg string) {
	if !experimentIDPattern.MatchString(experimentID) {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := c.store.UpdateGenerator(ctx, experimentID, reject(c.opts.AgentID, c.opts.Now(), msg)); err != nil && !errors.Is(err, ErrNotEmpty) {
		c.log.Warn("could not record rejected start", "experimentId", experimentID, "err", err)
	}
}

func (c *Controller) startRun(cfg config.TrafficConfig, now time.Time) {
	runCtx, cancel := context.WithCancelCause(context.Background())
	ar := &activeRun{experimentID: cfg.ExperimentID, ctx: runCtx, cancel: cancel, done: make(chan engine.Result, 1)}

	// Independent of the engine's own duration handling: even a misbehaving
	// run cannot exceed the agent's max duration.
	maxDur := c.opts.MaxDuration
	ar.hardStop = time.AfterFunc(maxDur, func() {
		cancel(&engine.StopCause{Reason: engine.ReasonMaxDuration, Message: fmt.Sprintf("hard stop: max duration %s reached", maxDur)})
	})

	leaseCtx, stopLease := context.WithCancel(runCtx)
	ar.stopLease = stopLease
	go c.leaseLoop(leaseCtx, ar)

	_, notes := cfg.Clamp(c.opts.Limits)
	msg := fmt.Sprintf("running experiment %s", cfg.ExperimentID)
	if len(notes) > 0 {
		msg += " (" + strings.Join(notes, "; ") + ")"
	}
	started := now
	c.cur = ar
	c.updateStatus(func(s *Status) {
		s.State = StateRunning
		s.ExperimentID = cfg.ExperimentID
		s.RunStartedAt = &started
		s.Message = msg
	})
	c.log.Info(msg)

	go func() { ar.done <- c.runner.Run(runCtx, cfg) }()
}

func (c *Controller) leaseLoop(ctx context.Context, ar *activeRun) {
	t := time.NewTicker(c.opts.LeaseRenewInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		rctx, cancel := context.WithTimeout(ctx, c.opts.LeaseRenewInterval)
		err := c.store.UpdateGenerator(rctx, ar.experimentID, renew(c.opts.AgentID, c.opts.Now(), c.opts.LeaseDuration))
		cancel()
		switch {
		case err == nil:
		case errors.Is(err, ErrLeaseLost), errors.Is(err, ErrExperimentNotFound):
			ar.cancel(&engine.StopCause{Reason: engine.ReasonError, Message: "lease lost: " + err.Error()})
			return
		default:
			if ctx.Err() == nil {
				c.log.Warn("lease renewal failed; will retry", "experimentId", ar.experimentID, "err", err)
			}
		}
	}
}

func (c *Controller) finishRun(res engine.Result) {
	ar := c.cur
	c.cur = nil
	ar.hardStop.Stop()
	ar.stopLease()

	reason, message := res.Reason, res.Message
	natural := res.Err == nil && (reason == engine.ReasonCompleted || reason == engine.ReasonTotalArrivals)
	var sc *engine.StopCause
	if !natural && errors.As(context.Cause(ar.ctx), &sc) {
		reason, message = sc.Reason, sc.Message
	}
	if reason == "" {
		reason = engine.ReasonError
	}
	ar.cancel(nil)

	var err error
	for attempt := range 3 {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		err = c.store.UpdateGenerator(ctx, ar.experimentID, finish(c.opts.AgentID, c.opts.Now(), string(reason), message))
		cancel()
		if err == nil || errors.Is(err, ErrLeaseLost) || errors.Is(err, ErrAlreadyFinished) || errors.Is(err, ErrExperimentNotFound) {
			break
		}
		time.Sleep(time.Duration(attempt+1) * 500 * time.Millisecond)
	}
	summary := fmt.Sprintf("experiment %s finished: %s (%s)", ar.experimentID, reason, message)
	if err != nil {
		summary += fmt.Sprintf("; could not update experiment: %v", err)
	}
	c.log.Info(summary, "scheduled", res.Summary.ArrivalsScheduled, "dropped", res.Summary.ArrivalsDropped,
		"requests", res.Summary.Requests.Total)
	c.updateStatus(func(s *Status) {
		s.State = StateIdle
		s.ExperimentID = ""
		s.RunStartedAt = nil
		s.Message = summary
	})
}

func (c *Controller) shutdown() {
	if c.cur == nil {
		c.updateStatus(func(s *Status) { s.Message = "agent stopped" })
		return
	}
	c.cur.cancel(&engine.StopCause{Reason: engine.ReasonError, Message: "generator agent shut down during run"})
	c.updateStatus(func(s *Status) { s.State = StateStopping })
	select {
	case res := <-c.cur.done:
		c.finishRun(res)
	case <-time.After(c.opts.ShutdownTimeout):
		c.finishRun(engine.Result{Reason: engine.ReasonError, Message: "generator agent shut down during run"})
	}
}

func sleepCtx(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return true
	case <-ctx.Done():
		return false
	}
}
