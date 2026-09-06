package codeintel

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/thebtf/engram/internal/uci"
)

func TestUCIRealCorpusPreparedCapacity(t *testing.T) {
	root := os.Getenv("ENGRAM_UCI_REAL_CORPUS_ROOT")
	parserPath := os.Getenv("ENGRAM_UCI_REAL_CORPUS_PARSER")
	if root == "" || parserPath == "" {
		t.Skip("real-corpus capacity probe requires ENGRAM_UCI_REAL_CORPUS_ROOT and ENGRAM_UCI_REAL_CORPUS_PARSER")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	parser, err := uci.NewTreeSitterWorker(uci.TreeSitterWorkerConfig{
		ExecutablePath:       parserPath,
		ExpectedBundleDigest: uci.TreeSitterBundleDigest(),
		MaxInputBytes:        uciRuntimeParserMaxInputBytes,
		MaxOutputBytes:       uciRuntimeParserMaxOutputBytes,
		Timeout:              uciRuntimeParserTimeout,
		Environment:          uciRuntimeParserEnvironment(),
	})
	if err != nil {
		t.Fatal(err)
	}
	collaborator := &UCIPreparedIndexCollaborator{
		parserBundleDigest: uci.TreeSitterBundleDigest(),
		scanner:            newUCIRuntimeScanner(),
		treeSitterParser:   parser,
		goProfile: uci.GoExtractionProfile{
			ProfileKey: uciRuntimeGoProfileKey,
			ParserKey:  uciRuntimeGoParserKey,
		},
	}
	scan, err := collaborator.scanner.Scan(ctx, uci.AuthorizedRootEvidence{RootPath: root})
	if err != nil {
		t.Fatal(err)
	}
	var present, excluded, unreadable, sourceBytes int
	for _, file := range scan.Files {
		switch file.State {
		case uci.IndexFilePresent:
			present++
			sourceBytes += len(file.Body)
		case uci.IndexFileExcluded:
			excluded++
		case uci.IndexFileUnreadable:
			unreadable++
		}
	}
	binding := uci.IndexBinding{
		Scope: uci.IndexScope{
			SourceID:      "11111111-1111-4111-8111-111111111111",
			CheckoutID:    "22222222-2222-4222-8222-222222222222",
			IncarnationID: "33333333-3333-4333-8333-333333333333",
		},
		ProfileID:     "44444444-4444-4444-8444-444444444444",
		LocalRootID:   "root:real-corpus-probe",
		WorkstationID: "workstation:real-corpus-probe",
	}
	plan, err := collaborator.prepareAdmissionPlan(ctx, uciPreparedLocalTarget{binding: binding}, scan)
	if err != nil {
		var capacity *uci.IndexCapacityError
		if errors.As(err, &capacity) {
			t.Fatalf("capacity refusal scope=%s resource=%s required=%d limit=%d files=%d present=%d excluded=%d unreadable=%d source_bytes=%d", capacity.Scope(), capacity.Resource(), capacity.Required(), capacity.Limit(), len(scan.Files), present, excluded, unreadable, sourceBytes)
		}
		t.Fatal(err)
	}
	payloadBytes := 0
	maxPayloadBytes := 0
	artifacts := 0
	memberships := 0
	definitions := 0
	references := 0
	chunks := 0
	edgeReplacements := 0
	edges := 0
	for index, payload := range plan.payloads {
		payloadBytes += len(payload)
		if len(payload) > maxPayloadBytes {
			maxPayloadBytes = len(payload)
		}
		frame := plan.frames[index]
		artifacts += len(frame.Artifacts)
		memberships += len(frame.Memberships)
		for _, artifact := range frame.Artifacts {
			definitions += len(artifact.Definitions)
			references += len(artifact.References)
			chunks += len(artifact.Chunks)
		}
		edgeReplacements += len(frame.EdgeReplacements)
		for _, replacement := range frame.EdgeReplacements {
			edges += len(replacement.Edges)
		}
	}
	t.Logf("real corpus capacity files=%d present=%d excluded=%d unreadable=%d source_bytes=%d frames=%d payload_bytes=%d max_payload_bytes=%d artifacts=%d definitions=%d references=%d chunks=%d memberships=%d edge_replacements=%d edges=%d uploaded=%d coverage=%+v errors=%d", len(scan.Files), present, excluded, unreadable, sourceBytes, len(plan.frames), payloadBytes, maxPayloadBytes, artifacts, definitions, references, chunks, memberships, edgeReplacements, edges, plan.uploaded, plan.coverage, len(plan.errors))
}
