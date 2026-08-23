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

const activeProjectIdentityStatusV3 = "active"

var errProjectIdentityV3StoreUnavailable = errors.New("V3 project identity store is unavailable")

// LookupAnchorBindingV3 reads only the V3 anchor binding. It never falls back
// to V2 IDs, remotes, names, paths, identifiers, caches, or scoped records.
func (store *Store) LookupAnchorBindingV3(ctx context.Context, anchorProjectID string) (projectidentity.AnchorBindingV3, error) {
	if store == nil || store.DB == nil {
		return projectidentity.AnchorBindingV3{}, errProjectIdentityV3StoreUnavailable
	}
	return lookupAnchorBindingV3(ctx, store.DB, anchorProjectID)
}

// RegisterAnchorBindingV3 serializes the sole authorized V3 binding mutation.
// It is idempotent for the same active anchor and turns every competing or
// incomplete binding into a resolver-owned decision hold.
func (store *Store) RegisterAnchorBindingV3(ctx context.Context, registration projectidentity.AnchorRegistrationV3) (projectidentity.AnchorBindingV3, error) {
	if store == nil || store.DB == nil {
		return projectidentity.AnchorBindingV3{}, errProjectIdentityV3StoreUnavailable
	}
	if !registration.IsCausallyBoundFirstMutation() {
		return projectidentity.AnchorBindingV3{State: projectidentity.AnchorBindingDecisionRequiredV3}, nil
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
		case projectidentity.AnchorBindingDecisionRequiredV3:
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

// LookupAdministrativeTargetV3 uses only the explicit server-issued
// administrative target after the resolver has verified authorization and its
// complete audit requirement. It does not accept a general selector.
func (store *Store) LookupAdministrativeTargetV3(ctx context.Context, target projectidentity.AdministrativeTargetReferenceV3) (projectidentity.AnchorBindingV3, error) {
	if store == nil || store.DB == nil {
		return projectidentity.AnchorBindingV3{}, errProjectIdentityV3StoreUnavailable
	}
	var projects []Project
	if err := store.DB.WithContext(ctx).Where("project_key = ?", string(target)).Order("id ASC").Find(&projects).Error; err != nil {
		return projectidentity.AnchorBindingV3{}, err
	}
	return anchorBindingFromProjectsV3(projects), nil
}

func lookupAnchorBindingV3(ctx context.Context, db *gormlib.DB, anchorProjectID string) (projectidentity.AnchorBindingV3, error) {
	var projects []Project
	if err := db.WithContext(ctx).Where("anchor_project_id = ?", anchorProjectID).Order("id ASC").Find(&projects).Error; err != nil {
		return projectidentity.AnchorBindingV3{}, err
	}
	return anchorBindingFromProjectsV3(projects), nil
}

func anchorBindingFromProjectsV3(projects []Project) projectidentity.AnchorBindingV3 {
	if len(projects) == 0 {
		return projectidentity.AnchorBindingV3{State: projectidentity.AnchorBindingMissingV3}
	}
	if len(projects) != 1 {
		return projectidentity.AnchorBindingV3{State: projectidentity.AnchorBindingDecisionRequiredV3}
	}
	project := projects[0]
	if project.IdentityStatus.String != activeProjectIdentityStatusV3 || !project.ProjectKey.Valid || !project.IdentityScope.Valid {
		return projectidentity.AnchorBindingV3{State: projectidentity.AnchorBindingDecisionRequiredV3}
	}
	return projectidentity.AnchorBindingV3{
		State:      projectidentity.AnchorBindingActiveV3,
		ProjectKey: project.ProjectKey.String,
		Scope:      project.IdentityScope.String,
	}
}
