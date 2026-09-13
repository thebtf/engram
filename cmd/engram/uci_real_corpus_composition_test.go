package main

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	gormdb "github.com/thebtf/engram/internal/db/gorm"
	"github.com/thebtf/engram/internal/privacy"
	"github.com/thebtf/engram/internal/uci"
)

const (
	uciRealCorpusCompositionSchemaVersion         = "engram.uci-real-corpus-composition/v2"
	uciRealCorpusPreparedMembershipMode           = "unknown"
	uciRealCorpusScannerModeUnavailable           = "unavailable"
	uciRealCorpusUnsupportedCapabilityCause       = "unsupported_capability"
	uciRealCorpusUnreadableCause                  = "unreadable"
	uciRealCorpusCapacityLegacyPartBytes    int64 = 1 << 20
	uciRealCorpusCapacityLegacyBuildBytes   int64 = 16 << 20
)

var uciRealCorpusCompositionProtectedPaths = [...]string{
	".agent",
	".env",
	".env.*",
	".cache",
	"build",
	"coverage",
	"credentials",
	"dist",
	"keys",
	"node_modules",
	"target",
	"transcripts",
	"vendor",
}

var uciRealCorpusCompositionCapabilities = [...]string{
	"openapi-json:v1:.json",
	"openapi-yaml:v1:.yaml:.yml",
	"go:v1:.go",
	"javascript:v1:.js",
	"typescript:v1:.ts",
	"tsx:v1:.tsx",
	"markdown:v1:.md:.markdown",
	"json:v1:.json",
	"yaml:v1:.yaml:.yml",
	"sql:v1:.sql",
}

var uciRealCorpusCompositionExcludedCapabilities = [...]string{
	"csharp:v1:.cs",
	"vue:v1:.vue",
}

// uciRealCorpusFrozenManifest retains the private path-level scanner census only
// in memory. Its marshalled form contains aggregate evidence, never paths or bodies.
type uciRealCorpusFrozenManifest struct {
	SchemaVersion            string            `json:"schema_version"`
	ScannerPolicyDigest      string            `json:"scanner_policy_digest"`
	ManifestDigest           string            `json:"manifest_digest"`
	BaselineManifestDigest   string            `json:"baseline_manifest_digest"`
	CanaryDeltaDigest        string            `json:"canary_delta_digest"`
	EntryCount               uint64            `json:"entry_count"`
	BaselineEntryCount       uint64            `json:"baseline_entry_count"`
	CanaryEntryCount         uint64            `json:"canary_entry_count"`
	ScannerStateHistogram    map[string]uint64 `json:"scanner_state_histogram"`
	MembershipStateHistogram map[string]uint64 `json:"membership_state_histogram"`
	BaselineStateHistogram   map[string]uint64 `json:"baseline_state_histogram"`
	CanaryStateHistogram     map[string]uint64 `json:"canary_state_histogram"`
	ReasonHistogram          map[string]uint64 `json:"reason_histogram"`
	ScannerModeHistogram     map[string]uint64 `json:"scanner_mode_histogram"`
	entries                  []uciRealCorpusFrozenEntry
}

// uciRealCorpusComposition is the receipt-safe exact-composition proof. It
// contains bounded counts, histograms, and digests only.
type uciRealCorpusComposition struct {
	SchemaVersion               string                          `json:"schema_version"`
	ScannerPolicyDigest         string                          `json:"scanner_policy_digest"`
	FrozenManifestDigest        string                          `json:"frozen_manifest_digest"`
	BaselineManifestDigest      string                          `json:"baseline_manifest_digest"`
	CanaryDeltaDigest           string                          `json:"canary_delta_digest"`
	ViewManifestDigest          string                          `json:"view_manifest_digest"`
	ReconstructedManifestDigest string                          `json:"reconstructed_manifest_digest"`
	StagedPartsDigest           string                          `json:"staged_parts_digest"`
	StagedEdgesDigest           string                          `json:"staged_edges_digest"`
	ArtifactProofDigest         string                          `json:"artifact_proof_digest"`
	Packing                     uciRealCorpusPackedPartCapacity `json:"packing"`
	MembershipCount             uint64                          `json:"membership_count"`
	ArtifactProofCount          uint64                          `json:"artifact_proof_count"`
	EdgeCount                   uint64                          `json:"edge_count"`
	BaselineMembershipCount     uint64                          `json:"baseline_membership_count"`
	CanaryMembershipCount       uint64                          `json:"canary_membership_count"`
	MembershipStateHistogram    map[string]uint64               `json:"membership_state_histogram"`
	BaselineStateHistogram      map[string]uint64               `json:"baseline_state_histogram"`
	CanaryStateHistogram        map[string]uint64               `json:"canary_state_histogram"`
	ReasonHistogram             map[string]uint64               `json:"reason_histogram"`
	ScannerModeHistogram        map[string]uint64               `json:"scanner_mode_histogram"`
	PersistedModeHistogram      map[string]uint64               `json:"persisted_mode_histogram"`
	ExcludedCapabilities        []string                        `json:"excluded_capabilities"`
}

type uciRealCorpusPackedPartCapacity struct {
	ObservedPartCount           uint64 `json:"observed_packed_part_count"`
	ObservedTotalEncodedBytes   uint64 `json:"observed_total_encoded_bytes"`
	ObservedMaxEncodedPartBytes uint64 `json:"observed_max_encoded_part_bytes"`
	ConfiguredMaxPartCount      uint64 `json:"configured_max_packed_part_count"`
	ConfiguredMaxTotalBytes     uint64 `json:"configured_max_total_encoded_bytes"`
	ConfiguredMaxPartBytes      uint64 `json:"configured_max_encoded_part_bytes"`
}

// uciRealCorpusCapacityRecovery is a receipt-safe real-store proof that a
// fragmented full corpus can recover without changing the accepted View.
type uciRealCorpusCapacityRecovery struct {
	FullCorpusFragmented              bool   `json:"full_corpus_fragmented"`
	LegacyPartLimitExceeded           bool   `json:"legacy_part_limit_exceeded"`
	LegacyBuildLimitExceeded          bool   `json:"legacy_build_limit_exceeded"`
	InterruptedStagingObserved        bool   `json:"interrupted_staging_observed"`
	ResumedStagingObserved            bool   `json:"resumed_staging_observed"`
	ExactPartDigestsObserved          bool   `json:"exact_part_digests_observed"`
	IncompleteFinalizeRejected        bool   `json:"incomplete_finalize_rejected"`
	ConflictingFinalizeRejected       bool   `json:"conflicting_finalize_rejected"`
	AtomicFinalizeObserved            bool   `json:"atomic_finalize_observed"`
	PriorViewPreservedAfterIncomplete bool   `json:"prior_view_preserved_after_incomplete"`
	PriorViewPreservedAfterConflict   bool   `json:"prior_view_preserved_after_conflict"`
	SuccessfulFinalizeObserved        bool   `json:"successful_finalize_observed"`
	AllGuaranteesObserved             bool   `json:"all_guarantees_observed"`
	SourcePartCount                   uint64 `json:"source_part_count"`
	SourcePayloadBytes                uint64 `json:"source_payload_bytes"`
	SourceMaxPartBytes                uint64 `json:"source_max_part_bytes"`
	LegacyMaxPartBytes                uint64 `json:"legacy_max_part_bytes"`
	LegacyMaxBuildBytes               uint64 `json:"legacy_max_build_bytes"`
	QuotaMaxParts                     uint64 `json:"quota_max_parts"`
	QuotaMaxBuildBytes                uint64 `json:"quota_max_build_bytes"`
	QuotaMaxPartBytes                 uint64 `json:"quota_max_part_bytes"`
	InterruptedPartCount              uint64 `json:"interrupted_part_count"`
	ResumedPartCount                  uint64 `json:"resumed_part_count"`
	PriorViewCount                    uint64 `json:"prior_view_count"`
	AfterIncompleteViewCount          uint64 `json:"after_incomplete_view_count"`
	AfterConflictViewCount            uint64 `json:"after_conflict_view_count"`
	PublishedViewCount                uint64 `json:"published_view_count"`
	ReplayViewCount                   uint64 `json:"replay_view_count"`
	SourcePartsDigest                 string `json:"source_parts_digest"`
	ResumedPartsDigest                string `json:"resumed_parts_digest"`
	ManifestDigest                    string `json:"manifest_digest"`
	PriorViewDigest                   string `json:"prior_view_digest"`
	PublishedViewDigest               string `json:"published_view_digest"`
}

type uciRealCorpusFrozenEntry struct {
	path            string
	scannerState    uci.IndexFileState
	membershipState uci.IndexFileState
	reason          string
	scannerMode     string
	contentDigest   string
	canary          bool
}

type uciRealCorpusCompositionPublicationRow struct {
	BuildID            string `gorm:"column:build_id"`
	ManifestMode       string `gorm:"column:manifest_mode"`
	SealedManifest     string `gorm:"column:sealed_manifest"`
	ViewManifestDigest string `gorm:"column:view_manifest_digest"`
	ViewState          string `gorm:"column:view_state"`
}

type uciRealCorpusCompositionPartRow struct {
	Sequence     int64  `gorm:"column:sequence"`
	PartDigest   string `gorm:"column:part_digest"`
	Payload      string `gorm:"column:payload"`
	PayloadBytes int64  `gorm:"column:payload_bytes"`
}

type uciRealCorpusCompositionMembershipRow struct {
	PathKey     string         `gorm:"column:path_key"`
	DisplayPath string         `gorm:"column:display_path"`
	Mode        string         `gorm:"column:mode"`
	FileState   string         `gorm:"column:file_state"`
	ArtifactID  sql.NullString `gorm:"column:artifact_id"`
}

type uciRealCorpusCompositionStagedBuild struct {
	memberships     map[string]uci.IndexMembership
	artifactProofs  map[string]uci.IndexArtifactProof
	partsDigest     string
	manifestDigest  string
	edgesDigest     string
	partCount       uint64
	payloadBytes    uint64
	maxPayloadBytes uint64
	edgeCount       uint64
}

type uciRealCorpusRecordingGitRunner struct {
	stageModes map[string]string
}

func (runner *uciRealCorpusRecordingGitRunner) Run(ctx context.Context, invocation uci.GitInvocation) (uci.GitResult, error) {
	result, err := (uci.ExecGitRunner{}).Run(ctx, invocation)
	if err != nil || result.ExitCode != 0 || !uciRealCorpusStageInvocation(invocation.Args) {
		return result, err
	}
	if err := runner.captureStageModes(result.Stdout); err != nil {
		return uci.GitResult{}, err
	}
	return result, nil
}

func (runner *uciRealCorpusRecordingGitRunner) captureStageModes(output []byte) error {
	if runner == nil {
		return errors.New("real-corpus composition scanner recorder is unavailable")
	}
	if len(output) == 0 {
		return nil
	}
	if output[len(output)-1] != 0 {
		return errors.New("real-corpus composition scanner stage output is malformed")
	}
	if runner.stageModes == nil {
		runner.stageModes = make(map[string]string)
	}
	for start := 0; start < len(output); {
		record, next, err := uciRealCorpusStageModeRecord(output, start)
		if err != nil {
			return err
		}
		runner.recordStageMode(record)
		start = next
	}
	return nil
}

type uciRealCorpusStageModeEntry struct {
	path string
	mode string
}

func uciRealCorpusStageModeRecord(output []byte, start int) (uciRealCorpusStageModeEntry, int, error) {
	end := start
	for end < len(output) && output[end] != 0 {
		end++
	}
	if end == start || end == len(output) {
		return uciRealCorpusStageModeEntry{}, 0, errors.New("real-corpus composition scanner stage output is malformed")
	}
	path, mode, err := uciRealCorpusStageMode(string(output[start:end]))
	if err != nil {
		return uciRealCorpusStageModeEntry{}, 0, err
	}
	return uciRealCorpusStageModeEntry{path: path, mode: mode}, end + 1, nil
}

func (runner *uciRealCorpusRecordingGitRunner) recordStageMode(record uciRealCorpusStageModeEntry) {
	if record.mode == "" {
		return
	}
	previous, found := runner.stageModes[record.path]
	if !found || (previous != "160000" && record.mode == "160000") {
		runner.stageModes[record.path] = record.mode
	}
}

func uciRealCorpusStageMode(record string) (string, string, error) {
	if len(record) < 3 || record[1] != ' ' {
		return "", "", errors.New("real-corpus composition scanner stage output is malformed")
	}
	switch record[0] {
	case '?':
		candidatePath := record[2:]
		if !uciRealCorpusCompositionPath(candidatePath) {
			return "", "", errors.New("real-corpus composition scanner stage output is malformed")
		}
		return candidatePath, "", nil
	case 'H', 'S', 'M':
		stageRecord := record[2:]
		separator := strings.IndexByte(stageRecord, '\t')
		if separator < 0 || separator == len(stageRecord)-1 {
			return "", "", errors.New("real-corpus composition scanner stage output is malformed")
		}
		header := strings.Fields(stageRecord[:separator])
		if len(header) != 3 || !uciRealCorpusGitMode(header[0]) {
			return "", "", errors.New("real-corpus composition scanner stage output is malformed")
		}
		candidatePath := stageRecord[separator+1:]
		if !uciRealCorpusCompositionPath(candidatePath) {
			return "", "", errors.New("real-corpus composition scanner stage output is malformed")
		}
		return candidatePath, header[0], nil
	default:
		return "", "", errors.New("real-corpus composition scanner stage output is malformed")
	}
}

