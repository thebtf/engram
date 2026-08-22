package projectidentity_test

import (
	"context"
	"errors"
	"os"
	"testing"

	gormdb "github.com/thebtf/engram/internal/db/gorm"
	"gorm.io/gorm/logger"
)

func openProjectIdentityTestStore(t *testing.T) *gormdb.Store {
	t.Helper()
	dsn := os.Getenv("DATABASE_DSN")
	if dsn == "" {
		t.Skip("DATABASE_DSN not set, skipping project identity integration test")
	}
	store, err := gormdb.NewStore(gormdb.Config{DSN: dsn, LogLevel: logger.Silent})
	if err != nil {
		t.Fatalf("open project identity test store: %v", err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Errorf("close project identity test store: %v", err)
		}
	})
	return store
}

func clearSelectorRows(t *testing.T, store *gormdb.Store, selectors ...string) {
	t.Helper()
	for _, selector := range selectors {
		if err := store.DB.Unscoped().Exec(`DELETE FROM projects WHERE id = ? OR COALESCE(legacy_ids, ARRAY[]::TEXT[]) @> ARRAY[?]::TEXT[]`, selector, selector).Error; err != nil {
			t.Fatalf("clear selector %q: %v", selector, err)
		}
	}
}

func selectorRowCount(t *testing.T, store *gormdb.Store, selector string) int64 {
	t.Helper()
	var count int64
	if err := store.DB.Unscoped().Model(&gormdb.Project{}).
		Where(`id = ? OR COALESCE(legacy_ids, ARRAY[]::TEXT[]) @> ARRAY[?]::TEXT[]`, selector, selector).
		Count(&count).Error; err != nil {
		t.Fatalf("count selector %q rows: %v", selector, err)
	}
	return count
}

func TestLegacySelectorFence_StoreBoundary(t *testing.T) {
	store := openProjectIdentityTestStore(t)
	ctx := context.Background()

	t.Run("unknown selector only refuses without any row", func(t *testing.T) {
		selector := "ar1-fence-store-unknown"
		clearSelectorRows(t, store, selector)
		t.Cleanup(func() { clearSelectorRows(t, store, selector) })

		resolution, err := gormdb.RegisterAndResolve(ctx, store.DB, selector, nil)
		var identityErr *gormdb.ProjectIdentityError
		if !errors.As(err, &identityErr) || identityErr == nil || identityErr.Code != gormdb.ProjectIdentityAmbiguous || identityErr.UpgradeAction != gormdb.UpgradeActionSendProjectIdentityV2 {
			t.Errorf("error=%T %v, want selector-only PROJECT_IDENTITY_AMBIGUOUS with %q", err, err, gormdb.UpgradeActionSendProjectIdentityV2)
		}
		if resolution.CanonicalProjectID != "" {
			t.Errorf("canonical project=%q, want empty after selector-only refusal", resolution.CanonicalProjectID)
		}
		if count := selectorRowCount(t, store, selector); count != 0 {
			t.Errorf("selector-only request created or retained %d project/alias rows, including soft-deleted rows", count)
		}
	})

	t.Run("known unique V2 selector remains resolvable", func(t *testing.T) {
		selector := "ar1-fence-store-v2-selector"
		canonical := "ar1-fence-store-v2-canonical"
		remote := "https://example.invalid/ar1/fence.git"
		clearSelectorRows(t, store, selector, canonical)
		t.Cleanup(func() { clearSelectorRows(t, store, selector, canonical) })
		if err := gormdb.UpsertProject(ctx, store.DB, canonical, selector, remote, "packages/fence/", "fence"); err != nil {
			t.Fatalf("seed known V2 project: %v", err)
		}

		resolution, err := gormdb.RegisterAndResolve(ctx, store.DB, selector, &gormdb.ProjectIdentityV2{
			Version:         gormdb.ProjectIdentityVersionV2,
			LegacyProjectID: selector,
			DisplayName:     "fence",
			GitRemote:       remote,
			RelativePath:    "packages/fence/",
		})
		if err != nil {
			t.Fatalf("resolve known V2 selector: %v", err)
		}
		if resolution.CanonicalProjectID != canonical {
			t.Fatalf("canonical project=%q, want %q", resolution.CanonicalProjectID, canonical)
		}
	})
}
