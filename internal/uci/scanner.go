package uci

import (
	"bytes"
	"context"
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
	"time"
	"unicode/utf8"
)

const (
	defaultScannerMaxFileBytes   int64 = 8 << 20
	scannerReadAttempts                = 2
	scannerSecretInspectionLimit int64 = 64 << 10
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

// ScannerResult contains scanner-local facts only. It does not select a
// context, grant access, publish a view, or request deletions.
type ScannerResult struct {
	Files       []ScannerFile
	Census      ScannerCensus
	Observation IndexObservation
	Coverage    IndexCoverage
}

// Scanner enumerates one explicitly authorized Git worktree through narrowly
// fixed Git plumbing and a read-only filesystem boundary.
type Scanner struct {
	git    GitRunner
	files  ScannerFileSystem
	policy ScannerPolicy
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
	started := time.Now().UTC()
	result := ScannerResult{
		Observation: IndexObservation{ScanStart: started},
		Coverage: IndexCoverage{
			Lexical: IndexCoverageUnavailable,
			Vector:  IndexCoverageUnavailable,
		},
	}

	if scanner == nil || scanner.git == nil || scanner.files == nil {
		return scanner.failed(result, ErrScannerUnavailable)
	}
	if ctx == nil {
		return scanner.failed(result, fmt.Errorf("%w: nil context", ErrScannerInvalidRoot))
	}
	if err := ctx.Err(); err != nil {
		return scanner.incomplete(result, err)
	}

	root, err := scannerAuthorizedRoot(evidence.RootPath)
	if err != nil {
		return scanner.failed(result, err)
	}

	rootInfo, err := scanner.files.Lstat(root)
	if err != nil || scannerInfoIsReparse(rootInfo) || !rootInfo.Mode.IsDir() {
		return scanner.failed(result, fmt.Errorf("%w: root unavailable", ErrScannerInvalidRoot))
	}

	repositoryRoot, err := scanner.gitLine(ctx, root, "rev-parse", "--show-toplevel")
	if err != nil {
		return scanner.classifyGitError(result, err)
	}
	normalizedRepositoryRoot, err := scannerAuthorizedRoot(repositoryRoot)
	if err != nil || !scannerPathsEqual(root, normalizedRepositoryRoot) {
		return scanner.failed(result, fmt.Errorf("%w: repository root does not match authorization", ErrScannerInvalidRoot))
	}

	if _, err := scanner.gitLine(ctx, root, "rev-parse", "--absolute-git-dir"); err != nil {
		return scanner.classifyGitError(result, err)
	}
	if _, err := scanner.gitLine(ctx, root, "rev-parse", "--git-common-dir"); err != nil {
		return scanner.classifyGitError(result, err)
	}
	if _, err := scanner.gitLine(ctx, root, "rev-parse", "--git-path", "HEAD"); err != nil {
		return scanner.classifyGitError(result, err)
	}

	objectFormat, err := scanner.gitLine(ctx, root, "rev-parse", "--show-object-format")
	if err != nil {
		return scanner.classifyGitError(result, err)
	}
	if objectFormat != "sha1" && objectFormat != "sha256" {
		return scanner.failed(result, fmt.Errorf("%w: unsupported object format", ErrScannerMalformed))
	}
	result.Observation.ObjectFormat = scannerStringPointer(objectFormat)

	head, hasHead, err := scanner.gitHead(ctx, root, objectFormat)
	if err != nil {
		return scanner.classifyGitError(result, err)
	}
	if hasHead {
		result.Observation.HeadOID = scannerStringPointer(head)
	}
	refLabel, hasRefLabel, err := scanner.gitRefLabel(ctx, root)
	if err != nil {
		return scanner.classifyGitError(result, err)
	}
	if hasRefLabel {
		result.Observation.RefLabel = scannerStringPointer(refLabel)
	}

	statusOutput, err := scanner.gitOutput(ctx, root, "status", "--porcelain=v2", "-z")
	if err != nil {
		return scanner.classifyGitError(result, err)
	}
	statusRecords, err := scannerNULRecords(statusOutput)
	if err != nil {
		return scanner.failed(result, err)
	}
	result.Observation.Dirty = len(statusRecords) > 0

	stageOutput, err := scanner.gitOutput(ctx, root, "ls-files", "--stage", "-z")
	if err != nil {
		return scanner.classifyGitError(result, err)
	}
	staged, err := scannerStagedCandidates(stageOutput, objectFormat)
	if err != nil {
		return scanner.failed(result, err)
	}

	untrackedOutput, err := scanner.gitOutput(ctx, root, "ls-files", "--others", "--exclude-standard", "-z")
	if err != nil {
		return scanner.classifyGitError(result, err)
	}
	untracked, err := scannerUntrackedCandidates(untrackedOutput)
	if err != nil {
		return scanner.failed(result, err)
	}

	candidates, err := scannerMergeCandidates(staged, untracked, scanner.policy.IncludeUntracked)
	if err != nil {
		return scanner.failed(result, err)
	}

	result.Files = make([]ScannerFile, 0, len(candidates))
	partial := false
	for _, candidate := range candidates {
		if err := ctx.Err(); err != nil {
			return scanner.incomplete(result, err)
		}

		file, incomplete, err := scanner.scanCandidate(ctx, root, candidate)
		if err != nil {
			return scanner.failed(result, err)
		}
		if incomplete {
			partial = true
		}
		result.Files = append(result.Files, file)
		switch file.State {
		case IndexFileExcluded:
			result.Coverage.ExcludedFiles++
		case IndexFileUnreadable:
			result.Coverage.UnreadableFiles++
		}
	}

	if partial {
		scanner.finish(&result, IndexScanIncomplete)
	} else {
		scanner.finish(&result, IndexScanComplete)
	}
	return result, nil
}

func (scanner *Scanner) scanCandidate(ctx context.Context, root string, candidate scannerCandidate) (ScannerFile, bool, error) {
	if err := scannerValidateGitPath(candidate.path); err != nil {
		return ScannerFile{}, false, err
	}
	if scannerPathProtected(scanner.policy.ProtectedPaths, candidate.path) {
		return scannerExcludedFile(candidate.path, ScannerExclusionProtected), false, nil
	}
	if candidate.mode == "160000" {
		return scannerExcludedFile(candidate.path, ScannerExclusionSubmodule), false, nil
	}
	if candidate.mode == "120000" {
		return scannerExcludedFile(candidate.path, ScannerExclusionReparseEscape), false, nil
	}

	fullPath, err := scannerJoinRoot(root, candidate.path)
	if err != nil {
		return ScannerFile{}, false, err
	}

	location, err := scanner.checkCandidateLocation(root, candidate.path)
	if err != nil {
		return scannerUnreadableFile(candidate.path), true, nil
	}
	switch location {
	case scannerLocationReparse:
		return scannerExcludedFile(candidate.path, ScannerExclusionReparseEscape), false, nil
	case scannerLocationNestedRepository:
		return scannerExcludedFile(candidate.path, ScannerExclusionNestedRepository), false, nil
	case scannerLocationUnsupported:
		return scannerExcludedFile(candidate.path, ScannerExclusionUnsupportedType), false, nil
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

func (scanner *Scanner) readStableFile(ctx context.Context, fullPath, relativePath string) (ScannerFile, bool, error) {
	maxBytes := scannerMaxBytes(scanner.policy.MaxFileBytes)
	for attempt := range scannerReadAttempts {
		if err := ctx.Err(); err != nil {
			return ScannerFile{}, true, err
		}

		initial, err := scanner.files.Lstat(fullPath)
		if err != nil {
			if attempt+1 < scannerReadAttempts {
				continue
			}
			return scannerUnreadableFile(relativePath), true, nil
		}
		if scannerInfoIsReparse(initial) {
			return scannerExcludedFile(relativePath, ScannerExclusionReparseEscape), false, nil
		}
		if !initial.Mode.IsRegular() {
			return scannerExcludedFile(relativePath, ScannerExclusionUnsupportedType), false, nil
		}
		if initial.Size < 0 || (initial.Size > maxBytes && !scannerMayInspectForSecret(initial.Size, scanner.policy.SecretDetector)) {
			return scannerExcludedFile(relativePath, ScannerExclusionTooLarge), false, nil
		}

		body, err := scanner.files.ReadFile(fullPath)
		if err != nil {
			if attempt+1 < scannerReadAttempts {
				continue
			}
			return scannerUnreadableFile(relativePath), true, nil
		}
		if err := ctx.Err(); err != nil {
			return ScannerFile{}, true, err
		}

		current, err := scanner.files.Lstat(fullPath)
		if err != nil || !scannerSameFileInfo(initial, current) || int64(len(body)) != current.Size {
			if attempt+1 < scannerReadAttempts {
				continue
			}
			return ScannerFile{
				Path:      relativePath,
				State:     IndexFileUnreadable,
				Exclusion: ScannerExclusionChanging,
			}, true, nil
		}
		if current.Size < 0 || (current.Size > maxBytes && !scannerMayInspectForSecret(current.Size, scanner.policy.SecretDetector)) {
			return scannerExcludedFile(relativePath, ScannerExclusionTooLarge), false, nil
		}
		if scannerBinary(body) {
			return scannerExcludedFile(relativePath, ScannerExclusionBinary), false, nil
		}
		if detector := scanner.policy.SecretDetector; detector != nil && detector.ContainsSecret(relativePath, body) {
			return scannerExcludedFile(relativePath, ScannerExclusionSecret), false, nil
		}
		if current.Size > maxBytes {
			return scannerExcludedFile(relativePath, ScannerExclusionTooLarge), false, nil
		}

		return ScannerFile{
			Path:  relativePath,
			Body:  append([]byte(nil), body...),
			State: IndexFilePresent,
		}, false, nil
	}

	return scannerUnreadableFile(relativePath), true, nil
}

func (scanner *Scanner) gitHead(ctx context.Context, root, objectFormat string) (string, bool, error) {
	result, err := scanner.runGit(ctx, root, "rev-parse", "--verify", "HEAD")
	if err != nil {
		return "", false, err
	}
	if result.ExitCode == 1 || (result.ExitCode == 128 && len(result.Stdout) == 0) {
		return "", false, nil
	}
	if result.ExitCode != 0 {
		return "", false, fmt.Errorf("%w: HEAD resolution", ErrScannerGitFailure)
	}

	head, err := scannerGitLine(result.Stdout)
	if err != nil {
		return "", false, err
	}
	if !scannerValidOID(head, objectFormat) {
		return "", false, fmt.Errorf("%w: invalid HEAD", ErrScannerMalformed)
	}
	return head, true, nil
}

func (scanner *Scanner) gitRefLabel(ctx context.Context, root string) (string, bool, error) {
	result, err := scanner.runGit(ctx, root, "symbolic-ref", "--quiet", "--short", "HEAD")
	if err != nil {
		return "", false, err
	}
	if result.ExitCode == 1 {
		return "", false, nil
	}
	if result.ExitCode != 0 {
		return "", false, fmt.Errorf("%w: symbolic HEAD", ErrScannerGitFailure)
	}
	label, err := scannerGitLine(result.Stdout)
	if err != nil {
		return "", false, err
	}
	return label, true, nil
}

func (scanner *Scanner) gitLine(ctx context.Context, root string, command ...string) (string, error) {
	output, err := scanner.gitOutput(ctx, root, command...)
	if err != nil {
		return "", err
	}
	return scannerGitLine(output)
}

func (scanner *Scanner) gitOutput(ctx context.Context, root string, command ...string) ([]byte, error) {
	result, err := scanner.runGit(ctx, root, command...)
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

func (scanner *Scanner) classifyGitError(result ScannerResult, err error) (ScannerResult, error) {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return scanner.incomplete(result, err)
	}
	return scanner.failed(result, err)
}

func (scanner *Scanner) failed(result ScannerResult, err error) (ScannerResult, error) {
	scanner.finish(&result, IndexScanFailed)
	return result, err
}

func (scanner *Scanner) incomplete(result ScannerResult, err error) (ScannerResult, error) {
	scanner.finish(&result, IndexScanIncomplete)
	return result, err
}

func (scanner *Scanner) finish(result *ScannerResult, outcome IndexScanOutcome) {
	result.Observation.ScanEnd = time.Now().UTC()
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
		separator := strings.IndexByte(record, '\t')
		if separator < 0 {
			return nil, fmt.Errorf("%w: index record", ErrScannerMalformed)
		}
		header := strings.Fields(record[:separator])
		if len(header) != 3 || !scannerValidMode(header[0]) || !scannerValidOID(header[1], objectFormat) {
			return nil, fmt.Errorf("%w: index header", ErrScannerMalformed)
		}
		stage, err := strconv.Atoi(header[2])
		if err != nil || stage < 0 || stage > 3 {
			return nil, fmt.Errorf("%w: index stage", ErrScannerMalformed)
		}
		candidate := scannerCandidate{path: record[separator+1:], mode: header[0], tracked: true}
		if err := scannerValidateGitPath(candidate.path); err != nil {
			return nil, err
		}
		candidates = append(candidates, candidate)
	}
	return candidates, nil
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
	for _, candidate := range staged {
		existing, exists := byPath[candidate.path]
		if !exists || (existing.mode != "160000" && candidate.mode == "160000") {
			byPath[candidate.path] = candidate
		}
	}
	if includeUntracked {
		for _, candidate := range untracked {
			if _, exists := byPath[candidate.path]; !exists {
				byPath[candidate.path] = candidate
			}
		}
	}

	if runtime.GOOS == "windows" {
		caseFolded := make(map[string]string, len(byPath))
		for candidatePath := range byPath {
			key := strings.ToLower(candidatePath)
			if previous, exists := caseFolded[key]; exists && previous != candidatePath {
				return nil, fmt.Errorf("%w: case-ambiguous candidates", ErrScannerMalformed)
			}
			caseFolded[key] = candidatePath
		}
	}

	paths := make([]string, 0, len(byPath))
	for candidatePath := range byPath {
		paths = append(paths, candidatePath)
	}
	sort.Strings(paths)

	merged := make([]scannerCandidate, 0, len(paths))
	for _, candidatePath := range paths {
		merged = append(merged, byPath[candidatePath])
	}
	return merged, nil
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

func scannerGitLine(output []byte) (string, error) {
	if len(output) == 0 || bytes.IndexByte(output, 0) >= 0 {
		return "", fmt.Errorf("%w: line plumbing output", ErrScannerMalformed)
	}
	if output[len(output)-1] == '\n' {
		output = output[:len(output)-1]
		if len(output) > 0 && output[len(output)-1] == '\r' {
			output = output[:len(output)-1]
		}
	}
	if len(output) == 0 || !utf8.Valid(output) {
		return "", fmt.Errorf("%w: invalid line", ErrScannerMalformed)
	}
	return string(output), nil
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
