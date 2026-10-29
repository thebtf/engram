package mcp

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/thebtf/engram/pkg/models"
)

type fakeMemorySignificanceUpdater struct {
	calls []memorySignificanceUpdateCall
	err   error
}

type memorySignificanceUpdateCall struct {
	id     int64
	rating string
}

func (f *fakeMemorySignificanceUpdater) RateMemorySignificance(_ context.Context, id int64, rating string) error {
	f.calls = append(f.calls, memorySignificanceUpdateCall{id: id, rating: rating})
	return f.err
}

type fakeMemorySignificanceStore struct {
	memory  *models.Memory
	updates []memorySignificanceStoreUpdateCall
	getErr  error
	setErr  error
}

type memorySignificanceStoreUpdateCall struct {
	id     int64
	fields map[string]any
}

func (f *fakeMemorySignificanceStore) Get(_ context.Context, id int64) (*models.Memory, error) {
	if f.getErr != nil {
		return nil, f.getErr
	}
	if f.memory == nil || f.memory.ID != id {
		return nil, nil
	}
	clone := *f.memory
	if f.memory.Tags != nil {
		clone.Tags = append([]string(nil), f.memory.Tags...)
	}
	return &clone, nil
}

func (f *fakeMemorySignificanceStore) UpdateLifecycleFields(_ context.Context, id int64, fields map[string]any) error {
	if f.setErr != nil {
		return f.setErr
	}
	copyFields := make(map[string]any, len(fields))
	for k, v := range fields {
		copyFields[k] = v
	}
	f.updates = append(f.updates, memorySignificanceStoreUpdateCall{id: id, fields: copyFields})
	if f.memory != nil && f.memory.ID == id {
		if v, ok := fields["importance_base"].(float64); ok {
			f.memory.ImportanceBase = v
		}
		if v, ok := fields["ts_alpha"].(float64); ok {
			f.memory.TsAlpha = v
		}
		if v, ok := fields["ts_beta"].(float64); ok {
			f.memory.TsBeta = v
		}
		if v, ok := fields["citation_count"].(int); ok {
			f.memory.CitationCount = v
		}
		if v, ok := fields["consecutive_citation_count"].(int); ok {
			f.memory.ConsecutiveCitationCount = v
		}
	}
	return nil
}

func TestMemoryStoreSignificanceUpdaterPersistsChangedFields(t *testing.T) {
	for _, tc := range []struct {
		name          string
		rating        string
		before        *models.Memory
		wantAlpha     float64
		wantBeta      float64
		wantCitations int
		wantStreak    int
	}{
		{
			name:      "useful persists alpha citation and streak",
			rating:    "useful",
			before:    &models.Memory{ID: 42, Content: "fixture", ImportanceBase: 0.5, TsAlpha: 1, TsBeta: 1, CitationCount: 4, ConsecutiveCitationCount: 2},
			wantAlpha: 2, wantBeta: 1, wantCitations: 5, wantStreak: 3,
		},
		{
			name:      "not_useful persists beta and resets streak",
			rating:    "not_useful",
			before:    &models.Memory{ID: 43, Content: "fixture", ImportanceBase: 0.5, TsAlpha: 3, TsBeta: 2, CitationCount: 7, ConsecutiveCitationCount: 4},
			wantAlpha: 3, wantBeta: 3, wantCitations: 7, wantStreak: 0,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := &fakeMemorySignificanceStore{memory: tc.before}
			updater := newMemoryStoreSignificanceUpdater(store)

			err := updater.RateMemorySignificance(context.Background(), tc.before.ID, tc.rating)
			require.NoError(t, err)
			require.Len(t, store.updates, 1)
			assert.Equal(t, tc.before.ID, store.updates[0].id)
			assert.Equal(t, tc.wantAlpha, store.updates[0].fields["ts_alpha"])
			assert.Equal(t, tc.wantBeta, store.updates[0].fields["ts_beta"])
			assert.Equal(t, tc.wantCitations, store.updates[0].fields["citation_count"])
			assert.Equal(t, tc.wantStreak, store.updates[0].fields["consecutive_citation_count"])
			assert.Equal(t, tc.wantAlpha, store.memory.TsAlpha)
			assert.Equal(t, tc.wantBeta, store.memory.TsBeta)
			assert.Equal(t, tc.wantCitations, store.memory.CitationCount)
			assert.Equal(t, tc.wantStreak, store.memory.ConsecutiveCitationCount)
		})
	}
}

