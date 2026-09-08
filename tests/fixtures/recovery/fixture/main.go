package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"

	"gorm.io/gorm/logger"

	"github.com/thebtf/engram/internal/config"
	engramcrypto "github.com/thebtf/engram/internal/crypto"
	engramgorm "github.com/thebtf/engram/internal/db/gorm"
	"github.com/thebtf/engram/pkg/models"
)

const (
	project                         = "recovery-fixture"
	memoryContent                   = "Durable recovery memory sentinel remains recallable after restore"
	ruleContent                     = "Always preserve the recovery sentinel during PostgreSQL recovery"
	credentialKey                   = "recovery.api_key"
	credentialSecret                = "recovery-secret-value"
	documentBody                    = "Recovery document sentinel body"
	issueTitle                      = "Recovery issue sentinel"
	codeContent                     = "func RecoverySentinel() string { return \"restored\" }"
	recoveryUCIFixtureKey           = "recovery-uci"
	recoveryUCIRealm                = "recovery-uci-realm"
	recoveryUCISourceID             = "10000000-0000-4000-8000-000000000001"
	recoveryUCICheckoutID           = "10000000-0000-4000-8000-000000000002"
	recoveryUCIIncarnationID        = "10000000-0000-4000-8000-000000000003"
	recoveryUCIProfileID            = "10000000-0000-4000-8000-000000000004"
	recoveryUCIViewID               = "10000000-0000-4000-8000-000000000005"
	recoveryUCIBlobID               = "10000000-0000-4000-8000-000000000006"
	recoveryUCIArtifactID           = "10000000-0000-4000-8000-000000000007"
	recoveryUCIChunkID              = "10000000-0000-4000-8000-000000000008"
	recoveryUCIMembershipID         = "10000000-0000-4000-8000-000000000009"
	recoveryUCIExposureID           = "10000000-0000-4000-8000-000000000010"
	recoveryUCICompletionEvidenceID = "10000000-0000-4000-8000-000000000011"
	recoveryUCIExposureRef          = "uci-exp_20000000-0000-4000-8000-000000000001"
	recoveryUCIHeadOID              = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	recoveryUCIProjectionContent    = "package recovery\n"
)

func main() {
	action := flag.String("action", "assert", "seed, assert, seed-uci, or assert-uci")
	dsnFile := flag.String("dsn-file", "", "path to a private file containing the PostgreSQL DSN")
	keyFile := flag.String("key-file", "", "path to a private file containing the AES-256 vault key")
	flag.Parse()

	dsnBytes, err := os.ReadFile(*dsnFile)
	if err != nil {
		fatalf("read dsn-file: %v", err)
	}
	keyBytes, err := os.ReadFile(*keyFile)
	if err != nil {
		fatalf("read key-file: %v", err)
	}

	store, err := engramgorm.NewStore(engramgorm.Config{
		DSN:      strings.TrimSpace(string(dsnBytes)),
		MaxConns: 5,
		LogLevel: logger.Silent,
	})
	if err != nil {
		fatalf("open store: %v", err)
	}

	key := strings.TrimSpace(string(keyBytes))
	vault, err := engramcrypto.NewVault(&config.Config{EncryptionKey: key})
	if err != nil {
		fatalf("open vault: %v", err)
	}

	ctx := context.Background()
	switch *action {
	case "seed":
		err = seed(ctx, store, vault)
	case "assert":
		err = assertRestored(ctx, store, vault)
	case "seed-uci":
		err = seedUCI(ctx, store)
	case "assert-uci":
		err = assertUCI(ctx, store)
	default:
		err = fmt.Errorf("unsupported action %q", *action)
	}
	if err != nil {
		fatalf("%s recovery fixture: %v", *action, err)
	}

	fmt.Printf("{\"action\":%q,\"status\":\"pass\",\"project\":%q}\n", *action, project)
}

