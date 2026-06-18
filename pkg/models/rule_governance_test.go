package models

import (
	"errors"
	"testing"
)

func TestRuleLegalEscapeValidation(t *testing.T) {
	valid := []string{
		RuleEscapeNoData,
		RuleEscapeNull,
		"HYPOTHESIS: likely project scoped",
		"BLOCKED: missing source session",
		"NEEDS CLARIFICATION: which audience?",
	}
	for _, value := range valid {
		if !IsRuleLegalEscape(value) {
			t.Fatalf("expected %q to be a legal escape", value)
		}
	}

	invalid := []string{"", "maybe", "HYPOTHESIS", "BLOCKED", "NEEDS CLARIFICATION"}
	for _, value := range invalid {
		if IsRuleLegalEscape(value) {
			t.Fatalf("expected %q to be rejected as a legal escape", value)
		}
	}
}

func TestRuleVersionTransitionValidation(t *testing.T) {
	req := RuleTransitionRequest{
		Actor:           "agent-test",
		ActorKind:       RuleActorAgent,
		Reason:          "dry-run render passed",
		EvidenceHandles: []string{"evidence:unit-test"},
	}

	if err := ValidateRuleVersionTransition(RuleStateDraft, RuleStateShadow, req); err != nil {
		t.Fatalf("draft -> shadow should be legal: %v", err)
	}

	err := ValidateRuleVersionTransition(RuleStateRejected, RuleStateActiveProject, req)
	if !errors.Is(err, ErrInvalidRuleTransition) {
		t.Fatalf("rejected -> active_project must return ErrInvalidRuleTransition, got %v", err)
	}
}

func TestRuleVersionTransitionRequiresNonBlankEvidence(t *testing.T) {
	req := RuleTransitionRequest{
		Actor:           "agent-test",
		ActorKind:       RuleActorAgent,
		Reason:          "blank evidence should fail",
		EvidenceHandles: []string{"   "},
	}

	err := ValidateRuleVersionTransition(RuleStateDraft, RuleStateShadow, req)
	if !errors.Is(err, ErrRuleRequiredFieldMissing) {
		t.Fatalf("blank evidence handle must return ErrRuleRequiredFieldMissing, got %v", err)
	}
}

func TestRuleAuthorityRejectsBackgroundLLMAndSystemGlobalKernel(t *testing.T) {
	for _, actorKind := range []RuleActorKind{RuleActorBackground, RuleActorLLM, RuleActorSystem} {
		req := RuleTransitionRequest{
			Actor:           string(actorKind) + "-test",
			ActorKind:       actorKind,
			Reason:          "attempted privileged promotion",
			EvidenceHandles: []string{"evidence:unit-test"},
			SnapshotID:      "snap-test",
		}

		err := ValidateRuleActorAuthority(RuleStateActiveShared, RuleStateActiveGlobal, req)
		if !errors.Is(err, ErrRuleAuthorityDenied) {
			t.Fatalf("%s actor must not promote active_global, got %v", actorKind, err)
		}

		err = ValidateRuleActorAuthority(RuleStateActiveGlobal, RuleStateKernel, req)
		if !errors.Is(err, ErrRuleAuthorityDenied) {
			t.Fatalf("%s actor must not promote kernel, got %v", actorKind, err)
		}
	}
}

func TestRuleAuthorityRejectsUnknownActorKind(t *testing.T) {
	req := RuleTransitionRequest{
		Actor:           "unknown-actor-test",
		ActorKind:       RuleActorKind("script"),
		Reason:          "attempted transition with unregistered authority",
		EvidenceHandles: []string{"evidence:unit-test"},
		SnapshotID:      "snap-test",
	}

	err := ValidateRuleVersionTransition(RuleStateDraft, RuleStateShadow, req)
	if !errors.Is(err, ErrRuleAuthorityDenied) {
		t.Fatalf("unknown actor kind must be rejected on ordinary transitions, got %v", err)
	}

	err = ValidateRuleActorAuthority(RuleStateActiveShared, RuleStateActiveGlobal, req)
	if !errors.Is(err, ErrRuleAuthorityDenied) {
		t.Fatalf("unknown actor kind must not promote active_global, got %v", err)
	}
}

func TestRuleSnapshotRequirement(t *testing.T) {
	if RequiresRuleSnapshot(RuleStateDraft, RuleStateShadow) {
		t.Fatal("draft -> shadow must not require a rollback snapshot")
	}
	if !RequiresRuleSnapshot(RuleStateCanary, RuleStateActiveProject) {
		t.Fatal("canary -> active_project must require a rollback snapshot")
	}
	if !RequiresRuleSnapshot(RuleStateActiveGlobal, RuleStateKernel) {
		t.Fatal("active_global -> kernel must require a rollback snapshot")
	}
}
