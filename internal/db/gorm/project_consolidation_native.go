package gorm

import (
	"context"
	"database/sql"
	"encoding/json"
	"reflect"
	"sort"
	"time"

	"github.com/google/uuid"
	"github.com/lib/pq"
	gormlib "gorm.io/gorm"
)

// ConsolidationNativeRequest consumes an ordinary authenticated Preflight plan.
// It is a local maintenance boundary, not an HTTP-selectable verification mode.
type ConsolidationNativeRequest struct {
	PlanReference   string
	PlanFingerprint string
	ExpectedPlanID  string
}

type ConsolidationNativeResult struct {
	PlanID         string               `json:"plan_id"`
	TargetIdentity string               `json:"target_identity"`
	DryRun         bool                 `json:"dry_run"`
	Replay         bool                 `json:"replay"`
	Receipt        ConsolidationReceipt `json:"receipt"`
}

func (s *ProjectConsolidationStore) DryRunNative(ctx context.Context, request ConsolidationNativeRequest) (ConsolidationNativeResult, error) {
	return s.applyNative(ctx, request, true)
}

func (s *ProjectConsolidationStore) ApplyNative(ctx context.Context, request ConsolidationNativeRequest) (ConsolidationNativeResult, error) {
	return s.applyNative(ctx, request, false)
}

func (s *ProjectConsolidationStore) nativePlan(request ConsolidationNativeRequest, commit bool) (consolidationPlanState, error) {
	state, err := s.plan(request.PlanReference, request.PlanFingerprint)
	if err != nil {
		return state, err
	}
	id, err := uuid.Parse(state.ID)
	if err != nil || id == uuid.Nil || id.String() != state.ID || uuid.NewSHA1(uuid.NameSpaceOID, []byte(state.Fingerprint)).String() != state.ID {
		return state, consolidationHold("STALE_PLAN")
	}
	if commit && request.ExpectedPlanID == "" {
		return state, consolidationHold("PLAN_ID_REQUIRED")
	}
	if request.ExpectedPlanID != "" && request.ExpectedPlanID != state.ID {
		return state, consolidationHold("STALE_PLAN")
	}
	return state, nil
}

func (s *ProjectConsolidationStore) applyNative(ctx context.Context, request ConsolidationNativeRequest, dryRun bool) (ConsolidationNativeResult, error) {
	if err := s.available(); err != nil {
		return ConsolidationNativeResult{}, err
	}
	if s.restoreDB == nil {
		return ConsolidationNativeResult{}, consolidationHold("ISOLATED_TARGET_MISSING")
	}
	state, err := s.nativePlan(request, !dryRun)
	if err != nil {
		return ConsolidationNativeResult{}, err
	}
	return s.applyPrepared(ctx, ConsolidationApplyRequest{PlanReference: request.PlanReference, PlanFingerprint: request.PlanFingerprint, IdempotencyKey: state.ID}, state, dryRun, true)
}

type consolidationNativeRow struct {
	Digest  string
	Version string
}

type consolidationNativeTable struct {
	PrimaryKey []string
	Rows       map[string]consolidationNativeRow
	Objects    map[string]JSONRaw
}

type consolidationNativeSnapshot struct {
	Schema    string
	Sequences []consolidationSequence
	Tables    map[string]consolidationNativeTable
}

