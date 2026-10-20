package gorm

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"reflect"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/thebtf/engram/internal/projectidentity"
	gormlib "gorm.io/gorm"
)

const projectPartitionRepairKind = "project-partition-repair/v1"

type ProjectPartitionRepairPlan struct {
	Reference           string `json:"plan_reference"`
	Fingerprint         string `json:"plan_fingerprint"`
	PlanID              string `json:"plan_id"`
	ParentReceiptDigest string `json:"parent_receipt_digest"`
	Partitions          int    `json:"partitions"`
	NewProjects         int    `json:"new_projects"`
	MembershipChanges   int    `json:"membership_changes"`
	UnassignedAdditions int    `json:"unassigned_additions"`
}

type ProjectPartitionRepairApplyRequest struct {
	PlanReference   string
	PlanFingerprint string
	ExpectedPlanID  string
}

type ProjectPartitionRepairReceipt struct {
	Kind                string                   `json:"kind"`
	PlanID              string                   `json:"plan_id"`
	Fingerprint         string                   `json:"plan_fingerprint"`
	ParentOperationID   string                   `json:"parent_operation_id"`
	ParentReceiptDigest string                   `json:"parent_receipt_digest"`
	TargetIdentity      string                   `json:"target_identity"`
	Partitions          []projectPartition       `json:"partitions"`
	MembershipChanges   int                      `json:"membership_changes"`
	UnassignedAdditions int                      `json:"unassigned_additions"`
	Changes             []projectPartitionChange `json:"changes"`
	PreservedDigest     string                   `json:"preserved_digest"`
	CompletedAt         time.Time                `json:"completed_at"`
	Proof               string                   `json:"proof"`
}

type ProjectPartitionRepairResult struct {
	DryRun  bool                          `json:"dry_run"`
	Replay  bool                          `json:"replay"`
	Receipt ProjectPartitionRepairReceipt `json:"receipt"`
}

type projectPartition struct {
	RegistryReference string `json:"registry_reference"`
	Group             string `json:"group"`
	ProjectID         string `json:"project_id"`
	Label             string `json:"name"`
	Create            bool   `json:"create"`
}

type projectPartitionPatch struct {
	Before *ConsolidationMembership
	After  ConsolidationMembership
}

type projectPartitionChange struct {
	Table        string  `json:"table"`
	Key          JSONRaw `json:"key"`
	BeforeDigest string  `json:"before_digest"`
	AfterDigest  string  `json:"after_digest"`
}

type projectPartitionRepairState struct {
	ID                  string
	Fingerprint         string
	SourceIdentity      string
	SourceSnapshot      string
	Source              consolidationNativeSnapshot
	ParentOperationID   string
	ParentReceiptDigest string
	Partitions          []projectPartition
	Patches             []projectPartitionPatch
}

func projectPartitionFingerprint(state projectPartitionRepairState) string {
	state.ID, state.Fingerprint = "", ""
	return structuralDigest(state)
}

