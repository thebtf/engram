package gorm

import (
	"bytes"
	"context"
	"crypto/md5"
	"database/sql"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/lib/pq"
	"github.com/stretchr/testify/require"
	"github.com/thebtf/engram/internal/config"
	vaultcrypto "github.com/thebtf/engram/internal/crypto"
	"gorm.io/driver/postgres"
	gormlib "gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func TestProjectConsolidationStreamIntegrity(t *testing.T) {
	vault, err := vaultcrypto.OpenExistingVault(&config.Config{EncryptionKey: strings.Repeat("ab", 32)})
	require.NoError(t, err)
	store := NewProjectConsolidationStore(nil, vault, nil)
	state := consolidationPlanState{ID: "fixture-plan", Fingerprint: "fixture-fingerprint", Census: consolidationCensusState{Database: "fixture-source", Schema: "fixture-schema", Snapshot: "fixture-revision", NativeSnapshot: "fixture-native-snapshot", Tables: []consolidationTable{{Name: "session_transcripts", Count: 1, PrimaryKey: pq.StringArray{"id"}}}}}
	manifest := consolidationBackupState{Format: consolidationStreamFormat, PlanID: state.ID, Fingerprint: state.Fingerprint, Source: state.Census.Database, Schema: state.Census.Schema, Snapshot: state.Census.Snapshot, NativeSnapshot: state.Census.NativeSnapshot, Classification: consolidationClassification(state.Census), Tables: state.Census.Tables}
	binding := structuralDigest(manifest)
	// The measured largest owner row is 571491 JSON UTF-8 bytes. Its body is
	// not this synthetic fixture; the check exercises three byte chunks and a
	// UTF-8 code point crossing a chunk boundary without treating the frame cap
	// as a row cap. No owner-data or whole-corpus acceptance is claimed.
	payload := []byte(`{"id":1,"body":"` + strings.Repeat("я", 300000) + `"}`)
	var sealed bytes.Buffer
	previous := ""
	ordinal := uint64(0)
	var frames []consolidationFrame
	var ciphers [][]byte
	emit := func(frame consolidationFrame) {
		frame.Ordinal, frame.Previous, frame.Binding = ordinal, previous, binding
		ciphertext, err := store.writeFrame(&sealed, frame)
		require.NoError(t, err)
		require.LessOrEqual(t, len(ciphertext), consolidationFrameBytes)
		frames = append(frames, frame)
		ciphers = append(ciphers, ciphertext)
		ordinal++
		previous = consolidationCipherHash(ciphertext)
	}
	emit(consolidationFrame{Kind: "manifest", Manifest: &manifest})
	for offset, chunk := 0, uint32(0); offset < len(payload); chunk++ {
		end := offset + consolidationChunkBytes
		if end > len(payload) {
			end = len(payload)
		}
		emit(consolidationFrame{Kind: "row", Table: "session_transcripts", Row: 1, Chunk: chunk, Last: end == len(payload), Key: JSONRaw(`{"id":1}`), NativeVersion: "fixture-version", Payload: payload[offset:end]})
		offset = end
	}
	emit(consolidationFrame{Kind: "final", Manifest: &manifest})
	reader := bytes.NewReader(sealed.Bytes())
	_, header, err := store.readFrame(reader)
	require.NoError(t, err)
	_, err = store.backupManifest(header, state)
	require.NoError(t, err)
	walk := consolidationFrameWalk{Manifest: manifest, Binding: binding, Previous: consolidationCipherHash(header), Ordinal: 1}
	var restored bytes.Buffer
	for !walk.Finished {
		frame, ciphertext, err := store.readFrame(reader)
		require.NoError(t, err)
		require.NoError(t, walk.accept(frame, ciphertext))
		_, err = restored.Write(frame.Payload)
		require.NoError(t, err)
	}
	require.Equal(t, payload, restored.Bytes())
	require.Zero(t, reader.Len())
	for _, length := range []int{0, 3, sealed.Len() - 1} {
		truncated := bytes.NewReader(sealed.Bytes()[:length])
		for {
			_, _, err := store.readFrame(truncated)
			if err != nil {
				require.EqualError(t, err, "PROJECT_ADOPTION_BACKUP_TRUNCATED")
				break
			}
		}
	}
	tampered := append([]byte(nil), sealed.Bytes()...)
	tampered[20] ^= 1
	_, _, err = store.readFrame(bytes.NewReader(tampered))
	require.EqualError(t, err, "PROJECT_ADOPTION_BACKUP_AUTHENTICATION_FAILED")
	tampered = append([]byte(nil), sealed.Bytes()...)
	tampered[len(tampered)-1] ^= 1
	finalTamper := bytes.NewReader(tampered)
	for range frames {
		_, _, err = store.readFrame(finalTamper)
		if err != nil {
			break
		}
	}
	require.EqualError(t, err, "PROJECT_ADOPTION_BACKUP_AUTHENTICATION_FAILED")
	foreign, err := vaultcrypto.OpenExistingVault(&config.Config{EncryptionKey: strings.Repeat("cd", 32)})
	require.NoError(t, err)
	_, _, err = NewProjectConsolidationStore(nil, foreign, nil).readFrame(bytes.NewReader(sealed.Bytes()))
	require.EqualError(t, err, "PROJECT_ADOPTION_BACKUP_AUTHENTICATION_FAILED")
	walk = consolidationFrameWalk{Manifest: manifest, Binding: binding, Previous: consolidationCipherHash(header), Ordinal: 1}
	require.EqualError(t, walk.accept(frames[2], ciphers[2]), "PROJECT_ADOPTION_BACKUP_CHAIN_MISMATCH")
	require.NoError(t, walk.accept(frames[1], ciphers[1]))
	require.EqualError(t, walk.accept(frames[1], ciphers[1]), "PROJECT_ADOPTION_BACKUP_CHAIN_MISMATCH")
	wrong := state
	wrong.Census.Database = "other-source"
	_, err = store.backupManifest(header, wrong)
	require.EqualError(t, err, "PROJECT_ADOPTION_BACKUP_MANIFEST_MISMATCH")
	wrong = state
	wrong.Census.Schema = "other-schema"
	_, err = store.backupManifest(header, wrong)
	require.EqualError(t, err, "PROJECT_ADOPTION_BACKUP_MANIFEST_MISMATCH")
	wrong = state
	wrong.Census.Gaps = []string{"changed-classification"}
	_, err = store.backupManifest(header, wrong)
	require.EqualError(t, err, "PROJECT_ADOPTION_BACKUP_MANIFEST_MISMATCH")
	var huge [4]byte
	binary.BigEndian.PutUint32(huge[:], consolidationFrameBytes+1)
	_, _, err = store.readFrame(bytes.NewReader(huge[:]))
	require.EqualError(t, err, "PROJECT_ADOPTION_FRAME_CAPACITY_EXCEEDED")
	_, err = store.writeFrame(io.Discard, consolidationFrame{Payload: make([]byte, consolidationFrameBytes)})
	require.EqualError(t, err, "PROJECT_ADOPTION_FRAME_CAPACITY_EXCEEDED")
}

