// Package gorm provides GORM-based database operations for engram.
package gorm

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"unicode"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	ProjectIdentityVersionV2 uint32 = 2

	ProjectIdentityInvalid     = "PROJECT_IDENTITY_INVALID"
	ProjectIdentityAmbiguous   = "PROJECT_IDENTITY_AMBIGUOUS"
	ProjectIdentityUnavailable = "PROJECT_IDENTITY_UNAVAILABLE"

	UpgradeActionRegenerateProjectIdentityV2 = "regenerate_project_identity_v2"
	UpgradeActionSendProjectIdentityV2       = "send_project_identity_v2"
	UpgradeActionRetryProjectRegistration    = "retry_project_identity_registration"
)

var (
	strictProjectAnchorV2   = regexp.MustCompile(`^[0-9a-f]{32}$`)
	strictProjectSelectorV2 = regexp.MustCompile(`^[A-Za-z0-9_.\\/:-]+$`)
)

// ProjectIdentityV2 mirrors the additive protobuf/HTTP contract at the store
// boundary without coupling persistence to either transport package.
type ProjectIdentityV2 struct {
	Version         uint32 `json:"version"`
	LegacyProjectID string `json:"legacy_project_id,omitempty"`
	DisplayName     string `json:"display_name,omitempty"`
	GitRemote       string `json:"git_remote,omitempty"`
	RelativePath    string `json:"relative_path,omitempty"`
	NonGitAnchor    string `json:"non_git_anchor,omitempty"`
	AnchorShared    *bool  `json:"anchor_shared,omitempty"`
}

type ProjectIdentityResolution struct {
	CanonicalProjectID string `json:"canonical_project"`
}

// ProjectIdentityError is stable across HTTP and gRPC. Err is diagnostic;
// clients branch only on Code and UpgradeAction.
type ProjectIdentityError struct {
	Code          string
	UpgradeAction string
	Err           error
}

func (e *ProjectIdentityError) Error() string {
	if e == nil {
		return ""
	}
	if e.Err == nil {
		return e.Code
	}
	return e.Code + ": " + e.Err.Error()
}

func (e *ProjectIdentityError) Unwrap() error { return e.Err }

func invalidProjectIdentity(reason string) error {
	return &ProjectIdentityError{Code: ProjectIdentityInvalid, UpgradeAction: UpgradeActionRegenerateProjectIdentityV2, Err: fmt.Errorf("%s", reason)}
}

func ambiguousProjectIdentity(reason string) error {
	return &ProjectIdentityError{Code: ProjectIdentityAmbiguous, UpgradeAction: UpgradeActionSendProjectIdentityV2, Err: fmt.Errorf("%s", reason)}
}

func unavailableProjectIdentity(err error) error {
	return &ProjectIdentityError{Code: ProjectIdentityUnavailable, UpgradeAction: UpgradeActionRetryProjectRegistration, Err: err}
}

// ProjectIdentityPublicMessage returns a stable transport-safe diagnostic. Raw
// database errors remain server-side and must never be serialized to clients.
func ProjectIdentityPublicMessage(err error) string {
	var identityErr *ProjectIdentityError
	if errors.As(err, &identityErr) {
		switch identityErr.Code {
		case ProjectIdentityInvalid:
			return "project identity metadata is invalid"
		case ProjectIdentityAmbiguous:
			return "project identity selector is ambiguous"
		}
	}
	return "project identity registration is unavailable"
}

