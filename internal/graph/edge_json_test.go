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
	localBoundary := boundary.In(plusThree)
	edge := Edge{ValidFrom: localBoundary, ValidUntil: localBoundary, CreatedAt: localBoundary, SupersededAt: &localBoundary}
	_, err := json.Marshal(edge)
	require.Error(t, err, "raw Edge JSON must keep its existing behavior")
	encoded, err := json.Marshal(EdgeForJSON(edge))
	require.NoError(t, err)
	var decoded Edge
	require.NoError(t, json.Unmarshal(encoded, &decoded))
	for _, value := range []time.Time{decoded.ValidFrom, decoded.ValidUntil, decoded.CreatedAt, *decoded.SupersededAt} {
		require.True(t, value.Equal(boundary), "serialization must preserve the stored instant")
	}
	require.Equal(t, 10000, edge.ValidUntil.Year(), "normalization must not mutate the original edge")
	require.Equal(t, 10000, edge.SupersededAt.Year(), "normalization must not mutate the pointed-to time")

	ordinary := time.Date(2026, 4, 5, 6, 7, 8, 0, plusThree)
	encoded, err = json.Marshal(EdgeForJSON(Edge{ValidUntil: ordinary, SupersededAt: &ordinary}))
	require.NoError(t, err)
	require.Contains(t, string(encoded), `"valid_until":"2026-04-05T06:07:08+03:00"`)
	require.Contains(t, string(encoded), `"superseded_at":"2026-04-05T06:07:08+03:00"`)

	_, err = json.Marshal(EdgeForJSON(Edge{ValidUntil: time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC)}))
	require.Error(t, err, "an instant outside JSON's UTC range must remain an error")
}
