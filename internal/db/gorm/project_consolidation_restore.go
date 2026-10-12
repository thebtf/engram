package gorm

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
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
		row.Name = name
		if err := tx.Raw("SELECT last_value,is_called FROM " + quoted(name)).Scan(&row).Error; err != nil {
			return nil, err
		}
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

// VerifyBackup restores a complete sealed snapshot into the explicitly supplied
// empty isolated target. It commits useful restored data, never overwrites an
// existing target, and does not mark a mere decryption as restore verification.
func (s *ProjectConsolidationStore) VerifyBackup(ctx context.Context, token, fingerprint string, encrypted []byte) (string, error) {
	if err := s.available(); err != nil {
		return "", err
	}
	state, err := s.plan(token, fingerprint)
	if err != nil {
		return "", err
	}
	if err := s.distinctTarget(ctx); err != nil {
		return "", err
	}
	if len(encrypted) == 0 || len(encrypted) > 40<<20 {
		return "", consolidationHold("BACKUP_MISSING")
	}
	raw, err := s.vault.Decrypt(encrypted)
	if err != nil {
		return "", consolidationHold("BACKUP_MISSING")
	}
	var backup consolidationBackupState
	if json.Unmarshal([]byte(raw), &backup) != nil || backup.PlanID != state.ID || backup.Fingerprint != fingerprint || backup.Schema != state.Census.Schema {
		return "", consolidationHold("BACKUP_MISSING")
	}
	// Source changed after export: retain the export, but refuse to call it the
	// current recovery point. New writes need a new census/plan/export.
	err = s.db.WithContext(ctx).Transaction(func(tx *gormlib.DB) error {
		if err := s.fresh(tx, state.Census); err != nil {
			return err
		}
		return consolidationCompare(tx, backup.Tables)
	}, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return "", err
	}
	id := uuid.NewSHA1(uuid.NameSpaceOID, []byte(fingerprint+structuralDigest(encrypted))).String()
	err = s.restoreDB.WithContext(ctx).Transaction(func(tx *gormlib.DB) error {
		if err := consolidationLock(tx); err != nil {
			return err
		}
		identity, err := consolidationDatabaseIdentity(tx)
		if err != nil {
			return err
		}
		var existing consolidationBackupRow
		if err := tx.Where("backup_id = ?", id).First(&existing).Error; err == nil {
			snapshot, err := consolidationRevision(tx)
			if err != nil {
				return err
			}
			if existing.RestoreIdentity != identity || existing.RestoreSnapshot != snapshot || existing.PlanFingerprint != fingerprint {
				return consolidationHold("STALE_PLAN")
			}
			return nil
		} else if !errors.Is(err, gormlib.ErrRecordNotFound) {
			return err
		}
		schema, columns, _, err := consolidationSchema(tx)
		if err != nil {
			return err
		}
		if schema != backup.Schema {
			return consolidationHold("SCHEMA_MISMATCH")
		}
		tables, err := consolidationDataTables(tx)
		if err != nil {
			return err
		}
		if len(tables) != len(backup.Tables) {
			return consolidationHold("SCHEMA_MISMATCH")
		}
		var nonempty bool
		for _, table := range tables {
			var count int64
			if err := tx.Table(table.Name).Count(&count).Error; err != nil {
				return err
			}
			nonempty = nonempty || count != 0
		}
		if nonempty {
			// A corrected map can reuse this exact previously verified restore;
			// no data is overwritten and no prior receipt is edited or forged.
			snapshot, err := consolidationRevision(tx)
			if err != nil {
				return err
			}
			var verified int64
			if err := tx.Model(&consolidationBackupRow{}).Where("restore_identity = ? AND restore_snapshot = ?", identity, snapshot).Count(&verified).Error; err != nil {
				return err
			}
			if verified == 0 {
				return consolidationHold("RESTORE_TARGET_NOT_EMPTY")
			}
			if err := consolidationCompare(tx, backup.Tables); err != nil {
				return err
			}
			if err := s.verifyCredentials(ctx, tx); err != nil {
				return err
			}
			return tx.Create(&consolidationBackupRow{BackupID: id, PlanID: state.ID, PlanFingerprint: fingerprint, Ciphertext: encrypted, RestoreIdentity: identity, RestoreSnapshot: snapshot, RestoreVerifiedAt: time.Now().UTC()}).Error
		}
		var fks []struct {
			TableName, ConstraintName     string
			Deferrable, InitiallyDeferred bool
		}
		if err := tx.Raw(`SELECT cl.relname AS table_name,c.conname AS constraint_name,c.condeferrable AS deferrable,c.condeferred AS initially_deferred
			FROM pg_constraint c JOIN pg_class cl ON cl.oid=c.conrelid JOIN pg_namespace n ON n.oid=cl.relnamespace
			WHERE n.nspname=current_schema() AND c.contype='f' ORDER BY cl.relname,c.conname`).Scan(&fks).Error; err != nil {
			return err
		}
		for _, fk := range fks {
			if err := tx.Exec("ALTER TABLE " + quoted(fk.TableName) + " ALTER CONSTRAINT " + quoted(fk.ConstraintName) + " DEFERRABLE INITIALLY DEFERRED").Error; err != nil {
				return err
			}
		}
		for _, table := range tables {
			var fields []string
			for _, column := range columns {
				if column.TableName == table.Name && column.IsGenerated == "NEVER" {
					fields = append(fields, quoted(column.ColumnName))
				}
			}
			if len(fields) == 0 {
				return consolidationHold("INVENTORY_INCOMPLETE")
			}
			columnsSQL := strings.Join(fields, ",")
			if err := tx.Exec("INSERT INTO "+quoted(table.Name)+" ("+columnsSQL+") OVERRIDING SYSTEM VALUE SELECT "+columnsSQL+" FROM jsonb_populate_recordset(NULL::"+quoted(table.Name)+",?::jsonb)", string(backup.Tables[table.Name])).Error; err != nil {
				return consolidationHold("RESTORE_CONSTRAINT_FAILED")
			}
		}
		if err := tx.Exec(`SET CONSTRAINTS ALL IMMEDIATE`).Error; err != nil {
			return consolidationHold("RESTORE_CONSTRAINT_FAILED")
		}
		for _, fk := range fks {
			mode := " NOT DEFERRABLE"
			if fk.Deferrable {
				mode = " DEFERRABLE INITIALLY IMMEDIATE"
				if fk.InitiallyDeferred {
					mode = " DEFERRABLE INITIALLY DEFERRED"
				}
			}
			if err := tx.Exec("ALTER TABLE " + quoted(fk.TableName) + " ALTER CONSTRAINT " + quoted(fk.ConstraintName) + mode).Error; err != nil {
				return err
			}
		}
		if err := consolidationCompare(tx, backup.Tables); err != nil {
			return err
		}
		if err := s.verifyCredentials(ctx, tx); err != nil {
			return err
		}
		// setval does not roll back in PostgreSQL. This only advances isolated
		// empty-target sequences; retries never replace owner data or lower them.
		for _, seq := range backup.Sequences {
			var present int64
			if err := tx.Raw(`SELECT count(*) FROM pg_sequences WHERE schemaname=current_schema() AND sequencename=?`, seq.Name).Scan(&present).Error; err != nil {
				return err
			}
			if present != 1 {
				return consolidationHold("SCHEMA_MISMATCH")
			}
			if err := tx.Exec(`SELECT setval(?::regclass,?,?)`, quoted(seq.Name), seq.LastValue, seq.IsCalled).Error; err != nil {
				return err
			}
		}
		snapshot, err := consolidationRevision(tx)
		if err != nil {
			return err
		}
		return tx.Create(&consolidationBackupRow{BackupID: id, PlanID: state.ID, PlanFingerprint: fingerprint, Ciphertext: encrypted, RestoreIdentity: identity, RestoreSnapshot: snapshot, RestoreVerifiedAt: time.Now().UTC()}).Error
	})
	return id, err
}

