package gorm

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
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
	TypeDefinition, CollationName                                     string
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
	NativeVersion                                               string
}

type consolidationCensusState struct {
	SourceVersion, Database, Snapshot, Schema, Fingerprint string
	NativeSnapshot                                         string
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
	err := tx.Raw(`SELECT c.table_name,c.column_name,c.data_type,c.udt_name,c.is_generated,c.is_nullable,c.column_default,
	 format_type(a.atttypid,a.atttypmod) AS type_definition,COALESCE(c.collation_name,'') AS collation_name
	 FROM information_schema.columns c JOIN information_schema.tables t USING(table_schema,table_name)
	 JOIN pg_attribute a ON a.attrelid=to_regclass(quote_ident(c.table_schema)||'.'||quote_ident(c.table_name)) AND a.attname=c.column_name
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
		UNION ALL SELECT '',e.extname,'extension',e.extversion FROM pg_extension e
		UNION ALL SELECT '',p.proname||'('||pg_get_function_identity_arguments(p.oid)||')','routine',pg_get_functiondef(p.oid)
		FROM pg_proc p JOIN pg_namespace n ON n.oid=p.pronamespace WHERE n.nspname=current_schema() AND p.prokind IN ('f','p')
		UNION ALL SELECT '',sequencename,'sequence',concat_ws(':',start_value,min_value,max_value,increment_by,cycle,cache_size) FROM pg_sequences WHERE schemaname=current_schema()
		UNION ALL SELECT viewname,viewname,'view',definition FROM pg_views WHERE schemaname=current_schema()
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
		AND t.table_name NOT LIKE 'project_consolidation_%' ORDER BY t.table_name`).Scan(&tables).Error
	return tables, err
}

func consolidationDatabaseIdentity(tx *gormlib.DB) (string, error) {
	var binding struct {
		Database, SystemIdentifier string
		OID                        int64
	}
	err := tx.Raw(`SELECT current_database(),(SELECT system_identifier::text FROM pg_control_system()),(SELECT oid::bigint FROM pg_database WHERE datname=current_database())`).Row().Scan(&binding.Database, &binding.SystemIdentifier, &binding.OID)
	if err != nil || binding.Database == "" || binding.SystemIdentifier == "" || binding.OID <= 0 {
		return "", consolidationHold("SOURCE_IDENTITY_UNAVAILABLE")
	}
	return structuralDigest(binding), nil
}

func consolidationRevision(tx *gormlib.DB) (string, error) {
	tables, err := consolidationDataTables(tx)
	if err != nil {
		return "", err
	}
	h := sha256.New()
	encoder := json.NewEncoder(h)
	for _, table := range tables {
		if err := encoder.Encode(table.Name); err != nil {
			return "", err
		}
		rows, err := tx.Raw("SELECT " + consolidationKeySQL(table, "t") + "::text,xmin::text FROM " + quoted(table.Name) + " t ORDER BY " + consolidationOrderSQL(table, "t")).Rows()
		if err != nil {
			return "", err
		}
		for rows.Next() {
			var key []byte
			var version string
			if err := rows.Scan(&key, &version); err != nil {
				rows.Close()
				return "", err
			}
			if err := encoder.Encode(struct {
				Key     string
				Version string
			}{string(key), version}); err != nil {
				rows.Close()
				return "", err
			}
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return "", err
		}
	}
	sequences, err := consolidationSequences(tx)
	if err != nil {
		return "", err
	}
	if err := encoder.Encode(sequences); err != nil {
		return "", err
	}
	return "sha256:" + hex.EncodeToString(h.Sum(nil)), nil
}

// Only structural projections cross the connection during census. Bodies,
// vectors, transcripts and serialized history remain in the private export.
const consolidationMetadataBytes = 1 << 20

func consolidationKeySQL(table consolidationTable, alias string) string {
	if len(table.PrimaryKey) == 0 {
		return "jsonb_build_object('__native_ctid'," + alias + ".ctid::text,'__native_xmin'," + alias + ".xmin::text)"
	}
	var fields []string
	for _, key := range table.PrimaryKey {
		fields = append(fields, pq.QuoteLiteral(key), "to_jsonb("+alias+"."+quoted(key)+")")
	}
	return "jsonb_build_object(" + strings.Join(fields, ",") + ")"
}

