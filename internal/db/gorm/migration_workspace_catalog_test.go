package gorm

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestWorkspaceCatalogMigration182RetainsUnnamedHistoricalCheckouts(t *testing.T) {
	db, _ := openInterventionReceiptMigrationTestDB(t)
	fixture := newBrowserReadGrantMigrationFixture(t, db)

	require.NoError(t, workspaceCatalogMigration182().Migrate(db))
	require.NoError(t, workspaceCatalogMigration182().Migrate(db), "the additive migration must be safe to retry before registration")

	var row struct {
		DisplayName *string `gorm:"column:display_name"`
	}
	require.NoError(t, db.Raw(`SELECT display_name FROM ci_checkouts WHERE checkout_id = ?`, fixture.checkout.CheckoutID).Scan(&row).Error)
	require.Nil(t, row.DisplayName, "historical checkouts must remain unnamed rather than receiving a synthetic identity")

	require.Error(t, db.Exec(`UPDATE ci_checkouts SET display_name = ' ' WHERE checkout_id = ?`, fixture.checkout.CheckoutID).Error, "database constraint rejects blank presentation metadata")
	_, err := NewBrowserReadGrantStore(db).SetOwnerChoiceLabel(context.Background(), fixture.owner.ID, browserReadGrantMigrationPrincipal(fixture.owner.ID), fixture.checkout.CheckoutID, "file:///private/worktree")
	require.ErrorIs(t, err, ErrBrowserReadGrantDenied, "application validation rejects locator-shaped metadata")
}
