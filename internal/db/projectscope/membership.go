package projectscope

import "github.com/thebtf/engram/internal/projectidentity"

// MembershipPredicate is shared by the canonical content and issue readers.
// Alias and field are fixed, trusted SQL identifiers supplied by the caller.
func MembershipPredicate(alias, field string) string {
	return `((` + alias + `.` + field + ` = ? AND NOT EXISTS (SELECT 1 FROM project_consolidation_memberships membership
 WHERE membership.table_name = ? AND membership.field_path = '["` + field + `"]' AND membership.row_key->>'id' = CAST(` + alias + `.id AS text))) OR EXISTS
 (SELECT 1 FROM project_consolidation_memberships membership WHERE membership.table_name = ? AND membership.field_path = '["` + field + `"]'
 AND membership.row_key->>'id' = CAST(` + alias + `.id AS text) AND membership.project_id = ?
 AND NOT EXISTS (SELECT 1 FROM project_consolidation_memberships conflict
 WHERE conflict.table_name = membership.table_name AND conflict.row_key = membership.row_key AND conflict.field_path = membership.field_path
 AND (conflict.project_id IS NULL OR conflict.project_id <> membership.project_id))))`
}

// MemoryPredicate requires a live canonical project before membership or
// native UUID fallback can authorize any row.
func MemoryPredicate(alias, project string) (string, []any) {
	if !projectidentity.ValidProjectID(project) {
		return "FALSE", nil
	}
	return `(EXISTS (SELECT 1 FROM projects selected_project WHERE selected_project.id = ? AND selected_project.removed_at IS NULL AND (selected_project.identity_status IS NULL OR selected_project.identity_status = 'active')) AND ` + MembershipPredicate(alias, "project") + `)`, []any{project, project, "memories", "memories", project}
}
