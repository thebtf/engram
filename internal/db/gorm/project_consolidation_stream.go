package gorm

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/lib/pq"
	gormlib "gorm.io/gorm"
)

const (
	consolidationStreamFormat = "engram.owner-stream/2"
	consolidationChunkBytes   = 256 << 10
	consolidationFrameBytes   = 512 << 10
)

type consolidationFrame struct {
	Ordinal                                       uint64
	Previous, Binding, Kind, Table, NativeVersion string
	Row                                           uint64
	Chunk                                         uint32
	Last                                          bool
	Key                                           JSONRaw
	Payload                                       []byte
	Manifest                                      *consolidationBackupState
}

type consolidationStoredFrame struct {
	BackupID   string `gorm:"primaryKey"`
	Ordinal    uint64 `gorm:"primaryKey"`
	Ciphertext []byte
}

func (consolidationStoredFrame) TableName() string { return "project_consolidation_frames" }

func consolidationClassification(census consolidationCensusState) string {
	return structuralDigest(struct {
		Rows  []consolidationRow
		Gaps  []string
		Roles []consolidationRole
	}{census.Rows, census.Gaps, consolidationRoles})
}

func consolidationCipherHash(ciphertext []byte) string {
	sum := sha256.Sum256(ciphertext)
	return hex.EncodeToString(sum[:])
}

func consolidationCanonicalKey(raw []byte) (JSONRaw, error) {
	var value map[string]json.RawMessage
	if len(raw) > consolidationMetadataBytes || json.Unmarshal(raw, &value) != nil || len(value) == 0 {
		return nil, consolidationHold("UNSTABLE_NATIVE_KEY")
	}
	encoded, err := json.Marshal(value)
	return JSONRaw(encoded), err
}

// Vault's nonce||ciphertext||tag format and nil AAD remain unchanged. All
// contextual binding lives inside its authenticated plaintext, never in an
// unauthenticated transport header or a project ID used as a crypto key.
func (s *ProjectConsolidationStore) writeFrame(dst io.Writer, frame consolidationFrame) ([]byte, error) {
	var compressed bytes.Buffer
	writer := gzip.NewWriter(&compressed)
	return s.writeCompressedFrame(dst, frame, &compressed, writer)
}

func (s *ProjectConsolidationStore) writeCompressedFrame(dst io.Writer, frame consolidationFrame, compressed *bytes.Buffer, writer *gzip.Writer) ([]byte, error) {
	raw, err := json.Marshal(frame)
	if err != nil {
		return nil, err
	}
	if len(raw) > consolidationFrameBytes-28 {
		return nil, consolidationHold("FRAME_CAPACITY_EXCEEDED")
	}
	compressed.Reset()
	writer.Reset(compressed)
	if _, err := writer.Write(raw); err != nil {
		_ = writer.Close()
		return nil, err
	}
	if err := writer.Close(); err != nil {
		return nil, err
	}
	ciphertext, err := s.vault.Encrypt(compressed.String())
	if err != nil {
		return nil, consolidationHold("BACKUP_MISSING")
	}
	if len(ciphertext) > consolidationFrameBytes {
		return nil, consolidationHold("FRAME_CAPACITY_EXCEEDED")
	}
	var size [4]byte
	binary.BigEndian.PutUint32(size[:], uint32(len(ciphertext)))
	if err := consolidationWriteAll(dst, size[:]); err != nil {
		return nil, err
	}
	if err := consolidationWriteAll(dst, ciphertext); err != nil {
		return nil, err
	}
	return ciphertext, nil
}

func consolidationWriteAll(dst io.Writer, raw []byte) error {
	for len(raw) > 0 {
		n, err := dst.Write(raw)
		if n < 0 || n > len(raw) {
			return io.ErrShortWrite
		}
		raw = raw[n:]
		if err != nil {
			return err
		}
		if n == 0 {
			return io.ErrShortWrite
		}
	}
	return nil
}

func (s *ProjectConsolidationStore) readFrame(src io.Reader) (consolidationFrame, []byte, error) {
	var frame consolidationFrame
	var size [4]byte
	if _, err := io.ReadFull(src, size[:]); err != nil {
		return frame, nil, consolidationHold("BACKUP_TRUNCATED")
	}
	n := binary.BigEndian.Uint32(size[:])
	if n < 28 || n > consolidationFrameBytes {
		return frame, nil, consolidationHold("FRAME_CAPACITY_EXCEEDED")
	}
	ciphertext := make([]byte, int(n))
	if _, err := io.ReadFull(src, ciphertext); err != nil {
		return frame, nil, consolidationHold("BACKUP_TRUNCATED")
	}
	decoded, err := s.decodeFrame(ciphertext)
	return decoded, ciphertext, err
}

func (s *ProjectConsolidationStore) decodeFrame(ciphertext []byte) (consolidationFrame, error) {
	var frame consolidationFrame
	if len(ciphertext) < 28 || len(ciphertext) > consolidationFrameBytes {
		return frame, consolidationHold("FRAME_CAPACITY_EXCEEDED")
	}
	authenticated, err := s.vault.Decrypt(ciphertext)
	if err != nil {
		return frame, consolidationHold("BACKUP_AUTHENTICATION_FAILED")
	}
	reader := strings.NewReader(authenticated)
	compressed, err := gzip.NewReader(reader)
	if err != nil {
		return frame, consolidationHold("BACKUP_AUTHENTICATION_FAILED")
	}
	defer compressed.Close()
	compressed.Multistream(false)
	raw, err := io.ReadAll(io.LimitReader(compressed, consolidationFrameBytes-28+1))
	if len(raw) > consolidationFrameBytes-28 {
		return frame, consolidationHold("FRAME_CAPACITY_EXCEEDED")
	}
	if err != nil || reader.Len() != 0 || json.Unmarshal(raw, &frame) != nil || len(frame.Payload) > consolidationChunkBytes || len(frame.Key) > consolidationMetadataBytes {
		return frame, consolidationHold("BACKUP_AUTHENTICATION_FAILED")
	}
	return frame, nil
}

