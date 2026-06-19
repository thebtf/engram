package gorm

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/thebtf/engram/pkg/models"
)

func TestMigration146_RuleInjectionEventsTable(t *testing.T) {
	db := openCandidateTestDB(t)
	require.True(t, db.Migrator().HasTable("rule_injection_events"))

	for _, indexName := range []string{
		"idx_rule_injection_events_project_created",
		"idx_rule_injection_events_session_created",
		"idx_rule_injection_events_rule_version_created",
		"idx_rule_injection_events_event_created",
	} {
		var count int64
		require.NoError(t, db.Raw(`
			SELECT COUNT(*) FROM pg_indexes
			WHERE tablename = 'rule_injection_events' AND indexname = ?
		`, indexName).Scan(&count).Error)
		require.Equal(t, int64(1), count, "index %s must exist", indexName)
	}

	err := db.Exec(`
		INSERT INTO rule_injection_events (session_id, project, surface, event_type)
		VALUES (?, ?, ?, ?)
	`, "rg2-invalid-session", "rg2-project", "session-start", "invalid_event").Error
	require.Error(t, err, "invalid rule injection event type must be rejected")
}

func TestRuleInjectionEventStore_RecordAndListBySession(t *testing.T) {
	db := openCandidateTestDB(t)
	store := NewRuleInjectionEventStore(db)
	ctx := context.Background()

	project := fmt.Sprintf("rg2-rule-events-%d", time.Now().UnixNano())
	sessionID := project + "-session"
	t.Cleanup(func() {
		_ = db.Exec(`DELETE FROM rule_injection_events WHERE project = ?`, project).Error
		_ = db.Exec(`DELETE FROM rule_versions WHERE content LIKE ?`, "RG-2 event fixture "+project+"%").Error
		_ = db.Exec(`DELETE FROM rule_families WHERE family_key LIKE ?`, "rg2-family-event-"+project+"%").Error
		_ = db.Exec(`DELETE FROM behavioral_rules WHERE edited_by = ?`, project).Error
	})

	versionID := insertRuleVersionFixture(t, db, "event-"+project, models.RuleStateActiveProject, "developer", 10)
	behavioralStore := NewBehavioralRulesStore(&Store{DB: db})
	legacyRule, err := behavioralStore.Create(ctx, &models.BehavioralRule{
		Project:  &project,
		Content:  "RG-2 event fixture legacy " + project,
		Priority: 9,
		EditedBy: project,
	})
	require.NoError(t, err)

	require.NoError(t, store.RecordEvents(ctx, []*models.RuleInjectionEvent{
		{
			SessionID:      sessionID,
			Project:        project,
			Surface:        "session-start",
			EventType:      models.RuleInjectionEmittedContextual,
			RuleVersionID:  &versionID,
			BudgetPosition: 1,
		},
		{
			SessionID:              sessionID,
			Project:                project,
			Surface:                "session-start",
			EventType:              models.RuleInjectionFallbackLegacy,
			LegacyBehavioralRuleID: &legacyRule.ID,
			Reason:                 "legacy_behavioral_rule_fallback",
			BudgetPosition:         2,
		},
	}))

	got, err := store.ListBySession(ctx, sessionID, 10)
	require.NoError(t, err)
	require.Len(t, got, 2)
	require.Equal(t, models.RuleInjectionEmittedContextual, got[0].EventType)
	require.NotNil(t, got[0].RuleVersionID)
	require.Equal(t, versionID, *got[0].RuleVersionID)
	require.Equal(t, 1, got[0].BudgetPosition)

	require.Equal(t, models.RuleInjectionFallbackLegacy, got[1].EventType)
	require.NotNil(t, got[1].LegacyBehavioralRuleID)
	require.Equal(t, legacyRule.ID, *got[1].LegacyBehavioralRuleID)
	require.Equal(t, "legacy_behavioral_rule_fallback", got[1].Reason)
	require.Equal(t, 2, got[1].BudgetPosition)
}

