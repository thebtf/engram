package uci

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

const (
	defaultScannerMaxFileBytes   int64 = 8 << 20
	scannerReadAttempts                = 2
	scannerSecretInspectionLimit int64 = 64 << 10
	// scannerProjectAnchorPath is tracked identity control metadata. The V3
	// resolver consumes it before code admission; it is not source content and
	// must not affect code-search coverage.
	scannerProjectAnchorPath = ".engram-project"
)

var (
	ErrScannerUnavailable = errors.New("uci scanner: dependency unavailable")
	ErrScannerInvalidRoot = errors.New("uci scanner: invalid authorized root")
	ErrScannerGitFailure  = errors.New("uci scanner: git plumbing failed")
	ErrScannerMalformed   = errors.New("uci scanner: malformed git output")
)

// AuthorizedRootEvidence is the already-authorized repository root that may be
// enumerated. It deliberately contains no selection or authorization inputs.
type AuthorizedRootEvidence struct {
	RootPath string
}

// GitInvocation is one fixed, argument-vector Git plumbing invocation.
type GitInvocation struct {
	Args []string
	Env  []string
}

// GitResult captures local Git process output without interpreting it as shell
// input.
type GitResult struct {
	Stdout   []byte
	Stderr   []byte
	ExitCode int
}

// GitRunner executes the argument vector supplied by Scanner. Implementations
// must not invoke a shell.
type GitRunner interface {
	Run(context.Context, GitInvocation) (GitResult, error)
}

// ExecGitRunner is the production GitRunner implementation. The executable is
// "git" unless Path is supplied by trusted process configuration.
type ExecGitRunner struct {
	Path string
}

// Run executes Git directly with the supplied argument vector. A process exit
// status is returned in GitResult so Scanner can distinguish expected unborn
// HEAD from a plumbing failure.
func (runner ExecGitRunner) Run(ctx context.Context, invocation GitInvocation) (GitResult, error) {
	if len(invocation.Args) == 0 {
		return GitResult{}, fmt.Errorf("%w: empty argument vector", ErrScannerGitFailure)
	}

	executable := runner.Path
	if executable == "" {
		executable = "git"
	}

	command := exec.CommandContext(ctx, executable, invocation.Args...)
	command.Env = append([]string(nil), invocation.Env...)

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr

	err := command.Run()
	result := GitResult{Stdout: stdout.Bytes(), Stderr: stderr.Bytes()}
	if err == nil {
		return result, nil
	}

	var exitError *exec.ExitError
	if errors.As(err, &exitError) {
		result.ExitCode = exitError.ExitCode()
		return result, nil
	}
	return result, err
}

// ScannerFileInfo is the minimum lstat fact Scanner needs to admit a local
// file without following a symlink, junction, or other reparse point.
type ScannerFileInfo struct {
	Mode         fs.FileMode
	Size         int64
	ModTime      time.Time
	ReparsePoint bool
}

// ScannerFileSystem is a narrow read-only filesystem boundary. Lstat must not
// follow links. ReadFile is used only after Scanner has admitted the path.
type ScannerFileSystem interface {
	Lstat(string) (ScannerFileInfo, error)
	ReadFile(string) ([]byte, error)
}

// OSScannerFileSystem is the production ScannerFileSystem implementation.
type OSScannerFileSystem struct{}

// Lstat returns link-preserving metadata and detects Windows reparse points.
func (OSScannerFileSystem) Lstat(filePath string) (ScannerFileInfo, error) {
	info, err := os.Lstat(filePath)
	if err != nil {
		return ScannerFileInfo{}, err
	}
	return ScannerFileInfo{
		Mode:         info.Mode(),
		Size:         info.Size(),
		ModTime:      info.ModTime(),
		ReparsePoint: scannerFileIsReparse(info),
	}, nil
}

// ReadFile reads one scanner-admitted path.
func (OSScannerFileSystem) ReadFile(filePath string) ([]byte, error) {
	return os.ReadFile(filePath)
}

// ScannerSecretDetector classifies bytes that must never become scanner output.
type ScannerSecretDetector interface {
	ContainsSecret(path string, body []byte) bool
}

// ScannerSecretDetectorFunc adapts a function to ScannerSecretDetector.
type ScannerSecretDetectorFunc func(path string, body []byte) bool

// ContainsSecret implements ScannerSecretDetector.
func (detector ScannerSecretDetectorFunc) ContainsSecret(filePath string, body []byte) bool {
	return detector(filePath, body)
}

// ScannerPolicy constrains local evidence admission.
type ScannerPolicy struct {
	IncludeUntracked bool
	MaxFileBytes     int64
	ProtectedPaths   []string
	SecretDetector   ScannerSecretDetector
}

// ScannerExclusion describes why a known candidate did not yield content.
type ScannerExclusion string

const (
	ScannerExclusionNone             ScannerExclusion = ""
	ScannerExclusionProtected        ScannerExclusion = "protected"
	ScannerExclusionSecret           ScannerExclusion = "secret"
	ScannerExclusionTooLarge         ScannerExclusion = "too_large"
	ScannerExclusionUnsupportedType  ScannerExclusion = "unsupported_type"
	ScannerExclusionBinary           ScannerExclusion = "binary"
	ScannerExclusionNestedRepository ScannerExclusion = "nested_repository"
	ScannerExclusionSubmodule        ScannerExclusion = "submodule"
	ScannerExclusionReparseEscape    ScannerExclusion = "reparse_escape"
	ScannerExclusionChanging         ScannerExclusion = "changing"
)

// ScannerFile is one deterministic local evidence fact. Body is set only for
// Present files and is copied before return.
type ScannerFile struct {
	Path      string
	Body      []byte
	State     IndexFileState
	Exclusion ScannerExclusion
}

// ScannerCensus says whether the result has authority to reconcile every
// eligible membership. Only a complete census can authorize delete-all.
type ScannerCensus struct {
	Outcome      IndexScanOutcome
	Complete     bool
	CanDeleteAll bool
}

// ScannerDiagnostics is the invocation-local attribution for Scanner.Scan.
// It is diagnostic-only: it never affects admission, coverage, or publication.
type ScannerDiagnostics struct {
	GitTopologyDuration   time.Duration
	GitStatusDuration     time.Duration
	GitCandidatesDuration time.Duration
	GitStagedDuration     time.Duration
	GitUntrackedDuration  time.Duration
	CandidateLoopDuration time.Duration
	TotalDuration         time.Duration
	ResidualDuration      time.Duration
	CandidateCount        int
	AdmittedCount         int
	ExcludedCount         int
	UnreadableCount       int
	BytesRead             int64
}

