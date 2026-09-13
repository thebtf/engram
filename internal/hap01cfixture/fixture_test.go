package hap01cfixture

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/thebtf/engram/internal/auth"
	gormdb "github.com/thebtf/engram/internal/db/gorm"
)

func TestOpenRefusesUnsafeDSNBeforeConnecting(t *testing.T) {
	const runID = "fixture-run"
	validDatabase := "hap01c_" + runID
	tests := []struct {
		name string
		dsn  string
	}{
		{
			name: "remote host",
			dsn:  "postgres://fixture:password@example.invalid/" + validDatabase + "?sslmode=disable",
		},
		{
			name: "shared database",
			dsn:  "postgres://fixture:password@127.0.0.1/hap01c_other-run?sslmode=disable",
		},
		{
			name: "ssl enabled",
			dsn:  "postgres://fixture:password@localhost/" + validDatabase + "?sslmode=require",
		},
		{
			name: "non postgres scheme",
			dsn:  "mysql://fixture:password@localhost/" + validDatabase + "?sslmode=disable",
		},
		{
			name: "unsafe query override",
			dsn:  "postgresql://fixture:password@127.0.0.1/" + validDatabase + "?sslmode=disable&host=example.invalid",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			dsnFile := filepath.Join(t.TempDir(), "dsn.txt")
			writePrivateFixtureFile(t, dsnFile, []byte(test.dsn))
			connectCalls := 0
			_, err := openWithDependencies(context.Background(), dsnFile, runID, dependencies{
				openStore: func(gormdb.Config) (*gormdb.Store, error) {
					connectCalls++
					return nil, errors.New("unexpected store open")
				},
				preflight: func(context.Context, string, string) error {
					connectCalls++
					return errors.New("unexpected preflight")
				},
				now:    func() time.Time { return time.Unix(0, 0).UTC() },
				random: bytes.NewReader(make([]byte, 64)),
			})
			if !IsBoundaryError(err) {
				t.Fatalf("Open error = %v, want boundary refusal", err)
			}
			if connectCalls != 0 {
				t.Fatalf("unsafe DSN reached a connection dependency %d times", connectCalls)
			}
		})
	}
}

func TestOpenRefusesNonPrivateDSNBeforeConnecting(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows os.FileMode does not expose POSIX private-mode bits")
	}
	dsnFile := filepath.Join(t.TempDir(), "dsn.txt")
	if err := os.WriteFile(dsnFile, []byte("postgres://fixture:password@localhost/hap01c_fixture-run?sslmode=disable"), 0o644); err != nil {
		t.Fatalf("write DSN: %v", err)
	}
	if err := os.Chmod(dsnFile, 0o644); err != nil {
		t.Fatalf("chmod DSN: %v", err)
	}
	connectCalls := 0
	_, err := openWithDependencies(context.Background(), dsnFile, "fixture-run", dependencies{
		openStore: func(gormdb.Config) (*gormdb.Store, error) {
			connectCalls++
			return nil, errors.New("unexpected store open")
		},
		preflight: func(context.Context, string, string) error {
			connectCalls++
			return nil
		},
		now:    func() time.Time { return time.Unix(0, 0).UTC() },
		random: bytes.NewReader(make([]byte, 64)),
	})
	if !IsBoundaryError(err) {
		t.Fatalf("Open error = %v, want boundary refusal", err)
	}
	if connectCalls != 0 {
		t.Fatalf("non-private DSN reached a connection dependency %d times", connectCalls)
	}
}