func TestRuleInjectionEventStore_AggregateByProjectRuleAndEventType(t *testing.T) {
	db := openCandidateTestDB(t)
	store := NewRuleInjectionEventStore(db)
	ctx := context.Background()

	project := fmt.Sprintf("rg3-rule-telemetry-%d", time.Now().UnixNano())
	versionID := insertRuleVersionFixture(t, db, "rg3-telemetry-"+project, models.RuleStateActiveProject, "developer", 10)
	otherVersionID := insertRuleVersionFixture(t, db, "rg3-telemetry-other-"+project, models.RuleStateActiveProject, "developer", 9)
	since := time.Now().Add(-1 * time.Minute).UTC()
	require.NoError(t, store.RecordEvents(ctx, []*models.RuleInjectionEvent{
		{
			SessionID:      project + "-session-1",
			Project:        project,
			Surface:        "session-start",
			EventType:      models.RuleInjectionEmittedContextual,
			RuleVersionID:  &versionID,
			BudgetPosition: 1,
		},
		{
			SessionID:      project + "-session-2",
			Project:        project,
			Surface:        "session-start",
			EventType:      models.RuleInjectionSuppressedPredicate,
			RuleVersionID:  &versionID,
			Reason:         "predicate_false",
			BudgetPosition: 0,
		},
		{
			SessionID:      project + "-session-3",
			Project:        project,
			Surface:        "session-start",
			EventType:      models.RuleInjectionSuppressedPredicate,
			RuleVersionID:  &versionID,
			Reason:         "predicate_false",
			BudgetPosition: 0,
		},
		{
			SessionID:     project + "-session-other",
			Project:       project,
			Surface:       "session-start",
			EventType:     models.RuleInjectionEmittedContextual,
			RuleVersionID: &otherVersionID,
		},
	}))

	aggregate, err := store.AggregateByProjectRuleAndEventType(ctx, RuleInjectionTelemetryParams{
		Project:       project,
		RuleVersionID: versionID,
		Since:         since,
		Limit:         10,
	})
	require.NoError(t, err)

	require.Equal(t, project, aggregate.Project)
	require.Equal(t, versionID, aggregate.RuleVersionID)
	require.False(t, aggregate.NoData)
	require.Len(t, aggregate.Buckets, 2)

	byType := map[models.RuleInjectionEventType]RuleInjectionTelemetryBucket{}
	for _, bucket := range aggregate.Buckets {
		byType[bucket.EventType] = bucket
	}
	require.Equal(t, 1, byType[models.RuleInjectionEmittedContextual].Count)
	require.Equal(t, 2, byType[models.RuleInjectionSuppressedPredicate].Count)
	require.Contains(t, byType[models.RuleInjectionSuppressedPredicate].Reasons, "predicate_false")
	require.NotZero(t, byType[models.RuleInjectionSuppressedPredicate].LastSeenAt)
}

func TestRuleInjectionEventStore_AggregateByProjectRuleAndEventTypeReturnsNoDataWhenEmpty(t *testing.T) {
	db := openCandidateTestDB(t)
	store := NewRuleInjectionEventStore(db)
	ctx := context.Background()
	project := fmt.Sprintf("rg3-rule-telemetry-empty-%d", time.Now().UnixNano())

	aggregate, err := store.AggregateByProjectRuleAndEventType(ctx, RuleInjectionTelemetryParams{
		Project: project,
		Since:   time.Now().Add(-1 * time.Minute).UTC(),
		Limit:   10,
	})
	require.NoError(t, err)
	require.True(t, aggregate.NoData)
	require.Equal(t, project, aggregate.Project)
	require.Empty(t, aggregate.Buckets)
}

func TestRuleInjectionEventStore_RejectsInvalidEvents(t *testing.T) {
	db := openCandidateTestDB(t)
	store := NewRuleInjectionEventStore(db)

	err := store.RecordEvents(context.Background(), []*models.RuleInjectionEvent{{
		SessionID: "rg2-invalid",
		Project:   "rg2",
		Surface:   "session-start",
		EventType: models.RuleInjectionEventType("bad"),
	}})
	require.ErrorIs(t, err, models.ErrRuleRequiredFieldMissing)
}