func seed(ctx context.Context, store *engramgorm.Store, vault *engramcrypto.Vault) error {
	memoryStore := engramgorm.NewMemoryStore(store)
	if _, err := memoryStore.Create(ctx, &models.Memory{
		Project: project, Content: memoryContent, Tags: []string{"recovery", "backup"},
		SourceAgent: "recovery-fixture", PrivacyScope: "project",
	}); err != nil {
		return fmt.Errorf("seed memory: %w", err)
	}

	projectCopy := project
	if _, err := engramgorm.NewBehavioralRulesStore(store).Create(ctx, &models.BehavioralRule{
		Project: &projectCopy, Content: ruleContent, Priority: 100, Enabled: true, EditedBy: "recovery-fixture",
	}); err != nil {
		return fmt.Errorf("seed behavioral rule: %w", err)
	}

	ciphertext, err := vault.Encrypt(credentialSecret)
	if err != nil {
		return fmt.Errorf("encrypt credential: %w", err)
	}
	if _, err := engramgorm.NewCredentialStore(store).Create(ctx, &models.Credential{
		Project: project, Key: credentialKey, EncryptedSecret: ciphertext,
		EncryptionKeyFingerprint: vault.Fingerprint(), Scope: "project", EditedBy: "recovery-fixture",
	}); err != nil {
		return fmt.Errorf("seed credential: %w", err)
	}

	issueStore := engramgorm.NewIssueStore(store.DB)
	if _, err := issueStore.CreateIssue(ctx, &engramgorm.Issue{
		Title: issueTitle, Body: "Recovery issue body", Status: "open", Priority: "high", Type: "task",
		SourceProject: project, TargetProject: project, SourceAgent: "recovery-fixture",
	}); err != nil {
		return fmt.Errorf("seed issue: %w", err)
	}

	if _, err := engramgorm.NewDocumentStore(store).UpsertDocument(
		ctx, project, "recovery.md", "Recovery document", documentBody,
	); err != nil {
		return fmt.Errorf("seed document: %w", err)
	}

	digest := sha256.Sum256([]byte(codeContent))
	if err := engramgorm.NewCodeChunkStore(store.DB).Upsert(ctx, &engramgorm.CodeChunk{
		ProjectID: project, FilePath: "recovery.go", Language: "go", ChunkType: "function",
		Content: codeContent, ContentSHA256: hex.EncodeToString(digest[:]), IndexSessionID: "recovery-index-session",
		ByteStart: 0, ByteEnd: len(codeContent),
	}); err != nil {
		return fmt.Errorf("seed code chunk: %w", err)
	}

	statements := []string{
		`DO $$ BEGIN IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'engram_recovery_reader') THEN CREATE ROLE engram_recovery_reader NOLOGIN; END IF; END $$`,
		`GRANT CONNECT ON DATABASE engram TO engram_recovery_reader`,
		`GRANT USAGE ON SCHEMA public TO engram_recovery_reader`,
		`GRANT SELECT ON ALL TABLES IN SCHEMA public TO engram_recovery_reader`,
		`ALTER DEFAULT PRIVILEGES IN SCHEMA public GRANT SELECT ON TABLES TO engram_recovery_reader`,
		`CREATE TABLE IF NOT EXISTS recovery_payload AS SELECT g AS id, repeat('recovery-payload-', 64) || g::text AS body FROM generate_series(1, 50000) AS g`,
	}
	for _, statement := range statements {
		if err := store.DB.WithContext(ctx).Exec(statement).Error; err != nil {
			return fmt.Errorf("seed recovery SQL: %w", err)
		}
	}
	return nil
}

