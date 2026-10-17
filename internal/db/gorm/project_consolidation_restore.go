package gorm

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	gormlib "gorm.io/gorm"
)

type consolidationSequence struct {
	Name      string
	LastValue int64
	IsCalled  bool
}

func consolidationSequences(tx *gormlib.DB) ([]consolidationSequence, error) {
	var names []string
	if err := tx.Raw(`SELECT sequencename FROM pg_sequences WHERE schemaname=current_schema() ORDER BY sequencename`).Scan(&names).Error; err != nil {
		return nil, err
	}
	result := make([]consolidationSequence, 0, len(names))
	for _, name := range names {
		var row consolidationSequence
		if err := tx.Raw("SELECT last_value,is_called FROM " + quoted(name)).Scan(&row).Error; err != nil {
			return nil, err
		}
		row.Name = name
		result = append(result, row)
	}
	return result, nil
}

func (s *ProjectConsolidationStore) distinctTarget(ctx context.Context) error {
	if s.restoreDB == nil {
		return consolidationHold("ISOLATED_TARGET_MISSING")
	}
	source, err := consolidationDatabaseIdentity(s.db.WithContext(ctx))
	if err != nil {
		return err
	}
	target, err := consolidationDatabaseIdentity(s.restoreDB.WithContext(ctx))
	if err != nil {
		return err
	}
	if source == target {
		return consolidationHold("TARGET_IS_SOURCE")
	}
	return nil
}

func (s *ProjectConsolidationStore) verifyCredentials(ctx context.Context, tx *gormlib.DB) error {
	var exists bool
	if err := tx.Raw(`SELECT to_regclass(quote_ident(current_schema())||'.credentials') IS NOT NULL`).Scan(&exists).Error; err != nil {
		return err
	}
	if !exists {
		return nil
	}
	reader := NewCredentialStore(&Store{DB: tx})
	var last int64
	for {
		var credential Credential
		err := tx.Where("id>?", last).Order("id").First(&credential).Error
		if errors.Is(err, gormlib.ErrRecordNotFound) {
			break
		}
		if err != nil {
			return err
		}
		last = credential.ID
		if !s.vault.MatchesFingerprint(credential.EncryptionKeyFingerprint) {
			return consolidationHold("SECRET_UNREADABLE")
		}
		if _, err := s.vault.Decrypt(credential.EncryptedSecret); err != nil {
			return consolidationHold("SECRET_UNREADABLE")
		}
		if credential.DeletedAt != nil {
			continue
		}
		read, err := reader.GetByID(ctx, credential.ID)
		if err != nil || read.ID != credential.ID || read.Project != credential.Project || read.Key != credential.Key || read.Scope != credential.Scope || read.Version != credential.Version {
			return consolidationHold("SECRET_UNREADABLE")
		}
		if _, err := s.vault.Decrypt(read.EncryptedSecret); err != nil {
			return consolidationHold("SECRET_UNREADABLE")
		}
	}
	return nil
}

const consolidationRollbackBoundary = "Do not overwrite new writes from this export: stop writers, take a fresh export, inventory post-cutover writes, then forward-compensate or restore into another empty isolated target."

