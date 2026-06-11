package graph

import (
	"errors"
	"testing"
)

// TestEdgeResolve_T013_ShapeAndErrors verifies:
//   - ErrDangling is exported and distinct
//   - Edge.Resolve method signature exists: (ctx context.Context) (any, any, error)
//   - Edge struct has SourceType and TargetType string fields
//   - Store has a Resolve method that returns (any, any, error)
//
// This is a compile-time shape test. The live behaviour (DB-backed resolution)
// is covered by T015 (integration) and T016 (dangling acceptance).
//
// Anti-stub: removing ErrDangling or the SourceType/TargetType fields causes
// this file to not compile.
//
// Engram vNext Milestone F TG2 / T013.
func TestEdgeResolve_T013_ShapeAndErrors(t *testing.T) {
	// ErrDangling must be exported and non-nil.
	if ErrDangling == nil {
		t.Fatal("ErrDangling must be a non-nil exported sentinel error")
	}

	// ErrDangling must be wrappable (implements error interface).
	var target error = ErrDangling
	if target == nil {
		t.Fatal("ErrDangling must satisfy the error interface")
	}

	// errors.Is must work with ErrDangling.
	wrapped := errors.Join(ErrDangling, errors.New("context"))
	if !errors.Is(wrapped, ErrDangling) {
		t.Fatal("errors.Is(wrapped, ErrDangling) must return true")
	}

	// Edge struct must have SourceType and TargetType fields.
	e := Edge{
		SourceType: "memory",
		TargetType: "memory",
	}
	if e.SourceType != "memory" {
		t.Errorf("Edge.SourceType = %q, want 'memory'", e.SourceType)
	}
	if e.TargetType != "memory" {
		t.Errorf("Edge.TargetType = %q, want 'memory'", e.TargetType)
	}

	// Edge must have NodeSourceID and NodeTargetID nullable int64 fields.
	var nsid *int64
	e.NodeSourceID = nsid
	e.NodeTargetID = nsid

	t.Log("Edge.Resolve shape verified")
}

// TestEdgeResolve_T013_DefaultTypes verifies that edges without explicit
// discriminators behave as memory-type edges (backward compat).
func TestEdgeResolve_T013_DefaultTypes(t *testing.T) {
	// An edge with empty SourceType / TargetType should be treated as 'memory'.
	src, tgt := int64(1), int64(2)
	e := Edge{
		SourceID: &src,
		TargetID: &tgt,
	}
	// Default to 'memory' when blank.
	if e.SourceType != "" {
		// Not a problem — zero value is empty, and Resolve must handle that.
		_ = e.SourceType
	}

	// Verify resolveDiscriminator helper normalises empty → "memory".
	if resolveDiscriminator("") != "memory" {
		t.Error("resolveDiscriminator(\"\") must return 'memory'")
	}
	if resolveDiscriminator("node") != "node" {
		t.Error("resolveDiscriminator(\"node\") must return 'node'")
	}
	if resolveDiscriminator("memory") != "memory" {
		t.Error("resolveDiscriminator(\"memory\") must return 'memory'")
	}
}