func (s *ProjectConsolidationStore) backupManifest(ciphertext []byte, state consolidationPlanState) (consolidationBackupState, error) {
	var manifest consolidationBackupState
	frame, err := s.decodeFrame(ciphertext)
	if err != nil {
		return manifest, err
	}
	if frame.Kind != "manifest" || frame.Ordinal != 0 || frame.Previous != "" || frame.Manifest == nil {
		return manifest, consolidationHold("BACKUP_AUTHENTICATION_FAILED")
	}
	manifest = *frame.Manifest
	if manifest.Format != consolidationStreamFormat || manifest.PlanID != state.ID || manifest.Fingerprint != state.Fingerprint || manifest.Source != state.Census.Database || manifest.Schema != state.Census.Schema || manifest.Snapshot != state.Census.Snapshot || manifest.NativeSnapshot != state.Census.NativeSnapshot || manifest.Classification != consolidationClassification(state.Census) || structuralDigest(manifest.Tables) != structuralDigest(state.Census.Tables) || frame.Binding != structuralDigest(manifest) || frame.Table != "" || frame.Row != 0 || frame.Chunk != 0 || frame.Last || len(frame.Key) != 0 || len(frame.Payload) != 0 || frame.NativeVersion != "" {
		return manifest, consolidationHold("BACKUP_MANIFEST_MISMATCH")
	}
	return manifest, nil
}

