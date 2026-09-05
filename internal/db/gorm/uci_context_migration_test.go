package gorm

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	gormlib "gorm.io/gorm"
)

const (
	uciContextRegistryMigrationID  = "171_uci_context_registry"
	uciIndexProjectionMigrationID  = "172_uci_index_projection"
	uciMigrationRegistryBoundaryID = "170_task_memory_context_reference_receipts"
)

type uciAppliedMigration struct {
	ID string `gorm:"column:id"`
}

// TestUCIContextMigrationsReserve171And172 binds the UCI allocation to the
// migration chain that actually ran in an isolated PostgreSQL schema. Migration
// 172 may remain reserved until the projection slice lands.
func TestUCIContextMigrationsReserve171And172(t *testing.T) {
	db, schema := openInterventionReceiptMigrationTestDB(t)

	var applied []uciAppliedMigration
	require.NoError(t, db.Raw(`SELECT id FROM migrations`).Scan(&applied).Error, "read applied migration registry from isolated PostgreSQL schema")
	require.NotEmpty(t, applied, "isolated PostgreSQL migration registry is empty")

	ids := make(map[string]struct{}, len(applied))
	sequences := make(map[int]string, len(applied))
	maxBaseSequence := 0
	for _, migration := range applied {
		if _, exists := ids[migration.ID]; exists {
			t.Fatalf("isolated migration registry reuses ID %q; UCI allocation is unsafe", migration.ID)
		}
		ids[migration.ID] = struct{}{}

		sequence := uciMigrationSequence(t, migration.ID)
		if prior, exists := sequences[sequence]; exists {
			t.Fatalf("isolated migration registry reuses sequence %d for %q and %q; UCI allocation is unsafe", sequence, prior, migration.ID)
		}
		sequences[sequence] = migration.ID

		if sequence <= 170 {
			if sequence > maxBaseSequence {
				maxBaseSequence = sequence
			}
			continue
		}

		switch sequence {
		case 171:
			require.Equal(t, uciContextRegistryMigrationID, migration.ID, "migration sequence 171 was allocated by an intervening migration")
		case 172:
			require.Equal(t, uciIndexProjectionMigrationID, migration.ID, "migration sequence 172 was allocated by an intervening migration")
		default:
			t.Fatalf("migration %q was allocated after the observed boundary before UCI migrations 171 and 172 completed; re-evaluate the plan", migration.ID)
		}
	}

	require.Equal(t, 170, maxBaseSequence, "isolated migration registry high-water changed before the reserved UCI range")
	require.Contains(t, ids, uciMigrationRegistryBoundaryID, "isolated migration registry did not apply the observed boundary")

	_, contextRegistryApplied := ids[uciContextRegistryMigrationID]
	require.True(t, contextRegistryApplied, "UCI context migration is unimplemented: isolated schema %q reached %q but did not apply %q", schema, uciMigrationRegistryBoundaryID, uciContextRegistryMigrationID)
}

func uciMigrationSequence(t *testing.T, id string) int {
	t.Helper()
	prefix, _, ok := strings.Cut(id, "_")
	require.True(t, ok, "migration ID %q has no numeric sequence", id)
	sequence, err := strconv.Atoi(prefix)
	require.NoError(t, err, "migration ID %q has invalid numeric sequence", id)
	return sequence
}

var uciContextRegistryTableNames = [...]string{
	"spaces",
	"sources",
	"space_sources",
	"legacy_context_aliases",
	"ci_profiles",
	"ci_checkouts",
	"ci_views",
}

