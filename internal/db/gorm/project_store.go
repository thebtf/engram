// Package gorm provides GORM-based database operations for engram.
package gorm

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"unicode"

	"github.com/thebtf/engram/internal/auditcontext"
	"github.com/thebtf/engram/internal/projectidentity"

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
	strictProjectAnchorV2    = regexp.MustCompile(`^[0-9a-f]{32}$`)
	strictProjectSelectorV2  = regexp.MustCompile(`^[A-Za-z0-9_.\\/:-]+$`)
	reservedProjectBindingV2 = regexp.MustCompile(`^p2[gn]_[0-9a-f]{32}$`)
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

func projectIdentityWriteError(err error) error {
	var identityErr *ProjectIdentityError
	if errors.As(err, &identityErr) && identityErr != nil {
		return err
	}
	return unavailableProjectIdentity(err)
}

// ProjectIdentityPublicMessage returns a stable transport-safe diagnostic. Raw
// database errors remain server-side and must never be serialized to clients.
func ProjectIdentityPublicMessage(err error) string {
	var identityErr *ProjectIdentityError
	if errors.As(err, &identityErr) && identityErr != nil {
		switch identityErr.Code {
		case ProjectIdentityInvalid:
			return "project identity metadata is invalid"
		case ProjectIdentityAmbiguous:
			return "project identity selector is ambiguous"
		}
	}
	return "project identity registration is unavailable"
}

// RegisterAndResolve reads a pinned UUID or an evidence-backed migration redirect.
// Historical metadata is validated but never mints, binds or mutates a project.
func RegisterAndResolve(ctx context.Context, db *gorm.DB, selector string, identity *ProjectIdentityV2) (ProjectIdentityResolution, error) {
	if err := validateProjectSelectorV2(selector); err != nil {
		return ProjectIdentityResolution{}, err
	}
	if identity != nil {
		if err := ValidateProjectIdentityV2(*identity); err != nil {
			return ProjectIdentityResolution{}, err
		}
	}
	if projectidentity.ValidProjectID(selector) {
		return ProjectIdentityResolution{CanonicalProjectID: selector}, nil
	}
	if db == nil {
		return ProjectIdentityResolution{}, unavailableProjectIdentity(fmt.Errorf("project identity database is not ready"))
	}
	canonical, err := (&ProjectConsolidationStore{db: db}).ResolveLegacy(ctx, selector, auditcontext.SourceSession(ctx))
	if err != nil {
		var refusal *ConsolidationError
		if errors.As(err, &refusal) {
			return ProjectIdentityResolution{}, err
		}
		return ProjectIdentityResolution{}, unavailableProjectIdentity(err)
	}
	if !projectidentity.ValidProjectID(canonical) {
		return ProjectIdentityResolution{}, consolidationHold("UNRESOLVED_MEMBERSHIP")
	}
	return ProjectIdentityResolution{CanonicalProjectID: canonical}, nil
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
	if reservedProjectBindingV2.MatchString(alias) {
		return invalidProjectIdentity("project alias uses the reserved identity binding namespace")
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
			return projectIdentityWriteError(err)
		}
		return nil
	})
}