// uciFreezeRealCorpusManifest scans through the same scanner policy as the
// production prepared-index runtime, records only relative path facts, and
// treats known untracked canaries as a separate delta.
func uciFreezeRealCorpusManifest(ctx context.Context, root, semanticQuery string) (uciRealCorpusFrozenManifest, error) {
	if ctx == nil {
		return uciRealCorpusFrozenManifest{}, errors.New("real-corpus composition context is required")
	}
	if strings.TrimSpace(semanticQuery) == "" {
		return uciRealCorpusFrozenManifest{}, errors.New("real-corpus semantic query is required")
	}
	runner := &uciRealCorpusRecordingGitRunner{stageModes: make(map[string]string)}
	scanner := uci.NewScanner(
		runner,
		uci.OSScannerFileSystem{},
		uci.ScannerPolicy{
			IncludeUntracked: true,
			ProtectedPaths:   append([]string(nil), uciRealCorpusCompositionProtectedPaths[:]...),
			SecretDetector: uci.ScannerSecretDetectorFunc(func(filePath string, body []byte) bool {
				return uciRealCorpusProtectedSecretPath(filePath) || privacy.ContainsSecrets(string(body))
			}),
		},
	)
	scan, err := scanner.Scan(ctx, uci.AuthorizedRootEvidence{RootPath: root})
	if err != nil {
		return uciRealCorpusFrozenManifest{}, errors.New("real-corpus composition scanner failed")
	}
	if scan.Census.Outcome != uci.IndexScanComplete || !scan.Census.Complete || !scan.Census.CanDeleteAll || scan.Coverage.Structural != uci.IndexCoverageComplete {
		return uciRealCorpusFrozenManifest{}, errors.New("real-corpus composition scanner census is incomplete")
	}

	entries, lexicalOverlap, err := uciRealCorpusFreezeEntries(ctx, scan.Files, runner.stageModes, semanticQuery)
	if err != nil {
		return uciRealCorpusFrozenManifest{}, err
	}
	if len(lexicalOverlap) != 0 {
		tokens := make([]string, 0, len(lexicalOverlap))
		for token := range lexicalOverlap {
			tokens = append(tokens, token)
		}
		sort.Strings(tokens)
		return uciRealCorpusFrozenManifest{}, fmt.Errorf("real-corpus semantic query overlaps indexed corpus tokens: %s", strings.Join(tokens, ","))
	}
	manifest, err := uciRealCorpusBuildFrozenManifest(entries)
	if err != nil {
		return uciRealCorpusFrozenManifest{}, err
	}
	return manifest, nil
}

func uciRealCorpusFreezeEntries(ctx context.Context, files []uci.ScannerFile, stageModes map[string]string, semanticQuery string) ([]uciRealCorpusFrozenEntry, map[string]struct{}, error) {
	entries := make([]uciRealCorpusFrozenEntry, 0, len(files))
	lexicalOverlap := make(map[string]struct{})
	for _, file := range files {
		if err := ctx.Err(); err != nil {
			return nil, nil, err
		}
		membershipState, reason, err := uciRealCorpusFrozenMembershipState(file)
		if err != nil {
			return nil, nil, errors.New("real-corpus composition scanner file state is invalid")
		}
		if membershipState == uci.IndexFilePresent {
			for _, token := range uciRealCorpusLexicalOverlap(semanticQuery, string(file.Body)) {
				lexicalOverlap[token] = struct{}{}
			}
		}
		contentDigest := ""
		if file.State == uci.IndexFilePresent {
			contentDigest = uciRealCorpusSHA256(file.Body)
		}
		mode := stageModes[file.Path]
		if mode == "" {
			mode = uciRealCorpusScannerModeUnavailable
		}
		_, canary := uciRealCorpusCanaries[file.Path]
		entries = append(entries, uciRealCorpusFrozenEntry{
			path: file.Path, scannerState: file.State, membershipState: membershipState, reason: reason,
			scannerMode: mode, contentDigest: contentDigest, canary: canary,
		})
	}
	return entries, lexicalOverlap, nil
}

// uciVerifyRealCorpusComposition proves that one successful publication is an
// exact realization of a prior frozen scanner census, rather than a threshold
// of aggregate database rows.
func uciVerifyRealCorpusComposition(ctx context.Context, authority *uciInstalledAcceptanceAuthority, publication uciInstalledAcceptancePublication, frozen uciRealCorpusFrozenManifest) (uciRealCorpusComposition, error) {
	if ctx == nil {
		return uciRealCorpusComposition{}, errors.New("real-corpus composition context is required")
	}
	if err := uciRealCorpusValidateFrozenManifest(frozen); err != nil {
		return uciRealCorpusComposition{}, err
	}
	if err := uciRealCorpusValidateCompositionAuthority(authority, publication); err != nil {
		return uciRealCorpusComposition{}, err
	}

	publicationRow, completion, err := uciRealCorpusLoadCompositionPublication(ctx, authority, publication)
	if err != nil {
		return uciRealCorpusComposition{}, err
	}
	staged, err := uciRealCorpusLoadStagedBuild(ctx, authority, publicationRow.BuildID)
	if err != nil {
		return uciRealCorpusComposition{}, err
	}
	packing, err := uciRealCorpusObservePackedPartCapacity(staged)
	if err != nil {
		return uciRealCorpusComposition{}, err
	}

	if err := uciRealCorpusValidateStagedCompletion(publicationRow, completion, staged); err != nil {
		return uciRealCorpusComposition{}, err
	}

	persisted, err := uciRealCorpusLoadTemporalMemberships(ctx, authority, publication)
	if err != nil {
		return uciRealCorpusComposition{}, err
	}
	if err := uciRealCorpusVerifyMembershipProjection(frozen.entries, staged.memberships, persisted, staged.artifactProofs); err != nil {
		return uciRealCorpusComposition{}, err
	}
	if err := uciRealCorpusVerifyArtifactProofs(ctx, authority, staged.artifactProofs); err != nil {
		return uciRealCorpusComposition{}, err
	}

	artifactProofDigest, err := uciRealCorpusArtifactProofDigest(staged.artifactProofs)
	if err != nil {
		return uciRealCorpusComposition{}, errors.New("real-corpus composition artifact proof digest failed")
	}
	composition := uciRealCorpusComposition{
		SchemaVersion:               uciRealCorpusCompositionSchemaVersion,
		ScannerPolicyDigest:         frozen.ScannerPolicyDigest,
		FrozenManifestDigest:        frozen.ManifestDigest,
		BaselineManifestDigest:      frozen.BaselineManifestDigest,
		CanaryDeltaDigest:           frozen.CanaryDeltaDigest,
		ViewManifestDigest:          publicationRow.ViewManifestDigest,
		ReconstructedManifestDigest: staged.manifestDigest,
		StagedPartsDigest:           staged.partsDigest,
		StagedEdgesDigest:           staged.edgesDigest,
		ArtifactProofDigest:         artifactProofDigest,
		Packing:                     packing,
		MembershipCount:             uint64(len(staged.memberships)),
		ArtifactProofCount:          uint64(len(staged.artifactProofs)),
		EdgeCount:                   staged.edgeCount,
		BaselineMembershipCount:     frozen.BaselineEntryCount,
		CanaryMembershipCount:       frozen.CanaryEntryCount,
		MembershipStateHistogram:    uciRealCorpusCloneHistogram(frozen.MembershipStateHistogram),
		BaselineStateHistogram:      uciRealCorpusCloneHistogram(frozen.BaselineStateHistogram),
		CanaryStateHistogram:        uciRealCorpusCloneHistogram(frozen.CanaryStateHistogram),
		ReasonHistogram:             uciRealCorpusCloneHistogram(frozen.ReasonHistogram),
		ScannerModeHistogram:        uciRealCorpusCloneHistogram(frozen.ScannerModeHistogram),
		PersistedModeHistogram:      uciRealCorpusPersistedModeHistogram(persisted),
		ExcludedCapabilities:        append([]string(nil), uciRealCorpusCompositionExcludedCapabilities[:]...),
	}
	return composition, nil
}

// uciVerifyRealCorpusCapacityRecovery proves the adverse publication path with
// the real installed-store source parts. All mutation stays inside a rolled-back
// transaction and fresh clone checkouts, leaving the accepted publication intact.
type uciRealCorpusCapacitySource struct {
	completion        uci.IndexManifestCompletion
	staged            uciRealCorpusCompositionStagedBuild
	parts             []uci.IndexPart
	sourcePartsDigest string
	manifestDigest    string
}

type uciRealCorpusCapacityLegacyFailures struct {
	partLimit  int
	buildLimit int
}

type uciRealCorpusCapacityRun struct {
	checkout       *gormdb.UCICheckout
	publisher      uci.IndexStore
	profileID      string
	priorContext   uci.ContextRef
	priorView      *gormdb.UCIView
	priorViewCount uint64
}

type uciRealCorpusCapacityRecoveryBuild struct {
	build                uci.IndexBuildRef
	expectedAcks         []uci.IndexPartAck
	manifest             uci.IndexManifestCompletion
	interruptedPartCount int
}

type uciRealCorpusCapacityRecoveryOutcome struct {
	interruptedPartCount     int
	resumedPartCount         int
	afterIncompleteViewCount uint64
	afterConflictViewCount   uint64
	publishedViewCount       uint64
	replayViewCount          uint64
	resumedPartsDigest       string
	publishedView            *gormdb.UCIView
}
type uciRealCorpusCapacityRecoveryInput struct {
	ctx        context.Context
	tx         *gorm.DB
	contexts   *gormdb.UCIContextStore
	authorizer *gormdb.UCIContextAuthorizer
	authority  *uciInstalledAcceptanceAuthority
	caller     uci.IndexCaller
	source     uciRealCorpusCapacitySource
	run        uciRealCorpusCapacityRun
	legacy     uciRealCorpusCapacityLegacyFailures
}

type uciRealCorpusCapacityResumeInput struct {
	ctx        context.Context
	tx         *gorm.DB
	authorizer *gormdb.UCIContextAuthorizer
	caller     uci.IndexCaller
	run        uciRealCorpusCapacityRun
	recovery   uciRealCorpusCapacityRecoveryBuild
	parts      []uci.IndexPart
	actualAcks []uci.IndexPartAck
}

type uciRealCorpusCapacityPublicationInput struct {
	ctx       context.Context
	tx        *gorm.DB
	contexts  *gormdb.UCIContextStore
	caller    uci.IndexCaller
	run       uciRealCorpusCapacityRun
	publisher uci.IndexStore
	build     uci.IndexBuildRef
	manifest  uci.IndexManifestCompletion
}

func uciVerifyRealCorpusCapacityRecovery(ctx context.Context, authority *uciInstalledAcceptanceAuthority, publication uciInstalledAcceptancePublication, composition uciRealCorpusComposition) (uciRealCorpusCapacityRecovery, error) {
	source, err := uciRealCorpusCapacitySourceFor(ctx, authority, publication, composition)
	if err != nil {
		return uciRealCorpusCapacityRecovery{}, err
	}
	tx := authority.store.GetDB().WithContext(ctx).Begin()
	if tx.Error != nil {
		return uciRealCorpusCapacityRecovery{}, errors.New("real-corpus capacity isolation transaction could not start")
	}
	rolledBack := false
	defer func() {
		if !rolledBack {
			_ = tx.Rollback().Error
		}
	}()
	result, err := uciRealCorpusExecuteCapacityRecovery(ctx, tx, authority, source)
	if err != nil {
		return uciRealCorpusCapacityRecovery{}, err
	}
	if err := tx.Rollback().Error; err != nil {
		return uciRealCorpusCapacityRecovery{}, errors.New("real-corpus capacity isolation transaction could not roll back")
	}
	rolledBack = true
	return result, nil
}

func uciRealCorpusCapacitySourceFor(ctx context.Context, authority *uciInstalledAcceptanceAuthority, publication uciInstalledAcceptancePublication, composition uciRealCorpusComposition) (uciRealCorpusCapacitySource, error) {
	if ctx == nil {
		return uciRealCorpusCapacitySource{}, errors.New("real-corpus capacity context is required")
	}
	if err := uciRealCorpusValidateCompositionAuthority(authority, publication); err != nil {
		return uciRealCorpusCapacitySource{}, err
	}
	if authority.token == nil || authority.token.ID == "" {
		return uciRealCorpusCapacitySource{}, errors.New("real-corpus capacity authority is incomplete")
	}
	publicationRow, completion, err := uciRealCorpusLoadCompositionPublication(ctx, authority, publication)
	if err != nil {
		return uciRealCorpusCapacitySource{}, err
	}
	staged, err := uciRealCorpusLoadStagedBuild(ctx, authority, publicationRow.BuildID)
	if err != nil {
		return uciRealCorpusCapacitySource{}, err
	}
	if err := uciRealCorpusValidateStagedCompletion(publicationRow, completion, staged); err != nil {
		return uciRealCorpusCapacitySource{}, err
	}
	if composition.StagedPartsDigest != staged.partsDigest || composition.ReconstructedManifestDigest != staged.manifestDigest || composition.ViewManifestDigest != publicationRow.ViewManifestDigest || composition.MembershipCount != uint64(len(staged.memberships)) || composition.EdgeCount != staged.edgeCount {
		return uciRealCorpusCapacitySource{}, errors.New("real-corpus capacity composition does not match the observed publication")
	}
	parts, sourceAcks, err := uciRealCorpusCapacityLoadParts(ctx, authority, publicationRow.BuildID)
	if err != nil {
		return uciRealCorpusCapacitySource{}, err
	}
	sourceBuildPartsDigest, err := uci.DigestIndexParts(sourceAcks)
	if err != nil || string(sourceBuildPartsDigest) != staged.partsDigest || sourceBuildPartsDigest != completion.PartsDigest || uint64(len(parts)) != staged.partCount {
		return uciRealCorpusCapacitySource{}, errors.New("real-corpus capacity source parts do not match the sealed publication")
	}
	sourcePartsDigest, err := uciRealCorpusCapacityPartSequenceDigest(sourceAcks)
	if err != nil {
		return uciRealCorpusCapacitySource{}, err
	}
	manifestDigest, err := uciRealCorpusCapacityBareIndexDigest(completion.ManifestDigest)
	if err != nil {
		return uciRealCorpusCapacitySource{}, err
	}
	if err := uciRealCorpusValidateCapacityLimits(composition, staged); err != nil {
		return uciRealCorpusCapacitySource{}, err
	}
	return uciRealCorpusCapacitySource{completion: completion, staged: staged, parts: parts, sourcePartsDigest: sourcePartsDigest, manifestDigest: manifestDigest}, nil
}