func (s *ProjectConsolidationStore) verifyCredentials(ctx context.Context, tx *gormlib.DB) error {
	var exists bool
	if err := tx.Raw(`SELECT to_regclass(quote_ident(current_schema())||'.credentials') IS NOT NULL`).Scan(&exists).Error; err != nil {
		return err
	}
	if !exists {
		return nil
	}
	var credentials []Credential
	if err := tx.Find(&credentials).Error; err != nil {
		return err
	}
	reader := NewCredentialStore(&Store{DB: tx})
	for _, credential := range credentials {
		if !s.vault.MatchesFingerprint(credential.EncryptionKeyFingerprint) {
			return consolidationHold("SECRET_UNREADABLE")
		}
		if _, err := s.vault.Decrypt(credential.EncryptedSecret); err != nil {
			return consolidationHold("SECRET_UNREADABLE")
		}
		if credential.DeletedAt != nil {
			continue
		}
		read, err := reader.Get(ctx, credential.Project, credential.Key)
		if err != nil || read.ID != credential.ID || read.Scope != credential.Scope || read.Version != credential.Version {
			return consolidationHold("SECRET_UNREADABLE")
		}
		if _, err := s.vault.Decrypt(read.EncryptedSecret); err != nil {
			return consolidationHold("SECRET_UNREADABLE")
		}
	}
	return nil
}

