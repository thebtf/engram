//go:build windows

// Package control — Windows stub for the legacy product control socket.
//
// The legacy socket has no named-pipe implementation on Windows. Current plugin
// auto-upgrade does not use this channel: ensure-binary.js installs atomically,
// and normal Engram launch delegates live-daemon replacement to muxcore
// RestartWithSuccessor.
//
// Start intentionally remains non-fatal so daemon startup is portable.
package control

// Start is a no-op on Windows. A WARN identifies the unavailable legacy command.
func (l *Listener) Start() error {
	l.logger.Warn("control socket: legacy graceful-restart unavailable on Windows; normal muxcore update flow unaffected")
	return nil
}