// Planning reads the source only; no arbitrary caller-provided assignments exist.
func (s *ProjectConsolidationStore) PlanProjectPartitionRepair(ctx context.Context, parentReceiptDigest string) (ProjectPartitionRepairPlan, error) {
	if err := s.available(); err != nil {
		return ProjectPartitionRepairPlan{}, err
	}
	if len(parentReceiptDigest) != 64 {
		return ProjectPartitionRepairPlan{}, consolidationHold("PARENT_RECEIPT_REQUIRED")
	}
	state := projectPartitionRepairState{ParentReceiptDigest: parentReceiptDigest}
	err := s.db.WithContext(ctx).Transaction(func(tx *gormlib.DB) error {
		var err error
		state.SourceIdentity, err = consolidationDatabaseIdentity(tx)
		if err != nil {
			return err
		}
		if err := tx.Raw("SELECT txid_current_snapshot()::text").Scan(&state.SourceSnapshot).Error; err != nil {
			return err
		}
		state.Source, err = consolidationNativeRead(tx, nil)
		if err != nil {
			return err
		}
		var operations []consolidationOperation
		if err := tx.Order("idempotency_key").Find(&operations).Error; err != nil {
			return err
		}
		for _, operation := range operations {
			if consolidationCipherHash(operation.Receipt) != parentReceiptDigest {
				continue
			}
			var receipt ConsolidationReceipt
			if json.Unmarshal(operation.Receipt, &receipt) != nil || receipt.OperationID != operation.OperationID || receipt.Reference != "urn:engram:migration:receipt:"+operation.OperationID || len(receipt.ProjectIDs) == 0 || state.ParentOperationID != "" {
				return consolidationHold("PARENT_RECEIPT_INVALID")
			}
			state.ParentOperationID = operation.OperationID
		}
		if state.ParentOperationID == "" {
			return consolidationHold("PARENT_RECEIPT_INVALID")
		}
		return s.partitionRepairMapping(tx, &state, operations)
	}, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return ProjectPartitionRepairPlan{}, err
	}
	state.Fingerprint = projectPartitionFingerprint(state)
	state.ID = uuid.NewSHA1(uuid.NameSpaceOID, []byte(state.Fingerprint)).String()
	token, err := s.seal(projectPartitionRepairKind, state)
	if err != nil {
		return ProjectPartitionRepairPlan{}, err
	}
	plan := ProjectPartitionRepairPlan{Reference: token, Fingerprint: state.Fingerprint, PlanID: state.ID, ParentReceiptDigest: parentReceiptDigest, Partitions: len(state.Partitions)}
	for _, partition := range state.Partitions {
		if partition.Create {
			plan.NewProjects++
		}
	}
	for _, patch := range state.Patches {
		if patch.Before == nil {
			plan.UnassignedAdditions++
		} else {
			plan.MembershipChanges++
		}
	}
	return plan, nil
}