// ScannerResult contains scanner-local facts only. It does not select a
// context, grant access, publish a view, or request deletions.
type ScannerResult struct {
	Files       []ScannerFile
	Census      ScannerCensus
	Observation IndexObservation
	Coverage    IndexCoverage
	Diagnostics ScannerDiagnostics
}

// Scanner enumerates one explicitly authorized Git worktree through narrowly
// fixed Git plumbing and a read-only filesystem boundary.
type Scanner struct {
	git    GitRunner
	files  ScannerFileSystem
	policy ScannerPolicy

	topologyMu     sync.Mutex
	topologyCache  *scannerTopologyCache
	candidateMu    sync.Mutex
	candidateCache *scannerCandidateCache
}

// NewScanner constructs a boundary scanner. Its policy slice is copied so a
// caller cannot mutate the scanner's protection rules while it scans.
func NewScanner(git GitRunner, files ScannerFileSystem, policy ScannerPolicy) *Scanner {
	policy.ProtectedPaths = append([]string(nil), policy.ProtectedPaths...)
	return &Scanner{git: git, files: files, policy: policy}
}

// Scan emits deterministic local evidence for one already-authorized root. A
// malformed Git candidate or plumbing failure is failed and never grants
// delete-all authority; per-file read failures and races remain explicit facts.
func (scanner *Scanner) Scan(ctx context.Context, evidence AuthorizedRootEvidence) (ScannerResult, error) {
	started := time.Now()
	result := ScannerResult{
		Observation: IndexObservation{ScanStart: started.UTC()},
		Coverage: IndexCoverage{
			Lexical: IndexCoverageUnavailable,
			Vector:  IndexCoverageUnavailable,
		},
	}

	if scanner == nil || scanner.git == nil || scanner.files == nil {
		return scanner.failed(result, started, ErrScannerUnavailable)
	}
	if ctx == nil {
		return scanner.failed(result, started, fmt.Errorf("%w: nil context", ErrScannerInvalidRoot))
	}
	if err := ctx.Err(); err != nil {
		return scanner.incomplete(result, started, err)
	}

	root, err := scannerAuthorizedRoot(evidence.RootPath)
	if err != nil {
		return scanner.failed(result, started, err)
	}

	rootInfo, err := scanner.files.Lstat(root)
	if err != nil || scannerInfoIsReparse(rootInfo) || !rootInfo.Mode.IsDir() {
		scanner.clearTopologyCache()
		return scanner.failed(result, started, fmt.Errorf("%w: root unavailable", ErrScannerInvalidRoot))
	}

	topology, err := scanner.repositoryTopology(ctx, root, rootInfo, &result.Diagnostics.GitTopologyDuration)
	if err != nil {
		return scanner.classifyGitError(result, started, err)
	}
	objectFormat := topology.objectFormat
	result.Observation.ObjectFormat = scannerStringPointer(objectFormat)

	statusOutput, err := scanner.gitOutput(ctx, root, &result.Diagnostics.GitStatusDuration, "status", "--porcelain=v2", "--branch", "-z", "--untracked-files=all")
	if err != nil {
		return scanner.classifyGitError(result, started, err)
	}
	statusRecords, head, hasHead, refLabel, hasRefLabel, err := scannerStatusBranchRecords(statusOutput, objectFormat)
	if err != nil {
		return scanner.failed(result, started, err)
	}
	if hasHead {
		result.Observation.HeadOID = scannerStringPointer(head)
	}
	if hasRefLabel {
		result.Observation.RefLabel = scannerStringPointer(refLabel)
	}
	result.Observation.Dirty = len(statusRecords) > 0

	candidates, err := scanner.scanCandidates(ctx, root, topology, statusRecords, &result.Diagnostics.GitCandidatesDuration)
	if err != nil {
		return scanner.classifyGitError(result, started, err)
	}
	result.Diagnostics.CandidateCount = len(candidates)
	partial, interrupted, err := scanner.scanCandidateFiles(ctx, root, candidates, &result, started)
	if err != nil {
		if interrupted {
			return scanner.incomplete(result, started, err)
		}
		return scanner.failed(result, started, err)
	}

	if partial {
		scanner.finish(&result, started, IndexScanIncomplete)
	} else {
		scanner.finish(&result, started, IndexScanComplete)
	}
	return result, nil
}

func (scanner *Scanner) scanCandidateFiles(ctx context.Context, root string, candidates []scannerCandidate, result *ScannerResult, started time.Time) (bool, bool, error) {
	result.Files = make([]ScannerFile, 0, len(candidates))
	partial := false
	candidateLoopStarted := time.Now()
	for _, candidate := range candidates {
		if err := ctx.Err(); err != nil {
			result.Diagnostics.CandidateLoopDuration = time.Since(candidateLoopStarted)
			return partial, true, err
		}
		if candidate.path == scannerProjectAnchorPath {
			continue
		}

		file, incomplete, bytesRead, err := scanner.scanCandidate(ctx, root, candidate)
		result.Diagnostics.BytesRead += bytesRead
		if err != nil {
			result.Diagnostics.CandidateLoopDuration = time.Since(candidateLoopStarted)
			return partial, false, err
		}
		if incomplete {
			partial = true
		}
		result.Files = append(result.Files, file)
		switch file.State {
		case IndexFilePresent:
			result.Diagnostics.AdmittedCount++
		case IndexFileExcluded:
			result.Coverage.ExcludedFiles++
			result.Diagnostics.ExcludedCount++
		case IndexFileUnreadable:
			result.Coverage.UnreadableFiles++
			result.Diagnostics.UnreadableCount++
		}
	}
	result.Diagnostics.CandidateLoopDuration = time.Since(candidateLoopStarted)
	return partial, false, nil
}

type scannerTopologyCache struct {
	input    scannerTopologyInput
	topology scannerRepositoryTopology
}

type scannerTopologyInput struct {
	root              string
	rootInfo          ScannerFileInfo
	markerInfo        ScannerFileInfo
	markerFingerprint [sha256.Size]byte
}

type scannerRepositoryTopology struct {
	root         string
	gitDir       string
	commonGitDir string
	headPath     string
	objectFormat string
}

type scannerCandidateCache struct {
	topology scannerRepositoryTopology
	index    scannerIndexFingerprint
	tracked  []scannerCandidate
}

type scannerIndexFingerprint struct {
	path    string
	present bool
	digest  [sha256.Size]byte
}

