package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/thebtf/engram/pkg/models"
)

// mockMemoryStoreForRecall is a minimal mock that satisfies the interface
// surface used by handleRecallMemory (List + UpdateLifecycleFields).
type mockMemoryStoreForRecall struct {
	memories []*models.Memory
}

func (m *mockMemoryStoreForRecall) List(_ context.Context, _ string, _ int) ([]*models.Memory, error) {
	return m.memories, nil
}

func (m *mockMemoryStoreForRecall) UpdateLifecycleFields(_ context.Context, _ int64, _ map[string]any) error {
	return nil
}

func (m *mockMemoryStoreForRecall) SearchFTS(_ context.Context, _, _ string, _ int) ([]*models.Memory, error) {
	return m.memories, nil
}

func (m *mockMemoryStoreForRecall) GetByIDs(_ context.Context, _ string, ids []int64) ([]*models.Memory, error) {
	result := make([]*models.Memory, 0)
	for _, mem := range m.memories {
		for _, id := range ids {
			if mem.ID == id {
				result = append(result, mem)
				break
			}
		}
	}
	return result, nil
}

func (m *mockMemoryStoreForRecall) Get(_ context.Context, id int64) (*models.Memory, error) {
	for _, mem := range m.memories {
		if mem.ID == id {
			return mem, nil
		}
	}
	return nil, fmt.Errorf("not found: %d", id)
}

func (m *mockMemoryStoreForRecall) Create(_ context.Context, mem *models.Memory) (*models.Memory, error) {
	return mem, nil
}

func (m *mockMemoryStoreForRecall) Update(_ context.Context, mem *models.Memory) (*models.Memory, error) {
	return mem, nil
}
func (m *mockMemoryStoreForRecall) Delete(_ context.Context, _ int64) error { return nil }
func (m *mockMemoryStoreForRecall) FindByProject(_ context.Context, _ string) ([]*models.Memory, error) {
	return m.memories, nil
}

func (m *mockMemoryStoreForRecall) FindSimilarByContent(_ context.Context, _, _ string, _ float32, _ int) ([]*models.Memory, error) {
	return nil, nil
}

func (m *mockMemoryStoreForRecall) FindInjectable(_ context.Context, _ string, _ int) ([]*models.Memory, error) {
	return m.memories, nil
}
func (m *mockMemoryStoreForRecall) Suppress(_ context.Context, _ int64) error       { return nil }
func (m *mockMemoryStoreForRecall) Rate(_ context.Context, _ int64, _ string) error { return nil }

// TestRecallMemory_FlagON_SchemaHasVnextParams verifies that when ENGRAM_VNEXT_ENABLED=true,
// the recall_memory tool schema includes all four vnext-gated parameters and the scope params.
// Flag matrix: ON/OFF (ENGRAM_VNEXT_ENABLED=true, ENGRAM_VNEXT_F_ENABLED=false).
func TestRecallMemory_FlagON_SchemaHasVnextParams(t *testing.T) {
	t.Setenv("ENGRAM_VNEXT_ENABLED", "true")
	t.Setenv("ENGRAM_VNEXT_F_ENABLED", "")

	tool := recallMemoryTool()
	props, _ := tool.InputSchema["properties"].(map[string]any)

	// Vnext hybrid params present.
	vnextParams := []string{"expand_graph", "min_confidence", "tier_filter", "explain"}
	for _, p := range vnextParams {
		if _, ok := props[p]; !ok {
			t.Errorf("ON/OFF schema must include vnext param %q", p)
		}
	}
	// Scope params unconditional.
	for _, p := range []string{"session_id", "include_scopes"} {
		if _, ok := props[p]; !ok {
			t.Errorf("ON/OFF schema must always include scope param %q", p)
		}
	}
}

// TestRecallMemory_FlagMatrix_BothEnabled_SchemaCombinesParams verifies that
// both ENGRAM_VNEXT_ENABLED=true and ENGRAM_VNEXT_F_ENABLED=true together produce
// a schema that composes all hybrid params AND scope params.
// Flag matrix: ON/ON (both flags true).
func TestRecallMemory_FlagMatrix_BothEnabled_SchemaCombinesParams(t *testing.T) {
	t.Setenv("ENGRAM_VNEXT_ENABLED", "true")
	t.Setenv("ENGRAM_VNEXT_F_ENABLED", "true")

	tool := recallMemoryTool()
	props, _ := tool.InputSchema["properties"].(map[string]any)

	// All six params (4 hybrid + 2 scope) must be present.
	allParams := []string{"expand_graph", "min_confidence", "tier_filter", "explain", "session_id", "include_scopes"}
	for _, p := range allParams {
		if _, ok := props[p]; !ok {
			t.Errorf("ON/ON schema must include param %q (hybrid+scope composition)", p)
		}
	}
}

func TestRecallRetiredActionsAreUnknown(t *testing.T) {
	t.Parallel()
	s := &Server{}
	for _, action := range []string{"preset", "by_concept", "by_type", "similar", "timeline", "sessions", "explain"} {
		action := action
		t.Run(action, func(t *testing.T) {
			t.Parallel()
			args, err := json.Marshal(map[string]any{"action": action, "query": "test", "project": "testproj"})
			if err != nil {
				t.Fatal(err)
			}
			_, err = s.handleRecall(context.Background(), args)
			if err == nil {
				t.Fatalf("expected unknown-action error for recall(%s)", action)
			}
			if !strings.Contains(err.Error(), "unknown recall action") {
				t.Fatalf("retired action %q returned %q", action, err.Error())
			}
		})
	}
}
