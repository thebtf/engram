package codeintel

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"path"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/thebtf/engram/internal/handlers/engramcore"
	"github.com/thebtf/engram/internal/uci"
	pb "github.com/thebtf/engram/proto/engram/v1"
	"google.golang.org/protobuf/types/known/timestamppb"
)

const uciPreparedSHA256Prefix = "sha256:"

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
	Logger             *slog.Logger
}

// UCIPreparedIndexCollaborator prepares and publishes immutable UCI admission
// frames for server-authorized targets. It has no project selector or path
// authority: the server binding and local registry jointly determine its root.
type UCIPreparedIndexCollaborator struct {
	workstationID            string
	clientInstanceID         string
	parserBundleDigest       uci.IndexDigest
	registry                 *UCILocalRegistry
	scanner                  UCIPreparedIndexScanner
	treeSitterParser         UCIPreparedTreeSitterParser
	goProfile                uci.GoExtractionProfile
	logger                   *slog.Logger
	scannerAggregateObserver uciPreparedScannerAggregateObserver
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
		logger:             config.Logger,
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
	if collaborator.logger != nil || collaborator.scannerAggregateObserver != nil {
		aggregate := uciPreparedScannerAggregateFor(local, scan)
		if collaborator.logger != nil {
			collaborator.logger.Info("codeintel: prepared scanner phase aggregate",
				"source_id", aggregate.SourceID,
				"checkout_id", aggregate.CheckoutID,
				"profile_id", aggregate.ProfileID,
				"observed_fs_seq", aggregate.ObservedFSSeq,
				"scan_started_at", aggregate.ScanStartedAt.Format(time.RFC3339Nano),
				"scan_completed_at", aggregate.ScanCompletedAt.Format(time.RFC3339Nano),
				"git_topology_duration_ns", aggregate.GitTopologyDurationNS,
				"git_status_duration_ns", aggregate.GitStatusDurationNS,
				"git_candidates_duration_ns", aggregate.GitCandidatesDurationNS,
				"git_staged_duration_ns", aggregate.GitStagedDurationNS,
				"git_untracked_duration_ns", aggregate.GitUntrackedDurationNS,
				"candidate_loop_duration_ns", aggregate.CandidateLoopDurationNS,
				"scan_total_duration_ns", aggregate.ScanTotalDurationNS,
				"residual_duration_ns", aggregate.ResidualDurationNS,
				"candidate_count", aggregate.CandidateCount,
				"admitted_count", aggregate.AdmittedCount,
				"excluded_count", aggregate.ExcludedCount,
				"unreadable_count", aggregate.UnreadableCount,
				"bytes_read", aggregate.BytesRead,
			)
		}
		if collaborator.scannerAggregateObserver != nil {
			collaborator.scannerAggregateObserver(aggregate)
		}
	}
	return scan, nil
}

const (
	uciPreparedMembershipMode                  = "unknown"
	uciPreparedGoResolverRevision              = "uci-prepared-go-call/v1"
	uciPreparedGoResolverRule                  = "go-direct-call/v1"
	uciPreparedGoResolverExplanation           = "unique same-package Go function declaration"
	uciPreparedGoPartialMessage                = "Go extraction is partial"
	uciPreparedTreeSitterProfileKeyVersion     = "uci-prepared-tree-sitter/v1"
	uciPreparedStructuredProfileKeyVersion     = "uci-prepared-structured/v1"
	uciPreparedTreeSitterUnavailableMessage    = "Tree-sitter parser is unavailable"
	uciPreparedTreeSitterProtocolMessage       = "Tree-sitter parser protocol failure"
	uciPreparedTreeSitterBundleMismatchMessage = "Tree-sitter parser bundle mismatch"
	uciPreparedTreeSitterPartialMessage        = "Tree-sitter parser coverage is partial"
	uciPreparedOpenAPIUnavailableMessage       = "OpenAPI extraction is unavailable"
)

type uciPreparedAdmissionFile struct {
	path       string
	membership uci.IndexAdmissionMembership
	artifact   *uci.IndexAdmissionArtifact
	edges      []uci.IndexAdmissionEdge
	errors     []string
}

// uciPreparedSourceCapability is one explicit, path-selected input contract.
// Its order is part of selection: OpenAPI entries precede their JSON/YAML
// fallbacks so ordinary structured files are never content-promoted.
type uciPreparedSourceCapability struct {
	key                string
	version            string
	extension          string
	alternateExtension string
	jsonYAMLFormat     uci.JSONYAMLFormat
	openAPIFormat      uci.OpenAPIFormat
	treeSitterLanguage uci.TreeSitterLanguage
	partialMessage     string
	prepare            uciPreparedSourcePreparer
}

// uciPreparedAdmissionInput carries one path-selected source preparation
// request without widening the preparer contract as formats are added.
type uciPreparedAdmissionInput struct {
	ctx               context.Context
	sourceID          string
	analysisProfileID string
	goProfile         uci.IndexAdmissionArtifactProfile
	file              uci.ScannerFile
	prepared          uciPreparedAdmissionFile
	capability        *uciPreparedSourceCapability
}