func (s *ProjectConsolidationStore) execute(tx *gormlib.DB, state consolidationPlanState, before map[string]json.RawMessage) (ConsolidationReceipt, error) {
	expected := make(map[string][]map[string]json.RawMessage, len(before))
	for table, raw := range before {
		var rows []map[string]json.RawMessage
		if json.Unmarshal(raw, &rows) != nil {
			return ConsolidationReceipt{}, consolidationHold("INVENTORY_INCOMPLETE")
		}
		expected[table] = rows
	}
	byRef := make(map[string]consolidationRow, len(state.Census.Rows))
	for _, row := range state.Census.Rows {
		byRef[row.Reference] = row
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
			if err := tx.Create(&Project{ID: target}).Error; err != nil {
				return ConsolidationReceipt{}, consolidationCollision(err)
			}
			var raw string
			if err := tx.Raw(`SELECT to_jsonb(p)::text FROM projects p WHERE id=?`, target).Scan(&raw).Error; err != nil {
				return ConsolidationReceipt{}, err
			}
			var object map[string]json.RawMessage
			if json.Unmarshal([]byte(raw), &object) != nil {
				return ConsolidationReceipt{}, consolidationHold("CONSERVATION_FAILED")
			}
			expected["projects"] = append(expected["projects"], object)
		}
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
		if row.Global {
			counts[key].After++
			continue
		}
		var nativeKey map[string]json.RawMessage
		if json.Unmarshal(row.Key, &nativeKey) != nil {
			return ConsolidationReceipt{}, consolidationHold("INVENTORY_INCOMPLETE")
		}
		var object map[string]json.RawMessage
		for _, candidate := range expected[row.Table] {
			match := true
			for k, v := range nativeKey {
				if !consolidationJSONEqual(candidate[k], v) {
					match = false
					break
				}
			}
			if match {
				object = candidate
				break
			}
		}
		if object == nil {
			return ConsolidationReceipt{}, consolidationHold("UNSTABLE_NATIVE_KEY")
		}
		mutable := m.ProjectID != "" && (row.Kind == "tenant" || row.Kind == "array" || row.Kind == "json_addressing")
		if mutable {
			moved, err := consolidationReplaceOccurrence(object[row.Column], row.Path, row.Selector, m.ProjectID)
			if err != nil {
				return ConsolidationReceipt{}, err
			}
			if err := tx.Exec("UPDATE "+quoted(row.Table)+" t SET "+quoted(row.Column)+"=r."+quoted(row.Column)+" FROM jsonb_populate_record(NULL::"+quoted(row.Table)+",?::jsonb) r WHERE to_jsonb(t) @> ?::jsonb", `{"`+row.Column+`":`+string(moved)+`}`, string(row.Key)).Error; err != nil {
				return ConsolidationReceipt{}, consolidationCollision(err)
			}
			object[row.Column] = moved
			if _, primary := nativeKey[row.Column]; primary {
				nativeKey[row.Column] = moved
			}
		}
		keyJSON, _ := json.Marshal(nativeKey)
		pathJSON, _ := json.Marshal(append([]string{row.Column}, row.Path...))
		var target *string
		if m.ProjectID != "" {
			value := m.ProjectID
			target = &value
		}
		membership := ConsolidationMembership{Reference: row.Reference, UnresolvedReference: m.UnresolvedReference, Table: row.Table, RowKey: JSONRaw(keyJSON), FieldPath: JSONRaw(pathJSON), SelectorReference: row.SelectorReference, ProjectID: target, Reason: m.Reason, PlanFingerprint: state.Fingerprint}
		var prior ConsolidationMembership
		if err := tx.Where("reference = ?", row.Reference).First(&prior).Error; err == nil {
			if prior.PlanFingerprint != state.Fingerprint {
				return ConsolidationReceipt{}, consolidationHold("STALE_MAPPING")
			}
		} else if errors.Is(err, gormlib.ErrRecordNotFound) {
			if err := tx.Create(&membership).Error; err != nil {
				return ConsolidationReceipt{}, consolidationCollision(err)
			}
		} else {
			return ConsolidationReceipt{}, err
		}
		counts[key].After++
	}
	if err := s.installRedirects(tx, state); err != nil {
		return ConsolidationReceipt{}, err
	}
	after := make(map[string]json.RawMessage, len(expected))
	for table, rows := range expected {
		raw, err := json.Marshal(rows)
		if err != nil {
			return ConsolidationReceipt{}, err
		}
		if len(rows) == 0 {
			raw = []byte("[]")
		}
		after[table] = raw
	}
	if err := consolidationCompare(tx, after); err != nil {
		return ConsolidationReceipt{}, err
	}
	if err := s.verifyCredentials(tx.Statement.Context, tx); err != nil {
		return ConsolidationReceipt{}, err
	}
	snapshot, err := consolidationRevision(tx)
	if err != nil {
		return ConsolidationReceipt{}, err
	}
	operation := uuid.NewSHA1(uuid.NameSpaceOID, []byte(state.Fingerprint)).String()
	receipt := ConsolidationReceipt{OperationID: operation, Reference: "urn:engram:migration:receipt:" + operation, Snapshot: snapshot, RollbackBoundary: "Do not overwrite new writes from this export: stop writers, take a fresh export, inventory post-cutover writes, then forward-compensate or restore into another empty isolated target."}
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

