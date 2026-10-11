package controller

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mrmacsi/ticketing-autoscale-lab/load-generator/config"
	"github.com/mrmacsi/ticketing-autoscale-lab/load-generator/engine"
)

// memStore is an in-memory Store with Firestore-like semantics.
type memStore struct {
	mu          sync.Mutex
	status      *Status
	statusLog   []Status
	experiments map[string]*Generator // nil value = experiment exists, generator map empty
	cmds        chan Command
	watchErrs   int // number of WatchCommands calls that fail immediately
	watchCalls  int
}

func newMemStore(expIDs ...string) *memStore {
	s := &memStore{experiments: map[string]*Generator{}, cmds: make(chan Command, 16)}
	for _, id := range expIDs {
		s.experiments[id] = &Generator{}
	}
	return s
}

func (s *memStore) WatchCommands(ctx context.Context, fn func(Command)) error {
	s.mu.Lock()
	s.watchCalls++
	fail := s.watchCalls <= s.watchErrs
	s.mu.Unlock()
	if fail {
		return errors.New("listener broken")
	}
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case c := <-s.cmds:
			fn(c)
		}
	}
}

func (s *memStore) LoadStatus(context.Context) (Status, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.status == nil {
		return Status{}, false, nil
	}
	return *s.status, true, nil
}

func (s *memStore) WriteStatus(_ context.Context, st Status) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.status = &st
	s.statusLog = append(s.statusLog, st)
	return nil
}

func (s *memStore) UpdateGenerator(_ context.Context, id string, fn GeneratorUpdate) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	g, exists := s.experiments[id]
	var cur Generator
	if exists {
		cur = *g
	}
	next, err := fn(cur, exists)
	if err != nil {
		return err
	}
	s.experiments[id] = &next
	return nil
}

func (s *memStore) gen(id string) Generator {
	s.mu.Lock()
	defer s.mu.Unlock()
	if g := s.experiments[id]; g != nil {
		return *g
	}
	return Generator{}
}

func (s *memStore) lastStatus() Status {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.status == nil {
		return Status{}
	}
	return *s.status
}

// fakeRunner simulates a run that lasts `duration` (0 = until cancelled).
type fakeRunner struct {
	duration time.Duration
	calls    atomic.Int32
	active   atomic.Int32
	maxConc  atomic.Int32
	mu       sync.Mutex
	cfgs     []config.TrafficConfig
}

func (r *fakeRunner) Run(ctx context.Context, cfg config.TrafficConfig) engine.Result {
	r.calls.Add(1)
	n := r.active.Add(1)
	defer r.active.Add(-1)
	for {
		m := r.maxConc.Load()
		if n <= m || r.maxConc.CompareAndSwap(m, n) {
			break
		}
	}
	r.mu.Lock()
	r.cfgs = append(r.cfgs, cfg)
	r.mu.Unlock()
	var done <-chan time.Time
	if r.duration > 0 {
		done = time.After(r.duration)
	}
	select {
	case <-done:
		return engine.Result{Reason: engine.ReasonCompleted, Message: "done"}
	case <-ctx.Done():
		return engine.Result{Reason: engine.ReasonStopped, Message: "cancelled"}
	}
}

const agentID = "loadgen-vm/boot-new"

func testOptions() Options {
	return Options{
		AgentID: agentID, Version: "test", TargetBaseURL: "http://target",
		Limits:            config.Limits{MaxRate: 500, MaxVUs: 2000, MaxDurationSeconds: 1800},
		HeartbeatInterval: 20 * time.Millisecond, LeaseRenewInterval: 20 * time.Millisecond,
		LeaseDuration: 200 * time.Millisecond, MaxDuration: 10 * time.Second,
	}
}

func validConfig(expID string) *config.TrafficConfig {
	return &config.TrafficConfig{
		ExperimentID: expID, EventID: "evt-1", Seed: 1, DurationSeconds: 60,
		Pattern: config.Pattern{Type: "constant", Rate: 10},
		Mix:     config.Mix{Browse: 100},
	}
}