func TestUCIContextMigration171SchemaIsIdempotentAndPreservesLegacySentinels(t *testing.T) {
	db, _ := openInterventionReceiptMigrationTestDB(t)
	token := uuid.NewString()
	projectID := "uci-context-project-" + token
	require.NoError(t, db.Exec(`
		INSERT INTO projects (id, git_remote, relative_path, display_name)
		VALUES (?, ?, ?, ?)
	`, projectID, "https://example.invalid/"+token, "uci/"+token, "UCI "+token).Error)

	chunk := &CodeChunk{
		ProjectID:      projectID,
		FilePath:       "uci/" + token + ".go",
		ByteStart:      0,
		ByteEnd:        16,
		Language:       "go",
		ChunkType:      "function",
		Content:        "package uci\n",
		ContentSHA256:  "sha256-" + token,
		IndexSessionID: "uci-context-" + token,
	}
	require.NoError(t, NewCodeChunkStore(db).Upsert(context.Background(), chunk))

	type projectSentinel struct {
		GitRemote    string `gorm:"column:git_remote"`
		RelativePath string `gorm:"column:relative_path"`
		DisplayName  string `gorm:"column:display_name"`
	}
	type codeChunkSentinel struct {
		Content        string `gorm:"column:content"`
		IndexSessionID string `gorm:"column:index_session_id"`
	}
	var beforeProject projectSentinel
	var beforeChunk codeChunkSentinel
	require.NoError(t, db.Raw(`SELECT git_remote, relative_path, display_name FROM projects WHERE id = ?`, projectID).Scan(&beforeProject).Error)
	require.NoError(t, db.Raw(`
		SELECT content, index_session_id
		FROM code_chunks
		WHERE project_id = ? AND file_path = ? AND byte_start = ? AND content_sha256 = ?
	`, chunk.ProjectID, chunk.FilePath, chunk.ByteStart, chunk.ContentSHA256).Scan(&beforeChunk).Error)

	require.NoError(t, db.Exec("DELETE FROM migrations WHERE id = ?", uciContextRegistryMigrationID).Error)
	require.NoError(t, runMigrations(db), "migration 171 must reapply over retained legacy rows")
	assertUCIContextMigration171Tables(t, db)
	for _, tableName := range uciContextRegistryTableNames {
		var count int64
		require.NoError(t, db.Table(tableName).Count(&count).Error)
		require.Zero(t, count, "migration 171 must not guess registry rows for %s", tableName)
	}

	var afterProject projectSentinel
	var afterChunk codeChunkSentinel
	require.NoError(t, db.Raw(`SELECT git_remote, relative_path, display_name FROM projects WHERE id = ?`, projectID).Scan(&afterProject).Error)
	require.NoError(t, db.Raw(`
		SELECT content, index_session_id
		FROM code_chunks
		WHERE project_id = ? AND file_path = ? AND byte_start = ? AND content_sha256 = ?
	`, chunk.ProjectID, chunk.FilePath, chunk.ByteStart, chunk.ContentSHA256).Scan(&afterChunk).Error)
	require.Equal(t, beforeProject, afterProject, "migration 171 must not rewrite the projects sentinel")
	require.Equal(t, beforeChunk, afterChunk, "migration 171 must not rewrite the code_chunks sentinel")

	var applied int64
	require.NoError(t, db.Table("migrations").Where("id = ?", uciContextRegistryMigrationID).Count(&applied).Error)
	require.Equal(t, int64(1), applied, "migration 171 must be restored to the migration registry exactly once")
}

