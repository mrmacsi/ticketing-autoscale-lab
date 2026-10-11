// Command waiting-room is the lab's FIFO queue server (docs/waiting-room.md).
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	redis "github.com/redis/go-redis/v9"

	"github.com/mrmacsi/ticketing-autoscale-lab/waiting-room/capacity"
	"github.com/mrmacsi/ticketing-autoscale-lab/waiting-room/gcp"
	"github.com/mrmacsi/ticketing-autoscale-lab/waiting-room/inventory"
	"github.com/mrmacsi/ticketing-autoscale-lab/waiting-room/queue"
	"github.com/mrmacsi/ticketing-autoscale-lab/waiting-room/server"
	"github.com/mrmacsi/ticketing-autoscale-lab/waiting-room/signals"
	"github.com/mrmacsi/ticketing-autoscale-lab/waiting-room/token"
)

var version = "dev"

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, log); err != nil {
		log.Error("waiting room stopped", "err", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, log *slog.Logger) error {
	port, err := envInt("PORT", 8080)
	if err != nil {
		return err
	}
	appPort, err := envInt("APP_PORT", 8080)
	if err != nil {
		return err
	}
	capPoll, err := envInt("CAPACITY_POLL_MS", 2000)
	if err != nil {
		return err
	}
	invPoll, err := envInt("INVENTORY_POLL_MS", 1000)
	if err != nil {
		return err
	}
	sigPoll, err := envInt("SIGNAL_POLL_MS", 1000)
	if err != nil {
		return err
	}
	controlInterval, err := envInt("CONTROL_INTERVAL_MS", 1000)
	if err != nil {
		return err
	}

	tokens := gcp.NewMetadataTokens()
	adminToken, err := resolveAdminToken(ctx, tokens)
	if err != nil {
		return err
	}

	apiClient := &http.Client{Timeout: 10 * time.Second}
	appClient := &http.Client{
		Timeout: 5 * time.Second,
		Transport: &http.Transport{
			DialContext:         (&net.Dialer{Timeout: 2 * time.Second}).DialContext,
			MaxIdleConnsPerHost: 4,
			IdleConnTimeout:     30 * time.Second,
		},
	}
	source, err := capacitySource(tokens, apiClient, appClient, appPort)
	if err != nil {
		return err
	}

	room, closeStore, err := openStore(ctx, token.NewSigner(adminToken), log)
	if err != nil {
		return err
	}
	defer closeStore()
	room.SetCapacity(queue.CapacityUpdate{Source: source.Name()})

	// Every instance serves customers; only the leader (always, for the memory store) admits, runs the
	// controller and polls the app servers. A new leader picks up within one interval.
	bg, cancelBG := context.WithCancel(context.Background())
	defer cancelBG()
	monitor := &capacity.Monitor{Source: source, Apply: room.SetCapacity, Interval: ms(capPoll), Log: log}
	invPoller := &inventory.Poller{Room: room, Client: appClient, Interval: ms(invPoll), Log: log}
	sigPoller := &signals.Poller{Room: room, Client: appClient, Interval: ms(sigPoll), Log: log}
	if room.Leading() {
		monitor.Poll(bg)
	}
	go room.Run(bg, 100*time.Millisecond)
	go whileLeading(bg, room, ms(capPoll), monitor.Poll)
	go whileLeading(bg, room, ms(invPoll), invPoller.Poll)
	go whileLeading(bg, room, ms(sigPoll), sigPoller.Poll)
	go room.RunControl(bg, ms(controlInterval))
	go logSummary(bg, room, log)

	srv := &http.Server{
		Addr:              ":" + strconv.Itoa(port),
		Handler:           withVersion(server.New(room, adminToken, log), version),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      server.MaxWait + 15*time.Second, // must exceed the 25 s long poll
		IdleTimeout:       120 * time.Second,
		MaxHeaderBytes:    16 << 10,
		ErrorLog:          slog.NewLogLogger(log.Handler(), slog.LevelWarn),
	}
	errc := make(chan error, 1)
	go func() { errc <- srv.ListenAndServe() }()
	log.Info("waiting room listening", "version", version, "port", port, "store", room.Kind(), "instance", queue.Hostname(),
		"capacitySource", source.Name(),
		"appPort", appPort, "capacityPollMs", capPoll, "inventoryPollMs", invPoll, "signalPollMs", sigPoll, "controlIntervalMs", controlInterval)

	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
	}
	log.Info("shutting down")
	room.Close() // answer every long poll now with its current status
	sctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	if err := srv.Shutdown(sctx); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

func resolveAdminToken(ctx context.Context, tokens gcp.TokenSource) (string, error) {
	if t := os.Getenv("LAB_ADMIN_TOKEN"); t != "" {
		return t, nil
	}
	name := os.Getenv("LAB_ADMIN_TOKEN_SECRET")
	if name == "" {
		return "", errors.New("LAB_ADMIN_TOKEN or LAB_ADMIN_TOKEN_SECRET is required")
	}
	client := &http.Client{Timeout: 10 * time.Second}
	var lastErr error
	for attempt := range 5 { // the metadata server or IAM may lag a freshly created VM
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return "", ctx.Err()
			case <-time.After(time.Duration(attempt) * 2 * time.Second):
			}
		}
		t, err := gcp.AccessSecret(ctx, client, tokens, "", name)
		if err == nil {
			return t, nil
		}
		lastErr = err
	}
	return "", fmt.Errorf("read admin token: %w", lastErr)
}

