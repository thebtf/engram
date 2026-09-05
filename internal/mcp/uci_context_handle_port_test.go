package mcp

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"github.com/thebtf/engram/internal/auditcontext"
	"github.com/thebtf/engram/internal/auth"
	"github.com/thebtf/engram/internal/uci"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const (
	uciHandleTestSession     = "uci-handle-session"
	uciHandleTestSource      = "11111111-1111-5111-8111-111111111111"
	uciHandleTestCheckout    = "22222222-2222-5222-8222-222222222222"
	uciHandleTestIncarnation = "33333333-3333-5333-8333-333333333333"
	uciHandleTestProfile     = "44444444-4444-5444-8444-444444444444"
	uciHandleTestView        = "55555555-5555-5555-8555-555555555555"
	uciHandleTestWorkstation = "uci-handle-keycard"
	uciHandleTestPrincipal   = "agent/uci-handle"
	uciHandleTestLocalRoot   = "uci-handle-root"
)

type uciHandleCatalogFake struct {
	binding uci.IndexBinding
	calls   []uci.IndexBindingSelector
}

func (fake *uciHandleCatalogFake) LoadIndexBinding(_ context.Context, selector uci.IndexBindingSelector) (uci.IndexBinding, error) {
	fake.calls = append(fake.calls, selector.Clone())
	if ref, pinned := selector.Context(); pinned {
		if fake.binding.Context == nil || !uciHandleRefsEqual(ref, *fake.binding.Context) {
			return uci.IndexBinding{}, errors.New("unexpected view selector")
		}
	} else {
		checkout, following := selector.Checkout()
		if !following || checkout.Scope != fake.binding.Scope || checkout.ProfileID != fake.binding.ProfileID {
			return uci.IndexBinding{}, errors.New("unexpected checkout selector")
		}
	}
	return fake.binding.Clone(), nil
}

var _ uci.IndexBindingCatalog = (*uciHandleCatalogFake)(nil)

type uciHandleAuthorizerFake struct {
	allowed bool
	calls   []uci.ContextAccess
}

func (fake *uciHandleAuthorizerFake) AuthorizeContext(_ context.Context, access uci.ContextAccess) error {
	fake.calls = append(fake.calls, access)
	if !fake.allowed {
		return errors.New("fixture authorization denied")
	}
	return nil
}

var _ uci.ContextAuthorizer = (*uciHandleAuthorizerFake)(nil)

func TestUCIContextHandlePortReauthorizesOpaqueViewHandle(t *testing.T) {
	binding := uciHandleTestBinding(t, true)
	catalog := &uciHandleCatalogFake{binding: binding}
	authorizer := &uciHandleAuthorizerFake{allowed: true}
	port, err := NewUCIContextHandlePort(NewServer(ServerOptions{Version: "uci-handle-test"}), catalog, authorizer)
	require.NoError(t, err)
	ctx := uciHandleTestContext()

	handle, err := port.IssueCodeContextHandle(ctx, uciHandleTestSession, binding)
	require.NoError(t, err)
	require.NotEmpty(t, handle)
	require.NotContains(t, handle, binding.Scope.SourceID)
	require.NotContains(t, handle, binding.Scope.CheckoutID)

	got, err := port.AuthorizeCodeContextHandle(ctx, uciHandleTestSession, handle)
	require.NoError(t, err)
	require.Equal(t, binding, got)
	require.Len(t, catalog.calls, 2, "issue and use must each reload durable binding")
	require.Len(t, authorizer.calls, 2, "issue and use must each reauthorize")

	authorizer.allowed = false
	_, err = port.AuthorizeCodeContextHandle(ctx, uciHandleTestSession, handle)
	require.Equal(t, codes.PermissionDenied, status.Code(err))
	require.Equal(t, string(uci.PermissionDenied), status.Convert(err).Message())

	_, err = port.AuthorizeCodeContextHandle(ctx, "foreign-session", handle)
	require.Equal(t, codes.FailedPrecondition, status.Code(err))
	require.Equal(t, string(uci.ContextMismatch), status.Convert(err).Message())
}