func TestProjectConsolidationMetadataCodecAndNativeKeys(t *testing.T) {
	vault, err := vaultcrypto.OpenExistingVault(&config.Config{EncryptionKey: strings.Repeat("ab", 32)})
	require.NoError(t, err)
	store := NewProjectConsolidationStore(nil, vault, nil)
	original := consolidationCensusState{Database: "fixture", NativeSnapshot: "1:1:", Rows: []consolidationRow{{Table: "documents", Column: "", Kind: "inherited", Key: JSONRaw(`{"id":9007199254740993}`), NativeVersion: "9"}}}
	token, err := store.seal("census", original)
	require.NoError(t, err)
	var decoded consolidationCensusState
	require.NoError(t, store.open(token, "census", &decoded))
	require.Equal(t, original, decoded)
	require.EqualError(t, store.open(token, "plan", &decoded), "PROJECT_ADOPTION_STALE_PLAN")
	key, err := consolidationCanonicalKey([]byte(`{"version": 2, "id": 9007199254740993}`))
	require.NoError(t, err)
	require.JSONEq(t, `{"id":9007199254740993,"version":2}`, string(key))
	predicate, args, err := consolidationNativePredicate(key, "t")
	require.NoError(t, err)
	require.Contains(t, predicate, `t."id"`)
	require.Contains(t, args, "9007199254740993")
	for _, table := range []string{"documents", "content", "project_identity_comparisons"} {
		state := consolidationCensusState{}
		store.addCensusRow(&state, table, "", "inherited", key, nil, "", false)
		mapping, err := store.buildMapping(state.Rows, nil)
		require.NoError(t, err)
		require.Len(t, mapping, 1)
		require.Empty(t, mapping[0].ProjectID)
		require.NotEmpty(t, mapping[0].UnresolvedReference)
		path, _ := json.Marshal(append([]string{state.Rows[0].Column}, state.Rows[0].Path...))
		require.JSONEq(t, `[""]`, string(path))
		require.False(t, state.Rows[0].Global)
	}
}

