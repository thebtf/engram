package gorm

import (
	"context"
	"database/sql"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"github.com/thebtf/engram/internal/projectidentity"
	gormlib "gorm.io/gorm"
)

type projectIdentityV3ResolverVerifier struct {
	authorized  bool
	adminTarget projectidentity.ProjectKeyV3
	calls       int
	lastRequest projectidentity.AuthorizationVerificationRequestV3
}

func (verifier *projectIdentityV3ResolverVerifier) VerifyAuthorizationV3(_ context.Context, request projectidentity.AuthorizationVerificationRequestV3) (projectidentity.AuthorizationVerificationV3, error) {
	verifier.calls++
	verifier.lastRequest = request
	response := projectidentity.AuthorizationVerificationV3{Authorized: verifier.authorized}
	if request.Intent() == projectidentity.AdminTargetIntentV3 {
		response.AdministrativeTargetProjectKey = verifier.adminTarget
	}
	return response, nil
}

func TestProjectIdentityV3ResolverLookupRejectsUnverifiedCapability(t *testing.T) {
	store, cleanup := openIntegrationTestDB(t)
	t.Cleanup(cleanup)

	_, err := store.LookupAnchorBindingV3(context.Background(), projectidentity.VerifiedAuthorizationV3{}, uuid.NewString())
	require.ErrorIs(t, err, projectidentity.ErrAuthorizationVerificationRequiredV3, "direct adapter lookup must reject an unverified capability")
}

