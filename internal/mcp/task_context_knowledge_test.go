package mcp

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/stretchr/testify/require"
	dbgorm "github.com/thebtf/engram/internal/db/gorm"
	"github.com/thebtf/engram/internal/taskcontext"
)

func TestTaskEvidencePreservesSourceAndFragmentHashes(t *testing.T) {
	body := strings.Repeat("é", 3001)
	hash := sha256.Sum256([]byte(body))
	item, ok := taskEvidence("note", 42, 3, "approved.md", body, hex.EncodeToString(hash[:]))
	require.True(t, ok)
	require.True(t, item.Excerpt)
	require.True(t, utf8.ValidString(item.Content))
	require.LessOrEqual(t, len(item.Content), 4000)
	require.Equal(t, hex.EncodeToString(hash[:]), item.SourceHash)
	response := taskcontext.Closed("ok", "OK")
	response.Knowledge = &taskcontext.Knowledge{MemoryStatus: "empty", NoteStatus: "ok", RuleStatus: "empty", Items: []taskcontext.Evidence{item}}
	require.NoError(t, response.Validate())
	_, ok = taskEvidence("note", 42, 3, "approved.md", body, strings.Repeat("0", 64))
	require.False(t, ok)
	memory, ok := taskEvidence("memory", 7, 0, "", "unversioned memory", "")
	require.True(t, ok)
	require.Zero(t, memory.Version)
}

func TestTaskContextAdmissionWithoutCodeAndNativeGuard(t *testing.T) {
	t.Setenv("ENGRAM_CODE_INTEL_ENABLED", "false")
	server := NewServer(ServerOptions{Version: "test"})
	server.SetMemoryStore(&dbgorm.MemoryStore{})
	found := false
	for _, tool := range server.ListTools() {
		found = found || tool.Name == "codebase_task_context"
	}
	require.True(t, found)
	raw, err := json.Marshal(taskcontext.Request{Schema: 1, HostSessionID: "host", Query: "restore", DeadlineUnixMS: time.Now().Add(time.Second).UnixMilli(), MaxBytes: 8000})
	require.NoError(t, err)
	text, err := server.handleCodebaseTaskContext(context.Background(), raw)
	require.NoError(t, err)
	var response taskcontext.Response
	require.NoError(t, json.Unmarshal([]byte(text), &response))
	require.Equal(t, "forbidden", response.Status)
	require.Equal(t, "PERMISSION_DENIED", response.ReasonCode)
}