// Whole row bodies stay in PostgreSQL except the sparse declared edits. Native
// checks read every row digest; no frame table, archive or body copy is created.
func consolidationNativeRead(tx *gormlib.DB, wanted map[string][]string) (consolidationNativeSnapshot, error) {
	result := consolidationNativeSnapshot{Tables: map[string]consolidationNativeTable{}}
	if err := tx.Exec("SET LOCAL TimeZone='UTC'; SET LOCAL extra_float_digits=3; SET LOCAL row_security=off").Error; err != nil {
		return result, err
	}
	var unsupported int64
	if err := tx.Raw("SELECT (SELECT count(*) FROM pg_largeobject_metadata)+(SELECT count(*) FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname=current_schema() AND c.relkind IN ('f','m'))").Scan(&unsupported).Error; err != nil {
		return result, err
	}
	if unsupported != 0 {
		return result, consolidationHold("INVENTORY_INCOMPLETE")
	}
	var err error
	result.Schema, _, _, err = consolidationSchema(tx)
	if err != nil {
		return result, err
	}
	result.Sequences, err = consolidationSequences(tx)
	if err != nil {
		return result, err
	}
	tables, err := consolidationDataTables(tx)
	if err != nil {
		return result, err
	}
	for _, table := range tables {
		entry := consolidationNativeTable{PrimaryKey: table.PrimaryKey, Rows: map[string]consolidationNativeRow{}, Objects: map[string]JSONRaw{}}
		key := consolidationKeySQL(table, "t")
		query := "SELECT " + key + "::text,encode(sha256(convert_to(to_jsonb(t)::text,'UTF8')),'hex'),t.xmin::text,CASE WHEN " + key + "=ANY(?::jsonb[]) THEN convert_to(to_jsonb(t)::text,'UTF8') ELSE NULL END FROM " + quoted(table.Name) + " t ORDER BY " + consolidationOrderSQL(table, "t")
		keys := wanted[table.Name]
		if keys == nil {
			keys = []string{}
		}
		rows, err := tx.Raw(query, pq.Array(keys)).Rows()
		if err != nil {
			return result, err
		}
		for rows.Next() {
			var raw, body []byte
			var row consolidationNativeRow
			if err := rows.Scan(&raw, &row.Digest, &row.Version, &body); err != nil {
				rows.Close()
				return result, err
			}
			canonical, err := consolidationCanonicalKey(raw)
			if err != nil {
				rows.Close()
				return result, err
			}
			address := string(canonical)
			if _, duplicate := entry.Rows[address]; duplicate {
				rows.Close()
				return result, consolidationHold("UNSTABLE_NATIVE_KEY")
			}
			entry.Rows[address] = row
			if body != nil {
				entry.Objects[address] = body
			}
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return result, err
		}
		result.Tables[table.Name] = entry
	}
	return result, nil
}

func consolidationNativeCompareTable(expected, actual consolidationNativeTable, versions bool) error {
	if !reflect.DeepEqual(expected.PrimaryKey, actual.PrimaryKey) || len(expected.Rows) != len(actual.Rows) {
		return consolidationHold("CONSERVATION_FAILED")
	}
	if len(expected.PrimaryKey) == 0 && !versions {
		counts := map[string]int64{}
		for _, row := range expected.Rows {
			counts[row.Digest]++
		}
		for _, row := range actual.Rows {
			counts[row.Digest]--
			if counts[row.Digest] < 0 {
				return consolidationHold("CONSERVATION_FAILED")
			}
		}
		for _, count := range counts {
			if count != 0 {
				return consolidationHold("CONSERVATION_FAILED")
			}
		}
		return nil
	}
	for key, row := range expected.Rows {
		other, ok := actual.Rows[key]
		if !ok || row.Digest != other.Digest || (versions && row.Version != other.Version) {
			return consolidationHold("CONSERVATION_FAILED")
		}
	}
	return nil
}

func consolidationNativeCompare(expected, actual consolidationNativeSnapshot, versions bool) error {
	if expected.Schema != actual.Schema || !reflect.DeepEqual(expected.Sequences, actual.Sequences) || len(expected.Tables) != len(actual.Tables) {
		return consolidationHold("CONSERVATION_FAILED")
	}
	for name, table := range expected.Tables {
		other, ok := actual.Tables[name]
		if !ok {
			return consolidationHold("CONSERVATION_FAILED")
		}
		if err := consolidationNativeCompareTable(table, other, versions); err != nil {
			return err
		}
	}
	return nil
}

