package codeintel

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/thebtf/engram/internal/handlers/engramcore"
	"github.com/thebtf/engram/internal/uci"
	pb "github.com/thebtf/engram/proto/engram/v1"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// UCIPreparedIndexScanner is the read-only scanner boundary used by the
// prepared index collaborator. It never selects a source or checkout.
type UCIPreparedIndexScanner interface {
	Scan(context.Context, uci.AuthorizedRootEvidence) (uci.ScannerResult, error)
}

// UCIPreparedTreeSitterParser is the local grammar-only parser port. A nil
// parser is allowed for Go-only input; parser-required input fails before Begin.
type UCIPreparedTreeSitterParser interface {
	Parse(context.Context, uci.TreeSitterParseRequest) (uci.TreeSitterArtifact, error)
}

var _ UCIPreparedTreeSitterParser = (*uci.TreeSitterWorker)(nil)

// UCIPreparedIndexConfig binds one prepared index collaborator to the exact
// daemon identity, selected server parser bundle, and local operational
// evidence it is allowed to use.
type UCIPreparedIndexConfig struct {
	WorkstationID      string
	ClientInstanceID   string
	ParserBundleDigest uci.IndexDigest
	Registry           *UCILocalRegistry
	Scanner            UCIPreparedIndexScanner
	TreeSitterParser   UCIPreparedTreeSitterParser
	GoProfile          uci.GoExtractionProfile
}

// UCIPreparedIndexCollaborator prepares and publishes immutable UCI admission
// frames for server-authorized targets. It has no project selector or path
// authority: the server binding and local registry jointly determine its root.
type UCIPreparedIndexCollaborator struct {
	workstationID      string
	clientInstanceID   string
	parserBundleDigest uci.IndexDigest
	registry           *UCILocalRegistry
	scanner            UCIPreparedIndexScanner
	treeSitterParser   UCIPreparedTreeSitterParser
	goProfile          uci.GoExtractionProfile
}

// NewUCIPreparedIndexCollaborator constructs the daemon-side prepared-index
// collaborator with a domain-validated Go profile and parser-required fail-close boundary.
func NewUCIPreparedIndexCollaborator(config UCIPreparedIndexConfig) (*UCIPreparedIndexCollaborator, error) {
	if !validUCIPreparedIndexIdentity(config.WorkstationID) {
		return nil, fmt.Errorf("uci prepared index: workstation identity is invalid")
	}
	if !validUCIPreparedIndexIdentity(config.ClientInstanceID) {
		return nil, fmt.Errorf("uci prepared index: client instance identity is invalid")
	}
	if config.Registry == nil {
		return nil, fmt.Errorf("uci prepared index: local registry is required")
	}
	if config.Scanner == nil {
		return nil, fmt.Errorf("uci prepared index: scanner is required")
	}
	if !validUCIPreparedIndexDigest(config.ParserBundleDigest) {
		return nil, fmt.Errorf("uci prepared index: parser bundle digest is invalid")
	}
	if _, err := uci.GoIndexAdmissionArtifactProfile(config.GoProfile); err != nil {
		return nil, fmt.Errorf("uci prepared index: Go extraction profile is invalid: %w", err)
	}
	return &UCIPreparedIndexCollaborator{
		workstationID:      config.WorkstationID,
		clientInstanceID:   config.ClientInstanceID,
		parserBundleDigest: config.ParserBundleDigest,
		registry:           config.Registry,
		scanner:            config.Scanner,
		treeSitterParser:   config.TreeSitterParser,
		goProfile:          config.GoProfile,
	}, nil
}

type uciPreparedLocalTarget struct {
	binding          uci.IndexBinding
	root             UCILocalApprovedRoot
	checkout         UCILocalCheckoutRecord
	observedSequence int64
}

func (collaborator *UCIPreparedIndexCollaborator) resolveLocalTarget(ctx context.Context, binding uci.IndexBinding, rootHint string) (uciPreparedLocalTarget, error) {
	if collaborator == nil || collaborator.registry == nil {
		return uciPreparedLocalTarget{}, fmt.Errorf("uci prepared index: collaborator is unavailable")
	}
	if ctx == nil {
		return uciPreparedLocalTarget{}, fmt.Errorf("uci prepared index: context is required")
	}
	if err := ctx.Err(); err != nil {
		return uciPreparedLocalTarget{}, err
	}
	binding = binding.Clone()
	if err := binding.Validate(); err != nil {
		return uciPreparedLocalTarget{}, fmt.Errorf("uci prepared index: server binding is invalid: %w", err)
	}
	if binding.WorkstationID != collaborator.workstationID {
		return uciPreparedLocalTarget{}, fmt.Errorf("uci prepared index: binding workstation does not match collaborator")
	}

	root, found, err := collaborator.registry.ApprovedRoot(ctx, binding.LocalRootID)
	if err != nil {
		return uciPreparedLocalTarget{}, fmt.Errorf("uci prepared index: load approved root: %w", err)
	}
	if !found || root.RootID != binding.LocalRootID || root.SourceID != binding.Scope.SourceID {
		return uciPreparedLocalTarget{}, fmt.Errorf("uci prepared index: binding root is not approved for the source")
	}
	if rootHint != root.RootPath {
		return uciPreparedLocalTarget{}, fmt.Errorf("uci prepared index: root hint does not exactly match the approved root")
	}

	checkout, found, err := collaborator.registry.Snapshot(ctx, binding.Scope.CheckoutID)
	if err != nil {
		return uciPreparedLocalTarget{}, fmt.Errorf("uci prepared index: load local checkout: %w", err)
	}
	if !found ||
		checkout.RootID != binding.LocalRootID ||
		checkout.SourceID != binding.Scope.SourceID ||
		checkout.CheckoutID != binding.Scope.CheckoutID ||
		checkout.IncarnationID != binding.Scope.IncarnationID ||
		checkout.CommonGitDirFingerprint != root.CommonGitDirFingerprint ||
		checkout.WorkstationID != binding.WorkstationID ||
		checkout.WorkstationID != collaborator.workstationID ||
		checkout.ClientInstanceID != collaborator.clientInstanceID {
		return uciPreparedLocalTarget{}, fmt.Errorf("uci prepared index: local checkout does not match the server binding")
	}
	return uciPreparedLocalTarget{
		binding:          binding,
		root:             root,
		checkout:         checkout,
		observedSequence: checkout.DirtySequence,
	}, nil
}