func (s *ProjectConsolidationStore) execute(tx *gormlib.DB, state consolidationPlanState) (ConsolidationReceipt, error) {
	if len(state.Census.Rows) != len(state.Mapping) {
		return ConsolidationReceipt{}, consolidationHold("STALE_MAPPING")
	}
	if err := consolidationRowBuffer(tx); err != nil {
		return ConsolidationReceipt{}, err
	}
	if err := consolidationControlBuffer(tx); err != nil {
		return ConsolidationReceipt{}, err
	}
	if state.retained == nil {
		var err error
		state.retained, err = s.retainedMemberships(tx, state.Census.Rows)
		if err != nil {
			return ConsolidationReceipt{}, err
		}
	}
	byRef := make(map[string]consolidationRow, len(state.Census.Rows))
	nativeKeys := map[string]JSONRaw{}
	changedKeys := map[string]bool{}
	references := map[string][]string{}
	for _, row := range state.Census.Rows {
		byRef[row.Reference] = row
		if consolidationMetadataTable(row.Table) {
			continue
		}
		key := consolidationObjectKey(row.Table, row.Key)
		nativeKeys[key] = row.Key
		if state.native != nil {
			if restored, ok := state.native.targetKeys[key]; ok {
				nativeKeys[key] = restored
				changedKeys[key] = string(restored) != string(row.Key)
			}
		}
		if state.native == nil {
			var mapped string
			if err := tx.Raw(`SELECT COALESCE((SELECT target_key::text FROM pg_temp.engram_owner_native_rows WHERE table_name=? AND row_key=?::jsonb),'')`, row.Table, string(row.Key)).Scan(&mapped).Error; err != nil {
				return ConsolidationReceipt{}, err
			}
			if len(mapped) != 0 {
				canonical, err := consolidationCanonicalKey([]byte(mapped))
				if err != nil {
					return ConsolidationReceipt{}, err
				}
				nativeKeys[key] = canonical
				changedKeys[key] = string(canonical) != string(row.Key)
			}
		}
		references[key] = append(references[key], row.Reference)
	}
	targets := map[string]bool{}
	for _, m := range state.Mapping {
		if m.ProjectID != "" {
			targets[m.ProjectID] = true
		}
	}
	// Canonical projects use projects.id itself. Historical registry rows and
	// their FK-constrained old UUID evidence survive unchanged as provenance.
	for target := range targets {
		var count int64
		if err := tx.Table("projects").Where("id = ?", target).Count(&count).Error; err != nil {
			return ConsolidationReceipt{}, err
		}
		if count == 0 {
			project := Project{ID: target}
			project.CreatedAt = tx.NowFunc().UTC().Truncate(time.Microsecond)
			if err := tx.Create(&project).Error; err != nil {
				return ConsolidationReceipt{}, consolidationCollision(err)
			}
			if err := consolidationExpectProject(tx, project); err != nil {
				return ConsolidationReceipt{}, err
			}
			if state.native != nil {
				if err := state.native.createdProject(project); err != nil {
					return ConsolidationReceipt{}, err
				}
			}
		}
	}
	restoreGuards, err := consolidationSuspendWriteGuards(tx)
	if err != nil {
		return ConsolidationReceipt{}, err
	}
	counts := map[string]*ConsolidationRoleCount{}
	for _, m := range state.Mapping {
		row, ok := byRef[m.Reference]
		if !ok {
			return ConsolidationReceipt{}, consolidationHold("STALE_PLAN")
		}
		key := row.Table + "." + row.Column
		if counts[key] == nil {
			counts[key] = &ConsolidationRoleCount{Family: row.Table, Role: row.Column, Disposition: row.Kind}
		}
		counts[key].Before++
		if row.Global || consolidationMetadataTable(row.Table) {
			counts[key].After++
			continue
		}
		originalKey := row.Key
		objectKey := consolidationObjectKey(row.Table, originalKey)
		keyJSON := nativeKeys[objectKey]
		if m.ProjectID != "" && row.Column != "" && consolidationMutable(row.Kind) {
			row.Key = keyJSON
			var err error
			keyJSON, err = consolidationMutation(tx, row, m.ProjectID)
			if err != nil {
				return ConsolidationReceipt{}, err
			}
			if string(keyJSON) != string(nativeKeys[objectKey]) {
				changedKeys[objectKey] = true
				nativeKeys[objectKey] = keyJSON
			}
		}
		membership, retained, err := consolidationPlannedMembership(state, byRef[m.Reference], m, keyJSON)
		if err != nil {
			return ConsolidationReceipt{}, err
		}
		if err := consolidationWriteMembership(tx, membership, retained); err != nil {
			return ConsolidationReceipt{}, err
		}
		counts[key].After++
	}
	for objectKey := range changedKeys {
		for _, member := range state.retained.objects[objectKey] {
			member.RowKey = nativeKeys[objectKey]
			// A resolved occurrence may have acquired a target in the same plan.
			var actual ConsolidationMembership
			if err := tx.Where("reference=?", member.Reference).First(&actual).Error; err != nil {
				return ConsolidationReceipt{}, err
			}
			member.ProjectID = actual.ProjectID
			if err := consolidationWriteMembership(tx, member, true); err != nil {
				return ConsolidationReceipt{}, err
			}
		}
		for _, ref := range references[objectKey] {
			var member ConsolidationMembership
			if err := tx.Where("reference=?", ref).First(&member).Error; errors.Is(err, gormlib.ErrRecordNotFound) {
				continue
			} else if err != nil {
				return ConsolidationReceipt{}, err
			}
			member.RowKey = nativeKeys[objectKey]
			if err := consolidationWriteMembership(tx, member, true); err != nil {
				return ConsolidationReceipt{}, err
			}
		}
	}
	if err := s.installRedirects(tx, state); err != nil {
		return ConsolidationReceipt{}, err
	}
	if err := tx.Exec(`SET CONSTRAINTS ALL IMMEDIATE`).Error; err != nil {
		return ConsolidationReceipt{}, consolidationCollision(err)
	}
	if err := restoreGuards(); err != nil {
		return ConsolidationReceipt{}, err
	}
	if err := s.verifyCredentials(tx.Statement.Context, tx); err != nil {
		return ConsolidationReceipt{}, err
	}
	schema, _, _, err := consolidationSchema(tx)
	if err != nil {
		return ConsolidationReceipt{}, err
	}
	if schema != state.Census.Schema {
		return ConsolidationReceipt{}, consolidationHold("SCHEMA_MISMATCH")
	}
	operation := uuid.NewSHA1(uuid.NameSpaceOID, []byte(state.Fingerprint)).String()
	receipt := ConsolidationReceipt{OperationID: operation, Reference: "urn:engram:migration:receipt:" + operation, RollbackBoundary: consolidationRollbackBoundary}
	for target := range targets {
		receipt.ProjectIDs = append(receipt.ProjectIDs, target)
	}
	for _, count := range counts {
		receipt.Roles = append(receipt.Roles, *count)
	}
	sort.Strings(receipt.ProjectIDs)
	sort.Slice(receipt.Roles, func(i, j int) bool {
		if receipt.Roles[i].Family != receipt.Roles[j].Family {
			return receipt.Roles[i].Family < receipt.Roles[j].Family
		}
		return receipt.Roles[i].Role < receipt.Roles[j].Role
	})
	return receipt, nil
}