func (scanner *Scanner) repositoryTopology(ctx context.Context, root string, rootInfo ScannerFileInfo, duration *time.Duration) (scannerRepositoryTopology, error) {
	input, err := scanner.topologyInput(root, rootInfo)
	if err != nil {
		scanner.clearTopologyCache()
		return scannerRepositoryTopology{}, err
	}
	if topology, ok := scanner.cachedTopology(input); ok {
		return topology, nil
	}
	scanner.clearCandidateCache()

	output, err := scanner.gitOutput(ctx, root, duration, "rev-parse", "--show-toplevel", "--absolute-git-dir", "--git-common-dir", "--git-path", "HEAD", "--show-object-format")
	if err != nil {
		scanner.clearTopologyCache()
		return scannerRepositoryTopology{}, err
	}
	topology, err := scannerRepositoryTopologyFromOutput(output, root)
	if err != nil {
		scanner.clearTopologyCache()
		return scannerRepositoryTopology{}, err
	}

	currentRootInfo, err := scanner.files.Lstat(root)
	if err != nil || scannerInfoIsReparse(currentRootInfo) || !currentRootInfo.Mode.IsDir() {
		return topology, nil
	}
	current, err := scanner.topologyInput(root, currentRootInfo)
	if err == nil && scannerTopologyInputsEqual(input, current) {
		scanner.storeTopology(input, topology)
	}
	return topology, nil
}

func (scanner *Scanner) topologyInput(root string, rootInfo ScannerFileInfo) (scannerTopologyInput, error) {
	markerPath := filepath.Join(root, ".git")
	markerInfo, err := scanner.files.Lstat(markerPath)
	if err != nil || scannerInfoIsReparse(markerInfo) || (!markerInfo.Mode.IsDir() && !markerInfo.Mode.IsRegular()) {
		return scannerTopologyInput{}, fmt.Errorf("%w: repository marker unavailable", ErrScannerInvalidRoot)
	}

	input := scannerTopologyInput{root: root, rootInfo: rootInfo, markerInfo: markerInfo}
	if markerInfo.Mode.IsRegular() {
		marker, err := scanner.files.ReadFile(markerPath)
		if err != nil {
			return scannerTopologyInput{}, fmt.Errorf("%w: repository marker unavailable", ErrScannerInvalidRoot)
		}
		input.markerFingerprint = sha256.Sum256(marker)
	}
	return input, nil
}

func (scanner *Scanner) cachedTopology(input scannerTopologyInput) (scannerRepositoryTopology, bool) {
	scanner.topologyMu.Lock()
	defer scanner.topologyMu.Unlock()
	if scanner.topologyCache == nil || !scannerTopologyInputsEqual(scanner.topologyCache.input, input) {
		return scannerRepositoryTopology{}, false
	}
	return scanner.topologyCache.topology, true
}

func (scanner *Scanner) storeTopology(input scannerTopologyInput, topology scannerRepositoryTopology) {
	scanner.topologyMu.Lock()
	defer scanner.topologyMu.Unlock()
	scanner.topologyCache = &scannerTopologyCache{input: input, topology: topology}
}

func (scanner *Scanner) clearTopologyCache() {
	scanner.topologyMu.Lock()
	scanner.topologyCache = nil
	scanner.topologyMu.Unlock()
	scanner.clearCandidateCache()
}

func scannerTopologyInputsEqual(left, right scannerTopologyInput) bool {
	if !scannerPathsEqual(left.root, right.root) || left.rootInfo.Mode != right.rootInfo.Mode || left.rootInfo.ReparsePoint != right.rootInfo.ReparsePoint || left.markerInfo.Mode != right.markerInfo.Mode || left.markerInfo.ReparsePoint != right.markerInfo.ReparsePoint {
		return false
	}
	return !left.markerInfo.Mode.IsRegular() || left.markerFingerprint == right.markerFingerprint
}

func (scanner *Scanner) scanCandidates(ctx context.Context, root string, topology scannerRepositoryTopology, statusRecords []string, duration *time.Duration) ([]scannerCandidate, error) {
	untracked, err := scannerStatusUntrackedCandidates(statusRecords)
	if err != nil {
		return nil, err
	}

	index, err := scanner.indexFingerprint(topology)
	if err != nil {
		scanner.clearCandidateCache()
		return nil, err
	}
	tracked, cached := scanner.cachedCandidates(topology, index)
	if !cached {
		output, err := scanner.gitOutput(ctx, root, duration, "ls-files", "--stage", "--others", "--exclude-standard", "-t", "-z")
		if err != nil {
			scanner.clearCandidateCache()
			return nil, err
		}
		tracked, _, err = scannerCombinedCandidates(output, topology.objectFormat)
		if err != nil {
			scanner.clearCandidateCache()
			return nil, err
		}

		current, err := scanner.indexFingerprint(topology)
		if err != nil {
			scanner.clearCandidateCache()
			return nil, err
		}
		if !scannerIndexFingerprintsEqual(index, current) {
			scanner.clearCandidateCache()
			return nil, fmt.Errorf("%w: Git index changed during candidate enumeration", ErrScannerMalformed)
		}
		scanner.storeCandidates(topology, current, tracked)
	}

	return scannerMergeCandidates(tracked, untracked, scanner.policy.IncludeUntracked)
}

func (scanner *Scanner) indexFingerprint(topology scannerRepositoryTopology) (scannerIndexFingerprint, error) {
	if topology.gitDir == "" || strings.IndexByte(topology.gitDir, 0) >= 0 {
		return scannerIndexFingerprint{}, fmt.Errorf("%w: Git index path", ErrScannerMalformed)
	}

	indexPath := filepath.Join(topology.gitDir, "index")
	initial, err := scanner.files.Lstat(indexPath)
	if errors.Is(err, fs.ErrNotExist) {
		return scannerIndexFingerprint{path: indexPath}, nil
	}
	if err != nil || scannerInfoIsReparse(initial) || !initial.Mode.IsRegular() || initial.Size < 0 {
		return scannerIndexFingerprint{}, fmt.Errorf("%w: Git index unavailable", ErrScannerMalformed)
	}

	body, err := scanner.files.ReadFile(indexPath)
	if err != nil {
		return scannerIndexFingerprint{}, fmt.Errorf("%w: Git index unavailable", ErrScannerMalformed)
	}
	current, err := scanner.files.Lstat(indexPath)
	if err != nil || !scannerSameFileInfo(initial, current) || current.Size < 0 || int64(len(body)) != current.Size {
		return scannerIndexFingerprint{}, fmt.Errorf("%w: Git index changed", ErrScannerMalformed)
	}
	return scannerIndexFingerprint{
		path:    indexPath,
		present: true,
		digest:  sha256.Sum256(body),
	}, nil
}

