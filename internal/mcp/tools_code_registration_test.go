package mcp

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/thebtf/engram/internal/auth"
	"github.com/thebtf/engram/internal/uci"
)

type registrationContextApplication struct {
	*uciCodebaseContextApplicationFake
	calls         []uci.ResolveContextInput
	parserBundles []*bool
}

func (app *registrationContextApplication) RegisterLocalGit(_ context.Context, input uci.ResolveContextInput, sourceID, label, locator string, parserBundle *bool) (uci.RegisteredCheckoutSelector, error) {
	app.calls = append(app.calls, input)
	app.parserBundles = append(app.parserBundles, parserBundle)
	if sourceID == "not-a-uuid" {
		return uci.RegisteredCheckoutSelector{}, uci.NewContextError(uci.ContextMismatch, nil)
	}
	if label == "" && sourceID != uciCodebaseContextTestSource || locator != "file:///private/checkout-a" {
		return uci.RegisteredCheckoutSelector{}, uci.NewContextError(uci.PermissionDenied, nil)
	}
	binding := uci.IndexBinding{
		Scope:     uci.IndexScope{SourceID: uciCodebaseContextTestSource, CheckoutID: uciCodebaseContextTestCheckoutA, IncarnationID: uciCodebaseContextTestIncarnationA},
		ProfileID: uciCodebaseContextTestProfile, LocalRootID: locator, WorkstationID: input.WorkstationID,
	}
	app.catalog.bindings[binding.Scope.CheckoutID] = binding
	app.authorizer.allowed[input.Principal] = map[string]bool{binding.Scope.CheckoutID: true}
	return uci.RegisteredCheckoutSelector{Scope: binding.Scope, ProfileID: binding.ProfileID}, nil
}

func TestRegisterCodebaseContextIssuesNoViewTargetOnlyForAuthenticatedOwner(t *testing.T) {
	fixture := newUCICodebaseContextFixture(t)
	application := &registrationContextApplication{uciCodebaseContextApplicationFake: fixture.application}
	fixture.server.SetCodebaseContextApplication(application)
	owner := auth.WithIdentity(ContextWithSession(context.Background(), "new-owner-session"), auth.ClientWithPrincipal("read-write", "keycard-41", "browser-user/41", auth.PrincipalKindHuman))
	response := callUCICodebaseContext(t, fixture.server, owner, map[string]any{"action": "register", "source_label": "engram", "locator": "file:///private/checkout-a"})
	payload := decodeUCICodebaseContextResponse(t, response)
	require.Equal(t, "checkout", payload["binding_kind"])
	require.Equal(t, uciCodebaseContextTestSource, payload["source_id"])
	require.Equal(t, uciCodebaseContextTestCheckoutA, payload["checkout_id"])
	require.Nil(t, payload["context"])
	require.NotEmpty(t, payload["context_handle"])
	recorded, err := json.Marshal(payload)
	require.NoError(t, err)
	require.NotContains(t, string(recorded), "/private/")
	require.Len(t, application.calls, 1)
	require.Equal(t, "browser-user/41", application.calls[0].Principal)
	require.Equal(t, "keycard-41", application.calls[0].WorkstationID)
	require.Equal(t, []*bool{nil}, application.parserBundles)
	retry := decodeUCICodebaseContextResponse(t, callUCICodebaseContext(t, fixture.server, owner, map[string]any{"action": "register", "source_label": "engram", "locator": "file:///private/checkout-a"}))
	require.Equal(t, payload["checkout_id"], retry["checkout_id"])
	require.Equal(t, payload["context_handle"], retry["context_handle"])
	selected := decodeUCICodebaseContextResponse(t, callUCICodebaseContext(t, fixture.server, owner, map[string]any{"action": "select", "context_handle": retry["context_handle"]}))
	require.Equal(t, retry["checkout_id"], selected["checkout_id"])
	require.Nil(t, selected["context"])
	reconnected := auth.WithIdentity(ContextWithSession(context.Background(), "reconnected-owner-session"), auth.ClientWithPrincipal("read-write", "keycard-41", "browser-user/41", auth.PrincipalKindHuman))
	recovered := decodeUCICodebaseContextResponse(t, callUCICodebaseContext(t, fixture.server, reconnected, map[string]any{"action": "register", "source_label": "engram", "locator": "file:///private/checkout-a"}))
	require.Equal(t, payload["checkout_id"], recovered["checkout_id"])
	require.NotEmpty(t, recovered["context_handle"])
	reconnectedSelection := decodeUCICodebaseContextResponse(t, callUCICodebaseContext(t, fixture.server, reconnected, map[string]any{"action": "select", "context_handle": recovered["context_handle"]}))
	require.Equal(t, payload["checkout_id"], reconnectedSelection["checkout_id"])
	require.Nil(t, reconnectedSelection["context"])
	recoveredBytes, err := json.Marshal(recovered)
	require.NoError(t, err)
	require.NotContains(t, string(recoveredBytes), "/private/")
	malformed := callUCICodebaseContext(t, fixture.server, owner, map[string]any{"action": "register", "source_id": "not-a-uuid", "locator": "file:///private/checkout-a"})
	parserSelection := decodeUCICodebaseContextResponse(t, callUCICodebaseContext(t, fixture.server, owner, map[string]any{"action": "register", "source_label": "engram", "locator": "file:///private/checkout-a", "parser_bundle": true}))
	require.Equal(t, payload["checkout_id"], parserSelection["checkout_id"])
	require.NotNil(t, application.parserBundles[len(application.parserBundles)-1])
	require.True(t, *application.parserBundles[len(application.parserBundles)-1])
	unknownDigest := callUCICodebaseContext(t, fixture.server, owner, map[string]any{"action": "register", "source_label": "engram", "locator": "file:///private/checkout-a", "parser_bundle_digest": "sha256:arbitrary"})
	require.Equal(t, "CONTEXT_MISMATCH", unknownDigest.Error.Data)
	require.Equal(t, "CONTEXT_MISMATCH", malformed.Error.Data)
	forbidden := callUCICodebaseContext(t, fixture.server, owner, map[string]any{"action": "register", "source_id": uciCodebaseContextTestSource, "locator": "file:///private/checkout-a", "checkout_id": uciCodebaseContextTestCheckoutA})
	require.Equal(t, "CONTEXT_MISMATCH", forbidden.Error.Data)

	foreign := auth.WithIdentity(ContextWithSession(context.Background(), "foreign-session"), auth.ClientWithPrincipal("read-write", "keycard-99", "browser-user/99", auth.PrincipalKindAgent))
	denied := callUCICodebaseContext(t, fixture.server, foreign, map[string]any{"action": "register", "source_id": uciCodebaseContextTestSource, "locator": "file:///private/checkout-a"})
	require.NotNil(t, denied.Error)
	readOnly := auth.WithIdentity(ContextWithSession(context.Background(), "readonly-session"), auth.ClientWithPrincipal("read-only", "keycard-ro", "browser-user/41", auth.PrincipalKindHuman))
	readOnlyDenied := callUCICodebaseContext(t, fixture.server, readOnly, map[string]any{"action": "register", "source_id": uciCodebaseContextTestSource, "locator": "file:///private/checkout-a"})
	require.NotNil(t, readOnlyDenied.Error)
	require.Len(t, application.calls, 5)
}
