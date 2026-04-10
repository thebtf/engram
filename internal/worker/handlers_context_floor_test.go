package worker

import (
	"context"
	"testing"

	"github.com/thebtf/engram/pkg/models"
)

// TestFillToFloor_ZeroFloor_NoFetch verifies that fillToFloor does NOT call fetch when floor == 0.
// This is the v4 silence path (FR-1): an empty result set must remain empty.
func TestFillToFloor_ZeroFloor_NoFetch(t *testing.T) {
	fetchCalled := false
	fetch := func(_ context.Context, _ int) ([]*models.Observation, error) {
		fetchCalled = true
		return []*models.Observation{{ID: 99}}, nil
	}
	result := fillToFloor(context.Background(), 0, nil, nil, fetch)
	if fetchCalled {
		t.Fatal("floor=0: expected fetch NOT to be called, but it was")
	}
	if len(result) != 0 {
		t.Fatalf("floor=0: expected empty result, got %d observations", len(result))
	}
}

// TestFillToFloor_ZeroFloor_ExistingObservations verifies that floor=0 leaves an existing slice unchanged.
func TestFillToFloor_ZeroFloor_ExistingObservations(t *testing.T) {
	existing := []*models.Observation{{ID: 1}, {ID: 2}}
	fetchCalled := false
	fetch := func(_ context.Context, _ int) ([]*models.Observation, error) {
		fetchCalled = true
		return nil, nil
	}
	result := fillToFloor(context.Background(), 0, existing, nil, fetch)
	if fetchCalled {
		t.Fatal("floor=0: expected fetch NOT to be called even with existing observations")
	}
	if len(result) != 2 {
		t.Fatalf("floor=0: expected 2 unchanged observations, got %d", len(result))
	}
}

// TestFillToFloor_FloorThree_EmptyInput_FetchesFill verifies that floor=3 triggers fill when list is empty.
func TestFillToFloor_FloorThree_EmptyInput_FetchesFill(t *testing.T) {
	fetchCalled := false
	fillObservations := []*models.Observation{{ID: 10}, {ID: 20}, {ID: 30}}
	fetch := func(_ context.Context, _ int) ([]*models.Observation, error) {
		fetchCalled = true
		return fillObservations, nil
	}
	result := fillToFloor(context.Background(), 3, nil, nil, fetch)
	if !fetchCalled {
		t.Fatal("floor=3 with empty list: expected fetch to be called")
	}
	if len(result) != 3 {
		t.Fatalf("floor=3 with empty list: expected 3 observations after fill, got %d", len(result))
	}
}

// TestFillToFloor_FloorThree_AlreadyMet_NoFetch verifies that no fill occurs when the floor is already met.
func TestFillToFloor_FloorThree_AlreadyMet_NoFetch(t *testing.T) {
	existing := []*models.Observation{{ID: 1}, {ID: 2}, {ID: 3}}
	fetchCalled := false
	fetch := func(_ context.Context, _ int) ([]*models.Observation, error) {
		fetchCalled = true
		return nil, nil
	}
	result := fillToFloor(context.Background(), 3, existing, nil, fetch)
	if fetchCalled {
		t.Fatal("floor=3 already met: expected fetch NOT to be called")
	}
	if len(result) != 3 {
		t.Fatalf("floor=3 already met: expected 3 unchanged observations, got %d", len(result))
	}
}

// TestFillToFloor_Deduplication verifies that fill does not add observations already in existing.
func TestFillToFloor_Deduplication(t *testing.T) {
	existing := []*models.Observation{{ID: 1}}
	// fetch returns ID 1 (duplicate) and ID 2 (new)
	fetch := func(_ context.Context, _ int) ([]*models.Observation, error) {
		return []*models.Observation{{ID: 1}, {ID: 2}}, nil
	}
	result := fillToFloor(context.Background(), 2, existing, nil, fetch)
	if len(result) != 2 {
		t.Fatalf("expected 2 observations after dedup fill, got %d", len(result))
	}
	ids := map[int64]bool{}
	for _, o := range result {
		ids[o.ID] = true
	}
	if !ids[1] || !ids[2] {
		t.Fatalf("expected IDs {1, 2} in result, got %v", ids)
	}
}
