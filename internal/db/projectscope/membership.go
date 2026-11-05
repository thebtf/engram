package projectscope

import "github.com/thebtf/engram/internal/projectidentity"

// NativeMembershipPredicate retains the original unique-project mutation scope.
func NativeMembershipPredicate(alias, field string) string {
	return `((` + alias + `.` + field + ` = ? AND NOT EXISTS (SELECT 1 FROM project_consolidation_memberships membership
 WHERE membership.table_name = ? AND membership.field_path = '["` + field + `"]' AND membership.row_key->>'id' = CAST(` + alias + `.id AS text))) OR EXISTS
 (SELECT 1 FROM project_consolidation_memberships membership WHERE membership.table_name = ? AND membership.field_path = '["` + field + `"]'
 AND membership.row_key->>'id' = CAST(` + alias + `.id AS text) AND membership.project_id = ?
 AND NOT EXISTS (SELECT 1 FROM project_consolidation_memberships conflict
 WHERE conflict.table_name = membership.table_name AND conflict.row_key = membership.row_key AND conflict.field_path = membership.field_path
 AND (conflict.project_id IS NULL OR conflict.project_id <> membership.project_id))))`
}

// CanonicalReadProjectSQL maps only unambiguous, active, non-cyclic global
// UUID redirects. The expression is a trusted SQL identifier or expression.
func CanonicalReadProjectSQL(value string) string {
	return `COALESCE((SELECT redirect.project_id FROM project_consolidation_redirects redirect
 JOIN projects src ON src.id = redirect.selector AND src.removed_at IS NULL AND (src.identity_status IS NULL OR src.identity_status = 'active')
 JOIN projects dst ON dst.id = redirect.project_id AND dst.removed_at IS NULL AND (dst.identity_status IS NULL OR dst.identity_status = 'active')
 WHERE redirect.selector = ` + value + ` AND redirect.session_id = '' AND redirect.selector <> redirect.project_id
 AND redirect.selector ~ '^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$'
 AND redirect.selector <> '00000000-0000-0000-0000-000000000000'
 AND redirect.project_id ~ '^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$'
 AND redirect.project_id <> '00000000-0000-0000-0000-000000000000'
 AND NOT EXISTS (SELECT 1 FROM project_consolidation_redirects disagreement WHERE disagreement.selector = redirect.selector AND disagreement.project_id <> redirect.project_id)
 AND NOT EXISTS (SELECT 1 FROM project_consolidation_redirects chain WHERE chain.selector = redirect.project_id OR chain.project_id = redirect.selector)), ` + value + `)`
}

// MembershipPredicate expands reads without changing mutation authority or
// membership conflict and unassigned-row handling.
func MembershipPredicate(alias, field string) string {
	match := func(value string) string {
		return `EXISTS (SELECT 1 FROM (SELECT ?::text AS chosen) selected_scope WHERE ` + CanonicalReadProjectSQL("selected_scope.chosen") + ` = ` + CanonicalReadProjectSQL(value) + `)`
	}
	return `((` + match(alias+`.`+field) + ` AND NOT EXISTS (SELECT 1 FROM project_consolidation_memberships membership
 WHERE membership.table_name = ? AND membership.field_path = '["` + field + `"]' AND membership.row_key->>'id' = CAST(` + alias + `.id AS text))) OR EXISTS
 (SELECT 1 FROM project_consolidation_memberships membership WHERE membership.table_name = ? AND membership.field_path = '["` + field + `"]'
 AND membership.row_key->>'id' = CAST(` + alias + `.id AS text) AND ` + match("membership.project_id") + `
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
