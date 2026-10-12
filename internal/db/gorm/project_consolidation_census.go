package gorm

import (
	"context"
	"database/sql"
	"encoding/json"
	"sort"
	"strconv"
	"strings"

	"github.com/lib/pq"
	gormlib "gorm.io/gorm"
)

type consolidationColumn struct {
	TableName, ColumnName, DataType, UdtName, IsGenerated, IsNullable string
	ColumnDefault                                                     *string
}

type consolidationTable struct {
	Name       string         `json:"table"`
	Count      int64          `json:"row_count"`
	PrimaryKey pq.StringArray `json:"primary_key" gorm:"type:text[]"`
	Kind       string         `json:"domain"`
}

type consolidationForeignKey struct {
	TableName, ParentTable string
	Columns, ParentColumns pq.StringArray `gorm:"type:text[]"`
}

type consolidationRow struct {
	Reference, Table, Column, Kind, Selector, SelectorReference string
	Key                                                         JSONRaw
	Path                                                        []string
	Global                                                      bool
	Parents                                                     []string
}

type consolidationCensusState struct {
	SourceVersion, Database, Snapshot, Schema, Fingerprint string
	Tables                                                 []consolidationTable
	Rows                                                   []consolidationRow
	Gaps                                                   []string
}

type ConsolidationCensus struct {
	Reference     string                 `json:"census_reference"`
	Fingerprint   string                 `json:"census_fingerprint"`
	SourceVersion string                 `json:"source_version"`
	Database      string                 `json:"database_identity"`
	Snapshot      string                 `json:"snapshot_identity"`
	Schema        string                 `json:"schema_identity"`
	Tables        []consolidationTable   `json:"tables"`
	Rows          []ConsolidationMapping `json:"rows"`
	Gaps          []string               `json:"gaps"`
}

func consolidationColumns(tx *gormlib.DB) ([]consolidationColumn, error) {
	var columns []consolidationColumn
	err := tx.Raw(`SELECT c.table_name,c.column_name,c.data_type,c.udt_name,c.is_generated,c.is_nullable,c.column_default
		FROM information_schema.columns c JOIN information_schema.tables t USING(table_schema,table_name)
		WHERE c.table_schema=current_schema() AND t.table_type='BASE TABLE' ORDER BY c.table_name,c.ordinal_position`).Scan(&columns).Error
	return columns, err
}

func consolidationSchema(tx *gormlib.DB) (string, []consolidationColumn, []consolidationForeignKey, error) {
	columns, err := consolidationColumns(tx)
	if err != nil {
		return "", nil, nil, err
	}
	var definitions []struct{ TableName, Name, Kind, Definition string }
	err = tx.Raw(`SELECT cl.relname AS table_name,c.conname AS name,c.contype::text AS kind,pg_get_constraintdef(c.oid) AS definition
		FROM pg_constraint c JOIN pg_class cl ON cl.oid=c.conrelid JOIN pg_namespace n ON n.oid=cl.relnamespace WHERE n.nspname=current_schema()
		UNION ALL SELECT tablename,indexname,'index',indexdef FROM pg_indexes WHERE schemaname=current_schema()
		UNION ALL SELECT cl.relname,t.tgname,'trigger',pg_get_triggerdef(t.oid) FROM pg_trigger t JOIN pg_class cl ON cl.oid=t.tgrelid
		JOIN pg_namespace n ON n.oid=cl.relnamespace WHERE n.nspname=current_schema() AND NOT t.tgisinternal
		ORDER BY 1,2,3`).Scan(&definitions).Error
	if err != nil {
		return "", nil, nil, err
	}
	var fks []consolidationForeignKey
	err = tx.Raw(`SELECT cl.relname AS table_name, parent.relname AS parent_table,
		ARRAY(SELECT a.attname::text FROM unnest(c.conkey) WITH ORDINALITY k(num,ord) JOIN pg_attribute a ON a.attrelid=c.conrelid AND a.attnum=k.num ORDER BY k.ord) AS columns,
		ARRAY(SELECT a.attname::text FROM unnest(c.confkey) WITH ORDINALITY k(num,ord) JOIN pg_attribute a ON a.attrelid=c.confrelid AND a.attnum=k.num ORDER BY k.ord) AS parent_columns
		FROM pg_constraint c JOIN pg_class cl ON cl.oid=c.conrelid JOIN pg_class parent ON parent.oid=c.confrelid
		JOIN pg_namespace n ON n.oid=cl.relnamespace WHERE n.nspname=current_schema() AND c.contype='f' ORDER BY cl.relname,c.conname`).Scan(&fks).Error
	if err != nil {
		return "", nil, nil, err
	}
	return structuralDigest(struct {
		Columns     []consolidationColumn
		Definitions any
	}{columns, definitions}), columns, fks, nil
}