func uciRealCorpusValidateCapacityLimits(composition uciRealCorpusComposition, staged uciRealCorpusCompositionStagedBuild) error {
	limits := uci.DefaultIndexPublicationLimits()
	if limits.LeaseTTL <= 0 || limits.MaxPartBytes <= 0 || limits.MaxParts == 0 || limits.MaxBuildBytes <= 0 || limits.MaxManifestEntries == 0 || limits.MaxEdges == 0 || limits.MaxArtifactBytes <= 0 || staged.partCount > uint64(limits.MaxParts) || staged.payloadBytes > uint64(limits.MaxBuildBytes) || staged.maxPayloadBytes > uint64(limits.MaxPartBytes) || uint64(len(staged.memberships)) > limits.MaxManifestEntries || staged.edgeCount > limits.MaxEdges || composition.Packing.ObservedPartCount != staged.partCount || composition.Packing.ObservedTotalEncodedBytes != staged.payloadBytes || composition.Packing.ObservedMaxEncodedPartBytes != staged.maxPayloadBytes || composition.Packing.ConfiguredMaxPartCount != uint64(limits.MaxParts) || composition.Packing.ConfiguredMaxTotalBytes != uint64(limits.MaxBuildBytes) || composition.Packing.ConfiguredMaxPartBytes != uint64(limits.MaxPartBytes) {
		return errors.New("real-corpus capacity limits do not match the observed publication")
	}
	if staged.partCount < 2 || staged.payloadBytes <= uint64(uciRealCorpusCapacityLegacyBuildBytes) || staged.maxPayloadBytes <= uint64(uciRealCorpusCapacityLegacyPartBytes) {
		return errors.New("real-corpus capacity did not require fragmentation beyond legacy bounds")
	}
	return nil
}

func uciRealCorpusExecuteCapacityRecovery(ctx context.Context, tx *gorm.DB, authority *uciInstalledAcceptanceAuthority, source uciRealCorpusCapacitySource) (uciRealCorpusCapacityRecovery, error) {
	contexts := gormdb.NewUCIContextStore(tx)
	authorizer := gormdb.NewUCIContextAuthorizer(contexts)
	caller := uci.IndexCaller{AuthRealm: authority.source.AuthRealm, Principal: authority.principal, OwnerInstance: "uci-real-corpus-capacity-" + uuid.NewString()}
	legacy, err := uciRealCorpusCapacityObserveLegacyFailures(ctx, tx, contexts, authorizer, authority, caller, source.parts)
	if err != nil {
		return uciRealCorpusCapacityRecovery{}, err
	}
	run, err := uciRealCorpusCapacityStartRun(ctx, tx, contexts, authorizer, authority, caller)
	if err != nil {
		return uciRealCorpusCapacityRecovery{}, err
	}
	outcome, err := uciRealCorpusCapacityRecover(uciRealCorpusCapacityRecoveryInput{ctx: ctx, tx: tx, contexts: contexts, authorizer: authorizer, authority: authority, caller: caller, source: source, run: run, legacy: legacy})
	if err != nil {
		return uciRealCorpusCapacityRecovery{}, err
	}
	return uciRealCorpusCapacityRecoveryResult(source, legacy, run, outcome)
}

func uciRealCorpusCapacityObserveLegacyFailures(ctx context.Context, tx *gorm.DB, contexts *gormdb.UCIContextStore, authorizer *gormdb.UCIContextAuthorizer, authority *uciInstalledAcceptanceAuthority, caller uci.IndexCaller, parts []uci.IndexPart) (uciRealCorpusCapacityLegacyFailures, error) {
	limits := uci.DefaultIndexPublicationLimits()
	legacyPartCheckout, err := uciRealCorpusCapacityRegisterCheckout(ctx, contexts, authority, "legacy-part-"+uuid.NewString())
	if err != nil {
		return uciRealCorpusCapacityLegacyFailures{}, err
	}
	legacyPartLimits := limits
	legacyPartLimits.MaxPartBytes = uciRealCorpusCapacityLegacyPartBytes
	legacyPartPublisher, err := gormdb.NewUCIProjectionStore(tx).Publisher(authorizer, uci.IndexPublicationConfig{Limits: legacyPartLimits})
	if err != nil {
		return uciRealCorpusCapacityLegacyFailures{}, errors.New("real-corpus capacity legacy part publisher is unavailable")
	}
	legacyPartBegin, err := legacyPartPublisher.Begin(ctx, caller, uciRealCorpusCapacityBeginInput("uci-real-corpus-capacity-legacy-part-"+uuid.NewString(), legacyPartCheckout, authority.profile.ProfileID, nil, uci.IndexJobRecovery))
	if err != nil {
		return uciRealCorpusCapacityLegacyFailures{}, errors.New("real-corpus capacity legacy part build could not start")
	}
	partLimit, err := uciRealCorpusCapacityObservePartLimitFailure(ctx, legacyPartPublisher, caller, legacyPartBegin.Build, parts, uint64(uciRealCorpusCapacityLegacyPartBytes))
	if err != nil {
		return uciRealCorpusCapacityLegacyFailures{}, err
	}
	legacyBuildCheckout, err := uciRealCorpusCapacityRegisterCheckout(ctx, contexts, authority, "legacy-build-"+uuid.NewString())
	if err != nil {
		return uciRealCorpusCapacityLegacyFailures{}, err
	}
	legacyBuildLimits := limits
	legacyBuildLimits.MaxBuildBytes = uciRealCorpusCapacityLegacyBuildBytes
	legacyBuildPublisher, err := gormdb.NewUCIProjectionStore(tx).Publisher(authorizer, uci.IndexPublicationConfig{Limits: legacyBuildLimits})
	if err != nil {
		return uciRealCorpusCapacityLegacyFailures{}, errors.New("real-corpus capacity legacy build publisher is unavailable")
	}
	legacyBuildBegin, err := legacyBuildPublisher.Begin(ctx, caller, uciRealCorpusCapacityBeginInput("uci-real-corpus-capacity-legacy-build-"+uuid.NewString(), legacyBuildCheckout, authority.profile.ProfileID, nil, uci.IndexJobRecovery))
	if err != nil {
		return uciRealCorpusCapacityLegacyFailures{}, errors.New("real-corpus capacity legacy build could not start")
	}
	buildLimit, err := uciRealCorpusCapacityObserveBuildLimitFailure(ctx, legacyBuildPublisher, caller, legacyBuildBegin.Build, parts, uint64(uciRealCorpusCapacityLegacyBuildBytes))
	if err != nil {
		return uciRealCorpusCapacityLegacyFailures{}, err
	}
	return uciRealCorpusCapacityLegacyFailures{partLimit: partLimit, buildLimit: buildLimit}, nil
}

func uciRealCorpusCapacityStartRun(ctx context.Context, tx *gorm.DB, contexts *gormdb.UCIContextStore, authorizer *gormdb.UCIContextAuthorizer, authority *uciInstalledAcceptanceAuthority, caller uci.IndexCaller) (uciRealCorpusCapacityRun, error) {
	checkout, err := uciRealCorpusCapacityRegisterCheckout(ctx, contexts, authority, "recovery-"+uuid.NewString())
	if err != nil {
		return uciRealCorpusCapacityRun{}, err
	}
	publisher, err := gormdb.NewUCIProjectionStore(tx).Publisher(authorizer, uci.IndexPublicationConfig{Limits: uci.DefaultIndexPublicationLimits()})
	if err != nil {
		return uciRealCorpusCapacityRun{}, errors.New("real-corpus capacity recovery publisher is unavailable")
	}
	priorBegin, err := publisher.Begin(ctx, caller, uciRealCorpusCapacityBeginInput("uci-real-corpus-capacity-prior-"+uuid.NewString(), checkout, authority.profile.ProfileID, nil, uci.IndexJobInitial))
	if err != nil {
		return uciRealCorpusCapacityRun{}, errors.New("real-corpus capacity prior view build could not start")
	}
	priorAck, err := uciRealCorpusCapacityStage(ctx, publisher, caller, priorBegin.Build, 0, uci.IndexPart{})
	if err != nil {
		return uciRealCorpusCapacityRun{}, errors.New("real-corpus capacity prior view stage failed")
	}
	priorManifest, err := uciRealCorpusCapacityEmptyManifest(priorAck)
	if err != nil {
		return uciRealCorpusCapacityRun{}, err
	}
	priorPublished, err := publisher.Finalize(ctx, caller, uci.IndexFinalizeInput{Build: priorBegin.Build, Manifest: priorManifest})
	if err != nil {
		return uciRealCorpusCapacityRun{}, errors.New("real-corpus capacity prior view finalize failed")
	}
	priorView, err := contexts.GetCurrentView(ctx, checkout.CheckoutID)
	if err != nil || !uciRealCorpusCapacityCurrentMatchesPublished(priorView, priorPublished) {
		return uciRealCorpusCapacityRun{}, errors.New("real-corpus capacity prior selected View is unavailable")
	}
	priorViewCount, err := uciRealCorpusCapacityViewCount(ctx, tx, checkout)
	if err != nil || priorViewCount != 1 {
		return uciRealCorpusCapacityRun{}, errors.New("real-corpus capacity prior View count is invalid")
	}
	return uciRealCorpusCapacityRun{checkout: checkout, publisher: publisher, profileID: authority.profile.ProfileID, priorContext: priorPublished.Context, priorView: priorView, priorViewCount: priorViewCount}, nil
}

func uciRealCorpusCapacityRecover(input uciRealCorpusCapacityRecoveryInput) (uciRealCorpusCapacityRecoveryOutcome, error) {
	recovery, err := uciRealCorpusCapacityBeginRecovery(input.ctx, input.authority, input.caller, input.source, input.run)
	if err != nil {
		return uciRealCorpusCapacityRecoveryOutcome{}, err
	}
	actualAcks, afterIncompleteViewCount, err := uciRealCorpusCapacityStageInterrupted(input.ctx, input.tx, input.contexts, input.caller, input.run, recovery, input.source.parts)
	if err != nil {
		return uciRealCorpusCapacityRecoveryOutcome{}, err
	}
	resumedPublisher, resumedBuild, actualAcks, err := uciRealCorpusCapacityResumeStaging(uciRealCorpusCapacityResumeInput{ctx: input.ctx, tx: input.tx, authorizer: input.authorizer, caller: input.caller, run: input.run, recovery: recovery, parts: input.source.parts, actualAcks: actualAcks})
	if err != nil {
		return uciRealCorpusCapacityRecoveryOutcome{}, err
	}
	resumedPartsDigest, err := uciRealCorpusCapacityRequireExactResume(actualAcks, recovery.expectedAcks, input.source.sourcePartsDigest, input.legacy)
	if err != nil {
		return uciRealCorpusCapacityRecoveryOutcome{}, err
	}
	publicationInput := uciRealCorpusCapacityPublicationInput{ctx: input.ctx, tx: input.tx, contexts: input.contexts, caller: input.caller, run: input.run, publisher: resumedPublisher, build: resumedBuild, manifest: recovery.manifest}
	afterConflictViewCount, err := uciRealCorpusCapacityRejectConflictingFinalize(publicationInput)
	if err != nil {
		return uciRealCorpusCapacityRecoveryOutcome{}, err
	}
	publishedView, publishedViewCount, replayViewCount, err := uciRealCorpusCapacityFinalizeAndReplay(publicationInput)
	if err != nil {
		return uciRealCorpusCapacityRecoveryOutcome{}, err
	}
	return uciRealCorpusCapacityRecoveryOutcome{interruptedPartCount: recovery.interruptedPartCount, resumedPartCount: len(actualAcks), afterIncompleteViewCount: afterIncompleteViewCount, afterConflictViewCount: afterConflictViewCount, publishedViewCount: publishedViewCount, replayViewCount: replayViewCount, resumedPartsDigest: resumedPartsDigest, publishedView: publishedView}, nil
}