// RegisterAndResolve validates and transactionally resolves a selector before
// tenant data access. It deliberately uses only the existing projects table:
// schema changes remain governed by gormigrate, never request-path DDL.
func RegisterAndResolve(ctx context.Context, db *gorm.DB, selector string, identity *ProjectIdentityV2) (ProjectIdentityResolution, error) {
	if err := validateProjectSelectorV2(selector); err != nil {
		return ProjectIdentityResolution{}, err
	}
	if identity != nil {
		if err := ValidateProjectIdentityV2(*identity); err != nil {
			return ProjectIdentityResolution{}, err
		}
	}
	if db == nil {
		return ProjectIdentityResolution{}, unavailableProjectIdentity(fmt.Errorf("project identity database is not ready"))
	}

	var resolution ProjectIdentityResolution
	err := db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		lockKeys := []string{"selector:" + selector}
		bindingKey := ""
		if identity != nil {
			bindingKey = projectIdentityBindingKey(selector, *identity)
			lockKeys = append(lockKeys, "binding:"+bindingKey)
			if identity.LegacyProjectID != "" {
				lockKeys = append(lockKeys, "selector:"+identity.LegacyProjectID)
			}
		}
		sort.Strings(lockKeys)
		lastKey := ""
		for _, key := range lockKeys {
			if key == lastKey {
				continue
			}
			lastKey = key
			if err := tx.Exec(`SELECT pg_advisory_xact_lock(hashtextextended(?, 0))`, key).Error; err != nil {
				return unavailableProjectIdentity(fmt.Errorf("lock project identity: %w", err))
			}
		}

		if identity == nil {
			projects, err := findProjectCandidates(ctx, tx, selector)
			if err != nil {
				return unavailableProjectIdentity(err)
			}
			switch len(projects) {
			case 0:
				if err := createProjectIdentityRow(ctx, tx, selector, "", "", selector, nil); err != nil {
					return unavailableProjectIdentity(err)
				}
				resolution.CanonicalProjectID = selector
				return nil
			case 1:
				resolution.CanonicalProjectID = projects[0].ID
				return nil
			default:
				return ambiguousProjectIdentity("legacy selector maps to multiple canonical projects")
			}
		}

		// A binding key is either the deterministic p2 identity or an alias on
		// an older canonical row. It stores no raw non-git anchor.
		bound, err := findProjectCandidates(ctx, tx, bindingKey)
		if err != nil {
			return unavailableProjectIdentity(err)
		}
		if len(bound) > 1 {
			return ambiguousProjectIdentity("full identity binding is duplicated")
		}
		if len(bound) == 1 {
			canonical := bound[0].ID
			if identity.GitRemote != "" && !projectHasGitIdentity(bound[0], identity.GitRemote, identity.RelativePath) {
				return ambiguousProjectIdentity("binding key conflicts with stored git identity")
			}
			if err := appendProjectAliases(ctx, tx, canonical, selector, identity.LegacyProjectID, bindingKey); err != nil {
				return unavailableProjectIdentity(err)
			}
			resolution.CanonicalProjectID = canonical
			return nil
		}

		if identity.GitRemote != "" {
			exact, err := findGitIdentity(ctx, tx, identity.GitRemote, identity.RelativePath)
			if err != nil {
				return unavailableProjectIdentity(err)
			}
			if len(exact) > 1 {
				return ambiguousProjectIdentity("git identity maps to multiple canonical projects")
			}
			if len(exact) == 1 {
				canonical := exact[0].ID
				if err := appendProjectAliases(ctx, tx, canonical, selector, identity.LegacyProjectID, bindingKey); err != nil {
					return unavailableProjectIdentity(err)
				}
				resolution.CanonicalProjectID = canonical
				return nil
			}
		}

		legacyCandidates, err := findCombinedProjectCandidates(ctx, tx, selector, identity.LegacyProjectID)
		if err != nil {
			return unavailableProjectIdentity(err)
		}
		if len(legacyCandidates) > 1 {
			return ambiguousProjectIdentity("selector and legacy identity map to different canonical projects")
		}
		if len(legacyCandidates) == 1 {
			refreshed, err := lockProjectIdentityCandidate(ctx, tx, legacyCandidates[0].ID)
			if err != nil {
				return unavailableProjectIdentity(err)
			}
			legacyCandidates[0] = refreshed
		}
		canonical := bindingKey
		if len(legacyCandidates) == 1 && projectIsUnboundLegacy(legacyCandidates[0]) {
			canonical = legacyCandidates[0].ID
			if identity.GitRemote != "" {
				result := tx.WithContext(ctx).Model(&Project{}).
					Where("id = ? AND (git_remote IS NULL OR git_remote = '')", canonical).
					Updates(map[string]any{
						"git_remote":    identity.GitRemote,
						"relative_path": identity.RelativePath,
						"display_name":  nullStringValue(identity.DisplayName),
					})
				if result.Error != nil {
					return unavailableProjectIdentity(fmt.Errorf("bind legacy project: %w", result.Error))
				}
				if result.RowsAffected != 1 {
					canonical = bindingKey
				}
			}
		}

		if canonical == bindingKey {
			if err := createProjectIdentityRow(ctx, tx, canonical, identity.GitRemote, identity.RelativePath, identity.DisplayName, []string{selector, identity.LegacyProjectID}); err != nil {
				return unavailableProjectIdentity(err)
			}
		}
		if err := appendProjectAliases(ctx, tx, canonical, selector, identity.LegacyProjectID, bindingKey); err != nil {
			return unavailableProjectIdentity(err)
		}
		resolution.CanonicalProjectID = canonical
		return nil
	})
	if err != nil {
		return ProjectIdentityResolution{}, err
	}
	return resolution, nil
}

