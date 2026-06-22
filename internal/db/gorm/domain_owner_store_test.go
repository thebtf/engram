package gorm

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	gormlib "gorm.io/gorm"
)

func TestMigration150_DomainOwnerRegistry(t *testing.T) {
	db, cleanup := openTestDB(t)
	defer cleanup()

	domain := fmt.Sprintf("zz-test-domain-%d", time.Now().UnixNano())
	t.Cleanup(func() {
		_ = db.Exec(`DELETE FROM memory_domain_owners WHERE domain LIKE 'zz-test-domain-%'`).Error
	})

	for _, column := range []string{"domain", "owner_principal", "owner_principal_kind", "mode", "created_at", "updated_at"} {
		var count int
		require.NoError(t, db.Raw(`
			SELECT COUNT(*)
			FROM information_schema.columns
			WHERE table_schema = 'public'
			  AND table_name = 'memory_domain_owners'
			  AND column_name = ?
		`, column).Scan(&count).Error)
		require.Equal(t, 1, count, "memory_domain_owners.%s must exist", column)
	}

	require.NoError(t, db.Exec(`
		INSERT INTO memory_domain_owners (domain, owner_principal, owner_principal_kind, mode)
		VALUES (?, 'agent/alice', 'agent', 'warn')
	`, domain).Error)

	emptyDomainErr := db.Exec(`
		INSERT INTO memory_domain_owners (domain, owner_principal, owner_principal_kind, mode)
		VALUES ('', 'agent/alice', 'agent', 'warn')
	`).Error
	require.Error(t, emptyDomainErr, "empty domain must be rejected")

	emptyOwnerErr := db.Exec(`
		INSERT INTO memory_domain_owners (domain, owner_principal, owner_principal_kind, mode)
		VALUES (?, '', 'agent', 'warn')
	`, domain+"-empty-owner").Error
	require.Error(t, emptyOwnerErr, "empty owner principal must be rejected")

	invalidKindErr := db.Exec(`
		INSERT INTO memory_domain_owners (domain, owner_principal, owner_principal_kind, mode)
		VALUES (?, 'agent/alice', 'daemon', 'warn')
	`, domain+"-bad-kind").Error
	require.Error(t, invalidKindErr, "invalid owner principal kind must be rejected")

	invalidModeErr := db.Exec(`
		INSERT INTO memory_domain_owners (domain, owner_principal, owner_principal_kind, mode)
		VALUES (?, 'agent/alice', 'agent', 'observe')
	`, domain+"-bad-mode").Error
	require.Error(t, invalidModeErr, "invalid mode must be rejected")

	var idxCount int
	require.NoError(t, db.Raw(`
		SELECT COUNT(*)
		FROM pg_indexes
		WHERE tablename = 'memory_domain_owners'
		  AND indexname = 'idx_memory_domain_owners_owner'
	`).Scan(&idxCount).Error)
	require.Equal(t, 1, idxCount, "owner lookup index must exist")
}

func TestDomainOwnerStore(t *testing.T) {
	db, cleanup := openTestDB(t)
	defer cleanup()

	ctx := context.Background()
	store := NewDomainOwnerStore(&Store{DB: db})
	domain := fmt.Sprintf("zz-test-domain-%d", time.Now().UnixNano())
	t.Cleanup(func() {
		_ = db.Exec(`DELETE FROM memory_domain_owners WHERE domain LIKE ?`, domain+"%").Error
	})

	created, err := store.Upsert(ctx, &DomainOwner{
		Domain:             domain,
		OwnerPrincipal:     "agent/alice",
		OwnerPrincipalKind: "agent",
		Mode:               "warn",
	})
	require.NoError(t, err)
	require.Equal(t, domain, created.Domain)
	require.Equal(t, "agent/alice", created.OwnerPrincipal)
	require.Equal(t, "agent", created.OwnerPrincipalKind)
	require.Equal(t, "warn", created.Mode)
	require.False(t, created.CreatedAt.IsZero())
	require.False(t, created.UpdatedAt.IsZero())

	got, err := store.Get(ctx, domain)
	require.NoError(t, err)
	require.Equal(t, created.Domain, got.Domain)
	require.Equal(t, created.OwnerPrincipal, got.OwnerPrincipal)

	updated, err := store.Upsert(ctx, &DomainOwner{
		Domain:             domain,
		OwnerPrincipal:     "agent/alice",
		OwnerPrincipalKind: "agent",
		Mode:               "reject",
	})
	require.NoError(t, err)
	require.Equal(t, "reject", updated.Mode)
	require.Equal(t, created.CreatedAt.Unix(), updated.CreatedAt.Unix(), "upsert must update the row, not replace history")
	require.True(t, !updated.UpdatedAt.Before(created.UpdatedAt))

	listed, err := store.List(ctx, DomainOwnerListOptions{
		Domain:             domain,
		OwnerPrincipal:     "agent/alice",
		OwnerPrincipalKind: "agent",
		Mode:               "reject",
		Limit:              10,
	})
	require.NoError(t, err)
	require.NotEmpty(t, listed)
	var saw bool
	for _, row := range listed {
		if row.Domain == domain {
			saw = true
			assert.Equal(t, "reject", row.Mode)
		}
	}
	require.True(t, saw, "list must support owner/mode lookup")

	_, err = store.Get(ctx, domain+"-missing")
	require.ErrorIs(t, err, gormlib.ErrRecordNotFound)

	for name, row := range map[string]*DomainOwner{
		"nil":          nil,
		"empty domain": {Domain: "", OwnerPrincipal: "agent/alice", OwnerPrincipalKind: "agent", Mode: "warn"},
		"empty owner":  {Domain: domain + "-empty-owner", OwnerPrincipal: "", OwnerPrincipalKind: "agent", Mode: "warn"},
		"bad kind":     {Domain: domain + "-bad-kind", OwnerPrincipal: "agent/alice", OwnerPrincipalKind: "daemon", Mode: "warn"},
		"bad mode":     {Domain: domain + "-bad-mode", OwnerPrincipal: "agent/alice", OwnerPrincipalKind: "agent", Mode: "observe"},
	} {
		t.Run("rejects "+name, func(t *testing.T) {
			_, err := store.Upsert(ctx, row)
			require.Error(t, err)
		})
	}
}

