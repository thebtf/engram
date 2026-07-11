package models

import (
	"encoding/json"
	"testing"
)

// TestSnapshotOpType_IsValid verifies all 6 op types and invalid rejection.
// Engram vNext Milestone F TG6 / T040.
func TestSnapshotOpType_IsValid(t *testing.T) {
	valid := []SnapshotOpType{
		SnapshotOpIngestDoc, SnapshotOpBulkPromote,
		SnapshotOpBulkDelete, SnapshotOpBulkSupersede,
		SnapshotOpCandidateReviewAction, SnapshotOpForgettingReviewAction,
	}
	for _, op := range valid {
		if !op.IsValid() {
			t.Errorf("expected %q to be valid op type", op)
		}
	}
	if SnapshotOpType("invalid").IsValid() {
		t.Error("expected 'invalid' op type to be rejected")
	}
	if SnapshotOpType("").IsValid() {
		t.Error("expected empty op type to be rejected")
	}
}

func TestSnapshotOpIngestDoc_PersistedButNotExecutable(t *testing.T) {
	if !SnapshotOpIngestDoc.IsValid() {
		t.Fatal("historical ingest_doc rows must remain readable")
	}
	if SnapshotOpIngestDoc.IsExecutable() {
		t.Fatal("historical ingest_doc discriminator must never be executable")
	}
	for _, op := range []SnapshotOpType{SnapshotOpBulkPromote, SnapshotOpBulkDelete, SnapshotOpBulkSupersede} {
		if !op.IsExecutable() {
			t.Fatalf("retained bulk operation %q must remain executable", op)
		}
	}
	for _, op := range []SnapshotOpType{SnapshotOpCandidateReviewAction, SnapshotOpForgettingReviewAction} {
		if op.IsExecutable() {
			t.Fatalf("review discriminator %q is not a Facade.Execute operation", op)
		}
	}
}

// TestSnapshotStatus_IsValid verifies all 3 statuses and invalid rejection.
func TestSnapshotStatus_IsValid(t *testing.T) {
	valid := []SnapshotStatus{
		SnapshotStatusPreview, SnapshotStatusCommitted, SnapshotStatusRolledBack,
	}
	for _, s := range valid {
		if !s.IsValid() {
			t.Errorf("expected %q to be valid snapshot status", s)
		}
	}
	if SnapshotStatus("deleted").IsValid() {
		t.Error("expected 'deleted' status to be rejected")
	}
}

// TestNewBulkOpSnapshot_Validation verifies constructor validation rules.
// Anti-stub: returning an empty snapshot without validation fails these assertions.
func TestNewBulkOpSnapshot_Validation(t *testing.T) {
	validBS := json.RawMessage(`{"1":{"content":"original"}}`)

	// Happy path.
	snap, err := NewBulkOpSnapshot("snap-001", SnapshotOpBulkPromote, "master", validBS)
	if err != nil {
		t.Fatalf("expected no error for valid inputs, got %v", err)
	}
	if snap.SnapshotID != "snap-001" {
		t.Errorf("snapshot_id mismatch: got %q", snap.SnapshotID)
	}
	if snap.OpType != SnapshotOpBulkPromote {
		t.Errorf("op_type mismatch: got %q", snap.OpType)
	}
	if snap.Actor != "master" {
		t.Errorf("actor mismatch: got %q", snap.Actor)
	}
	if snap.Status != SnapshotStatusCommitted {
		t.Errorf("expected default status committed, got %q", snap.Status)
	}
	if snap.Pinned {
		t.Error("pinned must default to false")
	}
	if snap.AffectedMemoryIDs == nil {
		t.Error("AffectedMemoryIDs must be non-nil slice")
	}
	if snap.CreatedAt.IsZero() {
		t.Error("CreatedAt must be set")
	}

	// Missing snapshot_id.
	_, err = NewBulkOpSnapshot("", SnapshotOpBulkPromote, "master", validBS)
	if err == nil {
		t.Error("expected error for missing snapshot_id")
	}

	// Invalid op_type.
	_, err = NewBulkOpSnapshot("snap-002", "invalid_op", "master", validBS)
	if err == nil {
		t.Error("expected error for invalid op_type")
	}

	// Missing actor.
	_, err = NewBulkOpSnapshot("snap-003", SnapshotOpBulkDelete, "", validBS)
	if err == nil {
		t.Error("expected error for missing actor")
	}

	// Nil before_state allowed (becomes empty object).
	snap2, err := NewBulkOpSnapshot("snap-004", SnapshotOpIngestDoc, "actor-1", nil)
	if err != nil {
		t.Fatalf("nil before_state should be allowed, got %v", err)
	}
	if string(snap2.BeforeState) != "{}" {
		t.Errorf("nil before_state must become {}, got %q", snap2.BeforeState)
	}
}

// TestBulkOpSnapshot_JSONRoundtrip verifies the JSON shape is stable.
func TestBulkOpSnapshot_JSONRoundtrip(t *testing.T) {
	bs := json.RawMessage(`{"42":{"content":"test row"}}`)
	snap, err := NewBulkOpSnapshot("snap-json-01", SnapshotOpBulkSupersede, "test-actor", bs)
	if err != nil {
		t.Fatalf("NewBulkOpSnapshot: %v", err)
	}
	snap.AffectedMemoryIDs = []int64{42}
	snap.SourceSessionID = "sess-001"

	data, err := json.Marshal(snap)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var decoded BulkOpSnapshot
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if decoded.SnapshotID != snap.SnapshotID {
		t.Errorf("snapshot_id: got %q", decoded.SnapshotID)
	}
	if decoded.OpType != snap.OpType {
		t.Errorf("op_type: got %q", decoded.OpType)
	}
	if len(decoded.AffectedMemoryIDs) != 1 || decoded.AffectedMemoryIDs[0] != 42 {
		t.Errorf("affected_memory_ids: got %v", decoded.AffectedMemoryIDs)
	}
	if decoded.SourceSessionID != "sess-001" {
		t.Errorf("source_session_id: got %q", decoded.SourceSessionID)
	}
}
