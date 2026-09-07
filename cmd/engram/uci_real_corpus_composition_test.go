package main

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	gormdb "github.com/thebtf/engram/internal/db/gorm"
	"github.com/thebtf/engram/internal/privacy"
	"github.com/thebtf/engram/internal/uci"
)

const (
	uciRealCorpusCompositionSchemaVersion   = "engram.uci-real-corpus-composition/v1"
	uciRealCorpusPreparedMembershipMode     = "unknown"
	uciRealCorpusScannerModeUnavailable     = "unavailable"
	uciRealCorpusUnsupportedCapabilityCause = "unsupported_capability"
	uciRealCorpusUnreadableCause            = "unreadable"
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
	SchemaVersion               string            `json:"schema_version"`
	ScannerPolicyDigest         string            `json:"scanner_policy_digest"`
	FrozenManifestDigest        string            `json:"frozen_manifest_digest"`
	BaselineManifestDigest      string            `json:"baseline_manifest_digest"`
	CanaryDeltaDigest           string            `json:"canary_delta_digest"`
	ViewManifestDigest          string            `json:"view_manifest_digest"`
	ReconstructedManifestDigest string            `json:"reconstructed_manifest_digest"`
	StagedPartsDigest           string            `json:"staged_parts_digest"`
	StagedEdgesDigest           string            `json:"staged_edges_digest"`
	ArtifactProofDigest         string            `json:"artifact_proof_digest"`
	StagedPartCount             uint64            `json:"staged_part_count"`
	StagedPayloadBytes          uint64            `json:"staged_payload_bytes"`
	MembershipCount             uint64            `json:"membership_count"`
	ArtifactProofCount          uint64            `json:"artifact_proof_count"`
	EdgeCount                   uint64            `json:"edge_count"`
	BaselineMembershipCount     uint64            `json:"baseline_membership_count"`
	CanaryMembershipCount       uint64            `json:"canary_membership_count"`
	MembershipStateHistogram    map[string]uint64 `json:"membership_state_histogram"`
	BaselineStateHistogram      map[string]uint64 `json:"baseline_state_histogram"`
	CanaryStateHistogram        map[string]uint64 `json:"canary_state_histogram"`
	ReasonHistogram             map[string]uint64 `json:"reason_histogram"`
	ScannerModeHistogram        map[string]uint64 `json:"scanner_mode_histogram"`
	PersistedModeHistogram      map[string]uint64 `json:"persisted_mode_histogram"`
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
	memberships    map[string]uci.IndexMembership
	artifactProofs map[string]uci.IndexArtifactProof
	partsDigest    string
	manifestDigest string
	edgesDigest    string
	partCount      uint64
	payloadBytes   uint64
	edgeCount      uint64
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
		end := start
		for end < len(output) && output[end] != 0 {
			end++
		}
		if end == start || end == len(output) {
			return errors.New("real-corpus composition scanner stage output is malformed")
		}
		record := string(output[start:end])
		separator := strings.IndexByte(record, '\t')
		if separator < 0 || separator == len(record)-1 {
			return errors.New("real-corpus composition scanner stage output is malformed")
		}
		header := strings.Fields(record[:separator])
		if len(header) != 3 || !uciRealCorpusGitMode(header[0]) {
			return errors.New("real-corpus composition scanner stage output is malformed")
		}
		candidatePath := record[separator+1:]
		if !uciRealCorpusCompositionPath(candidatePath) {
			return errors.New("real-corpus composition scanner stage output is malformed")
		}
		if previous, found := runner.stageModes[candidatePath]; !found || (previous != "160000" && header[0] == "160000") {
			runner.stageModes[candidatePath] = header[0]
		}
		start = end + 1
	}
	return nil
}