func consolidationMembershipFor(state consolidationPlanState, row consolidationRow, mapping ConsolidationMapping, key JSONRaw) ConsolidationMembership {
	path, _ := json.Marshal(append([]string{row.Column}, row.Path...))
	var target *string
	if mapping.ProjectID != "" {
		value := mapping.ProjectID
		target = &value
	}
	return ConsolidationMembership{Reference: row.Reference, UnresolvedReference: mapping.UnresolvedReference, Table: row.Table, RowKey: key, SourceRowKey: row.Key, FieldPath: JSONRaw(path), SelectorReference: row.SelectorReference, ProjectID: target, Reason: mapping.Reason, PlanFingerprint: state.Fingerprint, NativeVersion: row.NativeVersion, SourceSnapshot: state.Census.NativeSnapshot}
}

func consolidationWriteMembership(tx *gormlib.DB, member ConsolidationMembership, retained bool) error {
	if retained {
		var prior ConsolidationMembership
		if err := tx.Where("reference=?", member.Reference).First(&prior).Error; err != nil {
			return err
		}
		if err := consolidationNormalizeMembership(&prior); err != nil {
			return err
		}
		if !reflect.DeepEqual(prior.RowKey, member.RowKey) || !reflect.DeepEqual(prior.ProjectID, member.ProjectID) {
			if err := tx.Model(&ConsolidationMembership{}).Where("reference=?", member.Reference).Updates(map[string]any{"row_key": member.RowKey, "project_id": member.ProjectID}).Error; err != nil {
				return consolidationCollision(err)
			}
		}
	} else if err := tx.Create(&member).Error; err != nil {
		return consolidationCollision(err)
	}
	object, err := consolidationMembershipObject(member)
	if err != nil {
		return err
	}
	key, _ := json.Marshal(map[string]string{"reference": member.Reference})
	return consolidationExpectControl(tx, "project_consolidation_memberships", JSONRaw(key), object)
}