func startCmd(seq int64, expID string) Command {
	return Command{Seq: seq, Action: ActionStart, ExperimentID: expID, Config: validConfig(expID), IssuedAt: time.Now(), IssuedBy: "user@example.com"}
}

func stopCmd(seq int64, expID string) Command {
	return Command{Seq: seq, Action: ActionStop, ExperimentID: expID, IssuedAt: time.Now(), IssuedBy: "user@example.com"}
}

func startController(t *testing.T, store *memStore, runner Runner, opts Options) (*Controller, func()) {
	t.Helper()
	c := New(store, runner, opts)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		if err := c.Run(ctx); err != nil {
			t.Errorf("Run: %v", err)
		}
	}()
	return c, func() { cancel(); <-done }
}

func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func TestStartRunsAndFinishes(t *testing.T) {
	store := newMemStore("exp_a")
	runner := &fakeRunner{duration: 150 * time.Millisecond}
	_, stop := startController(t, store, runner, testOptions())
	defer stop()

	store.cmds <- startCmd(1, "exp_a")
	eventually(t, "running", func() bool {
		st := store.lastStatus()
		return st.State == StateRunning && st.ExperimentID == "exp_a" && st.RunStartedAt != nil && st.LastCommandSeq == 1
	})
	g := store.gen("exp_a")
	if g.Owner != agentID || g.State != GenRunning || g.StartedAt == nil {
		t.Fatalf("generator = %+v", g)
	}
	firstLease := g.LeaseExpiresAt
	eventually(t, "lease renewed", func() bool {
		return store.gen("exp_a").LeaseExpiresAt.After(firstLease) || store.gen("exp_a").State == GenFinished
	})
	eventually(t, "finished", func() bool { return store.gen("exp_a").State == GenFinished })
	g = store.gen("exp_a")
	if g.FinishReason != "completed" || g.FinishedAt == nil || g.Owner != agentID {
		t.Fatalf("generator = %+v", g)
	}
	eventually(t, "idle", func() bool {
		st := store.lastStatus()
		return st.State == StateIdle && st.ExperimentID == "" && st.RunStartedAt == nil
	})
	st := store.lastStatus()
	if st.Limits.MaxRate != 500 || st.TargetBaseURL != "http://target" || st.Version != "test" || st.AgentID != agentID {
		t.Fatalf("status = %+v", st)
	}
}

func TestDuplicateAndOldCommandsAreIgnored(t *testing.T) {
	store := newMemStore("exp_a", "exp_b")
	runner := &fakeRunner{}
	_, stop := startController(t, store, runner, testOptions())
	defer stop()

	cmd := startCmd(5, "exp_a")
	store.cmds <- cmd
	store.cmds <- cmd                  // snapshot redelivery
	store.cmds <- startCmd(3, "exp_b") // older seq
	store.cmds <- stopCmd(4, "exp_a")  // older stop must not stop the run
	eventually(t, "running", func() bool { return store.lastStatus().State == StateRunning })
	time.Sleep(100 * time.Millisecond)
	if runner.calls.Load() != 1 || store.lastStatus().State != StateRunning || store.lastStatus().LastCommandSeq != 5 {
		t.Fatalf("calls=%d status=%+v", runner.calls.Load(), store.lastStatus())
	}
	if !store.gen("exp_b").Empty() {
		t.Fatalf("exp_b touched: %+v", store.gen("exp_b"))
	}
}

func TestStaleStartIsIgnored(t *testing.T) {
	store := newMemStore("exp_old")
	runner := &fakeRunner{}
	_, stop := startController(t, store, runner, testOptions())
	defer stop()

	cmd := startCmd(1, "exp_old")
	cmd.IssuedAt = time.Now().Add(-5 * time.Minute)
	store.cmds <- cmd
	eventually(t, "stale message", func() bool { return strings.Contains(store.lastStatus().Message, "stale") })
	if runner.calls.Load() != 0 || store.lastStatus().LastCommandSeq != 1 {
		t.Fatalf("calls=%d status=%+v", runner.calls.Load(), store.lastStatus())
	}
	if g := store.gen("exp_old"); g.State != GenFinished || g.FinishReason != "error" || !strings.Contains(g.Message, "stale") {
		t.Fatalf("generator = %+v", g)
	}
}

