package gorm

import (
	"context"
	"database/sql"
	"errors"

	"github.com/google/uuid"
	"github.com/lib/pq"
	"github.com/thebtf/engram/internal/projectidentity"
	gormlib "gorm.io/gorm"
)

const (
	activeProjectIdentityStatusV3 = "active"
	mergedProjectIdentityStatusV3 = "merged"
)

var (
	errProjectIdentityV3StoreUnavailable = errors.New("V3 project identity store is unavailable")
	errProjectIdentityV3InvalidAttempt   = errors.New("invalid V3 project resolution attempt")
)

// LookupAnchorBindingV3 reads only the V3 anchor binding. It never falls back
// to V2 IDs, remotes, names, paths, identifiers, caches, or scoped records.
func (store *Store) LookupAnchorBindingV3(ctx context.Context, anchorProjectID string) (projectidentity.AnchorBindingV3, error) {
	if store == nil || store.DB == nil {
		return projectidentity.AnchorBindingV3{}, errProjectIdentityV3StoreUnavailable
	}
	return lookupAnchorBindingV3(ctx, store.DB, anchorProjectID)
}

// RegisterAnchorBindingV3 serializes the sole authorized V3 binding mutation.
// It is idempotent for the same active anchor and rejects any command without
// the resolver-created server verification capability.
func (store *Store) RegisterAnchorBindingV3(ctx context.Context, registration projectidentity.AnchorRegistrationV3) (projectidentity.AnchorBindingV3, error) {
	if store == nil || store.DB == nil {
		return projectidentity.AnchorBindingV3{}, errProjectIdentityV3StoreUnavailable
	}
	if !registration.IsCausallyBoundFirstMutation() {
		return projectidentity.AnchorBindingV3{}, projectidentity.ErrAuthorizationVerificationRequiredV3
	}

	var binding projectidentity.AnchorBindingV3
	err := store.DB.WithContext(ctx).Transaction(func(tx *gormlib.DB) error {
		if err := tx.Exec(`SELECT pg_advisory_xact_lock(hashtextextended(?, 0))`, "v3-anchor:"+registration.AnchorProjectID()).Error; err != nil {
			return err
		}

		current, err := lookupAnchorBindingV3(ctx, tx, registration.AnchorProjectID())
		if err != nil {
			return err
		}
		switch current.State {
		case projectidentity.AnchorBindingActiveV3:
			if current.Scope != string(registration.Scope()) {
				binding = projectidentity.AnchorBindingV3{State: projectidentity.AnchorBindingDecisionRequiredV3}
				return nil
			}
			binding = current
			return nil
		case projectidentity.AnchorBindingDecisionRequiredV3, projectidentity.AnchorBindingRedirectedV3:
			binding = current
			return nil
		case projectidentity.AnchorBindingMissingV3:
			projectKey := uuid.NewString()
			project := Project{
				ID:              projectKey,
				ProjectKey:      sql.NullString{String: projectKey, Valid: true},
				AnchorProjectID: sql.NullString{String: registration.AnchorProjectID(), Valid: true},
				IdentityScope:   sql.NullString{String: string(registration.Scope()), Valid: true},
				IdentityStatus:  sql.NullString{String: activeProjectIdentityStatusV3, Valid: true},
				LegacyIDs:       pq.StringArray{},
			}
			if err := tx.WithContext(ctx).Create(&project).Error; err != nil {
				return err
			}
			binding = projectidentity.AnchorBindingV3{
				State:      projectidentity.AnchorBindingActiveV3,
				ProjectKey: projectKey,
				Scope:      string(registration.Scope()),
			}
			return nil
		default:
			binding = projectidentity.AnchorBindingV3{State: projectidentity.AnchorBindingDecisionRequiredV3}
			return nil
		}
	})
	if err != nil {
		return projectidentity.AnchorBindingV3{}, err
	}
	return binding, nil
}

// LookupAdministrativeTargetV3 accepts only a verified server capability. The
// opaque caller target reference is never used as a project_key selector.
func (store *Store) LookupAdministrativeTargetV3(ctx context.Context, authorization projectidentity.VerifiedAuthorizationV3) (projectidentity.AnchorBindingV3, error) {
	if store == nil || store.DB == nil {
		return projectidentity.AnchorBindingV3{}, errProjectIdentityV3StoreUnavailable
	}
	projectKey, ok := authorization.AdministrativeTargetProjectKey()
	if !ok {
		return projectidentity.AnchorBindingV3{}, projectidentity.ErrAuthorizationVerificationRequiredV3
	}
	var projects []Project
	if err := store.DB.WithContext(ctx).Where("project_key = ?", string(projectKey)).Order("id ASC").Find(&projects).Error; err != nil {
		return projectidentity.AnchorBindingV3{}, err
	}
	return anchorBindingFromProjectsV3(projects), nil
}