func TestProjectConsolidationFinalManifestAndCounts(t *testing.T) {
	manifest := consolidationBackupState{Format: consolidationStreamFormat, Tables: []consolidationTable{{Name: "documents", Count: 1}}}
	binding := structuralDigest(manifest)
	row := consolidationFrame{Ordinal: 1, Previous: "header", Binding: binding, Kind: "row", Table: "documents", Row: 1, Last: true, Key: JSONRaw(`{"id":1}`), NativeVersion: "9", Payload: []byte(`{"id":1}`)}
	newWalk := func() consolidationFrameWalk {
		return consolidationFrameWalk{Manifest: manifest, Binding: binding, Previous: "header", Ordinal: 1}
	}
	for _, change := range []func(*consolidationFrame){
		func(f *consolidationFrame) { f.Row = 2 },
		func(f *consolidationFrame) { f.Table = "content" },
		func(f *consolidationFrame) { f.Key = JSONRaw(`null`) },
		func(f *consolidationFrame) { f.NativeVersion = "" },
		func(f *consolidationFrame) { f.Chunk = 1 },
		func(f *consolidationFrame) { f.Last = false },
	} {
		walk, invalid := newWalk(), row
		change(&invalid)
		require.EqualError(t, walk.accept(invalid, []byte("row")), "PROJECT_ADOPTION_BACKUP_CHAIN_MISMATCH")
	}
	walk := newWalk()
	final := consolidationFrame{Ordinal: 1, Previous: "header", Binding: binding, Kind: "final", Manifest: &manifest}
	require.EqualError(t, walk.accept(final, []byte("final")), "PROJECT_ADOPTION_BACKUP_TRUNCATED")
	require.NoError(t, walk.accept(row, []byte("row")))
	final.Ordinal, final.Previous = 2, consolidationCipherHash([]byte("row"))
	changed := manifest
	changed.Snapshot = "other-snapshot"
	invalid := final
	invalid.Manifest = &changed
	require.EqualError(t, walk.accept(invalid, []byte("final")), "PROJECT_ADOPTION_BACKUP_TRUNCATED")
	invalid = final
	invalid.Key = JSONRaw(`{"id":1}`)
	require.EqualError(t, walk.accept(invalid, []byte("final")), "PROJECT_ADOPTION_BACKUP_TRUNCATED")
	require.NoError(t, walk.accept(final, []byte("final")))
	require.True(t, walk.Finished)
	require.EqualError(t, walk.accept(final, []byte("final")), "PROJECT_ADOPTION_BACKUP_CHAIN_MISMATCH")
	manifest.Tables[0].Count = 2
	walk = newWalk()
	row.Binding = binding
	require.NoError(t, walk.accept(row, []byte("row")))
	require.EqualError(t, walk.accept(final, []byte("final")), "PROJECT_ADOPTION_BACKUP_TRUNCATED")
}

