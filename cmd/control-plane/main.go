package main

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/Aritra7/cloudify-platform/internal/api"
	"github.com/Aritra7/cloudify-platform/internal/migrations"
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

	store, closeStore, err := migrationStore(ctx)
	if err != nil {
		return err
	}
	defer closeStore()
	migrationService := migrations.NewService(store)

	server := &http.Server{
		Addr:              address(),
		Handler:           api.NewServer(migrationService).Handler(),
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

	select {
	case err := <-serveErrors:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
		slog.Info("shutdown requested")
	}

	shutdownContext, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	return server.Shutdown(shutdownContext)
}

func migrationStore(ctx context.Context) (migrations.Store, func(), error) {
	databaseURL := os.Getenv("CLOUDIFY_DATABASE_URL")
	if databaseURL == "" {
		slog.Warn("CLOUDIFY_DATABASE_URL is not set; migration state is not durable")
		return migrations.NewMemoryStore(), func() {}, nil
	}

	database, err := sql.Open("pgx", databaseURL)
	if err != nil {
		return nil, func() {}, err
	}
	pingContext, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := database.PingContext(pingContext); err != nil {
		_ = database.Close()
		return nil, func() {}, err
	}

	return migrations.NewPostgresStore(database), func() { _ = database.Close() }, nil
}

func address() string {
	if value := os.Getenv("CLOUDIFY_HTTP_ADDRESS"); value != "" {
		return value
	}
	return defaultAddress
}