func TestUCIContextStoreMigrationRoundTripsSourceLessSpaceAndStagingView(t *testing.T) {
	db, store := openUCIContextMigrationStore(t)
	ctx := context.Background()
	token := uuid.NewString()
	realm := "uci-context-realm-" + token

	spaceInput := CreateSpaceInput{AuthRealm: realm, DisplayName: "space-" + token}
	space, err := store.CreateSpace(ctx, spaceInput)
	require.NoError(t, err)
	var linkCount int64
	require.NoError(t, db.Model(&UCISpaceSource{}).Where("space_id = ?", space.SpaceID).Count(&linkCount).Error)
	require.Zero(t, linkCount, "a space must be valid before it has any source links")
	gotSpace, err := store.GetSpace(ctx, space.SpaceID)
	require.NoError(t, err)
	require.Equal(t, spaceInput.AuthRealm, gotSpace.AuthRealm)
	require.Equal(t, spaceInput.DisplayName, gotSpace.DisplayName)

	source, err := store.CreateSource(ctx, CreateSourceInput{
		AuthRealm:   realm,
		Kind:        UCISourceGit,
		DisplayName: "source-" + token,
	})
	require.NoError(t, err)
	require.NoError(t, store.LinkSpaceSource(ctx, LinkSpaceSourceInput{
		SpaceID:      space.SpaceID,
		SourceID:     source.SourceID,
		DisplayOrder: 3,
	}))
	var link UCISpaceSource
	require.NoError(t, db.Where("space_id = ? AND source_id = ?", space.SpaceID, source.SourceID).First(&link).Error)
	require.Equal(t, realm, link.AuthRealm)
	require.Equal(t, 3, link.DisplayOrder)

	checkout, err := store.RegisterCheckout(ctx, RegisterCheckoutInput{
		SourceID:       source.SourceID,
		WorkstationID:  "workstation-" + token,
		Kind:           UCICheckoutWorkingTree,
		OwnerPrincipal: "principal-" + token,
		LocatorRef:     "file:///uci/" + token,
	})
	require.NoError(t, err)
	gotCheckout, err := store.GetCheckout(ctx, checkout.CheckoutID)
	require.NoError(t, err)
	require.Equal(t, checkout.SourceID, gotCheckout.SourceID)
	require.Equal(t, checkout.IncarnationID, gotCheckout.IncarnationID)

	profileInput := newUCIContextMigrationProfileInput(token)
	profile, err := store.CreateProfile(ctx, profileInput)
	require.NoError(t, err)
	gotProfile, err := store.GetProfile(ctx, profile.ProfileID)
	require.NoError(t, err)
	require.JSONEq(t, profileInput.BuildContextJSON, gotProfile.BuildContextJSON)

	viewInput := newUCIContextMigrationViewInput(checkout, profile, 1, token)
	headOID, objectFormat, refLabel := *viewInput.HeadOID, *viewInput.ObjectFormat, *viewInput.RefLabel
	view, err := store.CreateView(ctx, viewInput)
	require.NoError(t, err)
	require.Equal(t, headOID, *viewInput.HeadOID, "CreateView must not rewrite the caller's head OID")
	require.Equal(t, objectFormat, *viewInput.ObjectFormat, "CreateView must not rewrite the caller's object format")
	require.Equal(t, refLabel, *viewInput.RefLabel, "CreateView must not rewrite the caller's ref label")
	require.NotSame(t, viewInput.HeadOID, view.HeadOID, "stored view must not retain caller-owned pointer")
	require.NotSame(t, viewInput.ObjectFormat, view.ObjectFormat, "stored view must not retain caller-owned pointer")
	require.NotSame(t, viewInput.RefLabel, view.RefLabel, "stored view must not retain caller-owned pointer")

	gotView, err := store.GetView(ctx, view.ViewID)
	require.NoError(t, err)
	require.Equal(t, UCIViewStaging, gotView.State)
	require.Equal(t, checkout.CheckoutID, gotView.CheckoutID)
	require.Equal(t, source.SourceID, gotView.SourceID)
	require.Equal(t, checkout.IncarnationID, gotView.IncarnationID)
	require.Equal(t, profile.ProfileID, gotView.ProfileID)
	require.Equal(t, int64(1), gotView.Generation)

	_, err = store.CreateView(ctx, viewInput)
	require.Error(t, err, "a checkout generation must be unique")
	var viewCount int64
	require.NoError(t, db.Model(&UCIView{}).Where("checkout_id = ?", checkout.CheckoutID).Count(&viewCount).Error)
	require.Equal(t, int64(1), viewCount, "duplicate checkout generation must not create another view")
}

func TestUCIContextStoreMigrationRejectsRealmAndViewBoundaryViolations(t *testing.T) {
	db, store := openUCIContextMigrationStore(t)
	ctx := context.Background()
	token := uuid.NewString()
	realm := "uci-context-realm-" + token
	otherRealm := "uci-context-other-realm-" + token

	space, err := store.CreateSpace(ctx, CreateSpaceInput{AuthRealm: realm, DisplayName: "space-" + token})
	require.NoError(t, err)
	otherSource, err := store.CreateSource(ctx, CreateSourceInput{AuthRealm: otherRealm, Kind: UCISourceDirectory, DisplayName: "other-source-" + token})
	require.NoError(t, err)
	require.Error(t, store.LinkSpaceSource(ctx, LinkSpaceSourceInput{SpaceID: space.SpaceID, SourceID: otherSource.SourceID}), "space and source links must not cross realms")
	var linkCount int64
	require.NoError(t, db.Model(&UCISpaceSource{}).Where("space_id = ?", space.SpaceID).Count(&linkCount).Error)
	require.Zero(t, linkCount, "rejected cross-realm link must not persist")

	source, err := store.CreateSource(ctx, CreateSourceInput{AuthRealm: realm, Kind: UCISourceGit, DisplayName: "source-" + token})
	require.NoError(t, err)
	checkout, err := store.RegisterCheckout(ctx, RegisterCheckoutInput{
		SourceID:       source.SourceID,
		WorkstationID:  "workstation-" + token,
		Kind:           UCICheckoutWorkingTree,
		OwnerPrincipal: "principal-" + token,
		LocatorRef:     "file:///uci/" + token,
	})
	require.NoError(t, err)
	profile, err := store.CreateProfile(ctx, newUCIContextMigrationProfileInput(token))
	require.NoError(t, err)

	wrongSource := newUCIContextMigrationViewInput(checkout, profile, 1, token)
	wrongSource.SourceID = otherSource.SourceID
	_, err = store.CreateView(ctx, wrongSource)
	require.Error(t, err, "view checkout/source tuple must match the registered checkout")

	wrongIncarnation := newUCIContextMigrationViewInput(checkout, profile, 2, token)
	wrongIncarnation.IncarnationID = uuid.NewString()
	_, err = store.CreateView(ctx, wrongIncarnation)
	require.Error(t, err, "view checkout/incarnation tuple must match the registered checkout")
	var viewCount int64
	require.NoError(t, db.Model(&UCIView{}).Where("checkout_id = ?", checkout.CheckoutID).Count(&viewCount).Error)
	require.Zero(t, viewCount, "rejected checkout/source/incarnation tuples must not persist")
}

