package worker

import (
	"net/http"
	"testing"
)

func TestDisabledIdentityRouteBoundaries(t *testing.T) {
	cases := []struct {
		method, path           string
		memory, administration bool
	}{
		{http.MethodPost, "/api/memories", true, false},
		{http.MethodGet, "/api/memories/42", true, false},
		{http.MethodGet, "/api/memories/+42", true, false},
		{http.MethodPatch, "/api/memories/42", true, false},
		{http.MethodGet, "/api/memories/42/revisions", true, false},
		{http.MethodPost, "/api/auth/principals", false, true},
		{http.MethodPost, "/api/auth/tokens", false, true},
		{http.MethodGet, "/api/memories/principal", false, false},
		{http.MethodPost, "/api/memories/suppress", false, false},
		{http.MethodGet, "/api/memories/42/audit", false, false},
		{http.MethodPatch, "/api/memories/42/revisions", false, false},
		{http.MethodGet, "/api/memories/42/revisions/extra", false, false},
		{http.MethodDelete, "/api/memories/42", false, false},
		{http.MethodGet, "/api/memories/0", false, false},
		{http.MethodGet, "/api/memories/-1", false, false},
		{http.MethodGet, "/api/auth/tokens", false, false},
		{http.MethodGet, "/api/auth/me", false, false},
	}
	for _, tc := range cases {
		t.Run(tc.method+" "+tc.path, func(t *testing.T) {
			memory, administration := disabledIdentityRoute(tc.method, tc.path)
			if memory != tc.memory || administration != tc.administration {
				t.Errorf("got (%t, %t), want (%t, %t)", memory, administration, tc.memory, tc.administration)
			}
		})
	}
}
