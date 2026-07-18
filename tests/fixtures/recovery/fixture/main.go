package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
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
	project         = "recovery-fixture"
	memoryContent   = "Durable recovery memory sentinel remains recallable after restore"
	ruleContent     = "Always preserve the recovery sentinel during PostgreSQL recovery"
	credentialKey   = "recovery.api_key"
	credentialSecret = "recovery-secret-value"
	documentBody    = "Recovery document sentinel body"
	issueTitle      = "Recovery issue sentinel"
	codeContent     = "func RecoverySentinel() string { return \"restored\" }"
)

func main() {
	action := flag.String("action", "assert", "seed or assert")
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

func fatalf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(1)
}
