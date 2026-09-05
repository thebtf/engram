package gorm

import (
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
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
// migration chain that actually ran in an isolated PostgreSQL schema. The final
// assertion is intentionally RED until migration 171 is implemented; migration
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