func uciRealCorpusCapacityBeginRecovery(ctx context.Context, authority *uciInstalledAcceptanceAuthority, caller uci.IndexCaller, source uciRealCorpusCapacitySource, run uciRealCorpusCapacityRun) (uciRealCorpusCapacityRecoveryBuild, error) {
	recoveryInput := uciRealCorpusCapacityBeginInput("uci-real-corpus-capacity-resume-"+uuid.NewString(), run.checkout, authority.profile.ProfileID, &run.priorContext, uci.IndexJobRecovery)
	recoveryBegin, err := run.publisher.Begin(ctx, caller, recoveryInput)
	if err != nil || recoveryBegin.Published != nil {
		return uciRealCorpusCapacityRecoveryBuild{}, errors.New("real-corpus capacity recovery build could not start")
	}
	expectedAcks, err := uciRealCorpusCapacityAcks(recoveryBegin.Build.BuildID, source.parts)
	if err != nil {
		return uciRealCorpusCapacityRecoveryBuild{}, err
	}
	expectedPartsDigest, err := uci.DigestIndexParts(expectedAcks)
	if err != nil {
		return uciRealCorpusCapacityRecoveryBuild{}, errors.New("real-corpus capacity recovery parts digest failed")
	}
	manifest := source.completion
	manifest.PartsDigest = expectedPartsDigest
	interruptedPartCount := len(source.parts) / 2
	if interruptedPartCount == 0 || interruptedPartCount >= len(source.parts) {
		return uciRealCorpusCapacityRecoveryBuild{}, errors.New("real-corpus capacity interruption boundary is invalid")
	}
	return uciRealCorpusCapacityRecoveryBuild{build: recoveryBegin.Build, expectedAcks: expectedAcks, manifest: manifest, interruptedPartCount: interruptedPartCount}, nil
}

func uciRealCorpusCapacityStageInterrupted(ctx context.Context, tx *gorm.DB, contexts *gormdb.UCIContextStore, caller uci.IndexCaller, run uciRealCorpusCapacityRun, recovery uciRealCorpusCapacityRecoveryBuild, parts []uci.IndexPart) ([]uci.IndexPartAck, uint64, error) {
	actualAcks := make([]uci.IndexPartAck, 0, len(parts))
	for sequence := range recovery.interruptedPartCount {
		ack, err := uciRealCorpusCapacityStage(ctx, run.publisher, caller, recovery.build, uint32(sequence), parts[sequence])
		if err != nil || ack != recovery.expectedAcks[sequence] {
			return nil, 0, errors.New("real-corpus capacity interrupted stage did not retain the exact part digest")
		}
		actualAcks = append(actualAcks, ack)
	}
	if _, err := run.publisher.Finalize(ctx, caller, uci.IndexFinalizeInput{Build: recovery.build, ExpectedParent: &run.priorContext, Manifest: recovery.manifest}); err == nil {
		return nil, 0, errors.New("real-corpus capacity incomplete finalize published a View")
	}
	count, err := uciRealCorpusCapacityRequireCurrentView(ctx, tx, contexts, run.checkout, run.priorView, run.priorViewCount)
	if err != nil {
		return nil, 0, errors.New("real-corpus capacity incomplete finalize changed the selected View")
	}
	return actualAcks, count, nil
}

func uciRealCorpusCapacityResumeStaging(input uciRealCorpusCapacityResumeInput) (uci.IndexStore, uci.IndexBuildRef, []uci.IndexPartAck, error) {
	publisher, err := gormdb.NewUCIProjectionStore(input.tx).Publisher(input.authorizer, uci.IndexPublicationConfig{Limits: uci.DefaultIndexPublicationLimits()})
	if err != nil {
		return nil, uci.IndexBuildRef{}, nil, errors.New("real-corpus capacity resumed publisher is unavailable")
	}
	beginInput := uciRealCorpusCapacityBeginInput("uci-real-corpus-capacity-resume-"+uuid.NewString(), input.run.checkout, input.run.profileID, &input.run.priorContext, uci.IndexJobRecovery)
	begin, err := publisher.Begin(input.ctx, input.caller, beginInput)
	if err != nil || begin.Build != input.recovery.build || begin.Published != nil {
		return nil, uci.IndexBuildRef{}, nil, errors.New("real-corpus capacity staged build did not resume")
	}
	replayedAck, err := uciRealCorpusCapacityStage(input.ctx, publisher, input.caller, begin.Build, 0, input.parts[0])
	if err != nil || replayedAck != input.actualAcks[0] {
		return nil, uci.IndexBuildRef{}, nil, errors.New("real-corpus capacity resumed stage did not replay the exact part digest")
	}
	for sequence := input.recovery.interruptedPartCount; sequence < len(input.parts); sequence++ {
		ack, err := uciRealCorpusCapacityStage(input.ctx, publisher, input.caller, begin.Build, uint32(sequence), input.parts[sequence])
		if err != nil || ack != input.recovery.expectedAcks[sequence] {
			return nil, uci.IndexBuildRef{}, nil, errors.New("real-corpus capacity resumed stage did not retain the exact part digest")
		}
		input.actualAcks = append(input.actualAcks, ack)
	}
	return publisher, begin.Build, input.actualAcks, nil
}

func uciRealCorpusCapacityRequireExactResume(actual, expected []uci.IndexPartAck, sourcePartsDigest string, legacy uciRealCorpusCapacityLegacyFailures) (string, error) {
	resumedPartsDigest, err := uciRealCorpusCapacityPartSequenceDigest(actual)
	if err != nil || !uciRealCorpusCapacitySameAcks(expected, actual) || sourcePartsDigest != resumedPartsDigest || legacy.partLimit >= len(actual) || legacy.buildLimit >= len(actual) {
		return "", errors.New("real-corpus capacity resumed part digests are not exact")
	}
	return resumedPartsDigest, nil
}

func uciRealCorpusCapacityRequireCurrentView(ctx context.Context, tx *gorm.DB, contexts *gormdb.UCIContextStore, checkout *gormdb.UCICheckout, expected *gormdb.UCIView, expectedCount uint64) (uint64, error) {
	current, err := contexts.GetCurrentView(ctx, checkout.CheckoutID)
	count, countErr := uciRealCorpusCapacityViewCount(ctx, tx, checkout)
	if err != nil || countErr != nil || count != expectedCount || !uciRealCorpusCapacitySameView(expected, current) {
		return 0, errors.New("real-corpus capacity selected View changed")
	}
	return count, nil
}

func uciRealCorpusCapacityRejectConflictingFinalize(input uciRealCorpusCapacityPublicationInput) (uint64, error) {
	conflicting := input.manifest
	digest, err := uciRealCorpusCapacityConflictingDigest(input.manifest.PartsDigest)
	if err != nil {
		return 0, err
	}
	conflicting.PartsDigest = digest
	if _, err := input.publisher.Finalize(input.ctx, input.caller, uci.IndexFinalizeInput{Build: input.build, ExpectedParent: &input.run.priorContext, Manifest: conflicting}); err == nil {
		return 0, errors.New("real-corpus capacity conflicting finalize published a View")
	}
	count, err := uciRealCorpusCapacityRequireCurrentView(input.ctx, input.tx, input.contexts, input.run.checkout, input.run.priorView, input.run.priorViewCount)
	if err != nil {
		return 0, errors.New("real-corpus capacity conflicting finalize changed the selected View")
	}
	return count, nil
}

func uciRealCorpusCapacityFinalizeAndReplay(input uciRealCorpusCapacityPublicationInput) (*gormdb.UCIView, uint64, uint64, error) {
	published, err := input.publisher.Finalize(input.ctx, input.caller, uci.IndexFinalizeInput{Build: input.build, ExpectedParent: &input.run.priorContext, Manifest: input.manifest})
	if err != nil {
		return nil, 0, 0, errors.New("real-corpus capacity resumed finalize failed")
	}
	publishedView, publishedCount, err := uciRealCorpusCapacityPublishedView(input.ctx, input.tx, input.contexts, input.run.checkout, input.run.priorViewCount+1, published)
	if err != nil {
		return nil, 0, 0, errors.New("real-corpus capacity finalize was not atomic")
	}
	replayed, err := input.publisher.Finalize(input.ctx, input.caller, uci.IndexFinalizeInput{Build: input.build, ExpectedParent: &input.run.priorContext, Manifest: input.manifest})
	if err != nil || !uciRealCorpusCapacitySamePublished(published, replayed) {
		return nil, 0, 0, errors.New("real-corpus capacity finalize did not replay one publication")
	}
	_, replayCount, err := uciRealCorpusCapacityPublishedView(input.ctx, input.tx, input.contexts, input.run.checkout, input.run.priorViewCount+1, published)
	if err != nil || replayCount != publishedCount {
		return nil, 0, 0, errors.New("real-corpus capacity finalize replay changed the selected View")
	}
	return publishedView, publishedCount, replayCount, nil
}

func uciRealCorpusCapacityPublishedView(ctx context.Context, tx *gorm.DB, contexts *gormdb.UCIContextStore, checkout *gormdb.UCICheckout, expectedCount uint64, published uci.IndexPublishedView) (*gormdb.UCIView, uint64, error) {
	view, err := contexts.GetCurrentView(ctx, checkout.CheckoutID)
	count, countErr := uciRealCorpusCapacityViewCount(ctx, tx, checkout)
	if err != nil || countErr != nil || count != expectedCount || !uciRealCorpusCapacityCurrentMatchesPublished(view, published) {
		return nil, 0, errors.New("real-corpus capacity published View is invalid")
	}
	return view, count, nil
}

func uciRealCorpusCapacityRecoveryResult(source uciRealCorpusCapacitySource, legacy uciRealCorpusCapacityLegacyFailures, run uciRealCorpusCapacityRun, outcome uciRealCorpusCapacityRecoveryOutcome) (uciRealCorpusCapacityRecovery, error) {
	limits := uci.DefaultIndexPublicationLimits()
	result := uciRealCorpusCapacityRecovery{
		FullCorpusFragmented:              true,
		LegacyPartLimitExceeded:           true,
		LegacyBuildLimitExceeded:          true,
		InterruptedStagingObserved:        true,
		ResumedStagingObserved:            true,
		ExactPartDigestsObserved:          true,
		IncompleteFinalizeRejected:        true,
		ConflictingFinalizeRejected:       true,
		AtomicFinalizeObserved:            true,
		PriorViewPreservedAfterIncomplete: true,
		PriorViewPreservedAfterConflict:   true,
		SuccessfulFinalizeObserved:        true,
		SourcePartCount:                   uint64(len(source.parts)),
		SourcePayloadBytes:                source.staged.payloadBytes,
		SourceMaxPartBytes:                source.staged.maxPayloadBytes,
		LegacyMaxPartBytes:                uint64(uciRealCorpusCapacityLegacyPartBytes),
		LegacyMaxBuildBytes:               uint64(uciRealCorpusCapacityLegacyBuildBytes),
		QuotaMaxParts:                     uint64(limits.MaxParts),
		QuotaMaxBuildBytes:                uint64(limits.MaxBuildBytes),
		QuotaMaxPartBytes:                 uint64(limits.MaxPartBytes),
		InterruptedPartCount:              uint64(outcome.interruptedPartCount),
		ResumedPartCount:                  uint64(outcome.resumedPartCount),
		PriorViewCount:                    run.priorViewCount,
		AfterIncompleteViewCount:          outcome.afterIncompleteViewCount,
		AfterConflictViewCount:            outcome.afterConflictViewCount,
		PublishedViewCount:                outcome.publishedViewCount,
		ReplayViewCount:                   outcome.replayViewCount,
		SourcePartsDigest:                 source.sourcePartsDigest,
		ResumedPartsDigest:                outcome.resumedPartsDigest,
		ManifestDigest:                    source.manifestDigest,
		PriorViewDigest:                   uciInstalledAcceptanceStringDigest(run.priorView.ViewID),
		PublishedViewDigest:               uciInstalledAcceptanceStringDigest(outcome.publishedView.ViewID),
	}
	result.AllGuaranteesObserved = result.FullCorpusFragmented && result.LegacyPartLimitExceeded && result.LegacyBuildLimitExceeded && result.InterruptedStagingObserved && result.ResumedStagingObserved && result.ExactPartDigestsObserved && result.IncompleteFinalizeRejected && result.ConflictingFinalizeRejected && result.AtomicFinalizeObserved && result.PriorViewPreservedAfterIncomplete && result.PriorViewPreservedAfterConflict && result.SuccessfulFinalizeObserved
	if !result.AllGuaranteesObserved || result.PriorViewDigest == result.PublishedViewDigest {
		return uciRealCorpusCapacityRecovery{}, errors.New("real-corpus capacity guarantees were not all observed")
	}
	return result, nil
}

