package mcp

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/stretchr/testify/require"
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