func TestUCIContextHandlePortPreservesBootstrapSelectionUntilPublication(t *testing.T) {
	bootstrap := uciHandleTestBinding(t, false)
	catalog := &uciHandleCatalogFake{binding: bootstrap}
	authorizer := &uciHandleAuthorizerFake{allowed: true}
	port, err := NewUCIContextHandlePort(NewServer(ServerOptions{Version: "uci-bootstrap-handle-test"}), catalog, authorizer)
	require.NoError(t, err)
	ctx := uciHandleTestContext()

	handle, err := port.IssueCodeContextHandle(ctx, uciHandleTestSession, bootstrap)
	require.NoError(t, err)
	got, err := port.AuthorizeCodeIndexScope(ctx, uciHandleTestSession, bootstrap.Scope, bootstrap.ProfileID)
	require.NoError(t, err)
	require.Equal(t, bootstrap, got)

	published := bootstrap.Clone()
	published.Context = &uci.ContextRef{
		SourceID:          bootstrap.Scope.SourceID,
		CheckoutID:        bootstrap.Scope.CheckoutID,
		ViewID:            uuid.NewString(),
		AnalysisProfileID: bootstrap.ProfileID,
		Generation:        1,
	}
	catalog.binding = published
	got, err = port.AuthorizeCodeContextHandle(ctx, uciHandleTestSession, handle)
	require.NoError(t, err)
	require.Equal(t, published, got, "a checkout selector follows its fixed checkout into the first published View")
}

func TestUCIContextHandlePortRejectsCheckoutScopeOrProfileDrift(t *testing.T) {
	bootstrap := uciHandleTestBinding(t, false)
	catalog := &uciHandleCatalogFake{binding: bootstrap}
	authorizer := &uciHandleAuthorizerFake{allowed: true}
	port, err := NewUCIContextHandlePort(NewServer(ServerOptions{Version: "uci-handle-drift-test"}), catalog, authorizer)
	require.NoError(t, err)
	ctx := uciHandleTestContext()

	handle, err := port.IssueCodeContextHandle(ctx, uciHandleTestSession, bootstrap)
	require.NoError(t, err)

	incarnationDrift := bootstrap.Clone()
	incarnationDrift.Scope.IncarnationID = uuid.NewString()
	catalog.binding = incarnationDrift
	_, err = port.AuthorizeCodeContextHandle(ctx, uciHandleTestSession, handle)
	require.Equal(t, codes.FailedPrecondition, status.Code(err))
	require.Equal(t, string(uci.ContextMismatch), status.Convert(err).Message())

	catalog.binding = bootstrap.Clone()
	profileDrift := bootstrap.Clone()
	profileDrift.ProfileID = uuid.NewString()
	catalog.binding = profileDrift
	_, err = port.AuthorizeCodeContextHandle(ctx, uciHandleTestSession, handle)
	require.Equal(t, codes.FailedPrecondition, status.Code(err))
	require.Equal(t, string(uci.ContextMismatch), status.Convert(err).Message())
}

func uciHandleTestBinding(t *testing.T, withView bool) uci.IndexBinding {
	t.Helper()
	binding := uci.IndexBinding{
		Scope: uci.IndexScope{
			SourceID:      uciHandleTestSource,
			CheckoutID:    uciHandleTestCheckout,
			IncarnationID: uciHandleTestIncarnation,
		},
		ProfileID:     uciHandleTestProfile,
		LocalRootID:   uciHandleTestLocalRoot,
		WorkstationID: uciHandleTestWorkstation,
	}
	if withView {
		binding.Context = &uci.ContextRef{
			SourceID:          binding.Scope.SourceID,
			CheckoutID:        binding.Scope.CheckoutID,
			ViewID:            uciHandleTestView,
			AnalysisProfileID: binding.ProfileID,
			Generation:        1,
		}
	}
	require.NoError(t, binding.Validate())
	return binding
}

func uciHandleTestContext() context.Context {
	identity := auth.ClientWithPrincipal("read-write", uciHandleTestWorkstation, uciHandleTestPrincipal, auth.PrincipalKindAgent)
	return auditcontext.WithSourceSession(auth.WithIdentity(context.Background(), identity), uciHandleTestSession)
}

func uciHandleRefsEqual(left, right uci.ContextRef) bool {
	if (left.SpaceID == nil) != (right.SpaceID == nil) {
		return false
	}
	if left.SpaceID != nil && *left.SpaceID != *right.SpaceID {
		return false
	}
	return left.SourceID == right.SourceID &&
		left.CheckoutID == right.CheckoutID &&
		left.ViewID == right.ViewID &&
		left.AnalysisProfileID == right.AnalysisProfileID &&
		left.Generation == right.Generation
}