type uciPreparedSourcePreparer func(*UCIPreparedIndexCollaborator, uciPreparedAdmissionInput) (uciPreparedAdmissionFile, error)

var uciPreparedSourceCapabilities = [...]uciPreparedSourceCapability{
	{
		key:            "openapi-json",
		version:        "v1",
		extension:      ".json",
		openAPIFormat:  uci.OpenAPIFormatJSON,
		partialMessage: "OpenAPI extraction is partial",
		prepare:        uciPreparedPrepareOpenAPIAdmissionFile,
	},
	{
		key:                "openapi-yaml",
		version:            "v1",
		extension:          ".yaml",
		alternateExtension: ".yml",
		openAPIFormat:      uci.OpenAPIFormatYAML,
		partialMessage:     "OpenAPI extraction is partial",
		prepare:            uciPreparedPrepareOpenAPIAdmissionFile,
	},
	{
		key:            "go",
		version:        "v1",
		extension:      ".go",
		partialMessage: uciPreparedGoPartialMessage,
		prepare:        uciPreparedPrepareGoAdmissionFile,
	},
	{
		key:                "javascript",
		version:            "v1",
		extension:          ".js",
		treeSitterLanguage: uci.TreeSitterLanguageJavaScript,
		partialMessage:     uciPreparedTreeSitterPartialMessage,
		prepare:            uciPreparedPrepareTreeSitterAdmissionFile,
	},
	{
		key:                "typescript",
		version:            "v1",
		extension:          ".ts",
		treeSitterLanguage: uci.TreeSitterLanguageTypeScript,
		partialMessage:     uciPreparedTreeSitterPartialMessage,
		prepare:            uciPreparedPrepareTreeSitterAdmissionFile,
	},
	{
		key:                "tsx",
		version:            "v1",
		extension:          ".tsx",
		treeSitterLanguage: uci.TreeSitterLanguageTSX,
		partialMessage:     uciPreparedTreeSitterPartialMessage,
		prepare:            uciPreparedPrepareTreeSitterAdmissionFile,
	},
	{
		key:                "markdown",
		version:            "v1",
		extension:          ".md",
		alternateExtension: ".markdown",
		partialMessage:     "Markdown extraction is partial",
		prepare:            uciPreparedPrepareMarkdownAdmissionFile,
	},
	{
		key:            "json",
		version:        "v1",
		extension:      ".json",
		jsonYAMLFormat: uci.JSONYAMLFormatJSON,
		partialMessage: "JSON extraction is partial",
		prepare:        uciPreparedPrepareJSONYAMLAdmissionFile,
	},
	{
		key:                "yaml",
		version:            "v1",
		extension:          ".yaml",
		alternateExtension: ".yml",
		jsonYAMLFormat:     uci.JSONYAMLFormatYAML,
		partialMessage:     "YAML extraction is partial",
		prepare:            uciPreparedPrepareJSONYAMLAdmissionFile,
	},
	{
		key:            "sql",
		version:        "v1",
		extension:      ".sql",
		partialMessage: "SQL extraction is partial",
		prepare:        uciPreparedPrepareSQLAdmissionFile,
	},
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

	unresolved, err := uciPreparedAddResolvedSourceEdges(prepared)
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
		capability, found := uciPreparedSourceCapabilityForPath(file.Path)
		if !found {
			prepared.membership.State = uci.IndexAdmissionMembershipUnsupported
			prepared.errors = append(prepared.errors, file.Path+": source language is unsupported")
			return prepared, nil
		}
		if capability.prepare == nil {
			return uciPreparedAdmissionFile{}, fmt.Errorf("uci prepared index: source capability %q has no preparer", capability.key)
		}
		return capability.prepare(collaborator, uciPreparedAdmissionInput{ctx: ctx, sourceID: sourceID, analysisProfileID: analysisProfileID, goProfile: goProfile, file: file, prepared: prepared, capability: capability})
	default:
		return uciPreparedAdmissionFile{}, fmt.Errorf("uci prepared index: scanner returned unsupported state %q for %q", file.State, file.Path)
	}
}

func uciPreparedSourceCapabilityForPath(filePath string) (*uciPreparedSourceCapability, bool) {
	for index := range uciPreparedSourceCapabilities {
		capability := &uciPreparedSourceCapabilities[index]
		if capability.matches(filePath) {
			return capability, true
		}
	}
	return nil, false
}

func (capability *uciPreparedSourceCapability) matches(filePath string) bool {
	if capability.openAPIFormat != "" {
		base := path.Base(filePath)
		return uciPreparedOpenAPIPathMatches(base, capability.extension) ||
			(capability.alternateExtension != "" && uciPreparedOpenAPIPathMatches(base, capability.alternateExtension))
	}
	extension := path.Ext(filePath)
	return strings.EqualFold(extension, capability.extension) ||
		(capability.alternateExtension != "" && strings.EqualFold(extension, capability.alternateExtension))
}