// validateProjectSelectorV2 owns the strict transport-independent outer
// selector contract. Legacy alias metadata intentionally remains governed by
// ValidateProjectAliasV2 so established aliases may retain internal spaces.
func validateProjectSelectorV2(selector string) error {
	if selector == "" || len(selector) > 256 || strings.TrimSpace(selector) != selector ||
		strings.Contains(selector, "..") || containsProjectIdentityControl(selector) ||
		!strictProjectSelectorV2.MatchString(selector) {
		return invalidProjectIdentity("project selector is empty or malformed")
	}
	return nil
}

// AttachLegacyAlias adds an old-client selector only when it is absent or
// already points to canonical. A conflicting alias fails before mutation.
func AttachLegacyAlias(ctx context.Context, db *gorm.DB, canonical, alias string) error {
	if err := ValidateProjectAliasV2(canonical); err != nil {
		return err
	}
	if err := ValidateProjectAliasV2(alias); err != nil {
		return err
	}
	if db == nil {
		return unavailableProjectIdentity(fmt.Errorf("project identity database is not ready"))
	}
	return db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		locks := []string{"selector:" + alias, "selector:" + canonical}
		sort.Strings(locks)
		for _, key := range locks {
			if err := tx.Exec(`SELECT pg_advisory_xact_lock(hashtextextended(?, 0))`, key).Error; err != nil {
				return unavailableProjectIdentity(fmt.Errorf("lock project alias: %w", err))
			}
		}
		var canonicalCount int64
		if err := tx.WithContext(ctx).Model(&Project{}).Where("id = ? AND removed_at IS NULL", canonical).Count(&canonicalCount).Error; err != nil {
			return unavailableProjectIdentity(err)
		}
		if canonicalCount != 1 {
			return unavailableProjectIdentity(fmt.Errorf("canonical project %s is unavailable", canonical))
		}
		candidates, err := findProjectCandidates(ctx, tx, alias)
		if err != nil {
			return unavailableProjectIdentity(err)
		}
		if len(candidates) > 1 || len(candidates) == 1 && candidates[0].ID != canonical {
			return ambiguousProjectIdentity("legacy alias already selects a different canonical project")
		}
		if err := appendProjectAliases(ctx, tx, canonical, alias); err != nil {
			return unavailableProjectIdentity(err)
		}
		return nil
	})
}

