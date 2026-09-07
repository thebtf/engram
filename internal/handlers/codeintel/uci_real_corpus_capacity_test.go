package codeintel

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	engramgorm "github.com/thebtf/engram/internal/db/gorm"
	"github.com/thebtf/engram/internal/uci"
	gormlib "gorm.io/gorm"
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
	if os.Getenv("ENGRAM_UCI_REAL_CORPUS_ADMIT") != "1" {
		return
	}
	store, err := engramgorm.NewStore(engramgorm.Config{DSN: os.Getenv("DATABASE_DSN"), MaxConns: 2})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	rollbackProbe := errors.New("real corpus admission probe rollback")
	err = store.GetDB().Transaction(func(tx *gormlib.DB) error {
		now := time.Now().UTC()
		if err := tx.Create(&engramgorm.UCISource{
			SourceID: binding.Scope.SourceID, AuthRealm: "real-corpus-probe", Kind: engramgorm.UCISourceGit,
			DisplayName: "real corpus probe", State: engramgorm.UCISourceActive, CreatedAt: now, UpdatedAt: now,
		}).Error; err != nil {
			return err
		}
		if err := tx.Create(&engramgorm.UCIAnalysisProfile{
			ProfileID: binding.ProfileID, ParserBundleDigest: string(uci.TreeSitterBundleDigest()),
			ResolverRevision: "real-corpus-probe", ChunkerRevision: "real-corpus-probe",
			IgnorePolicyDigest: fmt.Sprintf("sha256:%064d", 0), BuildContextJSON: `{}`,
			SecretPolicyRevision: "real-corpus-probe", CreatedAt: now,
		}).Error; err != nil {
			return err
		}
		parts, err := engramgorm.NewUCIProjectionStore(tx).AdmitIndexFrames(ctx, binding.Scope.SourceID, binding.ProfileID, plan.frames)
		if err != nil {
			return err
		}
		t.Logf("real corpus direct admission parts=%d", len(parts))
		return rollbackProbe
	})
	if !errors.Is(err, rollbackProbe) {
		t.Fatalf("real corpus direct admission: %v", err)
	}
}
