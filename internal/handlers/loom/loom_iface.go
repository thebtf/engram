package loom

import (
	"context"

	loom "github.com/thebtf/aimux/loom"
)

// loomEngine is a narrow interface over the real *loom.LoomEngine method set.
// It exists so tests can inject a fake implementation without spinning up a
// real SQLite database.
type loomEngine interface {
	RegisterWorker(wt loom.WorkerType, w loom.Worker)
	RecoverCrashed() (int, error)
	Submit(ctx context.Context, req loom.TaskRequest) (string, error)
	Get(taskID string) (*loom.Task, error)
	List(projectID string, statuses ...loom.TaskStatus) ([]*loom.Task, error)
	Cancel(taskID string) error
	CancelAllForProject(projectID string) (int, error)
	Events() *loom.EventBus
}

// compile-time assertion: *loom.LoomEngine must satisfy loomEngine.
var _ loomEngine = (*loom.LoomEngine)(nil)
