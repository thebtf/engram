package gorm

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestIssueSessionSelectionAttributionCompatibility(t *testing.T) {
	const first = "01a1a365-f562-75dc-a577-53db1b92693d"
	const second = "01a1a365-f562-75dc-a577-53db1b92693e"
	const conversation = "conversation-20261104_alpha.17"
	for _, tc := range []struct {
		name, transport, reported, client, session, source string
	}{
		{"uuid_transport_precedence", first, second, "omp", first, "transport"},
		{"uuid_reported", "", first, "omp", first, "reported"},
		{"openclaw_reported_opaque_conversation", "", conversation, "openclaw", conversation, "reported"},
		{"openclaw_transport_opaque_conversation", conversation, second, "openclaw", conversation, "transport"},
		{"unknown_client_keeps_valid_reported_provenance", "", first, "future-host-client", first, "reported"},
		{"missing_client_does_not_block", "", first, "", first, "reported"},
		{"missing_session_stays_unknown", "", "", "openclaw", "", ""},
		{"nil_uuid_reported_stays_unknown", "", uuid.Nil.String(), "openclaw", "", ""},
		{"nil_uuid_transport_stays_unknown", uuid.Nil.String(), "", "omp", "", ""},
		{"nil_transport_can_fall_back_to_reported", uuid.Nil.String(), second, "omp", second, "reported"},
		{"unsafe_transport_can_fall_back_to_reported", "bad\ntransport", second, "openclaw", second, "reported"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := NewIssueAttribution(tc.transport, tc.reported, tc.client)
			require.NoError(t, err, "optional provenance must not reject an otherwise authorized operation")
			assert.Equal(t, tc.session, got.SessionID)
			assert.Equal(t, tc.source, got.SessionSource)
			if tc.client == "" {
				assert.Empty(t, got.Client, "a missing client must not be inferred from the session")
			}
		})
	}

	for index, value := range []string{
		"bad\nconversation", "bad\rconversation", "bad\tconversation", "bad\x00conversation",
		"bad\x1bconversation", "bad\x7fconversation", "bad\u0085conversation", strings.Repeat("s", 16384),
	} {
		for _, transport := range []bool{false, true} {
			label := "reported"
			if transport {
				label = "transport"
			}
			t.Run(fmt.Sprintf("%s_unsafe_%d", label, index), func(t *testing.T) {
				var got IssueAttribution
				var err error
				if transport {
					got, err = NewIssueAttribution(value, "", "openclaw")
				} else {
					got, err = NewIssueAttribution("", value, "openclaw")
				}
				require.NoError(t, err, "unsafe or oversized optional metadata is unknown, not an operation failure")
				assert.Empty(t, got.SessionID)
				assert.Empty(t, got.SessionSource)
			})
		}
	}
}

func TestIssueSessionSelectionStoreRoundTripAndDefaultList(t *testing.T) {
	db := openCandidateTestDB(t)
	store := NewIssueStore(db)
	ctx := context.Background()
	project := sessionTestProject(t, db)
	const first = "01a1a365-f562-75dc-a577-53db1b92693d"
	const second = "01a1a365-f562-75dc-a577-53db1b92693e"

	// Decode the real Issue wire model, not a hypothetical new Go field.
	// No session rows are created: recipient references must not require liveness.
	for i, recipient := range []string{first, second, ""} {
		input := map[string]any{
			"title": "recipient persistence fixture", "body": "unchanged user body",
			"source_project": project, "target_project": project,
			"created_by_session": second, "created_by_session_source": "reported",
			"source_client": "openclaw",
		}
		if recipient != "" {
			input["target_session"] = recipient
		}
		raw, err := json.Marshal(input)
		require.NoError(t, err)
		var issue Issue
		require.NoError(t, json.Unmarshal(raw, &issue))
		id, err := store.CreateIssue(ctx, &issue)
		require.NoError(t, err)

		// Database JSON reads the actual persisted row even before a new model
		// field exists; an input-echo implementation cannot satisfy this assertion.
		var persisted string
		require.NoError(t, db.Raw("SELECT to_jsonb(issues)::text FROM issues WHERE id = ?", id).Scan(&persisted).Error)
		var row map[string]any
		require.NoError(t, json.Unmarshal([]byte(persisted), &row))
		if recipient != "" {
			assert.Equal(t, recipient, row["target_session"])
		} else {
			assert.True(t, row["target_session"] == nil || row["target_session"] == "", "omission must remain unknown")
		}
		assert.Equal(t, second, row["created_by_session"])
		assert.Equal(t, project, row["target_project"])

		reloaded := NewIssueStore(db)
		got, comments, err := reloaded.GetIssue(ctx, id)
		require.NoError(t, err)
		assert.Empty(t, comments)
		assert.Equal(t, "unchanged user body", got.Body)
		assert.Equal(t, second, got.CreatedBySession)
		encoded, err := json.Marshal(got)
		require.NoError(t, err)
		var response map[string]any
		require.NoError(t, json.Unmarshal(encoded, &response))
		if i < 2 {
			assert.Equal(t, recipient, response["target_session"], "GetIssue must reconstruct the persisted recipient")
		}
	}

	rows, total, err := store.ListIssuesEx(ctx, IssueListParams{
		TargetProject: project, Statuses: []string{"open"}, Limit: 20,
	})
	require.NoError(t, err)
	assert.EqualValues(t, 3, total)
	require.Len(t, rows, 3, "ordinary project lists must retain both recipient sessions and the untargeted issue")
	var recipients []string
	for _, issue := range rows {
		raw, err := json.Marshal(issue)
		require.NoError(t, err)
		var response map[string]any
		require.NoError(t, json.Unmarshal(raw, &response))
		if recipient, ok := response["target_session"].(string); ok && recipient != "" {
			recipients = append(recipients, recipient)
		}
	}
	assert.ElementsMatch(t, []string{first, second}, recipients)
}