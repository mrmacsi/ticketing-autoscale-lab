// Command loadgen is the ticketing lab traffic generator.
//
//	loadgen agent   long-running Firestore-driven agent (default)
//	loadgen run     one-shot local run without Firestore
//	loadgen version print the version
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"cloud.google.com/go/compute/metadata"
	"cloud.google.com/go/firestore"
	"github.com/google/uuid"

	"github.com/mrmacsi/ticketing-autoscale-lab/load-generator/config"
	"github.com/mrmacsi/ticketing-autoscale-lab/load-generator/controller"
	"github.com/mrmacsi/ticketing-autoscale-lab/load-generator/engine"
	"github.com/mrmacsi/ticketing-autoscale-lab/load-generator/fsstore"
	"github.com/mrmacsi/ticketing-autoscale-lab/load-generator/metrics"
	"github.com/mrmacsi/ticketing-autoscale-lab/load-generator/ticketapi"
)

// version is overridden at build time with -ldflags "-X main.version=...".
var version = "1.0.0"

func main() {
	cmd := "agent"
	args := os.Args[1:]
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		cmd, args = args[0], args[1:]
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	var err error
	switch cmd {
	case "agent":
		err = runAgent(ctx)
	case "run":
		err = runLocal(ctx, args)
	case "version":
		fmt.Println(version)
	default:
		err = fmt.Errorf("unknown command %q (want agent, run or version)", cmd)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "loadgen:", err)
		os.Exit(1)
	}
}

func runAgent(ctx context.Context) error {
	log := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))

	target := os.Getenv("TARGET_BASE_URL")
	if target == "" {
		return errors.New("TARGET_BASE_URL is required")
	}
	allowed := splitList(os.Getenv("ALLOWED_TARGET_HOSTS"))
	if err := checkTarget(target, allowed, false); err != nil {
		return err
	}
	queueURL, err := queueBaseURL(os.Getenv("QUEUE_BASE_URL"), target, allowed, false)
	if err != nil {
		return err
	}
	limits, err := limitsFromEnv()
	if err != nil {
		return err
	}
	heartbeat, err := envInt("HEARTBEAT_SECONDS", 2)
	if err != nil {
		return err
	}
	project := os.Getenv("GOOGLE_CLOUD_PROJECT")
	if project == "" {
		project = firestore.DetectProjectID
	}
	database := envString("FIRESTORE_DATABASE", "ticketing-scale-lab")
	agentID := agentIDFromEnv(ctx)

	client, err := firestore.NewClientWithDatabase(ctx, project, database)
	if err != nil {
		return fmt.Errorf("firestore client: %w", err)
	}
	defer client.Close()
	store := fsstore.New(client)

	runner := controller.RunnerFunc(func(ctx context.Context, cfg config.TrafficConfig) engine.Result {
		sink := metrics.NewAsyncSink(store.TrafficWriter(cfg.ExperimentID), metrics.AsyncSinkOptions{Logger: log})
		res := engine.Run(ctx, cfg, engine.Options{TargetBaseURL: target, QueueBaseURL: queueURL, Limits: limits, Sink: sink, Logger: log})
		cctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		if err := sink.Close(cctx); err != nil {
			log.Error("traffic buckets incomplete", "experimentId", cfg.ExperimentID, "err", err)
			res.Message += "; " + err.Error()
		}
		return res
	})

	log.Info("agent starting", "agentId", agentID, "version", version, "target", target, "queue", queueURL,
		"database", database, "emulator", os.Getenv("FIRESTORE_EMULATOR_HOST") != "", "limits", limits)
	ctrl := controller.New(store, runner, controller.Options{
		AgentID:           agentID,
		Version:           version,
		TargetBaseURL:     target,
		Limits:            limits,
		HeartbeatInterval: time.Duration(heartbeat) * time.Second,
		Logger:            log,
	})
	return ctrl.Run(ctx)
}

