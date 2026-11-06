package graph

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestEdgeJSONBoundaryPreservesInstantAndErrors(t *testing.T) {
	plusThree := time.FixedZone("synthetic", 3*60*60)
	boundary := time.Date(9999, 12, 31, 23, 59, 59, 0, time.UTC)
	edge := Edge{ValidUntil: boundary.In(plusThree)}
	encoded, err := json.Marshal(edge)
	require.NoError(t, err)
	var decoded Edge
	require.NoError(t, json.Unmarshal(encoded, &decoded))
	require.True(t, decoded.ValidUntil.Equal(boundary), "serialization must preserve the stored instant")
	require.Equal(t, 10000, edge.ValidUntil.Year(), "marshaling must not mutate the original edge")

	ordinary := time.Date(2026, 4, 5, 6, 7, 8, 0, plusThree)
	encoded, err = json.Marshal(Edge{ValidUntil: ordinary})
	require.NoError(t, err)
	require.Contains(t, string(encoded), `"valid_until":"2026-04-05T06:07:08+03:00"`)

	_, err = json.Marshal(Edge{ValidUntil: time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC)})
	require.Error(t, err, "an instant outside JSON's UTC range must remain an error")
}