func consolidationReplaceOccurrence(raw json.RawMessage, path []string, old, target string) (json.RawMessage, error) {
	var value any
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.UseNumber()
	if decoder.Decode(&value) != nil {
		return nil, consolidationHold("INVENTORY_INCOMPLETE")
	}
	var replace func(any, []string) (any, error)
	replace = func(value any, path []string) (any, error) {
		if len(path) == 0 {
			if actual, ok := value.(string); !ok || actual != old {
				return nil, consolidationHold("STALE_MAPPING")
			}
			return target, nil
		}
		switch typed := value.(type) {
		case map[string]any:
			child, ok := typed[path[0]]
			if !ok {
				return nil, consolidationHold("STALE_MAPPING")
			}
			moved, err := replace(child, path[1:])
			if err != nil {
				return nil, err
			}
			typed[path[0]] = moved
			return typed, nil
		case []any:
			i, err := strconv.Atoi(path[0])
			if err != nil || i < 0 || i >= len(typed) {
				return nil, consolidationHold("STALE_MAPPING")
			}
			moved, err := replace(typed[i], path[1:])
			if err != nil {
				return nil, err
			}
			typed[i] = moved
			return typed, nil
		default:
			return nil, consolidationHold("STALE_MAPPING")
		}
	}
	moved, err := replace(value, path)
	if err != nil {
		return nil, err
	}
	return json.Marshal(moved)
}

type consolidationRedirectBinding struct {
	Selector string
	Target   string
}

type consolidationSessionRedirectBinding struct {
	Row    consolidationRow
	Target string
}

func consolidationRedirectBindings(state consolidationPlanState) ([]consolidationRedirectBinding, []consolidationSessionRedirectBinding) {
	type bucket struct {
		Target string
		Safe   bool
		Seen   bool
	}
	buckets := map[string]bucket{}
	var sessions []consolidationSessionRedirectBinding
	for i, row := range state.Census.Rows {
		if row.Table == "sdk_sessions" && row.Column == "project" && state.Mapping[i].ProjectID != "" {
			sessions = append(sessions, consolidationSessionRedirectBinding{Row: row, Target: state.Mapping[i].ProjectID})
		}
		if row.Selector == "" || row.Global || row.Kind == "locator" || row.Kind == "unknown" || row.Kind == "historical_identity" {
			continue
		}
		mapping := state.Mapping[i]
		b := buckets[row.Selector]
		if !b.Seen {
			b = bucket{Target: mapping.ProjectID, Safe: mapping.ProjectID != "", Seen: true}
		} else if mapping.ProjectID == "" || mapping.ProjectID != b.Target {
			b.Safe = false
		}
		buckets[row.Selector] = b
	}
	var basic []consolidationRedirectBinding
	for selector, b := range buckets {
		if b.Safe {
			basic = append(basic, consolidationRedirectBinding{Selector: selector, Target: b.Target})
		}
	}
	return basic, sessions
}

func (s *ProjectConsolidationStore) installRedirects(tx *gormlib.DB, state consolidationPlanState) error {
	basic, bindings := consolidationRedirectBindings(state)
	for _, binding := range basic {
		if err := consolidationRedirect(tx, binding.Selector, "", binding.Target, state.Fingerprint); err != nil {
			return err
		}
	}
	for _, binding := range bindings {
		key := binding.Row.Key
		if state.native != nil {
			address := consolidationObjectKey(binding.Row.Table, key)
			if restored, ok := state.native.targetKeys[address]; ok {
				key = restored
			}
			if changed, ok := state.native.afterKeys[address]; ok {
				key = changed
			}
		}
		var sessions []SDKSession
		if err := tx.Raw("SELECT * FROM sdk_sessions t WHERE to_jsonb(t) @> ?::jsonb", string(key)).Scan(&sessions).Error; err != nil {
			return err
		}
		for _, session := range sessions {
			ids := []string{session.ClaudeSessionID}
			if session.SDKSessionID.Valid {
				ids = append(ids, session.SDKSessionID.String)
			}
			for _, id := range ids {
				if id != "" {
					if err := consolidationRedirect(tx, binding.Row.Selector, id, binding.Target, state.Fingerprint); err != nil {
						return err
					}
				}
			}
		}
	}
	return nil
}