func TestProjectIdentityV3ResolverStoreAuthorizationRedirectAndAudit(t *testing.T) {
	store, cleanup := openIntegrationTestDB(t)
	t.Cleanup(cleanup)
	db := store.GetDB()
	require.NoError(t, projectIdentityV3ResolutionAttemptsMigration163().Migrate(db))
	require.NoError(t, projectIdentityV3ResolutionAttemptAdminAuditMigration164().Migrate(db))
	ctx := context.Background()
	prefix := "t022-resolver-" + uuid.NewString()

	registrationAnchorID := uuid.NewString()
	sourceAnchorID := uuid.NewString()
	targetAnchorID := uuid.NewString()
	sourceProjectKey := uuid.NewString()
	targetProjectKey := uuid.NewString()
	correlationPrefix := prefix + "-correlation-"
	t.Cleanup(func() {
		require.NoError(t, db.Exec(`DELETE FROM project_resolution_attempts WHERE correlation LIKE ?`, correlationPrefix+"%").Error)
		require.NoError(t, db.Exec(`DELETE FROM project_merge_audit_sources WHERE source_project_key = ?`, sourceProjectKey).Error)
		require.NoError(t, db.Exec(`DELETE FROM project_merge_audits WHERE target_project_key = ?`, targetProjectKey).Error)
		require.NoError(t, db.Exec(`DELETE FROM projects WHERE project_key IN (?, ?, ?)`, sourceProjectKey, targetProjectKey).Error)
	})

	err := store.RegisterAnchorBindingAndRecordAttemptV3(ctx, projectidentity.AnchorRegistrationV3{}, nil)
	require.ErrorIs(t, err, projectidentity.ErrAuthorizationVerificationRequiredV3, "direct adapter registration must reject a missing capability")
	_, err = store.LookupAdministrativeTargetV3(ctx, projectidentity.VerifiedAuthorizationV3{})
	require.ErrorIs(t, err, projectidentity.ErrAuthorizationVerificationRequiredV3, "direct adapter admin lookup must reject an unverified capability")

	registrationRequest := resolverStoreRequest(t, projectidentity.RegisterAnchorIntentV3, registrationAnchorID, correlationPrefix+"registration")
	registrationAuthorization, err := projectidentity.NewAuthorizationReferenceV3("authorization-" + uuid.NewString())
	require.NoError(t, err)
	registrationRequest.RegistrationAuthorization = registrationAuthorization
	verifier := &projectIdentityV3ResolverVerifier{authorized: true}
	resolver := projectidentity.NewResolverV3(store, verifier)
	first, err := resolver.ResolveProjectV3(ctx, registrationRequest)
	require.NoError(t, err)
	second, err := resolver.ResolveProjectV3(ctx, registrationRequest)
	require.NoError(t, err)
	require.Equal(t, projectidentity.ProjectResolvedOutcomeV3, first.Resolution().Outcome())
	require.Equal(t, first.Resolution().CanonicalProjectKey(), second.Resolution().CanonicalProjectKey(), "binding must be idempotent")
	var registrationBindings int64
	require.NoError(t, db.Model(&Project{}).Where("anchor_project_id = ?", registrationAnchorID).Count(&registrationBindings).Error)
	require.EqualValues(t, 1, registrationBindings, "transactional binding must create one row")

	completedAt := time.Now().UTC()
	require.NoError(t, db.Create(&Project{
		ID:              prefix + "-source",
		ProjectKey:      nullString(sourceProjectKey),
		AnchorProjectID: nullString(sourceAnchorID),
		IdentityScope:   nullString("repository"),
		IdentityStatus:  nullString(mergedProjectIdentityStatusV3),
	}).Error)
	require.NoError(t, db.Create(&Project{
		ID:              prefix + "-target",
		ProjectKey:      nullString(targetProjectKey),
		AnchorProjectID: nullString(targetAnchorID),
		IdentityScope:   nullString("repository"),
		IdentityStatus:  nullString(activeProjectIdentityStatusV3),
	}).Error)
	mergeAudit := ProjectMergeAudit{
		TargetProjectKey:  targetProjectKey,
		EvidenceClass:     "t022-test",
		ConflictPolicy:    "hold",
		BeforeTableCounts: `{}`,
		AfterTableCounts:  `{}`,
		Fingerprints:      `{}`,
		PrivacyResult:     "preserved",
		Actor:             "test",
		CompletedAt:       &completedAt,
		RollbackBoundary:  "test-boundary",
	}
	require.NoError(t, db.Create(&mergeAudit).Error)
	require.NoError(t, db.Create(&ProjectMergeAuditSource{MergeID: mergeAudit.MergeID, SourceProjectKey: sourceProjectKey}).Error)

	redirectRequest := resolverStoreRequest(t, projectidentity.ResolveExistingIntentV3, sourceAnchorID, correlationPrefix+"redirect")
	redirected, err := resolver.ResolveProjectV3(ctx, redirectRequest)
	require.NoError(t, err)
	require.Equal(t, projectidentity.ProjectRedirectedOutcomeV3, redirected.Resolution().Outcome())
	require.Equal(t, projectidentity.ProjectKeyV3(targetProjectKey), redirected.Resolution().CanonicalProjectKey())
	require.Equal(t, projectidentity.RedirectReferenceV3(mergeAudit.MergeID), redirected.Resolution().RedirectReference())

	adminRequest := resolverStoreRequest(t, projectidentity.AdminTargetIntentV3, targetAnchorID, correlationPrefix+"admin")
	audit, err := projectidentity.NewAdminAuditV3("actor-"+uuid.NewString(), "purpose-"+uuid.NewString(), "decision-"+uuid.NewString(), "retain-"+uuid.NewString())
	require.NoError(t, err)
	opaqueTarget, err := projectidentity.NewAdministrativeTargetReferenceV3("opaque-target-" + uuid.NewString())
	require.NoError(t, err)
	adminAuthorization, err := projectidentity.NewAuthorizationReferenceV3("admin-authorization-" + uuid.NewString())
	require.NoError(t, err)
	adminRequirement, err := projectidentity.NewAdminTargetRequirementV3(adminAuthorization, adminRequest.Correlation, opaqueTarget, audit)
	require.NoError(t, err)
	adminRequest.AdminTarget = &adminRequirement
	serverTarget, err := projectidentity.NewProjectKeyV3(targetProjectKey)
	require.NoError(t, err)
	verifier.adminTarget = serverTarget
	adminResult, err := resolver.ResolveProjectV3(ctx, adminRequest)
	require.NoError(t, err)
	require.Equal(t, projectidentity.ProjectResolvedOutcomeV3, adminResult.Resolution().Outcome())
	require.Equal(t, serverTarget, adminResult.Resolution().CanonicalProjectKey())
	require.Equal(t, opaqueTarget, verifier.lastRequest.AdministrativeTarget(), "verifier must receive the opaque target reference")
	require.Equal(t, audit.Actor(), verifier.lastRequest.Audit().Actor())
	require.Equal(t, audit.Purpose(), verifier.lastRequest.Audit().Purpose())
	require.Equal(t, audit.Decision(), verifier.lastRequest.Audit().Decision())
	require.Equal(t, audit.RetentionOrRollback(), verifier.lastRequest.Audit().RetentionOrRollback())

	var attempts []ProjectResolutionAttempt
	require.NoError(t, db.Where("correlation LIKE ?", correlationPrefix+"%").Order("created_at ASC").Find(&attempts).Error)
	require.Len(t, attempts, 4, "two idempotent registrations, one redirect, and one admin target resolution must audit")
	for _, attempt := range attempts {
		require.Equal(t, "anchor_v3", attempt.Provenance)
		require.NotEmpty(t, attempt.Correlation)
		require.NotEmpty(t, attempt.Intent)
		require.NotEmpty(t, attempt.Outcome)
		require.Equal(t, 3, attempt.DescriptorVersion)
	}
	var adminAttempt struct {
		AdminTargetReference     string `gorm:"column:admin_target_reference"`
		AdminActor               string `gorm:"column:admin_actor"`
		AdminPurpose             string `gorm:"column:admin_purpose"`
		AdminDecision            string `gorm:"column:admin_decision"`
		AdminRetentionOrRollback string `gorm:"column:admin_retention_or_rollback"`
	}
	require.NoError(t, db.Raw(`
		SELECT admin_target_reference, admin_actor, admin_purpose, admin_decision, admin_retention_or_rollback
		FROM project_resolution_attempts
		WHERE correlation = ?`, adminRequest.Correlation).Scan(&adminAttempt).Error)
	for _, value := range []struct {
		persisted string
		raw       string
	}{
		{adminAttempt.AdminTargetReference, string(opaqueTarget)},
		{adminAttempt.AdminActor, audit.Actor()},
		{adminAttempt.AdminPurpose, audit.Purpose()},
		{adminAttempt.AdminDecision, audit.Decision()},
		{adminAttempt.AdminRetentionOrRollback, audit.RetentionOrRollback()},
	} {
		require.NotEmpty(t, value.persisted)
		require.NotEqual(t, value.raw, value.persisted)
		require.NotContains(t, value.persisted, value.raw)
	}
	var sensitiveColumns int
	require.NoError(t, db.Raw(`
		SELECT COUNT(*) FROM information_schema.columns
		WHERE table_schema = 'public' AND table_name = 'project_resolution_attempts'
		  AND column_name IN ('descriptor', 'authorization', 'target_reference', 'raw_payload', 'path', 'credential', 'project_key')
	`).Scan(&sensitiveColumns).Error)
	require.Zero(t, sensitiveColumns, "resolution-attempt storage must not retain raw/private inputs")
}