func (collaborator *UCIPreparedIndexCollaborator) scanCurrent(ctx context.Context, local uciPreparedLocalTarget) (uci.ScannerResult, error) {
	if collaborator == nil || collaborator.scanner == nil {
		return uci.ScannerResult{}, fmt.Errorf("uci prepared index: scanner is unavailable")
	}
	scan, err := collaborator.scanner.Scan(ctx, uci.AuthorizedRootEvidence{RootPath: local.root.RootPath})
	if err != nil {
		return uci.ScannerResult{}, fmt.Errorf("uci prepared index: scan approved root: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return uci.ScannerResult{}, err
	}
	if scan.Census.Outcome != uci.IndexScanComplete || !scan.Census.Complete || !scan.Census.CanDeleteAll || scan.Coverage.Structural != uci.IndexCoverageComplete {
		return uci.ScannerResult{}, fmt.Errorf("uci prepared index: scan is not a complete census")
	}
	scan.Observation.ObservedFSSeq = local.observedSequence
	return scan, nil
}

const (
	uciPreparedMembershipMode                  = "unknown"
	uciPreparedGoResolverRevision              = "uci-prepared-go-call/v1"
	uciPreparedGoResolverRule                  = "go-direct-call/v1"
	uciPreparedGoResolverExplanation           = "unique same-package Go function declaration"
	uciPreparedTreeSitterProfileKeyVersion     = "uci-prepared-tree-sitter/v1"
	uciPreparedTreeSitterUnavailableMessage    = "Tree-sitter parser is unavailable"
	uciPreparedTreeSitterProtocolMessage       = "Tree-sitter parser protocol failure"
	uciPreparedTreeSitterBundleMismatchMessage = "Tree-sitter parser bundle mismatch"
	uciPreparedTreeSitterPartialMessage        = "Tree-sitter parser coverage is partial"
)

type uciPreparedAdmissionFile struct {
	path       string
	membership uci.IndexAdmissionMembership
	artifact   *uci.IndexAdmissionArtifact
	edges      []uci.IndexAdmissionEdge
	errors     []string
}

type uciPreparedAdmissionPlan struct {
	frames   []uci.IndexAdmissionFrame
	payloads [][]byte
	coverage uci.IndexCoverage
	errors   []string
	uploaded int
}

type uciPreparedAdmissionRecordKind uint8

const (
	uciPreparedAdmissionArtifactRecord uciPreparedAdmissionRecordKind = iota + 1
	uciPreparedAdmissionMembershipRecord
	uciPreparedAdmissionEdgeReplacementRecord
)

// uciPreparedAdmissionRecord holds one complete logical v1 record. Packing
// may change only which frame contains this record; it never splits or drops it.
type uciPreparedAdmissionRecord struct {
	kind uciPreparedAdmissionRecordKind
	file *uciPreparedAdmissionFile
}

func (collaborator *UCIPreparedIndexCollaborator) prepareAdmissionPlan(ctx context.Context, local uciPreparedLocalTarget, scan uci.ScannerResult) (uciPreparedAdmissionPlan, error) {
	goProfile, err := uci.GoIndexAdmissionArtifactProfile(collaborator.goProfile)
	if err != nil {
		return uciPreparedAdmissionPlan{}, fmt.Errorf("uci prepared index: configure Go admission profile: %w", err)
	}
	goProfile.ExtractionProfileDigest = collaborator.parserBundleDigest

	files := append([]uci.ScannerFile(nil), scan.Files...)
	sort.Slice(files, func(left, right int) bool {
		return files[left].Path < files[right].Path
	})
	prepared := make([]uciPreparedAdmissionFile, 0, len(files))
	seenPaths := make(map[string]struct{}, len(files))

	for _, file := range files {
		if err := ctx.Err(); err != nil {
			return uciPreparedAdmissionPlan{}, err
		}
		if _, found := seenPaths[file.Path]; found {
			return uciPreparedAdmissionPlan{}, fmt.Errorf("uci prepared index: scanner repeated path %q", file.Path)
		}
		seenPaths[file.Path] = struct{}{}

		preparedFile, err := collaborator.prepareAdmissionFile(ctx, local.binding.Scope.SourceID, local.binding.ProfileID, goProfile, file)
		if err != nil {
			return uciPreparedAdmissionPlan{}, err
		}
		prepared = append(prepared, preparedFile)
	}

	unresolved, err := uciPreparedAddResolvedGoCallEdges(prepared)
	if err != nil {
		return uciPreparedAdmissionPlan{}, err
	}

	frames, payloads, err := uciPreparedPackFrames(local.binding.ProfileID, prepared)
	if err != nil {
		return uciPreparedAdmissionPlan{}, err
	}

	coverage, err := uciPreparedCoverageForFiles(scan.Coverage, prepared)
	if err != nil {
		return uciPreparedAdmissionPlan{}, err
	}
	if ^uint64(0)-coverage.UnresolvedReferences < unresolved {
		return uciPreparedAdmissionPlan{}, fmt.Errorf("uci prepared index: unresolved reference count overflow")
	}
	coverage.UnresolvedReferences += unresolved
	if err := uci.ValidateIndexAdmissionFramesForBinding(frames, local.binding); err != nil {
		return uciPreparedAdmissionPlan{}, fmt.Errorf("uci prepared index: validate complete packed build: %w", err)
	}

	artifactIDs := make(map[string]struct{})
	errors := make([]string, 0)
	for _, preparedFile := range prepared {
		errors = append(errors, preparedFile.errors...)
	}
	for _, frame := range frames {
		for _, artifact := range frame.Artifacts {
			artifactIDs[artifact.ArtifactID] = struct{}{}
		}
	}

	sort.Strings(errors)
	errors = uciPreparedUniqueStrings(errors)
	return uciPreparedAdmissionPlan{
		frames:   frames,
		payloads: payloads,
		coverage: coverage,
		errors:   errors,
		uploaded: len(artifactIDs),
	}, nil
}

func (collaborator *UCIPreparedIndexCollaborator) prepareAdmissionFile(ctx context.Context, sourceID, analysisProfileID string, goProfile uci.IndexAdmissionArtifactProfile, file uci.ScannerFile) (uciPreparedAdmissionFile, error) {
	prepared := uciPreparedAdmissionFile{
		path: file.Path,
		membership: uci.IndexAdmissionMembership{
			PathKey:     file.Path,
			DisplayPath: file.Path,
			Mode:        uciPreparedMembershipMode,
		},
	}
	switch file.State {
	case uci.IndexFileExcluded:
		prepared.membership.State = uciPreparedExcludedMembershipState(file.Exclusion)
		prepared.errors = append(prepared.errors, file.Path+": source is excluded")
		return prepared, nil
	case uci.IndexFileUnreadable:
		prepared.membership.State = uci.IndexAdmissionMembershipUnreadable
		prepared.errors = append(prepared.errors, file.Path+": source is unreadable")
		return prepared, nil
	case uci.IndexFilePresent:
		switch path.Ext(file.Path) {
		case ".go":
			extracted := uci.ExtractGo(file.Body, collaborator.goProfile)
			artifact, err := uci.NewIndexAdmissionArtifactFromGo(sourceID, goProfile, file.Body, extracted)
			if err != nil {
				if uci.IsIndexCapacityError(err) {
					return uciPreparedAdmissionFile{}, err
				}
				return uciPreparedAdmissionFile{}, fmt.Errorf("uci prepared index: normalize Go source %q: %w", file.Path, err)
			}
			artifactID := artifact.ArtifactID
			prepared.membership.State = uci.IndexAdmissionMembershipPresent
			prepared.membership.ArtifactID = &artifactID
			prepared.artifact = &artifact
			if artifact.Status == uci.IndexAdmissionArtifactPartial {
				prepared.errors = append(prepared.errors, file.Path+": Go extraction is partial")
			}
			return prepared, nil
		case ".js":
			return collaborator.prepareTreeSitterAdmissionFile(ctx, sourceID, analysisProfileID, file, prepared, uci.TreeSitterLanguageJavaScript)
		case ".ts":
			return collaborator.prepareTreeSitterAdmissionFile(ctx, sourceID, analysisProfileID, file, prepared, uci.TreeSitterLanguageTypeScript)
		case ".tsx":
			return collaborator.prepareTreeSitterAdmissionFile(ctx, sourceID, analysisProfileID, file, prepared, uci.TreeSitterLanguageTSX)
		default:
			prepared.membership.State = uci.IndexAdmissionMembershipUnsupported
			prepared.errors = append(prepared.errors, file.Path+": source language is unsupported")
			return prepared, nil
		}
	default:
		return uciPreparedAdmissionFile{}, fmt.Errorf("uci prepared index: scanner returned unsupported state %q for %q", file.State, file.Path)
	}
}

// prepareTreeSitterAdmissionFile treats the selected parser as part of the
// prepared runtime. A missing, invalid, or digest-mismatched parser aborts the
// entire build before Begin so a partial fallback cannot publish a new View.
func (collaborator *UCIPreparedIndexCollaborator) prepareTreeSitterAdmissionFile(ctx context.Context, sourceID, analysisProfileID string, file uci.ScannerFile, prepared uciPreparedAdmissionFile, language uci.TreeSitterLanguage) (uciPreparedAdmissionFile, error) {
	if collaborator.treeSitterParser == nil {
		return uciPreparedAdmissionFile{}, fmt.Errorf("uci prepared index: %s for %q", uciPreparedTreeSitterUnavailableMessage, file.Path)
	}
	profile, err := uci.TreeSitterIndexAdmissionArtifactProfile(language, collaborator.parserBundleDigest)
	if err != nil {
		return uciPreparedAdmissionFile{}, fmt.Errorf("uci prepared index: configure Tree-sitter admission profile: %w", err)
	}
	parsed, err := collaborator.treeSitterParser.Parse(ctx, uci.TreeSitterParseRequest{
		Language:   language,
		ProfileKey: uciPreparedTreeSitterProfileKey(analysisProfileID, language, collaborator.parserBundleDigest),
		Source:     append([]byte(nil), file.Body...),
	})
	if err != nil {
		if contextErr := ctx.Err(); contextErr != nil {
			return uciPreparedAdmissionFile{}, contextErr
		}
		return uciPreparedAdmissionFile{}, fmt.Errorf("uci prepared index: %s for %q: %w", uciPreparedTreeSitterFailureMessage(err), file.Path, err)
	}
	if parsed.BundleDigest != collaborator.parserBundleDigest {
		return uciPreparedAdmissionFile{}, fmt.Errorf("uci prepared index: %s for %q", uciPreparedTreeSitterBundleMismatchMessage, file.Path)
	}
	if parsed.Coverage == uci.IndexCoverageUnavailable {
		return uciPreparedAdmissionFile{}, fmt.Errorf("uci prepared index: %s for %q", uciPreparedTreeSitterUnavailableMessage, file.Path)
	}
	artifact, err := uci.NewIndexAdmissionArtifactFromTreeSitter(sourceID, profile, file.Body, parsed)
	if err != nil {
		if uci.IsIndexCapacityError(err) {
			return uciPreparedAdmissionFile{}, err
		}
		return uciPreparedAdmissionFile{}, fmt.Errorf("uci prepared index: %s for %q: %w", uciPreparedTreeSitterProtocolMessage, file.Path, err)
	}
	artifactID := artifact.ArtifactID
	prepared.membership.State = uci.IndexAdmissionMembershipPresent
	prepared.membership.ArtifactID = &artifactID
	prepared.artifact = &artifact
	if artifact.Status == uci.IndexAdmissionArtifactPartial {
		prepared.errors = append(prepared.errors, file.Path+": "+uciPreparedTreeSitterPartialMessage)
	}
	return prepared, nil
}

func uciPreparedTreeSitterProfileKey(analysisProfileID string, language uci.TreeSitterLanguage, bundleDigest uci.IndexDigest) string {
	return uciPreparedTreeSitterProfileKeyVersion + ":" + analysisProfileID + ":" + string(language) + ":" + string(bundleDigest)
}

func uciPreparedTreeSitterFailureMessage(err error) string {
	switch {
	case errors.Is(err, uci.ErrTreeSitterBundleMismatch):
		return uciPreparedTreeSitterBundleMismatchMessage
	case errors.Is(err, uci.ErrTreeSitterProtocol), errors.Is(err, uci.ErrTreeSitterInputLimit), errors.Is(err, uci.ErrTreeSitterOutputLimit):
		return uciPreparedTreeSitterProtocolMessage
	default:
		return uciPreparedTreeSitterUnavailableMessage
	}
}

func uciPreparedExcludedMembershipState(exclusion uci.ScannerExclusion) uci.IndexAdmissionMembershipState {
	if exclusion == uci.ScannerExclusionProtected {
		return uci.IndexAdmissionMembershipProtected
	}
	return uci.IndexAdmissionMembershipExcluded
}

func uciPreparedPackFrames(profileID string, files []uciPreparedAdmissionFile) ([]uci.IndexAdmissionFrame, [][]byte, error) {
	records, err := uciPreparedAdmissionRecords(files)
	if err != nil {
		return nil, nil, err
	}
	newFrame := func() uci.IndexAdmissionFrame {
		return uci.IndexAdmissionFrame{
			Version: uci.IndexAdmissionFrameVersion,
			Profile: uci.IndexAdmissionProfile{ID: profileID},
		}
	}

	frames := make([]uci.IndexAdmissionFrame, 0, 1)
	current := newFrame()
	for _, record := range records {
		for {
			err := uciPreparedTryAppendAdmissionRecord(&current, record)
			if err == nil {
				break
			}
			if !uci.IsIndexCapacityError(err) {
				return nil, nil, fmt.Errorf("uci prepared index: pack admission record: %w", err)
			}
			if uciPreparedFrameEmpty(current) {
				return nil, nil, err
			}
			frames = append(frames, current)
			current = newFrame()
		}
	}
	frames = append(frames, current)
	payloads, err := uciPreparedEncodeFrames(frames)
	if err != nil {
		return nil, nil, err
	}
	return frames, payloads, nil
}

func uciPreparedAdmissionRecords(files []uciPreparedAdmissionFile) ([]uciPreparedAdmissionRecord, error) {
	records := make([]uciPreparedAdmissionRecord, 0, len(files)*3)
	artifacts := make(map[string]*uci.IndexAdmissionArtifact, len(files))
	for index := range files {
		file := &files[index]
		if file.membership.State != uci.IndexAdmissionMembershipPresent || file.artifact == nil {
			continue
		}
		if owner, found := artifacts[file.artifact.ArtifactID]; found {
			if owner.ContentDigest != file.artifact.ContentDigest || owner.FactsDigest != file.artifact.FactsDigest {
				return nil, fmt.Errorf("uci prepared index: matching artifact identity has different facts")
			}
			continue
		}
		artifacts[file.artifact.ArtifactID] = file.artifact
		records = append(records, uciPreparedAdmissionRecord{
			kind: uciPreparedAdmissionArtifactRecord,
			file: file,
		})
	}
	for index := range files {
		records = append(records, uciPreparedAdmissionRecord{
			kind: uciPreparedAdmissionMembershipRecord,
			file: &files[index],
		})
	}
	for index := range files {
		records = append(records, uciPreparedAdmissionRecord{
			kind: uciPreparedAdmissionEdgeReplacementRecord,
			file: &files[index],
		})
	}
	return records, nil
}

func uciPreparedTryAppendAdmissionRecord(frame *uci.IndexAdmissionFrame, record uciPreparedAdmissionRecord) error {
	if frame == nil || record.file == nil {
		return fmt.Errorf("uci prepared index: invalid admission record")
	}
	artifacts := len(frame.Artifacts)
	memberships := len(frame.Memberships)
	replacements := len(frame.EdgeReplacements)
	switch record.kind {
	case uciPreparedAdmissionArtifactRecord:
		if record.file.artifact == nil {
			return fmt.Errorf("uci prepared index: artifact record is missing its artifact")
		}
		frame.Artifacts = append(frame.Artifacts, *record.file.artifact)
	case uciPreparedAdmissionMembershipRecord:
		frame.Memberships = append(frame.Memberships, record.file.membership)
	case uciPreparedAdmissionEdgeReplacementRecord:
		frame.EdgeReplacements = append(frame.EdgeReplacements, uci.IndexAdmissionEdgeReplacement{
			SourcePath: record.file.path,
			Edges:      record.file.edges,
		})
	default:
		return fmt.Errorf("uci prepared index: unsupported admission record")
	}
	if _, err := uci.EncodeIndexAdmissionFrame(*frame); err == nil {
		return nil
	} else {
		frame.Artifacts = frame.Artifacts[:artifacts]
		frame.Memberships = frame.Memberships[:memberships]
		frame.EdgeReplacements = frame.EdgeReplacements[:replacements]
		return err
	}
}

func uciPreparedFrameEmpty(frame uci.IndexAdmissionFrame) bool {
	return len(frame.Artifacts) == 0 && len(frame.Memberships) == 0 && len(frame.Deletions) == 0 && len(frame.EdgeReplacements) == 0
}

func uciPreparedEncodeFrames(frames []uci.IndexAdmissionFrame) ([][]byte, error) {
	payloads := make([][]byte, 0, len(frames))
	for _, frame := range frames {
		encoded, err := uci.EncodeIndexAdmissionFrame(frame)
		if err != nil {
			return nil, err
		}
		payloads = append(payloads, encoded)
	}
	if err := uci.ValidateIndexAdmissionPayloads(payloads); err != nil {
		return nil, err
	}
	return payloads, nil
}

func uciPreparedAddResolvedGoCallEdges(files []uciPreparedAdmissionFile) (uint64, error) {
	type definitionTarget struct {
		path       string
		artifactID string
		localKey   string
	}
	definitions := make(map[string][]definitionTarget)
	for _, prepared := range files {
		if prepared.artifact == nil || prepared.membership.State != uci.IndexAdmissionMembershipPresent || prepared.artifact.Profile.Language != uci.IndexAdmissionLanguageGo {
			continue
		}
		for _, definition := range prepared.artifact.Definitions {
			if definition.Kind != "function" || !strings.HasPrefix(definition.LocalSymbolKey, "func:") {
				continue
			}
			definitions[definition.SymbolKey] = append(definitions[definition.SymbolKey], definitionTarget{
				path:       prepared.path,
				artifactID: prepared.artifact.ArtifactID,
				localKey:   definition.LocalSymbolKey,
			})
		}
	}

	var unresolved uint64
	for index := range files {
		prepared := &files[index]
		if prepared.artifact == nil || prepared.membership.State != uci.IndexAdmissionMembershipPresent || prepared.artifact.Profile.Language != uci.IndexAdmissionLanguageGo {
			continue
		}
		for _, reference := range prepared.artifact.References {
			if reference.Kind != "call" {
				continue
			}
			targetSymbol, established := uciPreparedGoCallTargetSymbol(reference)
			if !established {
				unresolved++
				continue
			}
			if reference.OwnerSymbolKey == nil {
				unresolved++
				continue
			}
			sourceSymbolKey := *reference.OwnerSymbolKey
			targets := definitions[targetSymbol]
			if len(targets) != 1 {
				unresolved++
				continue
			}
			target := targets[0]
			targetSymbolKey := target.localKey
			prepared.edges = append(prepared.edges, uci.IndexAdmissionEdge{
				EdgeKey:          uciPreparedEdgeKey(prepared.path, reference.SiteKey, target.path, target.localKey),
				SourceArtifactID: prepared.artifact.ArtifactID,
				SourceSymbolKey:  &sourceSymbolKey,
				Target: &uci.IndexAdmissionEdgeTarget{
					PathKey:    target.path,
					ArtifactID: target.artifactID,
					SymbolKey:  &targetSymbolKey,
				},
				Relation:         uci.IndexRelation("calls"),
				EvidenceKind:     uci.IndexEvidenceKind("resolved"),
				ResolutionState:  uci.IndexResolutionState("resolved"),
				ResolverRevision: uciPreparedGoResolverRevision,
				Evidence: uci.IndexAdmissionEdgeEvidence{
					ReferenceSiteKey: reference.SiteKey,
					Span:             reference.Span,
					RuleKey:          uciPreparedGoResolverRule,
					Explanation:      uciPreparedGoResolverExplanation,
				},
			})
		}
	}
	return unresolved, nil
}

func uciPreparedGoCallTargetSymbol(reference uci.IndexAdmissionReference) (string, bool) {
	if reference.Kind != "call" {
		return "", false
	}
	separator := strings.LastIndex(reference.SymbolKey, "/call:")
	if separator < 0 {
		return "", false
	}
	return reference.SymbolKey[:separator] + "/func:" + reference.SymbolKey[separator+len("/call:"):], true
}

func uciPreparedEdgeKey(sourcePath, siteKey, targetPath, targetSymbol string) string {
	sum := sha256.Sum256([]byte(sourcePath + "\x00" + siteKey + "\x00" + targetPath + "\x00" + targetSymbol))
	return "go-call:" + hex.EncodeToString(sum[:])
}

func uciPreparedUniqueStrings(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	unique := values[:0]
	for _, value := range values {
		if len(unique) == 0 || unique[len(unique)-1] != value {
			unique = append(unique, value)
		}
	}
	return unique
}

func uciPreparedCoverageForFiles(scanCoverage uci.IndexCoverage, files []uciPreparedAdmissionFile) (uci.IndexCoverage, error) {
	coverage := scanCoverage
	coverage.Lexical = uci.IndexCoverageComplete
	coverage.Vector = uci.IndexCoverageUnavailable
	coverage.ExcludedFiles = 0
	coverage.UnreadableFiles = 0
	coverage.UnresolvedReferences = 0
	for _, prepared := range files {
		switch prepared.membership.State {
		case uci.IndexAdmissionMembershipPresent:
			if prepared.artifact == nil {
				return uci.IndexCoverage{}, fmt.Errorf("uci prepared index: present membership %q has no artifact", prepared.path)
			}
			if prepared.artifact.Status == uci.IndexAdmissionArtifactPartial {
				coverage.Lexical = uci.IndexCoveragePartial
			}
		case uci.IndexAdmissionMembershipUnreadable:
			coverage.UnreadableFiles++
			coverage.Lexical = uci.IndexCoveragePartial
		case uci.IndexAdmissionMembershipExcluded, uci.IndexAdmissionMembershipUnsupported, uci.IndexAdmissionMembershipProtected:
			coverage.ExcludedFiles++
			coverage.Lexical = uci.IndexCoveragePartial
		default:
			return uci.IndexCoverage{}, fmt.Errorf("uci prepared index: unsupported membership state %q", prepared.membership.State)
		}
	}
	return coverage, nil
}

var _ engramcore.PreparedIndexCollaborator = (*UCIPreparedIndexCollaborator)(nil)

type uciPreparedPublication struct {
	memberships    []uci.IndexMembership
	replacements   []uci.IndexEdgeReplacement
	manifestDigest uci.IndexDigest
	edgesDigest    uci.IndexDigest
	edgeCount      uint64
}

// IndexPreparedCodebase scans the root already bound by the server, turns its
// current bytes into private source-fact frames, and publishes only the real
// ContextRef returned by Finalize.
func (collaborator *UCIPreparedIndexCollaborator) IndexPreparedCodebase(ctx context.Context, target engramcore.ResolvedIndexTarget, rootHint string, client engramcore.UCIIndexClient) (*engramcore.IndexResult, error) {
	if ctx == nil {
		return nil, fmt.Errorf("uci prepared index: context is required")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if client == nil {
		return nil, fmt.Errorf("uci prepared index: publication client is required")
	}

	local, err := collaborator.resolveLocalTarget(ctx, target.BindingClone(), rootHint)
	if err != nil {
		return nil, err
	}
	if local.observedSequence < 0 {
		return nil, fmt.Errorf("uci prepared index: local observed sequence is invalid")
	}
	scan, err := collaborator.scanCurrent(ctx, local)
	if err != nil {
		return nil, err
	}
	plan, err := collaborator.prepareAdmissionPlan(ctx, local, scan)
	if err != nil {
		return nil, err
	}
	publication, err := plan.publication()
	if err != nil {
		return nil, err
	}
	buildKey, err := uciPreparedBuildKey(local, scan, plan, publication)
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	scope := uciPreparedProtoScope(local.binding)
	parent := uciPreparedProtoContext(local.binding.Context)
	jobKind := "reconcile"
	if parent == nil {
		jobKind = "initial_index"
	}
	begin, err := client.Begin(ctx, &pb.BeginCodeIndexRequest{
		Scope:          scope,
		OwnerInstance:  collaborator.clientInstanceID,
		BuildKey:       buildKey,
		ExpectedParent: parent,
		ManifestMode:   "full",
		JobKind:        jobKind,
	})
	if err != nil {
		return nil, fmt.Errorf("uci prepared index: begin publication: %w", err)
	}
	if !uciPreparedBeginMatches(begin, scope) {
		return nil, fmt.Errorf("uci prepared index: server returned an invalid build")
	}

	frames := make([]*pb.StageCodeIndexFrame, len(plan.payloads))
	for index, payload := range plan.payloads {
		frames[index] = &pb.StageCodeIndexFrame{
			Scope:         scope,
			BuildId:       begin.GetBuildId(),
			LeaseEpoch:    begin.GetLeaseEpoch(),
			Sequence:      uint64(index),
			PayloadDigest: string(uci.DigestIndexAdmissionPayload(payload)),
			Payload:       payload,
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	staged, err := client.Stage(ctx, frames)
	if err != nil {
		return nil, fmt.Errorf("uci prepared index: stage publication: %w", err)
	}
	if !uciPreparedStageMatches(staged, begin, len(frames)) {
		return nil, fmt.Errorf("uci prepared index: server returned an invalid stage acknowledgement")
	}

	coverageJSON, err := json.Marshal(struct {
		Structural           uci.IndexCoverageState `json:"structural"`
		Lexical              uci.IndexCoverageState `json:"lexical"`
		Vector               uci.IndexCoverageState `json:"vector"`
		ExcludedFiles        uint64                 `json:"excluded_files"`
		UnreadableFiles      uint64                 `json:"unreadable_files"`
		UnresolvedReferences uint64                 `json:"unresolved_references"`
	}{
		Structural:           plan.coverage.Structural,
		Lexical:              plan.coverage.Lexical,
		Vector:               plan.coverage.Vector,
		ExcludedFiles:        plan.coverage.ExcludedFiles,
		UnreadableFiles:      plan.coverage.UnreadableFiles,
		UnresolvedReferences: plan.coverage.UnresolvedReferences,
	})
	if err != nil {
		return nil, fmt.Errorf("uci prepared index: encode coverage: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	finalized, err := client.Finalize(ctx, &pb.FinalizeCodeIndexRequest{
		Scope:                      scope,
		BuildId:                    begin.GetBuildId(),
		LeaseEpoch:                 begin.GetLeaseEpoch(),
		ExpectedParent:             parent,
		ManifestPartCount:          uint64(len(plan.payloads)),
		PartsDigest:                staged.GetPartDigest(),
		ManifestEntryCount:         uint64(len(publication.memberships)),
		ManifestDigest:             string(publication.manifestDigest),
		EdgeCount:                  publication.edgeCount,
		EdgesDigest:                string(publication.edgesDigest),
		ObservedFilesystemSequence: uint64(local.observedSequence),
		ScanStartedAt:              timestamppb.New(scan.Observation.ScanStart),
		ScanCompletedAt:            timestamppb.New(scan.Observation.ScanEnd),
		ScanOutcome:                string(scan.Census.Outcome),
		CompleteCensus:             scan.Census.Complete,
		CoverageJson:               coverageJSON,
		HeadOid:                    scan.Observation.HeadOID,
		ObjectFormat:               scan.Observation.ObjectFormat,
		RefLabel:                   scan.Observation.RefLabel,
		Dirty:                      &scan.Observation.Dirty,
	})
	if err != nil {
		return nil, fmt.Errorf("uci prepared index: finalize publication: %w", err)
	}
	published, err := uciPreparedPublishedContext(finalized, local.binding, begin, local.observedSequence)
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if _, err := collaborator.registry.MarkReconciled(ctx, local.binding.Scope.CheckoutID, local.observedSequence); err != nil {
		return nil, fmt.Errorf("uci prepared index: acknowledge durable publication: %w", err)
	}
	return &engramcore.IndexResult{
		Context:  published,
		Uploaded: plan.uploaded,
		Embedded: 0,
		Deleted:  0,
		Errors:   append([]string(nil), plan.errors...),
	}, nil
}

func (plan uciPreparedAdmissionPlan) publication() (uciPreparedPublication, error) {
	publication := uciPreparedPublication{
		memberships:  make([]uci.IndexMembership, 0),
		replacements: make([]uci.IndexEdgeReplacement, 0),
	}
	parts := make([]uci.IndexPart, 0, len(plan.frames))
	seenMemberships := make(map[string]struct{})
	seenReplacements := make(map[string]struct{})
	for frameIndex, frame := range plan.frames {
		part, err := frame.PublicationPart()
		if err != nil {
			return uciPreparedPublication{}, fmt.Errorf("uci prepared index: derive publication part %d: %w", frameIndex, err)
		}
		parts = append(parts, part)
		for _, membership := range part.Memberships {
			if _, found := seenMemberships[membership.PathKey]; found {
				return uciPreparedPublication{}, fmt.Errorf("uci prepared index: duplicate packed membership %q", membership.PathKey)
			}
			seenMemberships[membership.PathKey] = struct{}{}
			publication.memberships = append(publication.memberships, membership)
		}
		for _, replacement := range part.EdgeReplacements {
			if _, found := seenReplacements[replacement.SourcePath]; found {
				return uciPreparedPublication{}, fmt.Errorf("uci prepared index: duplicate packed edge replacement %q", replacement.SourcePath)
			}
			seenReplacements[replacement.SourcePath] = struct{}{}
			if ^uint64(0)-publication.edgeCount < uint64(len(replacement.Edges)) {
				return uciPreparedPublication{}, fmt.Errorf("uci prepared index: edge count overflow")
			}
			publication.edgeCount += uint64(len(replacement.Edges))
			publication.replacements = append(publication.replacements, replacement)
		}
	}
	if err := uci.ValidateIndexPublicationParts(parts, uci.DefaultIndexPublicationLimits()); err != nil {
		return uciPreparedPublication{}, fmt.Errorf("uci prepared index: validate publication parts: %w", err)
	}
	manifestDigest, err := uci.DigestIndexManifest(publication.memberships)
	if err != nil {
		return uciPreparedPublication{}, fmt.Errorf("uci prepared index: digest manifest: %w", err)
	}
	edgesDigest, err := uci.DigestIndexEdges(publication.replacements)
	if err != nil {
		return uciPreparedPublication{}, fmt.Errorf("uci prepared index: digest edges: %w", err)
	}
	publication.manifestDigest = manifestDigest
	publication.edgesDigest = edgesDigest
	return publication, nil
}

func uciPreparedBuildKey(local uciPreparedLocalTarget, scan uci.ScannerResult, plan uciPreparedAdmissionPlan, publication uciPreparedPublication) (string, error) {
	payloadDigests := make([]string, len(plan.payloads))
	for index, payload := range plan.payloads {
		payloadDigests[index] = string(uci.DigestIndexAdmissionPayload(payload))
	}
	encoded, err := json.Marshal(struct {
		Version        string               `json:"version"`
		Scope          uci.IndexScope       `json:"scope"`
		ProfileID      string               `json:"profile_id"`
		ExpectedParent *uci.ContextRef      `json:"expected_parent,omitempty"`
		PayloadDigests []string             `json:"payload_digests"`
		ManifestDigest uci.IndexDigest      `json:"manifest_digest"`
		EdgesDigest    uci.IndexDigest      `json:"edges_digest"`
		EdgeCount      uint64               `json:"edge_count"`
		Observation    uci.IndexObservation `json:"observation"`
		Coverage       uci.IndexCoverage    `json:"coverage"`
	}{
		Version:        "uci-prepared-build/v1",
		Scope:          local.binding.Scope,
		ProfileID:      local.binding.ProfileID,
		ExpectedParent: local.binding.Context,
		PayloadDigests: payloadDigests,
		ManifestDigest: publication.manifestDigest,
		EdgesDigest:    publication.edgesDigest,
		EdgeCount:      publication.edgeCount,
		Observation:    scan.Observation,
		Coverage:       plan.coverage,
	})
	if err != nil {
		return "", fmt.Errorf("uci prepared index: encode build identity: %w", err)
	}
	sum := sha256.Sum256(encoded)
	return "uci-prepared-v1:" + hex.EncodeToString(sum[:]), nil
}

func uciPreparedProtoScope(binding uci.IndexBinding) *pb.CodeIndexScope {
	return &pb.CodeIndexScope{
		SourceId:          binding.Scope.SourceID,
		CheckoutId:        binding.Scope.CheckoutID,
		IncarnationId:     binding.Scope.IncarnationID,
		AnalysisProfileId: binding.ProfileID,
	}
}

func uciPreparedProtoContext(contextRef *uci.ContextRef) *pb.ContextRef {
	if contextRef == nil {
		return nil
	}
	result := &pb.ContextRef{
		SourceId:          contextRef.SourceID,
		CheckoutId:        contextRef.CheckoutID,
		ViewId:            contextRef.ViewID,
		Generation:        contextRef.Generation,
		AnalysisProfileId: contextRef.AnalysisProfileID,
	}
	if contextRef.SpaceID != nil {
		spaceID := *contextRef.SpaceID
		result.SpaceId = &spaceID
	}
	return result
}

func uciPreparedBeginMatches(response *pb.BeginCodeIndexResponse, scope *pb.CodeIndexScope) bool {
	return response != nil && response.GetBuildId() != "" && response.GetLeaseEpoch() != 0 && uciPreparedScopesMatch(response.GetScope(), scope)
}

func uciPreparedStageMatches(response *pb.StageCodeIndexResponse, begin *pb.BeginCodeIndexResponse, count int) bool {
	return response != nil && begin != nil && count > 0 && response.GetBuildId() == begin.GetBuildId() &&
		response.GetAcceptedSequence() == uint64(count-1) && response.GetAcceptedPartCount() == uint64(count) && response.GetPartDigest() != ""
}

func uciPreparedPublishedContext(response *pb.FinalizeCodeIndexResponse, binding uci.IndexBinding, begin *pb.BeginCodeIndexResponse, observedSequence int64) (uci.ContextRef, error) {
	if observedSequence < 0 || response == nil || begin == nil || response.GetBuildId() != begin.GetBuildId() || response.GetLeaseEpoch() != begin.GetLeaseEpoch() || response.GetAcceptedFilesystemSequence() != uint64(observedSequence) {
		return uci.ContextRef{}, fmt.Errorf("uci prepared index: server returned an invalid finalized build")
	}
	contextRef := response.GetPublishedContext()
	if contextRef == nil {
		return uci.ContextRef{}, fmt.Errorf("uci prepared index: server returned no published context")
	}
	result := uci.ContextRef{
		SourceID:          contextRef.GetSourceId(),
		CheckoutID:        contextRef.GetCheckoutId(),
		ViewID:            contextRef.GetViewId(),
		Generation:        contextRef.GetGeneration(),
		AnalysisProfileID: contextRef.GetAnalysisProfileId(),
	}
	if contextRef.SpaceId != nil {
		spaceID := contextRef.GetSpaceId()
		result.SpaceID = &spaceID
	}
	validated := binding.Clone()
	validated.Context = &result
	if err := validated.Validate(); err != nil {
		return uci.ContextRef{}, fmt.Errorf("uci prepared index: published context does not match binding: %w", err)
	}
	return result, nil
}

func uciPreparedScopesMatch(left, right *pb.CodeIndexScope) bool {
	return left != nil && right != nil && left.GetSourceId() == right.GetSourceId() && left.GetCheckoutId() == right.GetCheckoutId() &&
		left.GetIncarnationId() == right.GetIncarnationId() && left.GetAnalysisProfileId() == right.GetAnalysisProfileId()
}

func validUCIPreparedIndexIdentity(value string) bool {
	if value == "" || len(value) > uciLocalMaxOpaqueIDBytes || !utf8.ValidString(value) || strings.TrimSpace(value) != value {
		return false
	}
	for _, runeValue := range value {
		if runeValue < 0x20 || runeValue == 0x7f {
			return false
		}
	}
	return true
}

func validUCIPreparedIndexDigest(value uci.IndexDigest) bool {
	encoded := string(value)
	if len(encoded) != len("sha256:")+sha256.Size*2 || !strings.HasPrefix(encoded, "sha256:") {
		return false
	}
	for _, character := range encoded[len("sha256:"):] {
		if (character < '0' || character > '9') && (character < 'a' || character > 'f') {
			return false
		}
	}
	return true
}