func consolidationRedirect(tx *gormlib.DB, selector, session, target, fingerprint string) error {
	var prior []string
	if err := tx.Raw(`SELECT project_id::text FROM project_consolidation_redirects WHERE selector=? AND session_id=?`, selector, session).Scan(&prior).Error; err != nil {
		return err
	}
	if len(prior) > 0 {
		if len(prior) != 1 || prior[0] != target {
			return consolidationHold("CONFLICTING_MEMBERSHIP")
		}
		return nil
	}
	if err := tx.Exec(`INSERT INTO project_consolidation_redirects(selector,session_id,project_id,plan_fingerprint) VALUES(?,?,?::uuid,?)`, selector, session, target, fingerprint).Error; err != nil {
		return consolidationCollision(err)
	}
	var buffered bool
	if err := tx.Raw(`SELECT to_regclass('pg_temp.engram_owner_controls') IS NOT NULL`).Scan(&buffered).Error; err != nil || !buffered {
		return err
	}
	key, _ := json.Marshal(map[string]string{"selector": selector, "session_id": session})
	object, _ := json.Marshal(map[string]string{"selector": selector, "session_id": session, "project_id": target, "plan_fingerprint": fingerprint})
	return consolidationExpectControl(tx, "project_consolidation_redirects", JSONRaw(key), JSONRaw(object))
}

// Only the locked, isolated restore/apply transactions call this helper. Their
// rollback restores both historical data and transactional trigger DDL on error.
func consolidationSuspendWriteGuards(tx *gormlib.DB) (func() error, error) {
	var guards []struct{ SchemaName, TableName, Mode string }
	if err := tx.Raw(`SELECT n.nspname AS schema_name,c.relname AS table_name,t.tgenabled::text AS mode
		FROM pg_trigger t JOIN pg_class c ON c.oid=t.tgrelid JOIN pg_namespace n ON n.oid=c.relnamespace
		WHERE n.nspname=current_schema() AND t.tgname='engram_consolidation_scope_write' AND NOT t.tgisinternal
		AND t.tgfoid=to_regprocedure(quote_ident(current_schema())||'.engram_consolidation_write_guard()')
		AND t.tgenabled<>'D' ORDER BY c.relname`).Scan(&guards).Error; err != nil {
		return nil, err
	}
	for _, guard := range guards {
		if err := tx.Exec("ALTER TABLE " + quoted(guard.SchemaName) + "." + quoted(guard.TableName) + " DISABLE TRIGGER engram_consolidation_scope_write").Error; err != nil {
			return nil, err
		}
	}
	return func() error {
		for _, guard := range guards {
			mode := "ENABLE"
			switch guard.Mode {
			case "R":
				mode = "ENABLE REPLICA"
			case "A":
				mode = "ENABLE ALWAYS"
			}
			if err := tx.Exec("ALTER TABLE " + quoted(guard.SchemaName) + "." + quoted(guard.TableName) + " " + mode + " TRIGGER engram_consolidation_scope_write").Error; err != nil {
				return err
			}
		}
		return nil
	}, nil
}

type consolidationRetainedBindings struct {
	occurrences map[string]ConsolidationMembership
	objects     map[string][]ConsolidationMembership
}

func consolidationNormalizeMembership(member *ConsolidationMembership) error {
	var err error
	member.RowKey, err = consolidationCanonicalKey(member.RowKey)
	if err != nil {
		return consolidationHold("STALE_MAPPING")
	}
	member.SourceRowKey, err = consolidationCanonicalKey(member.SourceRowKey)
	if err != nil {
		return consolidationHold("STALE_MAPPING")
	}
	var path []string
	if json.Unmarshal(member.FieldPath, &path) != nil || len(path) == 0 {
		return consolidationHold("STALE_MAPPING")
	}
	member.FieldPath, err = json.Marshal(path)
	return err
}

func consolidationOccurrenceKey(table string, key JSONRaw, path JSONRaw) string {
	return consolidationObjectKey(table, key) + ":" + string(path)
}

