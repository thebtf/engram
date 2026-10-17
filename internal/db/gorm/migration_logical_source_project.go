package gorm

import (
	"fmt"

	"github.com/go-gormigrate/gormigrate/v2"
	"github.com/thebtf/engram/internal/projectidentity"
	gormlib "gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// Ops migration 186 retains historical membership before this additive association.
func logicalSourceProjectMigration187() *gormigrate.Migration {
	return &gormigrate.Migration{
		ID: "187_logical_source_project",
		Migrate: func(tx *gormlib.DB) error {
			for _, statement := range []string{
				`ALTER TABLE sources ADD COLUMN IF NOT EXISTS project_id TEXT`,
				`DO $$ BEGIN
				 IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conrelid='sources'::regclass AND conname='sources_logical_project_fk') THEN
				  ALTER TABLE sources ADD CONSTRAINT sources_logical_project_fk FOREIGN KEY(project_id) REFERENCES projects(id) ON DELETE RESTRICT;
				 END IF;
				 IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conrelid='sources'::regclass AND conname='sources_logical_project_uuid') THEN
				  ALTER TABLE sources ADD CONSTRAINT sources_logical_project_uuid CHECK(project_id IS NULL OR
				   (project_id ~ '^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$' AND project_id <> '00000000-0000-0000-0000-000000000000'));
				 END IF;
				END $$`,
				`UPDATE sources s SET project_id = membership.project_id
				 FROM (
				  SELECT m.row_key->>'source_id' AS source_id, MIN(m.project_id) AS project_id
				  FROM project_consolidation_memberships m
				  WHERE m.table_name = 'sources'
				  GROUP BY m.row_key->>'source_id'
				  HAVING COUNT(DISTINCT m.project_id) = 1 AND BOOL_AND(m.project_id IS NOT NULL
				   AND EXISTS (SELECT 1 FROM projects p WHERE p.id = m.project_id AND p.removed_at IS NULL
				    AND (p.identity_status IS NULL OR p.identity_status = 'active')))
				 ) membership
				 WHERE s.source_id::text = membership.source_id AND s.project_id IS NULL`,
				`CREATE INDEX IF NOT EXISTS idx_sources_logical_project ON sources(project_id) WHERE project_id IS NOT NULL`,
			} {
				if err := tx.Exec(statement).Error; err != nil {
					return fmt.Errorf("migration 187: %w", err)
				}
			}
			return nil
		},
		Rollback: func(*gormlib.DB) error { return fmt.Errorf("logical Source project membership must be retained") },
	}
}

func requireRegisteredSourceProject(tx *gormlib.DB, sourceID, projectID string) error {
	var source UCISource
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("source_id = ?", sourceID).Take(&source).Error; err != nil {
		return err
	}
	if source.ProjectID == nil || *source.ProjectID != projectID {
		return &projectidentity.LogicalError{Code: "PROJECT_IDENTITY_CONFLICT"}
	}
	return nil
}
