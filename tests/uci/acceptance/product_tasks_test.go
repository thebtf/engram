package acceptance

import (
	"os"
	"strings"
	"testing"
)

func TestUCIProductTaskContracts(t *testing.T) {
	requireUCIProductTaskContracts(t)
}

func TestUCIProductTaskCorpus(t *testing.T) {
	if os.Getenv(uciProductInputPathEnv) == "" {
		t.Skip("UCI product task corpus requires externally recorded evidence at ENGRAM_UCI_PRODUCT_INPUT")
	}

	requireUCIProductTaskContracts(t)

	report, err := RunUCIProductTaskCorpus(t.Context())
	if err != nil {
		t.Fatalf("UCI product task corpus RED: %v", err)
	}
	if !report.Passed {
		t.Fatal("UCI product task corpus returned an unaccepted report without a validation error")
	}
}

func requireUCIProductTaskContracts(t *testing.T) {
	t.Helper()
	if _, _, err := loadUCIProductTaskContracts(); err != nil {
		t.Fatalf("validate pre-registered UCI product task contracts: %v", err)
	}
}

func TestUCIProductCitationContextRefValidation(t *testing.T) {
	contextRef := uciProductTestContextRef()
	if !validUCIProductContextRef(contextRef) {
		t.Fatal("canonical UCI citation context was rejected")
	}
	contextRef.ViewID = "not-a-canonical-uuid"
	if validUCIProductContextRef(contextRef) {
		t.Fatal("non-canonical UCI citation context was accepted")
	}
	if validUCIProductSpan(UCIProductSpan{ByteStart: 4, ByteEnd: 4, LineStart: 2, LineEnd: 1}) {
		t.Fatal("unordered numeric source span was accepted")
	}
}

func TestUCIProductRelationEvidenceBindsCitedContext(t *testing.T) {
	const evidenceReference = "fixture://uci-product-corpus/relation-binding"
	digestA := "sha256:" + strings.Repeat("a", 64)
	digestB := "sha256:" + strings.Repeat("b", 64)
	contextRef := uciProductTestContextRef()
	mismatchedContext := contextRef
	mismatchedContext.ViewID = "66666666-6666-4666-8666-666666666666"
	task := uciProductTask{
		ID: "UCI-P07",
		Evidence: uciEvidenceContract{Required: []uciEvidenceExpectation{{
			Kind:      "relation_path",
			Reference: evidenceReference,
		}}},
	}
	result := UCIProductTaskResult{
		Outcome: "success",
		TopFive: []UCIProductSourceCitation{{
			Rank:              1,
			EvidenceReference: evidenceReference,
			Context:           contextRef,
			Path:              "internal/uci/query.go",
			Span:              UCIProductSpan{ByteStart: 0, ByteEnd: 16, LineStart: 1, LineEnd: 1},
			Digest:            digestA,
		}},
		RelationEvidence: []UCIProductRelationEvidence{{
			EvidenceReference: evidenceReference,
			CitationRank:      1,
			CitationContext:   mismatchedContext,
			Path:              []string{"source", "target"},
			Digest:            digestB,
		}},
	}
	violations := uciProductViolationCollector{}
	if validateUCIProductEvidence(result, task, &violations) {
		t.Fatal("relation evidence with a mismatched citation context was accepted")
	}
	for _, violation := range violations.values {
		if violation.Code == "relation_context_binding" {
			return
		}
	}
	t.Fatalf("relation context mismatch did not produce its binding violation: %#v", violations.values)
}

func uciProductTestContextRef() UCIProductContextRef {
	return UCIProductContextRef{
		SpaceID:           "11111111-1111-4111-8111-111111111111",
		SourceID:          "22222222-2222-4222-8222-222222222222",
		CheckoutID:        "33333333-3333-4333-8333-333333333333",
		ViewID:            "44444444-4444-4444-8444-444444444444",
		Generation:        1,
		AnalysisProfileID: "55555555-5555-4555-8555-555555555555",
	}
}
