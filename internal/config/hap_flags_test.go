package config

import "testing"

func TestHAP01BFlagsDefaultOffAndRevisionGated(t *testing.T) {
	t.Setenv(EnvHAP01BRelayEnabled, "")
	t.Setenv(EnvHAP01BRelayRevision, "omp-hap-01b/1")
	t.Setenv(EnvHAP01BLegacyDirectEnforcement, "")
	if HAP01BRelayEnabled() || HAP01BRelayRevision() != "" {
		t.Fatal("relay enablement helpers must remain dark by default")
	}
	if HAP01BRelayRevisionEnabled("omp-hap-01b/1") {
		t.Fatal("relay must remain disabled by default")
	}
	if HAP01BLegacyDirectEnforcementEnabled() {
		t.Fatal("legacy direct enforcement must remain disabled by default")
	}

	t.Setenv(EnvHAP01BRelayEnabled, "true")
	if !HAP01BRelayEnabled() || HAP01BRelayRevision() != "omp-hap-01b/1" {
		t.Fatal("enabled relay must expose only its exact configured revision")
	}
	if !HAP01BRelayRevisionEnabled("omp-hap-01b/1") {
		t.Fatal("enabled relay must accept its exact configured revision")
	}
	if HAP01BRelayRevisionEnabled("omp-hap-01b/2") {
		t.Fatal("enabled relay must reject another revision")
	}

	t.Setenv(EnvHAP01BLegacyDirectEnforcement, "true")
	if !HAP01BLegacyDirectEnforcementEnabled() {
		t.Fatal("explicit legacy direct enforcement flag must enable")
	}
}