func TestDomainOwnerStoreRaceAndConflict(t *testing.T) {
	db, cleanup := openTestDB(t)
	defer cleanup()

	ctx := context.Background()
	store := NewDomainOwnerStore(&Store{DB: db})
	domain := fmt.Sprintf("zz-test-domain-race-%d", time.Now().UnixNano())
	t.Cleanup(func() {
		_ = db.Exec(`DELETE FROM memory_domain_owners WHERE domain LIKE ?`, domain+"%").Error
	})

	initial, err := store.Upsert(ctx, &DomainOwner{
		Domain:             domain,
		OwnerPrincipal:     "agent/alice",
		OwnerPrincipalKind: "agent",
		Mode:               DomainOwnerModeWarn,
	})
	require.NoError(t, err)

	updated, err := store.UpdateIfUnchanged(ctx, &DomainOwner{
		Domain:             domain,
		OwnerPrincipal:     "agent/bob",
		OwnerPrincipalKind: "agent",
		Mode:               DomainOwnerModeReject,
	}, initial.UpdatedAt)
	require.NoError(t, err)
	require.Equal(t, "agent/bob", updated.OwnerPrincipal)
	require.Equal(t, DomainOwnerModeReject, updated.Mode)

	_, err = store.UpdateIfUnchanged(ctx, &DomainOwner{
		Domain:             domain,
		OwnerPrincipal:     "agent/charlie",
		OwnerPrincipalKind: "agent",
		Mode:               DomainOwnerModeOff,
	}, initial.UpdatedAt)
	require.Error(t, err)
	require.True(t, errors.Is(err, ErrDomainOwnerConflict), "stale compare/update must return a stable conflict error: %v", err)

	got, err := store.Get(ctx, domain)
	require.NoError(t, err)
	require.Equal(t, "agent/bob", got.OwnerPrincipal, "stale conflict must not partially update owner")
	require.Equal(t, DomainOwnerModeReject, got.Mode, "stale conflict must not partially update mode")

	raceDomain := domain + "-concurrent"
	contenders := []*DomainOwner{
		{Domain: raceDomain, OwnerPrincipal: "agent/delta", OwnerPrincipalKind: "agent", Mode: DomainOwnerModeWarn},
		{Domain: raceDomain, OwnerPrincipal: "service/echo", OwnerPrincipalKind: "service", Mode: DomainOwnerModeReject},
		{Domain: raceDomain, OwnerPrincipal: "human/frank", OwnerPrincipalKind: "human", Mode: DomainOwnerModeOff},
	}
	allowed := make(map[string]string, len(contenders))
	var wg sync.WaitGroup
	for i := 0; i < 30; i++ {
		row := contenders[i%len(contenders)]
		allowed[row.OwnerPrincipal+"|"+row.OwnerPrincipalKind] = row.Mode
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, upsertErr := store.Upsert(ctx, row)
			assert.NoError(t, upsertErr)
		}()
	}
	wg.Wait()

	final, err := store.Get(ctx, raceDomain)
	require.NoError(t, err)
	mode, ok := allowed[final.OwnerPrincipal+"|"+final.OwnerPrincipalKind]
	require.True(t, ok, "final owner/kind must be one complete contender, got %s/%s", final.OwnerPrincipal, final.OwnerPrincipalKind)
	require.Equal(t, mode, final.Mode, "final mode must match the same contender, not a half-updated row")
	require.False(t, final.UpdatedAt.IsZero())
}