func (s *ProjectConsolidationStore) exportFrames(tx *gormlib.DB, manifest consolidationBackupState, dst io.Writer) error {
	binding := structuralDigest(manifest)
	ordinal := uint64(0)
	previous := ""
	var compressed bytes.Buffer
	writer := gzip.NewWriter(&compressed)
	emit := func(frame consolidationFrame) error {
		frame.Ordinal, frame.Previous, frame.Binding = ordinal, previous, binding
		ciphertext, err := s.writeCompressedFrame(dst, frame, &compressed, writer)
		if err == nil {
			ordinal++
			previous = consolidationCipherHash(ciphertext)
		}
		return err
	}
	if err := emit(consolidationFrame{Kind: "manifest", Manifest: &manifest}); err != nil {
		return err
	}
	for _, table := range manifest.Tables {
		// Keep the ordered outer relation key-only. Each lateral invocation
		// serializes one typed native row once and orders only that row's chunks.
		// The final native-key prefix permits incremental, not corpus-wide, sort.
		keys := []string{"k.ctid", "k.xmin"}
		var match []string
		for _, key := range table.PrimaryKey {
			keys = append(keys, "k."+quoted(key))
			match = append(match, "t."+quoted(key)+"=k."+quoted(key))
		}
		if len(match) == 0 {
			match = append(match, "t.ctid=k.ctid", "t.xmin=k.xmin")
		}
		order := consolidationOrderSQL(table, "k")
		query := "SELECT " + consolidationKeySQL(table, "k") + "::text,k.xmin::text,n.part,n.last,n.payload FROM (SELECT " + strings.Join(keys, ",") + " FROM " + quoted(table.Name) + " k ORDER BY " + order + " OFFSET 0) k CROSS JOIN LATERAL (WITH body AS MATERIALIZED (SELECT convert_to(to_jsonb(t)::text,'UTF8') AS body FROM " + quoted(table.Name) + " t WHERE " + strings.Join(match, " AND ") + ") SELECT n.part,(n.part+1)*? >= octet_length(b.body) AS last,substring(b.body FROM n.part*?+1 FOR ?) AS payload FROM body b CROSS JOIN LATERAL generate_series(0,(octet_length(b.body)-1)/?) n(part) ORDER BY n.part) n ORDER BY " + order + ",n.part"
		rows, err := tx.Raw(query, consolidationChunkBytes, consolidationChunkBytes, consolidationChunkBytes, consolidationChunkBytes).Rows()
		if err != nil {
			return err
		}
		var count int64
		for rows.Next() {
			frame := consolidationFrame{Kind: "row", Table: table.Name, Row: uint64(count + 1)}
			var key []byte
			if err := rows.Scan(&key, &frame.NativeVersion, &frame.Chunk, &frame.Last, &frame.Payload); err != nil {
				rows.Close()
				return err
			}
			frame.Key, err = consolidationCanonicalKey(key)
			if err != nil {
				rows.Close()
				return err
			}
			if err := emit(frame); err != nil {
				rows.Close()
				return err
			}
			if frame.Last {
				count++
			}
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		if count != table.Count {
			return consolidationHold("STALE_PLAN")
		}
	}
	current, err := consolidationSequences(tx)
	if err != nil {
		return err
	}
	if structuralDigest(current) != structuralDigest(manifest.Sequences) {
		return consolidationHold("SEQUENCE_CHANGED")
	}
	return emit(consolidationFrame{Kind: "final", Manifest: &manifest})
}

func consolidationSetSequence(tx *gormlib.DB, seq consolidationSequence) error {
	// RESTART allocates transactional sequence storage; setval then preserves
	// the exact is_called state without leaking changes after a rollback.
	if err := tx.Exec("ALTER SEQUENCE " + quoted(seq.Name) + " RESTART WITH " + strconv.FormatInt(seq.LastValue, 10)).Error; err != nil {
		return err
	}
	return tx.Exec(`SELECT setval(?::regclass,?,?)`, quoted(seq.Name), seq.LastValue, seq.IsCalled).Error
}

// The temporary buffer contains only one original row at a time. Encrypted
// original frames remain in migration metadata for conservation and recovery.
func consolidationRowBuffer(tx *gormlib.DB) error {
	if err := tx.Exec(`CREATE TEMP TABLE IF NOT EXISTS engram_owner_row_chunks(part BIGINT PRIMARY KEY,payload BYTEA NOT NULL) ON COMMIT DROP`).Error; err != nil {
		return err
	}
	if err := tx.Exec(`CREATE TEMP TABLE IF NOT EXISTS engram_owner_row(object JSONB NOT NULL) ON COMMIT DROP`).Error; err != nil {
		return err
	}
	if err := tx.Exec(`CREATE TEMP TABLE IF NOT EXISTS engram_owner_native_rows(table_name TEXT,row_key JSONB,native_version TEXT,target_key JSONB,PRIMARY KEY(table_name,row_key)) ON COMMIT DROP`).Error; err != nil {
		return err
	}
	if err := tx.Exec(`CREATE TEMP TABLE IF NOT EXISTS engram_owner_multiplicity(table_name TEXT,digest TEXT,seen BIGINT,PRIMARY KEY(table_name,digest)) ON COMMIT DROP`).Error; err != nil {
		return err
	}
	if err := tx.Exec(`TRUNCATE pg_temp.engram_owner_multiplicity`).Error; err != nil {
		return err
	}
	return tx.Exec(`TRUNCATE pg_temp.engram_owner_row_chunks,pg_temp.engram_owner_row`).Error
}

// Only keys and digests cross row boundaries; ciphertext stays in PostgreSQL.
func consolidationControlBuffer(tx *gormlib.DB) error {
	var exists bool
	if err := tx.Raw(`SELECT to_regclass('pg_temp.engram_owner_controls') IS NOT NULL`).Scan(&exists).Error; err != nil || exists {
		return err
	}
	if err := tx.Exec(`CREATE TEMP TABLE engram_owner_controls(table_name TEXT,row_key JSONB,digest TEXT NOT NULL,PRIMARY KEY(table_name,row_key)) ON COMMIT DROP;
	 CREATE TEMP TABLE engram_owner_original_controls(LIKE pg_temp.engram_owner_controls INCLUDING ALL) ON COMMIT DROP;
	 CREATE TEMP TABLE engram_owner_additions(LIKE pg_temp.engram_owner_controls INCLUDING ALL) ON COMMIT DROP`).Error; err != nil {
		return err
	}
	tables, err := consolidationDataTables(tx)
	if err != nil {
		return err
	}
	for _, table := range tables {
		if !consolidationMetadataTable(table.Name) {
			continue
		}
		query := "INSERT INTO pg_temp.engram_owner_controls SELECT ?," + consolidationKeySQL(table, "t") + ",encode(sha256(convert_to(to_jsonb(t)::text,'UTF8')),'hex') FROM " + quoted(table.Name) + " t"
		if err := tx.Exec(query, table.Name).Error; err != nil {
			return err
		}
	}
	return tx.Exec(`INSERT INTO pg_temp.engram_owner_original_controls SELECT * FROM pg_temp.engram_owner_controls`).Error
}

func consolidationExpectControl(tx *gormlib.DB, table string, key JSONRaw, object JSONRaw) error {
	if !consolidationMetadataTable(table) {
		return consolidationHold("INVENTORY_INCOMPLETE")
	}
	query := "INSERT INTO pg_temp.engram_owner_controls(table_name,row_key,digest) SELECT ?,?::jsonb,encode(sha256(convert_to(to_jsonb(jsonb_populate_record(NULL::" + quoted(table) + ",?::jsonb))::text,'UTF8')),'hex') ON CONFLICT(table_name,row_key) DO UPDATE SET digest=excluded.digest"
	return tx.Exec(query, table, string(key), string(object)).Error
}

func consolidationCheckControls(tx *gormlib.DB) error {
	tables, err := consolidationDataTables(tx)
	if err != nil {
		return err
	}
	for _, table := range tables {
		if !consolidationMetadataTable(table.Name) {
			continue
		}
		var matches bool
		query := "SELECT NOT EXISTS(SELECT 1 FROM (SELECT " + consolidationKeySQL(table, "t") + " AS row_key,encode(sha256(convert_to(to_jsonb(t)::text,'UTF8')),'hex') AS digest FROM " + quoted(table.Name) + " t) a FULL JOIN (SELECT row_key,digest FROM pg_temp.engram_owner_controls WHERE table_name=?) e USING(row_key) WHERE a.digest IS DISTINCT FROM e.digest)"
		if err := tx.Raw(query, table.Name).Scan(&matches).Error; err != nil {
			return err
		}
		if !matches {
			return consolidationHold("CONSERVATION_FAILED")
		}
	}
	return nil
}

func consolidationBackupObject(backup consolidationBackupRow) (JSONRaw, error) {
	raw, err := json.Marshal(map[string]any{"backup_id": backup.BackupID, "plan_id": backup.PlanID, "plan_fingerprint": backup.PlanFingerprint, "ciphertext": "\\x" + hex.EncodeToString(backup.Ciphertext), "restore_identity": backup.RestoreIdentity, "restore_snapshot": backup.RestoreSnapshot, "restore_verified_at": backup.RestoreVerifiedAt})
	return JSONRaw(raw), err
}

func consolidationExpectBackup(tx *gormlib.DB, backup consolidationBackupRow) error {
	object, err := consolidationBackupObject(backup)
	if err != nil {
		return err
	}
	key, _ := json.Marshal(map[string]string{"backup_id": backup.BackupID})
	return consolidationExpectControl(tx, "project_consolidation_backups", JSONRaw(key), object)
}

func consolidationExpectFrame(tx *gormlib.DB, frame consolidationStoredFrame) error {
	key, _ := json.Marshal(map[string]any{"backup_id": frame.BackupID, "ordinal": frame.Ordinal})
	object, err := json.Marshal(map[string]any{"backup_id": frame.BackupID, "ordinal": frame.Ordinal, "ciphertext": "\\x" + hex.EncodeToString(frame.Ciphertext)})
	if err != nil {
		return err
	}
	return consolidationExpectControl(tx, "project_consolidation_frames", JSONRaw(key), JSONRaw(object))
}

func consolidationExpectProject(tx *gormlib.DB, project Project) error {
	object, err := consolidationProjectObject(project)
	if err != nil {
		return err
	}
	key, _ := json.Marshal(map[string]string{"id": project.ID})
	return tx.Exec(`INSERT INTO pg_temp.engram_owner_additions SELECT 'projects',?::jsonb,encode(sha256(convert_to(to_jsonb(jsonb_populate_record(NULL::projects,?::jsonb))::text,'UTF8')),'hex')`, string(key), string(object)).Error
}

type consolidationFrameWalk struct {
	Manifest          consolidationBackupState
	Binding, Previous string
	Ordinal           uint64
	TableIndex        int
	Row               uint64
	Chunk             uint32
	Key               JSONRaw
	Version           string
	Finished          bool
}

func (walk *consolidationFrameWalk) accept(frame consolidationFrame, ciphertext []byte) error {
	if walk.Finished || frame.Ordinal != walk.Ordinal || frame.Previous != walk.Previous || frame.Binding != walk.Binding {
		return consolidationHold("BACKUP_CHAIN_MISMATCH")
	}
	if frame.Kind == "final" {
		for walk.TableIndex < len(walk.Manifest.Tables) && walk.Manifest.Tables[walk.TableIndex].Count == 0 {
			walk.TableIndex++
		}
		if frame.Manifest == nil || structuralDigest(*frame.Manifest) != walk.Binding || walk.Chunk != 0 || walk.TableIndex != len(walk.Manifest.Tables) || frame.Table != "" || len(frame.Payload) != 0 || frame.Row != 0 || frame.Chunk != 0 || frame.Last || len(frame.Key) != 0 || frame.NativeVersion != "" {
			return consolidationHold("BACKUP_TRUNCATED")
		}
		walk.Finished = true
	} else {
		for walk.TableIndex < len(walk.Manifest.Tables) && walk.Manifest.Tables[walk.TableIndex].Count == 0 {
			walk.TableIndex++
		}
		if frame.Kind != "row" || frame.Manifest != nil || walk.TableIndex >= len(walk.Manifest.Tables) || frame.Table != walk.Manifest.Tables[walk.TableIndex].Name || frame.Row != walk.Row+1 || frame.Chunk != walk.Chunk || len(frame.Payload) == 0 || (!frame.Last && len(frame.Payload) != consolidationChunkBytes) {
			return consolidationHold("BACKUP_CHAIN_MISMATCH")
		}
		if walk.Chunk == 0 {
			key, err := consolidationCanonicalKey(frame.Key)
			if err != nil || string(key) != string(frame.Key) || frame.NativeVersion == "" {
				return consolidationHold("BACKUP_CHAIN_MISMATCH")
			}
			walk.Key = key
			walk.Version = frame.NativeVersion
		}
		if string(frame.Key) != string(walk.Key) || frame.NativeVersion != walk.Version {
			return consolidationHold("BACKUP_CHAIN_MISMATCH")
		}
		walk.Chunk++
		if frame.Last {
			walk.Row++
			walk.Chunk = 0
			if walk.Row == uint64(walk.Manifest.Tables[walk.TableIndex].Count) {
				walk.TableIndex++
				walk.Row = 0
			}
		}
	}
	walk.Ordinal++
	walk.Previous = consolidationCipherHash(ciphertext)
	return nil
}

func (s *ProjectConsolidationStore) consumeRow(tx *gormlib.DB, frame consolidationFrame, columns []consolidationColumn, edits map[string][]consolidationRowEdit, restore, changed bool) error {
	if err := tx.Exec(`INSERT INTO pg_temp.engram_owner_row_chunks(part,payload) VALUES(?,?)`, frame.Chunk, frame.Payload).Error; err != nil {
		return err
	}
	if !frame.Last {
		return nil
	}
	if err := tx.Exec(`INSERT INTO pg_temp.engram_owner_row(object) SELECT convert_from(string_agg(payload,''::bytea ORDER BY part),'UTF8')::jsonb FROM pg_temp.engram_owner_row_chunks`).Error; err != nil {
		return consolidationHold("BACKUP_ROW_INVALID")
	}
	if consolidationMetadataTable(frame.Table) {
		var compatible bool
		if err := tx.Raw(`SELECT NOT EXISTS(SELECT 1 FROM pg_temp.engram_owner_original_controls WHERE table_name=? AND row_key=?::jsonb AND digest<>(SELECT encode(sha256(convert_to(object::text,'UTF8')),'hex') FROM pg_temp.engram_owner_row))`, frame.Table, string(frame.Key)).Scan(&compatible).Error; err != nil {
			return err
		}
		if !compatible {
			return consolidationHold("CONSERVATION_FAILED")
		}
		if changed {
			var original bool
			if err := tx.Raw(`SELECT EXISTS(SELECT 1 FROM pg_temp.engram_owner_original_controls WHERE table_name=? AND row_key=?::jsonb)`, frame.Table, string(frame.Key)).Scan(&original).Error; err != nil {
				return err
			}
			if !original {
				return consolidationHold("CONSERVATION_FAILED")
			}
			return tx.Exec(`TRUNCATE pg_temp.engram_owner_row_chunks,pg_temp.engram_owner_row`).Error
		}
		var present bool
		if err := tx.Raw(`SELECT EXISTS(SELECT 1 FROM pg_temp.engram_owner_controls WHERE table_name=? AND row_key=?::jsonb)`, frame.Table, string(frame.Key)).Scan(&present).Error; err != nil {
			return err
		}
		if !present {
			for _, buffer := range []string{"engram_owner_controls", "engram_owner_original_controls"} {
				if err := tx.Exec("INSERT INTO pg_temp."+buffer+" SELECT ?,?::jsonb,encode(sha256(convert_to(object::text,'UTF8')),'hex') FROM pg_temp.engram_owner_row", frame.Table, string(frame.Key)).Error; err != nil {
					return err
				}
			}
		}
		restore = !present
	}
	if restore && frame.Table != "migrations" {
		var fields []string
		for _, column := range columns {
			if column.TableName == frame.Table && column.IsGenerated == "NEVER" {
				fields = append(fields, quoted(column.ColumnName))
			}
		}
		if len(fields) == 0 {
			return consolidationHold("SCHEMA_MISMATCH")
		}
		names := strings.Join(fields, ",")
		if err := tx.Exec("INSERT INTO " + quoted(frame.Table) + " (" + names + ") OVERRIDING SYSTEM VALUE SELECT " + names + " FROM pg_temp.engram_owner_row o CROSS JOIN LATERAL jsonb_populate_record(NULL::" + quoted(frame.Table) + ",o.object) r").Error; err != nil {
			return consolidationHold("RESTORE_CONSTRAINT_FAILED")
		}
	}
	key, err := consolidationCanonicalKey(frame.Key)
	if err != nil {
		return err
	}
	for _, edit := range edits[consolidationObjectKey(frame.Table, frame.Key)] {
		row, m := edit.Row, edit.Mapping
		if m.ProjectID == "" || row.Global || row.Column == "" || !consolidationMutable(row.Kind) {
			continue
		}
		path := append([]string{row.Column}, row.Path...)
		result := tx.Exec(`UPDATE pg_temp.engram_owner_row SET object=jsonb_set(object,?::text[],to_jsonb(?::text),false) WHERE COALESCE(object #>> ?::text[],'')=?`, pq.Array(path), m.ProjectID, pq.Array(path), row.Selector)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return consolidationHold("STALE_MAPPING")
		}
		var native map[string]json.RawMessage
		_ = json.Unmarshal(key, &native)
		if len(row.Path) == 0 && native[row.Column] != nil {
			native[row.Column], _ = json.Marshal(m.ProjectID)
			encoded, _ := json.Marshal(native)
			key = JSONRaw(encoded)
		}
	}
	predicate, args, err := consolidationNativePredicate(key, "t")
	if err != nil {
		return err
	}
	var native map[string]json.RawMessage
	_ = json.Unmarshal(key, &native)
	if native["__native_ctid"] != nil {
		// Physical source tuple addresses cannot survive a restore. Whole-row
		// equality and per-table multiplicity are checked instead; no mutation
		// or canonical claim is allowed on such an occurrence.
		predicate = "to_jsonb(t)=(SELECT object FROM pg_temp.engram_owner_row)"
		args = nil
	}
	var equal bool
	query := "SELECT count(*)=1 AND bool_and(to_jsonb(t)=o.object) FROM " + quoted(frame.Table) + " t CROSS JOIN pg_temp.engram_owner_row o WHERE " + predicate
	if native["__native_ctid"] != nil {
		if err := tx.Exec(`INSERT INTO pg_temp.engram_owner_multiplicity(table_name,digest,seen) SELECT ?,encode(sha256(convert_to(object::text,'UTF8')),'hex'),1 FROM pg_temp.engram_owner_row ON CONFLICT(table_name,digest) DO UPDATE SET seen=engram_owner_multiplicity.seen+1`, frame.Table).Error; err != nil {
			return err
		}
		query = "SELECT count(*) >= (SELECT seen FROM pg_temp.engram_owner_multiplicity WHERE table_name=? AND digest=(SELECT encode(sha256(convert_to(object::text,'UTF8')),'hex') FROM pg_temp.engram_owner_row)) AND COALESCE(bool_and(to_jsonb(t)=o.object),false) FROM " + quoted(frame.Table) + " t CROSS JOIN pg_temp.engram_owner_row o WHERE " + predicate
		args = []any{frame.Table}
	}
	if err := tx.Raw(query, args...).Scan(&equal).Error; err != nil {
		return err
	}
	if !equal {
		return consolidationHold("CONSERVATION_FAILED")
	}
	if native["__native_ctid"] != nil {
		query := "UPDATE pg_temp.engram_owner_native_rows SET target_key=(SELECT jsonb_build_object('__native_ctid',t.ctid::text,'__native_xmin',t.xmin::text) FROM " + quoted(frame.Table) + " t WHERE to_jsonb(t)=(SELECT object FROM pg_temp.engram_owner_row) ORDER BY t.ctid OFFSET (SELECT seen-1 FROM pg_temp.engram_owner_multiplicity WHERE table_name=? AND digest=(SELECT encode(sha256(convert_to(object::text,'UTF8')),'hex') FROM pg_temp.engram_owner_row)) LIMIT 1) WHERE table_name=? AND row_key=?::jsonb"
		if err := tx.Exec(query, frame.Table, frame.Table, string(frame.Key)).Error; err != nil {
			return err
		}
	}
	return tx.Exec(`DELETE FROM pg_temp.engram_owner_row_chunks; DELETE FROM pg_temp.engram_owner_row`).Error
}