func TestUCIContextStoreMigrationAliasesAreIdempotentAtomicAndInputSafe(t *testing.T) {
	db, store := openUCIContextMigrationStore(t)
	ctx := context.Background()
	token := uuid.NewString()
	realm := "uci-context-realm-" + token
	otherRealm := "uci-context-other-realm-" + token
	space, err := store.CreateSpace(ctx, CreateSpaceInput{AuthRealm: realm, DisplayName: "space-" + token})
	require.NoError(t, err)

	targetSpaceID := space.SpaceID
	aliasInput := newUCIContextMigrationAliasInput(realm, token, &targetSpaceID)
	first, err := store.UpsertLegacyContextAlias(ctx, aliasInput)
	require.NoError(t, err)
	require.Equal(t, targetSpaceID, *aliasInput.SpaceID, "upsert must not rewrite the caller's target value")
	require.NotSame(t, aliasInput.SpaceID, first.SpaceID, "stored alias must not retain caller-owned target pointer")
	retry, err := store.UpsertLegacyContextAlias(ctx, aliasInput)
	require.NoError(t, err)
	require.Equal(t, first.AliasID, retry.AliasID, "an exact alias retry must return the existing row")

	conflict := aliasInput
	conflict.Revision++
	_, err = store.UpsertLegacyContextAlias(ctx, conflict)
	require.True(t, errors.Is(err, ErrUCIAliasConflict), "conflicting alias retries must report ErrUCIAliasConflict")
	aliases, err := store.LookupLegacyContextAliases(ctx, uciContextMigrationAliasKey(aliasInput))
	require.NoError(t, err)
	require.Len(t, aliases, 1)
	require.Equal(t, first.AliasID, aliases[0].AliasID)
	require.Equal(t, int64(1), aliases[0].Revision, "conflicting retry must not overwrite the existing alias")
	require.JSONEq(t, first.Provenance, aliases[0].Provenance)

	crossRealm := aliasInput
	crossRealm.AuthRealm = otherRealm
	crossRealm.Value = "cross-realm-" + uuid.NewString()
	_, err = store.UpsertLegacyContextAlias(ctx, crossRealm)
	require.Error(t, err, "alias targets must not cross realms")
	crossRealmAliases, err := store.LookupLegacyContextAliases(ctx, uciContextMigrationAliasKey(crossRealm))
	require.NoError(t, err)
	require.Empty(t, crossRealmAliases)

	unmapped := aliasInput
	unmapped.Value = "unmapped-" + uuid.NewString()
	unmapped.MappingState = UCIAliasUnmapped
	_, err = store.UpsertLegacyContextAlias(ctx, unmapped)
	require.Error(t, err, "unmapped aliases must not carry a target")
	unmappedAliases, err := store.LookupLegacyContextAliases(ctx, uciContextMigrationAliasKey(unmapped))
	require.NoError(t, err)
	require.Empty(t, unmappedAliases)

	atomicAlias := aliasInput
	atomicAlias.SpaceID = nil
	atomicSpaceName := "atomic-space-" + uuid.NewString()
	createdSpace, createdAlias, err := store.CreateSpaceWithLegacyAlias(ctx, CreateSpaceInput{AuthRealm: realm, DisplayName: atomicSpaceName}, atomicAlias)
	require.True(t, errors.Is(err, ErrUCIAliasConflict), "alias conflict must roll back its newly created space")
	require.Nil(t, createdSpace)
	require.Nil(t, createdAlias)
	require.Nil(t, atomicAlias.SpaceID, "CreateSpaceWithLegacyAlias must not assign the caller's alias input")
	var createdSpaceCount int64
	require.NoError(t, db.Model(&UCISpace{}).Where("auth_realm = ? AND display_name = ?", realm, atomicSpaceName).Count(&createdSpaceCount).Error)
	require.Zero(t, createdSpaceCount, "alias conflict must atomically roll back the new space")

	aliases, err = store.LookupLegacyContextAliases(ctx, uciContextMigrationAliasKey(aliasInput))
	require.NoError(t, err)
	require.Len(t, aliases, 1)
	require.Equal(t, first.AliasID, aliases[0].AliasID, "atomic conflict must leave the existing alias untouched")
}

