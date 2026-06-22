package principalmemory

import "strings"

const (
	ReasonCrossPrincipalPrivateDenied = "cross_principal_private_denied"
	ReasonInvalidPrincipalKind        = "invalid_principal_kind"
	ReasonInvalidVisibility           = "invalid_visibility"
)

// PrincipalRef identifies a memory owner or caller principal.
type PrincipalRef struct {
	Principal     string
	PrincipalKind string
}

// PrincipalAccessRequest asks whether a caller may see memory attributed to a target principal.
type PrincipalAccessRequest struct {
	Caller        PrincipalRef
	Target        PrincipalRef
	Visibility    string
	CallerIsAdmin bool
}

// PrincipalAccessDecision is intentionally protocol-neutral so REST and MCP adapters share one policy.
type PrincipalAccessDecision struct {
	Allowed       bool
	AuditRequired bool
	Reason        string
}

type PrincipalAccessPolicy struct{}

func (PrincipalAccessPolicy) Decide(req PrincipalAccessRequest) PrincipalAccessDecision {
	target := normalizePrincipalRef(req.Target)
	if target.PrincipalKind != "" && !isValidPrincipalKind(target.PrincipalKind) {
		return deny(ReasonInvalidPrincipalKind)
	}

	caller := normalizePrincipalRef(req.Caller)
	if caller.PrincipalKind != "" && !isValidPrincipalKind(caller.PrincipalKind) {
		return deny(ReasonInvalidPrincipalKind)
	}

	visibility := strings.TrimSpace(strings.ToLower(req.Visibility))
	switch visibility {
	case "", "shared":
		return PrincipalAccessDecision{Allowed: true}
	case "private":
		if caller.matches(target) {
			return PrincipalAccessDecision{Allowed: true}
		}
		if req.CallerIsAdmin {
			return PrincipalAccessDecision{Allowed: true, AuditRequired: true}
		}
		return deny(ReasonCrossPrincipalPrivateDenied)
	default:
		return deny(ReasonInvalidVisibility)
	}
}

func normalizePrincipalRef(ref PrincipalRef) PrincipalRef {
	return PrincipalRef{
		Principal:     strings.TrimSpace(ref.Principal),
		PrincipalKind: strings.TrimSpace(strings.ToLower(ref.PrincipalKind)),
	}
}

func (ref PrincipalRef) matches(other PrincipalRef) bool {
	ref = normalizePrincipalRef(ref)
	other = normalizePrincipalRef(other)
	return ref.Principal != "" &&
		ref.Principal == other.Principal &&
		ref.PrincipalKind == other.PrincipalKind
}

func isValidPrincipalKind(kind string) bool {
	switch strings.TrimSpace(strings.ToLower(kind)) {
	case "human", "agent", "service":
		return true
	default:
		return false
	}
}

func deny(reason string) PrincipalAccessDecision {
	return PrincipalAccessDecision{Reason: reason}
}
