package taskcontext

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/thebtf/engram/internal/uci"
)

func TestTaskContextWireClampsWithoutExtendingParent(t *testing.T) {
	start := time.Now()
	request := Request{Schema: 1, HostSessionID: "host", Query: "Alpha", DeadlineUnixMS: 9_007_199_254_740_991, MaxBytes: 1 << 30}
	require.NoError(t, request.Validate())
	require.Equal(t, start.Add(400*time.Millisecond), request.Deadline(start))
	require.Equal(t, MaxOutputBytes, request.OutputLimit())
	parent, stop := context.WithDeadline(t.Context(), start.Add(80*time.Millisecond))
	defer stop()
	ctx, cancel := request.Context(parent, start)
	defer cancel()
	deadline, _ := ctx.Deadline()
	require.Equal(t, start.Add(80*time.Millisecond), deadline)
	request.MaxBytes = 1
	require.Equal(t, MinOutputBytes, request.OutputLimit())
	request.MaxBytes = 0
	require.Equal(t, DefaultMaxBytes, request.OutputLimit())
}

func TestTaskContextWireRejectsMalformedAndBoundsWholeFrame(t *testing.T) {
	for _, raw := range [][]byte{
		[]byte(`{"schema":1,"schema":1}`),
		[]byte(`{"schema":1,"locator":"file:///borrowed"}`),
		[]byte(`{"schema":null}`),
		[]byte(`{"schema":1} {}`),
		[]byte("{\"query\":\"" + string([]byte{255}) + "\"}"),
		bytes.Repeat([]byte{'x'}, MaxInputBytes+1),
	} {
		var request Request
		require.Error(t, Decode(bytes.NewReader(raw), &request))
	}
	items := uci.QueryItems{{Excerpt: strings.Repeat("x", MaxOutputBytes)}}
	response := Closed("ok", "OK")
	response.Packet = &Packet{Search: uci.QueryResponse{Items: &items}}
	raw, err := Encode(response, 1024)
	require.NoError(t, err)
	require.LessOrEqual(t, len(raw), 1024)
	var closed Response
	require.NoError(t, json.Unmarshal(raw, &closed))
	require.Equal(t, "unavailable", closed.Status)
	require.Equal(t, "BUDGET_EXCEEDED", closed.ReasonCode)
	require.Nil(t, closed.Packet)
	require.NoError(t, closed.Validate())
	closed.ReasonCode = "UNKNOWN_GUESS"
	require.Error(t, closed.Validate())
	response.Packet = nil
	require.Error(t, response.Validate(), "ok without useful released evidence must fail closed")
}

func TestTaskContextFrameRetainsBoundedKnowledge(t *testing.T) {
	content := strings.Repeat("é", 2000)
	digest := sha256.Sum256([]byte(content))
	hash := hex.EncodeToString(digest[:])
	response := Closed("ok", "OK")
	response.Knowledge = &Knowledge{MemoryStatus: "empty", NoteStatus: "ok", RuleStatus: "empty", Items: []Evidence{{Kind: "note", ID: 42, Version: 2, Path: "approved.md", Content: content, ContentHash: hash, SourceHash: hash}}}
	raw, err := Encode(response, 1024)
	require.NoError(t, err)
	require.LessOrEqual(t, len(raw), 1024)
	var got Response
	require.NoError(t, json.Unmarshal(raw, &got))
	require.NoError(t, got.Validate())
	require.Equal(t, "ok", got.Status)
	require.Len(t, got.Knowledge.Items, 1)
	require.True(t, got.Knowledge.Items[0].Excerpt)
	require.Equal(t, hash, got.Knowledge.Items[0].SourceHash)
	require.NotEqual(t, hash, got.Knowledge.Items[0].ContentHash)
	require.Equal(t, content, response.Knowledge.Items[0].Content, "encoding must not mutate its input")
}