func TestSeedRequestRequiresExactClosedObject(t *testing.T) {
	const (
		runID     = "fixture-run"
		anchorID  = "11111111-1111-1111-1111-111111111111"
		projectID = "22222222-2222-2222-2222-222222222222"
	)
	requestPath := filepath.Join(t.TempDir(), "request.json")
	valid := []byte(`{"run_id":"fixture-run","anchor_project_id":"` + anchorID + `","canonical_project_key":"` + projectID + `","legacy_project_id":"legacy-fixture","legacy_direct_project_id":"abcdef","ambient_query_text":"HAP-01C qualification fixture memory."}`)
	if err := os.WriteFile(requestPath, valid, 0o644); err != nil {
		t.Fatalf("write request: %v", err)
	}
	request, err := readSeedRequest(requestPath, runID)
	if err != nil {
		t.Fatalf("read valid request: %v", err)
	}
	if request.CanonicalProjectKey != projectID || request.AnchorProjectID != anchorID || request.LegacyDirectProjectID != "abcdef" || request.AmbientQueryText != "HAP-01C qualification fixture memory." {
		t.Fatalf("read request = %#v", request)
	}

	for _, invalid := range [][]byte{
		[]byte(`{"run_id":"fixture-run","anchor_project_id":"` + anchorID + `","canonical_project_key":"` + projectID + `","legacy_project_id":"legacy-fixture","legacy_direct_project_id":"abcdef","ambient_query_text":"HAP-01C qualification fixture memory.","extra":"x"}`),
		[]byte(`{"run_id":"fixture-run","run_id":"fixture-run","anchor_project_id":"` + anchorID + `","canonical_project_key":"` + projectID + `","legacy_project_id":"legacy-fixture","legacy_direct_project_id":"abcdef","ambient_query_text":"HAP-01C qualification fixture memory."}`),
		[]byte(`{"run_id":"fixture-run","anchor_project_id":"` + anchorID + `","canonical_project_key":"` + projectID + `","legacy_project_id":"legacy-fixture","ambient_query_text":"HAP-01C qualification fixture memory."}`),
		[]byte(`{"run_id":"fixture-run","anchor_project_id":"` + anchorID + `","canonical_project_key":"` + projectID + `","legacy_project_id":"legacy-fixture","legacy_direct_project_id":"abcdef"}`),
		[]byte(`{"run_id":"fixture-run","anchor_project_id":"` + anchorID + `","canonical_project_key":"` + projectID + `","legacy_project_id":"legacy-fixture","legacy_direct_project_id":"abcdef","ambient_query_text":""}`),
		[]byte(`{"run_id":"fixture-run","anchor_project_id":"` + anchorID + `","canonical_project_key":"` + projectID + `","legacy_project_id":"legacy-fixture","legacy_direct_project_id":"too-long","ambient_query_text":"HAP-01C qualification fixture memory."}`),
	} {
		if err := os.WriteFile(requestPath, invalid, 0o644); err != nil {
			t.Fatalf("rewrite request: %v", err)
		}
		if _, err := readSeedRequest(requestPath, runID); !IsBoundaryError(err) {
			t.Fatalf("invalid request error = %v, want boundary refusal", err)
		}
	}
}

func TestRedactedReceiptsNeverContainSecrets(t *testing.T) {
	rawToken := "engram_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	dsn := "postgres://fixture:password@localhost/hap01c_fixture-run?sslmode=disable"
	values := []interface{}{
		SeedReceipt{
			Schema:      seedReceiptSchema,
			RunIDSHA256: redactedID("run", "fixture-run"),
			Keycards: []KeycardReceipt{{
				Class:       string(projectServiceCard),
				IDSHA256:    redactedID("keycard", "33333333-3333-3333-3333-333333333333"),
				TokenLength: len(rawToken),
			}},
		},
		RotationReceipt{
			Schema:      rotationReceiptSchema,
			RunIDSHA256: redactedID("run", "fixture-run"),
			RevokedKeycard: KeycardReceipt{
				Class:       string(projectServiceCard),
				IDSHA256:    redactedID("keycard", "33333333-3333-3333-3333-333333333333"),
				TokenLength: len(rawToken),
			},
		},
		SnapshotReceipt{
			Schema:                           snapshotReceiptSchema,
			RunIDSHA256:                      redactedID("run", "fixture-run"),
			AmbientDeliveryUnavailableReason: "NO_DURABLE_AMBIENT_ATTEMPT_COUNTER",
		},
	}
	for _, value := range values {
		encoded, err := json.Marshal(value)
		if err != nil {
			t.Fatalf("marshal receipt: %v", err)
		}
		for _, forbidden := range []string{rawToken, dsn, "password", "C:\\fixture"} {
			if bytes.Contains(encoded, []byte(forbidden)) {
				t.Fatalf("receipt retained forbidden value %q: %s", forbidden, encoded)
			}
		}
	}
}