type consolidationRowEdit struct {
	Row     consolidationRow
	Mapping ConsolidationMapping
}

func (s *ProjectConsolidationStore) walkStored(tx *gormlib.DB, backupID string, state consolidationPlanState, edits *consolidationPlanState, restore bool) error {
	var header consolidationStoredFrame
	if err := tx.Where("backup_id=? AND ordinal=0", backupID).First(&header).Error; err != nil {
		return consolidationHold("BACKUP_MISSING")
	}
	manifest, err := s.backupManifest(header.Ciphertext, state)
	if err != nil {
		return err
	}
	walk := consolidationFrameWalk{Manifest: manifest, Binding: structuralDigest(manifest), Previous: consolidationCipherHash(header.Ciphertext), Ordinal: 1}
	columns, err := consolidationColumns(tx)
	if err != nil {
		return err
	}
	byObject := map[string][]consolidationRowEdit{}
	if edits != nil {
		if len(edits.Census.Rows) != len(edits.Mapping) {
			return consolidationHold("STALE_MAPPING")
		}
		for i, row := range edits.Census.Rows {
			key := consolidationObjectKey(row.Table, row.Key)
			byObject[key] = append(byObject[key], consolidationRowEdit{row, edits.Mapping[i]})
		}
	}
	if err := consolidationRowBuffer(tx); err != nil {
		return err
	}
	if err := tx.Exec(`TRUNCATE pg_temp.engram_owner_native_rows`).Error; err != nil {
		return err
	}
	versions := make(map[string]string, len(state.Census.Rows))
	for _, row := range state.Census.Rows {
		versions[consolidationObjectKey(row.Table, row.Key)] = row.NativeVersion
	}
	for {
		var stored consolidationStoredFrame
		if err := tx.Where("backup_id=? AND ordinal=?", backupID, walk.Ordinal).First(&stored).Error; err != nil {
			return consolidationHold("BACKUP_TRUNCATED")
		}
		frame, err := s.decodeFrame(stored.Ciphertext)
		if err != nil {
			return err
		}
		if err := walk.accept(frame, stored.Ciphertext); err != nil {
			return err
		}
		if walk.Finished {
			break
		}
		if frame.Chunk == 0 {
			if versions[consolidationObjectKey(frame.Table, frame.Key)] != frame.NativeVersion {
				return consolidationHold("BACKUP_NATIVE_VERSION_MISMATCH")
			}
			if err := tx.Exec(`INSERT INTO pg_temp.engram_owner_native_rows(table_name,row_key,native_version) VALUES(?,?::jsonb,?)`, frame.Table, string(frame.Key), frame.NativeVersion).Error; err != nil {
				return consolidationHold("BACKUP_NATIVE_KEY_REPLAY")
			}
		}
		if err := s.consumeRow(tx, frame, columns, byObject, restore, edits != nil); err != nil {
			return err
		}
	}
	var count int64
	if err := tx.Model(&consolidationStoredFrame{}).Where("backup_id=?", backupID).Count(&count).Error; err != nil {
		return err
	}
	if count != int64(walk.Ordinal) {
		return consolidationHold("BACKUP_CHAIN_MISMATCH")
	}
	for _, table := range manifest.Tables {
		if consolidationMetadataTable(table.Name) {
			continue
		}
		var actual int64
		if err := tx.Table(table.Name).Count(&actual).Error; err != nil {
			return err
		}
		expected := table.Count
		if edits != nil && table.Name == "projects" {
			var added int64
			if err := tx.Raw(`SELECT count(*) FROM pg_temp.engram_owner_additions WHERE table_name='projects'`).Scan(&added).Error; err != nil {
				return err
			}
			expected += added
		}
		if actual != expected {
			return consolidationHold("CONSERVATION_FAILED")
		}
	}
	var additionsMatch bool
	if err := tx.Raw(`SELECT NOT EXISTS(SELECT 1 FROM pg_temp.engram_owner_additions e LEFT JOIN projects p ON jsonb_build_object('id',p.id)=e.row_key WHERE e.table_name='projects' AND e.digest IS DISTINCT FROM encode(sha256(convert_to(to_jsonb(p)::text,'UTF8')),'hex'))`).Scan(&additionsMatch).Error; err != nil {
		return err
	}
	if !additionsMatch {
		return consolidationHold("CONSERVATION_FAILED")
	}
	return consolidationCheckControls(tx)
}