func setS6OutcomeFlags(t *testing.T, masterEnabled, s6Enabled bool) {
	t.Helper()
	if masterEnabled {
		t.Setenv("ENGRAM_V7_PLUG_ENABLED", "true")
	} else {
		t.Setenv("ENGRAM_V7_PLUG_ENABLED", "false")
	}
	if s6Enabled {
		t.Setenv("ENGRAM_V7_S6_OUTCOME", "true")
	} else {
		t.Setenv("ENGRAM_V7_S6_OUTCOME", "false")
	}
}

func TestRateMemorySignificanceToolAdvertisedOnlyWhenS6FlagAndUpdaterArePresent(t *testing.T) {
	for _, tc := range []struct {
		name          string
		masterEnabled bool
		s6Enabled     bool
		withUpdater   bool
		wantTool      bool
	}{
		{name: "master off s6 on updater present", masterEnabled: false, s6Enabled: true, withUpdater: true, wantTool: false},
		{name: "master on s6 off updater present", masterEnabled: true, s6Enabled: false, withUpdater: true, wantTool: false},
		{name: "master on s6 on updater missing", masterEnabled: true, s6Enabled: true, withUpdater: false, wantTool: false},
		{name: "retired flags cannot activate tool", masterEnabled: true, s6Enabled: true, withUpdater: true, wantTool: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			setS6OutcomeFlags(t, tc.masterEnabled, tc.s6Enabled)
			srv := NewServer(ServerOptions{Version: "s6-red-test"})
			if tc.withUpdater {
				srv.setTestMemorySignificanceUpdater(&fakeMemorySignificanceUpdater{})
			}

			names := listedToolNames(srv.ListTools())
			require.Equal(t, tc.wantTool, names["rate_memory_significance"])
		})
	}
}

func TestRateMemorySignificanceDirectCallFailsClosedWhenS6Disabled(t *testing.T) {
	for _, tc := range []struct {
		name          string
		masterEnabled bool
		s6Enabled     bool
	}{
		{name: "master off s6 on updater present", masterEnabled: false, s6Enabled: true},
		{name: "master on s6 off updater present", masterEnabled: true, s6Enabled: false},
		{name: "retired flags enabled cannot activate writes", masterEnabled: true, s6Enabled: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			setS6OutcomeFlags(t, tc.masterEnabled, tc.s6Enabled)

			updater := &fakeMemorySignificanceUpdater{}
			srv := NewServer(ServerOptions{Version: "s6-red-test"})
			srv.setTestMemorySignificanceUpdater(updater)

			_, err := srv.callTool(context.Background(), "rate_memory_significance", mustJSON(t, map[string]any{
				"id":     42,
				"rating": "useful",
			}))

			require.ErrorContains(t, err, "rate_memory_significance is deferred")
			assert.Empty(t, updater.calls, "disabled S6 flag states must fail before any learning update")
		})
	}
}

func TestRateMemorySignificanceLegacyRatePathsAreRetired(t *testing.T) {
	for _, tc := range []struct {
		name        string
		tool        string
		args        map[string]any
		errContains string
	}{
		{
			name:        "legacy rate_memory tool",
			tool:        "rate_memory",
			args:        map[string]any{"id": 42, "rating": "useful"},
			errContains: "unknown tool: rate_memory",
		},
		{
			name:        "consolidated feedback rate action",
			tool:        "feedback",
			args:        map[string]any{"action": "rate", "id": 42, "rating": "not_useful"},
			errContains: "unknown feedback action",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			setS6OutcomeFlags(t, true, true)

			updater := &fakeMemorySignificanceUpdater{}
			srv := NewServer(ServerOptions{Version: "s6-red-test"})
			srv.setTestMemorySignificanceUpdater(updater)

			_, err := srv.callTool(context.Background(), tc.tool, mustJSON(t, tc.args))

			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.errContains)
			assert.Empty(t, updater.calls, "retired rate paths must not route to S6")
		})
	}
}