// This integration test requires a dedicated scratch DSN. It deliberately does
// not use DATABASE_DSN so a general development/test database cannot satisfy
// the fixture's dedicated-db guard by accident.
func TestFixtureSeedRotateAndSnapshotIntegration(t *testing.T) {
	fixture, request, secretsPath := openHAPFixtureIntegration(t)
	secrets, validator := seedHAPFixtureIntegration(t, fixture, request, secretsPath)
	assertHAPFixtureFailedPublication(t, fixture, request, secretsPath, secrets, validator)
	secrets = assertHAPFixturePostPublishConvergence(t, fixture, request, secretsPath, secrets, validator)
	secrets = assertHAPFixtureRotation(t, fixture, request, secretsPath, secrets, validator)
	assertHAPFixtureSnapshot(t, fixture, request, secretsPath)
}

func openHAPFixtureIntegration(t *testing.T) (*Fixture, seedRequest, string) {
	t.Helper()
	dsn := os.Getenv("HAP01C_FIXTURE_TEST_DSN")
	if dsn == "" {
		t.Skip("HAP01C_FIXTURE_TEST_DSN must name a dedicated hap01c_<run-id> PostgreSQL 17+ database")
	}
	parsed, err := url.Parse(dsn)
	if err != nil {
		t.Fatalf("parse integration DSN: %v", err)
	}
	database := strings.TrimPrefix(parsed.Path, "/")
	if !strings.HasPrefix(database, "hap01c_") {
		t.Fatalf("HAP01C_FIXTURE_TEST_DSN must name a dedicated hap01c_ database")
	}
	runID := strings.TrimPrefix(database, "hap01c_")
	if err := ValidateRunID(runID); err != nil {
		t.Fatalf("integration run id: %v", err)
	}
	directory := t.TempDir()
	dsnFile := filepath.Join(directory, "dsn.txt")
	requestFile := filepath.Join(directory, "request.json")
	secretsFile := filepath.Join(directory, "keycards.json")
	writePrivateFixtureFile(t, dsnFile, []byte(dsn))
	request := seedRequest{
		RunID:                 runID,
		AnchorProjectID:       uuid.NewString(),
		CanonicalProjectKey:   uuid.NewString(),
		LegacyProjectID:       "legacy-" + strings.ReplaceAll(uuid.NewString(), "-", ""),
		LegacyDirectProjectID: "abcdef",
		AmbientQueryText:      "HAP-01C qualification fixture memory.",
	}
	requestJSON, err := json.Marshal(map[string]string{
		"run_id":                   request.RunID,
		"anchor_project_id":        request.AnchorProjectID,
		"canonical_project_key":    request.CanonicalProjectKey,
		"legacy_project_id":        request.LegacyProjectID,
		"ambient_query_text":       request.AmbientQueryText,
		"legacy_direct_project_id": request.LegacyDirectProjectID,
	})
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	if err := os.WriteFile(requestFile, requestJSON, 0o600); err != nil {
		t.Fatalf("write request: %v", err)
	}
	fixture, err := Open(context.Background(), dsnFile, runID)
	if err != nil {
		t.Fatalf("open fixture: %v", err)
	}
	cleanupFixtureRows(t, fixture, request)
	t.Cleanup(func() {
		cleanupFixtureRows(t, fixture, request)
		_ = fixture.Close()
	})
	return fixture, request, secretsFile
}

func seedHAPFixtureIntegration(t *testing.T, fixture *Fixture, request seedRequest, secretsPath string) (secretsFile, *auth.Validator) {
	t.Helper()
	seed, err := fixture.Seed(context.Background(), fixtureRequestPath(secretsPath), secretsPath)
	if err != nil {
		t.Fatalf("seed fixture: %v", err)
	}
	if seed.MemoryRows != 1 || seed.RuleRows != 1 || len(seed.Keycards) != 4 {
		t.Fatalf("unexpected seed receipt: %#v", seed)
	}
	secretsRaw, err := readBoundedFile(secretsPath, maxSecretsBytes, true, "SECRETS")
	if err != nil {
		t.Fatalf("read seed secrets: %v", err)
	}
	secrets, err := parseSecrets(secretsRaw, request.RunID)
	if err != nil {
		t.Fatalf("parse seed secrets: %v", err)
	}
	validator := auth.NewValidator("", gormdb.NewTokenStore(fixture.store))
	if identity, err := validator.Validate(context.Background(), secrets.ProjectToken); err != nil || !identity.IsHAPProjectServiceFor(request.CanonicalProjectKey) {
		t.Fatalf("project keycard did not validate as the scoped service identity: %v", err)
	}
	if legacy, err := validator.Validate(context.Background(), secrets.LegacyDirectToken); err != nil || !legacy.IsHAPLegacyDirectFor(request.CanonicalProjectKey, time.Now().UTC()) {
		t.Fatalf("legacy keycard did not validate with its real expiry: %v", err)
	}
	return secrets, validator
}