// ValidateProjectAliasV2 validates a legacy selector without applying the
// stricter canonical selector character allow-list. Legacy directory-derived
// identifiers may contain internal spaces, but never edge whitespace,
// controls, or values too large for the projects identity columns.
func ValidateProjectAliasV2(alias string) error {
	if alias == "" || len(alias) > 256 || strings.TrimSpace(alias) != alias || containsProjectIdentityControl(alias) {
		return invalidProjectIdentity("project alias is malformed")
	}
	return nil
}

// ValidateProjectIdentityV2 validates the complete transport metadata without
// opening a transaction. HTTP callers use it before any registration write;
// RegisterAndResolve repeats it to keep every transport fail-closed.
func ValidateProjectIdentityV2(identity ProjectIdentityV2) error {
	if identity.Version != ProjectIdentityVersionV2 {
		return invalidProjectIdentity("unsupported identity version")
	}
	if len(identity.LegacyProjectID) > 256 || len(identity.DisplayName) > 256 ||
		strings.TrimSpace(identity.LegacyProjectID) != identity.LegacyProjectID ||
		strings.TrimSpace(identity.DisplayName) != identity.DisplayName ||
		containsProjectIdentityControl(identity.LegacyProjectID) || containsProjectIdentityControl(identity.DisplayName) {
		return invalidProjectIdentity("identity selector or display name is malformed")
	}
	hasGit := identity.GitRemote != "" || identity.RelativePath != ""
	hasAnchor := identity.NonGitAnchor != "" || identity.AnchorShared != nil
	if hasGit == hasAnchor {
		return invalidProjectIdentity("exactly one identity source is required")
	}
	if hasGit {
		if identity.GitRemote == "" || len(identity.GitRemote) > 2048 || strings.TrimSpace(identity.GitRemote) != identity.GitRemote || containsProjectIdentityControl(identity.GitRemote) {
			return invalidProjectIdentity("git_remote is missing or malformed")
		}
		if !normalizedProjectRelativePathV2(identity.RelativePath) {
			return invalidProjectIdentity("relative_path is not normalized")
		}
		if identity.NonGitAnchor != "" || identity.AnchorShared != nil {
			return invalidProjectIdentity("git identity carries non-git fields")
		}
		return nil
	}
	if !strictProjectAnchorV2.MatchString(identity.NonGitAnchor) || identity.AnchorShared == nil {
		return invalidProjectIdentity("non-git anchor must be 128-bit lowercase hex with explicit sharing")
	}
	return nil
}

func normalizedProjectRelativePathV2(value string) bool {
	if value == "" {
		return true
	}
	if len(value) > 4096 || strings.TrimSpace(value) != value ||
		strings.HasPrefix(value, "/") || !strings.HasSuffix(value, "/") ||
		strings.Contains(value, "\\") || containsProjectIdentityControl(value) {
		return false
	}
	for _, part := range strings.Split(strings.TrimSuffix(value, "/"), "/") {
		if part == "" || part == "." || part == ".." || strings.TrimSpace(part) != part {
			return false
		}
	}
	return true
}

func projectIdentityBindingKey(selector string, identity ProjectIdentityV2) string {
	var source string
	prefix := "p2g_"
	if identity.GitRemote != "" {
		source = fmt.Sprintf("v2\x00git\x00%s\x00%s", identity.GitRemote, identity.RelativePath)
	} else {
		prefix = "p2n_"
		source = fmt.Sprintf("v2\x00non-git\x00%s\x00%t", identity.NonGitAnchor, *identity.AnchorShared)
		if !*identity.AnchorShared {
			// The legacy ID is path-derived and stable across supported clients,
			// so one local workspace converges even when outer selectors differ.
			// Older metadata may omit it; retain selector isolation as a fallback.
			localScope := identity.LegacyProjectID
			if localScope == "" {
				localScope = selector
			}
			source += "\x00" + localScope
		}
	}
	sum := sha256.Sum256([]byte(source))
	return fmt.Sprintf("%s%x", prefix, sum[:16])
}

