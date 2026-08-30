// Package hap01cfixture controls the isolated PostgreSQL fixture used only by
// the HAP-01C qualification runner.
package hap01cfixture

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/lib/pq"
	"github.com/thebtf/engram/internal/auth"
	gormdb "github.com/thebtf/engram/internal/db/gorm"
	"github.com/thebtf/engram/pkg/cognitive"
	"github.com/thebtf/engram/pkg/models"
	"golang.org/x/crypto/bcrypt"
	gormlib "gorm.io/gorm"
	"gorm.io/gorm/logger"
)

const (
	seedReceiptSchema     = "hap-01c-fixture-seed-receipt/1"
	rotationReceiptSchema = "hap-01c-fixture-rotation-receipt/1"
	snapshotReceiptSchema = "hap-01c-fixture-snapshot/1"
	secretsSchema         = "hap-01c-fixture-secrets/1"

	maxDSNBytes          = 4 * 1024
	maxRequestBytes      = 8 * 1024
	maxSecretsBytes      = 16 * 1024
	maxAmbientQueryBytes = 512

	fixtureSourceAgent = "hap01c-fixture"
	legacyDirectTTL    = time.Hour
)

var (
	runIDPattern    = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,55}$`)
	legacyIDPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,55}$`)
)

// BoundaryError reports a refusal at the fixture's safety boundary. Its text is
// deliberately limited to a stable code and never includes a path, DSN, token,
// database error, or other private fixture value.
type BoundaryError struct {
	code string
}

func (e *BoundaryError) Error() string {
	return "hap01c fixture boundary: " + e.code
}

func boundary(code string) error {
	return &BoundaryError{code: code}
}

// IsBoundaryError reports whether err is a safety-boundary refusal suitable for
// the CLI's dedicated nonzero exit status.
func IsBoundaryError(err error) bool {
	var target *BoundaryError
	return errors.As(err, &target)
}

// ValidateRunID verifies that a run identifier can safely select one dedicated
// PostgreSQL database and one runner-owned scratch directory.
func ValidateRunID(runID string) error {
	if !runIDPattern.MatchString(runID) {
		return boundary("INVALID_RUN_ID")
	}
	return nil
}

// Fixture owns one already-isolated HAP-01C scratch database connection.
type Fixture struct {
	store *gormdb.Store
	runID string
	deps  dependencies
}

type dependencies struct {
	openStore      func(gormdb.Config) (*gormdb.Store, error)
	preflight      func(context.Context, string, string) error
	now            func() time.Time
	random         io.Reader
	replaceSecrets func(string, []byte) error
}

func defaultDependencies() dependencies {
	return dependencies{
		openStore: gormdb.NewStore,
		preflight: preflightPostgres,
		now: func() time.Time {
			return time.Now().UTC()
		},
		random:         rand.Reader,
		replaceSecrets: atomicReplacePrivateFile,
	}
}

// Open reads a private DSN file, proves that it reaches the exact run-owned
// loopback PostgreSQL 17+ database, then uses the project's Store constructor
// to apply the current migration set and verify the resulting substrate.
func Open(ctx context.Context, dsnFile, runID string) (*Fixture, error) {
	return openWithDependencies(ctx, dsnFile, runID, defaultDependencies())
}

func openWithDependencies(ctx context.Context, dsnFile, runID string, deps dependencies) (*Fixture, error) {
	if err := ValidateRunID(runID); err != nil {
		return nil, err
	}
	if deps.openStore == nil || deps.preflight == nil || deps.now == nil || deps.random == nil || deps.replaceSecrets == nil {
		return nil, boundary("FIXTURE_DEPENDENCIES_UNAVAILABLE")
	}

	rawDSN, err := readBoundedFile(dsnFile, maxDSNBytes, true, "DSN")
	if err != nil {
		return nil, err
	}
	connection, err := parseDedicatedDSN(rawDSN, runID)
	if err != nil {
		return nil, err
	}
	ctx = nonNilContext(ctx)
	if err := deps.preflight(ctx, connection.dsn, connection.database); err != nil {
		return nil, err
	}

	store, err := deps.openStore(gormdb.Config{
		DSN:      connection.dsn,
		MaxConns: 2,
		LogLevel: logger.Silent,
	})
	if err != nil {
		return nil, boundary("DATABASE_OPEN_FAILED")
	}
	fixture := &Fixture{store: store, runID: runID, deps: deps}
	if err := fixture.verifyOpenedStore(ctx, connection.database); err != nil {
		_ = store.Close()
		return nil, err
	}
	return fixture, nil
}

// Close releases the underlying project Store connection pool.
func (f *Fixture) Close() error {
	if f == nil || f.store == nil {
		return nil
	}
	return f.store.Close()
}

type dedicatedDSN struct {
	dsn      string
	database string
}

func parseDedicatedDSN(raw []byte, runID string) (dedicatedDSN, error) {
	dsn := strings.TrimSpace(string(raw))
	if dsn == "" || strings.IndexByte(dsn, 0) >= 0 {
		return dedicatedDSN{}, boundary("INVALID_DSN")
	}

	parsed, err := url.ParseRequestURI(dsn)
	if err != nil || parsed.Opaque != "" || parsed.Fragment != "" || parsed.RawFragment != "" {
		return dedicatedDSN{}, boundary("INVALID_DSN")
	}
	scheme := strings.ToLower(parsed.Scheme)
	if scheme != "postgres" && scheme != "postgresql" {
		return dedicatedDSN{}, boundary("INVALID_DSN_SCHEME")
	}
	if parsed.User == nil || parsed.User.Username() == "" {
		return dedicatedDSN{}, boundary("DSN_USER_REQUIRED")
	}
	if _, passwordPresent := parsed.User.Password(); !passwordPresent {
		return dedicatedDSN{}, boundary("DSN_PASSWORD_REQUIRED")
	}
	host := strings.ToLower(parsed.Hostname())
	if !isLoopbackHost(host) {
		return dedicatedDSN{}, boundary("NON_LOOPBACK_DSN_HOST")
	}
	port := parsed.Port()
	if port != "" {
		value, parseErr := strconv.Atoi(port)
		if parseErr != nil || value < 1 || value > 65535 {
			return dedicatedDSN{}, boundary("INVALID_DSN_PORT")
		}
	} else {
		parsed.Host = net.JoinHostPort(host, "5432")
	}

	expectedDatabase := "hap01c_" + runID
	if parsed.Path != "/"+expectedDatabase {
		return dedicatedDSN{}, boundary("NON_DEDICATED_DATABASE")
	}
	query, err := url.ParseQuery(parsed.RawQuery)
	if err != nil || len(query) != 1 || len(query["sslmode"]) != 1 || query.Get("sslmode") != "disable" {
		return dedicatedDSN{}, boundary("DSN_SSLMODE_REQUIRED")
	}
	parsed.RawQuery = "sslmode=disable"
	return dedicatedDSN{dsn: parsed.String(), database: expectedDatabase}, nil
}