func uciRealCorpusCapacityLoadParts(ctx context.Context, authority *uciInstalledAcceptanceAuthority, buildID string) ([]uci.IndexPart, []uci.IndexPartAck, error) {
	rows := make([]gormdb.UCIIndexBuildPart, 0)
	if query := authority.store.GetDB().WithContext(ctx).Where("build_id = ?", buildID).Order("sequence ASC").Find(&rows); query.Error != nil || len(rows) == 0 {
		return nil, nil, errors.New("real-corpus capacity source parts are unavailable")
	}
	parts := make([]uci.IndexPart, 0, len(rows))
	acks := make([]uci.IndexPartAck, 0, len(rows))
	for sequence, row := range rows {
		if row.Sequence != uint32(sequence) || row.PayloadBytes < 0 || !uciRealCorpusCompositionDigest(row.PartDigest) {
			return nil, nil, errors.New("real-corpus capacity source part is invalid")
		}
		var part uci.IndexPart
		if err := json.Unmarshal([]byte(row.Payload), &part); err != nil {
			return nil, nil, errors.New("real-corpus capacity source part is invalid")
		}
		encoded, err := json.Marshal(part)
		if err != nil || int64(len(encoded)) != row.PayloadBytes {
			return nil, nil, errors.New("real-corpus capacity source part bytes are invalid")
		}
		digest, err := uci.DigestIndexPart(part)
		if err != nil || string(digest) != row.PartDigest {
			return nil, nil, errors.New("real-corpus capacity source part digest is invalid")
		}
		parts = append(parts, part)
		acks = append(acks, uci.IndexPartAck{BuildID: buildID, Sequence: row.Sequence, Digest: digest})
	}
	return parts, acks, nil
}

func uciRealCorpusCapacityPartSequenceDigest(acks []uci.IndexPartAck) (string, error) {
	if len(acks) == 0 {
		return "", errors.New("real-corpus capacity part sequence is empty")
	}
	type digestEntry struct {
		Sequence uint32 `json:"sequence"`
		Digest   string `json:"digest"`
	}
	entries := make([]digestEntry, 0, len(acks))
	for sequence, ack := range acks {
		if ack.Sequence != uint32(sequence) || !uciRealCorpusCompositionDigest(string(ack.Digest)) {
			return "", errors.New("real-corpus capacity part sequence is invalid")
		}
		entries = append(entries, digestEntry{Sequence: ack.Sequence, Digest: string(ack.Digest)})
	}
	encoded, err := json.Marshal(entries)
	if err != nil {
		return "", errors.New("real-corpus capacity part sequence digest failed")
	}
	return strings.TrimPrefix(uciRealCorpusSHA256(encoded), "sha256:"), nil
}

func uciRealCorpusCapacityBareIndexDigest(value uci.IndexDigest) (string, error) {
	digest := string(value)
	if !uciRealCorpusCompositionDigest(digest) {
		return "", errors.New("real-corpus capacity digest is invalid")
	}
	return strings.TrimPrefix(digest, "sha256:"), nil
}

func uciRealCorpusCapacityRegisterCheckout(ctx context.Context, contexts *gormdb.UCIContextStore, authority *uciInstalledAcceptanceAuthority, label string) (*gormdb.UCICheckout, error) {
	if contexts == nil || authority == nil || authority.source == nil || authority.token == nil || authority.token.ID == "" || label == "" {
		return nil, errors.New("real-corpus capacity checkout authority is incomplete")
	}
	checkout, err := contexts.RegisterCheckout(ctx, gormdb.RegisterCheckoutInput{
		SourceID:       authority.source.SourceID,
		WorkstationID:  authority.token.ID,
		Kind:           gormdb.UCICheckoutWorkingTree,
		OwnerPrincipal: authority.principal,
		LocatorRef:     "file:///uci-real-corpus-capacity/" + label,
	})
	if err != nil {
		return nil, errors.New("real-corpus capacity checkout registration failed")
	}
	return checkout, nil
}

func uciRealCorpusCapacityBeginInput(buildKey string, checkout *gormdb.UCICheckout, profileID string, parent *uci.ContextRef, kind uci.IndexJobKind) uci.IndexBeginInput {
	return uci.IndexBeginInput{
		BuildKey: buildKey,
		Scope: uci.IndexScope{
			SourceID:      checkout.SourceID,
			CheckoutID:    checkout.CheckoutID,
			IncarnationID: checkout.IncarnationID,
		},
		ProfileID:      profileID,
		ExpectedParent: parent,
		Mode:           uci.IndexManifestFull,
		JobKind:        kind,
	}
}

func uciRealCorpusCapacityAcks(buildID string, parts []uci.IndexPart) ([]uci.IndexPartAck, error) {
	if buildID == "" || len(parts) == 0 {
		return nil, errors.New("real-corpus capacity acknowledgements are unavailable")
	}
	acks := make([]uci.IndexPartAck, 0, len(parts))
	for sequence, part := range parts {
		digest, err := uci.DigestIndexPart(part)
		if err != nil {
			return nil, errors.New("real-corpus capacity part digest failed")
		}
		acks = append(acks, uci.IndexPartAck{BuildID: buildID, Sequence: uint32(sequence), Digest: digest})
	}
	return acks, nil
}

func uciRealCorpusCapacityStage(ctx context.Context, publisher uci.IndexStore, caller uci.IndexCaller, build uci.IndexBuildRef, sequence uint32, part uci.IndexPart) (uci.IndexPartAck, error) {
	digest, err := uci.DigestIndexPart(part)
	if err != nil {
		return uci.IndexPartAck{}, err
	}
	return publisher.Stage(ctx, caller, uci.IndexStageInput{Build: build, Sequence: sequence, Digest: digest, Part: part})
}

func uciRealCorpusCapacityObservePartLimitFailure(ctx context.Context, publisher uci.IndexStore, caller uci.IndexCaller, build uci.IndexBuildRef, parts []uci.IndexPart, limit uint64) (int, error) {
	if limit == 0 {
		return 0, errors.New("real-corpus capacity legacy part limit is invalid")
	}
	for sequence, part := range parts {
		payloadBytes, err := uciRealCorpusCapacityPartBytes(part)
		if err != nil {
			return 0, err
		}
		if payloadBytes > limit {
			if _, stageErr := uciRealCorpusCapacityStage(ctx, publisher, caller, build, uint32(sequence), part); stageErr == nil {
				return 0, errors.New("real-corpus capacity legacy part limit accepted an oversized part")
			}
			return sequence, nil
		}
		if _, stageErr := uciRealCorpusCapacityStage(ctx, publisher, caller, build, uint32(sequence), part); stageErr != nil {
			return 0, errors.New("real-corpus capacity legacy part stage failed before its bound")
		}
	}
	return 0, errors.New("real-corpus capacity legacy part limit did not reject the full corpus")
}

func uciRealCorpusCapacityObserveBuildLimitFailure(ctx context.Context, publisher uci.IndexStore, caller uci.IndexCaller, build uci.IndexBuildRef, parts []uci.IndexPart, limit uint64) (int, error) {
	if limit == 0 {
		return 0, errors.New("real-corpus capacity legacy build limit is invalid")
	}
	var total uint64
	for sequence, part := range parts {
		payloadBytes, err := uciRealCorpusCapacityPartBytes(part)
		if err != nil || payloadBytes > limit {
			return 0, errors.New("real-corpus capacity legacy build part is invalid")
		}
		if total > limit-payloadBytes {
			if _, stageErr := uciRealCorpusCapacityStage(ctx, publisher, caller, build, uint32(sequence), part); stageErr == nil {
				return 0, errors.New("real-corpus capacity legacy build limit accepted an oversized aggregate")
			}
			return sequence, nil
		}
		if _, stageErr := uciRealCorpusCapacityStage(ctx, publisher, caller, build, uint32(sequence), part); stageErr != nil {
			return 0, errors.New("real-corpus capacity legacy build stage failed before its bound")
		}
		total += payloadBytes
	}
	return 0, errors.New("real-corpus capacity legacy build limit did not reject the full corpus")
}

func uciRealCorpusCapacityPartBytes(part uci.IndexPart) (uint64, error) {
	encoded, err := json.Marshal(part)
	if err != nil {
		return 0, errors.New("real-corpus capacity part encoding failed")
	}
	return uint64(len(encoded)), nil
}

func uciRealCorpusCapacityEmptyManifest(ack uci.IndexPartAck) (uci.IndexManifestCompletion, error) {
	partsDigest, err := uci.DigestIndexParts([]uci.IndexPartAck{ack})
	if err != nil {
		return uci.IndexManifestCompletion{}, err
	}
	manifestDigest, err := uci.DigestIndexManifest(nil)
	if err != nil {
		return uci.IndexManifestCompletion{}, err
	}
	edgesDigest, err := uci.DigestIndexEdges(nil)
	if err != nil {
		return uci.IndexManifestCompletion{}, err
	}
	started := time.Unix(1, 0).UTC()
	return uci.IndexManifestCompletion{
		PartCount:      1,
		PartsDigest:    partsDigest,
		ManifestDigest: manifestDigest,
		EdgesDigest:    edgesDigest,
		ScanOutcome:    uci.IndexScanComplete,
		CensusComplete: true,
		Observation: uci.IndexObservation{
			ObservedFSSeq: 0,
			ScanStart:     started,
			ScanEnd:       started.Add(time.Second),
		},
		Coverage: uci.IndexCoverage{
			Structural: uci.IndexCoverageComplete,
			Lexical:    uci.IndexCoverageComplete,
			Vector:     uci.IndexCoverageUnavailable,
		},
	}, nil
}

func uciRealCorpusCapacityViewCount(ctx context.Context, db *gorm.DB, checkout *gormdb.UCICheckout) (uint64, error) {
	if db == nil || checkout == nil {
		return 0, errors.New("real-corpus capacity View authority is unavailable")
	}
	var count int64
	if err := db.WithContext(ctx).Model(&gormdb.UCIView{}).Where("source_id = ? AND checkout_id = ? AND incarnation_id = ?", checkout.SourceID, checkout.CheckoutID, checkout.IncarnationID).Count(&count).Error; err != nil || count < 0 {
		return 0, errors.New("real-corpus capacity View count is unavailable")
	}
	return uint64(count), nil
}

func uciRealCorpusCapacitySameView(left, right *gormdb.UCIView) bool {
	return left != nil && right != nil && left.ViewID == right.ViewID && left.SourceID == right.SourceID && left.CheckoutID == right.CheckoutID &&
		left.IncarnationID == right.IncarnationID && left.Generation == right.Generation && left.ProfileID == right.ProfileID &&
		left.ManifestDigest == right.ManifestDigest && left.State == gormdb.UCIViewPublished && right.State == gormdb.UCIViewPublished
}

func uciRealCorpusCapacityCurrentMatchesPublished(current *gormdb.UCIView, published uci.IndexPublishedView) bool {
	return current != nil && current.State == gormdb.UCIViewPublished && current.ViewID == published.Context.ViewID &&
		current.Generation == published.Context.Generation && current.ManifestDigest == string(published.ManifestDigest)
}

func uciRealCorpusCapacitySamePublished(left, right uci.IndexPublishedView) bool {
	return left.BuildID == right.BuildID && left.Context.ViewID == right.Context.ViewID && left.Context.Generation == right.Context.Generation &&
		left.ManifestDigest == right.ManifestDigest && left.AcceptedFSSeq == right.AcceptedFSSeq && left.PublishedAt.Equal(right.PublishedAt)
}

func uciRealCorpusCapacitySameAcks(left, right []uci.IndexPartAck) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func uciRealCorpusCapacityConflictingDigest(value uci.IndexDigest) (uci.IndexDigest, error) {
	digest := string(value)
	if !uciRealCorpusCompositionDigest(digest) {
		return "", errors.New("real-corpus capacity conflicting digest is invalid")
	}
	bytes := []byte(digest)
	last := len(bytes) - 1
	if bytes[last] == '0' {
		bytes[last] = '1'
	} else {
		bytes[last] = '0'
	}
	return uci.IndexDigest(string(bytes)), nil
}

func uciRealCorpusFrozenMembershipState(file uci.ScannerFile) (uci.IndexFileState, string, error) {
	switch file.State {
	case uci.IndexFilePresent:
		if file.Exclusion != uci.ScannerExclusionNone {
			return "", "", errors.New("present scanner file has an exclusion")
		}
		if !uciRealCorpusCompositionCapabilityForPath(file.Path) {
			return uci.IndexFileExcluded, uciRealCorpusUnsupportedCapabilityCause, nil
		}
		return uci.IndexFilePresent, "", nil
	case uci.IndexFileExcluded:
		if file.Exclusion == uci.ScannerExclusionNone {
			return "", "", errors.New("excluded scanner file has no reason")
		}
		return uci.IndexFileExcluded, string(file.Exclusion), nil
	case uci.IndexFileUnreadable:
		switch file.Exclusion {
		case uci.ScannerExclusionNone:
			return uci.IndexFileUnreadable, uciRealCorpusUnreadableCause, nil
		case uci.ScannerExclusionChanging:
			return uci.IndexFileUnreadable, string(file.Exclusion), nil
		default:
			return "", "", errors.New("unreadable scanner file has an invalid reason")
		}
	default:
		return "", "", errors.New("scanner file state is unsupported")
	}
}

func uciRealCorpusCompositionCapabilityForPath(filePath string) bool {
	switch strings.ToLower(path.Ext(filePath)) {
	case ".go", ".js", ".ts", ".tsx", ".md", ".markdown", ".json", ".yaml", ".yml", ".sql":
		return true
	default:
		return false
	}
}

func uciRealCorpusProtectedSecretPath(filePath string) bool {
	normalized := strings.ToLower(strings.ReplaceAll(filePath, "\\", "/"))
	base := normalized
	if separator := strings.LastIndexByte(normalized, '/'); separator >= 0 {
		base = normalized[separator+1:]
	}
	return base == ".env" || strings.HasPrefix(base, ".env.") || base == "credentials" || base == "credentials.json" || base == "secrets" || base == "secrets.json"
}