func consolidationNativeEdit(raw JSONRaw, primaryKey []string, edits []consolidationRowEdit) (JSONRaw, JSONRaw, error) {
	for _, edit := range edits {
		row, mapping := edit.Row, edit.Mapping
		if mapping.ProjectID == "" || row.Global || row.Column == "" || !consolidationMutable(row.Kind) {
			continue
		}
		if len(primaryKey) == 0 {
			return nil, nil, consolidationHold("UNSTABLE_NATIVE_KEY")
		}
		changed, err := consolidationReplaceOccurrence(json.RawMessage(raw), append([]string{row.Column}, row.Path...), row.Selector, mapping.ProjectID)
		raw = JSONRaw(changed)
		if err != nil {
			return nil, nil, err
		}
	}
	var object map[string]json.RawMessage
	if json.Unmarshal(raw, &object) != nil {
		return nil, nil, consolidationHold("INVENTORY_INCOMPLETE")
	}
	key := make(map[string]json.RawMessage, len(primaryKey))
	for _, column := range primaryKey {
		value, ok := object[column]
		if !ok {
			return nil, nil, consolidationHold("UNSTABLE_NATIVE_KEY")
		}
		key[column] = value
	}
	encoded, err := json.Marshal(key)
	return raw, JSONRaw(encoded), err
}

type consolidationNativeChange struct {
	Key    json.RawMessage `json:"key"`
	Object json.RawMessage `json:"object"`
}

type consolidationNativeState struct {
	source      consolidationNativeSnapshot
	target      consolidationNativeSnapshot
	expected    consolidationNativeSnapshot
	pending     map[string]map[string]JSONRaw
	targetKeys  map[string]JSONRaw
	afterKeys   map[string]JSONRaw
	changedKeys map[string]bool
	projects    map[string]bool
}

func (s *ProjectConsolidationStore) prepareNative(ctx context.Context, state consolidationPlanState, tx *gormlib.DB, replay bool) (*consolidationNativeState, error) {
	wanted := map[string][]string{}
	edits := map[string]map[string][]consolidationRowEdit{}
	projects := map[string]bool{}
	for i, row := range state.Census.Rows {
		mapping := state.Mapping[i]
		if mapping.ProjectID != "" {
			projects[mapping.ProjectID] = true
		}
		if mapping.ProjectID == "" || row.Global || row.Column == "" || !consolidationMutable(row.Kind) {
			continue
		}
		if edits[row.Table] == nil {
			edits[row.Table] = map[string][]consolidationRowEdit{}
		}
		address := string(row.Key)
		if len(edits[row.Table][address]) == 0 {
			wanted[row.Table] = append(wanted[row.Table], address)
		}
		edits[row.Table][address] = append(edits[row.Table][address], consolidationRowEdit{Row: row, Mapping: mapping})
	}
	native := &consolidationNativeState{pending: map[string]map[string]JSONRaw{}, targetKeys: map[string]JSONRaw{}, afterKeys: map[string]JSONRaw{}, changedKeys: map[string]bool{}, projects: projects}
	err := s.db.WithContext(ctx).Transaction(func(source *gormlib.DB) error {
		if err := s.fresh(source, state.Census); err != nil {
			return err
		}
		var err error
		native.source, err = consolidationNativeRead(source, wanted)
		return err
	}, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return nil, err
	}
	if native.source.Schema != state.Census.Schema {
		return nil, consolidationHold("STALE_PLAN")
	}
	projectKeys := []string{}
	for id := range projects {
		key, _ := json.Marshal(map[string]string{"id": id})
		projectKeys = append(projectKeys, string(key))
	}
	native.target, err = consolidationNativeRead(tx, map[string][]string{"projects": projectKeys})
	if err != nil {
		return nil, err
	}
	if !replay {
		if err := consolidationNativeCompare(native.source, native.target, false); err != nil {
			return nil, err
		}
	}
	native.expected = consolidationNativeSnapshot{Schema: native.source.Schema, Sequences: native.source.Sequences, Tables: map[string]consolidationNativeTable{}}
	for name, table := range native.source.Tables {
		rows := make(map[string]consolidationNativeRow, len(table.Rows))
		for key, row := range table.Rows {
			rows[key] = row
		}
		native.expected.Tables[name] = consolidationNativeTable{PrimaryKey: table.PrimaryKey, Rows: rows, Objects: table.Objects}
		if len(table.PrimaryKey) == 0 {
			actual, ok := native.target.Tables[name]
			if !ok || consolidationNativeCompareTable(table, actual, false) != nil {
				return nil, consolidationHold("CONSERVATION_FAILED")
			}
			buckets := map[string][]string{}
			for key, row := range actual.Rows {
				buckets[row.Digest] = append(buckets[row.Digest], key)
			}
			for digest := range buckets {
				sort.Strings(buckets[digest])
			}
			keys := make([]string, 0, len(table.Rows))
			for key := range table.Rows {
				keys = append(keys, key)
			}
			sort.Strings(keys)
			for _, key := range keys {
				digest := table.Rows[key].Digest
				bound := buckets[digest][0]
				buckets[digest] = buckets[digest][1:]
				native.targetKeys[consolidationObjectKey(name, []byte(key))] = JSONRaw(bound)
			}
		}
	}
	for tableName, byKey := range edits {
		table, ok := native.source.Tables[tableName]
		if !ok {
			return nil, consolidationHold("INVENTORY_INCOMPLETE")
		}
		for key, changes := range byKey {
			body, ok := table.Objects[key]
			if !ok {
				return nil, consolidationHold("CONSERVATION_FAILED")
			}
			object, afterKey, err := consolidationNativeEdit(body, table.PrimaryKey, changes)
			if err != nil {
				return nil, err
			}
			address := consolidationObjectKey(tableName, []byte(key))
			native.afterKeys[address] = afterKey
			native.changedKeys[address] = string(afterKey) != key
			if _, collision := native.expected.Tables[tableName].Rows[string(afterKey)]; collision && string(afterKey) != key {
				return nil, consolidationHold("COLLISION")
			}
			delete(native.expected.Tables[tableName].Rows, key)
			if err := native.expect(tableName, afterKey, object); err != nil {
				return nil, err
			}
		}
	}
	if err := native.expectMetadata(state); err != nil {
		return nil, err
	}
	if replay {
		for id := range projects {
			key, _ := json.Marshal(map[string]string{"id": id})
			if _, existed := native.source.Tables["projects"].Rows[string(key)]; existed {
				continue
			}
			object := native.target.Tables["projects"].Objects[string(key)]
			if err := consolidationNativeCanonicalProject(object, id); err != nil {
				return nil, err
			}
			if err := native.expect("projects", JSONRaw(key), object); err != nil {
				return nil, err
			}
		}
	}
	return native, nil
}

