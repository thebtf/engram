package learning

import (
	"context"
	"database/sql"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/thebtf/engram/pkg/models"
)

func nullString(value string) sql.NullString {
	return sql.NullString{String: value, Valid: value != ""}
}

func TestMergeDecisionConstantsAndStruct(t *testing.T) {
	require.Equal(t, "CREATE_NEW", MergeActionCreateNew)
	require.Equal(t, "UPDATE", MergeActionUpdate)
	require.Equal(t, "SUPERSEDE", MergeActionSupersede)
	require.Equal(t, "SKIP", MergeActionSkip)

	decision := MergeDecision{Action: MergeActionCreateNew, TargetID: 42}
	require.Equal(t, MergeActionCreateNew, decision.Action)
	require.Equal(t, int64(42), decision.TargetID)
}

type mergeMockLLMClient struct {
	response string
	err      error
}

func (m *mergeMockLLMClient) Complete(_ context.Context, _, _ string) (string, error) {
	return m.response, m.err
}

func TestDecideMerge_ValidJSONResponse(t *testing.T) {
	newObs := &models.Observation{Type: models.ObsTypeDecision, Title: nullString("Rule: X"), Narrative: nullString("New narrative")}
	candidates := []*models.Observation{{ID: 7, Type: models.ObsTypeDecision, Title: nullString("Rule: X old")}}
	llm := &mergeMockLLMClient{response: `{"action":"UPDATE","target_id":7}`}

	decision, err := DecideMerge(context.Background(), llm, newObs, candidates)
	require.NoError(t, err)
	require.Equal(t, MergeActionUpdate, decision.Action)
	require.Equal(t, int64(7), decision.TargetID)
}

func TestDecideMerge_MalformedJSONFallsBackToCreateNew(t *testing.T) {
	newObs := &models.Observation{Type: models.ObsTypeDecision, Title: nullString("Rule: X")}
	llm := &mergeMockLLMClient{response: `not-json`}

	decision, err := DecideMerge(context.Background(), llm, newObs, nil)
	require.NoError(t, err)
	require.Equal(t, MergeActionCreateNew, decision.Action)
	require.Zero(t, decision.TargetID)
}

func TestDecideMerge_InvalidActionFallsBackToCreateNew(t *testing.T) {
	newObs := &models.Observation{Type: models.ObsTypeDecision, Title: nullString("Rule: X")}
	llm := &mergeMockLLMClient{response: `{"action":"DELETE","target_id":7}`}

	decision, err := DecideMerge(context.Background(), llm, newObs, nil)
	require.NoError(t, err)
	require.Equal(t, MergeActionCreateNew, decision.Action)
	require.Zero(t, decision.TargetID)
}

func TestDecideMerge_NilClientFallsBackToCreateNew(t *testing.T) {
	newObs := &models.Observation{Type: models.ObsTypeDecision, Title: nullString("Rule: X")}

	decision, err := DecideMerge(context.Background(), nil, newObs, nil)
	require.NoError(t, err)
	require.Equal(t, MergeActionCreateNew, decision.Action)
	require.Zero(t, decision.TargetID)
}

func TestDecideMerge_LLMErrorFallsBackToCreateNew(t *testing.T) {
	newObs := &models.Observation{Type: models.ObsTypeDecision, Title: nullString("Rule: X")}
	llm := &mergeMockLLMClient{err: context.DeadlineExceeded}

	decision, err := DecideMerge(context.Background(), llm, newObs, nil)
	require.NoError(t, err)
	require.Equal(t, MergeActionCreateNew, decision.Action)
	require.Zero(t, decision.TargetID)
}

func TestDecideMerge_AllActionValues(t *testing.T) {
	newObs := &models.Observation{Type: models.ObsTypeDecision, Title: nullString("Rule: X")}
	tests := []struct {
		name     string
		response string
		want     string
		targetID int64
	}{
		{name: "create_new", response: `{"action":"CREATE_NEW","target_id":0}`, want: MergeActionCreateNew, targetID: 0},
		{name: "update", response: `{"action":"UPDATE","target_id":7}`, want: MergeActionUpdate, targetID: 7},
		{name: "supersede", response: `{"action":"SUPERSEDE","target_id":8}`, want: MergeActionSupersede, targetID: 8},
		{name: "skip", response: `{"action":"SKIP","target_id":9}`, want: MergeActionSkip, targetID: 9},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			decision, err := DecideMerge(context.Background(), &mergeMockLLMClient{response: tt.response}, newObs, nil)
			require.NoError(t, err)
			require.Equal(t, tt.want, decision.Action)
			require.Equal(t, tt.targetID, decision.TargetID)
		})
	}
}

func TestDecideMerge_UpdateWithoutTargetFallsBackToCreateNew(t *testing.T) {
	newObs := &models.Observation{Type: models.ObsTypeDecision, Title: nullString("Rule: X")}
	decision, err := DecideMerge(context.Background(), &mergeMockLLMClient{response: `{"action":"UPDATE"}`}, newObs, nil)
	require.NoError(t, err)
	require.Equal(t, MergeActionCreateNew, decision.Action)
	require.Zero(t, decision.TargetID)
}

func TestDecideMerge_SupersedeWithoutTargetFallsBackToCreateNew(t *testing.T) {
	newObs := &models.Observation{Type: models.ObsTypeDecision, Title: nullString("Rule: X")}
	decision, err := DecideMerge(context.Background(), &mergeMockLLMClient{response: `{"action":"SUPERSEDE","target_id":0}`}, newObs, nil)
	require.NoError(t, err)
	require.Equal(t, MergeActionCreateNew, decision.Action)
	require.Zero(t, decision.TargetID)
}