func uciRealCorpusBuildFrozenManifest(entries []uciRealCorpusFrozenEntry) (uciRealCorpusFrozenManifest, error) {
	normalized := append([]uciRealCorpusFrozenEntry(nil), entries...)
	sort.Slice(normalized, func(left, right int) bool {
		return normalized[left].path < normalized[right].path
	})
	if len(normalized) == 0 {
		return uciRealCorpusFrozenManifest{}, errors.New("real-corpus composition scanner returned no files")
	}

	manifest := uciRealCorpusFrozenManifest{
		SchemaVersion:            uciRealCorpusCompositionSchemaVersion,
		ScannerStateHistogram:    make(map[string]uint64),
		MembershipStateHistogram: make(map[string]uint64),
		BaselineStateHistogram:   make(map[string]uint64),
		CanaryStateHistogram:     make(map[string]uint64),
		ReasonHistogram:          make(map[string]uint64),
		ScannerModeHistogram:     make(map[string]uint64),
		entries:                  normalized,
	}
	seen := make(map[string]struct{}, len(normalized))
	canaries := make(map[string]uciRealCorpusFrozenEntry, len(uciRealCorpusCanaries))
	for _, entry := range normalized {
		if err := uciRealCorpusRecordFrozenEntry(&manifest, seen, canaries, entry); err != nil {
			return uciRealCorpusFrozenManifest{}, err
		}
	}
	for canaryPath, body := range uciRealCorpusCanaries {
		entry, found := canaries[canaryPath]
		if !found || entry.scannerState != uci.IndexFilePresent || entry.membershipState != uci.IndexFilePresent || entry.reason != "" || entry.scannerMode != uciRealCorpusScannerModeUnavailable || entry.contentDigest != uciRealCorpusSHA256([]byte(body)) {
			return uciRealCorpusFrozenManifest{}, errors.New("real-corpus composition canary delta is incomplete")
		}
	}
	if manifest.CanaryEntryCount != uint64(len(uciRealCorpusCanaries)) {
		return uciRealCorpusFrozenManifest{}, errors.New("real-corpus composition canary delta is incomplete")
	}

	policyDigest, err := uciRealCorpusCompositionPolicyDigest()
	if err != nil {
		return uciRealCorpusFrozenManifest{}, errors.New("real-corpus composition scanner policy digest failed")
	}
	manifestDigest, err := uciRealCorpusFrozenEntriesDigest(normalized)
	if err != nil {
		return uciRealCorpusFrozenManifest{}, errors.New("real-corpus composition manifest digest failed")
	}
	baselineDigest, err := uciRealCorpusFrozenEntriesDigest(uciRealCorpusFrozenSubset(normalized, false))
	if err != nil {
		return uciRealCorpusFrozenManifest{}, errors.New("real-corpus composition baseline digest failed")
	}
	canaryDigest, err := uciRealCorpusFrozenEntriesDigest(uciRealCorpusFrozenSubset(normalized, true))
	if err != nil {
		return uciRealCorpusFrozenManifest{}, errors.New("real-corpus composition canary digest failed")
	}
	manifest.ScannerPolicyDigest = policyDigest
	manifest.ManifestDigest = manifestDigest
	manifest.BaselineManifestDigest = baselineDigest
	manifest.CanaryDeltaDigest = canaryDigest
	return manifest, nil
}

func uciRealCorpusRecordFrozenEntry(manifest *uciRealCorpusFrozenManifest, seen map[string]struct{}, canaries map[string]uciRealCorpusFrozenEntry, entry uciRealCorpusFrozenEntry) error {
	if !uciRealCorpusCompositionPath(entry.path) || entry.scannerMode == "" {
		return errors.New("real-corpus composition frozen path is invalid")
	}
	if _, found := seen[entry.path]; found {
		return errors.New("real-corpus composition scanner repeated a path")
	}
	seen[entry.path] = struct{}{}
	if entry.scannerState == uci.IndexFilePresent {
		if !uciRealCorpusCompositionDigest(entry.contentDigest) {
			return errors.New("real-corpus composition present file has no digest")
		}
	} else if entry.contentDigest != "" {
		return errors.New("real-corpus composition non-present scanner file carries a digest")
	}
	if _, knownCanary := uciRealCorpusCanaries[entry.path]; entry.canary != knownCanary {
		return errors.New("real-corpus composition canary classification is invalid")
	}
	manifest.EntryCount++
	manifest.ScannerStateHistogram[string(entry.scannerState)]++
	manifest.MembershipStateHistogram[string(entry.membershipState)]++
	manifest.ScannerModeHistogram[entry.scannerMode]++
	if entry.reason != "" {
		manifest.ReasonHistogram[entry.reason]++
	}
	if entry.canary {
		manifest.CanaryEntryCount++
		manifest.CanaryStateHistogram[string(entry.membershipState)]++
		canaries[entry.path] = entry
		return nil
	}
	manifest.BaselineEntryCount++
	manifest.BaselineStateHistogram[string(entry.membershipState)]++
	return nil
}

func uciRealCorpusValidateFrozenManifest(frozen uciRealCorpusFrozenManifest) error {
	actual, err := uciRealCorpusBuildFrozenManifest(frozen.entries)
	if err != nil {
		return err
	}
	if frozen.SchemaVersion != actual.SchemaVersion ||
		frozen.ScannerPolicyDigest != actual.ScannerPolicyDigest ||
		frozen.ManifestDigest != actual.ManifestDigest ||
		frozen.BaselineManifestDigest != actual.BaselineManifestDigest ||
		frozen.CanaryDeltaDigest != actual.CanaryDeltaDigest ||
		frozen.EntryCount != actual.EntryCount ||
		frozen.BaselineEntryCount != actual.BaselineEntryCount ||
		frozen.CanaryEntryCount != actual.CanaryEntryCount ||
		!uciRealCorpusSameHistogram(frozen.ScannerStateHistogram, actual.ScannerStateHistogram) ||
		!uciRealCorpusSameHistogram(frozen.MembershipStateHistogram, actual.MembershipStateHistogram) ||
		!uciRealCorpusSameHistogram(frozen.BaselineStateHistogram, actual.BaselineStateHistogram) ||
		!uciRealCorpusSameHistogram(frozen.CanaryStateHistogram, actual.CanaryStateHistogram) ||
		!uciRealCorpusSameHistogram(frozen.ReasonHistogram, actual.ReasonHistogram) ||
		!uciRealCorpusSameHistogram(frozen.ScannerModeHistogram, actual.ScannerModeHistogram) {
		return errors.New("real-corpus composition frozen manifest is inconsistent")
	}
	return nil
}

func uciRealCorpusCompositionPolicyDigest() (string, error) {
	return uciRealCorpusDigestValue(struct {
		Version              string   `json:"version"`
		IncludeUntracked     bool     `json:"include_untracked"`
		IncludeIgnored       bool     `json:"include_ignored"`
		ProtectedPaths       []string `json:"protected_paths"`
		Capabilities         []string `json:"capabilities"`
		ExcludedCapabilities []string `json:"excluded_capabilities"`
		SecretDetector       string   `json:"secret_detector"`
	}{
		Version:              uciRealCorpusCompositionSchemaVersion,
		IncludeUntracked:     true,
		IncludeIgnored:       false,
		ProtectedPaths:       append([]string(nil), uciRealCorpusCompositionProtectedPaths[:]...),
		Capabilities:         append([]string(nil), uciRealCorpusCompositionCapabilities[:]...),
		ExcludedCapabilities: append([]string(nil), uciRealCorpusCompositionExcludedCapabilities[:]...),
		SecretDetector:       "runtime-protected-secret-path+privacy-contains-secrets",
	})
}

func uciRealCorpusFrozenEntriesDigest(entries []uciRealCorpusFrozenEntry) (string, error) {
	type digestEntry struct {
		Path            string `json:"path"`
		ScannerState    string `json:"scanner_state"`
		MembershipState string `json:"membership_state"`
		Reason          string `json:"reason"`
		ScannerMode     string `json:"scanner_mode"`
		ContentDigest   string `json:"content_digest"`
		Canary          bool   `json:"canary"`
	}
	values := make([]digestEntry, 0, len(entries))
	for _, entry := range entries {
		values = append(values, digestEntry{
			Path:            entry.path,
			ScannerState:    string(entry.scannerState),
			MembershipState: string(entry.membershipState),
			Reason:          entry.reason,
			ScannerMode:     entry.scannerMode,
			ContentDigest:   entry.contentDigest,
			Canary:          entry.canary,
		})
	}
	sort.Slice(values, func(left, right int) bool {
		return values[left].Path < values[right].Path
	})
	return uciRealCorpusDigestValue(struct {
		Version string        `json:"version"`
		Entries []digestEntry `json:"entries"`
	}{Version: uciRealCorpusCompositionSchemaVersion, Entries: values})
}

func uciRealCorpusFrozenSubset(entries []uciRealCorpusFrozenEntry, canary bool) []uciRealCorpusFrozenEntry {
	result := make([]uciRealCorpusFrozenEntry, 0, len(entries))
	for _, entry := range entries {
		if entry.canary == canary {
			result = append(result, entry)
		}
	}
	return result
}

func uciRealCorpusValidateCompositionAuthority(authority *uciInstalledAcceptanceAuthority, publication uciInstalledAcceptancePublication) error {
	if authority == nil || authority.store == nil || authority.source == nil || authority.profile == nil || publication.sourceID == "" || publication.checkoutID == "" || publication.viewID == "" || publication.profileID == "" || publication.generation < 1 {
		return errors.New("real-corpus composition authority is incomplete")
	}
	checkout := authority.checkouts[uciInstalledAcceptanceClientA]
	if checkout == nil || publication.sourceID != authority.source.SourceID || publication.checkoutID != checkout.CheckoutID || publication.profileID != authority.profile.ProfileID {
		return errors.New("real-corpus composition publication authority does not match")
	}
	return nil
}

func uciRealCorpusLoadCompositionPublication(ctx context.Context, authority *uciInstalledAcceptanceAuthority, publication uciInstalledAcceptancePublication) (uciRealCorpusCompositionPublicationRow, uci.IndexManifestCompletion, error) {
	rows := make([]uciRealCorpusCompositionPublicationRow, 0, 1)
	db := authority.store.GetDB().WithContext(ctx)
	query := db.Raw(`
		SELECT job.job_id AS build_id,
		       job.manifest_mode AS manifest_mode,
		       job.sealed_manifest::text AS sealed_manifest,
		       view_row.manifest_digest AS view_manifest_digest,
		       view_row.state AS view_state
		FROM ci_views AS view_row
		JOIN ci_jobs AS job ON job.result_view_id = view_row.view_id
		WHERE view_row.view_id = ?
		  AND view_row.source_id = ?
		  AND view_row.checkout_id = ?
		  AND view_row.profile_id = ?
		  AND view_row.generation = ?
		  AND job.source_id = ?
		  AND job.checkout_id = ?
		  AND job.profile_id = ?
		  AND job.state = 'succeeded'
		  AND job.sealed_manifest IS NOT NULL
		ORDER BY job.updated_at ASC, job.job_id ASC
	`, publication.viewID, publication.sourceID, publication.checkoutID, publication.profileID, publication.generation, publication.sourceID, publication.checkoutID, publication.profileID)
	if query.Error != nil || query.Scan(&rows).Error != nil || len(rows) != 1 {
		return uciRealCorpusCompositionPublicationRow{}, uci.IndexManifestCompletion{}, errors.New("real-corpus composition publication is unavailable")
	}
	row := rows[0]
	if row.BuildID == "" || row.ManifestMode != string(uci.IndexManifestFull) || row.SealedManifest == "" || !uciRealCorpusCompositionDigest(row.ViewManifestDigest) || (row.ViewState != "published" && row.ViewState != "superseded") {
		return uciRealCorpusCompositionPublicationRow{}, uci.IndexManifestCompletion{}, errors.New("real-corpus composition publication is invalid")
	}
	var completion uci.IndexManifestCompletion
	if err := json.Unmarshal([]byte(row.SealedManifest), &completion); err != nil {
		return uciRealCorpusCompositionPublicationRow{}, uci.IndexManifestCompletion{}, errors.New("real-corpus composition sealed manifest is invalid")
	}
	return row, completion, nil
}

type uciRealCorpusStagedBuildAccumulator struct {
	staged       *uciRealCorpusCompositionStagedBuild
	buildID      string
	acks         []uci.IndexPartAck
	memberships  []uci.IndexMembership
	replacements []uci.IndexEdgeReplacement
}

func uciRealCorpusLoadStagedBuild(ctx context.Context, authority *uciInstalledAcceptanceAuthority, buildID string) (uciRealCorpusCompositionStagedBuild, error) {
	rows := make([]uciRealCorpusCompositionPartRow, 0)
	query := authority.store.GetDB().WithContext(ctx).Raw(`
		SELECT sequence, part_digest, payload::text AS payload, payload_bytes
		FROM ci_index_build_parts
		WHERE build_id = ?
		ORDER BY sequence ASC
	`, buildID)
	if query.Error != nil || query.Scan(&rows).Error != nil || len(rows) == 0 {
		return uciRealCorpusCompositionStagedBuild{}, errors.New("real-corpus composition staged parts are unavailable")
	}

	staged := uciRealCorpusCompositionStagedBuild{
		memberships:    make(map[string]uci.IndexMembership),
		artifactProofs: make(map[string]uci.IndexArtifactProof),
	}
	accumulator := uciRealCorpusStagedBuildAccumulator{
		staged:       &staged,
		buildID:      buildID,
		acks:         make([]uci.IndexPartAck, 0, len(rows)),
		memberships:  make([]uci.IndexMembership, 0),
		replacements: make([]uci.IndexEdgeReplacement, 0),
	}
	for sequence, row := range rows {
		if err := accumulator.append(row, sequence); err != nil {
			return uciRealCorpusCompositionStagedBuild{}, err
		}
	}
	if err := accumulator.finish(); err != nil {
		return uciRealCorpusCompositionStagedBuild{}, err
	}
	return staged, nil
}

