package worker

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/Aritra7/cloudify-platform/internal/migrations"
)

var safeRevision = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/-]{0,127}$`)

// Event is one structured line emitted while preparing or running a migration.
type Event struct {
	MigrationID string
	Phase       string
	Stream      Stream
	Message     string
	CreatedAt   time.Time
}

// EventSink receives worker output. A durable implementation will back the
// migration event-stream API; SlogSink is the initial operational adapter.
type EventSink interface {
	Emit(context.Context, Event) error
}

// SlogSink writes structured worker output through the process logger.
type SlogSink struct{}

func (SlogSink) Emit(_ context.Context, event Event) error {
	slog.Info(
		"migration worker output",
		"migration_id", event.MigrationID,
		"phase", event.Phase,
		"stream", event.Stream,
		"message", event.Message,
	)
	return nil
}

// PythonConfig configures the existing Cloudify migration engine adapter.
type PythonConfig struct {
	EngineRoot                string
	WorkRoot                  string
	PythonBinary              string
	GitBinary                 string
	AllowInsecureRepositories bool
}

// PythonWorker checks out one immutable source revision and runs Cloudify.
type PythonWorker struct {
	config PythonConfig
	runner CommandRunner
	sink   EventSink
	now    func() time.Time
}

// NewPythonWorker validates configuration before accepting work.
func NewPythonWorker(config PythonConfig, runner CommandRunner, sink EventSink) (*PythonWorker, error) {
	if runner == nil || sink == nil {
		return nil, errors.New("python worker requires a command runner and event sink")
	}
	if config.PythonBinary == "" {
		config.PythonBinary = "python3"
	}
	if config.GitBinary == "" {
		config.GitBinary = "git"
	}
	if config.WorkRoot == "" {
		config.WorkRoot = os.TempDir()
	}
	engineRoot, err := filepath.Abs(config.EngineRoot)
	if err != nil {
		return nil, fmt.Errorf("resolve engine root: %w", err)
	}
	if _, err := os.Stat(filepath.Join(engineRoot, "migration_orchestrator.py")); err != nil {
		return nil, fmt.Errorf("find migration engine: %w", err)
	}
	config.EngineRoot = engineRoot

	return &PythonWorker{
		config: config,
		runner: runner,
		sink:   sink,
		now:    func() time.Time { return time.Now().UTC() },
	}, nil
}

// Run executes the migration in an isolated temporary workspace.
func (worker *PythonWorker) Run(ctx context.Context, migration migrations.Migration) error {
	if err := worker.validateMigration(migration); err != nil {
		return err
	}

	workspace, err := os.MkdirTemp(worker.config.WorkRoot, "cloudify-migration-*")
	if err != nil {
		return fmt.Errorf("create migration workspace: %w", err)
	}
	defer func() {
		if err := os.RemoveAll(workspace); err != nil {
			slog.Error("remove migration workspace", "migration_id", migration.ID, "error", err)
		}
	}()

	checkout := filepath.Join(workspace, "source")
	commands := []struct {
		phase   string
		command Command
	}{
		{
			phase: "checkout",
			command: Command{
				Name: worker.config.GitBinary,
				Args: []string{"init", checkout},
				Env:  append(os.Environ(), "GIT_TERMINAL_PROMPT=0"),
			},
		},
		{
			phase: "checkout",
			command: Command{
				Name: worker.config.GitBinary,
				Args: []string{"-C", checkout, "remote", "add", "origin", migration.Source.RepositoryURL},
				Env:  append(os.Environ(), "GIT_TERMINAL_PROMPT=0"),
			},
		},
		{
			phase: "checkout",
			command: Command{
				Name: worker.config.GitBinary,
				Args: []string{"-C", checkout, "fetch", "--depth=1", "origin", migration.Source.Revision},
				Env:  append(os.Environ(), "GIT_TERMINAL_PROMPT=0"),
			},
		},
		{
			phase: "checkout",
			command: Command{
				Name: worker.config.GitBinary,
				Args: []string{"-C", checkout, "checkout", "--detach", "FETCH_HEAD"},
				Env:  append(os.Environ(), "GIT_TERMINAL_PROMPT=0"),
			},
		},
		{
			phase: "migration",
			command: Command{
				Name: worker.config.PythonBinary,
				Args: []string{
					"migration_orchestrator.py",
					"migrate",
					"--source-path", checkout,
					"--gcp-project", migration.Destination.ProjectID,
					"--region", migration.Destination.Region,
					"--mode", "automated",
				},
				Dir: worker.config.EngineRoot,
				Env: os.Environ(),
			},
		},
	}

	for _, step := range commands {
		phase := step.phase
		if err := worker.runner.Run(ctx, step.command, func(stream Stream, line string) error {
			return worker.sink.Emit(ctx, Event{
				MigrationID: migration.ID,
				Phase:       phase,
				Stream:      stream,
				Message:     line,
				CreatedAt:   worker.now(),
			})
		}); err != nil {
			return fmt.Errorf("%s phase: %w", phase, err)
		}
	}
	return nil
}

func (worker *PythonWorker) validateMigration(migration migrations.Migration) error {
	repositoryURL, err := url.Parse(migration.Source.RepositoryURL)
	if err != nil || repositoryURL.Host == "" {
		return errors.New("repository URL must be absolute")
	}
	if repositoryURL.User != nil {
		return errors.New("repository URL must not contain credentials")
	}
	if repositoryURL.Scheme != "https" && !(worker.config.AllowInsecureRepositories && repositoryURL.Scheme == "http") {
		return errors.New("repository URL must use HTTPS")
	}
	if !safeRevision.MatchString(migration.Source.Revision) || strings.Contains(migration.Source.Revision, "..") {
		return errors.New("repository revision contains unsupported characters")
	}
	if strings.TrimSpace(migration.Destination.ProjectID) == "" || strings.TrimSpace(migration.Destination.Region) == "" {
		return errors.New("GCP project and region are required")
	}
	return nil
}
