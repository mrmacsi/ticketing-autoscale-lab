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

	room := queue.New(token.NewSigner(adminToken), nil)
	room.SetCapacity(queue.CapacityUpdate{Source: source.Name()})

	bg, cancelBG := context.WithCancel(context.Background())
	defer cancelBG()
	go room.Run(bg, 100*time.Millisecond)
	go (&capacity.Monitor{Source: source, Apply: room.SetCapacity, Interval: ms(capPoll), Log: log}).Run(bg)
	go (&inventory.Poller{Room: room, Client: appClient, Interval: ms(invPoll), Log: log}).Run(bg)
	go (&signals.Poller{Room: room, Client: appClient, Interval: ms(sigPoll), Log: log}).Run(bg)
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
	log.Info("waiting room listening", "version", version, "port", port, "capacitySource", source.Name(),
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

// logSummary writes one line every 10 s while the room is enabled.
func logSummary(ctx context.Context, room *queue.Room, log *slog.Logger) {
	t := time.NewTicker(10 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s := room.Stats()
			if !s.Enabled {
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