func (accumulator *uciRealCorpusStagedBuildAccumulator) append(row uciRealCorpusCompositionPartRow, sequence int) error {
	part, digest, payloadBytes, err := uciRealCorpusDecodeStagedPart(row, sequence)
	if err != nil {
		return err
	}
	if err := accumulator.appendMemberships(part.Memberships); err != nil {
		return err
	}
	if err := accumulator.appendProofs(part.Artifacts); err != nil {
		return err
	}
	if err := accumulator.appendEdges(part.EdgeReplacements); err != nil {
		return err
	}
	return accumulator.appendPart(payloadBytes, uint32(sequence), digest)
}

func uciRealCorpusDecodeStagedPart(row uciRealCorpusCompositionPartRow, sequence int) (uci.IndexPart, uci.IndexDigest, uint64, error) {
	if row.Sequence != int64(sequence) || row.PayloadBytes < 0 || !uciRealCorpusCompositionDigest(row.PartDigest) {
		return uci.IndexPart{}, "", 0, errors.New("real-corpus composition staged part is invalid")
	}
	payloadBytes := uint64(row.PayloadBytes)
	var part uci.IndexPart
	if err := json.Unmarshal([]byte(row.Payload), &part); err != nil {
		return uci.IndexPart{}, "", 0, errors.New("real-corpus composition staged part is invalid")
	}
	encodedPart, err := json.Marshal(part)
	if err != nil || payloadBytes != uint64(len(encodedPart)) {
		return uci.IndexPart{}, "", 0, errors.New("real-corpus composition staged payload bytes do not match the encoded part")
	}
	digest, err := uci.DigestIndexPart(part)
	if err != nil || string(digest) != row.PartDigest {
		return uci.IndexPart{}, "", 0, errors.New("real-corpus composition staged part digest does not match")
	}
	if len(part.Deletions) != 0 {
		return uci.IndexPart{}, "", 0, errors.New("real-corpus composition full publication contains deletions")
	}
	return part, digest, payloadBytes, nil
}

func (accumulator *uciRealCorpusStagedBuildAccumulator) appendMemberships(memberships []uci.IndexMembership) error {
	for _, membership := range memberships {
		if _, found := accumulator.staged.memberships[membership.PathKey]; found {
			return errors.New("real-corpus composition staged publication repeats a path")
		}
		accumulator.staged.memberships[membership.PathKey] = membership
		accumulator.memberships = append(accumulator.memberships, membership)
	}
	return nil
}

func (accumulator *uciRealCorpusStagedBuildAccumulator) appendProofs(proofs []uci.IndexArtifactProof) error {
	for _, proof := range proofs {
		if prior, found := accumulator.staged.artifactProofs[proof.ArtifactID]; found && prior != proof {
			return errors.New("real-corpus composition staged artifact proof conflicts")
		}
		accumulator.staged.artifactProofs[proof.ArtifactID] = proof
	}
	return nil
}

func (accumulator *uciRealCorpusStagedBuildAccumulator) appendEdges(replacements []uci.IndexEdgeReplacement) error {
	for _, replacement := range replacements {
		if ^uint64(0)-accumulator.staged.edgeCount < uint64(len(replacement.Edges)) {
			return errors.New("real-corpus composition staged edge count overflow")
		}
		accumulator.staged.edgeCount += uint64(len(replacement.Edges))
		accumulator.replacements = append(accumulator.replacements, replacement)
	}
	return nil
}

func (accumulator *uciRealCorpusStagedBuildAccumulator) appendPart(payloadBytes uint64, sequence uint32, digest uci.IndexDigest) error {
	if ^uint64(0)-accumulator.staged.payloadBytes < payloadBytes {
		return errors.New("real-corpus composition staged payload count overflow")
	}
	accumulator.staged.payloadBytes += payloadBytes
	if payloadBytes > accumulator.staged.maxPayloadBytes {
		accumulator.staged.maxPayloadBytes = payloadBytes
	}
	accumulator.staged.partCount++
	accumulator.acks = append(accumulator.acks, uci.IndexPartAck{BuildID: accumulator.buildID, Sequence: sequence, Digest: digest})
	return nil
}

func (accumulator *uciRealCorpusStagedBuildAccumulator) finish() error {
	partsDigest, err := uci.DigestIndexParts(accumulator.acks)
	if err != nil {
		return errors.New("real-corpus composition staged parts digest failed")
	}
	manifestDigest, err := uci.DigestIndexManifest(accumulator.memberships)
	if err != nil {
		return errors.New("real-corpus composition reconstructed manifest is invalid")
	}
	edgesDigest, err := uci.DigestIndexEdges(accumulator.replacements)
	if err != nil {
		return errors.New("real-corpus composition reconstructed edges are invalid")
	}
	accumulator.staged.partsDigest = string(partsDigest)
	accumulator.staged.manifestDigest = string(manifestDigest)
	accumulator.staged.edgesDigest = string(edgesDigest)
	return nil
}

func uciRealCorpusObservePackedPartCapacity(staged uciRealCorpusCompositionStagedBuild) (uciRealCorpusPackedPartCapacity, error) {
	limits := uci.DefaultIndexPublicationLimits()
	if limits.MaxParts == 0 || limits.MaxBuildBytes <= 0 || limits.MaxPartBytes <= 0 {
		return uciRealCorpusPackedPartCapacity{}, errors.New("real-corpus configured publication limits are not finite")
	}
	packing := uciRealCorpusPackedPartCapacity{
		ObservedPartCount:           staged.partCount,
		ObservedTotalEncodedBytes:   staged.payloadBytes,
		ObservedMaxEncodedPartBytes: staged.maxPayloadBytes,
		ConfiguredMaxPartCount:      uint64(limits.MaxParts),
		ConfiguredMaxTotalBytes:     uint64(limits.MaxBuildBytes),
		ConfiguredMaxPartBytes:      uint64(limits.MaxPartBytes),
	}
	if packing.ObservedPartCount == 0 || packing.ObservedMaxEncodedPartBytes == 0 || packing.ObservedMaxEncodedPartBytes > packing.ObservedTotalEncodedBytes {
		return uciRealCorpusPackedPartCapacity{}, errors.New("real-corpus observed packed part measurements are invalid")
	}
	if packing.ObservedPartCount > packing.ConfiguredMaxPartCount || packing.ObservedTotalEncodedBytes > packing.ConfiguredMaxTotalBytes || packing.ObservedMaxEncodedPartBytes > packing.ConfiguredMaxPartBytes {
		return uciRealCorpusPackedPartCapacity{}, errors.New("real-corpus observed packed parts exceed configured publication limits")
	}
	return packing, nil
}

func uciRealCorpusValidateStagedCompletion(row uciRealCorpusCompositionPublicationRow, completion uci.IndexManifestCompletion, staged uciRealCorpusCompositionStagedBuild) error {
	if completion.PartCount == 0 || uint64(completion.PartCount) != staged.partCount ||
		completion.EntryCount != uint64(len(staged.memberships)) ||
		completion.EdgeCount != staged.edgeCount ||
		completion.ScanOutcome != uci.IndexScanComplete || !completion.CensusComplete ||
		completion.Coverage.Structural != uci.IndexCoverageComplete ||
		completion.Observation.ObservedFSSeq < 0 || completion.Observation.ScanStart.IsZero() || completion.Observation.ScanEnd.IsZero() ||
		!uciRealCorpusCompositionDigest(string(completion.PartsDigest)) ||
		!uciRealCorpusCompositionDigest(string(completion.ManifestDigest)) ||
		!uciRealCorpusCompositionDigest(string(completion.EdgesDigest)) {
		return errors.New("real-corpus composition sealed manifest is incomplete")
	}
	if string(completion.PartsDigest) != staged.partsDigest || string(completion.ManifestDigest) != staged.manifestDigest || string(completion.EdgesDigest) != staged.edgesDigest || row.ViewManifestDigest != staged.manifestDigest {
		return errors.New("real-corpus composition manifest digest does not match the staged publication")
	}
	return nil
}

func uciRealCorpusLoadTemporalMemberships(ctx context.Context, authority *uciInstalledAcceptanceAuthority, publication uciInstalledAcceptancePublication) (map[string]uciRealCorpusCompositionMembershipRow, error) {
	rows := make([]uciRealCorpusCompositionMembershipRow, 0)
	query := authority.store.GetDB().WithContext(ctx).Raw(`
		SELECT path_key, display_path, mode, file_state, artifact_id::text AS artifact_id
		FROM ci_memberships
		WHERE source_id = ?
		  AND checkout_id = ?
		  AND valid_from_generation <= ?
		  AND (valid_to_generation IS NULL OR valid_to_generation > ?)
		ORDER BY path_key ASC
	`, publication.sourceID, publication.checkoutID, publication.generation, publication.generation)
	if query.Error != nil || query.Scan(&rows).Error != nil {
		return nil, errors.New("real-corpus composition temporal memberships are unavailable")
	}
	memberships := make(map[string]uciRealCorpusCompositionMembershipRow, len(rows))
	for _, row := range rows {
		if !uciRealCorpusCompositionPath(row.PathKey) || row.DisplayPath != row.PathKey || row.Mode == "" {
			return nil, errors.New("real-corpus composition temporal membership is invalid")
		}
		if _, found := memberships[row.PathKey]; found {
			return nil, errors.New("real-corpus composition temporal membership repeats a path")
		}
		memberships[row.PathKey] = row
	}
	return memberships, nil
}

func uciRealCorpusVerifyMembershipProjection(entries []uciRealCorpusFrozenEntry, staged map[string]uci.IndexMembership, persisted map[string]uciRealCorpusCompositionMembershipRow, proofs map[string]uci.IndexArtifactProof) error {
	if len(entries) != len(staged) || len(entries) != len(persisted) {
		return errors.New("real-corpus composition membership path set does not match the frozen census")
	}
	expectedPaths := make(map[string]struct{}, len(entries))
	usedArtifacts := make(map[string]struct{})
	for _, entry := range entries {
		if _, duplicate := expectedPaths[entry.path]; duplicate {
			return errors.New("real-corpus composition frozen census repeats a path")
		}
		expectedPaths[entry.path] = struct{}{}
		stagedMembership, stagedFound := staged[entry.path]
		persistedMembership, persistedFound := persisted[entry.path]
		if !stagedFound || !persistedFound {
			return errors.New("real-corpus composition membership path is missing")
		}
		if err := uciRealCorpusVerifyMembership(entry, stagedMembership, persistedMembership, proofs, usedArtifacts); err != nil {
			return err
		}
	}
	return uciRealCorpusVerifyMembershipPathSets(expectedPaths, staged, persisted, proofs, usedArtifacts)
}

func uciRealCorpusVerifyMembershipPathSets(expected map[string]struct{}, staged map[string]uci.IndexMembership, persisted map[string]uciRealCorpusCompositionMembershipRow, proofs map[string]uci.IndexArtifactProof, usedArtifacts map[string]struct{}) error {
	for pathKey := range staged {
		if _, found := expected[pathKey]; !found {
			return errors.New("real-corpus composition staged publication contains a foreign path")
		}
	}
	for pathKey := range persisted {
		if _, found := expected[pathKey]; !found {
			return errors.New("real-corpus composition temporal projection contains a foreign path")
		}
	}
	for artifactID := range proofs {
		if _, used := usedArtifacts[artifactID]; !used {
			return errors.New("real-corpus composition staged artifact proof is unused")
		}
	}
	return nil
}

func uciRealCorpusVerifyMembership(entry uciRealCorpusFrozenEntry, staged uci.IndexMembership, persisted uciRealCorpusCompositionMembershipRow, proofs map[string]uci.IndexArtifactProof, usedArtifacts map[string]struct{}) error {
	if staged.PathKey != entry.path || staged.DisplayPath != entry.path || persisted.PathKey != entry.path || persisted.DisplayPath != entry.path ||
		staged.Mode != uciRealCorpusPreparedMembershipMode || persisted.Mode != uciRealCorpusPreparedMembershipMode ||
		staged.State != entry.membershipState || persisted.FileState != string(entry.membershipState) {
		return errors.New("real-corpus composition membership state does not match the frozen census")
	}
	if entry.membershipState != uci.IndexFilePresent {
		if staged.ArtifactID != nil || persisted.ArtifactID.Valid {
			return errors.New("real-corpus composition non-present membership carries an artifact")
		}
		return nil
	}
	if !uciRealCorpusCompositionDigest(entry.contentDigest) || staged.ArtifactID == nil || !persisted.ArtifactID.Valid || *staged.ArtifactID != persisted.ArtifactID.String {
		return errors.New("real-corpus composition present membership artifact does not match")
	}
	proof, found := proofs[*staged.ArtifactID]
	if !found || string(proof.ContentDigest) != entry.contentDigest {
		return errors.New("real-corpus composition present membership content digest does not match")
	}
	usedArtifacts[*staged.ArtifactID] = struct{}{}
	return nil
}

