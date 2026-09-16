package config

import (
	"os"
	"strconv"
	"strings"
)

const (
	// EnvHAP01BRelayEnabled opens the private bridge RPC surface. It defaults
	// off so existing gRPC callers retain their previous behavior.
	EnvHAP01BRelayEnabled = "ENGRAM_HAP_01B_RELAY_ENABLED"

	// EnvHAP01BRelayRevision is the exact installed relay revision accepted by
	// the server while the private bridge surface is enabled.
	EnvHAP01BRelayRevision = "ENGRAM_HAP_01B_RELAY_REVISION"

	// EnvHAP01BLegacyDirectEnforcement requires an expiring legacy-direct
	// keycard for the three old compatibility delivery routes. It defaults off.
	EnvHAP01BLegacyDirectEnforcement = "ENGRAM_HAP_01B_LEGACY_DIRECT_ENFORCEMENT"

	// EnvHAP01BAdapterSHA256 is the exact built extension digest accepted as a
	// version-skew attestation. It is not caller authentication.
	EnvHAP01BAdapterSHA256 = "ENGRAM_HAP_01B_ADAPTER_SHA256"

	// EnvHAP01BRegistrationToken is passed only to the MCP/daemon child and is
	// accepted only by relay V3 registration.
	EnvHAP01BRegistrationToken = "ENGRAM_HAP_01B_REGISTRATION_TOKEN"

	// EnvHAP01BProjectTokensJSON maps server-resolved canonical project keys to
	// child-only project service keycards.
	EnvHAP01BProjectTokensJSON = "ENGRAM_HAP_01B_PROJECT_TOKENS_JSON"
)

func hapBoolEnv(name string) bool {
	value, err := strconv.ParseBool(strings.TrimSpace(os.Getenv(name)))
	return err == nil && value
}

// HAP01BRelayEnabled reports whether the private listener may be constructed.
func HAP01BRelayEnabled() bool { return hapBoolEnv(EnvHAP01BRelayEnabled) }

// HAP01BRelayRevision returns the configured exact revision, or empty when the
// bridge is not enabled or the revision is absent.
func HAP01BRelayRevision() string {
	if !HAP01BRelayEnabled() {
		return ""
	}
	return strings.TrimSpace(os.Getenv(EnvHAP01BRelayRevision))
}

// HAP01BRelayRevisionEnabled reports whether a private bridge request carrying
// revision is admitted by the explicitly enabled, exact-revision gate.
func HAP01BRelayRevisionEnabled(revision string) bool {
	if !hapBoolEnv(EnvHAP01BRelayEnabled) {
		return false
	}
	expected := strings.TrimSpace(os.Getenv(EnvHAP01BRelayRevision))
	return expected != "" && revision == expected
}

// HAP01BLegacyDirectEnforcementEnabled reports whether old direct delivery
// endpoints must require a scoped, expiring legacy-direct keycard.
func HAP01BLegacyDirectEnforcementEnabled() bool {
	return hapBoolEnv(EnvHAP01BLegacyDirectEnforcement)
}
