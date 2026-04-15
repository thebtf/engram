package loom

import (
	"time"

	loom "github.com/thebtf/aimux/loom"
)

// taskEventPayload is the JSON body embedded in every
// notifications/loom/task_event push notification.
type taskEventPayload struct {
	TaskID    string    `json:"task_id"`
	ProjectID string    `json:"project_id"`
	Type      string    `json:"type"`
	Status    string    `json:"status"`
	RequestID string    `json:"request_id,omitempty"`
	Timestamp string    `json:"timestamp"`
}

// handleTaskEvent is called synchronously from the loom EventBus dispatch
// goroutine. It MUST return quickly — any slow path must be offloaded.
//
// If the notifier is nil (e.g. unit tests that do not wire a notifier), the
// method returns immediately to avoid a nil-pointer panic.
func (m *Module) handleTaskEvent(ev loom.TaskEvent) {
	if m.notifier == nil {
		return
	}

	payload := taskEventPayload{
		TaskID:    ev.TaskID,
		ProjectID: ev.ProjectID,
		Type:      string(ev.Type),
		Status:    string(ev.Status),
		RequestID: ev.RequestID,
		Timestamp: ev.Timestamp.UTC().Format(time.RFC3339Nano),
	}

	body, err := buildNotificationPayload("notifications/loom/task_event", payload)
	if err != nil {
		// JSON marshalling of a plain struct should never fail; log and drop.
		m.deps.Logger.WarnContext(m.deps.DaemonCtx, "loom: failed to marshal task event notification",
			"task_id", ev.TaskID,
			"error", err,
		)
		return
	}

	// Non-blocking: Notify is defined to return quickly (buffer or drop).
	// We discard the error because a failed notification is not a reason to
	// abort task dispatch.
	_ = m.notifier.Notify(ev.ProjectID, body)
}
