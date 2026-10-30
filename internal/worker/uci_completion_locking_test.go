package worker

import (
	"context"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/require"
	gormstore "github.com/thebtf/engram/internal/db/gorm"
	pb "github.com/thebtf/engram/proto/engram/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	gormlib "gorm.io/gorm"
)

func TestUCICompletionGRPCHoldsAuthorityLocksThroughAppend(t *testing.T) {
	fixture := newWorkerUCICompletionAuthorityFixture(t)
	entered, release, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var gate sync.Once
	callbackName := "completion-append-gate-" + uuid.NewString()
	require.NoError(t, fixture.store.GetDB().Callback().Create().Before("gorm:create").Register(callbackName, func(tx *gormlib.DB) {
		if tx.Statement.Table == "uci_completion_evidence" {
			gate.Do(func() { close(entered); <-release })
		}
	}))
	var response *pb.RecordUCICompletionResponse
	var rpcErr error
	go func() {
		defer close(done)
		response, rpcErr = fixture.client.RecordUCICompletion(fixture.callerA, fixture.request(fixture.projectA, fixture.parentA))
	}()
	t.Cleanup(func() {
		select {
		case <-release:
		default:
			close(release)
		}
		<-done
		if err := fixture.store.GetDB().Callback().Create().Remove(callbackName); err != nil {
			t.Errorf("remove completion append gate: %v", err)
		}
	})
	select {
	case <-entered:
	case <-done:
		t.Fatalf("authorized callback did not reach the child append: %v", rpcErr)
	}

	for _, row := range []struct {
		name, query, key string
	}{
		{"keycard revocation", "SELECT 1 FROM api_tokens WHERE id = ? FOR UPDATE NOWAIT", fixture.identityA.KeycardID},
		{"checkout transfer", "SELECT 1 FROM ci_checkouts WHERE checkout_id = ? FOR UPDATE NOWAIT", fixture.refA.CheckoutID},
		{"source retirement", "SELECT 1 FROM sources WHERE source_id = ? FOR UPDATE NOWAIT", fixture.refA.SourceID},
		{"View retirement", "SELECT 1 FROM ci_views WHERE view_id = ? FOR UPDATE NOWAIT", fixture.refA.ViewID},
		{"parent retention", "SELECT 1 FROM uci_exposures WHERE exposure_ref = ? FOR UPDATE NOWAIT", fixture.parentA},
	} {
		t.Run(row.name, func(t *testing.T) {
			tx := fixture.store.GetDB().Begin()
			require.NoError(t, tx.Error)
			var witness int
			lockErr := tx.Raw(row.query, row.key).Scan(&witness).Error
			require.NoError(t, tx.Rollback().Error)
			var databaseErr *pgconn.PgError
			require.ErrorAs(t, lockErr, &databaseErr, "authority must remain row-locked until the child transaction commits")
			require.Equal(t, "55P03", databaseErr.Code, "NOWAIT must observe lock contention, not a fixture or SQL error")
		})
	}
	require.Empty(t, fixture.children(t, fixture.parentA))
	close(release)
	<-done
	require.NoError(t, rpcErr)
	require.NotNil(t, response)
	require.True(t, response.GetAccepted())
	require.Len(t, fixture.children(t, fixture.parentA), 1)
}

func TestUCICompletionGRPCRechecksRevocationAfterAuthentication(t *testing.T) {
	fixture := newWorkerUCICompletionAuthorityFixture(t)
	entered, release, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var gate sync.Once
	callbackName := "completion-authentication-gate-" + uuid.NewString()
	require.NoError(t, fixture.store.GetDB().Callback().Query().After("gorm:query").Register(callbackName, func(tx *gormlib.DB) {
		if tx.Statement.Table == "api_tokens" && tx.Statement.Clauses["FOR"].Expression == nil {
			// The real validator has read the credential row, but has not yet
			// returned its authenticated identity to the gRPC handler.
			gate.Do(func() { close(entered); <-release })
		}
	}))
	var response *pb.RecordUCICompletionResponse
	var rpcErr error
	go func() {
		defer close(done)
		response, rpcErr = fixture.client.RecordUCICompletion(fixture.callerA, fixture.request(fixture.projectA, fixture.parentA))
	}()
	t.Cleanup(func() {
		select {
		case <-release:
		default:
			close(release)
		}
		<-done
		if err := fixture.store.GetDB().Callback().Query().Remove(callbackName); err != nil {
			t.Errorf("remove completion authentication gate: %v", err)
		}
	})
	select {
	case <-entered:
	case <-done:
		t.Fatalf("callback did not traverse the real keycard validator: %v", rpcErr)
	}
	require.NoError(t, gormstore.NewTokenStore(fixture.store).Revoke(context.Background(), fixture.identityA.KeycardID))
	close(release)
	<-done
	require.Nil(t, response)
	require.Equal(t, codes.PermissionDenied, status.Code(rpcErr), "a stale authenticated identity must not survive the transaction's live keycard check")
	require.Empty(t, fixture.children(t, fixture.parentA))
}
