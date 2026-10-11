package queue

import (
	"context"
	"os"
	"sync"
	"time"
)

// Store kinds (STORE environment variable).
const (
	StoreMemory = "memory"
	StoreRedis  = "redis"
)

// Store is the waiting room as the HTTP server, the pollers and main use it. Two implementations:
//
//   - memory (Memory(New(...))): one process holds the line; it is the leader by definition.
//   - redis (NewRedis): every instance shares one Redis; any instance serves join, status, release
//     and stats, and only the elected leader runs the admission tick, the controller and the pollers
//     (docs/waiting-room.md, "High availability").
//
// Errors are returned only where the shared store can fail; the memory store never fails.
type Store interface {
	Kind() string
	// Config returns the current configuration (the redis store falls back to the last value it read
	// if Redis is unreachable).
	Config() Config
	Configure(Config) (Config, error)
	Reset() error
	Join(eventID, customerRef string) (View, error)
	Status(ctx context.Context, id string, wait time.Duration) (View, error)
	Release(id, outcome string) (bool, error)
	Stats() (Stats, error)

	// Leader-side inputs and steps. On a redis follower they are no-ops.
	SetCapacity(CapacityUpdate)
	HealthyServers() []Server
	SetInventory(eventID string, available, held int, appSoldOut bool, at time.Time)
	SetInventoryUnknown(eventID, reason string)
	SetSignals(name string, s Signals)
	Tick()
	Control()
	// Run and RunControl call Tick and Control every interval until ctx is done.
	Run(ctx context.Context, interval time.Duration)
	RunControl(ctx context.Context, interval time.Duration)

	// Leading reports whether this instance runs the admission loop and the pollers now.
	Leading() bool
	// Close answers every open long poll with its current status and stops background work.
	Close()
}

// Memory adapts the in-memory Room to Store.
func Memory(r *Room) Store { return memoryStore{r} }

type memoryStore struct{ *Room }

func (memoryStore) Kind() string   { return StoreMemory }
func (memoryStore) Leading() bool  { return true }
func (m memoryStore) Reset() error { m.Room.Reset(); return nil }
func (m memoryStore) Configure(c Config) (Config, error) {
	return m.Room.Configure(c), nil
}
func (m memoryStore) Release(id, outcome string) (bool, error) {
	return m.Room.Release(id, outcome), nil
}
func (m memoryStore) Stats() (Stats, error) { return m.Room.Stats(), nil }

var (
	hostOnce sync.Once
	hostName string
)

// Hostname is this instance's name in Stats (os.Hostname, "unknown" if unavailable).
func Hostname() string {
	hostOnce.Do(func() {
		hostName, _ = os.Hostname()
		if hostName == "" {
			hostName = "unknown"
		}
	})
	return hostName
}

var (
	_ Store = memoryStore{}
	_ Store = (*RedisRoom)(nil)
)