func assertRestored(ctx context.Context, store *engramgorm.Store, vault *engramcrypto.Vault) error {
	memories, err := engramgorm.NewMemoryStore(store).SearchFTS(ctx, project, "durable recovery sentinel", 10)
	if err != nil {
		return fmt.Errorf("recall memory: %w", err)
	}
	if len(memories) != 1 || memories[0].Content != memoryContent {
		return fmt.Errorf("memory recall mismatch: got %d matches", len(memories))
	}

	projectCopy := project
	rules, err := engramgorm.NewBehavioralRulesStore(store).ListEnabled(ctx, &projectCopy, 10)
	if err != nil || len(rules) != 1 || rules[0].Content != ruleContent {
		return fmt.Errorf("behavioral rule mismatch: count=%d err=%v", len(rules), err)
	}

	credential, err := engramgorm.NewCredentialStore(store).Get(ctx, project, credentialKey)
	if err != nil {
		return fmt.Errorf("read credential: %w", err)
	}
	if !vault.MatchesFingerprint(credential.EncryptionKeyFingerprint) {
		return fmt.Errorf("vault key fingerprint mismatch")
	}
	plaintext, err := vault.Decrypt(credential.EncryptedSecret)
	if err != nil || plaintext != credentialSecret {
		return fmt.Errorf("decrypt credential: plaintext match=%t err=%v", plaintext == credentialSecret, err)
	}

	issues, total, err := engramgorm.NewIssueStore(store.DB).ListIssues(ctx, project, []string{"open"}, 10, 0)
	if err != nil || total != 1 || len(issues) != 1 || issues[0].Title != issueTitle {
		return fmt.Errorf("issue mismatch: total=%d count=%d err=%v", total, len(issues), err)
	}

	documentStore := engramgorm.NewDocumentStore(store)
	document, err := documentStore.GetDocument(ctx, project, "recovery.md")
	if err != nil || document == nil || !document.Hash.Valid {
		return fmt.Errorf("document metadata mismatch: document=%v err=%v", document, err)
	}
	content, err := documentStore.GetContent(ctx, document.Hash.String)
	if err != nil || content == nil || content.Doc != documentBody {
		return fmt.Errorf("document content mismatch: content=%v err=%v", content, err)
	}

	code, err := engramgorm.NewCodeChunkStore(store.DB).SearchCodeFTS(ctx, project, "RecoverySentinel", 10)
	if err != nil || len(code) != 1 || !strings.Contains(code[0].Content, "RecoverySentinel") {
		return fmt.Errorf("code index mismatch: count=%d err=%v", len(code), err)
	}

	var payloadCount int64
	if err := store.DB.WithContext(ctx).Table("recovery_payload").Count(&payloadCount).Error; err != nil || payloadCount != 50000 {
		return fmt.Errorf("recovery payload mismatch: count=%d err=%v", payloadCount, err)
	}
	var hasPrivilege bool
	if err := store.DB.WithContext(ctx).Raw(
		`SELECT has_table_privilege('engram_recovery_reader', 'public.memories', 'SELECT')`,
	).Scan(&hasPrivilege).Error; err != nil || !hasPrivilege {
		return fmt.Errorf("restored reader role/grant mismatch: value=%t err=%v", hasPrivilege, err)
	}
	return nil
}

type recoveryIntegritySnapshot struct {
	CredentialCiphertextDigest  string `gorm:"column:credential_ciphertext_digest"`
	CredentialFingerprintDigest string `gorm:"column:credential_fingerprint_digest"`
	DocumentHashDigest          string `gorm:"column:document_hash_digest"`
	CodeHashDigest              string `gorm:"column:code_hash_digest"`
}

