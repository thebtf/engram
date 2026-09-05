package legacyrelay

import (
	"context"
	"errors"

	muxcore "github.com/thebtf/mcp-mux/muxcore"
)

// SessionTrackingHandler upgrades the existing MCP dispatcher with muxcore
// SessionMeta observation while preserving its request, notification,
// lifecycle, and notifier behavior. Observation failure disables only relay
// bootstrap; it never breaks the existing MCP path.
type SessionTrackingHandler struct {
	next     muxcore.SessionHandler
	children *ChildRegistry
}

func NewSessionTrackingHandler(next muxcore.SessionHandler, children *ChildRegistry) (*SessionTrackingHandler, error) {
	if next == nil || children == nil {
		return nil, errors.New("legacy relay session tracking dependencies are missing")
	}
	return &SessionTrackingHandler{next: next, children: children}, nil
}

func (h *SessionTrackingHandler) HandleRequest(ctx context.Context, project muxcore.ProjectContext, request []byte) ([]byte, error) {
	return h.next.HandleRequest(ctx, project, request)
}

func (h *SessionTrackingHandler) HandleRequestWithSessionMeta(ctx context.Context, project muxcore.ProjectContext, meta muxcore.SessionMeta, request []byte) ([]byte, error) {
	h.observe(project, meta)
	if next, ok := h.next.(muxcore.SessionHandlerWithSessionMeta); ok {
		return next.HandleRequestWithSessionMeta(ctx, project, meta, request)
	}
	return h.next.HandleRequest(ctx, project, request)
}

func (h *SessionTrackingHandler) HandleNotification(ctx context.Context, project muxcore.ProjectContext, notification []byte) {
	if next, ok := h.next.(muxcore.NotificationHandler); ok {
		next.HandleNotification(ctx, project, notification)
	}
}

func (h *SessionTrackingHandler) HandleNotificationWithSessionMeta(ctx context.Context, project muxcore.ProjectContext, meta muxcore.SessionMeta, notification []byte) {
	h.observe(project, meta)
	if next, ok := h.next.(muxcore.NotificationHandlerWithSessionMeta); ok {
		next.HandleNotificationWithSessionMeta(ctx, project, meta, notification)
		return
	}
	h.HandleNotification(ctx, project, notification)
}

func (h *SessionTrackingHandler) OnProjectConnect(project muxcore.ProjectContext) {
	if next, ok := h.next.(muxcore.ProjectLifecycle); ok {
		next.OnProjectConnect(project)
	}
}

func (h *SessionTrackingHandler) OnProjectDisconnect(projectID string) {
	// Muxcore exposes only the diagnostic project ID here, not the disconnecting
	// peer process. Multiple live OMP sessions may share that ID, so deleting all
	// records would make one disconnect invalidate its siblings. The registry
	// prunes each process incarnation on observation/bootstrap instead.
	if next, ok := h.next.(muxcore.ProjectLifecycle); ok {
		next.OnProjectDisconnect(projectID)
	}
}

func (h *SessionTrackingHandler) SetNotifier(notifier muxcore.Notifier) {
	if next, ok := h.next.(muxcore.NotifierAware); ok {
		next.SetNotifier(notifier)
	}
}

func (h *SessionTrackingHandler) observe(project muxcore.ProjectContext, meta muxcore.SessionMeta) {
	if meta.Conn.PeerPid <= 0 {
		return
	}
	_, _ = h.children.Observe(meta.Conn.PeerPid, project.ID, project.Env)
}

var (
	_ muxcore.SessionHandler                     = (*SessionTrackingHandler)(nil)
	_ muxcore.SessionHandlerWithSessionMeta      = (*SessionTrackingHandler)(nil)
	_ muxcore.NotificationHandler                = (*SessionTrackingHandler)(nil)
	_ muxcore.NotificationHandlerWithSessionMeta = (*SessionTrackingHandler)(nil)
	_ muxcore.ProjectLifecycle                   = (*SessionTrackingHandler)(nil)
	_ muxcore.NotifierAware                      = (*SessionTrackingHandler)(nil)
)
