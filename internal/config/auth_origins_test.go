package config

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestAuthAllowedOriginsLoad(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	for _, tc := range []struct {
		name, value string
		want        []string
		invalid     bool
	}{
		{name: "empty default"},
		{name: "exact public origin", value: "https://console.example.test", want: []string{"https://console.example.test"}},
		{name: "multiple exact origins", value: " https://console.example.test, http://localhost:8080 ", want: []string{"https://console.example.test", "http://localhost:8080"}},
		{name: "IPv6 origin", value: "https://[2001:db8::1]:8443", want: []string{"https://[2001:db8::1]:8443"}},
		{name: "wildcard", value: "https://*.example.test", invalid: true},
		{name: "opaque", value: "null", invalid: true},
		{name: "path", value: "https://console.example.test/path", invalid: true},
		{name: "trailing slash", value: "https://console.example.test/", invalid: true},
		{name: "userinfo", value: "https://person@console.example.test", invalid: true},
		{name: "query", value: "https://console.example.test?query", invalid: true},
		{name: "fragment", value: "https://console.example.test#fragment", invalid: true},
		{name: "non HTTP scheme", value: "ftp://console.example.test", invalid: true},
		{name: "empty host", value: "https://", invalid: true},
		{name: "invalid port", value: "https://console.example.test:bad", invalid: true},
		{name: "out of range port", value: "https://console.example.test:65536", invalid: true},
		{name: "invalid IPv6", value: "https://[not-an-address]", invalid: true},
		{name: "empty list entry", value: "https://console.example.test,", invalid: true},
		{name: "mixed valid and invalid", value: "https://console.example.test,null", invalid: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("ENGRAM_AUTH_ALLOWED_ORIGINS", tc.value)
			cfg, err := Load()
			if tc.invalid {
				require.ErrorContains(t, err, "ENGRAM_AUTH_ALLOWED_ORIGINS")
				require.Nil(t, cfg, "bad policy must not return a partially admitted list")
				return
			}
			require.NoError(t, err)
			body, err := json.Marshal(cfg)
			require.NoError(t, err)
			var projection struct {
				Origins []string `json:"auth_allowed_origins"`
			}
			require.NoError(t, json.Unmarshal(body, &projection))
			require.Equal(t, tc.want, projection.Origins)
		})
	}
}

func TestAuthAllowedOriginsReloadRejectsInvalidConfig(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("ENGRAM_AUTH_ALLOWED_ORIGINS", "https://*.example.test")
	cfg, changed, err := Reload()
	require.ErrorContains(t, err, "ENGRAM_AUTH_ALLOWED_ORIGINS")
	require.Nil(t, cfg)
	require.Empty(t, changed)
}
