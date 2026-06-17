package gorm

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestCloseIssue_SlugAsymmetry reproduces the "only source project can close" bug:
// an issue created under a legacy slug (e.g. "aimux_<hash>") must be closable by the
// caller resolving to the SAME canonical project ("aimux"), even though the stored
// source_project string differs byte-for-byte from the caller's. Before the fix,
// CloseIssue compared the raw stored value against the already-resolved caller value,
// so the true owner was wrongly rejected. DATABASE_DSN-gated like the other store tests.
func TestCloseIssue_SlugAsymmetry(t *testing.T) {
	db, cleanup := openTestDB(t)
	defer cleanup()

	const canonical = "aimux"
	const legacySlug = "aimux_a1777ae2"
	defer db.Exec(`DELETE FROM issue_comments WHERE issue_id IN (SELECT id FROM issues WHERE target_project = ?)`, canonical)
	defer db.Exec(`DELETE FROM issues WHERE target_project = ?`, canonical)
	defer db.Exec(`DELETE FROM projects WHERE id = ?`, canonical)

	ctx := context.Background()
	store := NewIssueStore(db)

	// Register the project so legacySlug resolves to the canonical id.
	require.NoError(t, UpsertProject(ctx, db, canonical, legacySlug, "git@github.com:thebtf/aimux.git", "aimux", "aimux"))

	// Sanity: the legacy slug now resolves to canonical; canonical resolves to itself.
	require.Equal(t, canonical, ResolveProjectID(ctx, db, legacySlug), "legacy slug must resolve to canonical")
	require.Equal(t, canonical, ResolveProjectID(ctx, db, canonical), "canonical must resolve to itself (idempotent)")

	// Create the issue with source_project stored as the LEGACY slug (as an older
	// session would have stored it), targeting the same project.
	id, err := store.CreateIssue(ctx, &Issue{
		Title:         "issue created under legacy slug",
		SourceProject: legacySlug,
		TargetProject: canonical,
		Type:          "bug",
		Priority:      "medium",
	})
	require.NoError(t, err)
	require.NoError(t, store.UpdateIssueStatus(ctx, id, "resolved"))

	// Close as the SAME project but using the canonical id (what a current session
	// derives). This must succeed — both sides resolve to "aimux".
	err = store.CloseIssue(ctx, id, canonical)
	require.NoError(t, err, "true owner (canonical id) must be able to close an issue created under its legacy slug")

	issue, _, err := store.GetIssue(ctx, id)
	require.NoError(t, err)
	require.Equal(t, "closed", issue.Status)
}

// TestCloseIssue_RejectsForeignProject confirms the fix did not weaken authorization:
// a genuinely different project still cannot close another project's issue.
func TestCloseIssue_RejectsForeignProject(t *testing.T) {
	db, cleanup := openTestDB(t)
	defer cleanup()

	const owner = "engram"
	const foreign = "some-other-project"
	defer db.Exec(`DELETE FROM issues WHERE target_project = ?`, owner)

	ctx := context.Background()
	store := NewIssueStore(db)

	id, err := store.CreateIssue(ctx, &Issue{
		Title:         "owned by engram",
		SourceProject: owner,
		TargetProject: owner,
		Type:          "bug",
		Priority:      "low",
	})
	require.NoError(t, err)
	require.NoError(t, store.UpdateIssueStatus(ctx, id, "resolved"))

	err = store.CloseIssue(ctx, id, foreign)
	require.Error(t, err, "a foreign project must not be able to close another project's issue")

	// Dashboard operator bypass still works.
	require.NoError(t, store.CloseIssue(ctx, id, "dashboard"), "dashboard operator must always be able to close")
}