// seedUCI runs only after the candidate has opened the upgraded database. It
// uses stable SQL so the existing seed action continues to compile at v6.42.0.
func seedUCI(ctx context.Context, store *engramgorm.Store) error {
	exposureBindingDigest, err := recoveryUCIExposureBindingDigest()
	if err != nil {
		return fmt.Errorf("encode UCI exposure binding: %w", err)
	}
	completionBindingDigest, err := recoveryUCICompletionBindingDigest()
	if err != nil {
		return fmt.Errorf("encode UCI completion binding: %w", err)
	}
	projectionDigest := recoveryUCIOpaqueDigest("projection-content")
	statements := []struct {
		query string
		args  []any
	}{
		{
			`INSERT INTO sources (source_id, auth_realm, kind, display_name, state) VALUES (?, ?, ?, ?, ?)`,
			[]any{recoveryUCISourceID, recoveryUCIRealm, "git", "Recovery UCI source", "active"},
		},
		{
			`INSERT INTO ci_profiles (profile_id, parser_bundle_digest, resolver_revision, chunker_revision, ignore_policy_digest, build_context_json, secret_policy_revision) VALUES (?, ?, ?, ?, ?, ?::jsonb, ?)`,
			[]any{recoveryUCIProfileID, recoveryUCIOpaqueDigest("parser-bundle"), "recovery-uci-resolver-v1", "recovery-uci-chunker-v1", recoveryUCIOpaqueDigest("ignore-policy"), `{"fixture":"recovery-uci"}`, "recovery-uci-secret-policy-v1"},
		},
		{
			`INSERT INTO ci_checkouts (checkout_id, source_id, workstation_id, incarnation_id, kind, owner_principal, locator_ref, state) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			[]any{recoveryUCICheckoutID, recoveryUCISourceID, "recovery-uci-workstation", recoveryUCIIncarnationID, "working_tree", "recovery-uci-principal", "file:///recovery-uci", "registered"},
		},
		{
			`INSERT INTO ci_views (view_id, checkout_id, source_id, incarnation_id, generation, profile_id, head_oid, object_format, ref_label, dirty, observed_fs_seq, scan_start, scan_end, manifest_digest, state, coverage_json, published_at) VALUES (?, ?, ?, ?, 1, ?, ?, ?, ?, false, 1, now() - interval '1 second', now(), ?, ?, ?::jsonb, now())`,
			[]any{recoveryUCIViewID, recoveryUCICheckoutID, recoveryUCISourceID, recoveryUCIIncarnationID, recoveryUCIProfileID, recoveryUCIHeadOID, "sha1", "refs/heads/recovery-uci", recoveryUCIOpaqueDigest("manifest"), "published", `{"fixture":"recovery-uci"}`},
		},
		{
			`UPDATE ci_checkouts SET current_view_id = ? WHERE checkout_id = ?`,
			[]any{recoveryUCIViewID, recoveryUCICheckoutID},
		},
		{
			`INSERT INTO ci_blobs (blob_id, source_id, protection_domain, content_digest, byte_length, safe_content, encoding, storage_state) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			[]any{recoveryUCIBlobID, recoveryUCISourceID, "recovery", projectionDigest, int64(len(recoveryUCIProjectionContent)), []byte(recoveryUCIProjectionContent), "utf-8", "stored"},
		},
		{
			`INSERT INTO ci_parse_artifacts (artifact_id, source_id, blob_id, language, parser_revision, grammar_digest, extraction_profile_digest, status, diagnostics) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?::jsonb)`,
			[]any{recoveryUCIArtifactID, recoveryUCISourceID, recoveryUCIBlobID, "go", "recovery-uci-parser-v1", recoveryUCIOpaqueDigest("grammar"), recoveryUCIOpaqueDigest("extraction-profile"), "complete", `{"fixture":"recovery-uci"}`},
		},
		{
			`INSERT INTO ci_chunks (chunk_id, source_id, artifact_id, chunk_kind, ordinal, byte_start, byte_end, content_digest, text_for_search) VALUES (?, ?, ?, ?, 0, 0, ?, ?, ?)`,
			[]any{recoveryUCIChunkID, recoveryUCISourceID, recoveryUCIArtifactID, "file", len(recoveryUCIProjectionContent), projectionDigest, recoveryUCIProjectionContent},
		},
		{
			`UPDATE ci_parse_artifacts SET facts_digest = ?, sealed_at = now() WHERE artifact_id = ? AND source_id = ?`,
			[]any{recoveryUCIOpaqueDigest("facts"), recoveryUCIArtifactID, recoveryUCISourceID},
		},
		{
			`INSERT INTO ci_memberships (membership_id, source_id, checkout_id, path_key, display_path, artifact_id, file_state, mode, valid_from_generation) VALUES (?, ?, ?, ?, ?, ?, ?, ?, 1)`,
			[]any{recoveryUCIMembershipID, recoveryUCISourceID, recoveryUCICheckoutID, "recovery/uci.go", "recovery/uci.go", recoveryUCIArtifactID, "present", "100644"},
		},
		{
			`INSERT INTO uci_exposures (exposure_id, exposure_ref, auth_realm, source_id, checkout_id, view_id, client_ref, client_session_ref, request_ref, operation_kind, result_state, retrieval_mode, coverage_state, evidence_source, certainty, idempotency_key, idempotency_binding_digest, recorded_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, now())`,
			[]any{recoveryUCIExposureID, recoveryUCIExposureRef, recoveryUCIRealm, recoveryUCISourceID, recoveryUCICheckoutID, recoveryUCIViewID, recoveryUCIOpaqueDigest("client"), recoveryUCIOpaqueDigest("client-session"), recoveryUCIOpaqueDigest("request"), "code_search", "ok", "lexical", "complete", "fts", "established", recoveryUCIOpaqueDigest("idempotency"), exposureBindingDigest},
		},
		{
			`INSERT INTO uci_completion_evidence (completion_evidence_id, exposure_id, supported_host_ref, callback_ref, outcome, idempotency_key, idempotency_binding_digest, occurred_at) VALUES (?, ?, ?, ?, ?, ?, ?, now())`,
			[]any{recoveryUCICompletionEvidenceID, recoveryUCIExposureID, "recovery-uci-supported-host", "recovery-uci-callback", "succeeded", "recovery-uci-completion-idempotency", completionBindingDigest},
		},
	}
	for _, statement := range statements {
		if err := store.DB.WithContext(ctx).Exec(statement.query, statement.args...).Error; err != nil {
			return fmt.Errorf("seed UCI recovery fixture: %w", err)
		}
	}

	snapshot, err := captureRecoveryIntegrity(ctx, store)
	if err != nil {
		return err
	}
	if err := store.DB.WithContext(ctx).Exec(`
		CREATE TABLE IF NOT EXISTS recovery_fixture_integrity (
			fixture_key TEXT PRIMARY KEY,
			credential_ciphertext_digest TEXT NOT NULL,
			credential_fingerprint_digest TEXT NOT NULL,
			document_hash_digest TEXT NOT NULL,
			code_hash_digest TEXT NOT NULL
		)
	`).Error; err != nil {
		return fmt.Errorf("create recovery integrity snapshot: %w", err)
	}
	if err := store.DB.WithContext(ctx).Exec(`
		INSERT INTO recovery_fixture_integrity (
			fixture_key, credential_ciphertext_digest, credential_fingerprint_digest,
			document_hash_digest, code_hash_digest
		) VALUES (?, ?, ?, ?, ?)
	`, recoveryUCIFixtureKey, snapshot.CredentialCiphertextDigest, snapshot.CredentialFingerprintDigest, snapshot.DocumentHashDigest, snapshot.CodeHashDigest).Error; err != nil {
		return fmt.Errorf("store recovery integrity snapshot: %w", err)
	}
	return nil
}