func (scanner *Scanner) cachedCandidates(topology scannerRepositoryTopology, index scannerIndexFingerprint) ([]scannerCandidate, bool) {
	scanner.candidateMu.Lock()
	defer scanner.candidateMu.Unlock()
	if scanner.candidateCache == nil || !scannerRepositoryTopologiesEqual(scanner.candidateCache.topology, topology) || !scannerIndexFingerprintsEqual(scanner.candidateCache.index, index) {
		return nil, false
	}
	return scannerCopyCandidates(scanner.candidateCache.tracked), true
}

func (scanner *Scanner) storeCandidates(topology scannerRepositoryTopology, index scannerIndexFingerprint, tracked []scannerCandidate) {
	scanner.candidateMu.Lock()
	defer scanner.candidateMu.Unlock()
	scanner.candidateCache = &scannerCandidateCache{
		topology: topology,
		index:    index,
		tracked:  scannerCopyCandidates(tracked),
	}
}

func (scanner *Scanner) clearCandidateCache() {
	scanner.candidateMu.Lock()
	defer scanner.candidateMu.Unlock()
	scanner.candidateCache = nil
}

func scannerRepositoryTopologiesEqual(left, right scannerRepositoryTopology) bool {
	return scannerPathsEqual(left.root, right.root) &&
		scannerPathsEqual(left.gitDir, right.gitDir) &&
		scannerPathsEqual(left.commonGitDir, right.commonGitDir) &&
		scannerPathsEqual(left.headPath, right.headPath) &&
		left.objectFormat == right.objectFormat
}

func scannerIndexFingerprintsEqual(left, right scannerIndexFingerprint) bool {
	if !scannerPathsEqual(left.path, right.path) || left.present != right.present {
		return false
	}
	return !left.present || left.digest == right.digest
}

func scannerCopyCandidates(candidates []scannerCandidate) []scannerCandidate {
	return append([]scannerCandidate(nil), candidates...)
}

func scannerRepositoryTopologyFromOutput(output []byte, root string) (scannerRepositoryTopology, error) {
	values, err := scannerGitLines(output, 5)
	if err != nil {
		return scannerRepositoryTopology{}, err
	}
	normalizedRoot, err := scannerAuthorizedRoot(values[0])
	if err != nil || !scannerPathsEqual(root, normalizedRoot) {
		return scannerRepositoryTopology{}, fmt.Errorf("%w: repository root does not match authorization", ErrScannerInvalidRoot)
	}
	if values[1] == "" || values[2] == "" || values[3] == "" {
		return scannerRepositoryTopology{}, fmt.Errorf("%w: incomplete repository topology", ErrScannerMalformed)
	}
	if values[4] != "sha1" && values[4] != "sha256" {
		return scannerRepositoryTopology{}, fmt.Errorf("%w: unsupported object format", ErrScannerMalformed)
	}
	return scannerRepositoryTopology{
		root:         normalizedRoot,
		gitDir:       values[1],
		commonGitDir: values[2],
		headPath:     values[3],
		objectFormat: values[4],
	}, nil
}

func (scanner *Scanner) scanCandidate(ctx context.Context, root string, candidate scannerCandidate) (ScannerFile, bool, int64, error) {
	if err := scannerValidateGitPath(candidate.path); err != nil {
		return ScannerFile{}, false, 0, err
	}
	if scannerPathProtected(scanner.policy.ProtectedPaths, candidate.path) {
		return scannerExcludedFile(candidate.path, ScannerExclusionProtected), false, 0, nil
	}
	if candidate.mode == "160000" {
		return scannerExcludedFile(candidate.path, ScannerExclusionSubmodule), false, 0, nil
	}
	if candidate.mode == "120000" {
		return scannerExcludedFile(candidate.path, ScannerExclusionReparseEscape), false, 0, nil
	}

	fullPath, err := scannerJoinRoot(root, candidate.path)
	if err != nil {
		return ScannerFile{}, false, 0, err
	}

	location, err := scanner.checkCandidateLocation(root, candidate.path)
	if err != nil {
		return scannerUnreadableFile(candidate.path), true, 0, nil
	}
	switch location {
	case scannerLocationReparse:
		return scannerExcludedFile(candidate.path, ScannerExclusionReparseEscape), false, 0, nil
	case scannerLocationNestedRepository:
		return scannerExcludedFile(candidate.path, ScannerExclusionNestedRepository), false, 0, nil
	case scannerLocationUnsupported:
		return scannerExcludedFile(candidate.path, ScannerExclusionUnsupportedType), false, 0, nil
	}

	return scanner.readStableFile(ctx, fullPath, candidate.path)
}

func (scanner *Scanner) checkCandidateLocation(root, relativePath string) (scannerLocation, error) {
	components := strings.Split(relativePath, "/")
	parent := root
	for index := range len(components) - 1 {
		parent = filepath.Join(parent, components[index])
		info, err := scanner.files.Lstat(parent)
		if err != nil {
			return scannerLocationSafe, err
		}
		if scannerInfoIsReparse(info) {
			return scannerLocationReparse, nil
		}
		if !info.Mode.IsDir() {
			return scannerLocationUnsupported, nil
		}

		marker, markerErr := scanner.files.Lstat(filepath.Join(parent, ".git"))
		if markerErr == nil {
			_ = marker
			return scannerLocationNestedRepository, nil
		}
		if !errors.Is(markerErr, fs.ErrNotExist) {
			return scannerLocationSafe, markerErr
		}
	}
	return scannerLocationSafe, nil
}

func (scanner *Scanner) readStableFile(ctx context.Context, fullPath, relativePath string) (ScannerFile, bool, int64, error) {
	maxBytes := scannerMaxBytes(scanner.policy.MaxFileBytes)
	var bytesRead int64
	for attempt := range scannerReadAttempts {
		if err := ctx.Err(); err != nil {
			return ScannerFile{}, true, bytesRead, err
		}

		initial, err := scanner.files.Lstat(fullPath)
		if err != nil {
			if attempt+1 < scannerReadAttempts {
				continue
			}
			return scannerUnreadableFile(relativePath), true, bytesRead, nil
		}
		if file, excluded := scanner.initialReadExclusion(relativePath, initial, maxBytes); excluded {
			return file, false, bytesRead, nil
		}

		body, err := scanner.files.ReadFile(fullPath)
		if err != nil {
			if attempt+1 < scannerReadAttempts {
				continue
			}
			return scannerUnreadableFile(relativePath), true, bytesRead, nil
		}
		bytesRead += int64(len(body))
		if err := ctx.Err(); err != nil {
			return ScannerFile{}, true, bytesRead, err
		}

		file, changing := scanner.stableReadResult(fullPath, relativePath, initial, body, maxBytes)
		if changing && attempt+1 < scannerReadAttempts {
			continue
		}
		return file, changing, bytesRead, nil
	}

	return scannerUnreadableFile(relativePath), true, bytesRead, nil
}