func TestProjectIdentityV3ResolverReadFilterRejectsUnverifiedBeforeLookup(t *testing.T) {
	store, cleanup := openIntegrationTestDB(t)
	t.Cleanup(cleanup)
	db := store.GetDB()
	require.NoError(t, projectIdentityV3ResolutionAttemptsMigration163().Migrate(db))
	require.NoError(t, projectIdentityV3ResolutionAttemptAdminAuditMigration164().Migrate(db))
	ctx := context.Background()
	anchorProjectID := uuid.NewString()
	correlationValue := "t022-read-filter-" + uuid.NewString()
	callbackName := "t022_read_filter_lookup_" + uuid.NewString()[:8]
	var anchorLookupCalls int
	require.NoError(t, db.Callback().Query().Before("gorm:query").Register(callbackName, func(tx *gormlib.DB) {
		if tx.Statement.Table == "projects" {
			anchorLookupCalls++
		}
	}))
	t.Cleanup(func() { _ = db.Callback().Query().Remove(callbackName) })

	request := resolverStoreRequest(t, projectidentity.ReadFilterIntentV3, anchorProjectID, correlationValue)
	authorization, err := projectidentity.NewAuthorizationReferenceV3("read-filter-authorization-" + uuid.NewString())
	require.NoError(t, err)
	readFilter, err := projectidentity.NewReadFilterRequirementV3(authorization, request.Correlation)
	require.NoError(t, err)
	request.ReadFilter = &readFilter
	verifier := &projectIdentityV3ResolverVerifier{}

	result, err := projectidentity.NewResolverV3(store, verifier).ResolveProjectV3(ctx, request)
	require.Error(t, err)
	require.Equal(t, projectidentity.ProjectDescriptorInvalidOutcomeV3, result.Resolution().Outcome())
	require.Empty(t, result.Resolution().CanonicalProjectKey())
	require.Equal(t, 1, verifier.calls, "read_filter must ask the server verifier")
	require.Zero(t, anchorLookupCalls, "unverified read_filter must not look up a binding")
	require.NoError(t, db.Callback().Query().Remove(callbackName))

	var bindingCount, attemptCount int64
	require.NoError(t, db.Model(&Project{}).Where("anchor_project_id = ?", anchorProjectID).Count(&bindingCount).Error)
	require.NoError(t, db.Model(&ProjectResolutionAttempt{}).Where("correlation = ?", correlationValue).Count(&attemptCount).Error)
	require.Zero(t, bindingCount, "unverified read_filter must not mutate a binding")
	require.EqualValues(t, 1, attemptCount, "unverified read_filter refusal must remain auditable")
}