func TestProjectConsolidationUnknownObjectRolePreservesMetadata(t *testing.T) {
	vault, err := vaultcrypto.OpenExistingVault(&config.Config{EncryptionKey: strings.Repeat("ab", 32)})
	require.NoError(t, err)
	store := NewProjectConsolidationStore(nil, vault, nil)
	state := consolidationCensusState{}
	store.addCensusRow(&state, "versioned_documents", "metadata", "unknown", JSONRaw(`{"id":1}`), nil, "", false)
	mapping, err := store.buildMapping(state.Rows, []ConsolidationEvidence{{Reference: "urn:engram:fixture:proof", Kind: "portable_marker", Group: "urn:engram:fixture:group", ProjectID: "b42c9854-bf02-4d1b-867d-a2a096f9e4d0", Rows: []string{state.Rows[0].Reference}}})
	require.NoError(t, err)
	require.Empty(t, mapping[0].ProjectID)
	require.Equal(t, "unknown_field_role", mapping[0].Reason)
	require.NotEmpty(t, mapping[0].UnresolvedReference)
	require.False(t, consolidationMutable("unknown"))
	require.False(t, consolidationMutable("serialized_history"))
	require.False(t, consolidationMutable("historical_identity"))
	for _, column := range []string{"state", "digest", "source_book_job_id"} {
		require.Empty(t, consolidationFieldRole("ci_profiles", column))
	}
}

