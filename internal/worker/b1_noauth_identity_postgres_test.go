package worker

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestB1AuthDisabledDoesNotInventSelfPostgres(t *testing.T) {
	f := newOwnCardContextFixture(t, true)
	before := f.state(t)
	response := f.call(t, http.MethodPost, "/api/memories", ownCardContextPayload("synthetic no-auth must not create self"), ownCardContextCaller{})
	require.Contains(t, []int{http.StatusUnauthorized, http.StatusForbidden}, response.Code)
	require.Equal(t, before, f.state(t))
}
