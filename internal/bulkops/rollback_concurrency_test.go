package bulkops

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/lib/pq"
	"github.com/stretchr/testify/require"
	gormdb "github.com/thebtf/engram/internal/db/gorm"
	"github.com/thebtf/engram/pkg/models"
)

func TestRollback_ConcurrentOverlappingEntriesCompleteWithoutDeadlock(t *testing.T) {
	db, store := openRollbackTestDB(t)
	memories := gormdb.NewMemoryStore(store)
	snapshots := gormdb.NewSnapshotStore(db)
	audits := gormdb.NewAuditStore(db)
	ctx := context.Background()

	const iterations = 8
	const rowsPerSnapshot = 4
	for iteration := range iterations {
		ids := make([]int64, 0, rowsPerSnapshot)
		for row := range rowsPerSnapshot {
			memory, err := memories.Create(ctx, &models.Memory{
				Content:        fmt.Sprintf("rollback-concurrency-%d-%d", iteration, row),
				Project:        newBulkTestProject(t, db),
				SourceAgent:    "test",
				Status:         "active",
				Tags:           models.JSONStringArray{},
				SourceSessions: pq.StringArray{"rollback-concurrency"},
			})
			require.NoError(t, err)
			memoryID := memory.ID
			t.Cleanup(func() { _ = db.Exec("DELETE FROM memories WHERE id = ?", memoryID).Error })
			ids = append(ids, memory.ID)
		}

		createSnapshot := func(orderedIDs []int64) string {
			entries := make(map[string]models.SnapshotEntry, len(orderedIDs))
			for _, id := range orderedIDs {
				memory, err := memories.GetForSnapshot(ctx, id)
				require.NoError(t, err)
				token, err := models.SnapshotStateToken(memory)
				require.NoError(t, err)
				entries[fmt.Sprintf("memory:%d", id)] = models.SnapshotEntry{Kind: models.EntryKindRestore, PostStateToken: token}
			}
			raw, err := json.Marshal(entries)
			require.NoError(t, err)
			snapshot, err := models.NewBulkOpSnapshot("rollback-concurrency-"+uuid.NewString(), models.SnapshotOpBulkDelete, "master", raw)
			require.NoError(t, err)
			created, err := snapshots.Create(ctx, snapshot)
			require.NoError(t, err)
			cleanupRollbackSnapshot(t, db, created.SnapshotID)
			return created.SnapshotID
		}

		reversedIDs := append([]int64(nil), ids...)
		for left, right := 0, len(reversedIDs)-1; left < right; left, right = left+1, right-1 {
			reversedIDs[left], reversedIDs[right] = reversedIDs[right], reversedIDs[left]
		}
		snapshotA := createSnapshot(ids)
		snapshotB := createSnapshot(reversedIDs)

		start := make(chan struct{})
		errs := make([]error, 2)
		var group sync.WaitGroup
		group.Add(2)
		for index, snapshotID := range []string{snapshotA, snapshotB} {
			go func(index int, snapshotID string) {
				defer group.Done()
				<-start
				_, errs[index] = Rollback(ctx, adminIdentity(), snapshotID, snapshots, memories, audits, nil)
			}(index, snapshotID)
		}
		close(start)
		group.Wait()
		require.NoError(t, errs[0])
		require.NoError(t, errs[1])

	}
}
