package scope

import "strings"

// DomainOperation names the server-side action being checked by the domain
// ownership policy.
type DomainOperation string

const (
	DomainOperationWrite  DomainOperation = "write"
	DomainOperationRead   DomainOperation = "read"
	DomainOperationManage DomainOperation = "manage"
)

// DomainPolicyReason is a stable decision code for tests, logs, and adapters.
type DomainPolicyReason string

const (
	DomainPolicyReasonAllowed                DomainPolicyReason = "allowed"
	DomainPolicyReasonEmptyDomain            DomainPolicyReason = "empty_domain"
	DomainPolicyReasonMissingCallerPrincipal DomainPolicyReason = "missing_caller_principal"
	DomainPolicyReasonMissingOwner           DomainPolicyReason = "missing_owner"
	DomainPolicyReasonInvalidPrincipalKind   DomainPolicyReason = "invalid_principal_kind"
	DomainPolicyReasonOwnerMismatch          DomainPolicyReason = "owner_mismatch"
	DomainPolicyReasonUnsupportedOperation   DomainPolicyReason = "unsupported_operation"
)

// DomainPolicyRequest carries only the metadata needed for a domain ownership
// decision. It intentionally stays free of HTTP, MCP, gRPC, and database types.
type DomainPolicyRequest struct {
	Operation          DomainOperation
	Domain             string
	OwnerPrincipal     string
	OwnerPrincipalKind string
}

type DomainPolicyDecision struct {
	Allowed bool
	Reason  DomainPolicyReason
}

// DomainOwnershipPolicy defines CR-005's principal-owned memory-domain rules.
type DomainOwnershipPolicy struct{}

func (DomainOwnershipPolicy) Decide(caller KeycardContext, req DomainPolicyRequest) DomainPolicyDecision {
	switch req.Operation {
	case DomainOperationWrite, DomainOperationRead, DomainOperationManage:
		// supported
	default:
		return DomainPolicyDecision{Allowed: false, Reason: DomainPolicyReasonUnsupportedOperation}
	}

	if strings.TrimSpace(req.Domain) == "" {
		return DomainPolicyDecision{Allowed: true, Reason: DomainPolicyReasonEmptyDomain}
	}

	switch req.Operation {
	case DomainOperationWrite:
		if !hasPrincipal(caller.Principal, caller.PrincipalKind) {
			return DomainPolicyDecision{Allowed: false, Reason: DomainPolicyReasonMissingCallerPrincipal}
		}
		if !isValidPrincipalKind(caller.PrincipalKind) {
			return DomainPolicyDecision{Allowed: false, Reason: DomainPolicyReasonInvalidPrincipalKind}
		}
		return DomainPolicyDecision{Allowed: true, Reason: DomainPolicyReasonAllowed}
	case DomainOperationRead, DomainOperationManage:
		if !hasPrincipal(caller.Principal, caller.PrincipalKind) {
			return DomainPolicyDecision{Allowed: false, Reason: DomainPolicyReasonMissingCallerPrincipal}
		}
		if !hasPrincipal(req.OwnerPrincipal, req.OwnerPrincipalKind) {
			return DomainPolicyDecision{Allowed: false, Reason: DomainPolicyReasonMissingOwner}
		}
		if !isValidPrincipalKind(caller.PrincipalKind) || !isValidPrincipalKind(req.OwnerPrincipalKind) {
			return DomainPolicyDecision{Allowed: false, Reason: DomainPolicyReasonInvalidPrincipalKind}
		}
		if !samePrincipal(caller.Principal, caller.PrincipalKind, req.OwnerPrincipal, req.OwnerPrincipalKind) {
			return DomainPolicyDecision{Allowed: false, Reason: DomainPolicyReasonOwnerMismatch}
		}
		return DomainPolicyDecision{Allowed: true, Reason: DomainPolicyReasonAllowed}
	}

	return DomainPolicyDecision{Allowed: false, Reason: DomainPolicyReasonUnsupportedOperation}
}

func hasPrincipal(principal, kind string) bool {
	return strings.TrimSpace(principal) != "" && strings.TrimSpace(kind) != ""
}

func isValidPrincipalKind(kind string) bool {
	switch strings.TrimSpace(kind) {
	case "human", "agent", "service":
		return true
	default:
		return false
	}
}

func samePrincipal(leftPrincipal, leftKind, rightPrincipal, rightKind string) bool {
	return strings.TrimSpace(leftPrincipal) == strings.TrimSpace(rightPrincipal) &&
		strings.TrimSpace(leftKind) == strings.TrimSpace(rightKind)
}