func (n *consolidationNativeState) expect(table string, key JSONRaw, object JSONRaw) error {
	if _, ok := n.expected.Tables[table]; !ok || len(key) == 0 || len(object) == 0 {
		return consolidationHold("INVENTORY_INCOMPLETE")
	}
	if n.pending[table] == nil {
		n.pending[table] = map[string]JSONRaw{}
	}
	address := string(key)
	if prior, duplicate := n.pending[table][address]; duplicate && string(prior) != string(object) {
		return consolidationHold("COLLISION")
	}
	n.pending[table][address] = object
	return nil
}

func (n *consolidationNativeState) expectMetadata(state consolidationPlanState) error {
	for i, row := range state.Census.Rows {
		if row.Global || consolidationMetadataTable(row.Table) {
			continue
		}
		address := consolidationObjectKey(row.Table, row.Key)
		key := row.Key
		if target, ok := n.targetKeys[address]; ok {
			key = target
		}
		if after, ok := n.afterKeys[address]; ok {
			key = after
		}
		member, _, err := consolidationPlannedMembership(state, row, state.Mapping[i], key)
		if err != nil {
			return err
		}
		raw, err := consolidationMembershipObject(member)
		if err != nil {
			return err
		}
		ref, _ := json.Marshal(map[string]string{"reference": member.Reference})
		if err := n.expect("project_consolidation_memberships", JSONRaw(ref), raw); err != nil {
			return err
		}
	}
	if state.retained != nil {
		for address, members := range state.retained.objects {
			key, moved := n.targetKeys[address]
			if after, ok := n.afterKeys[address]; ok {
				key, moved = after, true
			}
			if !moved {
				continue
			}
			for _, member := range members {
				ref, _ := json.Marshal(map[string]string{"reference": member.Reference})
				if _, planned := n.pending["project_consolidation_memberships"][string(ref)]; planned {
					continue
				}
				member.RowKey = key
				raw, err := consolidationMembershipObject(member)
				if err != nil {
					return err
				}
				if err := n.expect("project_consolidation_memberships", JSONRaw(ref), raw); err != nil {
					return err
				}
			}
		}
	}
	basic, sessions := consolidationRedirectBindings(state)
	for _, redirect := range basic {
		if err := n.expectRedirect(redirect.Selector, "", redirect.Target, state.Fingerprint); err != nil {
			return err
		}
	}
	for _, binding := range sessions {
		var session struct {
			Claude string  `json:"claude_session_id"`
			SDK    *string `json:"sdk_session_id"`
		}
		if json.Unmarshal(n.source.Tables[binding.Row.Table].Objects[string(binding.Row.Key)], &session) != nil {
			return consolidationHold("INVENTORY_INCOMPLETE")
		}
		if session.Claude != "" {
			if err := n.expectRedirect(binding.Row.Selector, session.Claude, binding.Target, state.Fingerprint); err != nil {
				return err
			}
		}
		if session.SDK != nil && *session.SDK != "" {
			if err := n.expectRedirect(binding.Row.Selector, *session.SDK, binding.Target, state.Fingerprint); err != nil {
				return err
			}
		}
	}
	return nil
}