func (scanner *Scanner) initialReadExclusion(relativePath string, initial ScannerFileInfo, maxBytes int64) (ScannerFile, bool) {
	if scannerInfoIsReparse(initial) {
		return scannerExcludedFile(relativePath, ScannerExclusionReparseEscape), true
	}
	if !initial.Mode.IsRegular() {
		return scannerExcludedFile(relativePath, ScannerExclusionUnsupportedType), true
	}
	if initial.Size < 0 || (initial.Size > maxBytes && !scannerMayInspectForSecret(initial.Size, scanner.policy.SecretDetector)) {
		return scannerExcludedFile(relativePath, ScannerExclusionTooLarge), true
	}
	return ScannerFile{}, false
}

func (scanner *Scanner) stableReadResult(fullPath, relativePath string, initial ScannerFileInfo, body []byte, maxBytes int64) (ScannerFile, bool) {
	current, err := scanner.files.Lstat(fullPath)
	if err != nil || !scannerSameFileInfo(initial, current) || int64(len(body)) != current.Size {
		return ScannerFile{Path: relativePath, State: IndexFileUnreadable, Exclusion: ScannerExclusionChanging}, true
	}
	if current.Size < 0 || (current.Size > maxBytes && !scannerMayInspectForSecret(current.Size, scanner.policy.SecretDetector)) {
		return scannerExcludedFile(relativePath, ScannerExclusionTooLarge), false
	}
	if scannerBinary(body) {
		return scannerExcludedFile(relativePath, ScannerExclusionBinary), false
	}
	if detector := scanner.policy.SecretDetector; detector != nil && detector.ContainsSecret(relativePath, body) {
		return scannerExcludedFile(relativePath, ScannerExclusionSecret), false
	}
	if current.Size > maxBytes {
		return scannerExcludedFile(relativePath, ScannerExclusionTooLarge), false
	}
	return ScannerFile{Path: relativePath, Body: append([]byte(nil), body...), State: IndexFilePresent}, false
}

func (scanner *Scanner) gitOutput(ctx context.Context, root string, duration *time.Duration, command ...string) ([]byte, error) {
	started := time.Now()
	result, err := scanner.runGit(ctx, root, command...)
	*duration = time.Since(started)
	if err != nil {
		return nil, err
	}
	if result.ExitCode != 0 {
		return nil, fmt.Errorf("%w: non-zero exit", ErrScannerGitFailure)
	}
	return append([]byte(nil), result.Stdout...), nil
}

func (scanner *Scanner) runGit(ctx context.Context, root string, command ...string) (GitResult, error) {
	if err := ctx.Err(); err != nil {
		return GitResult{}, err
	}
	args := scannerGitArgs(root, command...)
	result, err := scanner.git.Run(ctx, GitInvocation{
		Args: args,
		Env:  scannerGitEnvironment(),
	})
	if err != nil {
		return GitResult{}, fmt.Errorf("%w: %w", ErrScannerGitFailure, err)
	}
	if err := ctx.Err(); err != nil {
		return GitResult{}, err
	}
	return result, nil
}

func (scanner *Scanner) classifyGitError(result ScannerResult, started time.Time, err error) (ScannerResult, error) {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return scanner.incomplete(result, started, err)
	}
	return scanner.failed(result, started, err)
}

func (scanner *Scanner) failed(result ScannerResult, started time.Time, err error) (ScannerResult, error) {
	scanner.finish(&result, started, IndexScanFailed)
	return result, err
}

func (scanner *Scanner) incomplete(result ScannerResult, started time.Time, err error) (ScannerResult, error) {
	scanner.finish(&result, started, IndexScanIncomplete)
	return result, err
}

func (scanner *Scanner) finish(result *ScannerResult, started time.Time, outcome IndexScanOutcome) {
	finished := time.Now()
	result.Observation.ScanEnd = finished.UTC()
	result.Diagnostics.TotalDuration = finished.Sub(started)
	measured := result.Diagnostics.GitTopologyDuration +
		result.Diagnostics.GitStatusDuration +
		result.Diagnostics.GitCandidatesDuration +
		result.Diagnostics.GitStagedDuration +
		result.Diagnostics.GitUntrackedDuration +
		result.Diagnostics.CandidateLoopDuration
	if result.Diagnostics.TotalDuration > measured {
		result.Diagnostics.ResidualDuration = result.Diagnostics.TotalDuration - measured
	}
	result.Census.Outcome = outcome
	switch outcome {
	case IndexScanComplete:
		result.Census.Complete = true
		result.Census.CanDeleteAll = true
		result.Coverage.Structural = IndexCoverageComplete
	case IndexScanIncomplete:
		result.Census.Complete = false
		result.Census.CanDeleteAll = false
		result.Coverage.Structural = IndexCoveragePartial
	default:
		result.Census.Complete = false
		result.Census.CanDeleteAll = false
		result.Coverage.Structural = IndexCoverageUnavailable
	}
}

type scannerCandidate struct {
	path    string
	mode    string
	tracked bool
}

func scannerStagedCandidates(output []byte, objectFormat string) ([]scannerCandidate, error) {
	records, err := scannerNULRecords(output)
	if err != nil {
		return nil, err
	}

	candidates := make([]scannerCandidate, 0, len(records))
	for _, record := range records {
		candidate, err := scannerStagedCandidate(record, objectFormat)
		if err != nil {
			return nil, err
		}
		candidates = append(candidates, candidate)
	}
	return candidates, nil
}

func scannerCombinedCandidates(output []byte, objectFormat string) ([]scannerCandidate, []scannerCandidate, error) {
	records, err := scannerNULRecords(output)
	if err != nil {
		return nil, nil, err
	}

	staged := make([]scannerCandidate, 0, len(records))
	untracked := make([]scannerCandidate, 0, len(records))
	for _, record := range records {
		if len(record) < 3 || record[1] != ' ' {
			return nil, nil, fmt.Errorf("%w: tagged candidate record", ErrScannerMalformed)
		}
		switch record[0] {
		case 'H', 'S', 'M':
			candidate, err := scannerStagedCandidate(record[2:], objectFormat)
			if err != nil {
				return nil, nil, err
			}
			staged = append(staged, candidate)
		case '?':
			candidate := scannerCandidate{path: record[2:]}
			if err := scannerValidateGitPath(candidate.path); err != nil {
				return nil, nil, err
			}
			untracked = append(untracked, candidate)
		default:
			return nil, nil, fmt.Errorf("%w: candidate status tag", ErrScannerMalformed)
		}
	}
	return staged, untracked, nil
}

