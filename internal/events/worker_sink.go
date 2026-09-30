package events

import (
	"context"

	"github.com/Aritra7/cloudify-platform/internal/worker"
)

// WorkerSink persists structured output emitted by the Python worker.
type WorkerSink struct {
	Store Store
}

func (sink WorkerSink) Emit(ctx context.Context, output worker.Event) error {
	_, err := sink.Store.Append(ctx, Event{
		MigrationID: output.MigrationID,
		Kind:        "worker_output",
		Phase:       output.Phase,
		Stream:      string(output.Stream),
		Message:     output.Message,
		CreatedAt:   output.CreatedAt,
	})
	return err
}
