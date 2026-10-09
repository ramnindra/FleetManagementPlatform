// Command controller is the cloud device controller service.
//
//	controller              run the HTTP API + MQTT client
//	controller migrate      apply DB migrations and exit (used by the Helm migration Job)
//	controller healthcheck  probe /healthz and exit 0/1 (used by the container HEALTHCHECK)
package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/ramnindra/fleetmanagementplatform/controller/internal/api"
	"github.com/ramnindra/fleetmanagementplatform/controller/internal/config"
	"github.com/ramnindra/fleetmanagementplatform/controller/internal/metrics"
	"github.com/ramnindra/fleetmanagementplatform/controller/internal/mqttc"
	"github.com/ramnindra/fleetmanagementplatform/controller/internal/service"
	"github.com/ramnindra/fleetmanagementplatform/controller/internal/store"
)

func main() {
	cfg := config.Load()
	setupLogging(cfg.LogLevel)

	cmd := ""
	if len(os.Args) > 1 {
		cmd = os.Args[1]
	}
	switch cmd {
	case "", "serve":
		serve(cfg)
	case "migrate":
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		pg := connectWithRetry(ctx, cfg)
		defer pg.Close()
		if err := pg.Migrate(ctx); err != nil {
			slog.Error("migrate_failed", "error", err)
			os.Exit(1)
		}
	case "healthcheck":
		healthcheck(cfg)
	default:
		slog.Error("unknown command", "command", cmd)
		os.Exit(2)
	}
}

func setupLogging(level string) {
	var l slog.Level
	if err := l.UnmarshalText([]byte(strings.ToUpper(level))); err != nil {
		l = slog.LevelInfo
	}
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: l})))
}

// connectWithRetry waits for Postgres: on a fresh Kubernetes deploy the
// controller pod can start before the database accepts connections.
func connectWithRetry(ctx context.Context, cfg config.Settings) *store.Postgres {
	for attempt := 1; ; attempt++ {
		pg, err := store.NewPostgres(ctx, cfg.DatabaseURL, cfg.DBMaxConns)
		if err == nil {
			if err = pg.Ping(ctx); err == nil {
				return pg
			}
			pg.Close()
		}
		if attempt == 10 || ctx.Err() != nil {
			slog.Error("database_unavailable", "error", err)
			os.Exit(1)
		}
		slog.Warn("database_connect_retry", "attempt", attempt, "error", err)
		time.Sleep(2 * time.Second)
	}
}

func serve(cfg config.Settings) {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	pg := connectWithRetry(ctx, cfg)
	defer pg.Close()

	// Dev/CI convenience only: in staging/prod the schema is applied by the
	// dedicated migration Job (templates/migration-job.yaml) before rollout.
	if cfg.Environment == "development" {
		if err := pg.Migrate(ctx); err != nil {
			slog.Error("migrate_failed", "error", err)
			os.Exit(1)
		}
	}

	svc := service.New(pg)
	m := metrics.New(pg, time.Duration(cfg.OfflineThresholdSeconds)*time.Second)
	mq := mqttc.New(cfg, svc, m)
	mq.Start()
	defer mq.Stop()

	srv := &http.Server{
		Addr:              cfg.HTTPAddr,
		Handler:           api.New(cfg, svc, mq, m),
		ReadHeaderTimeout: 10 * time.Second,
	}
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
	}()

	slog.Info("controller_started", "addr", cfg.HTTPAddr)
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		slog.Error("http_server_failed", "error", err)
		os.Exit(1)
	}
	slog.Info("controller_stopped")
}

func healthcheck(cfg config.Settings) {
	addr := cfg.HTTPAddr
	if strings.HasPrefix(addr, ":") {
		addr = "localhost" + addr
	}
	client := http.Client{Timeout: 3 * time.Second}
	resp, err := client.Get("http://" + addr + "/healthz")
	if err != nil || resp.StatusCode != http.StatusOK {
		os.Exit(1)
	}
}