func uciPreparedOpenAPIPathMatches(base, extension string) bool {
	want := "openapi" + extension
	if strings.EqualFold(base, want) {
		return true
	}
	want = "." + want
	return len(base) >= len(want) && strings.EqualFold(base[len(base)-len(want):], want)
}

func uciPreparedStructuredProfileKey(analysisProfileID string, capability *uciPreparedSourceCapability) string {
	return uciPreparedStructuredProfileKeyVersion + ":" + analysisProfileID + ":" + capability.key + ":" + capability.version
}

func uciPreparedPrepareGoAdmissionFile(collaborator *UCIPreparedIndexCollaborator, input uciPreparedAdmissionInput) (uciPreparedAdmissionFile, error) {
	extracted := uci.ExtractGo(input.file.Body, collaborator.goProfile)
	artifact, err := uci.NewIndexAdmissionArtifactFromGo(input.sourceID, input.goProfile, input.file.Body, extracted)
	if err != nil {
		if uci.IsIndexCapacityError(err) {
			return uciPreparedAdmissionFile{}, err
		}
		return uciPreparedAdmissionFile{}, fmt.Errorf("uci prepared index: normalize Go source %q: %w", input.file.Path, err)
	}
	return uciPreparedAttachAdmissionArtifact(input.prepared, artifact, input.capability.partialMessage), nil
}

func uciPreparedPrepareTreeSitterAdmissionFile(collaborator *UCIPreparedIndexCollaborator, input uciPreparedAdmissionInput) (uciPreparedAdmissionFile, error) {
	return collaborator.prepareTreeSitterAdmissionFile(input.ctx, input.sourceID, input.analysisProfileID, input.file, input.prepared, input.capability.treeSitterLanguage)
}

func uciPreparedPrepareMarkdownAdmissionFile(collaborator *UCIPreparedIndexCollaborator, input uciPreparedAdmissionInput) (uciPreparedAdmissionFile, error) {
	profile := uci.DefaultMarkdownExtractionProfile(uciPreparedStructuredProfileKey(input.analysisProfileID, input.capability))
	admissionProfile, err := uci.MarkdownIndexAdmissionArtifactProfile(profile)
	if err != nil {
		return uciPreparedAdmissionFile{}, fmt.Errorf("uci prepared index: configure Markdown admission profile: %w", err)
	}
	admissionProfile.ExtractionProfileDigest = collaborator.parserBundleDigest
	artifact, err := uci.NewIndexAdmissionArtifactFromMarkdown(input.sourceID, admissionProfile, profile, input.file.Body, uci.ExtractMarkdown(input.file.Body, profile))
	if err != nil {
		if uci.IsIndexCapacityError(err) {
			return uciPreparedAdmissionFile{}, err
		}
		return uciPreparedAdmissionFile{}, fmt.Errorf("uci prepared index: normalize Markdown source %q: %w", input.file.Path, err)
	}
	return uciPreparedAttachAdmissionArtifact(input.prepared, artifact, input.capability.partialMessage), nil
}

func uciPreparedPrepareJSONYAMLAdmissionFile(collaborator *UCIPreparedIndexCollaborator, input uciPreparedAdmissionInput) (uciPreparedAdmissionFile, error) {
	profile := uci.DefaultJSONYAMLExtractionProfile(uciPreparedStructuredProfileKey(input.analysisProfileID, input.capability), input.capability.jsonYAMLFormat)
	admissionProfile, err := uci.JSONYAMLIndexAdmissionArtifactProfile(profile)
	if err != nil {
		return uciPreparedAdmissionFile{}, fmt.Errorf("uci prepared index: configure %s admission profile: %w", input.capability.key, err)
	}
	admissionProfile.ExtractionProfileDigest = collaborator.parserBundleDigest
	artifact, err := uci.NewIndexAdmissionArtifactFromJSONYAML(input.sourceID, admissionProfile, profile, input.file.Body, uci.ExtractJSONYAML(input.file.Body, profile))
	if err != nil {
		if uci.IsIndexCapacityError(err) {
			return uciPreparedAdmissionFile{}, err
		}
		return uciPreparedAdmissionFile{}, fmt.Errorf("uci prepared index: normalize %s source %q: %w", input.capability.key, input.file.Path, err)
	}
	return uciPreparedAttachAdmissionArtifact(input.prepared, artifact, input.capability.partialMessage), nil
}