func isLoopbackHost(host string) bool {
	if host == "localhost" {
		return true
	}
	address := net.ParseIP(host)
	return address != nil && address.IsLoopback()
}

func preflightPostgres(parent context.Context, dsn, expectedDatabase string) error {
	config, err := pgx.ParseConfig(dsn)
	if err != nil {
		return boundary("INVALID_DSN")
	}
	db := stdlib.OpenDB(*config)
	defer db.Close()

	ctx, cancel := context.WithTimeout(nonNilContext(parent), 10*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		return boundary("DATABASE_UNAVAILABLE")
	}

	var database, versionText string
	if err := db.QueryRowContext(ctx, `
		SELECT current_database(), current_setting('server_version_num')
	`).Scan(&database, &versionText); err != nil {
		return boundary("DATABASE_PRECHECK_FAILED")
	}
	if database != expectedDatabase {
		return boundary("NON_DEDICATED_DATABASE")
	}
	version, err := strconv.Atoi(versionText)
	if err != nil || version < 170000 {
		return boundary("POSTGRES_17_REQUIRED")
	}

	var vectorAvailable bool
	if err := db.QueryRowContext(ctx, `
		SELECT EXISTS (SELECT 1 FROM pg_available_extensions WHERE name = 'vector')
	`).Scan(&vectorAvailable); err != nil || !vectorAvailable {
		return boundary("PGVECTOR_UNAVAILABLE")
	}
	return nil
}