// RecordResolutionAttemptV3 persists only contract-approved redacted fields.
func (store *Store) RecordResolutionAttemptV3(ctx context.Context, attempt projectidentity.ResolutionAttemptV3) error {
	if store == nil || store.DB == nil {
		return errProjectIdentityV3StoreUnavailable
	}
	if !attempt.Valid() {
		return errProjectIdentityV3InvalidAttempt
	}
	record := ProjectResolutionAttempt{
		AttemptID:         uuid.NewString(),
		Correlation:       string(attempt.Correlation()),
		Intent:            string(attempt.Intent()),
		Outcome:           string(attempt.Outcome()),
		DescriptorVersion: attempt.DescriptorVersion(),
		Provenance:        attempt.Provenance(),
	}
	if anchorProjectID := attempt.AnchorProjectID(); anchorProjectID != "" {
		record.AnchorProjectID = sql.NullString{String: anchorProjectID, Valid: true}
	}
	if redirect := attempt.RedirectReference(); redirect != "" {
		record.RedirectReference = sql.NullString{String: string(redirect), Valid: true}
	}
	return store.DB.WithContext(ctx).Create(&record).Error
}

func lookupAnchorBindingV3(ctx context.Context, db *gormlib.DB, anchorProjectID string) (projectidentity.AnchorBindingV3, error) {
	var projects []Project
	if err := db.WithContext(ctx).Where("anchor_project_id = ?", anchorProjectID).Order("id ASC").Find(&projects).Error; err != nil {
		return projectidentity.AnchorBindingV3{}, err
	}
	if len(projects) != 1 {
		return anchorBindingFromProjectsV3(projects), nil
	}
	project := projects[0]
	if project.IdentityStatus.Valid && project.IdentityStatus.String == mergedProjectIdentityStatusV3 {
		return lookupMergedAnchorBindingV3(ctx, db, project)
	}
	return anchorBindingFromProjectsV3(projects), nil
}

func lookupMergedAnchorBindingV3(ctx context.Context, db *gormlib.DB, source Project) (projectidentity.AnchorBindingV3, error) {
	if !source.ProjectKey.Valid {
		return projectidentity.AnchorBindingV3{State: projectidentity.AnchorBindingDecisionRequiredV3}, nil
	}
	type redirectTarget struct {
		ProjectKey        string `gorm:"column:project_key"`
		Scope             string `gorm:"column:scope"`
		RedirectReference string `gorm:"column:redirect_reference"`
	}
	var targets []redirectTarget
	err := db.WithContext(ctx).Raw(`
		SELECT target.project_key, target.identity_scope AS scope, audit.merge_id AS redirect_reference
		FROM project_merge_audit_sources AS source
		JOIN project_merge_audits AS audit ON audit.merge_id = source.merge_id
		JOIN projects AS target ON target.project_key = audit.target_project_key
		WHERE source.source_project_key = ?
		  AND audit.completed_at IS NOT NULL
		  AND target.identity_status = ?
		  AND target.project_key IS NOT NULL
		  AND target.identity_scope IS NOT NULL
		ORDER BY audit.merge_id ASC`, source.ProjectKey.String, activeProjectIdentityStatusV3).Scan(&targets).Error
	if err != nil {
		return projectidentity.AnchorBindingV3{}, err
	}
	if len(targets) != 1 {
		return projectidentity.AnchorBindingV3{State: projectidentity.AnchorBindingDecisionRequiredV3}, nil
	}
	target := targets[0]
	return projectidentity.AnchorBindingV3{
		State:             projectidentity.AnchorBindingRedirectedV3,
		ProjectKey:        target.ProjectKey,
		Scope:             target.Scope,
		RedirectReference: target.RedirectReference,
	}, nil
}

func anchorBindingFromProjectsV3(projects []Project) projectidentity.AnchorBindingV3 {
	if len(projects) == 0 {
		return projectidentity.AnchorBindingV3{State: projectidentity.AnchorBindingMissingV3}
	}
	if len(projects) != 1 {
		return projectidentity.AnchorBindingV3{State: projectidentity.AnchorBindingDecisionRequiredV3}
	}
	project := projects[0]
	if !project.IdentityStatus.Valid || project.IdentityStatus.String != activeProjectIdentityStatusV3 || !project.ProjectKey.Valid || !project.IdentityScope.Valid {
		return projectidentity.AnchorBindingV3{State: projectidentity.AnchorBindingDecisionRequiredV3}
	}
	return projectidentity.AnchorBindingV3{
		State:      projectidentity.AnchorBindingActiveV3,
		ProjectKey: project.ProjectKey.String,
		Scope:      project.IdentityScope.String,
	}
}