func uciRealCorpusVerifyArtifactProofs(ctx context.Context, authority *uciInstalledAcceptanceAuthority, proofs map[string]uci.IndexArtifactProof) error {
	projection := gormdb.NewUCIProjectionStore(authority.store.GetDB())
	for _, artifactID := range uciRealCorpusSortedProofIDs(proofs) {
		if err := ctx.Err(); err != nil {
			return err
		}
		actual, err := projection.DescribeIndexArtifact(ctx, authority.source.SourceID, artifactID)
		if err != nil || actual != proofs[artifactID] {
			return errors.New("real-corpus composition durable artifact proof does not match")
		}
	}
	return nil
}

func uciRealCorpusArtifactProofDigest(proofs map[string]uci.IndexArtifactProof) (string, error) {
	values := make([]uci.IndexArtifactProof, 0, len(proofs))
	for _, artifactID := range uciRealCorpusSortedProofIDs(proofs) {
		values = append(values, proofs[artifactID])
	}
	return uciRealCorpusDigestValue(struct {
		Version string                   `json:"version"`
		Proofs  []uci.IndexArtifactProof `json:"proofs"`
	}{Version: uciRealCorpusCompositionSchemaVersion, Proofs: values})
}

func uciRealCorpusSortedProofIDs(proofs map[string]uci.IndexArtifactProof) []string {
	ids := make([]string, 0, len(proofs))
	for artifactID := range proofs {
		ids = append(ids, artifactID)
	}
	sort.Strings(ids)
	return ids
}

func uciRealCorpusPersistedModeHistogram(persisted map[string]uciRealCorpusCompositionMembershipRow) map[string]uint64 {
	histogram := make(map[string]uint64)
	for _, row := range persisted {
		histogram[row.Mode]++
	}
	return histogram
}

func uciRealCorpusCloneHistogram(source map[string]uint64) map[string]uint64 {
	clone := make(map[string]uint64, len(source))
	for key, value := range source {
		clone[key] = value
	}
	return clone
}

func uciRealCorpusSameHistogram(left, right map[string]uint64) bool {
	if len(left) != len(right) {
		return false
	}
	for key, value := range left {
		if right[key] != value {
			return false
		}
	}
	return true
}

func uciRealCorpusDigestValue(value any) (string, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	return uciRealCorpusSHA256(encoded), nil
}

func uciRealCorpusSHA256(value []byte) string {
	sum := sha256.Sum256(value)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func uciRealCorpusCompositionDigest(value string) bool {
	if !strings.HasPrefix(value, "sha256:") || len(value) != len("sha256:")+sha256.Size*2 {
		return false
	}
	_, err := hex.DecodeString(strings.TrimPrefix(value, "sha256:"))
	return err == nil
}

func uciRealCorpusCompositionPath(value string) bool {
	return value != "" && value != "." && value != ".." && strings.IndexByte(value, 0) < 0 && !strings.Contains(value, "\\") && !strings.HasPrefix(value, "/") && !strings.HasSuffix(value, "/") && path.Clean(value) == value && !strings.Contains(value, ":")
}

func uciRealCorpusGitMode(value string) bool {
	if len(value) != 6 {
		return false
	}
	for index := range value {
		if value[index] < '0' || value[index] > '7' {
			return false
		}
	}
	return true
}

func uciRealCorpusStageInvocation(args []string) bool {
	for index := range args {
		if args[index] != "ls-files" {
			continue
		}
		seenStage, seenNUL := false, false
		for _, argument := range args[index+1:] {
			switch argument {
			case "--stage":
				seenStage = true
			case "-z":
				seenNUL = true
			}
		}
		return seenStage && seenNUL
	}
	return false
}

func TestUCIRealCorpusCapacityPartSequenceDigestIsReceiptSafe(t *testing.T) {
	partDigest := uci.IndexDigest(uciRealCorpusSHA256([]byte("part")))
	digest, err := uciRealCorpusCapacityPartSequenceDigest([]uci.IndexPartAck{{Sequence: 0, Digest: partDigest}})
	if err != nil {
		t.Fatal(err)
	}
	if !uciInstalledAcceptanceIsSHA256(digest) {
		t.Fatalf("capacity receipt digest = %q, want bare SHA-256", digest)
	}
}

func TestUCIRealCorpusFrozenManifestSeparatesCanaryDelta(t *testing.T) {
	entries := []uciRealCorpusFrozenEntry{{
		path:            "main.go",
		scannerState:    uci.IndexFilePresent,
		membershipState: uci.IndexFilePresent,
		scannerMode:     "100644",
		contentDigest:   uciRealCorpusSHA256([]byte("package main\n")),
	}}
	for canaryPath, body := range uciRealCorpusCanaries {
		entries = append(entries, uciRealCorpusFrozenEntry{
			path:            canaryPath,
			scannerState:    uci.IndexFilePresent,
			membershipState: uci.IndexFilePresent,
			scannerMode:     uciRealCorpusScannerModeUnavailable,
			contentDigest:   uciRealCorpusSHA256([]byte(body)),
			canary:          true,
		})
	}
	manifest, err := uciRealCorpusBuildFrozenManifest(entries)
	if err != nil {
		t.Fatal(err)
	}
	if manifest.EntryCount != uint64(len(entries)) || manifest.BaselineEntryCount != 1 || manifest.CanaryEntryCount != uint64(len(uciRealCorpusCanaries)) {
		t.Fatalf("manifest counts = entries=%d baseline=%d canary=%d", manifest.EntryCount, manifest.BaselineEntryCount, manifest.CanaryEntryCount)
	}
	if manifest.ManifestDigest == manifest.BaselineManifestDigest || manifest.ManifestDigest == manifest.CanaryDeltaDigest || manifest.BaselineManifestDigest == manifest.CanaryDeltaDigest {
		t.Fatal("full, baseline, and canary digests must remain distinct")
	}
	if manifest.BaselineStateHistogram[string(uci.IndexFilePresent)] != 1 || manifest.CanaryStateHistogram[string(uci.IndexFilePresent)] != uint64(len(uciRealCorpusCanaries)) {
		t.Fatalf("manifest state histograms = %#v / %#v", manifest.BaselineStateHistogram, manifest.CanaryStateHistogram)
	}
}

func TestUCIRealCorpusFreezeIncludesUntrackedCanaryDelta(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "baseline.go"), []byte("package fixture\n"), 0o600); err != nil {
		t.Fatal("write real-corpus scanner fixture")
	}
	runGit := func(args ...string) {
		command := exec.Command("git", args...)
		command.Dir = root
		if err := command.Run(); err != nil {
			t.Fatal("initialize real-corpus scanner fixture")
		}
	}
	runGit("init", "--quiet")
	runGit("add", "--", "baseline.go")
	if err := uciRealCorpusWriteCanaries(root); err != nil {
		t.Fatal("write real-corpus canary fixture")
	}
	t.Cleanup(func() { _ = uciRealCorpusRemoveCanaries(root) })

	manifest, err := uciFreezeRealCorpusManifest(context.Background(), root, "semanticallydisjointtoken")
	if err != nil {
		t.Fatal(err)
	}
	if manifest.BaselineEntryCount != 1 || manifest.CanaryEntryCount != uint64(len(uciRealCorpusCanaries)) {
		t.Fatalf("frozen scanner counts = baseline=%d canary=%d", manifest.BaselineEntryCount, manifest.CanaryEntryCount)
	}
	canaries := make(map[string]uciRealCorpusFrozenEntry, len(uciRealCorpusCanaries))
	for _, entry := range manifest.entries {
		if entry.canary {
			canaries[entry.path] = entry
		}
	}
	for canaryPath := range uciRealCorpusCanaries {
		entry, found := canaries[canaryPath]
		if !found || entry.scannerMode != uciRealCorpusScannerModeUnavailable || entry.scannerState != uci.IndexFilePresent || entry.membershipState != uci.IndexFilePresent {
			t.Fatalf("untracked canary %q did not remain an explicit present delta", canaryPath)
		}
	}
	if _, err := uciFreezeRealCorpusManifest(context.Background(), root, "package"); err == nil {
		t.Fatal("corpus-overlapping semantic query was accepted")
	}
}

func TestUCIRealCorpusFrozenMembershipStateMapsProductionOutcomes(t *testing.T) {
	tests := []struct {
		name       string
		file       uci.ScannerFile
		wantState  uci.IndexFileState
		wantReason string
	}{
		{
			name:       "unsupported capability",
			file:       uci.ScannerFile{Path: "notes.txt", State: uci.IndexFilePresent},
			wantState:  uci.IndexFileExcluded,
			wantReason: uciRealCorpusUnsupportedCapabilityCause,
		},
		{
			name:       "scanner exclusion",
			file:       uci.ScannerFile{Path: ".env", State: uci.IndexFileExcluded, Exclusion: uci.ScannerExclusionProtected},
			wantState:  uci.IndexFileExcluded,
			wantReason: string(uci.ScannerExclusionProtected),
		},
		{
			name:       "changing file",
			file:       uci.ScannerFile{Path: "changing.go", State: uci.IndexFileUnreadable, Exclusion: uci.ScannerExclusionChanging},
			wantState:  uci.IndexFileUnreadable,
			wantReason: string(uci.ScannerExclusionChanging),
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			state, reason, err := uciRealCorpusFrozenMembershipState(test.file)
			if err != nil || state != test.wantState || reason != test.wantReason {
				t.Fatalf("state, reason, err = %q, %q, %v", state, reason, err)
			}
		})
	}
}

func TestUCIRealCorpusMembershipProjectionRejectsStateArtifactAndDigestMismatch(t *testing.T) {
	artifactID := "11111111-1111-4111-8111-111111111111"
	contentDigest := uciRealCorpusSHA256([]byte("package fixture\n"))
	factsDigest := uciRealCorpusSHA256([]byte("facts"))
	entry := uciRealCorpusFrozenEntry{
		path:            "fixture.go",
		scannerState:    uci.IndexFilePresent,
		membershipState: uci.IndexFilePresent,
		scannerMode:     "100644",
		contentDigest:   contentDigest,
	}
	projection := func() (map[string]uci.IndexMembership, map[string]uciRealCorpusCompositionMembershipRow, map[string]uci.IndexArtifactProof) {
		return map[string]uci.IndexMembership{
				"fixture.go": {PathKey: "fixture.go", DisplayPath: "fixture.go", Mode: uciRealCorpusPreparedMembershipMode, State: uci.IndexFilePresent, ArtifactID: &artifactID},
			}, map[string]uciRealCorpusCompositionMembershipRow{
				"fixture.go": {PathKey: "fixture.go", DisplayPath: "fixture.go", Mode: uciRealCorpusPreparedMembershipMode, FileState: string(uci.IndexFilePresent), ArtifactID: sql.NullString{String: artifactID, Valid: true}},
			}, map[string]uci.IndexArtifactProof{
				artifactID: {ArtifactID: artifactID, ContentDigest: uci.IndexDigest(contentDigest), FactsDigest: uci.IndexDigest(factsDigest)},
			}
	}
	staged, persisted, proofs := projection()
	if err := uciRealCorpusVerifyMembershipProjection([]uciRealCorpusFrozenEntry{entry}, staged, persisted, proofs); err != nil {
		t.Fatalf("matching projection rejected: %v", err)
	}

	for _, test := range []struct {
		name   string
		mutate func(map[string]uci.IndexMembership, map[string]uciRealCorpusCompositionMembershipRow, map[string]uci.IndexArtifactProof)
	}{
		{
			name: "wrong state",
			mutate: func(staged map[string]uci.IndexMembership, _ map[string]uciRealCorpusCompositionMembershipRow, _ map[string]uci.IndexArtifactProof) {
				value := staged["fixture.go"]
				value.State = uci.IndexFileExcluded
				value.ArtifactID = nil
				staged["fixture.go"] = value
			},
		},
		{
			name: "wrong artifact",
			mutate: func(staged map[string]uci.IndexMembership, _ map[string]uciRealCorpusCompositionMembershipRow, _ map[string]uci.IndexArtifactProof) {
				foreign := "22222222-2222-4222-8222-222222222222"
				value := staged["fixture.go"]
				value.ArtifactID = &foreign
				staged["fixture.go"] = value
			},
		},
		{
			name: "wrong digest",
			mutate: func(_ map[string]uci.IndexMembership, _ map[string]uciRealCorpusCompositionMembershipRow, proofs map[string]uci.IndexArtifactProof) {
				value := proofs[artifactID]
				value.ContentDigest = uci.IndexDigest(uciRealCorpusSHA256([]byte("other")))
				proofs[artifactID] = value
			},
		},
		{
			name: "extra path",
			mutate: func(staged map[string]uci.IndexMembership, _ map[string]uciRealCorpusCompositionMembershipRow, _ map[string]uci.IndexArtifactProof) {
				staged["extra.go"] = uci.IndexMembership{PathKey: "extra.go", DisplayPath: "extra.go", Mode: uciRealCorpusPreparedMembershipMode, State: uci.IndexFileExcluded}
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			staged, persisted, proofs := projection()
			test.mutate(staged, persisted, proofs)
			if err := uciRealCorpusVerifyMembershipProjection([]uciRealCorpusFrozenEntry{entry}, staged, persisted, proofs); err == nil {
				t.Fatal("mismatched projection was accepted")
			}
		})
	}
}