func (f *Fixture) verifyOpenedStore(ctx context.Context, expectedDatabase string) error {
	if f == nil || f.store == nil || f.store.GetRawDB() == nil {
		return boundary("FIXTURE_NOT_OPEN")
	}
	ctx = nonNilContext(ctx)
	state, err := f.store.GetMigrationState(ctx)
	if err != nil || !containsMigration(state.AppliedIDs, "167_api_tokens_expires_at") {
		return boundary("CURRENT_MIGRATIONS_REQUIRED")
	}

	db := f.store.GetRawDB()
	var database string
	var vectorInstalled bool
	if err := db.QueryRowContext(ctx, `
		SELECT current_database(), EXISTS (SELECT 1 FROM pg_extension WHERE extname = 'vector')
	`).Scan(&database, &vectorInstalled); err != nil {
		return boundary("DATABASE_VERIFICATION_FAILED")
	}
	if database != expectedDatabase {
		return boundary("NON_DEDICATED_DATABASE")
	}
	if !vectorInstalled {
		return boundary("PGVECTOR_UNAVAILABLE")
	}
	for _, table := range []string{
		"projects",
		"project_resolution_attempts",
		"memories",
		"behavioral_rules",
		"api_tokens",
		"sdk_sessions",
		"injection_log",
		"attention_events",
		"agent_session_state",
	} {
		var exists bool
		if err := db.QueryRowContext(ctx, `SELECT to_regclass($1) IS NOT NULL`, "public."+table).Scan(&exists); err != nil || !exists {
			return boundary("CURRENT_MIGRATIONS_REQUIRED")
		}
	}
	var expiryColumn bool
	if err := db.QueryRowContext(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM information_schema.columns
			WHERE table_schema = 'public' AND table_name = 'api_tokens' AND column_name = 'expires_at'
		)
	`).Scan(&expiryColumn); err != nil || !expiryColumn {
		return boundary("CURRENT_MIGRATIONS_REQUIRED")
	}
	return nil
}

func containsMigration(ids []string, want string) bool {
	for _, id := range ids {
		if id == want {
			return true
		}
	}
	return false
}

// SeedReceipt reports only redacted fixture facts. It deliberately has no raw
// identifiers, paths, DSN, principal, token, or secret value.
type SeedReceipt struct {
	Schema                 string           `json:"schema"`
	RunIDSHA256            string           `json:"run_id_sha256"`
	AnchorProjectIDSHA256  string           `json:"anchor_project_id_sha256"`
	CanonicalKeySHA256     string           `json:"canonical_project_key_sha256"`
	LegacyProjectIDSHA256  string           `json:"legacy_project_id_sha256"`
	MemoryIDSHA256         string           `json:"memory_id_sha256"`
	RuleIDSHA256           string           `json:"rule_id_sha256"`
	SessionIDSHA256        string           `json:"session_id_sha256"`
	AttentionEventIDSHA256 string           `json:"attention_event_id_sha256"`
	ProjectRows            int              `json:"project_rows"`
	MemoryRows             int              `json:"memory_rows"`
	RuleRows               int              `json:"rule_rows"`
	SessionRows            int              `json:"session_rows"`
	AttentionEventRows     int              `json:"attention_event_rows"`
	Keycards               []KeycardReceipt `json:"keycards"`
}

// KeycardReceipt retains only the class, a one-way ID reference, raw-token
// length, and whether an expiry exists.
type KeycardReceipt struct {
	Class         string `json:"class"`
	IDSHA256      string `json:"id_sha256"`
	TokenLength   int    `json:"token_length"`
	ExpiryPresent bool   `json:"expiry_present"`
	Revoked       bool   `json:"revoked"`
}

type seedRequest struct {
	RunID               string
	AnchorProjectID     string
	CanonicalProjectKey string
	LegacyProjectID     string
	AmbientQueryText    string
}

type keycardClass string

const (
	ordinaryWorkstationCard keycardClass = "ordinary_workstation"
	registrationServiceCard keycardClass = "registration_service"
	projectServiceCard      keycardClass = "canonical_project_service"
	legacyDirectCard        keycardClass = "legacy_direct_agent"
)

type pendingKeycard struct {
	class         keycardClass
	name          string
	principal     string
	principalKind auth.PrincipalKind
	expiresAt     *time.Time
	raw           string
	tokenHash     string
	tokenPrefix   string
}

type issuedKeycard struct {
	pending pendingKeycard
	record  *gormdb.APIToken
}

// Seed applies the current project's stores to create one V3 binding, one
// canonical memory and behavioral rule, session/ambient inputs, and the four
// real keycard classes. Raw keycards are written only to a newly created,
// private secrets file.
func (f *Fixture) Seed(ctx context.Context, requestFile, secretsOut string) (SeedReceipt, error) {
	if err := f.requireOpen(); err != nil {
		return SeedReceipt{}, err
	}
	request, err := readSeedRequest(requestFile, f.runID)
	if err != nil {
		return SeedReceipt{}, err
	}
	cards, err := f.seedKeycards(request)
	if err != nil {
		return SeedReceipt{}, err
	}

	ctx = nonNilContext(ctx)
	var (
		createdMemory    *models.Memory
		createdRule      *models.BehavioralRule
		createdEventID   int64
		createdSessionID string
		issued           []issuedKeycard
		secretsWritten   bool
	)
	err = f.store.GetDB().WithContext(ctx).Transaction(func(tx *gormlib.DB) error {
		project := &gormdb.Project{
			ID:              request.CanonicalProjectKey,
			ProjectKey:      validNullString(request.CanonicalProjectKey),
			AnchorProjectID: validNullString(request.AnchorProjectID),
			IdentityScope:   validNullString("repository"),
			IdentityStatus:  validNullString("active"),
			LegacyIDs:       pq.StringArray{request.LegacyProjectID},
			DisplayName:     validNullString("hap01c-" + f.runID),
		}
		if err := tx.Create(project).Error; err != nil {
			return err
		}

		txStore := &gormdb.Store{DB: tx}
		tokenStore := gormdb.NewTokenStore(txStore)
		issued = make([]issuedKeycard, 0, len(cards))
		for _, card := range cards {
			record, err := tokenStore.CreateWithPrincipal(
				ctx,
				card.name,
				card.tokenHash,
				card.tokenPrefix,
				"read-write",
				card.principal,
				string(card.principalKind),
				card.expiresAt,
			)
			if err != nil {
				return err
			}
			issued = append(issued, issuedKeycard{pending: card, record: record})
		}

		createdSessionID = fixtureSessionID(f.runID)
		sessionStore := gormdb.NewSessionStore(txStore)
		if _, err := sessionStore.CreateSDKSession(ctx, createdSessionID, request.CanonicalProjectKey, "hap01c qualification fixture"); err != nil {
			return err
		}
		stateStore := gormdb.NewStateStore(tx, nil)
		if err := stateStore.WriteSessionState(ctx, createdSessionID, cognitive.SessionStateSlots{
			Focus:     map[string]interface{}{"fixture": "hap01c"},
			Execution: map[string]interface{}{"next_action": "relay qualification"},
			Horizons:  map[string]interface{}{"scope": "session"},
		}); err != nil {
			return err
		}

		memoryStore := gormdb.NewMemoryStore(txStore)
		createdMemory, err = memoryStore.Create(ctx, &models.Memory{
			Project:             request.CanonicalProjectKey,
			Content:             request.AmbientQueryText,
			SourceAgent:         fixtureSourceAgent,
			Tags:                []string{fixtureTag(f.runID), "hap01c"},
			PrivacyScope:        "project",
			SourceSessions:      []string{createdSessionID},
			SourceWorkstationID: issued[0].record.ID,
			OwnerPrincipal:      issued[0].pending.principal,
			OwnerPrincipalKind:  string(issued[0].pending.principalKind),
			AgentVisibility:     models.AgentVisibilityShared,
		})
		if err != nil {
			return err
		}

		ruleStore := gormdb.NewBehavioralRulesStore(txStore)
		projectID := request.CanonicalProjectKey
		createdRule, err = ruleStore.Create(ctx, &models.BehavioralRule{
			Project:  &projectID,
			Content:  "HAP-01C qualification fixture rule.",
			EditedBy: fixtureSourceAgent,
			Priority: 1,
		})
		if err != nil {
			return err
		}

		attentionStore := gormdb.NewAttentionEventStore(tx)
		event, err := attentionStore.Create(ctx, cognitive.AttentionEventRecord{
			Project:        request.CanonicalProjectKey,
			SessionID:      createdSessionID,
			SourceTurnHash: "sha256:" + redactedID("attention", f.runID),
			DerivedIntent:  "maintain HAP-01C qualification state",
			AgentConfirmed: true,
			Horizon:        "session",
			PrivacyClass:   "internal",
		})
		if err != nil {
			return err
		}
		createdEventID = event.ID

		secretBytes, err := json.Marshal(secretsFromIssued(f.runID, issued))
		if err != nil {
			return err
		}
		if err := writeNewPrivateFile(secretsOut, secretBytes); err != nil {
			return err
		}
		secretsWritten = true
		return nil
	})
	if err != nil {
		if secretsWritten {
			_ = os.Remove(secretsOut)
		}
		if IsBoundaryError(err) {
			return SeedReceipt{}, err
		}
		return SeedReceipt{}, boundary("SEED_FAILED")
	}

	return SeedReceipt{
		Schema:                 seedReceiptSchema,
		RunIDSHA256:            redactedID("run", f.runID),
		AnchorProjectIDSHA256:  redactedID("anchor", request.AnchorProjectID),
		CanonicalKeySHA256:     redactedID("project", request.CanonicalProjectKey),
		LegacyProjectIDSHA256:  redactedID("legacy", request.LegacyProjectID),
		MemoryIDSHA256:         redactedID("memory", strconv.FormatInt(createdMemory.ID, 10)),
		RuleIDSHA256:           redactedID("rule", strconv.FormatInt(createdRule.ID, 10)),
		SessionIDSHA256:        redactedID("session", createdSessionID),
		AttentionEventIDSHA256: redactedID("attention_event", strconv.FormatInt(createdEventID, 10)),
		ProjectRows:            1,
		MemoryRows:             1,
		RuleRows:               1,
		SessionRows:            1,
		AttentionEventRows:     1,
		Keycards:               keycardReceipts(issued),
	}, nil
}

func (f *Fixture) seedKeycards(request seedRequest) ([]pendingKeycard, error) {
	workstation := "hap01c-" + f.runID
	legacyExpiry := f.deps.now().UTC().Add(legacyDirectTTL)
	specs := []struct {
		class         keycardClass
		name          string
		principal     string
		principalKind auth.PrincipalKind
		expiresAt     *time.Time
	}{
		{
			class:         ordinaryWorkstationCard,
			name:          tokenName(f.runID, ordinaryWorkstationCard),
			principal:     "agent/" + workstation,
			principalKind: auth.PrincipalKindAgent,
		},
		{
			class:         registrationServiceCard,
			name:          tokenName(f.runID, registrationServiceCard),
			principal:     auth.RegistrationServicePrincipal(workstation),
			principalKind: auth.PrincipalKindService,
		},
		{
			class:         projectServiceCard,
			name:          tokenName(f.runID, projectServiceCard),
			principal:     auth.ProjectServicePrincipal(request.CanonicalProjectKey),
			principalKind: auth.PrincipalKindService,
		},
		{
			class:         legacyDirectCard,
			name:          tokenName(f.runID, legacyDirectCard),
			principal:     auth.LegacyDirectPrincipal(request.CanonicalProjectKey),
			principalKind: auth.PrincipalKindAgent,
			expiresAt:     &legacyExpiry,
		},
	}

	cards := make([]pendingKeycard, 0, len(specs))
	for _, spec := range specs {
		card, err := f.newKeycard(spec.class, spec.name, spec.principal, spec.principalKind, spec.expiresAt)
		if err != nil {
			return nil, err
		}
		cards = append(cards, card)
	}
	return cards, nil
}

func (f *Fixture) newKeycard(class keycardClass, name, principal string, principalKind auth.PrincipalKind, expiresAt *time.Time) (pendingKeycard, error) {
	now := f.deps.now().UTC()
	if err := auth.ValidateHAPPrincipalForIssuance(principal, principalKind, expiresAt, now); err != nil {
		return pendingKeycard{}, boundary("KEYCARD_CONTRACT_INVALID")
	}
	bytes := make([]byte, auth.TokenBodyLen/2)
	if _, err := io.ReadFull(f.deps.random, bytes); err != nil {
		return pendingKeycard{}, boundary("KEYCARD_RANDOMNESS_UNAVAILABLE")
	}
	raw := auth.TokenRawPrefix + hex.EncodeToString(bytes)
	hash, err := bcrypt.GenerateFromPassword([]byte(raw), bcrypt.DefaultCost)
	if err != nil {
		return pendingKeycard{}, boundary("KEYCARD_HASH_FAILED")
	}

	var copiedExpiry *time.Time
	if expiresAt != nil {
		value := expiresAt.UTC()
		copiedExpiry = &value
	}
	return pendingKeycard{
		class:         class,
		name:          name,
		principal:     principal,
		principalKind: principalKind,
		expiresAt:     copiedExpiry,
		raw:           raw,
		tokenHash:     string(hash),
		tokenPrefix:   raw[len(auth.TokenRawPrefix) : len(auth.TokenRawPrefix)+auth.TokenPrefixLen],
	}, nil
}

func keycardReceipts(issued []issuedKeycard) []KeycardReceipt {
	receipts := make([]KeycardReceipt, 0, len(issued))
	for _, card := range issued {
		receipts = append(receipts, KeycardReceipt{
			Class:         string(card.pending.class),
			IDSHA256:      redactedID("keycard", card.record.ID),
			TokenLength:   len(card.pending.raw),
			ExpiryPresent: card.record.ExpiresAt != nil,
			Revoked:       card.record.Revoked,
		})
	}
	return receipts
}

type secretsFile struct {
	Schema              string `json:"schema"`
	RunID               string `json:"run_id"`
	OrdinaryToken       string `json:"ordinary_token"`
	RegistrationToken   string `json:"registration_token"`
	ProjectToken        string `json:"project_token"`
	LegacyDirectToken   string `json:"legacy_direct_token"`
	OrdinaryTokenID     string `json:"ordinary_token_id"`
	RegistrationTokenID string `json:"registration_token_id"`
	ProjectTokenID      string `json:"project_token_id"`
	LegacyDirectTokenID string `json:"legacy_direct_token_id"`
}

func secretsFromIssued(runID string, issued []issuedKeycard) secretsFile {
	secrets := secretsFile{Schema: secretsSchema, RunID: runID}
	for _, card := range issued {
		switch card.pending.class {
		case ordinaryWorkstationCard:
			secrets.OrdinaryToken = card.pending.raw
			secrets.OrdinaryTokenID = card.record.ID
		case registrationServiceCard:
			secrets.RegistrationToken = card.pending.raw
			secrets.RegistrationTokenID = card.record.ID
		case projectServiceCard:
			secrets.ProjectToken = card.pending.raw
			secrets.ProjectTokenID = card.record.ID
		case legacyDirectCard:
			secrets.LegacyDirectToken = card.pending.raw
			secrets.LegacyDirectTokenID = card.record.ID
		}
	}
	return secrets
}

// RotationReceipt records project-keycard rotation without exposing either the
// old or replacement keycard, principal, or raw database IDs.
type RotationReceipt struct {
	Schema          string         `json:"schema"`
	RunIDSHA256     string         `json:"run_id_sha256"`
	RevokedKeycard  KeycardReceipt `json:"revoked_keycard"`
	ReplacementCard KeycardReceipt `json:"replacement_keycard"`
}

// RotateProjectKeycard revokes the current real project keycard and creates a
// replacement for the identical project service principal. The secrets file is
// held by an exclusive scratch lock and atomically replaced as mode 0600.
func (f *Fixture) RotateProjectKeycard(ctx context.Context, secretsPath string) (RotationReceipt, error) {
	if err := f.requireOpen(); err != nil {
		return RotationReceipt{}, err
	}
	unlock, err := acquireSecretsLock(secretsPath)
	if err != nil {
		return RotationReceipt{}, err
	}
	defer unlock()

	rawSecrets, err := readBoundedFile(secretsPath, maxSecretsBytes, true, "SECRETS")
	if err != nil {
		return RotationReceipt{}, err
	}
	secrets, err := parseSecrets(rawSecrets, f.runID)
	if err != nil {
		return RotationReceipt{}, err
	}

	ctx = nonNilContext(ctx)
	var oldCard, replacement issuedKeycard
	err = f.store.GetDB().WithContext(ctx).Transaction(func(tx *gormlib.DB) error {
		txStore := &gormdb.Store{DB: tx}
		tokenStore := gormdb.NewTokenStore(txStore)
		current, err := tokenStore.GetByID(ctx, secrets.ProjectTokenID)
		if err != nil || current == nil || current.Revoked || !f.isCurrentProjectCard(tx, current) {
			return boundary("PROJECT_KEYCARD_NOT_CURRENT")
		}
		oldCard = issuedKeycard{
			pending: pendingKeycard{class: projectServiceCard, raw: secrets.ProjectToken},
			record:  current,
		}

		replacementName, err := f.rotatedProjectTokenName()
		if err != nil {
			return err
		}
		pending, err := f.newKeycard(projectServiceCard, replacementName, current.Principal, auth.PrincipalKindService, current.ExpiresAt)
		if err != nil {
			return err
		}
		created, err := tokenStore.CreateWithPrincipal(
			ctx,
			pending.name,
			pending.tokenHash,
			pending.tokenPrefix,
			"read-write",
			pending.principal,
			string(pending.principalKind),
			pending.expiresAt,
		)
		if err != nil {
			return err
		}
		if err := tokenStore.Revoke(ctx, current.ID); err != nil {
			return err
		}
		oldCard.record = &gormdb.APIToken{ID: current.ID, Revoked: true}
		replacement = issuedKeycard{pending: pending, record: created}
		return nil
	})
	if err != nil {
		if IsBoundaryError(err) {
			return RotationReceipt{}, err
		}
		return RotationReceipt{}, boundary("PROJECT_KEYCARD_ROTATION_FAILED")
	}

	nextSecrets := secrets
	nextSecrets.ProjectToken = replacement.pending.raw
	nextSecrets.ProjectTokenID = replacement.record.ID
	encoded, err := json.Marshal(nextSecrets)
	if err != nil {
		return RotationReceipt{}, boundary("SECRETS_ENCODING_FAILED")
	}
	replaceErr := f.deps.replaceSecrets(secretsPath, encoded)
	publishedRaw, readErr := readBoundedFile(secretsPath, maxSecretsBytes, true, "SECRETS")
	if readErr == nil {
		published, parseErr := parseSecrets(publishedRaw, f.runID)
		if parseErr == nil && published == nextSecrets {
			return rotationReceipt(f.runID, oldCard, replacement), nil
		}
		if parseErr == nil && published == secrets {
			if recoveryErr := f.compensateProjectKeycardRotation(ctx, oldCard.record.ID, replacement.record.ID); recoveryErr != nil {
				return RotationReceipt{}, recoveryErr
			}
			if replaceErr != nil {
				return RotationReceipt{}, replaceErr
			}
			return RotationReceipt{}, boundary("SECRETS_FILE_REPLACE_FAILED")
		}
	}
	return RotationReceipt{}, boundary("PROJECT_KEYCARD_RECONCILIATION_FAILED")
}

func rotationReceipt(runID string, oldCard, replacement issuedKeycard) RotationReceipt {
	return RotationReceipt{
		Schema:          rotationReceiptSchema,
		RunIDSHA256:     redactedID("run", runID),
		RevokedKeycard:  keycardReceipt(oldCard),
		ReplacementCard: keycardReceipt(replacement),
	}
}

func (f *Fixture) compensateProjectKeycardRotation(ctx context.Context, oldTokenID, replacementTokenID string) error {
	if oldTokenID == "" || replacementTokenID == "" {
		return boundary("PROJECT_KEYCARD_RECONCILIATION_FAILED")
	}
	now := f.deps.now().UTC()
	err := f.store.GetDB().WithContext(nonNilContext(ctx)).Transaction(func(tx *gormlib.DB) error {
		replacement := tx.Model(&gormdb.APIToken{}).Where("id = ?", replacementTokenID).
			Updates(map[string]interface{}{"revoked": true, "revoked_at": &now})
		if replacement.Error != nil || replacement.RowsAffected != 1 {
			return errors.New("replacement compensation failed")
		}
		old := tx.Model(&gormdb.APIToken{}).Where("id = ?", oldTokenID).
			Updates(map[string]interface{}{"revoked": false, "revoked_at": nil})
		if old.Error != nil || old.RowsAffected != 1 {
			return errors.New("old keycard compensation failed")
		}
		return nil
	})
	if err != nil {
		return boundary("PROJECT_KEYCARD_RECONCILIATION_FAILED")
	}
	return nil
}

func (f *Fixture) isCurrentProjectCard(tx *gormlib.DB, token *gormdb.APIToken) bool {
	if token == nil || token.Scope != "read-write" || token.PrincipalKind != string(auth.PrincipalKindService) {
		return false
	}
	baseName := tokenName(f.runID, projectServiceCard)
	if token.Name != baseName && !strings.HasPrefix(token.Name, baseName+"-rotated-") {
		return false
	}
	principal, ok := auth.ParseHAPPrincipal(token.Principal)
	if !ok || principal.Class != auth.HAPPrincipalProject || !validCanonicalUUID(principal.Subject) {
		return false
	}
	var matches int64
	if err := tx.Model(&gormdb.Project{}).
		Where("id = ? AND project_key = ? AND identity_status = ?", principal.Subject, principal.Subject, "active").
		Count(&matches).Error; err != nil {
		return false
	}
	return matches == 1
}

func (f *Fixture) rotatedProjectTokenName() (string, error) {
	bytes := make([]byte, 8)
	if _, err := io.ReadFull(f.deps.random, bytes); err != nil {
		return "", boundary("KEYCARD_RANDOMNESS_UNAVAILABLE")
	}
	return tokenName(f.runID, projectServiceCard) + "-rotated-" + hex.EncodeToString(bytes), nil
}

func keycardReceipt(card issuedKeycard) KeycardReceipt {
	return KeycardReceipt{
		Class:         string(card.pending.class),
		IDSHA256:      redactedID("keycard", card.record.ID),
		TokenLength:   len(card.pending.raw),
		ExpiryPresent: card.record.ExpiresAt != nil,
		Revoked:       card.record.Revoked,
	}
}

// SnapshotReceipt contains only exact durable database counters. Ambient
// attempted delivery is deliberately unavailable because CORE's ambient meter
// and queue are in-process-only, not durable database telemetry.
type SnapshotReceipt struct {
	Schema                           string `json:"schema"`
	RunIDSHA256                      string `json:"run_id_sha256"`
	ResolutionAttempts               int64  `json:"resolution_attempts"`
	RegistrationAttempts             int64  `json:"registration_attempts"`
	SessionStartAttempts             int64  `json:"session_start_attempts"`
	TargetMemoryInjectionCount       int64  `json:"target_memory_injection_count"`
	MemoryRows                       int64  `json:"memory_rows"`
	RuleRows                         int64  `json:"rule_rows"`
	ActiveProjectTokenCount          int64  `json:"active_project_token_count"`
	RevokedProjectTokenCount         int64  `json:"revoked_project_token_count"`
	AmbientDeliveryAvailable         bool   `json:"ambient_delivery_available"`
	AmbientDeliveryUnavailableReason string `json:"ambient_delivery_unavailable_reason"`
}

// Snapshot returns counters owned by the current database schema. It does not
// approximate the runtime-only ambient counter.
func (f *Fixture) Snapshot(ctx context.Context, requestFile string) (SnapshotReceipt, error) {
	if err := f.requireOpen(); err != nil {
		return SnapshotReceipt{}, err
	}
	request, err := readSeedRequest(requestFile, f.runID)
	if err != nil {
		return SnapshotReceipt{}, err
	}
	ctx = nonNilContext(ctx)
	db := f.store.GetDB().WithContext(ctx)

	receipt := SnapshotReceipt{
		Schema:                           snapshotReceiptSchema,
		RunIDSHA256:                      redactedID("run", f.runID),
		AmbientDeliveryAvailable:         false,
		AmbientDeliveryUnavailableReason: "NO_DURABLE_AMBIENT_ATTEMPT_COUNTER",
	}
	if err := db.Model(&gormdb.ProjectResolutionAttempt{}).
		Where("anchor_project_id = ?", request.AnchorProjectID).
		Count(&receipt.ResolutionAttempts).Error; err != nil {
		return SnapshotReceipt{}, boundary("SNAPSHOT_FAILED")
	}
	if err := db.Model(&gormdb.ProjectResolutionAttempt{}).
		Where("anchor_project_id = ? AND intent = ?", request.AnchorProjectID, "register_anchor").
		Count(&receipt.RegistrationAttempts).Error; err != nil {
		return SnapshotReceipt{}, boundary("SNAPSHOT_FAILED")
	}
	if err := db.Table("injection_log").
		Where("project = ?", request.CanonicalProjectKey).
		Count(&receipt.SessionStartAttempts).Error; err != nil {
		return SnapshotReceipt{}, boundary("SNAPSHOT_FAILED")
	}

	tagJSON, err := json.Marshal([]string{fixtureTag(f.runID)})
	if err != nil {
		return SnapshotReceipt{}, boundary("SNAPSHOT_FAILED")
	}
	if err := db.Raw(`
		SELECT COALESCE(SUM(injection_count), 0)
		FROM memories
		WHERE project = ? AND source_agent = ? AND tags @> ?::jsonb AND content = ?
	`, request.CanonicalProjectKey, fixtureSourceAgent, string(tagJSON), request.AmbientQueryText).Scan(&receipt.TargetMemoryInjectionCount).Error; err != nil {
		return SnapshotReceipt{}, boundary("SNAPSHOT_FAILED")
	}
	if err := db.Table("memories").
		Where("project = ? AND source_agent = ? AND tags @> ?::jsonb AND content = ?", request.CanonicalProjectKey, fixtureSourceAgent, string(tagJSON), request.AmbientQueryText).
		Count(&receipt.MemoryRows).Error; err != nil {
		return SnapshotReceipt{}, boundary("SNAPSHOT_FAILED")
	}
	if err := db.Table("behavioral_rules").
		Where("project = ? AND edited_by = ? AND content = ? AND deleted_at IS NULL", request.CanonicalProjectKey, fixtureSourceAgent, "HAP-01C qualification fixture rule.").
		Count(&receipt.RuleRows).Error; err != nil {
		return SnapshotReceipt{}, boundary("SNAPSHOT_FAILED")
	}

	projectTokenPattern := escapeLike(tokenName(f.runID, projectServiceCard)) + "%"
	if err := db.Model(&gormdb.APIToken{}).
		Where("name LIKE ? ESCAPE '\\' AND revoked = false", projectTokenPattern).
		Count(&receipt.ActiveProjectTokenCount).Error; err != nil {
		return SnapshotReceipt{}, boundary("SNAPSHOT_FAILED")
	}
	if err := db.Model(&gormdb.APIToken{}).
		Where("name LIKE ? ESCAPE '\\' AND revoked = true", projectTokenPattern).
		Count(&receipt.RevokedProjectTokenCount).Error; err != nil {
		return SnapshotReceipt{}, boundary("SNAPSHOT_FAILED")
	}
	return receipt, nil
}

func (f *Fixture) requireOpen() error {
	if f == nil || f.store == nil || f.store.GetDB() == nil {
		return boundary("FIXTURE_NOT_OPEN")
	}
	return nil
}

func readSeedRequest(path, expectedRunID string) (seedRequest, error) {
	raw, err := readBoundedFile(path, maxRequestBytes, false, "REQUEST")
	if err != nil {
		return seedRequest{}, err
	}
	values, err := decodeExactStringObject(raw, []string{
		"run_id",
		"anchor_project_id",
		"canonical_project_key",
		"legacy_project_id",
		"ambient_query_text",
	})
	if err != nil {
		return seedRequest{}, boundary("INVALID_SEED_REQUEST")
	}
	request := seedRequest{
		RunID:               values["run_id"],
		AnchorProjectID:     values["anchor_project_id"],
		CanonicalProjectKey: values["canonical_project_key"],
		LegacyProjectID:     values["legacy_project_id"],
		AmbientQueryText:    values["ambient_query_text"],
	}
	if request.RunID != expectedRunID || ValidateRunID(request.RunID) != nil ||
		!validCanonicalUUID(request.AnchorProjectID) ||
		!validCanonicalUUID(request.CanonicalProjectKey) ||
		!legacyIDPattern.MatchString(request.LegacyProjectID) ||
		strings.TrimSpace(request.AmbientQueryText) == "" || len(request.AmbientQueryText) > maxAmbientQueryBytes {
		return seedRequest{}, boundary("INVALID_SEED_REQUEST")
	}
	return request, nil
}

func parseSecrets(raw []byte, expectedRunID string) (secretsFile, error) {
	values, err := decodeExactStringObject(raw, []string{
		"schema",
		"run_id",
		"ordinary_token",
		"registration_token",
		"project_token",
		"legacy_direct_token",
		"ordinary_token_id",
		"registration_token_id",
		"project_token_id",
		"legacy_direct_token_id",
	})
	if err != nil {
		return secretsFile{}, boundary("INVALID_SECRETS_FILE")
	}
	secrets := secretsFile{
		Schema:              values["schema"],
		RunID:               values["run_id"],
		OrdinaryToken:       values["ordinary_token"],
		RegistrationToken:   values["registration_token"],
		ProjectToken:        values["project_token"],
		LegacyDirectToken:   values["legacy_direct_token"],
		OrdinaryTokenID:     values["ordinary_token_id"],
		RegistrationTokenID: values["registration_token_id"],
		ProjectTokenID:      values["project_token_id"],
		LegacyDirectTokenID: values["legacy_direct_token_id"],
	}
	if secrets.Schema != secretsSchema || secrets.RunID != expectedRunID ||
		!validRawKeycard(secrets.OrdinaryToken) ||
		!validRawKeycard(secrets.RegistrationToken) ||
		!validRawKeycard(secrets.ProjectToken) ||
		!validRawKeycard(secrets.LegacyDirectToken) ||
		!validCanonicalUUID(secrets.OrdinaryTokenID) ||
		!validCanonicalUUID(secrets.RegistrationTokenID) ||
		!validCanonicalUUID(secrets.ProjectTokenID) ||
		!validCanonicalUUID(secrets.LegacyDirectTokenID) {
		return secretsFile{}, boundary("INVALID_SECRETS_FILE")
	}
	return secrets, nil
}

func decodeExactStringObject(raw []byte, expectedKeys []string) (map[string]string, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	start, err := decoder.Token()
	if err != nil {
		return nil, err
	}
	delimiter, ok := start.(json.Delim)
	if !ok || delimiter != '{' {
		return nil, fmt.Errorf("expected object")
	}
	allowed := make(map[string]struct{}, len(expectedKeys))
	for _, key := range expectedKeys {
		allowed[key] = struct{}{}
	}
	values := make(map[string]string, len(expectedKeys))
	for decoder.More() {
		keyToken, err := decoder.Token()
		if err != nil {
			return nil, err
		}
		key, ok := keyToken.(string)
		if !ok {
			return nil, fmt.Errorf("expected key")
		}
		if _, allowed := allowed[key]; !allowed {
			return nil, fmt.Errorf("unknown key")
		}
		if _, duplicate := values[key]; duplicate {
			return nil, fmt.Errorf("duplicate key")
		}
		var value string
		if err := decoder.Decode(&value); err != nil {
			return nil, err
		}
		values[key] = value
	}
	end, err := decoder.Token()
	if err != nil {
		return nil, err
	}
	if delimiter, ok := end.(json.Delim); !ok || delimiter != '}' {
		return nil, fmt.Errorf("expected object end")
	}
	var trailing interface{}
	if err := decoder.Decode(&trailing); err != io.EOF {
		return nil, fmt.Errorf("trailing content")
	}
	if len(values) != len(expectedKeys) {
		return nil, fmt.Errorf("missing key")
	}
	return values, nil
}

func validCanonicalUUID(value string) bool {
	parsed, err := uuid.Parse(value)
	return err == nil && parsed.String() == value
}

func validRawKeycard(value string) bool {
	if !strings.HasPrefix(value, auth.TokenRawPrefix) || len(value) != auth.TokenTotalLen {
		return false
	}
	_, err := hex.DecodeString(value[len(auth.TokenRawPrefix):])
	return err == nil
}

func readBoundedFile(path string, maxBytes int, requirePrivate bool, label string) ([]byte, error) {
	if path == "" {
		return nil, boundary(label + "_FILE_REQUIRED")
	}
	initial, err := os.Lstat(path)
	if err != nil || !initial.Mode().IsRegular() || initial.Mode()&os.ModeSymlink != 0 {
		return nil, boundary(label + "_FILE_UNAVAILABLE")
	}
	if requirePrivate && !privateFileMode(initial) {
		return nil, boundary(label + "_FILE_NOT_PRIVATE")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, boundary(label + "_FILE_UNAVAILABLE")
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !opened.Mode().IsRegular() || !os.SameFile(initial, opened) || (requirePrivate && !privateFileMode(opened)) {
		return nil, boundary(label + "_FILE_UNAVAILABLE")
	}
	current, err := os.Lstat(path)
	if err != nil || !os.SameFile(initial, current) {
		return nil, boundary(label + "_FILE_CHANGED")
	}
	content, err := io.ReadAll(io.LimitReader(file, int64(maxBytes)+1))
	if err != nil {
		return nil, boundary(label + "_FILE_UNAVAILABLE")
	}
	if len(content) > maxBytes {
		return nil, boundary(label + "_FILE_TOO_LARGE")
	}
	return content, nil
}

func writeNewPrivateFile(path string, content []byte) error {
	if path == "" {
		return boundary("SECRETS_FILE_REQUIRED")
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return boundary("SECRETS_FILE_CREATE_FAILED")
	}
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.Remove(path)
		}
	}()
	if err := file.Chmod(0o600); err != nil {
		_ = file.Close()
		return boundary("SECRETS_FILE_PERMISSION_FAILED")
	}
	if err := writeAndSync(file, content); err != nil {
		_ = file.Close()
		return boundary("SECRETS_FILE_WRITE_FAILED")
	}
	if err := file.Close(); err != nil {
		return boundary("SECRETS_FILE_WRITE_FAILED")
	}
	cleanup = false
	return nil
}

func atomicReplacePrivateFile(path string, content []byte) error {
	current, err := os.Lstat(path)
	if err != nil || !current.Mode().IsRegular() || current.Mode()&os.ModeSymlink != 0 || !privateFileMode(current) {
		return boundary("SECRETS_FILE_UNAVAILABLE")
	}
	directory := filepath.Dir(path)
	temporary, err := os.CreateTemp(directory, ".hap01c-secrets-")
	if err != nil {
		return boundary("SECRETS_FILE_WRITE_FAILED")
	}
	temporaryPath := temporary.Name()
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.Remove(temporaryPath)
		}
	}()
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return boundary("SECRETS_FILE_PERMISSION_FAILED")
	}
	if err := writeAndSync(temporary, content); err != nil {
		_ = temporary.Close()
		return boundary("SECRETS_FILE_WRITE_FAILED")
	}
	if err := temporary.Close(); err != nil {
		return boundary("SECRETS_FILE_WRITE_FAILED")
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return boundary("SECRETS_FILE_REPLACE_FAILED")
	}
	cleanup = false
	return nil
}

func privateFileMode(info os.FileInfo) bool {
	if info == nil {
		return false
	}
	// Windows does not expose POSIX 0600 permission bits through os.FileMode;
	// access control is inherited from the runner-owned user-profile scratch tree.
	if runtime.GOOS == "windows" {
		return true
	}
	return info.Mode().Perm() == 0o600
}

func writeAndSync(file *os.File, content []byte) error {
	written, err := file.Write(content)
	if err != nil || written != len(content) {
		return fmt.Errorf("write failed")
	}
	return file.Sync()
}

func acquireSecretsLock(path string) (func(), error) {
	if path == "" {
		return nil, boundary("SECRETS_FILE_REQUIRED")
	}
	lockPath := path + ".lock"
	file, err := os.OpenFile(lockPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return nil, boundary("SECRETS_FILE_LOCKED")
	}
	if err := file.Chmod(0o600); err != nil {
		_ = file.Close()
		_ = os.Remove(lockPath)
		return nil, boundary("SECRETS_FILE_PERMISSION_FAILED")
	}
	return func() {
		_ = file.Close()
		_ = os.Remove(lockPath)
	}, nil
}

func validNullString(value string) sql.NullString {
	return sql.NullString{String: value, Valid: true}
}

func tokenName(runID string, class keycardClass) string {
	return "hap01c:" + runID + ":" + string(class)
}

func fixtureTag(runID string) string {
	return "hap01c-fixture:" + runID
}

func fixtureSessionPrefix(runID string) string {
	return "hap01c-" + runID + "-"
}

func fixtureSessionID(runID string) string {
	return fixtureSessionPrefix(runID) + "seed"
}

func redactedID(kind, value string) string {
	sum := sha256.Sum256([]byte(kind + "\x00" + value))
	return hex.EncodeToString(sum[:])
}

func escapeLike(value string) string {
	value = strings.ReplaceAll(value, `\`, `\\`)
	value = strings.ReplaceAll(value, `%`, `\%`)
	return strings.ReplaceAll(value, `_`, `\_`)
}

func nonNilContext(ctx context.Context) context.Context {
	if ctx == nil {
		return context.Background()
	}
	return ctx
}