func assertUCI(ctx context.Context, store *engramgorm.Store) error {
	current, err := captureRecoveryIntegrity(ctx, store)
	if err != nil {
		return err
	}
	var expected recoveryIntegritySnapshot
	if err := store.DB.WithContext(ctx).Raw(`
		SELECT credential_ciphertext_digest, credential_fingerprint_digest, document_hash_digest, code_hash_digest
		FROM recovery_fixture_integrity
		WHERE fixture_key = ?
	`, recoveryUCIFixtureKey).Scan(&expected).Error; err != nil {
		return fmt.Errorf("read recovery integrity snapshot: %w", err)
	}
	if expected.CredentialCiphertextDigest == "" || expected.CredentialFingerprintDigest == "" || expected.DocumentHashDigest == "" || expected.CodeHashDigest == "" {
		return fmt.Errorf("recovery integrity snapshot missing")
	}
	if expected != current {
		return fmt.Errorf("recovery product and crypto integrity mismatch")
	}

	exposureBindingDigest, err := recoveryUCIExposureBindingDigest()
	if err != nil {
		return fmt.Errorf("encode UCI exposure assertion: %w", err)
	}
	completionBindingDigest, err := recoveryUCICompletionBindingDigest()
	if err != nil {
		return fmt.Errorf("encode UCI completion assertion: %w", err)
	}
	projectionDigest := recoveryUCIOpaqueDigest("projection-content")
	checks := []struct {
		label string
		query string
		args  []any
		want  int64
	}{
		{"UCI source", `SELECT COUNT(*) FROM sources WHERE source_id = ? AND auth_realm = ? AND kind = 'git' AND state = 'active'`, []any{recoveryUCISourceID, recoveryUCIRealm}, 1},
		{"UCI profile", `SELECT COUNT(*) FROM ci_profiles WHERE profile_id = ? AND parser_bundle_digest = ? AND ignore_policy_digest = ?`, []any{recoveryUCIProfileID, recoveryUCIOpaqueDigest("parser-bundle"), recoveryUCIOpaqueDigest("ignore-policy")}, 1},
		{"UCI checkout", `SELECT COUNT(*) FROM ci_checkouts WHERE checkout_id = ? AND source_id = ? AND incarnation_id = ? AND current_view_id = ? AND state = 'registered'`, []any{recoveryUCICheckoutID, recoveryUCISourceID, recoveryUCIIncarnationID, recoveryUCIViewID}, 1},
		{"UCI view", `SELECT COUNT(*) FROM ci_views WHERE view_id = ? AND checkout_id = ? AND source_id = ? AND incarnation_id = ? AND profile_id = ? AND manifest_digest = ? AND state = 'published'`, []any{recoveryUCIViewID, recoveryUCICheckoutID, recoveryUCISourceID, recoveryUCIIncarnationID, recoveryUCIProfileID, recoveryUCIOpaqueDigest("manifest")}, 1},
		{"UCI blob projection", `SELECT COUNT(*) FROM ci_blobs WHERE blob_id = ? AND source_id = ? AND content_digest = ? AND byte_length = ? AND storage_state = 'stored'`, []any{recoveryUCIBlobID, recoveryUCISourceID, projectionDigest, int64(len(recoveryUCIProjectionContent))}, 1},
		{"UCI parse projection", `SELECT COUNT(*) FROM ci_parse_artifacts WHERE artifact_id = ? AND source_id = ? AND blob_id = ? AND facts_digest = ? AND status = 'complete'`, []any{recoveryUCIArtifactID, recoveryUCISourceID, recoveryUCIBlobID, recoveryUCIOpaqueDigest("facts")}, 1},
		{"UCI chunk projection", `SELECT COUNT(*) FROM ci_chunks WHERE chunk_id = ? AND source_id = ? AND artifact_id = ? AND content_digest = ? AND text_for_search = ?`, []any{recoveryUCIChunkID, recoveryUCISourceID, recoveryUCIArtifactID, projectionDigest, recoveryUCIProjectionContent}, 1},
		{"UCI membership projection", `SELECT COUNT(*) FROM ci_memberships WHERE membership_id = ? AND source_id = ? AND checkout_id = ? AND artifact_id = ? AND file_state = 'present' AND valid_from_generation = 1 AND valid_to_generation IS NULL`, []any{recoveryUCIMembershipID, recoveryUCISourceID, recoveryUCICheckoutID, recoveryUCIArtifactID}, 1},
		{"UCI exposure evidence", `SELECT COUNT(*) FROM uci_exposures WHERE exposure_id = ? AND exposure_ref = ? AND auth_realm = ? AND source_id = ? AND checkout_id = ? AND view_id = ? AND client_ref = ? AND client_session_ref = ? AND request_ref = ? AND operation_kind = 'code_search' AND result_state = 'ok' AND retrieval_mode = 'lexical' AND coverage_state = 'complete' AND evidence_source = 'fts' AND certainty = 'established' AND idempotency_key = ? AND idempotency_binding_digest = ?`, []any{recoveryUCIExposureID, recoveryUCIExposureRef, recoveryUCIRealm, recoveryUCISourceID, recoveryUCICheckoutID, recoveryUCIViewID, recoveryUCIOpaqueDigest("client"), recoveryUCIOpaqueDigest("client-session"), recoveryUCIOpaqueDigest("request"), recoveryUCIOpaqueDigest("idempotency"), exposureBindingDigest}, 1},
		{"UCI completion evidence", `SELECT COUNT(*) FROM uci_completion_evidence WHERE completion_evidence_id = ? AND exposure_id = ? AND supported_host_ref = ? AND callback_ref = ? AND outcome = 'succeeded' AND idempotency_key = ? AND idempotency_binding_digest = ?`, []any{recoveryUCICompletionEvidenceID, recoveryUCIExposureID, "recovery-uci-supported-host", "recovery-uci-callback", "recovery-uci-completion-idempotency", completionBindingDigest}, 1},
		{"UCI append-only guards", `SELECT COUNT(*) FROM pg_trigger AS trigger_row JOIN pg_class AS relation ON relation.oid = trigger_row.tgrelid JOIN pg_namespace AS namespace ON namespace.oid = relation.relnamespace WHERE namespace.nspname = current_schema() AND relation.relname IN ('uci_exposures', 'uci_completion_evidence') AND trigger_row.tgname IN ('uci_exposures_append_only_guard', 'uci_completion_evidence_append_only_guard') AND trigger_row.tgenabled <> 'D' AND NOT trigger_row.tgisinternal`, nil, 2},
	}
	for _, check := range checks {
		var count int64
		if err := store.DB.WithContext(ctx).Raw(check.query, check.args...).Scan(&count).Error; err != nil {
			return fmt.Errorf("assert %s: %w", check.label, err)
		}
		if count != check.want {
			return fmt.Errorf("assert %s: expected %d rows, got %d", check.label, check.want, count)
		}
	}
	return nil
}