func (n *consolidationNativeState) expectRedirect(selector, session, target, fingerprint string) error {
	key, _ := json.Marshal(map[string]string{"selector": selector, "session_id": session})
	if _, existed := n.source.Tables["project_consolidation_redirects"].Rows[string(key)]; existed {
		return nil
	}
	raw, err := json.Marshal(map[string]string{"selector": selector, "session_id": session, "project_id": target, "plan_fingerprint": fingerprint})
	if err != nil {
		return err
	}
	return n.expect("project_consolidation_redirects", JSONRaw(key), raw)
}

func consolidationProjectObject(project Project) (JSONRaw, error) {
	raw, err := json.Marshal(map[string]any{"id": project.ID, "created_at": project.CreatedAt, "git_remote": nil, "relative_path": nil, "display_name": nil, "project_key": nil, "anchor_project_id": nil, "identity_scope": nil, "identity_status": nil, "removed_at": nil, "last_heartbeat": nil, "legacy_ids": nil})
	return JSONRaw(raw), err
}

func (n *consolidationNativeState) createdProject(project Project) error {
	raw, err := consolidationProjectObject(project)
	if err != nil {
		return err
	}
	key, _ := json.Marshal(map[string]string{"id": project.ID})
	return n.expect("projects", JSONRaw(key), raw)
}

func consolidationNativeCanonicalProject(raw JSONRaw, id string) error {
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil || len(fields) != 12 {
		return consolidationHold("CONSERVATION_FAILED")
	}
	var actual string
	var created time.Time
	if json.Unmarshal(fields["id"], &actual) != nil || actual != id || json.Unmarshal(fields["created_at"], &created) != nil || created.IsZero() || created.After(time.Now().Add(5*time.Second)) {
		return consolidationHold("CONSERVATION_FAILED")
	}
	for _, field := range []string{"git_remote", "relative_path", "display_name", "project_key", "anchor_project_id", "identity_scope", "identity_status", "removed_at", "last_heartbeat", "legacy_ids"} {
		if string(fields[field]) != "null" {
			return consolidationHold("CONSERVATION_FAILED")
		}
	}
	return nil
}

func consolidationOperationObject(operation consolidationOperation) (JSONRaw, error) {
	raw, err := json.Marshal(struct {
		Key         string          `json:"idempotency_key"`
		ID          string          `json:"operation_id"`
		PlanID      string          `json:"plan_id"`
		Fingerprint string          `json:"plan_fingerprint"`
		Receipt     json.RawMessage `json:"receipt"`
		Completed   time.Time       `json:"completed_at"`
	}{operation.IdempotencyKey, operation.OperationID, operation.PlanID, operation.PlanFingerprint, json.RawMessage(operation.Receipt), operation.CompletedAt})
	if err != nil {
		return nil, err
	}
	return JSONRaw(raw), nil
}

func (n *consolidationNativeState) operation(operation consolidationOperation) error {
	raw, err := consolidationOperationObject(operation)
	if err != nil {
		return err
	}
	key, _ := json.Marshal(map[string]string{"idempotency_key": operation.IdempotencyKey})
	return n.expect("project_consolidation_operations", JSONRaw(key), raw)
}