func capacitySource(tokens gcp.TokenSource, apiClient, appClient *http.Client, appPort int) (capacity.Source, error) {
	kind := os.Getenv("CAPACITY_SOURCE")
	if kind == "" {
		kind = "gce"
		if os.Getenv("STATIC_BACKENDS") != "" {
			kind = "static"
		}
	}
	switch kind {
	case "gce":
		g := &capacity.GCE{
			Project:        os.Getenv("GCP_PROJECT_ID"),
			BackendService: os.Getenv("BACKEND_SERVICE_NAME"),
			Group:          os.Getenv("INSTANCE_GROUP_URL"),
			AppPort:        appPort,
			Tokens:         tokens,
			Client:         apiClient,
		}
		if g.Project == "" || g.BackendService == "" || g.Group == "" {
			return nil, errors.New("CAPACITY_SOURCE=gce needs GCP_PROJECT_ID, BACKEND_SERVICE_NAME and INSTANCE_GROUP_URL")
		}
		return g, nil
	case "static":
		urls, err := capacity.ParseStaticBackends(os.Getenv("STATIC_BACKENDS"))
		if err != nil {
			return nil, err
		}
		return &capacity.Static{BaseURLs: urls, Client: appClient}, nil
	default:
		return nil, fmt.Errorf("CAPACITY_SOURCE must be gce or static, got %q", kind)
	}
}

// openStore builds the store selected by STORE: memory (default, one instance) or redis
// (REDIS_ADDR, optional REDIS_PASSWORD; any number of instances share it and elect a leader).
func openStore(ctx context.Context, signer *token.Signer, log *slog.Logger) (queue.Store, func(), error) {
	switch kind := os.Getenv("STORE"); kind {
	case "", queue.StoreMemory:
		room := queue.Memory(queue.New(signer, nil))
		return room, func() {}, nil
	case queue.StoreRedis:
		addr := os.Getenv("REDIS_ADDR")
		if addr == "" {
			return nil, nil, errors.New("STORE=redis needs REDIS_ADDR (host:port)")
		}
		rdb := redis.NewClient(&redis.Options{
			Addr:         addr,
			Password:     os.Getenv("REDIS_PASSWORD"),
			DialTimeout:  2 * time.Second,
			ReadTimeout:  2 * time.Second,
			WriteTimeout: 2 * time.Second,
			PoolSize:     64,
			MinIdleConns: 4,
		})
		var room *queue.RedisRoom
		var err error
		for attempt := range 15 { // Redis may still be starting (compose) or the network not ready (VM boot)
			if attempt > 0 {
				log.Warn("redis not reachable yet", "addr", addr, "err", err)
				select {
				case <-ctx.Done():
					rdb.Close()
					return nil, nil, ctx.Err()
				case <-time.After(2 * time.Second):
				}
			}
			cctx, cancel := context.WithTimeout(ctx, 3*time.Second)
			room, err = queue.NewRedis(cctx, rdb, signer, queue.RedisOptions{Elect: true, Log: log})
			cancel()
			if err == nil {
				break
			}
		}
		if err != nil {
			rdb.Close()
			return nil, nil, err
		}
		log.Info("redis store connected", "addr", addr, "instance", room.Instance(), "leader", room.Leading())
		return room, func() { room.Close(); rdb.Close() }, nil
	default:
		return nil, nil, fmt.Errorf("STORE must be memory or redis, got %q", kind)
	}
}

// whileLeading calls f every interval while this instance is the leader.
func whileLeading(ctx context.Context, room queue.Store, interval time.Duration, f func(context.Context)) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if room.Leading() {
				f(ctx)
			}
		}
	}
}

// logSummary writes one line every 10 s while the room is enabled (leader only, so N instances do
// not log the same numbers N times).
func logSummary(ctx context.Context, room queue.Store, log *slog.Logger) {
	t := time.NewTicker(10 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if !room.Leading() {
				continue
			}
			s, err := room.Stats()
			if err != nil || !s.Enabled {
				continue
			}
			log.Info("room", "waiting", s.Waiting, "active", s.Active, "capacity", s.Capacity,
				"healthyServers", s.HealthyServers, "joined", s.JoinedTotal, "admitted", s.AdmittedTotal,
				"released", s.ReleasedTotal, "expired", s.ExpiredTotal, "soldOutTotal", s.SoldOutTotal,
				"soldOut", s.SoldOut, "ratePerSecond", s.AdmissionRatePerSecond, "waitP95Ms", s.WaitMs.P95)
		}
	}
}

func envInt(name string, def int) (int, error) {
	v := os.Getenv(name)
	if v == "" {
		return def, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil || n <= 0 {
		return 0, fmt.Errorf("%s must be a positive integer, got %q", name, v)
	}
	return n, nil
}

func ms(n int) time.Duration { return time.Duration(n) * time.Millisecond }

func withVersion(s *server.Server, v string) *server.Server {
	s.Version = v
	return s
}