func (s *ProjectConsolidationStore) VerifyBackup(ctx context.Context, token, fingerprint string, src io.Reader) (string, error) {
	if err := s.available(); err != nil {
		return "", err
	}
	if src == nil {
		return "", consolidationHold("BACKUP_MISSING")
	}
	state, err := s.plan(token, fingerprint)
	if err != nil {
		return "", err
	}
	if err := s.distinctTarget(ctx); err != nil {
		return "", err
	}
	frame, header, err := s.readFrame(src)
	if err != nil {
		return "", err
	}
	manifest, err := s.backupManifest(header, state)
	if err != nil {
		return "", err
	}
	if frame.Kind != "manifest" {
		return "", consolidationHold("BACKUP_MANIFEST_MISMATCH")
	}
	// Select the authenticated inventory's recipe before any freshness check.
	historical, err := consolidationHistoricalCensus(state.Census)
	if err != nil {
		return "", err
	}
	fresh := s.fresh
	if historical {
		fresh = s.freshHistorical
	}
	var sourceSchema string
	checkSource := func() error {
		return s.db.WithContext(ctx).Transaction(func(source *gormlib.DB) error {
			if err := fresh(source, state.Census); err != nil {
				return err
			}
			schema, _, _, err := consolidationSchema(source)
			if err != nil {
				return err
			}
			if sourceSchema != "" && sourceSchema != schema {
				return consolidationHold("STALE_PLAN")
			}
			sourceSchema = schema
			sequences, err := consolidationSequences(source)
			if err != nil {
				return err
			}
			if structuralDigest(sequences) != structuralDigest(manifest.Sequences) {
				return consolidationHold("SEQUENCE_CHANGED")
			}
			return nil
		}, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	}
	if err := checkSource(); err != nil {
		return "", err
	}
	id := uuid.NewSHA1(uuid.NameSpaceOID, []byte(consolidationCipherHash(header))).String()
	err = s.restoreDB.WithContext(ctx).Transaction(func(tx *gormlib.DB) error {
		if err := tx.Exec("SET LOCAL TimeZone='UTC'; SET LOCAL extra_float_digits=3; SET LOCAL row_security=off").Error; err != nil {
			return err
		}
		if err := consolidationLock(tx); err != nil {
			return err
		}
		identity, err := consolidationDatabaseIdentity(tx)
		if err != nil {
			return err
		}
		checkSchema := func() error {
			schema, _, _, err := consolidationSchema(tx)
			if err != nil {
				return err
			}
			if schema != sourceSchema {
				return consolidationHold("SCHEMA_MISMATCH")
			}
			return nil
		}
		if err := checkSchema(); err != nil {
			return err
		}
		if err := consolidationControlBuffer(tx); err != nil {
			return err
		}
		var backup consolidationBackupRow
		priorErr := tx.Where("backup_id=?", id).First(&backup).Error
		replay := priorErr == nil
		if priorErr != nil && !errors.Is(priorErr, gormlib.ErrRecordNotFound) {
			return priorErr
		}
		if replay {
			snapshot, err := consolidationRevision(tx)
			if err != nil {
				return err
			}
			if backup.PlanID != state.ID || backup.PlanFingerprint != fingerprint || !bytes.Equal(backup.Ciphertext, header) || backup.RestoreIdentity != identity || backup.RestoreSnapshot != snapshot || backup.RestoreVerifiedAt.IsZero() {
				return consolidationHold("STALE_PLAN")
			}
		} else {
			backup = consolidationBackupRow{BackupID: id, PlanID: state.ID, PlanFingerprint: fingerprint, Ciphertext: header, RestoreIdentity: identity, RestoreSnapshot: "pending"}
			if err := tx.Create(&backup).Error; err != nil {
				return err
			}
			if err := consolidationExpectBackup(tx, backup); err != nil {
				return err
			}
			stored := consolidationStoredFrame{BackupID: id, Ordinal: 0, Ciphertext: header}
			if err := tx.Create(&stored).Error; err != nil {
				return err
			}
			if err := consolidationExpectFrame(tx, stored); err != nil {
				return err
			}
		}
		walk := consolidationFrameWalk{Manifest: manifest, Binding: structuralDigest(manifest), Previous: consolidationCipherHash(header), Ordinal: 1}
		for !walk.Finished {
			frame, ciphertext, err := s.readFrame(src)
			if err != nil {
				return err
			}
			if err := walk.accept(frame, ciphertext); err != nil {
				return err
			}
			stored := consolidationStoredFrame{BackupID: id, Ordinal: frame.Ordinal, Ciphertext: ciphertext}
			if replay {
				var original consolidationStoredFrame
				if err := tx.Where("backup_id=? AND ordinal=?", id, frame.Ordinal).First(&original).Error; err != nil || !bytes.Equal(original.Ciphertext, ciphertext) {
					return consolidationHold("BACKUP_CHAIN_MISMATCH")
				}
			} else {
				if err := tx.Create(&stored).Error; err != nil {
					return err
				}
				if err := consolidationExpectFrame(tx, stored); err != nil {
					return err
				}
			}
		}
		var trailing [1]byte
		n, err := io.ReadFull(src, trailing[:])
		if n != 0 || err != io.EOF {
			return consolidationHold("BACKUP_TRAILING_DATA")
		}
		nonempty := replay
		for _, table := range manifest.Tables {
			if table.Name == "migrations" || consolidationMetadataTable(table.Name) {
				continue
			}
			var count int64
			if err := tx.Table(table.Name).Count(&count).Error; err != nil {
				return err
			}
			nonempty = nonempty || count != 0
		}
		if !nonempty {
			if err := s.restoreStored(tx, id, state); err != nil {
				return err
			}
		} else if err := s.walkStored(tx, id, state, nil, false); err != nil {
			return err
		}
		if err := checkSchema(); err != nil {
			return err
		}
		if err := checkSource(); err != nil {
			return err
		}
		if err := s.verifyCredentials(ctx, tx); err != nil {
			return err
		}
		for _, seq := range manifest.Sequences {
			if !nonempty {
				if err := consolidationSetSequence(tx, seq); err != nil {
					return err
				}
			}
		}
		sequences, err := consolidationSequences(tx)
		if err != nil {
			return err
		}
		if structuralDigest(sequences) != structuralDigest(manifest.Sequences) {
			return consolidationHold("SEQUENCE_CHANGED")
		}
		if replay {
			return consolidationCheckControls(tx)
		}
		backup.RestoreVerifiedAt = time.Now().UTC().Truncate(time.Microsecond)
		if err := tx.Model(&consolidationBackupRow{}).Where("backup_id=?", id).Update("restore_verified_at", backup.RestoreVerifiedAt).Error; err != nil {
			return err
		}
		backup.RestoreSnapshot, err = consolidationRevision(tx)
		if err != nil {
			return err
		}
		// This receipt-only UPDATE retains the INSERT's transaction xmin.
		if err := tx.Model(&consolidationBackupRow{}).Where("backup_id=?", id).Update("restore_snapshot", backup.RestoreSnapshot).Error; err != nil {
			return err
		}
		if err := consolidationExpectBackup(tx, backup); err != nil {
			return err
		}
		return consolidationCheckControls(tx)
	})
	return id, err
}

func (s *ProjectConsolidationStore) restoreStored(tx *gormlib.DB, id string, state consolidationPlanState) error {
	var fks []struct {
		TableName, ConstraintName     string
		Deferrable, InitiallyDeferred bool
	}
	if err := tx.Raw(`SELECT cl.relname AS table_name,c.conname AS constraint_name,c.condeferrable AS deferrable,c.condeferred AS initially_deferred FROM pg_constraint c JOIN pg_class cl ON cl.oid=c.conrelid JOIN pg_namespace n ON n.oid=cl.relnamespace WHERE n.nspname=current_schema() AND c.contype='f' ORDER BY cl.relname,c.conname`).Scan(&fks).Error; err != nil {
		return err
	}
	for _, fk := range fks {
		if err := tx.Exec("ALTER TABLE " + quoted(fk.TableName) + " ALTER CONSTRAINT " + quoted(fk.ConstraintName) + " DEFERRABLE INITIALLY DEFERRED").Error; err != nil {
			return err
		}
	}
	restoreGuards, err := consolidationSuspendWriteGuards(tx)
	if err != nil {
		return err
	}
	if err := s.walkStored(tx, id, state, nil, true); err != nil {
		return err
	}
	if err := tx.Exec(`SET CONSTRAINTS ALL IMMEDIATE`).Error; err != nil {
		return consolidationHold("RESTORE_CONSTRAINT_FAILED")
	}
	if err := restoreGuards(); err != nil {
		return err
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
	return s.walkStored(tx, id, state, nil, false)
}

func consolidationMutable(kind string) bool {
	return kind == "tenant" || kind == "array" || kind == "json_addressing" || kind == "source_membership"
}

// SQL identifiers are selected exclusively from the sealed native census;
// clients supply evidence references, never tables, columns or SQL statements.
func consolidationMutation(tx *gormlib.DB, row consolidationRow, target string) (JSONRaw, error) {
	predicate, args, err := consolidationNativePredicate(row.Key, "t")
	if err != nil {
		return nil, err
	}
	var native map[string]json.RawMessage
	_ = json.Unmarshal(row.Key, &native)
	if native["__native_ctid"] != nil {
		return nil, consolidationHold("UNSTABLE_NATIVE_KEY")
	}
	path := append([]string{row.Column}, row.Path...)
	query := "UPDATE " + quoted(row.Table) + " t SET " + quoted(row.Column) + "=r." + quoted(row.Column) + " FROM pg_temp.engram_owner_row o CROSS JOIN LATERAL jsonb_populate_record(NULL::" + quoted(row.Table) + ",o.object) r WHERE " + predicate
	// Populate only the requested typed field; the full native body stays in
	// PostgreSQL, including exact vector, timestamp and credential bytes.
	if err := tx.Exec(`TRUNCATE pg_temp.engram_owner_row`).Error; err != nil {
		return nil, err
	}
	selectSQL := "INSERT INTO pg_temp.engram_owner_row(object) SELECT jsonb_set(to_jsonb(t),?::text[],to_jsonb(?::text),false) FROM " + quoted(row.Table) + " t WHERE " + predicate + " AND COALESCE(to_jsonb(t)#>>?::text[],'')=?"
	selectArgs := append([]any{pq.Array(path), target}, args...)
	selectArgs = append(selectArgs, pq.Array(path), row.Selector)
	result := tx.Exec(selectSQL, selectArgs...)
	if result.Error != nil {
		return nil, consolidationCollision(result.Error)
	}
	if result.RowsAffected != 1 {
		return nil, consolidationHold("STALE_MAPPING")
	}
	result = tx.Exec(query, args...)
	if result.Error != nil {
		return nil, consolidationCollision(result.Error)
	}
	if result.RowsAffected != 1 {
		return nil, consolidationHold("STALE_MAPPING")
	}
	if len(row.Path) == 0 && native[row.Column] != nil {
		native[row.Column], _ = json.Marshal(target)
	}
	encoded, err := json.Marshal(native)
	return JSONRaw(encoded), err
}