func consolidationDataTables(tx *gormlib.DB) ([]consolidationTable, error) {
	var tables []consolidationTable
	err := tx.Raw(`SELECT t.table_name AS name, COALESCE(ARRAY(SELECT a.attname::text FROM pg_index i
		CROSS JOIN LATERAL unnest(i.indkey) WITH ORDINALITY k(num,ord) JOIN pg_attribute a ON a.attrelid=i.indrelid AND a.attnum=k.num
		WHERE i.indrelid=to_regclass(quote_ident(t.table_schema)||'.'||quote_ident(t.table_name)) AND i.indisprimary ORDER BY k.ord),'{}'::text[]) AS primary_key
		FROM information_schema.tables t WHERE t.table_schema=current_schema() AND t.table_type='BASE TABLE'
		AND t.table_name <> 'migrations' AND t.table_name NOT LIKE 'project_consolidation_%' ORDER BY t.table_name`).Scan(&tables).Error
	return tables, err
}

func consolidationDatabaseIdentity(tx *gormlib.DB) (string, error) {
	var binding struct {
		Database, Schema, Server, Port string
		OID                            int64
	}
	err := tx.Raw(`SELECT current_database() AS database,current_schema() AS schema,COALESCE(inet_server_addr()::text,'local') AS server,
		COALESCE(inet_server_port()::text,'local') AS port,(SELECT oid::bigint FROM pg_database WHERE datname=current_database()) AS oid`).Scan(&binding).Error
	return structuralDigest(binding), err
}

func consolidationRevision(tx *gormlib.DB) (string, error) {
	tables, err := consolidationDataTables(tx)
	if err != nil {
		return "", err
	}
	stamps := make([]string, 0, len(tables))
	for _, table := range tables {
		var stamp string
		if err := tx.Raw("SELECT count(*)::text || ':' || md5(COALESCE(string_agg(xmin::text || ':' || ctid::text, ',' ORDER BY ctid),'')) FROM " + quoted(table.Name)).Scan(&stamp).Error; err != nil {
			return "", err
		}
		stamps = append(stamps, table.Name+":"+stamp)
	}
	return structuralDigest(stamps), nil
}

func consolidationSnapshot(tx *gormlib.DB) (map[string]json.RawMessage, error) {
	tables, err := consolidationDataTables(tx)
	if err != nil {
		return nil, err
	}
	result := make(map[string]json.RawMessage, len(tables))
	total := 0
	for _, table := range tables {
		var raw string
		if err := tx.Raw("SELECT COALESCE(jsonb_agg(to_jsonb(t) ORDER BY to_jsonb(t)::text),'[]'::jsonb)::text FROM " + quoted(table.Name) + " t").Scan(&raw).Error; err != nil {
			return nil, err
		}
		total += len(raw)
		if total > 32<<20 {
			return nil, consolidationHold("SNAPSHOT_CAPACITY_EXCEEDED")
		}
		result[table.Name] = json.RawMessage(raw)
	}
	return result, nil
}

func (s *ProjectConsolidationStore) Census(ctx context.Context, sourceVersion string) (ConsolidationCensus, error) {
	if err := s.available(); err != nil {
		return ConsolidationCensus{}, err
	}
	if strings.TrimSpace(sourceVersion) == "" || len(sourceVersion) > 256 {
		return ConsolidationCensus{}, consolidationHold("SOURCE_IDENTITY_REQUIRED")
	}
	var state consolidationCensusState
	err := s.db.WithContext(ctx).Transaction(func(tx *gormlib.DB) error {
		var err error
		state, err = s.census(tx, sourceVersion)
		return err
	}, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return ConsolidationCensus{}, err
	}
	reference, err := s.seal("census", state)
	rows := make([]ConsolidationMapping, 0, len(state.Rows))
	for _, row := range state.Rows {
		rows = append(rows, ConsolidationMapping{Reference: row.Reference, Family: row.Table, Role: row.Column, SelectorReference: row.SelectorReference, Reason: row.Kind})
	}
	return ConsolidationCensus{Reference: reference, Fingerprint: state.Fingerprint, SourceVersion: state.SourceVersion, Database: state.Database, Snapshot: state.Snapshot, Schema: state.Schema, Tables: state.Tables, Rows: rows, Gaps: state.Gaps}, err
}

