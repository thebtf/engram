package worker

import (
	"fmt"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	gormdb "github.com/thebtf/engram/internal/db/gorm"
)

func TestMemoryTransitionRejectsExplicitInvalidProject(t *testing.T) {
	db := openClawMemoryFixtureDB(t)
	for _, suffix := range []string{
		"?project", "?project=", "?project=global", "?project=self", "?project=%ZZ",
		"?project=11111111-1111-4111-8111-111111111111&project=11111111-1111-4111-8111-111111111111",
	} {
		t.Run(suffix, func(t *testing.T) {
			f := newOpenClawLifecycleFixture(t, db)
			target := f.seedPrivate(t, f.tokenA)
			witness := f.seedPrivate(t, f.tokenB)
			status, body := f.call(t, http.MethodPost, fmt.Sprintf("/api/memories/%d/transition%s", target.ID, suffix), f.tokenA, map[string]any{"action": "suppress"})
			assert.Equal(t, http.StatusBadRequest, status, body)
			assert.NotContains(t, body, target.Content)
			assert.Equal(t, target, f.rawRow(t, target.ID))
			assert.Equal(t, witness, f.rawRow(t, witness.ID))
		})
	}
}

func TestMemoryTransitionRejectsInvalidActionOrState(t *testing.T) {
	db := openClawMemoryFixtureDB(t)
	for _, scenario := range []struct {
		status string
		action string
		want   int
	}{
		{"active", "", http.StatusBadRequest},
		{"active", "delete", http.StatusBadRequest},
		{"active", "unsuppress", http.StatusConflict},
		{"flagged", "suppress", http.StatusConflict},
		{"archived", "suppress", http.StatusConflict},
		{"archived", "unsuppress", http.StatusConflict},
		{"archived", "archive", http.StatusConflict},
	} {
		t.Run(scenario.status+"/"+scenario.action, func(t *testing.T) {
			f := newOpenClawLifecycleFixture(t, db)
			target := f.seedPrivate(t, f.tokenA)
			witness := f.seedPrivate(t, f.tokenB)
			require.NoError(t, f.db.Model(&gormdb.Memory{}).Where("id = ?", target.ID).Update("status", scenario.status).Error)
			target = f.rawRow(t, target.ID)
			status, body := f.call(t, http.MethodPost, fmt.Sprintf("/api/memories/%d/transition", target.ID), f.tokenA, map[string]any{"action": scenario.action})
			assert.Equal(t, scenario.want, status, body)
			assert.NotContains(t, body, target.Content)
			assert.Equal(t, target, f.rawRow(t, target.ID))
			assert.Equal(t, witness, f.rawRow(t, witness.ID))
		})
	}
}

func TestOpenClawRegisteredMemorySuppressConflictRealRouter(t *testing.T) {
	db := openClawMemoryFixtureDB(t)
	for _, tool := range []string{"engram_suppress", "memory_forget"} {
		t.Run(tool, func(t *testing.T) {
			f := newOpenClawLifecycleFixture(t, db)
			target := f.seedPrivate(t, f.tokenA)
			witness := f.seedPrivate(t, f.tokenB)
			params := map[string]any{"id": fmt.Sprint(target.ID)}
			if tool == "engram_suppress" {
				params["id"] = target.ID
			}
			f.runRegisteredLifecycleTool(t, tool, "suppress", f.project, f.tokenA, params, false)
			f.assertOwnerState(t, target, "flagged", 1)
			flagged := f.rawRow(t, target.ID)
			output := f.runRegisteredLifecycleTool(t, tool, "suppress", f.project, f.tokenA, params, false, true)
			assert.NotContains(t, output, "outcome unknown", "A received HTTP 409 is a confirmed refusal, not transport uncertainty")
			assert.Equal(t, flagged, f.rawRow(t, target.ID), "The repeated suppress must preserve the entire flagged row and its version")
			assert.Equal(t, witness, f.rawRow(t, witness.ID))
			f.assertOwnerState(t, target, "flagged", 1)
		})
	}
}