func fixtureRequestPath(secretsPath string) string {
	return filepath.Join(filepath.Dir(secretsPath), "request.json")
}

func assertHAPFixtureFailedPublication(t *testing.T, fixture *Fixture, request seedRequest, secretsPath string, secrets secretsFile, validator *auth.Validator) {
	t.Helper()
	originalReplace := fixture.deps.replaceSecrets
	fixture.deps.replaceSecrets = func(string, []byte) error { return boundary("FORCED_SECRET_REPLACE_FAILURE") }
	if _, err := fixture.RotateProjectKeycard(context.Background(), secretsPath); !IsBoundaryError(err) {
		t.Fatalf("forced replacement error = %v, want boundary failure", err)
	}
	fixture.deps.replaceSecrets = originalReplace
	unchangedRaw, err := readBoundedFile(secretsPath, maxSecretsBytes, true, "SECRETS")
	if err != nil {
		t.Fatalf("read compensated secrets: %v", err)
	}
	unchanged, err := parseSecrets(unchangedRaw, request.RunID)
	if err != nil || unchanged.ProjectToken != secrets.ProjectToken || unchanged.ProjectTokenID != secrets.ProjectTokenID {
		t.Fatalf("failed publication changed active secret: %v", err)
	}
	if identity, err := validator.Validate(context.Background(), unchanged.ProjectToken); err != nil || !identity.IsHAPProjectServiceFor(request.CanonicalProjectKey) {
		t.Fatalf("compensation did not restore old project keycard: %v", err)
	}
}

func assertHAPFixturePostPublishConvergence(t *testing.T, fixture *Fixture, request seedRequest, secretsPath string, secrets secretsFile, validator *auth.Validator) secretsFile {
	t.Helper()
	originalReplace := fixture.deps.replaceSecrets
	fixture.deps.replaceSecrets = func(path string, content []byte) error {
		if err := atomicReplacePrivateFile(path, content); err != nil {
			return err
		}
		return boundary("FORCED_POST_PUBLISH_FAILURE")
	}
	publishedRotation, err := fixture.RotateProjectKeycard(context.Background(), secretsPath)
	if err != nil {
		t.Fatalf("post-publish rotation did not converge: %v", err)
	}
	if !publishedRotation.RevokedKeycard.Revoked || publishedRotation.ReplacementCard.Revoked {
		t.Fatalf("unexpected post-publish receipt: %#v", publishedRotation)
	}
	fixture.deps.replaceSecrets = originalReplace
	publishedRaw, err := readBoundedFile(secretsPath, maxSecretsBytes, true, "SECRETS")
	if err != nil {
		t.Fatalf("read post-publish secrets: %v", err)
	}
	published, err := parseSecrets(publishedRaw, request.RunID)
	if err != nil || published.ProjectTokenID == secrets.ProjectTokenID || published.ProjectToken == secrets.ProjectToken {
		t.Fatalf("post-publish error did not retain replacement secrets: %v", err)
	}
	if oldRecord, err := gormdb.NewTokenStore(fixture.store).GetByID(context.Background(), secrets.ProjectTokenID); err != nil || oldRecord == nil || !oldRecord.Revoked {
		t.Fatalf("post-publish error did not retain DB revocation: %v", err)
	}
	if identity, err := validator.Validate(context.Background(), published.ProjectToken); err != nil || !identity.IsHAPProjectServiceFor(request.CanonicalProjectKey) {
		t.Fatalf("post-publish replacement keycard did not validate: %v", err)
	}
	return published
}