func (s *ProjectConsolidationStore) census(tx *gormlib.DB, sourceVersion string) (consolidationCensusState, error) {
	state := consolidationCensusState{SourceVersion: sourceVersion, Gaps: []string{}}
	var err error
	state.Database, err = consolidationDatabaseIdentity(tx)
	if err != nil {
		return state, err
	}
	var fks []consolidationForeignKey
	var columns []consolidationColumn
	state.Schema, columns, fks, err = consolidationSchema(tx)
	if err != nil {
		return state, err
	}
	state.Snapshot, err = consolidationRevision(tx)
	if err != nil {
		return state, err
	}
	state.Tables, err = consolidationDataTables(tx)
	if err != nil {
		return state, err
	}
	snapshots, err := consolidationSnapshot(tx)
	if err != nil {
		return state, err
	}
	objects := map[string][]map[string]json.RawMessage{}
	byObject := map[string][]int{}
	for ti := range state.Tables {
		table := &state.Tables[ti]
		var objectsForTable []map[string]json.RawMessage
		if err := json.Unmarshal(snapshots[table.Name], &objectsForTable); err != nil {
			return state, err
		}
		objects[table.Name] = objectsForTable
		table.Count = int64(len(objectsForTable))
		table.Kind = consolidationDomain(table.Name)
		if len(table.PrimaryKey) == 0 && table.Count > 0 {
			state.Gaps = append(state.Gaps, table.Name+":no_stable_primary_key")
		}
		for index, object := range objectsForTable {
			key := map[string]json.RawMessage{}
			for _, name := range table.PrimaryKey {
				key[name] = object[name]
			}
			if len(key) == 0 {
				key["__inventory_ordinal"] = json.RawMessage(strconv.Itoa(index))
			}
			keyJSON, _ := json.Marshal(key)
			start := len(state.Rows)
			for _, column := range columns {
				if column.TableName != table.Name {
					continue
				}
				raw := object[column.ColumnName]
				if column.DataType == "jsonb" || column.DataType == "json" {
					var value any
					if json.Unmarshal(raw, &value) != nil {
						return state, consolidationHold("INVENTORY_INCOMPLETE")
					}
					for _, field := range consolidationJSONFields(value, nil, false) {
						kind := "unknown"
						if table.Kind == "durable_history" || table.Kind == "immutable_audit" {
							kind = "serialized_history"
						}
						if table.Name == "agent_session_state" || table.Name == "knowledge_nodes" || table.Name == "telemetry_snapshots" {
							kind = "json_addressing"
						}
						s.addCensusRow(&state, table.Name, column.ColumnName, kind, keyJSON, field.Path, field.Value, false)
					}
					continue
				}
				kind := consolidationFieldRole(table.Name, column.ColumnName)
				if kind == "" {
					continue
				}
				if kind == "unknown" {
					state.Gaps = append(state.Gaps, table.Name+"."+column.ColumnName+":unknown_field_role")
				}
				if column.DataType == "ARRAY" {
					var values []string
					if string(raw) != "null" && json.Unmarshal(raw, &values) != nil {
						return state, consolidationHold("INVENTORY_INCOMPLETE")
					}
					for i, value := range values {
						s.addCensusRow(&state, table.Name, column.ColumnName, kind, keyJSON, []string{strconv.Itoa(i)}, value, value == "")
					}
				} else {
					var value string
					if string(raw) != "null" {
						if json.Unmarshal(raw, &value) != nil {
							return state, consolidationHold("INVENTORY_INCOMPLETE")
						}
					}
					s.addCensusRow(&state, table.Name, column.ColumnName, kind, keyJSON, nil, value, value == "" && kind != "source_membership")
				}
			}
			if (len(state.Rows) == start && (table.Kind == "independent_view" || table.Kind == "derived_membership" || table.Name == "sources")) || table.Kind == "durable_history" {
				kind := "inherited"
				if table.Name == "sources" {
					kind = "source_membership"
				}
				s.addCensusRow(&state, table.Name, "", kind, keyJSON, nil, "", false)
			}
			indices := make([]int, len(state.Rows)-start)
			for i := range indices {
				indices[i] = start + i
			}
			byObject[consolidationObjectKey(table.Name, keyJSON)] = indices
		}
	}
	// FK parent lookup uses complete native key values, not spelling of selectors.
	for table, objectsInTable := range objects {
		for _, object := range objectsInTable {
			var key map[string]json.RawMessage = map[string]json.RawMessage{}
			for _, t := range state.Tables {
				if t.Name == table {
					for _, name := range t.PrimaryKey {
						key[name] = object[name]
					}
					break
				}
			}
			keyJSON, _ := json.Marshal(key)
			indices := byObject[consolidationObjectKey(table, keyJSON)]
			for _, fk := range fks {
				if fk.TableName != table {
					continue
				}
				if consolidationDomain(table) == "independent_view" && fk.ParentTable != "sources" && fk.ParentTable != "ci_checkouts" && fk.ParentTable != "ci_views" {
					continue
				}
				for _, parent := range objects[fk.ParentTable] {
					match := true
					for i, name := range fk.Columns {
						if string(object[name]) == "null" || !consolidationJSONEqual(object[name], parent[fk.ParentColumns[i]]) {
							match = false
							break
						}
					}
					if !match {
						continue
					}
					parentKey := map[string]json.RawMessage{}
					for _, t := range state.Tables {
						if t.Name == fk.ParentTable {
							for _, name := range t.PrimaryKey {
								parentKey[name] = parent[name]
							}
							break
						}
					}
					parentJSON, _ := json.Marshal(parentKey)
					for _, pi := range byObject[consolidationObjectKey(fk.ParentTable, parentJSON)] {
						p := state.Rows[pi]
						if p.Column != "" && p.Column != "project" && p.Column != "target_project" && !(p.Table == "rule_candidates" && p.Column == "source_project") && !(p.Table == "sources" && p.Column == "project_id") {
							continue
						}
						if p.Kind == "registry" || p.Kind == "historical_identity" || p.Global {
							continue
						}
						for _, i := range indices {
							if state.Rows[i].Kind == "inherited" {
								state.Rows[i].Parents = append(state.Rows[i].Parents, p.Reference)
							}
						}
					}
				}
			}
		}
	}
	for i := range state.Rows {
		sort.Strings(state.Rows[i].Parents)
		state.Rows[i].Parents = compactStrings(state.Rows[i].Parents)
	}
	sort.Strings(state.Gaps)
	state.Gaps = compactStrings(state.Gaps)
	state.Fingerprint = structuralDigest(struct{ Version, Database, Snapshot, Schema string }{sourceVersion, state.Database, state.Snapshot, state.Schema})
	return state, nil
}

