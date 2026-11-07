package projectidentity

import (
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Microsoft/go-winio"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

// A fixture-local include makes the real Git executable wait on a private
// named pipe. No shim, replacement Git executable or shared process is killed.
func TestNativeGitContextCancelsActualGitProcess(t *testing.T) {
	root := t.TempDir()
	gitLogical(t, root, "init", "--quiet", "--template=")
	config := filepath.Join(root, ".git", "config")
	original, err := os.ReadFile(config)
	require.NoError(t, err)
	pipe := `\\.\pipe\engram-native-git-cancel-` + uuid.NewString()
	listener, err := winio.ListenPipe(pipe, nil)
	require.NoError(t, err)
	defer listener.Close()
	gitLogical(t, root, "config", "--local", "--add", "include.path", pipe)
	defer func() { require.NoError(t, os.WriteFile(config, original, 0600)) }()
	accepted := make(chan net.Conn, 1)
	go func() {
		connection, err := listener.Accept()
		if err == nil {
			accepted <- connection
		}
	}()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	result := make(chan error, 1)
	go func() { _, _, err := NativeGitContext(ctx, root); result <- err }()
	var connection net.Conn
	select {
	case connection = <-accepted:
	case err := <-result:
		t.Fatalf("Git did not wait on fixture pipe: %v", err)
	case <-time.After(3 * time.Second):
		t.Fatal("real Git did not open the fixture pipe")
	}
	defer connection.Close()
	started := time.Now()
	cancel()
	select {
	case err := <-result:
		require.ErrorIs(t, err, context.Canceled)
	case <-time.After(500 * time.Millisecond):
		t.Fatal("request-owned Git process did not terminate after cancellation")
	}
	require.NoError(t, connection.SetReadDeadline(time.Now().Add(500*time.Millisecond)))
	var buffer [1]byte
	_, err = connection.Read(buffer[:])
	require.ErrorIs(t, err, io.EOF, "actual Git pipe handle survived cancellation")
	require.NoError(t, os.WriteFile(config, original, 0600))
	_, locator, err := NativeGitContext(t.Context(), root)
	require.NoError(t, err)
	require.NotEmpty(t, locator)
	t.Log(fmt.Sprintf("NATIVE_GIT_CANCEL_PASS elapsed_ms=%.3f actual_pipe_closed=true next=ok", float64(time.Since(started).Microseconds())/1000))
}
