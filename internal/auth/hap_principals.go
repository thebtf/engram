package auth

import (
	"fmt"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// HAPPrincipalClass identifies one credential class reserved for the legacy
// bridge. The three classes are intentionally disjoint: a keycard principal
// names exactly one admission role.
type HAPPrincipalClass string

const (
	HAPPrincipalRegistration HAPPrincipalClass = "registration"
	HAPPrincipalProject      HAPPrincipalClass = "project"
	HAPPrincipalLegacyDirect HAPPrincipalClass = "legacy_direct"
)

const (
	hapRegistrationPrincipalPrefix = "service/engram-daemon-registration/"
	hapProjectPrincipalPrefix      = "service/engram-daemon/"
	hapLegacyDirectPrincipalPrefix = "agent/omp-legacy/"
)

// HAPPrincipal is the parsed, non-authoritative class and subject encoded in
// a dedicated bridge keycard principal.
type HAPPrincipal struct {
	Class   HAPPrincipalClass
	Subject string
}

// RegistrationServicePrincipal returns the registration-only service principal
// for one daemon workstation.
func RegistrationServicePrincipal(workstation string) string {
	return hapRegistrationPrincipalPrefix + workstation
}

// ProjectServicePrincipal returns the service principal scoped to one canonical
// project resolved by the server.
func ProjectServicePrincipal(canonicalProject string) string {
	return hapProjectPrincipalPrefix + canonicalProject
}

// LegacyDirectPrincipal returns the direct-compatibility principal scoped to
// one canonical project. Issuance requires this class to carry an expiry.
func LegacyDirectPrincipal(canonicalProject string) string {
	return hapLegacyDirectPrincipalPrefix + canonicalProject
}

// ParseHAPPrincipal recognizes only complete bridge principal shapes. It does
// not grant authority: callers must additionally check the principal kind,
// source, expiry where required, and the resolved project match.
func ParseHAPPrincipal(principal string) (HAPPrincipal, bool) {
	for _, candidate := range []struct {
		prefix string
		class  HAPPrincipalClass
	}{
		{hapRegistrationPrincipalPrefix, HAPPrincipalRegistration},
		{hapProjectPrincipalPrefix, HAPPrincipalProject},
		{hapLegacyDirectPrincipalPrefix, HAPPrincipalLegacyDirect},
	} {
		if !strings.HasPrefix(principal, candidate.prefix) {
			continue
		}
		subject := strings.TrimPrefix(principal, candidate.prefix)
		if !validHAPPrincipalSubject(subject) {
			return HAPPrincipal{}, false
		}
		return HAPPrincipal{Class: candidate.class, Subject: subject}, true
	}
	return HAPPrincipal{}, false
}

func validHAPPrincipalSubject(subject string) bool {
	if subject == "" || len(subject) > 256 || !utf8.ValidString(subject) || strings.TrimSpace(subject) != subject {
		return false
	}
	for _, character := range subject {
		if unicode.IsControl(character) || unicode.IsSpace(character) || character == '/' || character == '\\' || character == '@' {
			return false
		}
	}
	return true
}

func hasHAPPrincipalNamespace(principal string) bool {
	return strings.HasPrefix(principal, hapRegistrationPrincipalPrefix) ||
		strings.HasPrefix(principal, hapProjectPrincipalPrefix) ||
		strings.HasPrefix(principal, hapLegacyDirectPrincipalPrefix)
}

func hapPrincipalKind(class HAPPrincipalClass) PrincipalKind {
	switch class {
	case HAPPrincipalRegistration, HAPPrincipalProject:
		return PrincipalKindService
	case HAPPrincipalLegacyDirect:
		return PrincipalKindAgent
	default:
		return ""
	}
}

// ValidateHAPPrincipalForIssuance checks bridge-only keycard invariants while
// leaving existing general principals untouched. A legacy direct keycard is
// usable only when its expiry is strictly in the future.
func ValidateHAPPrincipalForIssuance(principal string, kind PrincipalKind, expiresAt *time.Time, now time.Time) error {
	if !hasHAPPrincipalNamespace(principal) {
		return nil
	}
	parsed, ok := ParseHAPPrincipal(principal)
	if !ok {
		return fmt.Errorf("invalid HAP principal")
	}
	if kind != hapPrincipalKind(parsed.Class) {
		return fmt.Errorf("invalid HAP principal kind")
	}
	if parsed.Class == HAPPrincipalLegacyDirect {
		if expiresAt == nil {
			return fmt.Errorf("legacy direct keycards require expires_at")
		}
		if !expiresAt.After(now) {
			return fmt.Errorf("legacy direct keycard expires_at must be in the future")
		}
	}
	return nil
}

// HAPPrincipal returns the bridge principal only for a validated client
// keycard whose principal kind is the class's required kind.
func (i Identity) HAPPrincipal() (HAPPrincipal, bool) {
	if i.Source != SourceClient {
		return HAPPrincipal{}, false
	}
	principal, ok := ParseHAPPrincipal(i.Principal)
	if !ok || i.PrincipalKind != hapPrincipalKind(principal.Class) {
		return HAPPrincipal{}, false
	}
	return principal, true
}

// IsHAPRegistrationService reports whether this identity is a registration-only
// daemon service keycard.
func (i Identity) IsHAPRegistrationService() bool {
	principal, ok := i.HAPPrincipal()
	return ok && principal.Class == HAPPrincipalRegistration
}

// IsHAPProjectServiceFor reports whether this identity is a daemon service
// keycard scoped to exactly the server-resolved canonical project.
func (i Identity) IsHAPProjectServiceFor(canonicalProject string) bool {
	principal, ok := i.HAPPrincipal()
	return ok && canonicalProject != "" && principal.Class == HAPPrincipalProject && principal.Subject == canonicalProject
}

// IsHAPLegacyDirectFor reports whether this identity is an unexpired legacy
// direct keycard scoped to exactly the canonical project.
func (i Identity) IsHAPLegacyDirectFor(canonicalProject string, now time.Time) bool {
	principal, ok := i.HAPPrincipal()
	return ok &&
		canonicalProject != "" &&
		principal.Class == HAPPrincipalLegacyDirect &&
		principal.Subject == canonicalProject &&
		i.ExpiresAt != nil &&
		i.ExpiresAt.After(now)
}
