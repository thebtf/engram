package principalmemory

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	gormlib "gorm.io/gorm"

	gormdb "github.com/thebtf/engram/internal/db/gorm"
)

func TestDomainRegistryService_AllowsMissingOffAndSameOwner(t *testing.T) {
	store := &fakeDomainOwnerStore{
		rows: map[string]*gormdb.DomainOwner{
			"off-domain": {
				Domain:             "off-domain",
				OwnerPrincipal:     "agent/alice",
				OwnerPrincipalKind: "agent",
				Mode:               gormdb.DomainOwnerModeOff,
			},
			"same-owner": {
				Domain:             "same-owner",
				OwnerPrincipal:     "agent/alice",
				OwnerPrincipalKind: "agent",
				Mode:               gormdb.DomainOwnerModeWarn,
			},
		},
	}
	audit := &fakeAuditLogger{}
	svc := NewDomainRegistryService(store, audit)

	missing, err := svc.CheckWrite(context.Background(), DomainWriteCheckRequest{
		Domain: "missing-domain",
		Writer: PrincipalRef{Principal: "agent/bob", PrincipalKind: "agent"},
	})
	require.NoError(t, err)
	require.NotNil(t, missing)
	assert.True(t, missing.Allowed)
	assert.Equal(t, AuditStatusNotRequired, missing.AuditStatus)

	off, err := svc.CheckWrite(context.Background(), DomainWriteCheckRequest{
		Domain: "off-domain",
		Writer: PrincipalRef{Principal: "agent/bob", PrincipalKind: "agent"},
	})
	require.NoError(t, err)
	assert.True(t, off.Allowed)
	assert.Equal(t, gormdb.DomainOwnerModeOff, off.Mode)

	same, err := svc.CheckWrite(context.Background(), DomainWriteCheckRequest{
		Domain: "same-owner",
		Writer: PrincipalRef{Principal: "agent/alice", PrincipalKind: "agent"},
	})
	require.NoError(t, err)
	assert.True(t, same.Allowed)
	assert.Nil(t, same.Warning)
	assert.Empty(t, audit.entries, "allowed paths must not write warning/reject audit")
}

func TestDomainRegistryService_WarnAuditsAndAllowsCrossOwner(t *testing.T) {
	audit := &fakeAuditLogger{}
	svc := NewDomainRegistryService(&fakeDomainOwnerStore{
		rows: map[string]*gormdb.DomainOwner{
			"memory-lab": {
				Domain:             "memory-lab",
				OwnerPrincipal:     "agent/alice",
				OwnerPrincipalKind: "agent",
				Mode:               gormdb.DomainOwnerModeWarn,
			},
		},
	}, audit)

	decision, err := svc.CheckWrite(context.Background(), DomainWriteCheckRequest{
		Domain:          "memory-lab",
		Writer:          PrincipalRef{Principal: "agent/bob", PrincipalKind: "agent"},
		SourceSessionID: "session-99",
	})
	require.NoError(t, err)
	require.NotNil(t, decision)
	assert.True(t, decision.Allowed)
	assert.Equal(t, gormdb.DomainOwnerModeWarn, decision.Mode)
	require.NotNil(t, decision.Warning)
	assert.Equal(t, DomainWriteWarningCrossOwner, decision.Warning.Code)
	assert.Equal(t, "agent/alice", decision.Owner.Principal)
	assert.Equal(t, AuditStatusWritten, decision.AuditStatus)
	require.Len(t, audit.entries, 1)
	assert.Equal(t, AuditActionDomainWriteWarn, audit.entries[0].Action)
	assert.Equal(t, "agent/bob", audit.entries[0].Actor)
	assert.Equal(t, "session-99", audit.entries[0].SourceSessionID)
	assert.Equal(t, AuditReasonDomainCrossOwnerWarn, audit.entries[0].Reason)
}

func TestDomainRegistryService_RejectAuditsAndDeniesCrossOwner(t *testing.T) {
	audit := &fakeAuditLogger{}
	svc := NewDomainRegistryService(&fakeDomainOwnerStore{
		rows: map[string]*gormdb.DomainOwner{
			"memory-lab": {
				Domain:             "memory-lab",
				OwnerPrincipal:     "agent/alice",
				OwnerPrincipalKind: "agent",
				Mode:               gormdb.DomainOwnerModeReject,
			},
		},
	}, audit)

	decision, err := svc.CheckWrite(context.Background(), DomainWriteCheckRequest{
		Domain: "memory-lab",
		Writer: PrincipalRef{Principal: "agent/bob", PrincipalKind: "agent"},
	})
	require.ErrorIs(t, err, ErrDomainWriteRejected)
	require.NotNil(t, decision)
	assert.False(t, decision.Allowed)
	assert.Equal(t, gormdb.DomainOwnerModeReject, decision.Mode)
	assert.Equal(t, DomainWriteReasonCrossOwnerRejected, decision.Reason)
	assert.Equal(t, AuditStatusWritten, decision.AuditStatus)
	require.Len(t, audit.entries, 1)
	assert.Equal(t, AuditActionDomainWriteReject, audit.entries[0].Action)
	assert.Equal(t, AuditReasonDomainCrossOwnerReject, audit.entries[0].Reason)
}

func TestDomainRegistryService_InvalidPrincipalKindRejectsBeforeStore(t *testing.T) {
	store := &fakeDomainOwnerStore{}
	svc := NewDomainRegistryService(store, &fakeAuditLogger{})

	decision, err := svc.CheckWrite(context.Background(), DomainWriteCheckRequest{
		Domain: "memory-lab",
		Writer: PrincipalRef{Principal: "agent/bob", PrincipalKind: "daemon"},
	})
	require.Error(t, err)
	assert.Nil(t, decision)
	assert.Equal(t, 0, store.calls, "invalid writer kind must be rejected before store lookup")
}

func TestDomainRegistryService_AuditFailureFailsClosedForWarnAndReject(t *testing.T) {
	for _, tc := range []struct {
		name string
		mode string
	}{
		{name: "warn", mode: gormdb.DomainOwnerModeWarn},
		{name: "reject", mode: gormdb.DomainOwnerModeReject},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc := NewDomainRegistryService(&fakeDomainOwnerStore{
				rows: map[string]*gormdb.DomainOwner{
					"memory-lab": {
						Domain:             "memory-lab",
						OwnerPrincipal:     "agent/alice",
						OwnerPrincipalKind: "agent",
						Mode:               tc.mode,
					},
				},
			}, &fakeAuditLogger{err: errors.New("audit unavailable")})

			decision, err := svc.CheckWrite(context.Background(), DomainWriteCheckRequest{
				Domain: "memory-lab",
				Writer: PrincipalRef{Principal: "agent/bob", PrincipalKind: "agent"},
			})
			require.Error(t, err)
			assert.Nil(t, decision, "warn/reject paths fail closed when durable audit cannot be written")
		})
	}
}

type fakeDomainOwnerStore struct {
	rows  map[string]*gormdb.DomainOwner
	err   error
	calls int
}

func (f *fakeDomainOwnerStore) Get(ctx context.Context, domain string) (*gormdb.DomainOwner, error) {
	f.calls++
	if f.err != nil {
		return nil, f.err
	}
	row, ok := f.rows[domain]
	if !ok {
		return nil, gormlib.ErrRecordNotFound
	}
	cp := *row
	return &cp, nil
}