func consolidationObjectKey(table string, key []byte) string { return table + ":" + string(key) }
func consolidationJSONEqual(a, b json.RawMessage) bool {
	var av, bv any
	return json.Unmarshal(a, &av) == nil && json.Unmarshal(b, &bv) == nil && structuralDigest(av) == structuralDigest(bv)
}

func (s *ProjectConsolidationStore) addCensusRow(state *consolidationCensusState, table, column, kind string, key JSONRaw, path []string, selector string, global bool) {
	row := consolidationRow{Table: table, Column: column, Kind: kind, Key: key, Path: path, Selector: selector, Global: global}
	row.Reference = s.opaque("row", struct {
		Table, Column string
		Key           JSONRaw
		Path          []string
	}{table, column, key, path})
	if selector != "" {
		row.SelectorReference = s.opaque("selector", selector)
	}
	state.Rows = append(state.Rows, row)
}

func (s *ProjectConsolidationStore) fresh(tx *gormlib.DB, census consolidationCensusState) error {
	identity, err := consolidationDatabaseIdentity(tx)
	if err != nil {
		return err
	}
	schema, _, _, err := consolidationSchema(tx)
	if err != nil {
		return err
	}
	snapshot, err := consolidationRevision(tx)
	if err != nil {
		return err
	}
	if identity != census.Database || schema != census.Schema || snapshot != census.Snapshot {
		return consolidationHold("STALE_PLAN")
	}
	return nil
}