// RegisterLegacyProject accepts only a proven redirect to the selected UUID.
// It preserves historical rows and aliases without registration writes.
func RegisterLegacyProject(ctx context.Context, db *gorm.DB, canonical, alias, gitRemote, relativePath, displayName string) error {
	if err := validateProjectSelectorV2(canonical); err != nil {
		return err
	}
	if err := ValidateProjectAliasV2(alias); err != nil {
		return err
	}
	if gitRemoteHasUserinfo(gitRemote) {
		return invalidProjectIdentity("git_remote contains userinfo")
	}
	selected, err := RegisterAndResolve(ctx, db, canonical, nil)
	if err != nil {
		return err
	}
	legacy, err := RegisterAndResolve(ctx, db, alias, nil)
	if err != nil {
		return err
	}
	if selected.CanonicalProjectID != legacy.CanonicalProjectID {
		return consolidationHold("CONFLICTING_MEMBERSHIP")
	}
	return nil
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

// ValidateProjectIdentityV2 rejects malformed historical transport metadata.
// Accepted metadata does not establish project membership or authorization.
func ValidateProjectIdentityV2(identity ProjectIdentityV2) error {
	if identity.Version != ProjectIdentityVersionV2 {
		return invalidProjectIdentity("unsupported identity version")
	}
	if len(identity.LegacyProjectID) > 256 || len(identity.DisplayName) > 256 ||
		strings.TrimSpace(identity.LegacyProjectID) != identity.LegacyProjectID ||
		strings.TrimSpace(identity.DisplayName) != identity.DisplayName ||
		containsProjectIdentityControl(identity.LegacyProjectID) || containsProjectIdentityControl(identity.DisplayName) ||
		reservedProjectBindingV2.MatchString(identity.LegacyProjectID) {
		return invalidProjectIdentity("identity selector or display name is malformed or reserved")
	}
	hasGit := identity.GitRemote != "" || identity.RelativePath != ""
	hasAnchor := identity.NonGitAnchor != "" || identity.AnchorShared != nil
	if hasGit == hasAnchor {
		return invalidProjectIdentity("exactly one identity source is required")
	}
	if hasGit {
		if identity.GitRemote == "" || len(identity.GitRemote) > 2048 || strings.TrimSpace(identity.GitRemote) != identity.GitRemote || containsProjectIdentityControl(identity.GitRemote) || gitRemoteHasUserinfo(identity.GitRemote) {
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

func appendProjectAliases(ctx context.Context, tx *gorm.DB, canonical string, aliases ...string) error {
	return appendProjectAliasesOwned(ctx, tx, canonical, false, aliases...)
}

// appendProjectAliasesOwned appends aliases to canonical. An alias a different
// live row already owns fails the call unless skipOwned is set, in which case
// it is skipped and the remaining aliases still append. Only a full git
// identity may set skipOwned: there the v2 contract has already fixed the
// tenant, and the shadowing rows are legacy dirName or path-hash collisions
// that must not deny service. Every other caller stays fail-closed.
func appendProjectAliasesOwned(ctx context.Context, tx *gorm.DB, canonical string, skipOwned bool, aliases ...string) error {
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
		var owners int64
		if err := tx.WithContext(ctx).Model(&Project{}).
			Where("removed_at IS NULL AND id <> ? AND (id = ? OR COALESCE(legacy_ids, ARRAY[]::TEXT[]) @> ARRAY[?]::TEXT[])", canonical, alias, alias).
			Count(&owners).Error; err != nil {
			return fmt.Errorf("check project alias %s ownership: %w", alias, err)
		}
		if owners != 0 {
			if !skipOwned {
				return ambiguousProjectIdentity("project alias already selects a different canonical project")
			}
			continue
		}
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

func gitRemoteHasUserinfo(value string) bool {
	authority, ok := gitRemoteAuthority(value)
	return ok && strings.Contains(authority, "@")
}

func gitRemoteAuthority(value string) (string, bool) {
	if strings.HasPrefix(value, "//") {
		value = value[2:]
	} else {
		schemeEnd := strings.Index(value, "://")
		if schemeEnd <= 0 || !isURLScheme(value[:schemeEnd]) {
			return "", false
		}
		value = value[schemeEnd+3:]
	}
	if end := strings.IndexAny(value, "/?#"); end >= 0 {
		value = value[:end]
	}
	return value, true
}

func isURLScheme(value string) bool {
	for i := range len(value) {
		char := value[i]
		if char >= 'a' && char <= 'z' || char >= 'A' && char <= 'Z' ||
			i > 0 && (char >= '0' && char <= '9' || char == '+' || char == '-' || char == '.') {
			continue
		}
		return false
	}
	return true
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
	if gitRemoteHasUserinfo(gitRemote) {
		return invalidProjectIdentity("git_remote contains userinfo")
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

var ErrKnowledgeProjectForbidden = errors.New("project read forbidden")

var errProjectIdentityUnresolved = errors.New("project identity is missing or ambiguous")

// ResolveProjectIDStrict resolves one canonical project or legacy alias without
// mutation. It fails closed on missing or ambiguous rows and is used by
// credential-class enforcement where returning the caller input would turn a
// selector into authority.
func ResolveProjectIDStrict(ctx context.Context, db *gorm.DB, projectID string) (string, error) {
	if db == nil || strings.TrimSpace(projectID) != projectID || projectID == "" {
		return "", fmt.Errorf("project identity unavailable")
	}
	var canonicalIDs []string
	if err := db.WithContext(ctx).
		Raw(`SELECT id FROM projects
			WHERE removed_at IS NULL
			  AND (id = ? OR COALESCE(legacy_ids, ARRAY[]::TEXT[]) @> ARRAY[?]::TEXT[])
			LIMIT 2`, projectID, projectID).
		Scan(&canonicalIDs).Error; err != nil {
		return "", fmt.Errorf("resolve canonical project: %w", err)
	}
	if len(canonicalIDs) != 1 || canonicalIDs[0] == "" {
		return "", errProjectIdentityUnresolved
	}
	return canonicalIDs[0], nil
}