func TestStartWhileRunningIsRejected(t *testing.T) {
	store := newMemStore("exp_a", "exp_b")
	runner := &fakeRunner{}
	_, stop := startController(t, store, runner, testOptions())
	defer stop()

	store.cmds <- startCmd(1, "exp_a")
	eventually(t, "running", func() bool { return store.lastStatus().State == StateRunning })
	store.cmds <- startCmd(2, "exp_b")
	eventually(t, "rejection", func() bool { return strings.Contains(store.lastStatus().Message, "rejected") })
	if runner.calls.Load() != 1 || runner.maxConc.Load() != 1 || store.lastStatus().ExperimentID != "exp_a" {
		t.Fatalf("calls=%d maxConc=%d status=%+v", runner.calls.Load(), runner.maxConc.Load(), store.lastStatus())
	}
	if g := store.gen("exp_b"); g.State != GenFinished || g.FinishReason != "error" {
		t.Fatalf("exp_b generator = %+v", g)
	}
	if store.gen("exp_a").State != GenRunning {
		t.Fatal("exp_a must keep running")
	}
}

func TestStopCancelsRunImmediately(t *testing.T) {
	store := newMemStore("exp_a")
	runner := &fakeRunner{}
	_, stop := startController(t, store, runner, testOptions())
	defer stop()

	store.cmds <- startCmd(1, "exp_a")
	eventually(t, "running", func() bool { return store.lastStatus().State == StateRunning })
	stopAt := time.Now()
	store.cmds <- stopCmd(2, "exp_a")
	eventually(t, "finished", func() bool { return store.gen("exp_a").State == GenFinished })
	if el := time.Since(stopAt); el > time.Second {
		t.Fatalf("stop took %s", el)
	}
	if g := store.gen("exp_a"); g.FinishReason != "stopped" || !strings.Contains(g.Message, "user@example.com") {
		t.Fatalf("generator = %+v", g)
	}
	eventually(t, "idle", func() bool {
		st := store.lastStatus()
		return st.State == StateIdle && strings.Contains(st.Message, "stopped")
	})
}

func TestMaxDurationHardStop(t *testing.T) {
	store := newMemStore("exp_a")
	runner := &fakeRunner{} // ignores the configured duration entirely
	opts := testOptions()
	opts.MaxDuration = 300 * time.Millisecond
	_, stop := startController(t, store, runner, opts)
	defer stop()

	start := time.Now()
	store.cmds <- startCmd(1, "exp_a")
	eventually(t, "finished", func() bool { return store.gen("exp_a").State == GenFinished })
	if el := time.Since(start); el < 300*time.Millisecond || el > 1500*time.Millisecond {
		t.Fatalf("hard stop after %s", el)
	}
	if g := store.gen("exp_a"); g.FinishReason != "max_duration" {
		t.Fatalf("generator = %+v", g)
	}
}

