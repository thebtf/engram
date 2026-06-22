package scope

import (
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/thebtf/engram/pkg/models"
)

func TestDomainOwnershipPolicy_EmptyDomainAllowsLegacyBehavior(t *testing.T) {
	t.Parallel()
	policy := DomainOwnershipPolicy{}

	decision := policy.Decide(KeycardContext{}, domainPolicyRequest(DomainOperationRead, "  ", "", ""))

	require.True(t, decision.Allowed)
	require.Equal(t, DomainPolicyReasonEmptyDomain, decision.Reason)
}

func TestDomainOwnershipPolicy_NonEmptyDomainAllowsSamePrincipal(t *testing.T) {
	t.Parallel()
	policy := DomainOwnershipPolicy{}
	caller := domainPolicyCaller("agent/alice")

	for _, operation := range []DomainOperation{DomainOperationRead, DomainOperationManage} {
		decision := policy.Decide(caller, domainPolicyRequest(operation, "memory-lab", "agent/alice", "agent"))

		require.True(t, decision.Allowed, "operation %s should allow same principal", operation)
		require.Equal(t, DomainPolicyReasonAllowed, decision.Reason)
	}
}

func TestDomainOwnershipPolicy_NonEmptyDomainWriteRequiresPrincipal(t *testing.T) {
	t.Parallel()
	policy := DomainOwnershipPolicy{}

	decision := policy.Decide(KeycardContext{}, domainPolicyRequest(DomainOperationWrite, "memory-lab", "", ""))

	require.False(t, decision.Allowed)
	require.Equal(t, DomainPolicyReasonMissingCallerPrincipal, decision.Reason)
}

func TestDomainOwnershipPolicy_NonEmptyDomainWriteAllowsValidPrincipal(t *testing.T) {
	t.Parallel()
	policy := DomainOwnershipPolicy{}

	decision := policy.Decide(domainPolicyCaller("agent/alice"), domainPolicyRequest(DomainOperationWrite, "memory-lab", "", ""))

	require.True(t, decision.Allowed)
	require.Equal(t, DomainPolicyReasonAllowed, decision.Reason)
}

func TestDomainOwnershipPolicy_NonEmptyDomainWriteRejectsInvalidPrincipalKind(t *testing.T) {
	t.Parallel()
	policy := DomainOwnershipPolicy{}
	caller := KeycardContext{Principal: "agent/alice", PrincipalKind: "bogus"}

	decision := policy.Decide(caller, domainPolicyRequest(DomainOperationWrite, "memory-lab", "", ""))

	require.False(t, decision.Allowed)
	require.Equal(t, DomainPolicyReasonInvalidPrincipalKind, decision.Reason)
}

func TestDomainOwnershipPolicy_NonEmptyDomainReadRequiresOwner(t *testing.T) {
	t.Parallel()
	policy := DomainOwnershipPolicy{}
	caller := domainPolicyCaller("agent/alice")

	decision := policy.Decide(caller, domainPolicyRequest(DomainOperationRead, "memory-lab", "", ""))

	require.False(t, decision.Allowed)
	require.Equal(t, DomainPolicyReasonMissingOwner, decision.Reason)
}

func TestDomainOwnershipPolicy_NonEmptyDomainReadRejectsMismatchedPrincipal(t *testing.T) {
	t.Parallel()
	policy := DomainOwnershipPolicy{}
	caller := domainPolicyCaller("agent/alice")

	decision := policy.Decide(caller, domainPolicyRequest(DomainOperationRead, "memory-lab", "agent/bob", "agent"))

	require.False(t, decision.Allowed)
	require.Equal(t, DomainPolicyReasonOwnerMismatch, decision.Reason)
}

func TestDomainOwnershipPolicy_NonEmptyDomainReadRejectsInvalidOwnerKind(t *testing.T) {
	t.Parallel()
	policy := DomainOwnershipPolicy{}
	caller := domainPolicyCaller("agent/alice")

	decision := policy.Decide(caller, domainPolicyRequest(DomainOperationRead, "memory-lab", "agent/alice", "bogus"))

	require.False(t, decision.Allowed)
	require.Equal(t, DomainPolicyReasonInvalidPrincipalKind, decision.Reason)
}

func TestResolveMemory_NonEmptyDomainCannotBypassPrincipalPrivate(t *testing.T) {
	t.Parallel()
	mem := principalMemory(201, "agent/bob", models.AgentVisibilityPrivate)
	mem.Domain = "memory-lab"

	require.False(t, ResolveMemory(
		domainPolicyCaller("agent/alice"),
		mem,
		MemoryVisibilityOptions{},
	), "domain metadata must not make another principal's private row visible")
}

func TestResolveMemory_NonEmptyDomainRequiresOwnerMatchEvenWhenShared(t *testing.T) {
	t.Parallel()
	mem := principalMemory(202, "agent/bob", models.AgentVisibilityShared)
	mem.Domain = "memory-lab"

	require.False(t, ResolveMemory(
		domainPolicyCaller("agent/alice"),
		mem,
		MemoryVisibilityOptions{},
	), "domain-owned shared rows still require owner match")

	require.True(t, ResolveMemory(
		domainPolicyCaller("agent/bob"),
		mem,
		MemoryVisibilityOptions{},
	), "own domain-owned shared rows remain visible")
}

func domainPolicyCaller(principal string) KeycardContext {
	return KeycardContext{Principal: principal, PrincipalKind: "agent"}
}

func domainPolicyRequest(operation DomainOperation, domain, owner, ownerKind string) DomainPolicyRequest {
	return DomainPolicyRequest{
		Operation:          operation,
		Domain:             domain,
		OwnerPrincipal:     owner,
		OwnerPrincipalKind: ownerKind,
	}
}
