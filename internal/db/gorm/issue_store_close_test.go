package gorm

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// uniqueProjectID returns a test-only project id that cannot collide with real
// project rows on a shared/dev database. Tests MUST NOT clean up by project id
// (that would delete unrelated rows); they clean up by the specific inserted
// issue id instead.
func uniqueProjectID(t *testing.T, base string) string {
	t.Helper()
	return fmt.Sprintf("zz-test-%s-%d", base, time.Now().UnixNano())
}

// TestCloseIssue_SlugAsymmetry reproduces the "only source project can close" bug:
// an issue created under a legacy slug (e.g. "aimux_<hash>") must be closable by the
// caller resolving to the SAME canonical project, even though the stored source_project
// string differs byte-for-byte from the caller's. Before the fix, CloseIssue compared the
// raw stored value against the already-resolved caller value, so the true owner was wrongly
// rejected. DATABASE_DSN-gated; uses unique test-only ids and cleans up ONLY the rows it
// creates (never by project — that would be destructive on a shared DB).
func TestCloseIssue_SlugAsymmetry(t *testing.T) {
	db, cleanup := openTestDB(t)
	defer cleanup()

	ctx := context.Background()
	store := NewIssueStore(db)

	canonical := uniqueProjectID(t, "aimux") // present-day canonical id
	legacySlug := canonical + "_a1777ae2"    // older session's derived slug
	defer db.Exec(`DELETE FROM projects WHERE id = ?`, canonical)

	// Register the project so legacySlug resolves to the canonical id.
	require.NoError(t, UpsertProject(ctx, db, canonical, legacySlug, "git@github.com:thebtf/aimux.git", "aimux", "aimux"))

	// Sanity: legacy slug resolves to canonical; canonical resolves to itself (idempotent).
	require.Equal(t, canonical, ResolveProjectID(ctx, db, legacySlug), "legacy slug must resolve to canonical")
	require.Equal(t, canonical, ResolveProjectID(ctx, db, canonical), "canonical must resolve to itself")

	// Create the issue with source_project stored as the LEGACY slug (as an older session
	// would have stored it), targeting the same project.
	id, err := store.CreateIssue(ctx, &Issue{
		Title:         "issue created under legacy slug",
		SourceProject: legacySlug,
		TargetProject: canonical,
		Type:          "bug",
		Priority:      "medium",
	})
	require.NoError(t, err)
	// Clean up by the specific issue id only.
	defer db.Exec(`DELETE FROM issue_comments WHERE issue_id = ?`, id)
	defer db.Exec(`DELETE FROM issues WHERE id = ?`, id)

	require.NoError(t, store.UpdateIssueStatus(ctx, id, "resolved"))

	// Close as the SAME project but using the canonical id (what a current session derives).
	// Must succeed — both sides resolve to the canonical id.
	require.NoError(t, store.CloseIssue(ctx, id, canonical),
		"true owner (canonical id) must be able to close an issue created under its legacy slug")

	issue, _, err := store.GetIssue(ctx, id)
	require.NoError(t, err)
	require.Equal(t, "closed", issue.Status)
}

// TestCloseIssue_RejectsForeignProject confirms the fix did not weaken authorization:
// a genuinely different project still cannot close another project's issue, and the
// dashboard-operator bypass still works.
func TestCloseIssue_RejectsForeignProject(t *testing.T) {
	db, cleanup := openTestDB(t)
	defer cleanup()

	ctx := context.Background()
	store := NewIssueStore(db)

	owner := uniqueProjectID(t, "owner")
	foreign := uniqueProjectID(t, "foreign")

	id, err := store.CreateIssue(ctx, &Issue{
		Title:         "owned issue",
		SourceProject: owner,
		TargetProject: owner,
		Type:          "bug",
		Priority:      "low",
	})
	require.NoError(t, err)
	defer db.Exec(`DELETE FROM issue_comments WHERE issue_id = ?`, id)
	defer db.Exec(`DELETE FROM issues WHERE id = ?`, id)

	require.NoError(t, store.UpdateIssueStatus(ctx, id, "resolved"))

	err = store.CloseIssue(ctx, id, foreign)
	require.Error(t, err, "a foreign project must not be able to close another project's issue")
	require.ErrorContains(t, err, "close rejected")
	require.ErrorContains(t, err, owner)
	require.ErrorContains(t, err, foreign)

	// Dashboard operator bypass still works.
	require.NoError(t, store.CloseIssue(ctx, id, "dashboard"),
		"dashboard operator must always be able to close")
}