func findProjectCandidates(ctx context.Context, tx *gorm.DB, selector string) ([]Project, error) {
	if selector == "" {
		return nil, nil
	}
	var projects []Project
	err := tx.WithContext(ctx).
		Where(`removed_at IS NULL AND (id = ? OR COALESCE(legacy_ids, ARRAY[]::TEXT[]) @> ARRAY[?]::TEXT[])`, selector, selector).
		Order("id ASC").Find(&projects).Error
	return projects, err
}

func findCombinedProjectCandidates(ctx context.Context, tx *gorm.DB, selectors ...string) ([]Project, error) {
	byID := map[string]Project{}
	for _, selector := range selectors {
		projects, err := findProjectCandidates(ctx, tx, selector)
		if err != nil {
			return nil, err
		}
		for _, project := range projects {
			byID[project.ID] = project
		}
	}
	ids := make([]string, 0, len(byID))
	for id := range byID {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	projects := make([]Project, 0, len(ids))
	for _, id := range ids {
		projects = append(projects, byID[id])
	}
	return projects, nil
}

// lockProjectIdentityCandidate serializes the one-time conversion of a legacy
// row into a v2 binding and refreshes the row after any concurrent binder
// commits. The caller must re-evaluate projectIsUnboundLegacy on this value.
func lockProjectIdentityCandidate(ctx context.Context, tx *gorm.DB, id string) (Project, error) {
	var project Project
	err := tx.WithContext(ctx).
		Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("id = ? AND removed_at IS NULL", id).
		First(&project).Error
	if err != nil {
		return Project{}, fmt.Errorf("lock legacy project %s: %w", id, err)
	}
	return project, nil
}

func findGitIdentity(ctx context.Context, tx *gorm.DB, remote, relativePath string) ([]Project, error) {
	var projects []Project
	err := tx.WithContext(ctx).
		Where(`removed_at IS NULL AND git_remote = ? AND COALESCE(relative_path, '') = ?`, remote, relativePath).
		Order("id ASC").Find(&projects).Error
	return projects, err
}

func projectHasGitIdentity(project Project, remote, relativePath string) bool {
	return project.GitRemote.Valid && project.GitRemote.String == remote &&
		(!project.RelativePath.Valid && relativePath == "" || project.RelativePath.Valid && project.RelativePath.String == relativePath)
}

func projectIsUnboundLegacy(project Project) bool {
	if project.GitRemote.Valid && project.GitRemote.String != "" {
		return false
	}
	for _, alias := range project.LegacyIDs {
		if strings.HasPrefix(alias, "p2g_") || strings.HasPrefix(alias, "p2n_") {
			return false
		}
	}
	return !strings.HasPrefix(project.ID, "p2g_") && !strings.HasPrefix(project.ID, "p2n_")
}

func createProjectIdentityRow(ctx context.Context, tx *gorm.DB, id, remote, relativePath, displayName string, aliases []string) error {
	project := Project{
		ID:           id,
		GitRemote:    sql.NullString{String: remote, Valid: remote != ""},
		RelativePath: sql.NullString{String: relativePath, Valid: remote != ""},
		DisplayName:  sql.NullString{String: displayName, Valid: displayName != ""},
	}
	result := tx.WithContext(ctx).Clauses(clause.OnConflict{DoNothing: true}).Create(&project)
	if result.Error != nil {
		return fmt.Errorf("create project identity %s: %w", id, result.Error)
	}
	if result.RowsAffected == 0 {
		var active int64
		if err := tx.WithContext(ctx).Model(&Project{}).Where("id = ? AND removed_at IS NULL", id).Count(&active).Error; err != nil {
			return fmt.Errorf("verify project identity %s: %w", id, err)
		}
		if active != 1 {
			return fmt.Errorf("canonical project %s is unavailable", id)
		}
	}
	return appendProjectAliases(ctx, tx, id, aliases...)
}

func appendProjectAliases(ctx context.Context, tx *gorm.DB, canonical string, aliases ...string) error {
	seen := map[string]struct{}{}
	for _, alias := range aliases {
		if alias == "" || alias == canonical {
			continue
		}
		if strings.TrimSpace(alias) != alias || containsProjectIdentityControl(alias) {
			return invalidProjectIdentity("project alias is not normalized")
		}
		if _, ok := seen[alias]; ok {
			continue
		}
		seen[alias] = struct{}{}
		result := tx.WithContext(ctx).Exec(`UPDATE projects
			SET legacy_ids = array_append(COALESCE(legacy_ids, ARRAY[]::TEXT[]), ?)
			WHERE id = ? AND removed_at IS NULL AND NOT (COALESCE(legacy_ids, ARRAY[]::TEXT[]) @> ARRAY[?]::TEXT[])`, alias, canonical, alias)
		if result.Error != nil {
			return fmt.Errorf("append project alias %s to %s: %w", alias, canonical, result.Error)
		}
		if result.RowsAffected == 0 {
			var count int64
			if err := tx.WithContext(ctx).Model(&Project{}).Where("id = ? AND removed_at IS NULL", canonical).Count(&count).Error; err != nil || count != 1 {
				return fmt.Errorf("canonical project %s is unavailable", canonical)
			}
		}
	}
	return nil
}

func containsProjectIdentityControl(value string) bool {
	return strings.IndexFunc(value, unicode.IsControl) >= 0
}

func nullStringValue(value string) any {
	if value == "" {
		return nil
	}
	return value
}

// UpsertProject registers or updates a project identity record.
//
// newID is the canonical git-remote-based project ID.
// legacyID is the old path-based ID (may be empty on first git-based registration).
// gitRemote and relativePath are the git metadata used to derive newID.
// displayName is the human-readable project name (typically the directory name).
//
// When legacyID is non-empty, this function:
//  1. Upserts the project row (idempotent by primary key).
//  2. Appends legacyID to legacy_ids if not already present.
func UpsertProject(ctx context.Context, db *gorm.DB, newID, legacyID, gitRemote, relativePath, displayName string) error {
	if newID == "" {
		return fmt.Errorf("project newID must not be empty")
	}

	proj := Project{
		ID:           newID,
		GitRemote:    sql.NullString{String: gitRemote, Valid: gitRemote != ""},
		RelativePath: sql.NullString{String: relativePath, Valid: relativePath != ""},
		DisplayName:  sql.NullString{String: displayName, Valid: displayName != ""},
	}
	if err := db.WithContext(ctx).Clauses(clause.OnConflict{DoNothing: true}).Create(&proj).Error; err != nil {
		return fmt.Errorf("upsert project %s: %w", newID, err)
	}

	if legacyID != "" {
		// Append legacyID only if not already present in the array.
		appendSQL := `UPDATE projects
		              SET legacy_ids = array_append(legacy_ids, ?)
		              WHERE id = ?
		                AND NOT (COALESCE(legacy_ids, ARRAY[]::TEXT[]) @> ARRAY[?]::TEXT[])`
		if err := db.WithContext(ctx).Exec(appendSQL, legacyID, newID, legacyID).Error; err != nil {
			return fmt.Errorf("append legacy_id to project %s: %w", newID, err)
		}
	}

	return nil
}

// ResolveProjectID checks if projectID is a legacy alias in the projects table.
// Returns the canonical project ID when a matching alias is found,
// otherwise returns the input projectID unchanged.
func ResolveProjectID(ctx context.Context, db *gorm.DB, projectID string) string {
	if projectID == "" {
		return projectID
	}
	var canonicalID string
	if err := db.WithContext(ctx).
		Raw(`SELECT id FROM projects WHERE removed_at IS NULL AND COALESCE(legacy_ids, ARRAY[]::TEXT[]) @> ARRAY[?]::TEXT[] LIMIT 1`, projectID).
		Scan(&canonicalID).Error; err != nil || canonicalID == "" {
		return projectID
	}
	return canonicalID
}