func TestRestartRecoveryMarksInterruptedRun(t *testing.T) {
	store := newMemStore()
	prevAgent := "loadgen-vm/boot-old"
	started := time.Now().Add(-time.Minute)
	store.status = &Status{AgentID: prevAgent, State: StateRunning, ExperimentID: "exp_a", LastCommandSeq: 7, RunStartedAt: &started}
	store.experiments["exp_a"] = &Generator{Owner: prevAgent, State: GenRunning, LeaseExpiresAt: time.Now().Add(10 * time.Second), StartedAt: &started}
	runner := &fakeRunner{}
	_, stop := startController(t, store, runner, testOptions())
	defer stop()

	eventually(t, "recovered", func() bool { return store.gen("exp_a").State == GenFinished })
	g := store.gen("exp_a")
	if g.FinishReason != "error" || g.Message != RestartMessage || g.Owner != prevAgent {
		t.Fatalf("generator = %+v", g)
	}
	// The start that launched the interrupted run (seq 7) is redelivered and must not resume traffic.
	store.cmds <- startCmd(7, "exp_a")
	eventually(t, "idle heartbeat", func() bool {
		st := store.lastStatus()
		return st.AgentID == agentID && st.State == StateIdle && st.LastCommandSeq == 7
	})
	time.Sleep(50 * time.Millisecond)
	if runner.calls.Load() != 0 {
		t.Fatal("traffic was resumed after restart")
	}
	// Even a newer start for the same experiment cannot restart finished traffic.
	store.cmds <- startCmd(8, "exp_a")
	eventually(t, "claim refused", func() bool { return strings.Contains(store.lastStatus().Message, "already finished") })
	if runner.calls.Load() != 0 {
		t.Fatal("finished experiment was restarted")
	}
}

func TestRecoveryLeavesOtherHostsRunAlone(t *testing.T) {
	store := newMemStore()
	other := "other-vm/boot-x"
	store.status = &Status{AgentID: other, State: StateRunning, ExperimentID: "exp_a", LastCommandSeq: 3}
	store.experiments["exp_a"] = &Generator{Owner: other, State: GenRunning, LeaseExpiresAt: time.Now().Add(time.Minute)}
	_, stop := startController(t, store, &fakeRunner{}, testOptions())
	defer stop()
	eventually(t, "heartbeat", func() bool { return store.lastStatus().AgentID == agentID })
	if g := store.gen("exp_a"); g.State != GenRunning || g.Owner != other {
		t.Fatalf("generator = %+v", g)
	}
}

func TestClaimRespectsLiveLeaseAndTakesExpiredOne(t *testing.T) {
	store := newMemStore()
	store.experiments["exp_live"] = &Generator{Owner: "someone/boot-1", State: GenRunning, LeaseExpiresAt: time.Now().Add(time.Minute)}
	store.experiments["exp_expired"] = &Generator{Owner: "someone/boot-1", State: GenRunning, LeaseExpiresAt: time.Now().Add(-time.Minute)}
	runner := &fakeRunner{duration: 50 * time.Millisecond}
	_, stop := startController(t, store, runner, testOptions())
	defer stop()

	store.cmds <- startCmd(1, "exp_live")
	eventually(t, "claim failure", func() bool { return strings.Contains(store.lastStatus().Message, "claim failed") })
	if runner.calls.Load() != 0 || store.gen("exp_live").Owner != "someone/boot-1" {
		t.Fatal("must not take over a live lease")
	}
	store.cmds <- startCmd(2, "exp_expired")
	eventually(t, "finished", func() bool { return store.gen("exp_expired").State == GenFinished })
	if g := store.gen("exp_expired"); g.Owner != agentID || g.FinishReason != "completed" {
		t.Fatalf("generator = %+v", g)
	}
}

func TestLeaseLostStopsRun(t *testing.T) {
	store := newMemStore("exp_a")
	runner := &fakeRunner{}
	_, stop := startController(t, store, runner, testOptions())
	defer stop()

	store.cmds <- startCmd(1, "exp_a")
	eventually(t, "running", func() bool { return store.lastStatus().State == StateRunning })
	store.mu.Lock()
	store.experiments["exp_a"].Owner = "thief/boot-1"
	store.mu.Unlock()
	eventually(t, "idle", func() bool { return store.lastStatus().State == StateIdle })
	if runner.active.Load() != 0 || !strings.Contains(store.lastStatus().Message, "lease lost") {
		t.Fatalf("status = %+v", store.lastStatus())
	}
}