func (s *ProjectConsolidationStore) installRedirects(tx *gormlib.DB, state consolidationPlanState) error {
	type bucket struct {
		Target string
		Safe   bool
		Seen   bool
	}
	buckets := map[string]bucket{}
	for i, row := range state.Census.Rows {
		if row.Selector == "" || row.Global || row.Kind == "locator" || row.Kind == "unknown" || row.Kind == "historical_identity" {
			continue
		}
		m := state.Mapping[i]
		b := buckets[row.Selector]
		if !b.Seen {
			b = bucket{Target: m.ProjectID, Safe: m.ProjectID != "", Seen: true}
		} else if m.ProjectID == "" || m.ProjectID != b.Target {
			b.Safe = false
		}
		buckets[row.Selector] = b
	}
	for selector, b := range buckets {
		if b.Safe {
			if err := consolidationRedirect(tx, selector, "", b.Target, state.Fingerprint); err != nil {
				return err
			}
		}
	}
	// Session-specific redirect applies only to a proven SDK session occurrence;
	// it cannot redirect another project's rows from the same mixed old bucket.
	for i, row := range state.Census.Rows {
		if row.Table != "sdk_sessions" || row.Column != "project" || state.Mapping[i].ProjectID == "" {
			continue
		}
		var sessions []SDKSession
		if err := tx.Raw("SELECT * FROM sdk_sessions t WHERE to_jsonb(t) @> ?::jsonb", string(row.Key)).Scan(&sessions).Error; err != nil {
			return err
		}
		for _, session := range sessions {
			ids := []string{session.ClaudeSessionID}
			if session.SDKSessionID.Valid {
				ids = append(ids, session.SDKSessionID.String)
			}
			for _, id := range ids {
				if id != "" {
					if err := consolidationRedirect(tx, row.Selector, id, state.Mapping[i].ProjectID, state.Fingerprint); err != nil {
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
	return consolidationCollision(tx.Exec(`INSERT INTO project_consolidation_redirects(selector,session_id,project_id,plan_fingerprint) VALUES(?,?,?::uuid,?)`, selector, session, target, fingerprint).Error)
}