func uciPreparedPrepareSQLAdmissionFile(collaborator *UCIPreparedIndexCollaborator, input uciPreparedAdmissionInput) (uciPreparedAdmissionFile, error) {
	profile := uci.DefaultSQLExtractionProfile(uciPreparedStructuredProfileKey(input.analysisProfileID, input.capability))
	admissionProfile, err := uci.SQLIndexAdmissionArtifactProfile(profile)
	if err != nil {
		return uciPreparedAdmissionFile{}, fmt.Errorf("uci prepared index: configure SQL admission profile: %w", err)
	}
	admissionProfile.ExtractionProfileDigest = collaborator.parserBundleDigest
	artifact, err := uci.NewIndexAdmissionArtifactFromSQL(input.sourceID, admissionProfile, profile, input.file.Body, uci.ExtractSQL(input.file.Body, profile))
	if err != nil {
		if uci.IsIndexCapacityError(err) {
			return uciPreparedAdmissionFile{}, err
		}
		return uciPreparedAdmissionFile{}, fmt.Errorf("uci prepared index: normalize SQL source %q: %w", input.file.Path, err)
	}
	return uciPreparedAttachAdmissionArtifact(input.prepared, artifact, input.capability.partialMessage), nil
}

func uciPreparedPrepareOpenAPIAdmissionFile(collaborator *UCIPreparedIndexCollaborator, input uciPreparedAdmissionInput) (uciPreparedAdmissionFile, error) {
	profile := uci.DefaultOpenAPIExtractionProfile(uciPreparedStructuredProfileKey(input.analysisProfileID, input.capability), input.capability.openAPIFormat)
	admissionProfile, err := uci.OpenAPIIndexAdmissionArtifactProfile(profile)
	if err != nil {
		return uciPreparedAdmissionFile{}, fmt.Errorf("uci prepared index: configure OpenAPI admission profile: %w", err)
	}
	admissionProfile.ExtractionProfileDigest = collaborator.parserBundleDigest
	extracted := uci.ExtractOpenAPI(input.file.Body, profile)
	// A path-selected OpenAPI document with unavailable semantic coverage must
	// not be recast as generic JSON/YAML or published as partial facts.
	if extracted.Coverage == uci.IndexCoverageUnavailable {
		return uciPreparedAdmissionFile{}, fmt.Errorf("uci prepared index: %s for %q", uciPreparedOpenAPIUnavailableMessage, input.file.Path)
	}
	artifact, err := uci.NewIndexAdmissionArtifactFromOpenAPI(input.sourceID, admissionProfile, profile, input.file.Body, extracted)
	if err != nil {
		if uci.IsIndexCapacityError(err) {
			return uciPreparedAdmissionFile{}, err
		}
		return uciPreparedAdmissionFile{}, fmt.Errorf("uci prepared index: normalize OpenAPI source %q: %w", input.file.Path, err)
	}
	return uciPreparedAttachAdmissionArtifact(input.prepared, artifact, input.capability.partialMessage), nil
}