func scannerStatusUntrackedCandidates(records []string) ([]scannerCandidate, error) {
	candidates := make([]scannerCandidate, 0, len(records))
	for index := 0; index < len(records); index++ {
		candidate, include, consumeNext, err := scannerStatusUntrackedCandidate(records, index)
		if err != nil {
			return nil, err
		}
		if include {
			candidates = append(candidates, candidate)
		}
		if consumeNext {
			index++
		}
	}
	return candidates, nil
}

func scannerStatusUntrackedCandidate(records []string, index int) (scannerCandidate, bool, bool, error) {
	record := records[index]
	if len(record) < 2 || record[1] != ' ' {
		return scannerCandidate{}, false, false, fmt.Errorf("%w: status record", ErrScannerMalformed)
	}
	switch record[0] {
	case '?':
		candidate := scannerCandidate{path: record[2:]}
		if err := scannerValidateGitPath(candidate.path); err != nil {
			return scannerCandidate{}, false, false, err
		}
		return candidate, true, false, nil
	case '!':
		if err := scannerValidateGitPath(record[2:]); err != nil {
			return scannerCandidate{}, false, false, err
		}
		return scannerCandidate{}, false, false, nil
	case '1':
		fields, candidatePath, err := scannerStatusRecordFields(record, 8)
		if err != nil || fields[0] != "1" || !scannerValidStatusXY(fields[1]) || scannerValidateGitPath(candidatePath) != nil {
			return scannerCandidate{}, false, false, fmt.Errorf("%w: ordinary status record", ErrScannerMalformed)
		}
		return scannerCandidate{}, false, false, nil
	case '2':
		fields, candidatePath, err := scannerStatusRecordFields(record, 9)
		if err != nil || fields[0] != "2" || !scannerValidStatusXY(fields[1]) || scannerValidateGitPath(candidatePath) != nil || index+1 >= len(records) {
			return scannerCandidate{}, false, false, fmt.Errorf("%w: rename status record", ErrScannerMalformed)
		}
		if err := scannerValidateGitPath(records[index+1]); err != nil {
			return scannerCandidate{}, false, false, err
		}
		if fields[1][0] == '.' && (fields[1][1] == 'R' || fields[1][1] == 'C') {
			return scannerCandidate{path: candidatePath}, true, true, nil
		}
		return scannerCandidate{}, false, true, nil
	case 'u':
		fields, candidatePath, err := scannerStatusRecordFields(record, 10)
		if err != nil || fields[0] != "u" || !scannerValidStatusXY(fields[1]) || scannerValidateGitPath(candidatePath) != nil {
			return scannerCandidate{}, false, false, fmt.Errorf("%w: unmerged status record", ErrScannerMalformed)
		}
		return scannerCandidate{}, false, false, nil
	default:
		return scannerCandidate{}, false, false, fmt.Errorf("%w: status record kind", ErrScannerMalformed)
	}
}

func scannerStatusRecordFields(record string, count int) ([]string, string, error) {
	fields := make([]string, 0, count)
	start := 0
	for range count {
		end := strings.IndexByte(record[start:], ' ')
		if end <= 0 {
			return nil, "", fmt.Errorf("%w: status record fields", ErrScannerMalformed)
		}
		end += start
		fields = append(fields, record[start:end])
		start = end + 1
	}
	if start >= len(record) {
		return nil, "", fmt.Errorf("%w: status record path", ErrScannerMalformed)
	}
	return fields, record[start:], nil
}

func scannerValidStatusXY(value string) bool {
	if len(value) != 2 {
		return false
	}
	for index := range value {
		switch value[index] {
		case '.', 'M', 'T', 'A', 'D', 'R', 'C', 'U':
		default:
			return false
		}
	}
	return true
}

func scannerStagedCandidate(record, objectFormat string) (scannerCandidate, error) {
	separator := strings.IndexByte(record, '\t')
	if separator < 0 {
		return scannerCandidate{}, fmt.Errorf("%w: index record", ErrScannerMalformed)
	}
	header := strings.Fields(record[:separator])
	if len(header) != 3 || !scannerValidMode(header[0]) || !scannerValidOID(header[1], objectFormat) {
		return scannerCandidate{}, fmt.Errorf("%w: index header", ErrScannerMalformed)
	}
	stage, err := strconv.Atoi(header[2])
	if err != nil || stage < 0 || stage > 3 {
		return scannerCandidate{}, fmt.Errorf("%w: index stage", ErrScannerMalformed)
	}
	candidate := scannerCandidate{path: record[separator+1:], mode: header[0], tracked: true}
	if err := scannerValidateGitPath(candidate.path); err != nil {
		return scannerCandidate{}, err
	}
	return candidate, nil
}

func scannerUntrackedCandidates(output []byte) ([]scannerCandidate, error) {
	records, err := scannerNULRecords(output)
	if err != nil {
		return nil, err
	}

	candidates := make([]scannerCandidate, 0, len(records))
	for _, record := range records {
		if err := scannerValidateGitPath(record); err != nil {
			return nil, err
		}
		candidates = append(candidates, scannerCandidate{path: record})
	}
	return candidates, nil
}

func scannerMergeCandidates(staged, untracked []scannerCandidate, includeUntracked bool) ([]scannerCandidate, error) {
	byPath := make(map[string]scannerCandidate, len(staged)+len(untracked))
	scannerMergeStagedCandidates(byPath, staged)
	if includeUntracked {
		scannerMergeUntrackedCandidates(byPath, untracked)
	}
	if err := scannerRejectCaseAmbiguity(byPath); err != nil {
		return nil, err
	}
	return scannerSortedCandidates(byPath), nil
}

func scannerMergeStagedCandidates(byPath map[string]scannerCandidate, staged []scannerCandidate) {
	for _, candidate := range staged {
		existing, exists := byPath[candidate.path]
		if !exists || (existing.mode != "160000" && candidate.mode == "160000") {
			byPath[candidate.path] = candidate
		}
	}
}