func runLocal(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	cfgPath := fs.String("config", "", "TrafficConfig JSON file (required)")
	target := fs.String("target", os.Getenv("TARGET_BASE_URL"), "ticketing API base URL")
	queue := fs.String("queue", os.Getenv("QUEUE_BASE_URL"), "waiting room base URL (default: --target)")
	out := fs.String("out", "", "write bucket JSON lines to this file instead of stdout")
	summary := fs.Bool("summary", false, "print the merged run summary as JSON to stderr")
	quiet := fs.Bool("quiet", false, "suppress progress logs")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *cfgPath == "" || *target == "" {
		fs.Usage()
		return errors.New("--config and --target are required")
	}
	allowed := splitList(os.Getenv("ALLOWED_TARGET_HOSTS"))
	if err := checkTarget(*target, allowed, true); err != nil {
		return err
	}
	queueURL, err := queueBaseURL(*queue, *target, allowed, true)
	if err != nil {
		return err
	}
	limits, err := limitsFromEnv()
	if err != nil {
		return err
	}
	raw, err := os.ReadFile(*cfgPath)
	if err != nil {
		return err
	}
	cfg, err := config.Parse(raw)
	if err != nil {
		return err
	}
	if cfg.ExperimentID == "" {
		cfg.ExperimentID = "local"
	}

	var w io.Writer = os.Stdout
	if *out != "" {
		f, err := os.Create(*out)
		if err != nil {
			return err
		}
		defer f.Close()
		w = f
	}
	logLevel := slog.LevelInfo
	if *quiet {
		logLevel = slog.LevelError
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: logLevel}))
	sink := metrics.NewJSONLSink(w)
	res := engine.Run(ctx, cfg, engine.Options{TargetBaseURL: *target, QueueBaseURL: queueURL, Limits: limits, Sink: sink, Logger: log})
	if err := sink.Close(context.Background()); err != nil {
		return fmt.Errorf("write buckets: %w", err)
	}
	if *summary {
		enc := json.NewEncoder(os.Stderr)
		enc.SetIndent("", "  ")
		_ = enc.Encode(map[string]any{
			"finishReason": res.Reason,
			"message":      res.Message,
			"startedAt":    res.StartedAt.UTC(),
			"finishedAt":   res.FinishedAt.UTC(),
			"notes":        res.Notes,
			"summary":      res.Summary,
		})
	}
	if res.Err != nil {
		return res.Err
	}
	return nil
}

// checkTarget enforces the target allowlist. Local runs may also target
// loopback hosts without an allowlist entry.
func checkTarget(raw string, allowed []string, allowLoopback bool) error {
	u, err := ticketapi.ParseBaseURL(raw)
	if err != nil {
		return err
	}
	host := strings.ToLower(u.Hostname())
	for _, a := range allowed {
		a = strings.ToLower(a)
		if a == host || a == strings.ToLower(u.Host) {
			return nil
		}
	}
	if allowLoopback {
		if host == "localhost" {
			return nil
		}
		if ip := net.ParseIP(host); ip != nil && ip.IsLoopback() {
			return nil
		}
	}
	return fmt.Errorf("target host %q is not in ALLOWED_TARGET_HOSTS", u.Host)
}

// queueBaseURL resolves QUEUE_BASE_URL (default: the target) and applies the
// same host allowlist as the target, so the waiting room can never be used to
// send traffic anywhere else.
func queueBaseURL(raw, target string, allowed []string, allowLoopback bool) (string, error) {
	if strings.TrimSpace(raw) == "" {
		raw = target
	}
	if err := checkTarget(raw, allowed, allowLoopback); err != nil {
		return "", fmt.Errorf("QUEUE_BASE_URL: %w", err)
	}
	return strings.TrimSpace(raw), nil
}

func agentIDFromEnv(ctx context.Context) string {
	if id := strings.TrimSpace(os.Getenv("AGENT_ID")); id != "" {
		return id
	}
	host := ""
	mctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	if metadata.OnGCEWithContext(mctx) {
		if name, err := metadata.InstanceNameWithContext(mctx); err == nil {
			host = name
		}
	}
	if host == "" {
		host, _ = os.Hostname()
	}
	if host == "" {
		host = "loadgen"
	}
	return host + "/boot-" + uuid.NewString()
}

func limitsFromEnv() (config.Limits, error) {
	l := config.DefaultLimits()
	var err error
	if v := os.Getenv("MAX_RATE"); v != "" {
		if l.MaxRate, err = strconv.ParseFloat(v, 64); err != nil || l.MaxRate <= 0 {
			return l, fmt.Errorf("invalid MAX_RATE %q", v)
		}
	}
	if l.MaxVUs, err = envInt("MAX_VUS", l.MaxVUs); err != nil {
		return l, err
	}
	if l.MaxDurationSeconds, err = envInt("MAX_DURATION_SECONDS", l.MaxDurationSeconds); err != nil {
		return l, err
	}
	return l, nil
}

func envInt(name string, def int) (int, error) {
	v := os.Getenv(name)
	if v == "" {
		return def, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil || n <= 0 {
		return 0, fmt.Errorf("invalid %s %q", name, v)
	}
	return n, nil
}

func envString(name, def string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return def
}

func splitList(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}