// uciFreezeRealCorpusManifest scans through the same scanner policy as the
// production prepared-index runtime, records only relative path facts, and
// treats known untracked canaries as a separate delta.
func uciFreezeRealCorpusManifest(ctx context.Context, root string) (uciRealCorpusFrozenManifest, error) {
	if ctx == nil {
		return uciRealCorpusFrozenManifest{}, errors.New("real-corpus composition context is required")
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

	entries := make([]uciRealCorpusFrozenEntry, 0, len(scan.Files))
	for _, file := range scan.Files {
		if err := ctx.Err(); err != nil {
			return uciRealCorpusFrozenManifest{}, err
		}
		membershipState, reason, err := uciRealCorpusFrozenMembershipState(file)
		if err != nil {
			return uciRealCorpusFrozenManifest{}, errors.New("real-corpus composition scanner file state is invalid")
		}
		contentDigest := ""
		if file.State == uci.IndexFilePresent {
			contentDigest = uciRealCorpusSHA256(file.Body)
		}
		mode := runner.stageModes[file.Path]
		if mode == "" {
			mode = uciRealCorpusScannerModeUnavailable
		}
		_, canary := uciRealCorpusCanaries[file.Path]
		entries = append(entries, uciRealCorpusFrozenEntry{
			path:            file.Path,
			scannerState:    file.State,
			membershipState: membershipState,
			reason:          reason,
			scannerMode:     mode,
			contentDigest:   contentDigest,
			canary:          canary,
		})
	}
	manifest, err := uciRealCorpusBuildFrozenManifest(entries)
	if err != nil {
		return uciRealCorpusFrozenManifest{}, err
	}
	return manifest, nil
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
		StagedPartCount:             staged.partCount,
		StagedPayloadBytes:          staged.payloadBytes,
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
	}
	return composition, nil
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
		if !uciRealCorpusCompositionPath(entry.path) || entry.scannerMode == "" {
			return uciRealCorpusFrozenManifest{}, errors.New("real-corpus composition frozen path is invalid")
		}
		if _, found := seen[entry.path]; found {
			return uciRealCorpusFrozenManifest{}, errors.New("real-corpus composition scanner repeated a path")
		}
		seen[entry.path] = struct{}{}
		if entry.scannerState == uci.IndexFilePresent {
			if !uciRealCorpusCompositionDigest(entry.contentDigest) {
				return uciRealCorpusFrozenManifest{}, errors.New("real-corpus composition present file has no digest")
			}
		} else if entry.contentDigest != "" {
			return uciRealCorpusFrozenManifest{}, errors.New("real-corpus composition non-present scanner file carries a digest")
		}
		if _, knownCanary := uciRealCorpusCanaries[entry.path]; entry.canary != knownCanary {
			return uciRealCorpusFrozenManifest{}, errors.New("real-corpus composition canary classification is invalid")
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
		} else {
			manifest.BaselineEntryCount++
			manifest.BaselineStateHistogram[string(entry.membershipState)]++
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
		Version          string   `json:"version"`
		IncludeUntracked bool     `json:"include_untracked"`
		ProtectedPaths   []string `json:"protected_paths"`
		Capabilities     []string `json:"capabilities"`
		SecretDetector   string   `json:"secret_detector"`
	}{
		Version:          uciRealCorpusCompositionSchemaVersion,
		IncludeUntracked: true,
		ProtectedPaths:   append([]string(nil), uciRealCorpusCompositionProtectedPaths[:]...),
		Capabilities:     append([]string(nil), uciRealCorpusCompositionCapabilities[:]...),
		SecretDetector:   "runtime-protected-secret-path+privacy-contains-secrets",
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
	acks := make([]uci.IndexPartAck, 0, len(rows))
	memberships := make([]uci.IndexMembership, 0)
	replacements := make([]uci.IndexEdgeReplacement, 0)
	for sequence, row := range rows {
		if row.Sequence != int64(sequence) || row.PayloadBytes < 0 || !uciRealCorpusCompositionDigest(row.PartDigest) {
			return uciRealCorpusCompositionStagedBuild{}, errors.New("real-corpus composition staged part is invalid")
		}
		var part uci.IndexPart
		if err := json.Unmarshal([]byte(row.Payload), &part); err != nil {
			return uciRealCorpusCompositionStagedBuild{}, errors.New("real-corpus composition staged part is invalid")
		}
		digest, err := uci.DigestIndexPart(part)
		if err != nil || string(digest) != row.PartDigest {
			return uciRealCorpusCompositionStagedBuild{}, errors.New("real-corpus composition staged part digest does not match")
		}
		if len(part.Deletions) != 0 {
			return uciRealCorpusCompositionStagedBuild{}, errors.New("real-corpus composition full publication contains deletions")
		}
		for _, membership := range part.Memberships {
			if _, found := staged.memberships[membership.PathKey]; found {
				return uciRealCorpusCompositionStagedBuild{}, errors.New("real-corpus composition staged publication repeats a path")
			}
			staged.memberships[membership.PathKey] = membership
			memberships = append(memberships, membership)
		}
		for _, proof := range part.Artifacts {
			if prior, found := staged.artifactProofs[proof.ArtifactID]; found && prior != proof {
				return uciRealCorpusCompositionStagedBuild{}, errors.New("real-corpus composition staged artifact proof conflicts")
			}
			staged.artifactProofs[proof.ArtifactID] = proof
		}
		for _, replacement := range part.EdgeReplacements {
			if ^uint64(0)-staged.edgeCount < uint64(len(replacement.Edges)) {
				return uciRealCorpusCompositionStagedBuild{}, errors.New("real-corpus composition staged edge count overflow")
			}
			staged.edgeCount += uint64(len(replacement.Edges))
			replacements = append(replacements, replacement)
		}
		if ^uint64(0)-staged.payloadBytes < uint64(row.PayloadBytes) {
			return uciRealCorpusCompositionStagedBuild{}, errors.New("real-corpus composition staged payload count overflow")
		}
		staged.payloadBytes += uint64(row.PayloadBytes)
		staged.partCount++
		acks = append(acks, uci.IndexPartAck{BuildID: buildID, Sequence: uint32(row.Sequence), Digest: digest})
	}

	partsDigest, err := uci.DigestIndexParts(acks)
	if err != nil {
		return uciRealCorpusCompositionStagedBuild{}, errors.New("real-corpus composition staged parts digest failed")
	}
	manifestDigest, err := uci.DigestIndexManifest(memberships)
	if err != nil {
		return uciRealCorpusCompositionStagedBuild{}, errors.New("real-corpus composition reconstructed manifest is invalid")
	}
	edgesDigest, err := uci.DigestIndexEdges(replacements)
	if err != nil {
		return uciRealCorpusCompositionStagedBuild{}, errors.New("real-corpus composition reconstructed edges are invalid")
	}
	staged.partsDigest = string(partsDigest)
	staged.manifestDigest = string(manifestDigest)
	staged.edgesDigest = string(edgesDigest)
	return staged, nil
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
	for pathKey := range staged {
		if _, expected := expectedPaths[pathKey]; !expected {
			return errors.New("real-corpus composition staged publication contains a foreign path")
		}
	}
	for pathKey := range persisted {
		if _, expected := expectedPaths[pathKey]; !expected {
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

	manifest, err := uciFreezeRealCorpusManifest(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	if manifest.BaselineEntryCount != 1 || manifest.CanaryEntryCount != uint64(len(uciRealCorpusCanaries)) {
		t.Fatalf("frozen scanner counts = baseline=%d canary=%d", manifest.BaselineEntryCount, manifest.CanaryEntryCount)
	}
	for _, entry := range manifest.entries {
		if entry.canary && (entry.scannerMode != uciRealCorpusScannerModeUnavailable || entry.scannerState != uci.IndexFilePresent || entry.membershipState != uci.IndexFilePresent) {
			t.Fatal("untracked canary did not remain an explicit present delta")
		}
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