func TestInvalidStartIsRejected(t *testing.T) {
	store := newMemStore("exp_a", "exp_b")
	runner := &fakeRunner{}
	_, stop := startController(t, store, runner, testOptions())
	defer stop()

	bad := startCmd(1, "exp_a")
	bad.Config.Mix = config.Mix{Browse: 10}
	store.cmds <- bad
	eventually(t, "rejected", func() bool { return store.gen("exp_a").State == GenFinished })
	if !strings.Contains(store.gen("exp_a").Message, "sum to 100") {
		t.Fatalf("generator = %+v", store.gen("exp_a"))
	}
	mismatch := startCmd(2, "exp_b")
	mismatch.Config.ExperimentID = "exp_other"
	store.cmds <- mismatch
	eventually(t, "mismatch rejected", func() bool { return store.gen("exp_b").State == GenFinished })
	store.cmds <- Command{Seq: 3, Action: ActionStart, ExperimentID: "../evil", IssuedAt: time.Now(), Config: validConfig("../evil")}
	eventually(t, "bad id", func() bool { return strings.Contains(store.lastStatus().Message, "invalid experimentId") })
	if runner.calls.Load() != 0 {
		t.Fatal("invalid start ran")
	}
}

func TestListenerResubscribesAfterError(t *testing.T) {
	store := newMemStore("exp_a")
	store.watchErrs = 1
	runner := &fakeRunner{duration: 10 * time.Millisecond}
	_, stop := startController(t, store, runner, testOptions())
	defer stop()
	store.cmds <- startCmd(1, "exp_a")
	eventually(t, "finished after resubscribe", func() bool { return store.gen("exp_a").State == GenFinished })
}

func TestShutdownFinishesActiveRun(t *testing.T) {
	store := newMemStore("exp_a")
	runner := &fakeRunner{}
	_, stop := startController(t, store, runner, testOptions())
	store.cmds <- startCmd(1, "exp_a")
	eventually(t, "running", func() bool { return store.lastStatus().State == StateRunning })
	stop()
	if g := store.gen("exp_a"); g.State != GenFinished || g.FinishReason != "error" {
		t.Fatalf("generator = %+v", g)
	}
	if st := store.lastStatus(); st.State != StateIdle {
		t.Fatalf("final status = %+v", st)
	}
}

func TestGeneratorTransitions(t *testing.T) {
	now := time.Now()
	if _, err := claim("a", now, time.Second)(Generator{}, false); !errors.Is(err, ErrExperimentNotFound) {
		t.Fatalf("claim on missing doc: %v", err)
	}
	g, err := claim("a", now, time.Second)(Generator{}, true)
	if err != nil || g.Owner != "a" || !g.LeaseExpiresAt.Equal(now.Add(time.Second)) {
		t.Fatalf("claim: %+v %v", g, err)
	}
	if _, err := claim("b", now, time.Second)(g, true); !errors.Is(err, ErrClaimed) {
		t.Fatalf("claim of live lease: %v", err)
	}
	if _, err := claim("b", now.Add(2*time.Second), time.Second)(g, true); err != nil {
		t.Fatalf("claim of expired lease: %v", err)
	}
	if _, err := renew("b", now, time.Second)(g, true); !errors.Is(err, ErrLeaseLost) {
		t.Fatalf("renew by non-owner: %v", err)
	}
	f, err := finish("a", now, "stopped", "x")(g, true)
	if err != nil || f.State != GenFinished || f.FinishReason != "stopped" {
		t.Fatalf("finish: %+v %v", f, err)
	}
	if _, err := finish("a", now, "stopped", "x")(f, true); !errors.Is(err, ErrAlreadyFinished) {
		t.Fatalf("double finish: %v", err)
	}
	if _, err := claim("a", now.Add(time.Hour), time.Second)(f, true); !errors.Is(err, ErrAlreadyFinished) {
		t.Fatalf("claim finished: %v", err)
	}
	if _, err := reject("a", now, "no")(f, true); !errors.Is(err, ErrNotEmpty) {
		t.Fatalf("reject must not overwrite: %v", err)
	}
}

func TestSameHost(t *testing.T) {
	if !sameHost("vm/boot-1", "vm/boot-2") || !sameHost("fixed-id", "fixed-id") || sameHost("vm-a/boot-1", "vm-b/boot-1") || sameHost("", "x") {
		t.Fatal("sameHost")
	}
}
