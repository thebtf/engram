package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/thebtf/mcp-mux/muxcore/serverid"
)

func TestTaskContextClosedInputAndMissingInstallation(t *testing.T) {
	root := t.TempDir()
	t.Setenv("ENGRAM_DATA_DIR", filepath.Join(root, "absent"))
	t.Setenv("ENGRAM_CLIENT_INSTANCE_ID", "")
	for _, input := range []string{
		`{"schema":1,"schema":1,"host_session_id":"host","query":"Alpha","deadline_unix_ms":1,"max_bytes":4096}`,
		`{"schema":1,"host_session_id":"host","query":"Alpha","deadline_unix_ms":1,"max_bytes":4096,"locator":"file:///borrowed"}`,
		strings.Repeat("x", 21000),
	} {
		var output bytes.Buffer
		require.NoError(t, runTaskContext(context.Background(), time.Now(), strings.NewReader(input), &output))
		var response map[string]any
		require.NoError(t, json.Unmarshal(output.Bytes(), &response))
		require.Equal(t, "unavailable", response["status"])
		require.Equal(t, "INVALID_REQUEST", response["reason_code"])
		require.NotContains(t, output.String(), "borrowed")
	}
	input, err := json.Marshal(map[string]any{"schema": 1, "host_session_id": "host", "query": "Alpha", "deadline_unix_ms": time.Now().Add(time.Second).UnixMilli(), "max_bytes": 4096})
	require.NoError(t, err)
	var output bytes.Buffer
	require.NoError(t, runTaskContext(context.Background(), time.Now(), bytes.NewReader(input), &output))
	require.Contains(t, output.String(), `"status":"not_ready"`)
	require.Contains(t, output.String(), `"reason_code":"INSTALLATION_NOT_READY"`)
	_, err = os.Stat(filepath.Join(root, "absent"))
	require.True(t, os.IsNotExist(err), "read-only native path created installation state")
}

func TestTaskContextExpiredDeadlineDoesNoInstallationWork(t *testing.T) {
	input, err := json.Marshal(map[string]any{"schema": 1, "host_session_id": "host", "query": "Alpha", "deadline_unix_ms": time.Now().Add(-time.Second).UnixMilli(), "max_bytes": 1024})
	require.NoError(t, err)
	var output bytes.Buffer
	require.NoError(t, runTaskContext(context.Background(), time.Now(), bytes.NewReader(input), &output))
	require.Contains(t, output.String(), `"status":"deadline_exceeded"`)
	require.LessOrEqual(t, output.Len(), 1024)
}

func TestTaskContextOldDaemonDoesNotReconcile(t *testing.T) {
	root := t.TempDir()
	t.Setenv("TEMP", root)
	t.Setenv("TMP", root)
	t.Setenv("TMPDIR", root)
	instance := "task-old-daemon-fixture"
	t.Setenv("ENGRAM_CLIENT_INSTANCE_ID", instance)
	path := serverid.DaemonControlPath("", muxcoreInstallationNamespace(instance)) + ".marker.json"
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
	executable, err := os.Executable()
	require.NoError(t, err)
	marker, err := json.Marshal(muxcoreDaemonVersionMarker{SchemaVersion: 2, ProductVersion: "v6.46.4", DaemonCompatEpoch: 1, PID: os.Getpid(), DaemonGeneration: "old-fixture", Exe: executable})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(path, marker, 0o600))
	request, err := json.Marshal(map[string]any{"schema": 1, "host_session_id": "host", "query": "Alpha", "deadline_unix_ms": time.Now().Add(500 * time.Millisecond).UnixMilli(), "max_bytes": 4096})
	require.NoError(t, err)
	var output bytes.Buffer
	require.NoError(t, runTaskContext(t.Context(), time.Now(), bytes.NewReader(request), &output))
	require.Contains(t, output.String(), `"status":"not_ready"`)
	require.Contains(t, output.String(), `"reason_code":"DAEMON_NOT_READY"`)
	current, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, marker, current, "one-shot rewrote an old daemon marker")
}

func TestTaskContextParentCancellationClosesOnlyRequestInput(t *testing.T) {
	input, writer := io.Pipe()
	defer writer.Close()
	ctx, cancel := context.WithCancel(t.Context())
	var output bytes.Buffer
	finished := make(chan error, 1)
	go func() { finished <- runTaskContext(ctx, time.Now(), input, &output) }()
	cancel()
	select {
	case err := <-finished:
		require.NoError(t, err)
	case <-time.After(500 * time.Millisecond):
		t.Fatal("cancelled one-shot retained its stdin reader")
	}
	require.Contains(t, output.String(), `"status":"deadline_exceeded"`)
}
