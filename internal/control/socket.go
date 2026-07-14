// Package control implements the Engram daemon's legacy product control socket.
// It provides a simple line-delimited protocol for explicit operator commands,
// independent of muxcore. The current plugin auto-upgrade path does not use it.
//
// # Protocol
//
// Client writes a single command line terminated by '\n'. Server responds with
// a single response line terminated by '\n'. Connection is then closed.
//
//	graceful-restart\n  →  ACK\n
//	<unknown>\n         →  ERR unknown command\n
//
// # Socket path
//
// Unix/macOS:  ${ENGRAM_DATA_DIR}/run/engram.sock
// Windows:     listener is not started; the muxcore update flow does not depend on it.
//
// # PID file
//
// A PID file is written at ${ENGRAM_DATA_DIR}/run/engram.pid so that external
// discovery tools can read the socket path deterministically. The PID file is
// removed on Listener.Close.
//
// Design reference: tasks.md T056, Phase 8 pragmatic scope reduction. The
// socket remains only for legacy explicit commands.
package control

import (
	"bufio"
	"fmt"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// CommandHandler is the callback invoked when a valid command arrives on the
// control socket. The handler is called synchronously inside the per-connection
// goroutine. Slow handlers block the connection but not other connections.
//
// The returned string is written to the client as-is (without a trailing newline
// — the listener adds '\n' before writing). An empty string causes the listener
// to write an empty line, which is allowed but unusual.
type CommandHandler func(command string) string

// Listener wraps the net.Listener and owns the socket + PID file lifecycle.
// Call Start to begin accepting connections; call Close to tear down cleanly.
type Listener struct {
	socketPath string
	pidPath    string
	handler    CommandHandler
	logger     *slog.Logger
	ln         net.Listener
}

// NewListener creates a Listener that will accept on socketPath.
// socketPath and pidPath must be absolute paths; the parent directories must
// already exist (or will be created by Start).
func NewListener(socketPath, pidPath string, handler CommandHandler, logger *slog.Logger) *Listener {
	return &Listener{
		socketPath: socketPath,
		pidPath:    pidPath,
		handler:    handler,
		logger:     logger,
	}
}

// Close shuts down the underlying net.Listener and removes the socket file and
// PID file. Idempotent — safe to call multiple times.
func (l *Listener) Close() {
	if l.ln != nil {
		_ = l.ln.Close()
	}
	_ = os.Remove(l.socketPath)
	_ = os.Remove(l.pidPath)
}

// SocketDir returns the run-directory path under dataDir.
func SocketDir(dataDir string) string {
	return filepath.Join(dataDir, "run")
}

// SocketPath returns the canonical Unix socket path for the given dataDir.
func SocketPath(dataDir string) string {
	return filepath.Join(dataDir, "run", "engram.sock")
}

// PIDPath returns the canonical PID file path for the given dataDir.
func PIDPath(dataDir string) string {
	return filepath.Join(dataDir, "run", "engram.pid")
}

// writePID writes the current process PID to path with 0644 permissions.
func writePID(path string) error {
	pid := os.Getpid()
	return os.WriteFile(path, []byte(strconv.Itoa(pid)+"\n"), 0o644)
}

// handleConn processes a single client connection: read one line, call handler,
// write response, close. Errors are logged only — one bad connection does not
// stop the listener.
func (l *Listener) handleConn(conn net.Conn) {
	defer conn.Close()

	scanner := bufio.NewScanner(conn)
	if !scanner.Scan() {
		// EOF or read error before a full line — ignore silently.
		return
	}
	cmd := strings.TrimRight(scanner.Text(), "\r")

	var response string
	if l.handler != nil {
		response = l.handler(cmd)
	} else {
		response = fmt.Sprintf("ERR no handler registered")
	}

	if _, err := fmt.Fprintf(conn, "%s\n", response); err != nil {
		l.logger.Warn("control socket: write response failed",
			"command", cmd,
			"error", err,
		)
	}
}

// serve accepts connections in a loop until the listener is closed. Runs in its
// own goroutine; returns when ln.Accept returns an error (including on Close).
func (l *Listener) serve() {
	for {
		conn, err := l.ln.Accept()
		if err != nil {
			// Accept returns net.ErrClosed when Close() is called — that is
			// the normal shutdown path, not an error worth logging.
			if isClosedErr(err) {
				return
			}
			l.logger.Warn("control socket: accept error", "error", err)
			return
		}
		go l.handleConn(conn)
	}
}

// isClosedErr reports whether err is the "use of closed network connection"
// error returned by net.Listener.Accept after Close.
func isClosedErr(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(err.Error(), "use of closed network connection")
}