func consolidationOrderSQL(table consolidationTable, alias string) string {
	var fields []string
	for _, key := range table.PrimaryKey {
		fields = append(fields, alias+"."+quoted(key))
	}
	if len(fields) == 0 {
		fields = append(fields, alias+".ctid")
	}
	return strings.Join(fields, ",")
}

func consolidationJSONProjection(expression string) string {
	return `(WITH RECURSIVE fields(path,value,active) AS (
	 SELECT '{}'::text[],COALESCE(` + expression + `,'null'::jsonb),false
	 UNION ALL SELECT f.path||c.key,c.value,CASE WHEN jsonb_typeof(f.value)='object' THEN position('project' in c.key)>0 ELSE f.active END
	 FROM fields f CROSS JOIN LATERAL (
	 SELECT key,value FROM jsonb_each(CASE WHEN jsonb_typeof(f.value)='object' THEN f.value ELSE '{}'::jsonb END)
	 UNION ALL SELECT (ord-1)::text,value FROM jsonb_array_elements(CASE WHEN jsonb_typeof(f.value)='array' THEN f.value ELSE '[]'::jsonb END) WITH ORDINALITY a(value,ord)
	 ) c
	) SELECT COALESCE(jsonb_agg(jsonb_build_object('Path',path,'Value',value#>>'{}') ORDER BY path)
	 FILTER(WHERE active AND jsonb_typeof(value)='string' AND value#>>'{}'<>''),'[]'::jsonb) FROM fields)`
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
	if err := tx.Raw(`SELECT txid_current_snapshot()::text`).Scan(&state.NativeSnapshot).Error; err != nil {
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
	var largeObjects int64
	if err := tx.Raw(`SELECT count(*) FROM pg_largeobject_metadata`).Scan(&largeObjects).Error; err != nil {
		return state, consolidationHold("INVENTORY_INCOMPLETE")
	}
	if largeObjects != 0 {
		return state, consolidationHold("LARGE_OBJECT_NATIVE_EXPORT_REQUIRED")
	}
	byObject := map[string][]int{}
	for ti := range state.Tables {
		table := &state.Tables[ti]
		table.Kind = consolidationDomain(table.Name)
		var projection []string
		for _, column := range columns {
			if column.TableName != table.Name {
				continue
			}
			if column.DataType == "jsonb" || column.DataType == "json" {
				projection = append(projection, pq.QuoteLiteral(column.ColumnName), consolidationJSONProjection("to_jsonb(t."+quoted(column.ColumnName)+")"))
				if (table.Name == "ci_profiles" && column.ColumnName == "build_context_json") || (table.Name == "versioned_documents" && column.ColumnName == "metadata") {
					projection = append(projection, pq.QuoteLiteral("__owner_empty_"+column.ColumnName), "to_jsonb(t."+quoted(column.ColumnName)+")='{}'::jsonb")
				}
			} else if consolidationFieldRole(table.Name, column.ColumnName) != "" {
				projection = append(projection, pq.QuoteLiteral(column.ColumnName), "to_jsonb(t."+quoted(column.ColumnName)+")")
			}
		}
		objectSQL := "'{}'::jsonb"
		if len(projection) != 0 {
			objectSQL = "jsonb_build_object(" + strings.Join(projection, ",") + ")"
		}
		rows, err := tx.Raw("SELECT " + consolidationKeySQL(*table, "t") + "::text,t.xmin::text," + objectSQL + "::text FROM " + quoted(table.Name) + " t ORDER BY " + consolidationOrderSQL(*table, "t")).Rows()
		if err != nil {
			return state, err
		}
		for rows.Next() {
			var keyJSON, raw []byte
			var version string
			if err := rows.Scan(&keyJSON, &version, &raw); err != nil {
				rows.Close()
				return state, err
			}
			if len(keyJSON)+len(raw) > consolidationMetadataBytes {
				rows.Close()
				return state, consolidationHold("METADATA_ENTRY_CAPACITY_EXCEEDED")
			}
			canonical, err := consolidationCanonicalKey(keyJSON)
			if err != nil {
				rows.Close()
				return state, err
			}
			keyJSON = []byte(canonical)
			var object map[string]json.RawMessage
			if json.Unmarshal(raw, &object) != nil {
				rows.Close()
				return state, consolidationHold("INVENTORY_INCOMPLETE")
			}
			table.Count++
			start := len(state.Rows)
			for _, column := range columns {
				if column.TableName != table.Name {
					continue
				}
				raw, present := object[column.ColumnName]
				if !present {
					continue
				}
				if column.DataType == "jsonb" || column.DataType == "json" {
					var fields []consolidationJSONField
					if json.Unmarshal(raw, &fields) != nil {
						rows.Close()
						return state, consolidationHold("INVENTORY_INCOMPLETE")
					}
					if (table.Name == "ci_profiles" && column.ColumnName == "build_context_json") || (table.Name == "versioned_documents" && column.ColumnName == "metadata") {
						var emptyObject bool
						if json.Unmarshal(object["__owner_empty_"+column.ColumnName], &emptyObject) != nil || !emptyObject {
							s.addCensusRow(&state, table.Name, column.ColumnName, "unknown", keyJSON, nil, "", false)
							state.Gaps = append(state.Gaps, table.Name+"."+column.ColumnName+":unknown_object_role")
						}
					}
					for _, field := range fields {
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
				if kind == "unknown" {
					state.Gaps = append(state.Gaps, table.Name+"."+column.ColumnName+":unknown_field_role")
				}
				if column.DataType == "ARRAY" {
					var values []string
					if string(raw) != "null" && json.Unmarshal(raw, &values) != nil {
						rows.Close()
						return state, consolidationHold("INVENTORY_INCOMPLETE")
					}
					for i, value := range values {
						s.addCensusRow(&state, table.Name, column.ColumnName, kind, keyJSON, []string{strconv.Itoa(i)}, value, value == "")
					}
				} else {
					var value string
					if string(raw) != "null" && json.Unmarshal(raw, &value) != nil {
						rows.Close()
						return state, consolidationHold("INVENTORY_INCOMPLETE")
					}
					s.addCensusRow(&state, table.Name, column.ColumnName, kind, keyJSON, nil, value, value == "" && kind != "source_membership")
				}
			}
			// Every otherwise unassigned native object stays independently readable.
			// A table name alone never establishes global or canonical ownership.
			if len(state.Rows) == start || table.Kind == "durable_history" {
				kind := "inherited"
				if table.Name == "sources" {
					kind = "source_membership"
				}
				s.addCensusRow(&state, table.Name, "", kind, keyJSON, nil, "", false)
			}
			indices := make([]int, len(state.Rows)-start)
			for i := range indices {
				indices[i] = start + i
				state.Rows[start+i].NativeVersion = version
			}
			byObject[consolidationObjectKey(table.Name, keyJSON)] = indices
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return state, err
		}
		if len(table.PrimaryKey) == 0 && table.Count > 0 {
			state.Gaps = append(state.Gaps, table.Name+":physical_native_key_read_only")
		}
	}
	// PostgreSQL joins resolve complete FK keys without retaining payloads or
	// quadratic scans of parent objects in Go.
	for _, fk := range fks {
		var child, parent *consolidationTable
		for i := range state.Tables {
			if state.Tables[i].Name == fk.TableName {
				child = &state.Tables[i]
			}
			if state.Tables[i].Name == fk.ParentTable {
				parent = &state.Tables[i]
			}
		}
		if child == nil || parent == nil {
			continue
		}
		if child.Kind == "independent_view" && fk.ParentTable != "sources" && fk.ParentTable != "ci_checkouts" && fk.ParentTable != "ci_views" {
			continue
		}
		var conditions []string
		for i, name := range fk.Columns {
			conditions = append(conditions, "c."+quoted(name)+"=p."+quoted(fk.ParentColumns[i]))
		}
		rows, err := tx.Raw("SELECT " + consolidationKeySQL(*child, "c") + "::text," + consolidationKeySQL(*parent, "p") + "::text FROM " + quoted(child.Name) + " c JOIN " + quoted(parent.Name) + " p ON " + strings.Join(conditions, " AND ")).Rows()
		if err != nil {
			return state, err
		}
		for rows.Next() {
			var ck, pk []byte
			if err := rows.Scan(&ck, &pk); err != nil {
				rows.Close()
				return state, err
			}
			canonicalChild, err := consolidationCanonicalKey(ck)
			if err != nil {
				rows.Close()
				return state, err
			}
			canonicalParent, err := consolidationCanonicalKey(pk)
			if err != nil {
				rows.Close()
				return state, err
			}
			ck, pk = []byte(canonicalChild), []byte(canonicalParent)
			for _, pi := range byObject[consolidationObjectKey(parent.Name, pk)] {
				p := state.Rows[pi]
				if p.Column != "" && p.Column != "project" && p.Column != "target_project" && !(p.Table == "rule_candidates" && p.Column == "source_project") && !(p.Table == "sources" && p.Column == "project_id") {
					continue
				}
				if p.Kind == "registry" || p.Kind == "historical_identity" || p.Global {
					continue
				}
				for _, i := range byObject[consolidationObjectKey(child.Name, ck)] {
					if state.Rows[i].Kind == "inherited" {
						state.Rows[i].Parents = append(state.Rows[i].Parents, p.Reference)
					}
				}
			}
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return state, err
		}
	}
	for i := range state.Rows {
		sort.Strings(state.Rows[i].Parents)
		state.Rows[i].Parents = compactStrings(state.Rows[i].Parents)
	}
	sort.Strings(state.Gaps)
	state.Gaps = compactStrings(state.Gaps)
	state.Fingerprint = structuralDigest(struct{ Version, Database, Snapshot, Schema, Classification string }{sourceVersion, state.Database, state.Snapshot, state.Schema, consolidationClassification(state)})
	return state, nil
}

func consolidationObjectKey(table string, key []byte) string { return table + ":" + string(key) }

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
	names = append(names, quoted("project_consolidation_backups"), quoted("project_consolidation_frames"), quoted("project_consolidation_operations"), quoted("project_consolidation_memberships"), quoted("project_consolidation_redirects"))
	sort.Strings(names)
	if len(names) == 0 {
		return consolidationHold("INVENTORY_INCOMPLETE")
	}
	return tx.Exec("LOCK TABLE " + strings.Join(names, ",") + " IN EXCLUSIVE MODE").Error
}

func consolidationNativePredicate(key JSONRaw, alias string) (string, []any, error) {
	var native map[string]json.RawMessage
	if json.Unmarshal(key, &native) != nil || len(native) == 0 {
		return "", nil, consolidationHold("UNSTABLE_NATIVE_KEY")
	}
	if raw, physical := native["__native_ctid"]; physical {
		var ctid, xmin string
		if len(native) != 2 || json.Unmarshal(raw, &ctid) != nil || json.Unmarshal(native["__native_xmin"], &xmin) != nil {
			return "", nil, consolidationHold("UNSTABLE_NATIVE_KEY")
		}
		return alias + ".ctid=?::tid AND " + alias + ".xmin::text=?", []any{ctid, xmin}, nil
	}
	var names []string
	for name := range native {
		names = append(names, name)
	}
	sort.Strings(names)
	var terms []string
	var args []any
	for _, name := range names {
		terms = append(terms, "to_jsonb("+alias+"."+quoted(name)+")=?::jsonb")
		args = append(args, string(native[name]))
	}
	return strings.Join(terms, " AND "), args, nil
}

// SQL diagnostics may contain secret values in unique keys; transports only
// expose the stable redacted error code.
func consolidationCollision(err error) error {
	if err == nil {
		return nil
	}
	return consolidationHold("COLLISION")
}
