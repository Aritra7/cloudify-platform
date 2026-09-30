package worker

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Aritra7/cloudify-platform/internal/migrations"
)

type recordingRunner struct {
	mu       sync.Mutex
	commands []Command
}

func (runner *recordingRunner) Run(_ context.Context, command Command, output OutputFunc) error {
	runner.mu.Lock()
	runner.commands = append(runner.commands, command)
	runner.mu.Unlock()
	return output(StreamStdout, "completed "+command.Name)
}

type recordingSink struct {
	mu     sync.Mutex
	events []Event
}

func (sink *recordingSink) Emit(_ context.Context, event Event) error {
	sink.mu.Lock()
	defer sink.mu.Unlock()
	sink.events = append(sink.events, event)
	return nil
}

func TestPythonWorkerBuildsSafeCommandsAndCleansWorkspace(t *testing.T) {
	t.Parallel()

	engineRoot := t.TempDir()
	if err := os.WriteFile(filepath.Join(engineRoot, "migration_orchestrator.py"), []byte("# test"), 0o600); err != nil {
		t.Fatalf("write migration engine: %v", err)
	}
	workRoot := t.TempDir()
	runner := &recordingRunner{}
	sink := &recordingSink{}
	worker, err := NewPythonWorker(PythonConfig{
		EngineRoot:   engineRoot,
		WorkRoot:     workRoot,
		PythonBinary: "python-test",
		GitBinary:    "git-test",
	}, runner, sink)
	if err != nil {
		t.Fatalf("create worker: %v", err)
	}

	migration := testMigration()
	if err := worker.Run(context.Background(), migration); err != nil {
		t.Fatalf("run worker: %v", err)
	}
	if len(runner.commands) != 5 {
		t.Fatalf("commands = %d, want 5", len(runner.commands))
	}
	fetch := runner.commands[2]
	if fetch.Name != "git-test" || fetch.Args[len(fetch.Args)-1] != migration.Source.Revision {
		t.Fatalf("fetch command = %#v", fetch)
	}
	python := runner.commands[4]
	if python.Name != "python-test" || python.Dir != engineRoot {
		t.Fatalf("python command = %#v", python)
	}
	if strings.Join(python.Args, " ") != "migration_orchestrator.py migrate --source-path "+runner.commands[0].Args[1]+" --gcp-project example-project --region us-central1 --mode automated" {
		t.Fatalf("python arguments = %#v", python.Args)
	}
	if len(sink.events) != 5 {
		t.Fatalf("events = %d, want 5", len(sink.events))
	}

	entries, err := os.ReadDir(workRoot)
	if err != nil {
		t.Fatalf("read work root: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("workspaces were not cleaned up: %v", entries)
	}
}

func TestPythonWorkerRejectsUnsafeSourceBeforeExecution(t *testing.T) {
	t.Parallel()

	engineRoot := t.TempDir()
	if err := os.WriteFile(filepath.Join(engineRoot, "migration_orchestrator.py"), []byte("# test"), 0o600); err != nil {
		t.Fatalf("write migration engine: %v", err)
	}

	tests := []struct {
		name       string
		repository string
		revision   string
	}{
		{name: "embedded credentials", repository: "https://token@example.com/application", revision: "main"},
		{name: "insecure transport", repository: "http://example.com/application", revision: "main"},
		{name: "option injection", repository: "https://example.com/application", revision: "--upload-pack=bad"},
		{name: "revision traversal", repository: "https://example.com/application", revision: "main..evil"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			runner := &recordingRunner{}
			worker, err := NewPythonWorker(PythonConfig{EngineRoot: engineRoot}, runner, &recordingSink{})
			if err != nil {
				t.Fatalf("create worker: %v", err)
			}
			migration := testMigration()
			migration.Source.RepositoryURL = test.repository
			migration.Source.Revision = test.revision
			if err := worker.Run(context.Background(), migration); err == nil {
				t.Fatal("Run succeeded for unsafe source")
			}
			if len(runner.commands) != 0 {
				t.Fatalf("executed %d commands for unsafe source", len(runner.commands))
			}
		})
	}
}

func TestOSCommandRunnerCapturesOutput(t *testing.T) {
	t.Parallel()

	var lines []string
	err := (OSCommandRunner{}).Run(
		context.Background(),
		Command{Name: "printf", Args: []string{"first\\nsecond\\n"}},
		func(_ Stream, line string) error {
			lines = append(lines, line)
			return nil
		},
	)
	if err != nil {
		t.Fatalf("run printf: %v", err)
	}
	if strings.Join(lines, ",") != "first,second" {
		t.Fatalf("lines = %v", lines)
	}
}

func TestOSCommandRunnerCancelsProcessGroup(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	started := time.Now()
	err := (OSCommandRunner{TerminationGracePeriod: 20 * time.Millisecond}).Run(
		ctx,
		Command{Name: "sleep", Args: []string{"30"}},
		nil,
	)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error = %v, want context deadline", err)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("cancellation took %s", elapsed)
	}
}

func testMigration() migrations.Migration {
	return migrations.Migration{
		ID: "migration-1",
		Source: migrations.Source{
			RepositoryURL: "https://github.com/example/application",
			Revision:      "main",
		},
		Destination: migrations.Destination{
			Provider:  "gcp",
			ProjectID: "example-project",
			Region:    "us-central1",
			Runtime:   "cloud-run",
		},
	}
}