func uciPreparedAttachAdmissionArtifact(prepared uciPreparedAdmissionFile, artifact uci.IndexAdmissionArtifact, partialMessage string) uciPreparedAdmissionFile {
	artifactID := artifact.ArtifactID
	prepared.membership.State = uci.IndexAdmissionMembershipPresent
	prepared.membership.ArtifactID = &artifactID
	prepared.artifact = &artifact
	if artifact.Status == uci.IndexAdmissionArtifactPartial {
		prepared.errors = append(prepared.errors, prepared.path+": "+partialMessage)
	}
	return prepared
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
	return uciPreparedAttachAdmissionArtifact(prepared, artifact, uciPreparedTreeSitterPartialMessage), nil
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

type uciPreparedGoDefinitionTarget struct {
	path       string
	artifactID string
	localKey   string
}

func uciPreparedAddResolvedGoCallEdges(files []uciPreparedAdmissionFile) (uint64, error) {
	definitions := uciPreparedGoDefinitions(files)
	var unresolved uint64
	for index := range files {
		unresolved += uciPreparedAddGoFileCallEdges(&files[index], definitions)
	}
	return unresolved, nil
}

func uciPreparedGoDefinitions(files []uciPreparedAdmissionFile) map[string][]uciPreparedGoDefinitionTarget {
	definitions := make(map[string][]uciPreparedGoDefinitionTarget)
	for _, prepared := range files {
		if prepared.artifact == nil || prepared.membership.State != uci.IndexAdmissionMembershipPresent || prepared.artifact.Profile.Language != uci.IndexAdmissionLanguageGo {
			continue
		}
		for _, definition := range prepared.artifact.Definitions {
			if definition.Kind != "function" || !strings.HasPrefix(definition.LocalSymbolKey, "func:") {
				continue
			}
			definitions[definition.SymbolKey] = append(definitions[definition.SymbolKey], uciPreparedGoDefinitionTarget{
				path:       prepared.path,
				artifactID: prepared.artifact.ArtifactID,
				localKey:   definition.LocalSymbolKey,
			})
		}
	}
	return definitions
}

func uciPreparedAddGoFileCallEdges(prepared *uciPreparedAdmissionFile, definitions map[string][]uciPreparedGoDefinitionTarget) uint64 {
	if prepared.artifact == nil || prepared.membership.State != uci.IndexAdmissionMembershipPresent || prepared.artifact.Profile.Language != uci.IndexAdmissionLanguageGo {
		return 0
	}
	var unresolved uint64
	for _, reference := range prepared.artifact.References {
		if reference.Kind == "call" && !uciPreparedAppendGoCallEdge(prepared, reference, definitions) {
			unresolved++
		}
	}
	return unresolved
}

func uciPreparedAppendGoCallEdge(prepared *uciPreparedAdmissionFile, reference uci.IndexAdmissionReference, definitions map[string][]uciPreparedGoDefinitionTarget) bool {
	targetSymbol, established := uciPreparedGoCallTargetSymbol(reference)
	if !established || reference.OwnerSymbolKey == nil {
		return false
	}
	targets := definitions[targetSymbol]
	if len(targets) != 1 {
		return false
	}
	target := targets[0]
	sourceSymbolKey := *reference.OwnerSymbolKey
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
	return true
}

func uciPreparedAddResolvedSourceEdges(files []uciPreparedAdmissionFile) (uint64, error) {
	goUnresolved, err := uciPreparedAddResolvedGoCallEdges(files)
	if err != nil {
		return 0, err
	}
	treeSitterUnresolved, err := uciPreparedAddResolvedTreeSitterEdges(files)
	if err != nil {
		return 0, err
	}
	if ^uint64(0)-goUnresolved < treeSitterUnresolved {
		return 0, fmt.Errorf("uci prepared index: unresolved reference count overflow")
	}
	return goUnresolved + treeSitterUnresolved, nil
}

type uciPreparedTreeSitterTarget struct {
	path       string
	artifactID string
	symbolKey  string
}

func uciPreparedAddResolvedTreeSitterEdges(files []uciPreparedAdmissionFile) (uint64, error) {
	byPath, definitions := uciPreparedTreeSitterSources(files)
	var unresolved uint64
	for index := range files {
		unresolved += uciPreparedAddTreeSitterFileEdges(&files[index], byPath, definitions)
	}
	return unresolved, nil
}

func uciPreparedTreeSitterSources(files []uciPreparedAdmissionFile) (map[string]*uciPreparedAdmissionFile, map[string]map[string][]uciPreparedTreeSitterTarget) {
	byPath := make(map[string]*uciPreparedAdmissionFile, len(files))
	definitions := make(map[string]map[string][]uciPreparedTreeSitterTarget, len(files))
	for index := range files {
		file := &files[index]
		if file.artifact == nil || file.membership.State != uci.IndexAdmissionMembershipPresent {
			continue
		}
		byPath[file.path] = file
		if !uciPreparedTreeSitterLanguage(file.artifact.Profile.Language) {
			continue
		}
		byName := make(map[string][]uciPreparedTreeSitterTarget)
		for _, definition := range file.artifact.Definitions {
			name, ok := uciPreparedTreeSitterDefinitionName(definition.LocalSymbolKey)
			if !ok {
				continue
			}
			byName[name] = append(byName[name], uciPreparedTreeSitterTarget{path: file.path, artifactID: file.artifact.ArtifactID, symbolKey: definition.LocalSymbolKey})
		}
		definitions[file.path] = byName
	}
	return byPath, definitions
}

func uciPreparedAddTreeSitterFileEdges(file *uciPreparedAdmissionFile, byPath map[string]*uciPreparedAdmissionFile, definitions map[string]map[string][]uciPreparedTreeSitterTarget) uint64 {
	if file.artifact == nil || file.membership.State != uci.IndexAdmissionMembershipPresent || !uciPreparedTreeSitterLanguage(file.artifact.Profile.Language) {
		return 0
	}
	aliases, namespaces, unresolved := uciPreparedAddTreeSitterModuleEdges(file, byPath, definitions)
	unresolved += uciPreparedAddTreeSitterLocalEdges(file, aliases, namespaces, definitions)
	unresolved += uciPreparedAddTreeSitterExportEdges(file, definitions)
	return unresolved
}

func uciPreparedAddTreeSitterModuleEdges(file *uciPreparedAdmissionFile, byPath map[string]*uciPreparedAdmissionFile, definitions map[string]map[string][]uciPreparedTreeSitterTarget) (map[string]uciPreparedTreeSitterTarget, map[string]uciPreparedTreeSitterTarget, uint64) {
	aliases := make(map[string]uciPreparedTreeSitterTarget)
	namespaces := make(map[string]uciPreparedTreeSitterTarget)
	var unresolved uint64
	for _, reference := range file.artifact.References {
		module, imported, local, ok := uciPreparedTreeSitterModuleReference(reference)
		if !ok {
			continue
		}
		target, found := uciPreparedTreeSitterModuleTarget(byPath, definitions, file.path, module, imported)
		if !found {
			unresolved++
			continue
		}
		file.edges = append(file.edges, uciPreparedTreeSitterEdge(file.path, *file.artifact, reference, target))
		uciPreparedRememberTreeSitterLocal(aliases, namespaces, local, imported, target)
	}
	return aliases, namespaces, unresolved
}

func uciPreparedTreeSitterModuleTarget(byPath map[string]*uciPreparedAdmissionFile, definitions map[string]map[string][]uciPreparedTreeSitterTarget, sourcePath, module, imported string) (uciPreparedTreeSitterTarget, bool) {
	targetFile, found := uciPreparedResolveTreeSitterModule(byPath, sourcePath, module)
	if !found || targetFile.artifact == nil {
		return uciPreparedTreeSitterTarget{}, false
	}
	target := uciPreparedTreeSitterTarget{path: targetFile.path, artifactID: targetFile.artifact.ArtifactID}
	if imported == "" || imported == "*" || imported == "default" {
		return target, true
	}
	matches := definitions[targetFile.path][imported]
	if len(matches) != 1 {
		return uciPreparedTreeSitterTarget{}, false
	}
	return matches[0], true
}

func uciPreparedRememberTreeSitterLocal(aliases, namespaces map[string]uciPreparedTreeSitterTarget, local, imported string, target uciPreparedTreeSitterTarget) {
	if local == "" {
		return
	}
	if imported == "*" {
		namespaces[local] = target
	} else if target.symbolKey != "" {
		aliases[local] = target
	}
}

func uciPreparedAddTreeSitterLocalEdges(file *uciPreparedAdmissionFile, aliases, namespaces map[string]uciPreparedTreeSitterTarget, definitions map[string]map[string][]uciPreparedTreeSitterTarget) uint64 {
	var unresolved uint64
	for _, reference := range file.artifact.References {
		local, ok := uciPreparedTreeSitterLocalReference(reference)
		if !ok {
			continue
		}
		target, found := uciPreparedTreeSitterLocalTarget(local, aliases, namespaces, definitions)
		if !found {
			unresolved++
			continue
		}
		file.edges = append(file.edges, uciPreparedTreeSitterEdge(file.path, *file.artifact, reference, target))
	}
	return unresolved
}

func uciPreparedTreeSitterLocalTarget(local string, aliases, namespaces map[string]uciPreparedTreeSitterTarget, definitions map[string]map[string][]uciPreparedTreeSitterTarget) (uciPreparedTreeSitterTarget, bool) {
	if target, found := aliases[local]; found {
		return target, true
	}
	separator := strings.IndexByte(local, '.')
	if separator <= 0 || separator == len(local)-1 {
		return uciPreparedTreeSitterTarget{}, false
	}
	namespace, found := namespaces[local[:separator]]
	if !found {
		return uciPreparedTreeSitterTarget{}, false
	}
	matches := definitions[namespace.path][local[separator+1:]]
	if len(matches) != 1 {
		return uciPreparedTreeSitterTarget{}, false
	}
	return matches[0], true
}

func uciPreparedAddTreeSitterExportEdges(file *uciPreparedAdmissionFile, definitions map[string]map[string][]uciPreparedTreeSitterTarget) uint64 {
	var unresolved uint64
	for _, reference := range file.artifact.References {
		imported, _, ok := uciPreparedTreeSitterExportAlias(reference)
		if !ok {
			continue
		}
		matches := definitions[file.path][imported]
		if len(matches) != 1 {
			unresolved++
			continue
		}
		file.edges = append(file.edges, uciPreparedTreeSitterEdge(file.path, *file.artifact, reference, matches[0]))
	}
	return unresolved
}

func uciPreparedTreeSitterLanguage(language uci.IndexAdmissionLanguage) bool {
	switch language {
	case uci.IndexAdmissionLanguageJavaScript, uci.IndexAdmissionLanguageTypeScript, uci.IndexAdmissionLanguageTSX:
		return true
	default:
		return false
	}
}

func uciPreparedTreeSitterDefinitionName(localKey string) (string, bool) {
	separator := strings.LastIndexByte(localKey, ':')
	if separator < 1 || separator == len(localKey)-1 {
		return "", false
	}
	name := localKey[separator+1:]
	return name, !strings.ContainsAny(name, "@/#")
}

func uciPreparedTreeSitterModuleReference(reference uci.IndexAdmissionReference) (module, imported, local string, ok bool) {
	key, ok := uciPreparedTreeSitterSemanticKey(reference.SiteKey)
	if !ok {
		return "", "", "", false
	}
	var prefix string
	switch reference.Kind {
	case "import", "import_alias":
		prefix = "import:"
	case "reexport", "reexport_alias":
		prefix = "reexport:"
	default:
		return "", "", "", false
	}
	if !strings.HasPrefix(key, prefix) {
		return "", "", "", false
	}
	payload := strings.TrimPrefix(key, prefix)
	if reference.Kind == "import" || reference.Kind == "reexport" {
		return payload, "", "", payload != ""
	}
	hash := strings.LastIndexByte(payload, '#')
	colon := strings.LastIndexByte(payload, ':')
	if hash < 1 || colon <= hash+1 || colon == len(payload)-1 {
		return "", "", "", false
	}
	return payload[:hash], payload[hash+1 : colon], payload[colon+1:], true
}

func uciPreparedTreeSitterLocalReference(reference uci.IndexAdmissionReference) (string, bool) {
	key, ok := uciPreparedTreeSitterSemanticKey(reference.SiteKey)
	if !ok {
		return "", false
	}
	var prefix string
	switch reference.Kind {
	case "call":
		prefix = "call:"
	case "reference":
		prefix = "reference:"
	case "jsx_reference":
		prefix = "jsx_reference:"
	default:
		return "", false
	}
	if !strings.HasPrefix(key, prefix) {
		return "", false
	}
	local := strings.TrimPrefix(key, prefix)
	if local == "" || strings.ContainsAny(local, "()[]{}'\"") {
		return "", false
	}
	return local, true
}

func uciPreparedTreeSitterExportAlias(reference uci.IndexAdmissionReference) (imported, local string, ok bool) {
	if reference.Kind != "export_alias" {
		return "", "", false
	}
	key, ok := uciPreparedTreeSitterSemanticKey(reference.SiteKey)
	if !ok || !strings.HasPrefix(key, "export:") {
		return "", "", false
	}
	payload := strings.TrimPrefix(key, "export:")
	separator := strings.LastIndexByte(payload, ':')
	if separator < 1 || separator == len(payload)-1 {
		return "", "", false
	}
	return payload[:separator], payload[separator+1:], true
}

func uciPreparedTreeSitterSemanticKey(key string) (string, bool) {
	separator := strings.LastIndexByte(key, '@')
	if separator < 1 || separator == len(key)-1 {
		return "", false
	}
	position := key[separator+1:]
	colon := strings.IndexByte(position, ':')
	if colon < 1 || colon == len(position)-1 || !uciPreparedDecimal(position[:colon]) || !uciPreparedDecimal(position[colon+1:]) {
		return "", false
	}
	return key[:separator], true
}

func uciPreparedDecimal(value string) bool {
	if value == "" {
		return false
	}
	for _, character := range value {
		if character < '0' || character > '9' {
			return false
		}
	}
	return true
}

func uciPreparedResolveTreeSitterModule(files map[string]*uciPreparedAdmissionFile, sourcePath, module string) (*uciPreparedAdmissionFile, bool) {
	return uciPreparedUniqueModuleFile(files, uciPreparedTreeSitterModuleCandidates(sourcePath, module))
}

func uciPreparedTreeSitterModuleCandidates(sourcePath, module string) []string {
	if module == "" || !strings.HasPrefix(module, ".") || path.IsAbs(module) {
		return nil
	}
	base := path.Clean(path.Join(path.Dir(sourcePath), module))
	if base == "." || base == ".." || strings.HasPrefix(base, "../") {
		return nil
	}
	candidates := []string{base}
	extension := strings.ToLower(path.Ext(base))
	if extension == "" {
		for _, suffix := range []string{".ts", ".tsx", ".js", ".json", "/index.ts", "/index.tsx", "/index.js"} {
			candidates = append(candidates, base+suffix)
		}
	} else if extension == ".js" {
		stem := strings.TrimSuffix(base, path.Ext(base))
		candidates = append(candidates, stem+".ts", stem+".tsx")
	}
	return candidates
}

func uciPreparedUniqueModuleFile(files map[string]*uciPreparedAdmissionFile, candidates []string) (*uciPreparedAdmissionFile, bool) {
	var matched *uciPreparedAdmissionFile
	seen := make(map[string]struct{}, len(candidates))
	for _, candidate := range candidates {
		if _, duplicate := seen[candidate]; duplicate {
			continue
		}
		seen[candidate] = struct{}{}
		file := files[candidate]
		if file == nil {
			continue
		}
		if matched != nil && matched.path != file.path {
			return nil, false
		}
		matched = file
	}
	return matched, matched != nil
}

func uciPreparedTreeSitterEdge(sourcePath string, source uci.IndexAdmissionArtifact, reference uci.IndexAdmissionReference, target uciPreparedTreeSitterTarget) uci.IndexAdmissionEdge {
	var sourceSymbol *string
	if reference.OwnerSymbolKey != nil {
		value := *reference.OwnerSymbolKey
		sourceSymbol = &value
	}
	var targetSymbol *string
	if target.symbolKey != "" {
		value := target.symbolKey
		targetSymbol = &value
	}
	return uci.IndexAdmissionEdge{
		EdgeKey:          uciPreparedTreeSitterEdgeKey(sourcePath, reference.SiteKey, target.path, target.symbolKey, reference.Relation),
		SourceArtifactID: source.ArtifactID,
		SourceSymbolKey:  sourceSymbol,
		Target: &uci.IndexAdmissionEdgeTarget{
			PathKey:    target.path,
			ArtifactID: target.artifactID,
			SymbolKey:  targetSymbol,
		},
		Relation:         reference.Relation,
		EvidenceKind:     uci.IndexEvidenceKind("resolved"),
		ResolutionState:  uci.IndexResolutionState("resolved"),
		ResolverRevision: "uci-prepared-tree-sitter-module/v1",
		Evidence: uci.IndexAdmissionEdgeEvidence{
			ReferenceSiteKey: reference.SiteKey,
			Span:             reference.Span,
			RuleKey:          "tree-sitter-module-alias/v1",
			Explanation:      "unique relative module and exported symbol in the same prepared source",
		},
	}
}

func uciPreparedTreeSitterEdgeKey(sourcePath, siteKey, targetPath, targetSymbol string, relation uci.IndexRelation) string {
	state := sha256.New()
	for _, value := range []string{"uci-prepared-tree-sitter-edge/v1", sourcePath, siteKey, targetPath, targetSymbol, string(relation)} {
		_, _ = state.Write([]byte{byte(len(value) >> 24), byte(len(value) >> 16), byte(len(value) >> 8), byte(len(value))})
		_, _ = state.Write([]byte(value))
	}
	return "tree-sitter:" + hex.EncodeToString(state.Sum(nil))
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
	intentClaim := uciPreparedProtoIntentClaim(target.IndexIntentClaim())
	begin, err := client.Begin(ctx, &pb.BeginCodeIndexRequest{
		Scope:          scope,
		OwnerInstance:  collaborator.clientInstanceID,
		BuildKey:       buildKey,
		ExpectedParent: parent,
		ManifestMode:   "full",
		JobKind:        jobKind,
		IntentClaim:    intentClaim,
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
			IntentClaim:   intentClaim,
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
		IntentClaim:                intentClaim,
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
	parts, publication, err := uciPreparedPublicationParts(plan.frames)
	if err != nil {
		return uciPreparedPublication{}, err
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

func uciPreparedPublicationParts(frames []uci.IndexAdmissionFrame) ([]uci.IndexPart, uciPreparedPublication, error) {
	publication := uciPreparedPublication{memberships: make([]uci.IndexMembership, 0), replacements: make([]uci.IndexEdgeReplacement, 0)}
	parts := make([]uci.IndexPart, 0, len(frames))
	seenMemberships := make(map[string]struct{})
	seenReplacements := make(map[string]struct{})
	for frameIndex, frame := range frames {
		part, err := frame.PublicationPart()
		if err != nil {
			return nil, uciPreparedPublication{}, fmt.Errorf("uci prepared index: derive publication part %d: %w", frameIndex, err)
		}
		parts = append(parts, part)
		if err := uciPreparedAppendPublicationPart(&publication, part, seenMemberships, seenReplacements); err != nil {
			return nil, uciPreparedPublication{}, err
		}
	}
	return parts, publication, nil
}

func uciPreparedAppendPublicationPart(publication *uciPreparedPublication, part uci.IndexPart, seenMemberships, seenReplacements map[string]struct{}) error {
	if err := uciPreparedAppendMemberships(publication, part.Memberships, seenMemberships); err != nil {
		return err
	}
	return uciPreparedAppendEdgeReplacements(publication, part.EdgeReplacements, seenReplacements)
}

func uciPreparedAppendMemberships(publication *uciPreparedPublication, memberships []uci.IndexMembership, seen map[string]struct{}) error {
	for _, membership := range memberships {
		if _, found := seen[membership.PathKey]; found {
			return fmt.Errorf("uci prepared index: duplicate packed membership %q", membership.PathKey)
		}
		seen[membership.PathKey] = struct{}{}
		publication.memberships = append(publication.memberships, membership)
	}
	return nil
}

func uciPreparedAppendEdgeReplacements(publication *uciPreparedPublication, replacements []uci.IndexEdgeReplacement, seen map[string]struct{}) error {
	for _, replacement := range replacements {
		if _, found := seen[replacement.SourcePath]; found {
			return fmt.Errorf("uci prepared index: duplicate packed edge replacement %q", replacement.SourcePath)
		}
		if ^uint64(0)-publication.edgeCount < uint64(len(replacement.Edges)) {
			return fmt.Errorf("uci prepared index: edge count overflow")
		}
		seen[replacement.SourcePath] = struct{}{}
		publication.edgeCount += uint64(len(replacement.Edges))
		publication.replacements = append(publication.replacements, replacement)
	}
	return nil
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

func uciPreparedProtoIntentClaim(claim *uci.IndexIntentExecutionClaim) *pb.CodeIndexIntentClaim {
	if claim == nil {
		return nil
	}
	return &pb.CodeIndexIntentClaim{
		IntentRef: claim.IntentID, OwnerEpoch: uint64(claim.Epoch), ProcessNonce: claim.ProcessNonce,
	}
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
	if len(encoded) != len(uciPreparedSHA256Prefix)+sha256.Size*2 || !strings.HasPrefix(encoded, uciPreparedSHA256Prefix) {
		return false
	}
	for _, character := range encoded[len(uciPreparedSHA256Prefix):] {
		if (character < '0' || character > '9') && (character < 'a' || character > 'f') {
			return false
		}
	}
	return true
}