func (s *ProjectConsolidationStore) partitionRepairMapping(tx *gormlib.DB, state *projectPartitionRepairState, operations []consolidationOperation) error {
	var projects []Project
	if err := tx.Order("id").Find(&projects).Error; err != nil {
		return err
	}
	byID := make(map[string]Project, len(projects))
	aliases := map[string][]string{}
	for _, project := range projects {
		byID[project.ID] = project
		for _, alias := range project.LegacyIDs {
			if alias == "" {
				continue
			}
			ids := aliases[alias]
			if len(ids) == 0 || ids[len(ids)-1] != project.ID {
				aliases[alias] = append(ids, project.ID)
			}
		}
	}
	var members []ConsolidationMembership
	if err := tx.Where("table_name IN ?", []string{"projects", "memories", "versioned_documents", "issues"}).Order("reference").Find(&members).Error; err != nil {
		return err
	}
	addresses := make([]consolidationRow, len(members))
	for i, member := range members {
		addresses[i] = consolidationRow{Table: member.Table, Key: member.RowKey}
	}
	retained, err := s.retainedMemberships(tx, addresses)
	if err != nil {
		return err
	}
	occurrences := retained.occurrences
	accepted := map[string]string{}
	for _, operation := range operations {
		var receipt, authenticated ProjectPartitionRepairReceipt
		if json.Unmarshal(operation.Receipt, &receipt) != nil || receipt.Kind != projectPartitionRepairKind {
			continue
		}
		if s.open(receipt.Proof, projectPartitionRepairKind+"/receipt", &authenticated) != nil {
			return consolidationHold("REPLAY_CONFLICT")
		}
		receipt.Proof = ""
		if !reflect.DeepEqual(receipt, authenticated) {
			return consolidationHold("REPLAY_CONFLICT")
		}
		for _, partition := range receipt.Partitions {
			if prior := accepted[partition.RegistryReference]; prior != "" && prior != partition.ProjectID {
				return consolidationHold("CONFLICTING_MEMBERSHIP")
			}
			accepted[partition.RegistryReference] = partition.ProjectID
		}
	}
	partitionIDs := map[string]string{}
	for _, project := range projects {
		if projectidentity.ValidProjectID(project.ID) || project.RemovedAt != nil || project.IdentityStatus.Valid && project.IdentityStatus.String != "active" {
			continue
		}
		key, _ := json.Marshal(map[string]string{"id": project.ID})
		member, ok := occurrences[consolidationOccurrenceKey("projects", JSONRaw(key), JSONRaw(`["id"]`))]
		if !ok {
			return consolidationHold("STALE_MAPPING")
		}
		digest := sha256.Sum256([]byte(member.Reference))
		group := "urn:engram:group:retained-project-record:" + hex.EncodeToString(digest[:])
		id := uuid.NewSHA1(uuid.NameSpaceOID, []byte("engram.logical-project/v1\x00"+group)).String()
		if member.ProjectID != nil {
			if accepted[member.Reference] != *member.ProjectID {
				return consolidationHold("CONFLICTING_MEMBERSHIP")
			}
			id = *member.ProjectID
		}
		label := project.DisplayName.String
		if strings.TrimSpace(label) == "" {
			label = project.ID
		}
		if !validUCIContextDisplayLabel(label) {
			return consolidationHold("UNSAFE_PROJECT_LABEL")
		}
		_, exists := byID[id]
		if exists && member.ProjectID == nil {
			return consolidationHold("CONFLICTING_MEMBERSHIP")
		}
		if exists {
			current := byID[id]
			if !projectidentity.ValidProjectID(id) || current.RemovedAt != nil || current.IdentityStatus.Valid && current.IdentityStatus.String != "active" {
				return consolidationHold("CONFLICTING_MEMBERSHIP")
			}
		}
		state.Partitions = append(state.Partitions, projectPartition{member.Reference, group, id, label, !exists})
		partitionIDs[project.ID] = id
		if member.ProjectID == nil {
			after := member
			after.ProjectID = &id
			state.Patches = append(state.Patches, projectPartitionPatch{Before: &member, After: after})
		}
	}
	for _, role := range []struct{ table, field string }{{"memories", "project"}, {"versioned_documents", "project"}, {"issues", "source_project"}, {"issues", "target_project"}} {
		var rows []struct {
			ID       int64
			Selector sql.NullString
			Version  string
		}
		if err := tx.Raw("SELECT id," + quoted(role.field) + " AS selector,xmin::text AS version FROM " + quoted(role.table) + " ORDER BY id").Scan(&rows).Error; err != nil {
			return err
		}
		for _, row := range rows {
			key, _ := json.Marshal(map[string]int64{"id": row.ID})
			path, _ := json.Marshal([]string{role.field})
			member, exists := occurrences[consolidationOccurrenceKey(role.table, JSONRaw(key), JSONRaw(path))]
			if !row.Selector.Valid || row.Selector.String == "" {
				if exists || role.table != "memories" && role.field != "source_project" {
					continue
				}
				census := consolidationCensusState{NativeSnapshot: state.SourceSnapshot}
				s.addCensusRow(&census, role.table, role.field, "tenant", JSONRaw(key), nil, "", false)
				occurrence := census.Rows[0]
				occurrence.NativeVersion = row.Version
				after := consolidationMembershipFor(consolidationPlanState{Fingerprint: projectPartitionRepairKind, Census: census}, occurrence, ConsolidationMapping{UnresolvedReference: s.opaque("unresolved", occurrence.Reference), Reason: "unproven_historical_scope"}, JSONRaw(key))
				state.Patches = append(state.Patches, projectPartitionPatch{After: after})
				continue
			}
			id := partitionIDs[row.Selector.String]
			if _, exactPK := byID[row.Selector.String]; !exactPK && id == "" {
				if candidates := aliases[row.Selector.String]; len(candidates) == 1 {
					id = partitionIDs[candidates[0]]
				}
			}
			if id == "" {
				continue
			}
			if !exists {
				return consolidationHold("STALE_MAPPING")
			}
			if member.ProjectID != nil {
				if *member.ProjectID != id {
					return consolidationHold("CONFLICTING_MEMBERSHIP")
				}
				continue
			}
			if err := NewProjectConsolidationStore(tx, s.vault, nil).validateUnresolvedMembership(tx.Statement.Context, []ConsolidationMembership{member}); err != nil {
				return err
			}
			after := member
			after.ProjectID = &id
			state.Patches = append(state.Patches, projectPartitionPatch{Before: &member, After: after})
		}
	}
	sort.Slice(state.Patches, func(i, j int) bool { return state.Patches[i].After.Reference < state.Patches[j].After.Reference })
	return nil
}