func TestProjectIdentityV3ResolverResolveExistingRejectsUnverifiedBeforeLookup(t *testing.T) {
	store, cleanup := openIntegrationTestDB(t)
	t.Cleanup(cleanup)
	db := store.GetDB()
	require.NoError(t, projectIdentityV3ResolutionAttemptsMigration163().Migrate(db))
	require.NoError(t, projectIdentityV3ResolutionAttemptAdminAuditMigration164().Migrate(db))
	ctx := context.Background()
	correlationValue := "t022-resolve-existing-" + uuid.NewString()
	callbackName := "t022_resolve_existing_lookup_" + uuid.NewString()[:8]
	var anchorLookupCalls int
	require.NoError(t, db.Callback().Query().Before("gorm:query").Register(callbackName, func(tx *gormlib.DB) {
		if tx.Statement.Table == "projects" {
			anchorLookupCalls++
		}
	}))
	t.Cleanup(func() { _ = db.Callback().Query().Remove(callbackName) })
	t.Cleanup(func() {
		_ = db.Exec("DELETE FROM project_resolution_attempts WHERE correlation = ?", correlationValue).Error
	})

	request := resolverStoreRequest(t, projectidentity.ResolveExistingIntentV3, uuid.NewString(), correlationValue)
	verifier := &projectIdentityV3ResolverVerifier{}
	result, err := projectidentity.NewResolverV3(store, verifier).ResolveProjectV3(ctx, request)
	require.Error(t, err)
	require.Equal(t, projectidentity.ProjectDescriptorInvalidOutcomeV3, result.Resolution().Outcome())
	require.Empty(t, result.Resolution().CanonicalProjectKey())
	require.Equal(t, 1, verifier.calls, "resolve_existing must ask the server verifier")
	require.Zero(t, anchorLookupCalls, "unverified resolve_existing must not look up a binding")

	var attemptCount int64
	require.NoError(t, db.Model(&ProjectResolutionAttempt{}).Where("correlation = ?", correlationValue).Count(&attemptCount).Error)
	require.EqualValues(t, 1, attemptCount, "unverified resolve_existing refusal must remain auditable")
}