func assertHAPFixtureRotation(t *testing.T, fixture *Fixture, request seedRequest, secretsPath string, secrets secretsFile, validator *auth.Validator) secretsFile {
	t.Helper()
	oldRaw, oldID := secrets.ProjectToken, secrets.ProjectTokenID
	rotation, err := fixture.RotateProjectKeycard(context.Background(), secretsPath)
	if err != nil {
		t.Fatalf("rotate project keycard: %v", err)
	}
	if !rotation.RevokedKeycard.Revoked || rotation.ReplacementCard.Revoked {
		t.Fatalf("unexpected rotation receipt: %#v", rotation)
	}
	rotatedRaw, err := readBoundedFile(secretsPath, maxSecretsBytes, true, "SECRETS")
	if err != nil {
		t.Fatalf("read rotated secrets: %v", err)
	}
	rotated, err := parseSecrets(rotatedRaw, request.RunID)
	if err != nil {
		t.Fatalf("parse rotated secrets: %v", err)
	}
	if rotated.ProjectTokenID == oldID || rotated.ProjectToken == oldRaw {
		t.Fatal("rotation did not replace the project keycard")
	}
	oldRecord, err := gormdb.NewTokenStore(fixture.store).GetByID(context.Background(), oldID)
	if err != nil || oldRecord == nil || !oldRecord.Revoked {
		t.Fatalf("old keycard was not revoked: %v", err)
	}
	if _, err := validator.Validate(context.Background(), oldRaw); err == nil {
		t.Fatal("revoked project keycard still validated")
	}
	if identity, err := validator.Validate(context.Background(), rotated.ProjectToken); err != nil || !identity.IsHAPProjectServiceFor(request.CanonicalProjectKey) {
		t.Fatalf("replacement project keycard did not validate: %v", err)
	}
	return rotated
}

func assertHAPFixtureSnapshot(t *testing.T, fixture *Fixture, request seedRequest, secretsPath string) {
	t.Helper()
	var target gormdb.Memory
	if err := fixture.store.GetDB().Where("project = ? AND source_agent = ?", request.CanonicalProjectKey, fixtureSourceAgent).First(&target).Error; err != nil {
		t.Fatalf("read target memory: %v", err)
	}
	if err := gormdb.NewInjectionLogStore(fixture.store).Record(context.Background(), "01a-hap01c-omp-session", request.CanonicalProjectKey, []int64{target.ID}); err != nil {
		t.Fatalf("record OMP-shaped session telemetry: %v", err)
	}
	snapshot, err := fixture.Snapshot(context.Background(), fixtureRequestPath(secretsPath))
	if err != nil {
		t.Fatalf("snapshot fixture: %v", err)
	}
	if snapshot.MemoryRows != 1 || snapshot.RuleRows != 1 || snapshot.SessionStartAttempts != 1 || snapshot.ActiveProjectTokenCount != 1 || snapshot.RevokedProjectTokenCount != 3 {
		t.Fatalf("unexpected snapshot receipt: %#v", snapshot)
	}
	if snapshot.AmbientDeliveryAvailable || snapshot.AmbientDeliveryUnavailableReason != "NO_DURABLE_AMBIENT_ATTEMPT_COUNTER" {
		t.Fatalf("ambient telemetry gap was not explicit: %#v", snapshot)
	}
}

func cleanupFixtureRows(t *testing.T, fixture *Fixture, request seedRequest) {
	t.Helper()
	if fixture == nil || fixture.store == nil {
		return
	}
	db := fixture.store.GetDB()
	statements := []struct {
		query string
		args  []interface{}
	}{
		{`DELETE FROM injection_log WHERE project = ?`, []interface{}{request.CanonicalProjectKey}},
		{`DELETE FROM attention_events WHERE project = ?`, []interface{}{request.CanonicalProjectKey}},
		{`DELETE FROM agent_session_state WHERE session_id = ?`, []interface{}{fixtureSessionID(fixture.runID)}},
		{`DELETE FROM behavioral_rules WHERE project = ? AND edited_by = ?`, []interface{}{request.CanonicalProjectKey, fixtureSourceAgent}},
		{`DELETE FROM memories WHERE project = ? AND source_agent = ?`, []interface{}{request.CanonicalProjectKey, fixtureSourceAgent}},
		{`DELETE FROM api_tokens WHERE name LIKE ?`, []interface{}{tokenName(fixture.runID, ordinaryWorkstationCard)[:len("hap01c:")+len(fixture.runID)+1] + "%"}},
		{`DELETE FROM project_resolution_attempts WHERE anchor_project_id = ?`, []interface{}{request.AnchorProjectID}},
		{`DELETE FROM projects WHERE id = ?`, []interface{}{request.CanonicalProjectKey}},
	}
	for _, statement := range statements {
		if err := db.Exec(statement.query, statement.args...).Error; err != nil {
			t.Errorf("fixture cleanup failed: %v", err)
		}
	}
}

func writePrivateFixtureFile(t *testing.T, path string, content []byte) {
	t.Helper()
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatalf("write private fixture file: %v", err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatalf("chmod private fixture file: %v", err)
	}
}