func (n *consolidationNativeState) verifyAfter(tx *gormlib.DB) error {
	for tableName, objects := range n.pending {
		batch := make([]consolidationNativeChange, 0, 128)
		flush := func() error {
			if len(batch) == 0 {
				return nil
			}
			raw, err := json.Marshal(batch)
			if err != nil {
				return err
			}
			var normalized []struct{ Key, Digest string }
			query := "SELECT x.key::text AS key,encode(sha256(convert_to(to_jsonb(jsonb_populate_record(NULL::" + quoted(tableName) + ",x.object))::text,'UTF8')),'hex') AS digest FROM jsonb_to_recordset(?::jsonb)x(key jsonb,object jsonb)"
			if err := tx.Raw(query, string(raw)).Scan(&normalized).Error; err != nil {
				return err
			}
			if len(normalized) != len(batch) {
				return consolidationHold("CONSERVATION_FAILED")
			}
			for _, row := range normalized {
				key, err := consolidationCanonicalKey([]byte(row.Key))
				if err != nil {
					return err
				}
				n.expected.Tables[tableName].Rows[string(key)] = consolidationNativeRow{Digest: row.Digest}
			}
			batch = batch[:0]
			return nil
		}
		for key, object := range objects {
			batch = append(batch, consolidationNativeChange{Key: json.RawMessage(key), Object: json.RawMessage(object)})
			if len(batch) == cap(batch) {
				if err := flush(); err != nil {
					return err
				}
			}
		}
		if err := flush(); err != nil {
			return err
		}
	}
	actual, err := consolidationNativeRead(tx, nil)
	if err != nil {
		return err
	}
	if err := consolidationNativeCompare(n.expected, actual, false); err != nil {
		return err
	}
	for tableName, table := range n.target.Tables {
		if len(table.PrimaryKey) == 0 {
			if err := consolidationNativeCompareTable(table, actual.Tables[tableName], true); err != nil {
				return err
			}
			continue
		}
		for key, before := range table.Rows {
			if _, declared := n.pending[tableName][key]; declared {
				continue
			}
			if _, retained := n.expected.Tables[tableName].Rows[key]; !retained {
				continue
			}
			if after, ok := actual.Tables[tableName].Rows[key]; !ok || before.Version != after.Version {
				return consolidationHold("CONSERVATION_FAILED")
			}
		}
	}
	return nil
}

func (s *ProjectConsolidationStore) nativeSourceUnchanged(ctx context.Context, native *consolidationNativeState) error {
	return s.db.WithContext(ctx).Transaction(func(source *gormlib.DB) error {
		actual, err := consolidationNativeRead(source, nil)
		if err != nil {
			return err
		}
		return consolidationNativeCompare(native.source, actual, true)
	}, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
}

func consolidationNativeReplayReceipt(state consolidationPlanState, receipt ConsolidationReceipt) error {
	operation := uuid.NewSHA1(uuid.NameSpaceOID, []byte(state.Fingerprint)).String()
	if receipt.OperationID != operation || receipt.Reference != "urn:engram:migration:receipt:"+operation || receipt.RollbackBoundary != consolidationRollbackBoundary {
		return consolidationHold("REPLAY_CONFLICT")
	}
	targets := map[string]bool{}
	counts := map[string]ConsolidationRoleCount{}
	for i, row := range state.Census.Rows {
		if state.Mapping[i].ProjectID != "" {
			targets[state.Mapping[i].ProjectID] = true
		}
		key := row.Table + "." + row.Column
		count := counts[key]
		if count.Before == 0 {
			count = ConsolidationRoleCount{Family: row.Table, Role: row.Column, Disposition: row.Kind}
		}
		count.Before++
		count.After++
		counts[key] = count
	}
	if len(targets) != len(receipt.ProjectIDs) || len(counts) != len(receipt.Roles) {
		return consolidationHold("REPLAY_CONFLICT")
	}
	for _, id := range receipt.ProjectIDs {
		if !targets[id] {
			return consolidationHold("REPLAY_CONFLICT")
		}
		delete(targets, id)
	}
	for _, count := range receipt.Roles {
		key := count.Family + "." + count.Role
		if expected, ok := counts[key]; !ok || expected != count {
			return consolidationHold("REPLAY_CONFLICT")
		}
		delete(counts, key)
	}
	return nil
}