func captureRecoveryIntegrity(ctx context.Context, store *engramgorm.Store) (recoveryIntegritySnapshot, error) {
	credential, err := engramgorm.NewCredentialStore(store).Get(ctx, project, credentialKey)
	if err != nil || len(credential.EncryptedSecret) == 0 || credential.EncryptionKeyFingerprint == "" {
		return recoveryIntegritySnapshot{}, fmt.Errorf("read recovery credential integrity: %w", err)
	}
	document, err := engramgorm.NewDocumentStore(store).GetDocument(ctx, project, "recovery.md")
	if err != nil || document == nil || !document.Hash.Valid {
		return recoveryIntegritySnapshot{}, fmt.Errorf("read recovery document integrity: %w", err)
	}
	var codeHash string
	if err := store.DB.WithContext(ctx).Raw(`
		SELECT content_sha256
		FROM code_chunks
		WHERE project_id = ? AND file_path = ? AND byte_start = ?
	`, project, "recovery.go", 0).Scan(&codeHash).Error; err != nil || codeHash == "" {
		return recoveryIntegritySnapshot{}, fmt.Errorf("read recovery code hash integrity: %w", err)
	}
	return recoveryIntegritySnapshot{
		CredentialCiphertextDigest:  recoveryDigest(credential.EncryptedSecret),
		CredentialFingerprintDigest: recoveryDigest([]byte(credential.EncryptionKeyFingerprint)),
		DocumentHashDigest:          recoveryDigest([]byte(document.Hash.String)),
		CodeHashDigest:              recoveryDigest([]byte(codeHash)),
	}, nil
}