func scannerMergeUntrackedCandidates(byPath map[string]scannerCandidate, untracked []scannerCandidate) {
	for _, candidate := range untracked {
		if _, exists := byPath[candidate.path]; !exists {
			byPath[candidate.path] = candidate
		}
	}
}

func scannerRejectCaseAmbiguity(byPath map[string]scannerCandidate) error {
	if runtime.GOOS != "windows" {
		return nil
	}
	caseFolded := make(map[string]string, len(byPath))
	for candidatePath := range byPath {
		key := strings.ToLower(candidatePath)
		if previous, exists := caseFolded[key]; exists && previous != candidatePath {
			return fmt.Errorf("%w: case-ambiguous candidates", ErrScannerMalformed)
		}
		caseFolded[key] = candidatePath
	}
	return nil
}

func scannerSortedCandidates(byPath map[string]scannerCandidate) []scannerCandidate {
	paths := make([]string, 0, len(byPath))
	for candidatePath := range byPath {
		paths = append(paths, candidatePath)
	}
	sort.Strings(paths)
	merged := make([]scannerCandidate, 0, len(paths))
	for _, candidatePath := range paths {
		merged = append(merged, byPath[candidatePath])
	}
	return merged
}

func scannerNULRecords(output []byte) ([]string, error) {
	if len(output) == 0 {
		return nil, nil
	}
	if output[len(output)-1] != 0 {
		return nil, fmt.Errorf("%w: non-NUL-terminated plumbing output", ErrScannerMalformed)
	}

	records := make([]string, 0, bytes.Count(output, []byte{0}))
	for start := 0; start < len(output); {
		end := bytes.IndexByte(output[start:], 0)
		if end < 0 {
			return nil, fmt.Errorf("%w: NUL framing", ErrScannerMalformed)
		}
		end += start
		if end == start {
			return nil, fmt.Errorf("%w: empty NUL record", ErrScannerMalformed)
		}
		records = append(records, string(output[start:end]))
		start = end + 1
	}
	return records, nil
}

func scannerGitLines(output []byte, count int) ([]string, error) {
	if len(output) == 0 || output[len(output)-1] != '\n' || bytes.IndexByte(output, 0) >= 0 {
		return nil, fmt.Errorf("%w: repository topology lines", ErrScannerMalformed)
	}
	values := bytes.Split(output[:len(output)-1], []byte{'\n'})
	if len(values) != count {
		return nil, fmt.Errorf("%w: repository topology count", ErrScannerMalformed)
	}
	result := make([]string, count)
	for index, value := range values {
		if len(value) > 0 && value[len(value)-1] == '\r' {
			value = value[:len(value)-1]
		}
		if len(value) == 0 || !utf8.Valid(value) {
			return nil, fmt.Errorf("%w: repository topology value", ErrScannerMalformed)
		}
		result[index] = string(value)
	}
	return result, nil
}

func scannerStatusBranchRecords(output []byte, objectFormat string) ([]string, string, bool, string, bool, error) {
	records, err := scannerNULRecords(output)
	if err != nil {
		return nil, "", false, "", false, err
	}
	status := make([]string, 0, len(records))
	state := scannerBranchState{}
	for _, record := range records {
		statusRecord, include, err := scannerBranchRecord(record, objectFormat, &state)
		if err != nil {
			return nil, "", false, "", false, err
		}
		if include {
			status = append(status, statusRecord)
		}
	}
	if !state.sawOID || !state.sawHead {
		return nil, "", false, "", false, fmt.Errorf("%w: branch metadata omitted", ErrScannerMalformed)
	}
	return status, state.head, state.hasHead, state.refLabel, state.hasRefLabel, nil
}

type scannerBranchState struct {
	head        string
	refLabel    string
	hasHead     bool
	hasRefLabel bool
	sawOID      bool
	sawHead     bool
}

func scannerBranchRecord(record, objectFormat string, state *scannerBranchState) (string, bool, error) {
	switch {
	case strings.HasPrefix(record, "# branch.oid "):
		return "", false, scannerSetBranchOID(strings.TrimPrefix(record, "# branch.oid "), objectFormat, state)
	case strings.HasPrefix(record, "# branch.head "):
		return "", false, scannerSetBranchHead(strings.TrimPrefix(record, "# branch.head "), state)
	case strings.HasPrefix(record, "# "):
		if !utf8.ValidString(record) {
			return "", false, fmt.Errorf("%w: branch metadata", ErrScannerMalformed)
		}
		return "", false, nil
	default:
		return record, true, nil
	}
}

func scannerSetBranchOID(value, objectFormat string, state *scannerBranchState) error {
	if state.sawOID {
		return fmt.Errorf("%w: duplicate branch OID", ErrScannerMalformed)
	}
	state.sawOID = true
	if value == "(initial)" {
		return nil
	}
	if !scannerValidOID(value, objectFormat) {
		return fmt.Errorf("%w: branch OID", ErrScannerMalformed)
	}
	state.head, state.hasHead = value, true
	return nil
}

func scannerSetBranchHead(value string, state *scannerBranchState) error {
	if state.sawHead {
		return fmt.Errorf("%w: duplicate branch head", ErrScannerMalformed)
	}
	state.sawHead = true
	if value == "(detached)" || value == "(unknown)" {
		return nil
	}
	if value == "" || !utf8.ValidString(value) {
		return fmt.Errorf("%w: branch head", ErrScannerMalformed)
	}
	state.refLabel, state.hasRefLabel = value, true
	return nil
}

func scannerValidOID(value, objectFormat string) bool {
	length := 0
	switch objectFormat {
	case "sha1":
		length = 40
	case "sha256":
		length = 64
	default:
		return false
	}
	if len(value) != length {
		return false
	}
	for index := range len(value) {
		character := value[index]
		if !(character >= '0' && character <= '9') && !(character >= 'a' && character <= 'f') {
			return false
		}
	}
	return true
}

func scannerValidMode(mode string) bool {
	if len(mode) != 6 {
		return false
	}
	for index := range mode {
		if mode[index] < '0' || mode[index] > '7' {
			return false
		}
	}
	return true
}

func scannerValidateGitPath(candidate string) error {
	if candidate == "" || !utf8.ValidString(candidate) || strings.IndexByte(candidate, 0) >= 0 {
		return fmt.Errorf("%w: candidate path", ErrScannerMalformed)
	}
	if strings.Contains(candidate, "\\") || strings.HasPrefix(candidate, "/") || strings.HasSuffix(candidate, "/") {
		return fmt.Errorf("%w: non-root-relative candidate", ErrScannerMalformed)
	}
	if filepath.IsAbs(filepath.FromSlash(candidate)) || filepath.VolumeName(filepath.FromSlash(candidate)) != "" {
		return fmt.Errorf("%w: non-root-relative candidate", ErrScannerMalformed)
	}
	if path.Clean(candidate) != candidate {
		return fmt.Errorf("%w: non-canonical candidate", ErrScannerMalformed)
	}
	for _, component := range strings.Split(candidate, "/") {
		if component == "" || component == "." || component == ".." || strings.Contains(component, ":") {
			return fmt.Errorf("%w: non-root-relative candidate", ErrScannerMalformed)
		}
	}
	return nil
}

