// Package controller turns Firestore commands into at most one traffic run at
// a time: it tracks command sequence numbers, claims experiment ownership
// with a lease, heartbeats generator/status, enforces stop and the max
// duration hard stop, and recovers safely after a restart.
package controller

import (
	"context"
	"errors"
	"time"

	"github.com/mrmacsi/ticketing-autoscale-lab/load-generator/config"
)

// Command actions.
const (
	ActionStart = "start"
	ActionStop  = "stop"
)

// Command is the generator/command document.
type Command struct {
	Seq          int64
	Action       string
	ExperimentID string
	Config       *config.TrafficConfig
	IssuedAt     time.Time
	IssuedBy     string
	// DecodeErr is set when the document existed but could not be decoded.
	DecodeErr error
}

// State is generator/status.state.
type State string

// Agent states.
const (
	StateIdle     State = "idle"
	StateRunning  State = "running"
	StateStopping State = "stopping"
	StateError    State = "error"
)

// Status is the generator/status document.
type Status struct {
	AgentID        string
	State          State
	ExperimentID   string // "" is written as null
	LastCommandSeq int64
	HeartbeatAt    time.Time
	RunStartedAt   *time.Time
	Message        string
	Version        string
	Limits         config.Limits
	TargetBaseURL  string
}

// Generator states inside experiments/{id}.generator.
const (
	GenRunning  = "running"
	GenFinished = "finished"
)

// Generator is the experiments/{id}.generator map.
type Generator struct {
	Owner          string
	LeaseExpiresAt time.Time
	State          string
	StartedAt      *time.Time
	FinishedAt     *time.Time
	FinishReason   string
	Message        string
}

// Empty reports whether the generator map has never been written.
func (g Generator) Empty() bool { return g.Owner == "" && g.State == "" }

// Errors returned by generator transitions.
var (
	ErrExperimentNotFound = errors.New("experiment document not found")
	ErrClaimed            = errors.New("experiment is owned by another generator with a live lease")
	ErrAlreadyFinished    = errors.New("experiment traffic already finished")
	ErrLeaseLost          = errors.New("generator lease lost")
	ErrNotEmpty           = errors.New("generator map already set")
)

// GeneratorUpdate computes the new generator map from the current one.
// exists reports whether the experiment document exists. Returning an error
// aborts the transaction without writing.
type GeneratorUpdate func(current Generator, exists bool) (Generator, error)

// Store abstracts Firestore so the controller logic is testable in memory.
type Store interface {
	// WatchCommands delivers every snapshot of generator/command to fn
	// (synchronously, in order) until ctx is cancelled or the listener fails.
	WatchCommands(ctx context.Context, fn func(Command)) error
	// LoadStatus reads generator/status; ok is false when it does not exist.
	LoadStatus(ctx context.Context) (st Status, ok bool, err error)
	WriteStatus(ctx context.Context, st Status) error
	// UpdateGenerator applies fn to experiments/{id}.generator atomically
	// (read-modify-write in one transaction).
	UpdateGenerator(ctx context.Context, experimentID string, fn GeneratorUpdate) error
}

// claim takes ownership when the map is empty or its lease has expired.
func claim(agentID string, now time.Time, lease time.Duration) GeneratorUpdate {
	return func(g Generator, exists bool) (Generator, error) {
		if !exists {
			return g, ErrExperimentNotFound
		}
		if g.State == GenFinished {
			return g, ErrAlreadyFinished
		}
		if g.Owner != "" && g.Owner != agentID && g.LeaseExpiresAt.After(now) {
			return g, ErrClaimed
		}
		started := now
		return Generator{Owner: agentID, LeaseExpiresAt: now.Add(lease), State: GenRunning, StartedAt: &started}, nil
	}
}

// renew extends the lease while this agent still owns a running experiment.
func renew(agentID string, now time.Time, lease time.Duration) GeneratorUpdate {
	return func(g Generator, exists bool) (Generator, error) {
		if !exists || g.Owner != agentID || g.State != GenRunning {
			return g, ErrLeaseLost
		}
		g.LeaseExpiresAt = now.Add(lease)
		return g, nil
	}
}

// finish marks the run finished and releases the lease. Only the owner may
// finish; finishing twice is a no-op error (ErrAlreadyFinished).
func finish(owner string, now time.Time, reason, message string) GeneratorUpdate {
	return func(g Generator, exists bool) (Generator, error) {
		if !exists {
			return g, ErrExperimentNotFound
		}
		if g.Owner != owner {
			return g, ErrLeaseLost
		}
		if g.State == GenFinished {
			return g, ErrAlreadyFinished
		}
		at := now
		g.State = GenFinished
		g.FinishedAt = &at
		g.FinishReason = reason
		g.Message = message
		g.LeaseExpiresAt = now
		return g, nil
	}
}

// reject records a refused start on an experiment that no generator has
// touched yet, so control-api sees the reason. It never overwrites an
// existing generator map (e.g. of an older, finished experiment).
func reject(agentID string, now time.Time, message string) GeneratorUpdate {
	return func(g Generator, exists bool) (Generator, error) {
		if !exists {
			return g, ErrExperimentNotFound
		}
		if !g.Empty() {
			return g, ErrNotEmpty
		}
		at := now
		return Generator{Owner: agentID, LeaseExpiresAt: now, State: GenFinished, FinishedAt: &at, FinishReason: "error", Message: message}, nil
	}
}