func recoveryUCIExposureBindingDigest() (string, error) {
	return recoveryCanonicalDigest(struct {
		AuthRealm        string `json:"auth_realm"`
		Certainty        string `json:"certainty"`
		CheckoutID       string `json:"checkout_id"`
		ClientRef        string `json:"client_ref"`
		ClientSessionRef string `json:"client_session_ref"`
		Coverage         string `json:"coverage_state"`
		Evidence         string `json:"evidence_source"`
		IdempotencyKey   string `json:"idempotency_key"`
		Kind             string `json:"kind"`
		Operation        string `json:"operation_kind"`
		RequestRef       string `json:"request_ref"`
		Result           string `json:"result_state"`
		Retrieval        string `json:"retrieval_mode"`
		SourceID         string `json:"source_id"`
		Version          int    `json:"version"`
		ViewID           string `json:"view_id"`
	}{
		AuthRealm:        recoveryUCIRealm,
		Certainty:        "established",
		CheckoutID:       recoveryUCICheckoutID,
		ClientRef:        recoveryUCIOpaqueDigest("client"),
		ClientSessionRef: recoveryUCIOpaqueDigest("client-session"),
		Coverage:         "complete",
		Evidence:         "fts",
		IdempotencyKey:   recoveryUCIOpaqueDigest("idempotency"),
		Kind:             "exposure",
		Operation:        "code_search",
		RequestRef:       recoveryUCIOpaqueDigest("request"),
		Result:           "ok",
		Retrieval:        "lexical",
		SourceID:         recoveryUCISourceID,
		Version:          1,
		ViewID:           recoveryUCIViewID,
	})
}

func recoveryUCICompletionBindingDigest() (string, error) {
	return recoveryCanonicalDigest(struct {
		CallbackRef      string `json:"callback_ref"`
		ExposureRef      string `json:"exposure_ref"`
		IdempotencyKey   string `json:"idempotency_key"`
		Kind             string `json:"kind"`
		Outcome          string `json:"outcome"`
		SupportedHostRef string `json:"supported_host_ref"`
		Version          int    `json:"version"`
	}{
		CallbackRef:      "recovery-uci-callback",
		ExposureRef:      recoveryUCIExposureRef,
		IdempotencyKey:   "recovery-uci-completion-idempotency",
		Kind:             "completion",
		Outcome:          "succeeded",
		SupportedHostRef: "recovery-uci-supported-host",
		Version:          1,
	})
}

func recoveryUCIOpaqueDigest(value string) string {
	return recoveryDigest([]byte("recovery-uci:" + value))
}

func recoveryCanonicalDigest(value any) (string, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	return recoveryDigest(encoded), nil
}

func recoveryDigest(value []byte) string {
	digest := sha256.Sum256(value)
	return "sha256:" + hex.EncodeToString(digest[:])
}

func fatalf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(1)
}
