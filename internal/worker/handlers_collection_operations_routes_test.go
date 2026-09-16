package worker

import (
	"net/http"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/require"
)

func TestCollectionOperationRoutesStayDomainBound(t *testing.T) {
	service := &Service{router: chi.NewRouter()}
	service.setupRoutes()

	routes := make(map[string]map[string]bool)
	require.NoError(t, chi.Walk(service.router, func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		if routes[route] == nil {
			routes[route] = make(map[string]bool)
		}
		routes[route][method] = true
		return nil
	}))

	for _, route := range []string{
		"/api/issues/selection",
		"/api/issues/selection/current",
		"/api/issues/selection/page",
		"/api/issues/operations",
		"/api/memories/selection",
		"/api/memories/selection/current",
		"/api/memories/selection/page",
		"/api/memories/operations",
		"/api/memory/candidates/operations",
		"/api/documents",
		"/api/rules",
		"/api/code/tabs/handshake",
	} {
		require.Truef(t, routes[route][http.MethodPost], "missing POST %s", route)
	}

	for _, route := range []string{
		"/api/collections/operations",
		"/api/collections/{domain}/operations",
		"/api/collection/operations",
	} {
		require.Falsef(t, routes[route][http.MethodPost], "generic collection operation route must not exist: %s", route)
	}
}