func TestProjectIdentityV3ResolverRegistrationRollsBackWhenAttemptWriteFails(t *testing.T) {
	store, cleanup := openIntegrationTestDB(t)
	t.Cleanup(cleanup)
	db := store.GetDB()
	require.NoError(t, projectIdentityV3ResolutionAttemptsMigration163().Migrate(db))
	require.NoError(t, projectIdentityV3ResolutionAttemptAdminAuditMigration164().Migrate(db))
	ctx := context.Background()
	anchorProjectID := uuid.NewString()
	correlationValue := "t022-attempt-failure-" + uuid.NewString()
	constraintName := "t022_attempt_fail_" + uuid.NewString()[:8]
	require.NoError(t, db.Exec(fmt.Sprintf(
		"ALTER TABLE project_resolution_attempts ADD CONSTRAINT %s CHECK (correlation <> '%s')",
		constraintName,
		correlationValue,
	)).Error)
	t.Cleanup(func() {
		_ = db.Exec("ALTER TABLE project_resolution_attempts DROP CONSTRAINT IF EXISTS " + constraintName).Error
		_ = db.Exec("DELETE FROM project_resolution_attempts WHERE correlation = ?", correlationValue).Error
		_ = db.Exec("DELETE FROM projects WHERE anchor_project_id = ?", anchorProjectID).Error
	})

	request := resolverStoreRequest(t, projectidentity.RegisterAnchorIntentV3, anchorProjectID, correlationValue)
	authorization, err := projectidentity.NewAuthorizationReferenceV3("registration-authorization-" + uuid.NewString())
	require.NoError(t, err)
	request.RegistrationAuthorization = authorization

	result, err := projectidentity.NewResolverV3(store, &projectIdentityV3ResolverVerifier{authorized: true}).ResolveProjectV3(ctx, request)
	require.Error(t, err)
	require.Empty(t, result.Resolution().CanonicalProjectKey())

	var bindingCount, attemptCount int64
	require.NoError(t, db.Model(&Project{}).Where("anchor_project_id = ?", anchorProjectID).Count(&bindingCount).Error)
	require.NoError(t, db.Model(&ProjectResolutionAttempt{}).Where("correlation = ?", correlationValue).Count(&attemptCount).Error)
	require.Zero(t, bindingCount, "failed attempt write must roll back the binding")
	require.Zero(t, attemptCount, "failed attempt write must commit no audit row")
}

func resolverStoreRequest(t *testing.T, intent projectidentity.ResolutionIntentV3, anchorProjectID, correlationValue string) projectidentity.ResolveProjectRequestV3 {
	t.Helper()
	anchor := projectidentity.AnchorV3{Version: 3, ProjectID: anchorProjectID, Name: "t022-resolver", Scope: "repository"}
	descriptor, err := projectidentity.BuildDescriptorV3(anchor, nil, nil, "client-"+uuid.NewString())
	correlation, err := projectidentity.NewCorrelationV3(correlationValue)
	require.NoError(t, err)
	request := projectidentity.ResolveProjectRequestV3{Intent: intent, Anchor: anchor, Descriptor: descriptor, Correlation: correlation}
	if intent == projectidentity.ResolveExistingIntentV3 {
		authorization, err := projectidentity.NewAuthorizationReferenceV3("resolve-existing-authorization-" + uuid.NewString())
		require.NoError(t, err)
		request.ResolveExistingAuthorization = authorization
	}
	return request
}