func scannerAuthorizedRoot(raw string) (string, error) {
	if raw == "" || strings.IndexByte(raw, 0) >= 0 {
		return "", fmt.Errorf("%w: empty root", ErrScannerInvalidRoot)
	}
	root, err := filepath.Abs(raw)
	if err != nil {
		return "", fmt.Errorf("%w: root normalization", ErrScannerInvalidRoot)
	}
	return filepath.Clean(root), nil
}

func scannerJoinRoot(root, relativePath string) (string, error) {
	if err := scannerValidateGitPath(relativePath); err != nil {
		return "", err
	}
	joined := filepath.Join(root, filepath.FromSlash(relativePath))
	relative, err := filepath.Rel(root, joined)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) || filepath.IsAbs(relative) {
		return "", fmt.Errorf("%w: candidate escaped authorized root", ErrScannerMalformed)
	}
	return joined, nil
}

func scannerPathsEqual(left, right string) bool {
	left = filepath.Clean(left)
	right = filepath.Clean(right)
	if runtime.GOOS == "windows" {
		return strings.EqualFold(left, right)
	}
	return left == right
}

func scannerPathProtected(protectedPaths []string, candidate string) bool {
	first, _, _ := strings.Cut(candidate, "/")
	if scannerPathEqual(first, ".git") {
		return true
	}
	for _, protected := range protectedPaths {
		protected = strings.ReplaceAll(protected, "\\", "/")
		protected = strings.TrimPrefix(protected, "./")
		protected = strings.TrimPrefix(protected, "/")
		protected = strings.TrimSuffix(protected, "/")
		if protected == "" {
			continue
		}
		if strings.ContainsAny(protected, "*?[") {
			matched, err := path.Match(protected, candidate)
			if err == nil && matched {
				return true
			}
			continue
		}
		if scannerPathEqual(candidate, protected) || scannerPathPrefix(candidate, protected) {
			return true
		}
	}
	return false
}

func scannerPathEqual(left, right string) bool {
	if runtime.GOOS == "windows" {
		return strings.EqualFold(left, right)
	}
	return left == right
}

func scannerPathPrefix(candidate, prefix string) bool {
	if runtime.GOOS == "windows" {
		if len(candidate) < len(prefix) || !strings.EqualFold(candidate[:len(prefix)], prefix) {
			return false
		}
	} else if !strings.HasPrefix(candidate, prefix) {
		return false
	}
	return len(candidate) > len(prefix) && candidate[len(prefix)] == '/'
}

type scannerLocation uint8

const (
	scannerLocationSafe scannerLocation = iota
	scannerLocationReparse
	scannerLocationNestedRepository
	scannerLocationUnsupported
)

func scannerExcludedFile(candidate string, exclusion ScannerExclusion) ScannerFile {
	return ScannerFile{Path: candidate, State: IndexFileExcluded, Exclusion: exclusion}
}

func scannerUnreadableFile(candidate string) ScannerFile {
	return ScannerFile{Path: candidate, State: IndexFileUnreadable}
}

func scannerInfoIsReparse(info ScannerFileInfo) bool {
	return info.ReparsePoint || info.Mode&fs.ModeSymlink != 0
}

func scannerSameFileInfo(left, right ScannerFileInfo) bool {
	return left.Mode == right.Mode &&
		left.Size == right.Size &&
		left.ReparsePoint == right.ReparsePoint &&
		left.ModTime.Equal(right.ModTime)
}

func scannerMaxBytes(configured int64) int64 {
	if configured > 0 {
		return configured
	}
	return defaultScannerMaxFileBytes
}

func scannerMayInspectForSecret(size int64, detector ScannerSecretDetector) bool {
	return detector != nil && size <= scannerSecretInspectionLimit
}

func scannerBinary(body []byte) bool {
	return bytes.IndexByte(body, 0) >= 0 || !utf8.Valid(body)
}

func scannerStringPointer(value string) *string {
	copy := value
	return &copy
}

func scannerGitArgs(root string, command ...string) []string {
	args := make([]string, 0, 2+len(command))
	args = append(args, "-C", root)
	return append(args, command...)
}

func scannerGitEnvironment() []string {
	environment := os.Environ()
	filtered := make([]string, 0, len(environment)+22)
	for _, entry := range environment {
		name, _, found := strings.Cut(entry, "=")
		if !found || scannerInheritedGitInfluence(name) {
			continue
		}
		filtered = append(filtered, entry)
	}
	return append(filtered,
		"GIT_OPTIONAL_LOCKS=0",
		"GIT_TERMINAL_PROMPT=0",
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL="+os.DevNull,
		"GIT_CONFIG_COUNT=4",
		"GIT_CONFIG_KEY_0=core.hooksPath",
		"GIT_CONFIG_VALUE_0="+os.DevNull,
		"GIT_CONFIG_KEY_1=core.fsmonitor",
		"GIT_CONFIG_VALUE_1=false",
		"GIT_CONFIG_KEY_2=core.useBuiltinFSMonitor",
		"GIT_CONFIG_VALUE_2=false",
		"GIT_CONFIG_KEY_3=core.untrackedCache",
		"GIT_CONFIG_VALUE_3=false",
		"GIT_ASKPASS=",
		"SSH_ASKPASS=",
		"GIT_SSH=",
		"GIT_SSH_COMMAND=",
		"GIT_ALLOW_PROTOCOL=",
		"GIT_PROTOCOL_FROM_USER=0",
		"GIT_NO_REPLACE_OBJECTS=1",
		"GIT_ATTR_NOSYSTEM=1",
		"GCM_INTERACTIVE=Never",
	)
}

func scannerInheritedGitInfluence(name string) bool {
	upper := strings.ToUpper(name)
	if strings.HasPrefix(upper, "GIT_") {
		return true
	}
	switch upper {
	case "SSH_ASKPASS", "SSH_AUTH_SOCK", "SSH_AGENT_PID", "GCM_INTERACTIVE", "PAGER", "EDITOR", "VISUAL":
		return true
	default:
		return false
	}
}