func openUCIContextMigrationStore(t *testing.T) (*gormlib.DB, *UCIContextStore) {
	t.Helper()
	db, _ := openInterventionReceiptMigrationTestDB(t)
	return db, NewUCIContextStore(db)
}

func newUCIContextMigrationProfileInput(token string) CreateProfileInput {
	return CreateProfileInput{
		ParserBundleDigest:   uciContextMigrationDigest("a"),
		ResolverRevision:     "resolver-" + token,
		ChunkerRevision:      "chunker-" + token,
		IgnorePolicyDigest:   uciContextMigrationDigest("b"),
		BuildContextJSON:     `{"fixture":"` + token + `"}`,
		SecretPolicyRevision: "secret-policy-" + token,
	}
}

func newUCIContextMigrationViewInput(checkout *UCICheckout, profile *UCIAnalysisProfile, generation int64, token string) CreateViewInput {
	scanStart := time.Now().UTC().Truncate(time.Microsecond)
	headOID := strings.Repeat("a", 40)
	objectFormat := "sha1"
	refLabel := "refs/heads/uci-" + token
	return CreateViewInput{
		CheckoutID:     checkout.CheckoutID,
		SourceID:       checkout.SourceID,
		IncarnationID:  checkout.IncarnationID,
		Generation:     generation,
		ProfileID:      profile.ProfileID,
		HeadOID:        &headOID,
		ObjectFormat:   &objectFormat,
		RefLabel:       &refLabel,
		ObservedFSSeq:  generation,
		ScanStart:      scanStart,
		ScanEnd:        scanStart.Add(time.Second),
		ManifestDigest: uciContextMigrationDigest("c"),
		CoverageJSON:   `{"fixture":"` + token + `"}`,
		State:          UCIViewStaging,
	}
}

func newUCIContextMigrationAliasInput(realm, token string, spaceID *string) LegacyContextAliasInput {
	return LegacyContextAliasInput{
		AuthRealm:       realm,
		LegacyDomain:    "legacy-domain-" + token,
		Scheme:          "path",
		Value:           "legacy-value-" + token,
		ClientNamespace: "client-" + token,
		SpaceID:         spaceID,
		MappingState:    UCIAliasResolved,
		Revision:        1,
		Provenance:      `{"fixture":"` + token + `"}`,
	}
}

func uciContextMigrationAliasKey(in LegacyContextAliasInput) LegacyContextAliasKey {
	return LegacyContextAliasKey{
		AuthRealm:       in.AuthRealm,
		LegacyDomain:    in.LegacyDomain,
		Scheme:          in.Scheme,
		Value:           in.Value,
		ClientNamespace: in.ClientNamespace,
	}
}

func uciContextMigrationDigest(hex string) string {
	return "sha256:" + strings.Repeat(hex, 64)
}

func assertUCIContextMigration171Tables(t *testing.T, db *gormlib.DB) {
	t.Helper()
	require.Len(t, uciContextRegistryTableNames, 7, "migration 171 must own exactly seven registry tables")
	var actual []string
	require.NoError(t, db.Raw(`
		SELECT table_name
		FROM information_schema.tables
		WHERE table_schema = current_schema()
		  AND table_name IN (
			'spaces',
			'sources',
			'space_sources',
			'legacy_context_aliases',
			'ci_profiles',
			'ci_checkouts',
			'ci_views'
		  )
	`).Scan(&actual).Error)
	require.ElementsMatch(t, uciContextRegistryTableNames[:], actual)
}
