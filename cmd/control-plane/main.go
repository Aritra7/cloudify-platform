package main

import (
	"context"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"syscall"
	"time"

	"github.com/Aritra7/cloudify-platform/internal/api"
	"github.com/Aritra7/cloudify-platform/internal/auth"
	"github.com/Aritra7/cloudify-platform/internal/events"
	"github.com/Aritra7/cloudify-platform/internal/executor"
	"github.com/Aritra7/cloudify-platform/internal/iac"
	"github.com/Aritra7/cloudify-platform/internal/migrations"
	"github.com/Aritra7/cloudify-platform/internal/observability"
	"github.com/Aritra7/cloudify-platform/internal/plans"
	"github.com/Aritra7/cloudify-platform/internal/resources"
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
	planService := plans.NewService(stores.plans, plans.DefaultPolicy())
	resourceService := resources.NewService(stores.resources)
	metrics := &observability.Metrics{}
	dispatcherErrors, err := startDispatcher(ctx, stores.migrations, stores.events, metrics)
	if err != nil {
		return err
	}
	planDispatcherErrors, err := startPlanDispatcher(ctx, stores.plans)
	if err != nil {
		return err
	}
	projectorErrors := startResourceProjector(ctx, stores.plans, resourceService)

	apiServer := api.NewServerWithResources(migrationService, stores.events, planService, resourceService, metrics)
	if authenticationConfiguration := os.Getenv("CLOUDIFY_AUTH_TOKENS_JSON"); authenticationConfiguration != "" {
		authenticator, err := auth.NewBearerAuthenticator(authenticationConfiguration)
		if err != nil {
			return fmt.Errorf("configure API authentication: %w", err)
		}
		apiServer = api.NewAuthenticatedServerWithResources(migrationService, stores.events, planService, resourceService, metrics, authenticator)
	}
	server := &http.Server{
		Addr:              address(),
		Handler:           apiServer.Handler(),
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
	case err := <-planDispatcherErrors:
		if !errors.Is(err, context.Canceled) {
			runErr = fmt.Errorf("Terraform plan dispatcher stopped: %w", err)
		}
		stop()
	case err := <-projectorErrors:
		if !errors.Is(err, context.Canceled) {
			runErr = fmt.Errorf("managed-resource projector stopped: %w", err)
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

func startResourceProjector(ctx context.Context, planStore plans.Store, resourceService *resources.Service) <-chan error {
	projector := &resources.Projector{
		Plans: planStore, Resources: resourceService, PollInterval: 2 * time.Second, BatchSize: 200,
	}
	errors := make(chan error, 1)
	go func() { errors <- projector.Run(ctx) }()
	return errors
}

func startPlanDispatcher(ctx context.Context, store plans.Store) (<-chan error, error) {
	enabled, err := strconv.ParseBool(environment("CLOUDIFY_TERRAFORM_ENABLED", "false"))
	if err != nil {
		return nil, fmt.Errorf("parse CLOUDIFY_TERRAFORM_ENABLED: %w", err)
	}
	if !enabled {
		return nil, nil
	}
	if os.Getenv("CLOUDIFY_AUTH_TOKENS_JSON") == "" {
		return nil, errors.New("CLOUDIFY_AUTH_TOKENS_JSON is required when Terraform is enabled")
	}
	stateBucket := os.Getenv("CLOUDIFY_TERRAFORM_STATE_BUCKET")
	if stateBucket == "" {
		return nil, errors.New("CLOUDIFY_TERRAFORM_STATE_BUCKET is required when Terraform is enabled")
	}
	binaryPath, err := exec.LookPath(environment("CLOUDIFY_TERRAFORM_BINARY", "terraform"))
	if err != nil {
		return nil, fmt.Errorf("locate Terraform binary: %w", err)
	}
	workRoot, err := filepath.Abs(environment("CLOUDIFY_TERRAFORM_WORK_ROOT", filepath.Join(os.TempDir(), "cloudify-terraform-work")))
	if err != nil {
		return nil, fmt.Errorf("resolve Terraform work root: %w", err)
	}
	artifactRoot, err := filepath.Abs(environment("CLOUDIFY_TERRAFORM_ARTIFACT_ROOT", filepath.Join(os.TempDir(), "cloudify-terraform-artifacts")))
	if err != nil {
		return nil, fmt.Errorf("resolve Terraform artifact root: %w", err)
	}
	encryptionKey, err := base64.StdEncoding.DecodeString(os.Getenv("CLOUDIFY_TERRAFORM_ARTIFACT_KEY"))
	if err != nil || len(encryptionKey) != 32 {
		return nil, errors.New("CLOUDIFY_TERRAFORM_ARTIFACT_KEY must be a base64-encoded 32-byte key")
	}
	for _, directory := range []string{workRoot, artifactRoot} {
		if err := os.MkdirAll(directory, 0o700); err != nil {
			return nil, fmt.Errorf("create Terraform directory: %w", err)
		}
	}
	hostname, err := os.Hostname()
	if err != nil {
		hostname = "unknown-host"
	}
	artifactStore := iac.FileArtifactStore{Root: artifactRoot, EncryptionKey: encryptionKey}
	workspaceLocker := &iac.MemoryWorkspaceLocker{}
	planner := &iac.Planner{
		Artifacts: artifactStore, Locker: workspaceLocker,
		BinaryPath: binaryPath, StateBucket: stateBucket, StatePrefix: "cloudify/migrations",
	}
	planDispatcher := &plans.Dispatcher{
		Store: store, Planner: planner, WorkerID: fmt.Sprintf("terraform-%s-%d", hostname, os.Getpid()),
		WorkRoot: workRoot, LeaseDuration: 10 * time.Minute, HeartbeatInterval: 30 * time.Second,
		PollInterval: time.Second,
	}
	applier := &iac.Applier{
		Artifacts: artifactStore, Locker: workspaceLocker, BinaryPath: binaryPath,
		StateBucket: stateBucket, StatePrefix: "cloudify/migrations",
	}
	applyDispatcher := &plans.ApplyDispatcher{
		Store: store, Applier: applier, WorkerID: fmt.Sprintf("terraform-apply-%s-%d", hostname, os.Getpid()),
		WorkRoot: workRoot, LeaseDuration: 30 * time.Minute, HeartbeatInterval: 30 * time.Second,
		PollInterval: time.Second,
	}
	errors := make(chan error, 2)
	go func() { errors <- planDispatcher.Run(ctx) }()
	go func() { errors <- applyDispatcher.Run(ctx) }()
	slog.Info("Terraform dispatchers enabled", "plan_worker_id", planDispatcher.WorkerID, "apply_worker_id", applyDispatcher.WorkerID)
	return errors, nil
}

func startDispatcher(ctx context.Context, migrationStore migrations.Store, eventStore events.Store, metrics *observability.Metrics) (<-chan error, error) {
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
		Metrics:           metrics,
	}
	errors := make(chan error, 1)
	go func() { errors <- dispatcher.Run(ctx) }()
	slog.Info("migration dispatcher enabled", "worker_id", dispatcher.WorkerID)
	return errors, nil
}

type stores struct {
	migrations migrations.Store
	events     events.Store
	plans      plans.Store
	resources  resources.Store
}

func platformStores(ctx context.Context) (stores, func(), error) {
	databaseURL := os.Getenv("CLOUDIFY_DATABASE_URL")
	if databaseURL == "" {
		slog.Warn("CLOUDIFY_DATABASE_URL is not set; migration state is not durable")
		return stores{
			migrations: migrations.NewMemoryStore(),
			events:     events.NewMemoryStore(),
			plans:      plans.NewMemoryStore(),
			resources:  resources.NewMemoryStore(),
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
		plans:      plans.NewPostgresStore(database),
		resources:  resources.NewPostgresStore(database),
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