type consolidationJSONField struct {
	Path  []string
	Value string
}

func consolidationJSONFields(value any, path []string, active bool) []consolidationJSONField {
	var fields []consolidationJSONField
	switch typed := value.(type) {
	case string:
		if active && typed != "" {
			fields = append(fields, consolidationJSONField{append([]string{}, path...), typed})
		}
	case []any:
		for i, child := range typed {
			fields = append(fields, consolidationJSONFields(child, append(append([]string{}, path...), strconv.Itoa(i)), active)...)
		}
	case map[string]any:
		keys := make([]string, 0, len(typed))
		for key := range typed {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			fields = append(fields, consolidationJSONFields(typed[key], append(append([]string{}, path...), key), strings.Contains(key, "project"))...)
		}
	}
	return fields
}

func consolidationFieldRole(table, column string) string {
	for _, role := range consolidationRoles {
		if role.Table == table && role.Column == column {
			return role.Kind
		}
	}
	if strings.Contains(column, "project") {
		return "unknown"
	}
	return ""
}

func consolidationDomain(table string) string {
	switch table {
	case "task_memory_intervention_receipts", "intervention_evidence_policies", "project_merge_audits", "project_merge_audit_sources", "project_resolution_attempts", "project_identity_comparisons":
		return "immutable_audit"
	case "sources", "space_sources", "legacy_context_aliases", "content_chunks", "knowledge_edges":
		return "derived_membership"
	case "audit_log", "memory_versions", "memory_audit_log", "memory_audits", "versioned_document_comments", "issue_comments", "rule_versions", "rule_families", "rule_transition_log", "rule_governance_snapshots", "bulk_op_snapshots", "agent_session_state", "citation_log", "promotion_log", "rule_arbiter_evaluations":
		return "durable_history"
	}
	if strings.HasPrefix(table, "ci_") || strings.HasPrefix(table, "uci_") {
		return "independent_view"
	}
	return "durable_or_global"
}

func consolidationLock(tx *gormlib.DB) error {
	if err := tx.Exec(`SET LOCAL lock_timeout='5s'`).Error; err != nil {
		return err
	}
	tables, err := consolidationDataTables(tx)
	if err != nil {
		return err
	}
	var names []string
	for _, table := range tables {
		names = append(names, quoted(table.Name))
	}
	names = append(names, quoted("project_consolidation_backups"), quoted("project_consolidation_operations"), quoted("project_consolidation_memberships"), quoted("project_consolidation_redirects"))
	sort.Strings(names)
	if len(names) == 0 {
		return consolidationHold("INVENTORY_INCOMPLETE")
	}
	return tx.Exec("LOCK TABLE " + strings.Join(names, ",") + " IN EXCLUSIVE MODE").Error
}

func consolidationCompare(tx *gormlib.DB, tables map[string]json.RawMessage) error {
	current, err := consolidationDataTables(tx)
	if err != nil {
		return err
	}
	if len(current) != len(tables) {
		return consolidationHold("CONSERVATION_FAILED")
	}
	for _, table := range current {
		raw, ok := tables[table.Name]
		if !ok {
			return consolidationHold("CONSERVATION_FAILED")
		}
		var equal bool
		if err := tx.Raw("SELECT COALESCE(jsonb_agg(to_jsonb(t) ORDER BY to_jsonb(t)::text),'[]'::jsonb) = ?::jsonb FROM "+quoted(table.Name)+" t", string(raw)).Scan(&equal).Error; err != nil {
			return err
		}
		if !equal {
			return consolidationHold("CONSERVATION_FAILED")
		}
	}
	return nil
}

// SQL diagnostics may contain secret values in unique keys; transports only
// expose the stable redacted error code.
func consolidationCollision(err error) error {
	if err == nil {
		return nil
	}
	return consolidationHold("COLLISION")
}