func (s *ProjectConsolidationStore) DryRunProjectPartitionRepair(ctx context.Context, request ProjectPartitionRepairApplyRequest) (ProjectPartitionRepairResult, error) {
	return s.applyProjectPartitionRepair(ctx, request, true)
}

func (s *ProjectConsolidationStore) ApplyProjectPartitionRepair(ctx context.Context, request ProjectPartitionRepairApplyRequest) (ProjectPartitionRepairResult, error) {
	return s.applyProjectPartitionRepair(ctx, request, false)
}

func (s *ProjectConsolidationStore) applyProjectPartitionRepair(ctx context.Context, request ProjectPartitionRepairApplyRequest, dryRun bool) (ProjectPartitionRepairResult, error) {
	var result ProjectPartitionRepairResult
	if err := s.available(); err != nil {
		return result, err
	}
	if err := s.distinctTarget(ctx); err != nil {
		return result, err
	}
	var state projectPartitionRepairState
	if err := s.open(request.PlanReference, projectPartitionRepairKind, &state); err != nil {
		return result, err
	}
	if state.Fingerprint != request.PlanFingerprint || state.Fingerprint != projectPartitionFingerprint(state) || state.ID != uuid.NewSHA1(uuid.NameSpaceOID, []byte(state.Fingerprint)).String() {
		return result, consolidationHold("STALE_PLAN")
	}
	if !dryRun && request.ExpectedPlanID == "" {
		return result, consolidationHold("PLAN_ID_REQUIRED")
	}
	if request.ExpectedPlanID != "" && request.ExpectedPlanID != state.ID {
		return result, consolidationHold("STALE_PLAN")
	}
	freshSource := func() error {
		return s.db.WithContext(ctx).Transaction(func(tx *gormlib.DB) error {
			identity, err := consolidationDatabaseIdentity(tx)
			if err != nil {
				return err
			}
			if identity != state.SourceIdentity {
				return consolidationHold("STALE_PLAN")
			}
			actual, err := consolidationNativeRead(tx, nil)
			if err != nil {
				return err
			}
			return consolidationNativeCompare(state.Source, actual, true)
		}, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	}
	if err := freshSource(); err != nil {
		return result, err
	}
	rollback := errors.New("project partition dry run rollback")
	err := s.restoreDB.WithContext(ctx).Transaction(func(tx *gormlib.DB) error {
		if err := tx.Exec("SELECT pg_advisory_xact_lock(hashtextextended(?,0))", projectPartitionRepairKind).Error; err != nil {
			return err
		}
		if err := consolidationLock(tx); err != nil {
			return err
		}
		identity, err := consolidationDatabaseIdentity(tx)
		if err != nil {
			return err
		}
		if identity == state.SourceIdentity {
			return consolidationHold("TARGET_IS_SOURCE")
		}
		before, err := consolidationNativeRead(tx, nil)
		if err != nil {
			return err
		}
		var prior consolidationOperation
		err = tx.Where("idempotency_key=?", state.ID).Take(&prior).Error
		if err == nil {
			if prior.OperationID != state.ID || prior.PlanID != state.ID || prior.PlanFingerprint != state.Fingerprint {
				return consolidationHold("REPLAY_CONFLICT")
			}
			var receipt, authenticated ProjectPartitionRepairReceipt
			if json.Unmarshal(prior.Receipt, &receipt) != nil || s.open(receipt.Proof, projectPartitionRepairKind+"/receipt", &authenticated) != nil {
				return consolidationHold("REPLAY_CONFLICT")
			}
			proof := receipt.Proof
			receipt.Proof = ""
			if !reflect.DeepEqual(receipt, authenticated) || receipt.Kind != projectPartitionRepairKind || receipt.PlanID != state.ID || receipt.Fingerprint != state.Fingerprint || receipt.ParentOperationID != state.ParentOperationID || receipt.ParentReceiptDigest != state.ParentReceiptDigest || receipt.TargetIdentity != identity || !prior.CompletedAt.Equal(receipt.CompletedAt) {
				return consolidationHold("REPLAY_CONFLICT")
			}
			if err := partitionRepairReplay(state, receipt, before); err != nil {
				return err
			}
			receipt.Proof = proof
			result = ProjectPartitionRepairResult{DryRun: dryRun, Replay: true, Receipt: receipt}
			return freshSource()
		}
		if !errors.Is(err, gormlib.ErrRecordNotFound) {
			return err
		}
		if err := consolidationNativeCompare(state.Source, before, false); err != nil {
			return err
		}
		expected := partitionRepairSnapshotCopy(before)
		native := &consolidationNativeState{target: before, expected: expected, pending: map[string]map[string]JSONRaw{}}
		changes := make([]projectPartitionChange, 0, len(state.Partitions)+len(state.Patches))
		for _, partition := range state.Partitions {
			if !partition.Create {
				continue
			}
			project := Project{ID: partition.ProjectID, DisplayName: sql.NullString{String: partition.Label, Valid: true}}
			project.CreatedAt = tx.NowFunc().UTC().Truncate(time.Microsecond)
			if err := tx.Create(&project).Error; err != nil {
				return consolidationCollision(err)
			}
			if err := native.createdProject(project); err != nil {
				return err
			}
			key, _ := json.Marshal(map[string]string{"id": project.ID})
			changes = append(changes, projectPartitionChange{Table: "projects", Key: JSONRaw(key)})
		}
		receipt := ProjectPartitionRepairReceipt{Kind: projectPartitionRepairKind, PlanID: state.ID, Fingerprint: state.Fingerprint, ParentOperationID: state.ParentOperationID, ParentReceiptDigest: state.ParentReceiptDigest, TargetIdentity: identity, Partitions: state.Partitions, CompletedAt: time.Now().UTC().Truncate(time.Microsecond)}
		for _, patch := range state.Patches {
			member := patch.After
			key, _ := json.Marshal(map[string]string{"reference": member.Reference})
			change := projectPartitionChange{Table: "project_consolidation_memberships", Key: JSONRaw(key)}
			if patch.Before == nil {
				if err := tx.Create(&member).Error; err != nil {
					return consolidationCollision(err)
				}
				receipt.UnassignedAdditions++
			} else {
				change.BeforeDigest = before.Tables[change.Table].Rows[string(key)].Digest
				if change.BeforeDigest == "" {
					return consolidationHold("STALE_MAPPING")
				}
				updated := tx.Model(&ConsolidationMembership{}).Where("reference=? AND project_id IS NULL AND encode(sha256(convert_to(to_jsonb(project_consolidation_memberships)::text,'UTF8')),'hex')=?", member.Reference, change.BeforeDigest).Update("project_id", member.ProjectID)
				if updated.Error != nil {
					return updated.Error
				}
				if updated.RowsAffected != 1 {
					return consolidationHold("STALE_MAPPING")
				}
				receipt.MembershipChanges++
			}
			raw, err := consolidationMembershipObject(member)
			if err != nil {
				return err
			}
			if err := native.expect(change.Table, JSONRaw(key), raw); err != nil {
				return err
			}
			changes = append(changes, change)
		}
		after, err := consolidationNativeRead(tx, nil)
		if err != nil {
			return err
		}
		for i := range changes {
			changes[i].AfterDigest = after.Tables[changes[i].Table].Rows[string(changes[i].Key)].Digest
		}
		receipt.Changes = changes
		receipt.PreservedDigest = partitionRepairPreserved(before, changes, state.ID)
		receipt.Proof, err = s.seal(projectPartitionRepairKind+"/receipt", receipt)
		if err != nil {
			return err
		}
		raw, err := json.Marshal(receipt)
		if err != nil {
			return err
		}
		operation := consolidationOperation{IdempotencyKey: state.ID, OperationID: state.ID, PlanID: state.ID, PlanFingerprint: state.Fingerprint, Receipt: JSONRaw(raw), CompletedAt: receipt.CompletedAt}
		if err := tx.Create(&operation).Error; err != nil {
			return consolidationCollision(err)
		}
		if err := native.operation(operation); err != nil {
			return err
		}
		if err := native.verifyAfter(tx); err != nil {
			return err
		}
		if err := freshSource(); err != nil {
			return err
		}
		result = ProjectPartitionRepairResult{DryRun: dryRun, Receipt: receipt}
		if dryRun {
			return rollback
		}
		return nil
	})
	if errors.Is(err, rollback) {
		err = nil
	}
	if err != nil {
		return ProjectPartitionRepairResult{}, err
	}
	return result, nil
}

func partitionRepairSnapshotCopy(snapshot consolidationNativeSnapshot) consolidationNativeSnapshot {
	copy := consolidationNativeSnapshot{Schema: snapshot.Schema, Sequences: snapshot.Sequences, Tables: make(map[string]consolidationNativeTable, len(snapshot.Tables))}
	for name, table := range snapshot.Tables {
		entry := consolidationNativeTable{PrimaryKey: table.PrimaryKey, Rows: make(map[string]consolidationNativeRow, len(table.Rows)), Objects: map[string]JSONRaw{}}
		for key, row := range table.Rows {
			entry.Rows[key] = row
		}
		copy.Tables[name] = entry
	}
	return copy
}

func partitionRepairPreserved(snapshot consolidationNativeSnapshot, changes []projectPartitionChange, operationID string) string {
	copy := partitionRepairSnapshotCopy(snapshot)
	for _, change := range changes {
		delete(copy.Tables[change.Table].Rows, string(change.Key))
	}
	key, _ := json.Marshal(map[string]string{"idempotency_key": operationID})
	delete(copy.Tables["project_consolidation_operations"].Rows, string(key))
	return structuralDigest(copy)
}

func partitionRepairReplay(state projectPartitionRepairState, receipt ProjectPartitionRepairReceipt, actual consolidationNativeSnapshot) error {
	expected := partitionRepairSnapshotCopy(state.Source)
	for _, change := range receipt.Changes {
		entry, ok := expected.Tables[change.Table]
		if !ok {
			return consolidationHold("REPLAY_CONFLICT")
		}
		if entry.Rows[string(change.Key)].Digest != change.BeforeDigest {
			return consolidationHold("REPLAY_CONFLICT")
		}
		entry.Rows[string(change.Key)] = consolidationNativeRow{Digest: change.AfterDigest}
	}
	current := partitionRepairSnapshotCopy(actual)
	key, _ := json.Marshal(map[string]string{"idempotency_key": state.ID})
	delete(current.Tables["project_consolidation_operations"].Rows, string(key))
	if err := consolidationNativeCompare(expected, current, false); err != nil {
		return err
	}
	if partitionRepairPreserved(actual, receipt.Changes, state.ID) != receipt.PreservedDigest {
		return consolidationHold("CONSERVATION_FAILED")
	}
	return nil
}
