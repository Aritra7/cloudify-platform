package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/Aritra7/cloudify-platform/internal/api"
	"github.com/Aritra7/cloudify-platform/internal/events"
	"github.com/Aritra7/cloudify-platform/internal/executor"
	"github.com/Aritra7/cloudify-platform/internal/migrations"
	"github.com/Aritra7/cloudify-platform/internal/worker"
	_ "github.com/jackc/pgx/v5/stdlib"
)

const (
	defaultAddress  = ":8080"
	shutdownTimeout = 10 * time.Second
)

func main() {
	if err := run(); err != nil {
		slog.Error("control plane stopped", "error", err)
		os.Exit(1)
	}
}

func run() error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	stores, closeStores, err := platformStores(ctx)
	if err != nil {
		return err
	}
	defer closeStores()
	migrationService := migrations.NewService(stores.migrations)
	dispatcherErrors, err := startDispatcher(ctx, stores.migrations, stores.events)
	if err != nil {
		return err
	}

	server := &http.Server{
		Addr:              address(),
		Handler:           api.NewServer(migrationService, stores.events).Handler(),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	serveErrors := make(chan error, 1)
	go func() {
		slog.Info("control plane listening", "address", server.Addr)
		serveErrors <- server.ListenAndServe()
	}()

	var runErr error
	select {
	case err := <-serveErrors:
		if !errors.Is(err, http.ErrServerClosed) {
			runErr = err
		}
		stop()
	case err := <-dispatcherErrors:
		if !errors.Is(err, context.Canceled) {
			runErr = fmt.Errorf("migration dispatcher stopped: %w", err)
		}
		stop()
	case <-ctx.Done():
		slog.Info("shutdown requested")
	}

	shutdownContext, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	shutdownErr := server.Shutdown(shutdownContext)
	return errors.Join(runErr, shutdownErr)
}

func startDispatcher(ctx context.Context, migrationStore migrations.Store, eventStore events.Store) (<-chan error, error) {
	enabled, err := strconv.ParseBool(environment("CLOUDIFY_WORKER_ENABLED", "false"))
	if err != nil {
		return nil, fmt.Errorf("parse CLOUDIFY_WORKER_ENABLED: %w", err)
	}
	if !enabled {
		return nil, nil
	}

	pythonWorker, err := worker.NewPythonWorker(worker.PythonConfig{
		EngineRoot:   environment("CLOUDIFY_ENGINE_ROOT", "."),
		WorkRoot:     os.Getenv("CLOUDIFY_WORK_ROOT"),
		PythonBinary: environment("CLOUDIFY_PYTHON_BINARY", "python3"),
		GitBinary:    environment("CLOUDIFY_GIT_BINARY", "git"),
	}, worker.OSCommandRunner{TerminationGracePeriod: 10 * time.Second}, worker.RedactingSink{
		Next: events.WorkerSink{Store: eventStore},
	})
	if err != nil {
		return nil, fmt.Errorf("configure Python worker: %w", err)
	}

	hostname, err := os.Hostname()
	if err != nil {
		hostname = "unknown-host"
	}
	dispatcher := &executor.Dispatcher{
		Store:             migrationStore,
		Worker:            pythonWorker,
		WorkerID:          fmt.Sprintf("%s-%d", hostname, os.Getpid()),
		LeaseDuration:     2 * time.Minute,
		HeartbeatInterval: 30 * time.Second,
		CancellationPoll:  time.Second,
		PollInterval:      time.Second,
	}
	errors := make(chan error, 1)
	go func() { errors <- dispatcher.Run(ctx) }()
	slog.Info("migration dispatcher enabled", "worker_id", dispatcher.WorkerID)
	return errors, nil
}

type stores struct {
	migrations migrations.Store
	events     events.Store
}

func platformStores(ctx context.Context) (stores, func(), error) {
	databaseURL := os.Getenv("CLOUDIFY_DATABASE_URL")
	if databaseURL == "" {
		slog.Warn("CLOUDIFY_DATABASE_URL is not set; migration state is not durable")
		return stores{
			migrations: migrations.NewMemoryStore(),
			events:     events.NewMemoryStore(),
		}, func() {}, nil
	}

	database, err := sql.Open("pgx", databaseURL)
	if err != nil {
		return stores{}, func() {}, err
	}
	pingContext, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := database.PingContext(pingContext); err != nil {
		_ = database.Close()
		return stores{}, func() {}, err
	}

	return stores{
		migrations: migrations.NewPostgresStore(database),
		events:     events.NewPostgresStore(database),
	}, func() { _ = database.Close() }, nil
}

func address() string {
	return environment("CLOUDIFY_HTTP_ADDRESS", defaultAddress)
}

func environment(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}