// Measures the exporting backend's actual temporary files, not EXPLAIN text.
// Only the dedicated synthetic fixture is admitted; no owner corpus is read.
func TestProjectConsolidationExportBoundsServerPayload(t *testing.T) {
	deadlineText := os.Getenv("ENGRAM_CONSOLIDATION_FIXTURE_DEADLINE")
	if deadlineText == "" {
		t.Skip("exact admitted fixture deadline absent; no resource proof")
	}
	deadline, err := time.Parse(time.RFC3339, deadlineText)
	require.NoError(t, err)
	require.True(t, time.Now().Before(deadline), "fixture admission expired")
	ctx, cancel := context.WithDeadline(context.Background(), deadline)
	defer cancel()
	cfg := consolidationTestDatabaseConfig(t, os.Getenv("DATABASE_DSN"), "source")
	require.Equal(t, uint16(22366), cfg.Port, "existing admitted consolidation fixture only")
	pool := stdlib.OpenDB(*cfg)
	pool.SetMaxOpenConns(2)
	defer pool.Close()
	db, err := gormlib.Open(postgres.New(postgres.Config{Conn: pool}), &gormlib.Config{Logger: logger.Default.LogMode(logger.Silent), DisableAutomaticPing: true})
	require.NoError(t, err)
	db = db.WithContext(ctx)
	schema := "export_bounds_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	require.NoError(t, db.Exec("CREATE SCHEMA "+quoted(schema)).Error)
	defer func() { require.NoError(t, db.Exec("DROP SCHEMA "+quoted(schema)+" CASCADE").Error) }()
	require.NoError(t, db.Exec("CREATE TABLE "+quoted(schema)+`.rows(bucket INTEGER,id INTEGER,body TEXT,PRIMARY KEY(bucket,id))`).Error)
	require.NoError(t, db.Exec("INSERT INTO "+quoted(schema)+`.rows SELECT id%2,id,CASE WHEN id%3=0 THEN 'small' ELSE repeat('я',300000) END FROM generate_series(1,96) id`).Error)
	require.NoError(t, db.Exec("CREATE TABLE "+quoted(schema)+`.physical(body TEXT)`).Error)
	require.NoError(t, db.Exec("INSERT INTO "+quoted(schema)+`.physical SELECT repeat('я',300000) FROM generate_series(1,3)`).Error)
	require.NoError(t, db.Exec("ANALYZE "+quoted(schema)+".rows").Error)
	require.NoError(t, db.Exec("ANALYZE "+quoted(schema)+".physical").Error)
	tx := db.Begin(&sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	require.NoError(t, tx.Error)
	defer tx.Rollback()
	require.NoError(t, tx.Exec("SET LOCAL search_path="+quoted(schema)).Error)
	var backend int
	require.NoError(t, tx.Raw("SELECT pg_backend_pid()").Scan(&backend).Error)
	var memory int64
	require.NoError(t, tx.Raw("SELECT pg_size_bytes(current_setting('work_mem'))").Scan(&memory).Error)
	require.LessOrEqual(t, memory, int64(8<<20), "fixture must expose corpus-wide spill without changing work_mem")
	var tablespaces string
	require.NoError(t, tx.Raw("SELECT current_setting('temp_tablespaces')").Scan(&tablespaces).Error)
	require.Empty(t, tablespaces, "temporary-file oracle measures the fixture default tablespace")
	tables := []consolidationTable{{Name: "rows", Count: 96, PrimaryKey: pq.StringArray{"bucket", "id"}}, {Name: "physical", Count: 3}}
	type expectedRow struct {
		Key, Version, Digest string
		Bytes                int64
	}
	var expected []expectedRow
	for _, table := range tables {
		var next []expectedRow
		query := "SELECT " + consolidationKeySQL(table, "t") + "::text AS key,t.xmin::text AS version,md5(to_jsonb(t)::text) AS digest,octet_length(convert_to(to_jsonb(t)::text,'UTF8')) AS bytes FROM " + quoted(table.Name) + " t ORDER BY " + consolidationOrderSQL(table, "t")
		require.NoError(t, tx.Raw(query).Scan(&next).Error)
		for i := range next {
			key, err := consolidationCanonicalKey([]byte(next[i].Key))
			require.NoError(t, err)
			next[i].Key = string(key)
		}
		expected = append(expected, next...)
	}
	sequences, err := consolidationSequences(tx)
	require.NoError(t, err)
	manifest := consolidationBackupState{Format: consolidationStreamFormat, Tables: tables, Sequences: sequences}
	vault, err := vaultcrypto.OpenExistingVault(&config.Config{EncryptionKey: strings.Repeat("ab", 32)})
	require.NoError(t, err)
	store := NewProjectConsolidationStore(db, vault, nil)
	reader, writer := io.Pipe()
	defer reader.Close()
	finished := make(chan error, 1)
	go func() {
		err := store.exportFrames(tx, manifest, writer)
		_ = writer.CloseWithError(err)
		finished <- err
	}()
	header, ciphertext, err := store.readFrame(reader)
	require.NoError(t, err)
	require.Equal(t, "manifest", header.Kind)
	walk := consolidationFrameWalk{Manifest: manifest, Binding: structuralDigest(manifest), Previous: consolidationCipherHash(ciphertext), Ordinal: 1}
	missingFinal := walk
	require.EqualError(t, missingFinal.accept(consolidationFrame{Kind: "final", Ordinal: 1, Previous: walk.Previous, Binding: walk.Binding, Manifest: &manifest}, []byte("premature")), "PROJECT_ADOPTION_BACKUP_TRUNCATED")
	digest := md5.New()
	var rowBytes, peakTemp int64
	row := 0
	for !walk.Finished {
		frame, cipher, err := store.readFrame(reader)
		require.NoError(t, err)
		require.NoError(t, walk.accept(frame, cipher))
		if frame.Kind != "row" {
			continue
		}
		require.Less(t, row, len(expected))
		require.Equal(t, expected[row].Key, string(frame.Key))
		require.Equal(t, expected[row].Version, frame.NativeVersion)
		_, err = digest.Write(frame.Payload)
		require.NoError(t, err)
		rowBytes += int64(len(frame.Payload))
		if frame.Last {
			require.Equal(t, expected[row].Bytes, rowBytes)
			require.Equal(t, expected[row].Digest, hex.EncodeToString(digest.Sum(nil)))
			digest.Reset()
			rowBytes = 0
			row++
		}
		var temporary int64
		require.NoError(t, db.Raw(`SELECT COALESCE(sum(size),0) FROM pg_ls_tmpdir() WHERE name LIKE ?`, "pgsql_tmp"+strconv.Itoa(backend)+".%").Scan(&temporary).Error)
		if temporary > peakTemp {
			peakTemp = temporary
		}
	}
	require.NoError(t, <-finished)
	require.Equal(t, len(expected), row)
	_, _, err = store.readFrame(reader)
	require.EqualError(t, err, "PROJECT_ADOPTION_BACKUP_TRUNCATED")
	require.Less(t, peakTemp, int64(16<<20), "server must not sort the complete >32MiB serialized corpus")
	t.Logf("authenticated_rows=%d peak_export_backend_temp_bytes=%d", row, peakTemp)
}
