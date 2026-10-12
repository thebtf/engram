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

// BootstrapLogicalProject is the only normal identity creation roundtrip.
// Existing historical bindings must be migrated, never shadowed by a new UUID.
func BootstrapLogicalProject(ctx context.Context, db *gormlib.DB, input projectidentity.BootstrapRequest) (projectidentity.Project, error) {
	var result projectidentity.Project
	if err := projectidentity.ValidateRepositoryLocator(input.RepositoryLocator); err != nil {
		return result, err
	}
	if db == nil {
		return result, &projectidentity.LogicalError{Code: "PROJECT_RESOLUTION_UNAVAILABLE"}
	}
	err := db.WithContext(ctx).Transaction(func(tx *gormlib.DB) error {
		if err := tx.Exec(`SELECT pg_advisory_xact_lock(hashtextextended(?, 0))`, "logical-project:"+input.RepositoryLocator).Error; err != nil {
			return err
		}
		// Ops publishes proven locator aliases in the existing identifier catalog.
		var aliases []Project
		if err := tx.Raw(`SELECT p.* FROM projects p JOIN project_identifiers i ON i.project_key = p.project_key
			WHERE i.scheme = 'manual_alias' AND i.normalized_value = ? AND i.status IN ('active', 'redirected')`, input.RepositoryLocator).Scan(&aliases).Error; err != nil {
			return err
		}
		candidates := aliases
		if len(candidates) == 0 {
			var repositories []Project
			if err := tx.Where("git_remote IS NOT NULL AND git_remote <> ''").Find(&repositories).Error; err != nil {
				return err
			}
			for _, project := range repositories {
				locator := project.GitRemote.String
				if locator != input.RepositoryLocator {
					normalized, err := projectidentity.NormalizeRepositoryLocator(locator)
					if err != nil || normalized != input.RepositoryLocator {
						continue
					}
				}
				// A historical subdirectory binding is not a repository identity.
				if project.RelativePath.Valid && project.RelativePath.String != "" {
					continue
				}
				candidates = append(candidates, project)
			}
		}
		if len(candidates) > 1 {
			return &projectidentity.LogicalError{Code: "PROJECT_IDENTITY_CONFLICT"}
		}
		if len(candidates) == 1 {
			project := candidates[0]
			if project.RemovedAt != nil || project.IdentityStatus.Valid && project.IdentityStatus.String != "active" {
				return &projectidentity.LogicalError{Code: "PROJECT_IDENTITY_CONFLICT"}
			}
			if !projectidentity.ValidProjectID(project.ID) || project.ProjectKey.Valid && project.ProjectKey.String != project.ID {
				return &projectidentity.LogicalError{Code: "PROJECT_MIGRATION_REQUIRED"}
			}
			result = projectidentity.Project{ProjectID: project.ID, Name: project.DisplayName.String}
			return nil
		}
		id := uuid.NewString()
		result = projectidentity.Project{ProjectID: id, Name: input.Name}
		if err := projectidentity.ValidateProject(result); err != nil {
			return err
		}
		project := Project{
			ID: id, ProjectKey: sql.NullString{String: id, Valid: true}, AnchorProjectID: sql.NullString{String: id, Valid: true},
			GitRemote: sql.NullString{String: input.RepositoryLocator, Valid: true}, DisplayName: sql.NullString{String: input.Name, Valid: input.Name != ""},
			IdentityScope: sql.NullString{String: "repository", Valid: true}, IdentityStatus: sql.NullString{String: "active", Valid: true}, LegacyIDs: pq.StringArray{},
		}
		return tx.Create(&project).Error
	})
	if err != nil {
		var typed *projectidentity.LogicalError
		if errors.As(err, &typed) {
			return projectidentity.Project{}, err
		}
		return projectidentity.Project{}, &projectidentity.LogicalError{Code: "PROJECT_RESOLUTION_UNAVAILABLE"}
	}
	return result, nil
}