func (s *ProjectConsolidationStore) retainedMemberships(tx *gormlib.DB, rows []consolidationRow) (*consolidationRetainedBindings, error) {
	type object struct {
		Table string          `json:"table_name"`
		Key   json.RawMessage `json:"row_key"`
	}
	bindings := &consolidationRetainedBindings{occurrences: map[string]ConsolidationMembership{}, objects: map[string][]ConsolidationMembership{}}
	seen := map[string]bool{}
	var objects []object
	for _, row := range rows {
		if consolidationMetadataTable(row.Table) {
			continue
		}
		key := consolidationObjectKey(row.Table, row.Key)
		if !seen[key] {
			objects = append(objects, object{row.Table, json.RawMessage(row.Key)})
			seen[key] = true
		}
	}
	if len(objects) == 0 {
		return bindings, nil
	}
	raw, err := json.Marshal(objects)
	if err != nil {
		return nil, err
	}
	var retained []ConsolidationMembership
	if err := tx.Raw(`SELECT m.* FROM project_consolidation_memberships m
	 JOIN jsonb_to_recordset(?::jsonb) x(table_name text,row_key jsonb)
	 ON m.table_name=x.table_name AND m.row_key=x.row_key ORDER BY m.reference`, string(raw)).Scan(&retained).Error; err != nil {
		return nil, err
	}
	for _, member := range retained {
		if err := consolidationNormalizeMembership(&member); err != nil {
			return nil, err
		}
		var path []string
		_ = json.Unmarshal(member.FieldPath, &path)
		var nested []string
		if len(path) > 1 {
			nested = path[1:]
		}
		ref := s.opaque("row", struct {
			Table, Column string
			Key           JSONRaw
			Path          []string
		}{member.Table, path[0], member.SourceRowKey, nested})
		if ref != member.Reference || member.PlanFingerprint == "" || member.NativeVersion == "" || member.SourceSnapshot == "" {
			return nil, consolidationHold("STALE_MAPPING")
		}
		if member.ProjectID == nil {
			if _, _, err := s.unresolvedMembershipMetadata(&member); err != nil {
				return nil, err
			}
		}
		occurrence := consolidationOccurrenceKey(member.Table, member.RowKey, member.FieldPath)
		if _, duplicate := bindings.occurrences[occurrence]; duplicate {
			return nil, consolidationHold("CONFLICTING_MEMBERSHIP")
		}
		bindings.occurrences[occurrence] = member
		address := consolidationObjectKey(member.Table, member.RowKey)
		bindings.objects[address] = append(bindings.objects[address], member)
	}
	return bindings, nil
}

func consolidationPlannedMembership(state consolidationPlanState, row consolidationRow, mapping ConsolidationMapping, key JSONRaw) (ConsolidationMembership, bool, error) {
	path, _ := json.Marshal(append([]string{row.Column}, row.Path...))
	if state.retained != nil {
		if prior, ok := state.retained.occurrences[consolidationOccurrenceKey(row.Table, row.Key, JSONRaw(path))]; ok {
			if mapping.ProjectID != "" && prior.ProjectID != nil && *prior.ProjectID != mapping.ProjectID {
				return ConsolidationMembership{}, false, consolidationHold("CONFLICTING_MEMBERSHIP")
			}
			member := prior
			member.RowKey = key
			if mapping.ProjectID != "" && member.ProjectID == nil {
				target := mapping.ProjectID
				member.ProjectID = &target
			}
			return member, true, nil
		}
	}
	return consolidationMembershipFor(state, row, mapping, key), false, nil
}

func consolidationMembershipObject(member ConsolidationMembership) (JSONRaw, error) {
	raw, err := json.Marshal(struct {
		Reference           string          `json:"reference"`
		UnresolvedReference string          `json:"unresolved_reference"`
		Table               string          `json:"table_name"`
		RowKey              json.RawMessage `json:"row_key"`
		SourceRowKey        json.RawMessage `json:"source_row_key"`
		FieldPath           json.RawMessage `json:"field_path"`
		SelectorReference   string          `json:"selector_reference"`
		ProjectID           *string         `json:"project_id"`
		Reason              string          `json:"reason"`
		PlanFingerprint     string          `json:"plan_fingerprint"`
		NativeVersion       string          `json:"native_version"`
		SourceSnapshot      string          `json:"source_snapshot"`
	}{member.Reference, member.UnresolvedReference, member.Table, json.RawMessage(member.RowKey), json.RawMessage(member.SourceRowKey), json.RawMessage(member.FieldPath), member.SelectorReference, member.ProjectID, member.Reason, member.PlanFingerprint, member.NativeVersion, member.SourceSnapshot})
	return JSONRaw(raw), err
}
