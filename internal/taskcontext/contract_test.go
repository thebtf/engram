package taskcontext

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/thebtf/engram/internal/uci"
)

func TestTaskContextExplicitSearchRequiredIsValid(t *testing.T) {
	response := Closed("unavailable", "EXPLICIT_SEARCH_REQUIRED")
	require.NoError(t, response.Validate())
	require.Nil(t, response.Packet)
}

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