func nullString(value string) sql.NullString {
	return sql.NullString{String: value, Valid: true}
}

func TestProjectIdentityV3ResolverRedactsAdministrativeAuditAtRest(t *testing.T) {
	store, cleanup := openIntegrationTestDB(t)
	t.Cleanup(cleanup)
	db := store.GetDB()
	require.NoError(t, projectIdentityV3ResolutionAttemptsMigration163().Migrate(db))
	require.NoError(t, projectIdentityV3ResolutionAttemptAdminAuditMigration164().Migrate(db))
	ctx := context.Background()
	correlationValue := "t022-admin-redaction-" + uuid.NewString()
	anchorProjectID := uuid.NewString()
	projectKey := uuid.NewString()
	t.Cleanup(func() {
		_ = db.Exec("DELETE FROM project_resolution_attempts WHERE correlation = ?", correlationValue).Error
		_ = db.Exec("DELETE FROM projects WHERE project_key = ?", projectKey).Error
	})
	require.NoError(t, db.Create(&Project{
		ID:              "t022-admin-redaction-" + uuid.NewString(),
		ProjectKey:      nullString(projectKey),
		AnchorProjectID: nullString(anchorProjectID),
		IdentityScope:   nullString("repository"),
		IdentityStatus:  nullString(activeProjectIdentityStatusV3),
	}).Error)

	request := resolverStoreRequest(t, projectidentity.AdminTargetIntentV3, anchorProjectID, correlationValue)
	rawTarget := "target-secret-" + uuid.NewString()
	rawActor := "actor-secret-" + uuid.NewString()
	rawPurpose := "purpose-secret-" + uuid.NewString()
	rawDecision := "decision-secret-" + uuid.NewString()
	rawRetention := "retention-secret-" + uuid.NewString()
	target, err := projectidentity.NewAdministrativeTargetReferenceV3(rawTarget)
	require.NoError(t, err)
	audit, err := projectidentity.NewAdminAuditV3(rawActor, rawPurpose, rawDecision, rawRetention)
	require.NoError(t, err)
	authorization, err := projectidentity.NewAuthorizationReferenceV3("admin-authorization-" + uuid.NewString())
	require.NoError(t, err)
	requirement, err := projectidentity.NewAdminTargetRequirementV3(authorization, request.Correlation, target, audit)
	require.NoError(t, err)
	request.AdminTarget = &requirement
	verifiedProjectKey, err := projectidentity.NewProjectKeyV3(projectKey)
	require.NoError(t, err)

	result, err := projectidentity.NewResolverV3(store, &projectIdentityV3ResolverVerifier{authorized: true, adminTarget: verifiedProjectKey}).ResolveProjectV3(ctx, request)
	require.NoError(t, err)
	require.Equal(t, projectidentity.ProjectResolvedOutcomeV3, result.Resolution().Outcome())
	var attempt ProjectResolutionAttempt
	require.NoError(t, db.Where("correlation = ?", correlationValue).First(&attempt).Error)
	require.Equal(t, correlationValue, attempt.Correlation)
	require.Equal(t, string(projectidentity.AdminTargetIntentV3), attempt.Intent)
	require.Equal(t, string(projectidentity.ProjectResolvedOutcomeV3), attempt.Outcome)
	for _, value := range []struct {
		persisted sql.NullString
		raw       string
	}{
		{attempt.AdminTargetReference, rawTarget},
		{attempt.AdminActor, rawActor},
		{attempt.AdminPurpose, rawPurpose},
		{attempt.AdminDecision, rawDecision},
		{attempt.AdminRetentionOrRollback, rawRetention},
	} {
		require.True(t, value.persisted.Valid)
		require.NotEmpty(t, value.persisted.String)
		require.NotEqual(t, value.raw, value.persisted.String)
		require.NotContains(t, value.persisted.String, value.raw)
	}
}
