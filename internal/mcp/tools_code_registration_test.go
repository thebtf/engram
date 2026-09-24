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
	calls []uci.ResolveContextInput
}

func (app *registrationContextApplication) RegisterLocalGit(_ context.Context, input uci.ResolveContextInput, sourceID, label, locator string) (uci.RegisteredCheckoutSelector, error) {
	app.calls = append(app.calls, input)
	if label == "" && sourceID != uciCodebaseContextTestSource || locator != "file:///private/checkout-a" {
		return uci.RegisteredCheckoutSelector{}, codebaseContextClosedError(uci.PermissionDenied)
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

	foreign := auth.WithIdentity(ContextWithSession(context.Background(), "foreign-session"), auth.ClientWithPrincipal("read-write", "keycard-99", "browser-user/99", auth.PrincipalKindAgent))
	denied := callUCICodebaseContext(t, fixture.server, foreign, map[string]any{"action": "register", "source_id": uciCodebaseContextTestSource, "locator": "file:///private/checkout-a"})
	require.NotNil(t, denied.Error)
	readOnly := auth.WithIdentity(ContextWithSession(context.Background(), "readonly-session"), auth.ClientWithPrincipal("read-only", "keycard-ro", "browser-user/41", auth.PrincipalKindHuman))
	readOnlyDenied := callUCICodebaseContext(t, fixture.server, readOnly, map[string]any{"action": "register", "source_id": uciCodebaseContextTestSource, "locator": "file:///private/checkout-a"})
	require.NotNil(t, readOnlyDenied.Error)
	require.Len(t, application.calls, 1)
}
