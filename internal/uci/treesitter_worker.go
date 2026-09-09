package uci

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
	"unsafe"
)

const (
	// TreeSitterWorkerProtocolVersion is the single framed child-process protocol.
	TreeSitterWorkerProtocolVersion           = "uci-tree-sitter/v2"
	TreeSitterFactsExtractionContractRevision = "uci-tree-sitter-facts/v2"
	TreeSitterBundleSchemaRevision            = "uci-tree-sitter-bundle/v2"
	treeSitterWorkerMaxIdentifierBytes        = 4 << 10
	treeSitterWorkerHardMaxInputBytes         = 4 << 20
	treeSitterWorkerHardMaxOutputBytes        = 16 << 20
	treeSitterWorkerMaxProfileBytes           = 256
	treeSitterWorkerMaxDefinitions            = 2_048
	treeSitterWorkerMaxReferences             = 8_192
	treeSitterWorkerMaxChunks                 = 64
	treeSitterWorkerMaxChunkBytes             = 64 << 10
	treeSitterWorkerMaxDiagnostics            = 16
	treeSitterWorkerMaxDiagnosticBytes        = 512
	treeSitterWorkerCacheEntries              = 16
	treeSitterWorkerCacheMaxBytes             = 1 << 20
	// treeSitterWorkerCacheWorkerReserveBytes is charged inside the total cache
	// cap for preallocated map groups, map/slice headers, cacheOrder spare
	// capacity, mutex, and worker/container storage not attributable to one
	// cloned artifact.
	treeSitterWorkerCacheWorkerReserveBytes = 64 << 10
)

// TreeSitterBundleDigest returns the exact parser bundle identity shared by
// the parent worker, installed harness, and parser executable for this build.
func TreeSitterBundleDigest() IndexDigest {
	parts := []string{
		TreeSitterBundleSchemaRevision,
		TreeSitterWorkerProtocolVersion,
		TreeSitterFactsExtractionContractRevision,
		"github.com/tree-sitter/go-tree-sitter@v0.25.0",
		"github.com/tree-sitter/tree-sitter-javascript@v0.25.0",
		"github.com/tree-sitter/tree-sitter-typescript@v0.23.2",
		"go=" + runtime.Version(),
		"target=" + runtime.GOOS + "/" + runtime.GOARCH,
	}
	if build, ok := debug.ReadBuildInfo(); ok && build.GoVersion != "" {
		parts = append(parts, "build-go="+build.GoVersion)
	}
	sort.Strings(parts)
	state := sha256.New()
	for _, part := range parts {
		var length [4]byte
		binary.BigEndian.PutUint32(length[:], uint32(len(part)))
		_, _ = state.Write(length[:])
		_, _ = state.Write([]byte(part))
	}
	return IndexDigest("sha256:" + hex.EncodeToString(state.Sum(nil)))
}

var (
	// ErrTreeSitterBundleMismatch reports a child whose compiled parser bundle
	// does not match the caller-selected profile bundle.
	ErrTreeSitterBundleMismatch = errors.New("tree-sitter worker bundle digest mismatch")
	// ErrTreeSitterInputLimit reports source bytes rejected before child launch.
	ErrTreeSitterInputLimit = errors.New("tree-sitter worker input limit exceeded")
	// ErrTreeSitterOutputLimit reports a child response that exceeds its frame cap.
	ErrTreeSitterOutputLimit = errors.New("tree-sitter worker output limit exceeded")
	// ErrTreeSitterProtocol reports an invalid child-process request or response.
	ErrTreeSitterProtocol = errors.New("tree-sitter worker protocol error")
)

// TreeSitterLanguage is the closed source-language vocabulary accepted by the
// bundled parser worker.
type TreeSitterLanguage string

const (
	TreeSitterLanguageJavaScript TreeSitterLanguage = "javascript"
	TreeSitterLanguageTypeScript TreeSitterLanguage = "typescript"
	TreeSitterLanguageTSX        TreeSitterLanguage = "tsx"
)

const (
	// TreeSitterResolutionSyntaxOnly records a source-observed site with no
	// module or type-resolution claim.
	TreeSitterResolutionSyntaxOnly IndexResolutionState = "syntax_only"
	// TreeSitterResolutionPartial records syntax that exposes some target shape
	// (such as an alias or re-export) without an exact static target.
	TreeSitterResolutionPartial IndexResolutionState = "partial"
	// TreeSitterResolutionUnresolved records syntax whose target cannot be
	// represented from grammar-only evidence.
	TreeSitterResolutionUnresolved IndexResolutionState = "unresolved"
)

// TreeSitterWorkerConfig describes one explicitly configured local child
// process. Environment is an allowlist: no inherited environment entry is
// merged into the child.
type TreeSitterWorkerConfig struct {
	ExecutablePath       string
	Arguments            []string
	ExpectedBundleDigest IndexDigest
	MaxInputBytes        int
	MaxOutputBytes       int
	Timeout              time.Duration
	Environment          []string
}

// TreeSitterParseRequest contains only caller-owned source bytes and an
// extraction profile selector. It never includes a repository path.
type TreeSitterParseRequest struct {
	Language   TreeSitterLanguage
	ProfileKey string
	Source     []byte
}

// TreeSitterArtifact is immutable grammar-only extraction evidence derived
// from one source buffer.
type TreeSitterArtifact struct {
	Proof        IndexArtifactProof
	Coverage     IndexCoverageState
	Language     TreeSitterLanguage
	BundleDigest IndexDigest
	Text         string
	Definitions  []TreeSitterDefinition
	References   []TreeSitterReferenceSite
	Chunks       []TreeSitterChunk
	Diagnostics  []TreeSitterDiagnostic
}

// TreeSitterDefinition records one syntactically declared source symbol.
type TreeSitterDefinition struct {
	Kind      string    `json:"kind"`
	Name      string    `json:"name"`
	SymbolKey string    `json:"symbol_key"`
	LocalKey  string    `json:"local_key"`
	Span      IndexSpan `json:"span"`
}

// TreeSitterReferenceSite records one observed source syntax site. TargetKey
// stays blank until a separate authorized resolver produces exact identity.
type TreeSitterReferenceSite struct {
	Kind          string               `json:"kind"`
	SymbolKey     string               `json:"symbol_key"`
	LocalKey      string               `json:"local_key"`
	OwnerLocalKey string               `json:"owner_local_key"`
	RawTarget     string               `json:"raw_target"`
	TargetKey     string               `json:"target_key"`
	Resolution    IndexResolutionState `json:"resolution"`
	Span          IndexSpan            `json:"span"`
}

// TreeSitterChunk is one bounded UTF-8 source segment.
type TreeSitterChunk struct {
	Span          IndexSpan   `json:"span"`
	Text          string      `json:"text"`
	ContentDigest IndexDigest `json:"content_digest"`
}

// TreeSitterDiagnostic records a bounded grammar or representation limitation.
type TreeSitterDiagnostic struct {
	Code    string    `json:"code"`
	Span    IndexSpan `json:"span"`
	Message string    `json:"message"`
}

// parser child. Source uses the standard JSON base64 representation for []byte.
type TreeSitterWorkerWireRequest struct {
	Version    string             `json:"version"`
	Language   TreeSitterLanguage `json:"language"`
	ProfileKey string             `json:"profile_key"`
	Source     []byte             `json:"source"`
}

// TreeSitterWorkerWireResponse is the JSON-line result returned by the parser
// child. The parent validates and finalizes its immutable proof.
type TreeSitterWorkerWireResponse struct {
	Version      string                    `json:"version"`
	BundleDigest IndexDigest               `json:"bundle_digest"`
	Coverage     IndexCoverageState        `json:"coverage"`
	Text         string                    `json:"text"`
	Definitions  []TreeSitterDefinition    `json:"definitions"`
	References   []TreeSitterReferenceSite `json:"references"`
	Chunks       []TreeSitterChunk         `json:"chunks"`
	Diagnostics  []TreeSitterDiagnostic    `json:"diagnostics"`
}

type treeSitterWorkerCacheEntry struct {
	artifact TreeSitterArtifact
	bytes    int
}

// TreeSitterWorker executes the prebuilt local parser child with a bounded
// request, response, deadline, environment, and private working directory.
type TreeSitterWorker struct {
	config TreeSitterWorkerConfig

	cacheMu    sync.Mutex
	cache      map[[sha256.Size]byte]treeSitterWorkerCacheEntry
	cacheOrder [][sha256.Size]byte
	cacheBytes int
}

// NewTreeSitterWorker validates and freezes a local parser-worker launch
// policy. The executable must be absolute so PATH lookup cannot select source
// repository tooling.
func NewTreeSitterWorker(config TreeSitterWorkerConfig) (*TreeSitterWorker, error) {
	if config.ExecutablePath == "" || !filepath.IsAbs(config.ExecutablePath) {
		return nil, fmt.Errorf("%w: executable path must be absolute", ErrTreeSitterProtocol)
	}
	if !treeSitterDigestValid(config.ExpectedBundleDigest) {
		return nil, fmt.Errorf("%w: expected bundle digest must be sha256", ErrTreeSitterProtocol)
	}
	if config.MaxInputBytes <= 0 || config.MaxInputBytes > treeSitterWorkerHardMaxInputBytes {
		return nil, fmt.Errorf("%w: max input bytes must be within the hard cap", ErrTreeSitterProtocol)
	}
	if config.MaxOutputBytes <= 0 || config.MaxOutputBytes > treeSitterWorkerHardMaxOutputBytes {
		return nil, fmt.Errorf("%w: max output bytes must be within the hard cap", ErrTreeSitterProtocol)
	}
	if config.Timeout <= 0 {
		return nil, fmt.Errorf("%w: timeout must be positive", ErrTreeSitterProtocol)
	}

	environment, err := treeSitterWorkerEnvironment(config.Environment)
	if err != nil {
		return nil, err
	}
	config.Arguments = append([]string(nil), config.Arguments...)
	config.Environment = environment
	return &TreeSitterWorker{
		config:     config,
		cache:      make(map[[sha256.Size]byte]treeSitterWorkerCacheEntry, treeSitterWorkerCacheEntries),
		cacheBytes: treeSitterWorkerCacheWorkerReserveBytes,
	}, nil
}

// Parse validates one configured request and returns immutable grammar evidence
// from its bounded cache or, on a miss, from the configured parser child. It
// preserves caller cancellation and the worker deadline as context errors.
func (worker *TreeSitterWorker) Parse(ctx context.Context, request TreeSitterParseRequest) (TreeSitterArtifact, error) {
	if worker == nil {
		return TreeSitterArtifact{}, fmt.Errorf("%w: nil worker", ErrTreeSitterProtocol)
	}
	if ctx == nil {
		return TreeSitterArtifact{}, fmt.Errorf("%w: nil context", ErrTreeSitterProtocol)
	}
	if err := ctx.Err(); err != nil {
		return TreeSitterArtifact{}, err
	}
	requestLine, err := treeSitterPrepareWireRequest(request, worker.config.MaxInputBytes)
	if err != nil {
		return TreeSitterArtifact{}, err
	}
	cacheKey := sha256.Sum256(requestLine)
	if artifact, found := worker.cachedArtifact(cacheKey); found {
		return artifact, nil
	}

	privateDirectory, err := os.MkdirTemp("", "engram-uci-parser-")
	if err != nil {
		return TreeSitterArtifact{}, fmt.Errorf("%w: create private child directory: %v", ErrTreeSitterProtocol, err)
	}
	defer func() { _ = os.RemoveAll(privateDirectory) }()

	runContext, cancel := context.WithTimeout(ctx, worker.config.Timeout)
	defer cancel()
	command := exec.CommandContext(runContext, worker.config.ExecutablePath, worker.config.Arguments...)
	command.Dir = privateDirectory
	command.Env = append([]string(nil), worker.config.Environment...)
	command.Stdin = bytes.NewReader(requestLine)
	command.Stderr = io.Discard
	stdout, err := command.StdoutPipe()
	if err != nil {
		return TreeSitterArtifact{}, fmt.Errorf("%w: open child stdout: %v", ErrTreeSitterProtocol, err)
	}
	if err := command.Start(); err != nil {
		if contextErr := runContext.Err(); contextErr != nil {
			return TreeSitterArtifact{}, contextErr
		}
		return TreeSitterArtifact{}, fmt.Errorf("%w: start child: %v", ErrTreeSitterProtocol, err)
	}

	type outputResult struct {
		line []byte
		err  error
	}
	output := make(chan outputResult, 1)
	go func() {
		line, readErr := treeSitterReadWireLine(stdout, worker.config.MaxOutputBytes)
		output <- outputResult{line: line, err: readErr}
	}()

	var responseLine []byte
	select {
	case result := <-output:
		if result.err != nil {
			treeSitterStopChild(command)
			if contextErr := runContext.Err(); contextErr != nil {
				return TreeSitterArtifact{}, contextErr
			}
			return TreeSitterArtifact{}, result.err
		}
		responseLine = result.line
		if err := command.Wait(); err != nil {
			if contextErr := runContext.Err(); contextErr != nil {
				return TreeSitterArtifact{}, contextErr
			}
			return TreeSitterArtifact{}, fmt.Errorf("%w: child exited without a valid result", ErrTreeSitterProtocol)
		}
	case <-runContext.Done():
		treeSitterStopChild(command)
		return TreeSitterArtifact{}, runContext.Err()
	}
	if err := runContext.Err(); err != nil {
		return TreeSitterArtifact{}, err
	}

	response, err := treeSitterDecodeWireResponse(responseLine)
	if err != nil {
		return TreeSitterArtifact{}, err
	}
	if response.Version != TreeSitterWorkerProtocolVersion {
		return TreeSitterArtifact{}, fmt.Errorf("%w: response version did not match request", ErrTreeSitterProtocol)
	}
	if response.BundleDigest != worker.config.ExpectedBundleDigest {
		return TreeSitterArtifact{}, fmt.Errorf("%w: got %q, expected %q", ErrTreeSitterBundleMismatch, response.BundleDigest, worker.config.ExpectedBundleDigest)
	}

	artifact := TreeSitterArtifact{
		Coverage:     response.Coverage,
		Language:     request.Language,
		BundleDigest: response.BundleDigest,
		Text:         response.Text,
		Definitions:  append([]TreeSitterDefinition(nil), response.Definitions...),
		References:   append([]TreeSitterReferenceSite(nil), response.References...),
		Chunks:       append([]TreeSitterChunk(nil), response.Chunks...),
		Diagnostics:  append([]TreeSitterDiagnostic(nil), response.Diagnostics...),
	}
	if err := treeSitterValidateArtifact(request.Source, artifact); err != nil {
		return TreeSitterArtifact{}, err
	}
	artifact = treeSitterFinalizeArtifact(request.Source, request.ProfileKey, artifact)
	worker.cacheArtifact(cacheKey, artifact)
	return treeSitterCloneArtifact(artifact), nil
}

func (worker *TreeSitterWorker) cachedArtifact(key [sha256.Size]byte) (TreeSitterArtifact, bool) {
	worker.cacheMu.Lock()
	defer worker.cacheMu.Unlock()
	entry, found := worker.cache[key]
	if !found {
		return TreeSitterArtifact{}, false
	}
	return treeSitterCloneArtifact(entry.artifact), true
}

func (worker *TreeSitterWorker) cacheArtifact(key [sha256.Size]byte, artifact TreeSitterArtifact) {
	bytes := treeSitterArtifactCacheBytes(artifact)
	if bytes > treeSitterWorkerCacheMaxBytes-treeSitterWorkerCacheWorkerReserveBytes {
		return
	}
	entry := treeSitterWorkerCacheEntry{artifact: treeSitterCloneArtifact(artifact), bytes: bytes}
	worker.cacheMu.Lock()
	defer worker.cacheMu.Unlock()
	if worker.cache == nil {
		worker.cache = make(map[[sha256.Size]byte]treeSitterWorkerCacheEntry, treeSitterWorkerCacheEntries)
	}
	if worker.cacheBytes == 0 {
		worker.cacheBytes = treeSitterWorkerCacheWorkerReserveBytes
	}
	if _, found := worker.cache[key]; found {
		return
	}
	for len(worker.cacheOrder) >= treeSitterWorkerCacheEntries || worker.cacheBytes+bytes > treeSitterWorkerCacheMaxBytes {
		if len(worker.cacheOrder) == 0 {
			return
		}
		oldest := worker.cacheOrder[0]
		worker.cacheOrder = worker.cacheOrder[1:]
		oldEntry := worker.cache[oldest]
		delete(worker.cache, oldest)
		worker.cacheBytes -= oldEntry.bytes
	}
	worker.cache[key] = entry
	worker.cacheOrder = append(worker.cacheOrder, key)
	worker.cacheBytes += bytes
}

func treeSitterCloneArtifact(artifact TreeSitterArtifact) TreeSitterArtifact {
	clone := artifact
	clone.Definitions = append([]TreeSitterDefinition(nil), artifact.Definitions...)
	clone.References = append([]TreeSitterReferenceSite(nil), artifact.References...)
	clone.Chunks = append([]TreeSitterChunk(nil), artifact.Chunks...)
	clone.Diagnostics = append([]TreeSitterDiagnostic(nil), artifact.Diagnostics...)
	return clone
}

func treeSitterArtifactCacheBytes(artifact TreeSitterArtifact) int {
	// Per-entry payload counts only the cloned artifact and its retained strings.
	// Worker/map/order containers are charged once by the fixed worker reserve.
	bytes := int(unsafe.Sizeof(TreeSitterArtifact{}))
	bytes += cap(artifact.Definitions) * int(unsafe.Sizeof(TreeSitterDefinition{}))
	bytes += cap(artifact.References) * int(unsafe.Sizeof(TreeSitterReferenceSite{}))
	bytes += cap(artifact.Chunks) * int(unsafe.Sizeof(TreeSitterChunk{}))
	bytes += cap(artifact.Diagnostics) * int(unsafe.Sizeof(TreeSitterDiagnostic{}))
	bytes += len(artifact.Proof.ArtifactID) + len(artifact.Proof.ContentDigest) + len(artifact.Proof.FactsDigest)
	bytes += len(artifact.Coverage) + len(artifact.Language) + len(artifact.BundleDigest) + len(artifact.Text)
	for _, definition := range artifact.Definitions {
		bytes += len(definition.Kind) + len(definition.Name) + len(definition.SymbolKey) + len(definition.LocalKey)
	}
	for _, reference := range artifact.References {
		bytes += len(reference.Kind) + len(reference.SymbolKey) + len(reference.LocalKey) + len(reference.OwnerLocalKey) + len(reference.RawTarget) + len(reference.TargetKey) + len(reference.Resolution)
	}
	for _, chunk := range artifact.Chunks {
		bytes += len(chunk.Text) + len(chunk.ContentDigest)
	}
	for _, diagnostic := range artifact.Diagnostics {
		bytes += len(diagnostic.Code) + len(diagnostic.Message)
	}
	return bytes
}

// TreeSitterWireRequestDigest returns the SHA-256 digest of the exact canonical
// JSON line that Parse writes to the parser child for request.
func TreeSitterWireRequestDigest(request TreeSitterParseRequest) (IndexDigest, error) {
	requestLine, err := treeSitterPrepareWireRequest(request, treeSitterWorkerHardMaxInputBytes)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(requestLine)
	return IndexDigest("sha256:" + hex.EncodeToString(sum[:])), nil
}

func treeSitterPrepareWireRequest(request TreeSitterParseRequest, maximumInputBytes int) ([]byte, error) {
	if maximumInputBytes <= 0 || maximumInputBytes > treeSitterWorkerHardMaxInputBytes {
		return nil, fmt.Errorf("%w: max input bytes must be within the hard cap", ErrTreeSitterProtocol)
	}
	if len(request.Source) > maximumInputBytes {
		return nil, fmt.Errorf("%w: %d source bytes exceeds %d", ErrTreeSitterInputLimit, len(request.Source), maximumInputBytes)
	}
	if !treeSitterLanguageValid(request.Language) {
		return nil, fmt.Errorf("%w: invalid language", ErrTreeSitterProtocol)
	}
	if !treeSitterProfileKeyValid(request.ProfileKey) {
		return nil, fmt.Errorf("%w: invalid profile key", ErrTreeSitterProtocol)
	}

	requestLine, err := treeSitterEncodeWireRequest(TreeSitterWorkerWireRequest{
		Version:    TreeSitterWorkerProtocolVersion,
		Language:   request.Language,
		ProfileKey: request.ProfileKey,
		Source:     request.Source,
	})
	if err != nil {
		return nil, fmt.Errorf("%w: encode request: %v", ErrTreeSitterProtocol, err)
	}
	if len(requestLine) > treeSitterWorkerHardMaxOutputBytes {
		return nil, fmt.Errorf("%w: framed request exceeded hard cap", ErrTreeSitterInputLimit)
	}
	return requestLine, nil
}

func treeSitterEncodeWireRequest(request TreeSitterWorkerWireRequest) ([]byte, error) {
	var encoded bytes.Buffer
	encoder := json.NewEncoder(&encoded)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(request); err != nil {
		return nil, err
	}
	return encoded.Bytes(), nil
}

func treeSitterDecodeWireResponse(line []byte) (TreeSitterWorkerWireResponse, error) {
	var response TreeSitterWorkerWireResponse
	decoder := json.NewDecoder(bytes.NewReader(line))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&response); err != nil {
		return TreeSitterWorkerWireResponse{}, fmt.Errorf("%w: decode response: %v", ErrTreeSitterProtocol, err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return TreeSitterWorkerWireResponse{}, fmt.Errorf("%w: response contains trailing JSON", ErrTreeSitterProtocol)
	}
	return response, nil
}

func treeSitterReadWireLine(reader io.Reader, maximum int) ([]byte, error) {
	if maximum <= 0 {
		return nil, ErrTreeSitterOutputLimit
	}
	bufferSize := 4 << 10
	if maximum+1 < bufferSize {
		bufferSize = maximum + 1
	}
	buffered := bufio.NewReaderSize(reader, bufferSize)
	line := make([]byte, 0, min(maximum+1, bufferSize))
	for {
		fragment, err := buffered.ReadSlice('\n')
		if len(fragment) > 0 {
			if len(line) > maximum+1-len(fragment) {
				return nil, fmt.Errorf("%w: response frame is too large", ErrTreeSitterOutputLimit)
			}
			line = append(line, fragment...)
		}
		switch {
		case err == nil:
			line = line[:len(line)-1]
			if len(line) > maximum {
				return nil, fmt.Errorf("%w: response frame is too large", ErrTreeSitterOutputLimit)
			}
			if len(line) > 0 && line[len(line)-1] == '\r' {
				line = line[:len(line)-1]
			}
			extra, readErr := buffered.ReadByte()
			if readErr == nil {
				_ = extra
				return nil, fmt.Errorf("%w: multiple response frames", ErrTreeSitterProtocol)
			}
			if !errors.Is(readErr, io.EOF) {
				return nil, fmt.Errorf("%w: read trailing response bytes: %v", ErrTreeSitterProtocol, readErr)
			}
			return line, nil
		case errors.Is(err, bufio.ErrBufferFull):
			continue
		case errors.Is(err, io.EOF):
			return nil, fmt.Errorf("%w: response is missing a terminating newline", ErrTreeSitterProtocol)
		default:
			return nil, fmt.Errorf("%w: read response: %v", ErrTreeSitterProtocol, err)
		}
	}
}

func treeSitterStopChild(command *exec.Cmd) {
	if command == nil {
		return
	}
	if command.Process != nil {
		_ = command.Process.Kill()
	}
	_ = command.Wait()
}

func treeSitterWorkerEnvironment(entries []string) ([]string, error) {
	result := make([]string, 0, len(entries))
	seen := make(map[string]struct{}, len(entries))
	for _, entry := range entries {
		name, value, found := strings.Cut(entry, "=")
		if !found || !treeSitterEnvironmentNameValid(name) || strings.IndexByte(value, 0) >= 0 {
			return nil, fmt.Errorf("%w: invalid allowlisted environment entry", ErrTreeSitterProtocol)
		}
		canonicalName := strings.ToUpper(name)
		if _, exists := seen[canonicalName]; exists {
			return nil, fmt.Errorf("%w: duplicate allowlisted environment entry %q", ErrTreeSitterProtocol, name)
		}
		seen[canonicalName] = struct{}{}
		result = append(result, entry)
	}
	sort.Slice(result, func(left, right int) bool {
		leftName, _, _ := strings.Cut(result[left], "=")
		rightName, _, _ := strings.Cut(result[right], "=")
		return strings.ToUpper(leftName) < strings.ToUpper(rightName)
	})
	return result, nil
}

func treeSitterEnvironmentNameValid(name string) bool {
	if name == "" || len(name) > 128 {
		return false
	}
	for index := range len(name) {
		character := name[index]
		if (character >= 'a' && character <= 'z') || (character >= 'A' && character <= 'Z') || character == '_' {
			continue
		}
		if index > 0 && character >= '0' && character <= '9' {
			continue
		}
		return false
	}
	return true
}

func treeSitterLanguageValid(value TreeSitterLanguage) bool {
	text := string(value)
	if text == "" || len(text) > treeSitterWorkerMaxIdentifierBytes || !utf8.ValidString(text) || strings.TrimSpace(text) != text {
		return false
	}
	for _, character := range text {
		if character < 0x20 || character == 0x7f {
			return false
		}
	}
	return true
}

func treeSitterProfileKeyValid(value string) bool {
	if value == "" || len(value) > treeSitterWorkerMaxProfileBytes || !utf8.ValidString(value) || strings.TrimSpace(value) != value {
		return false
	}
	for _, character := range value {
		if character < 0x20 || character == 0x7f {
			return false
		}
	}
	return true
}

func treeSitterDigestValid(value IndexDigest) bool {
	const prefix = "sha256:"
	if !strings.HasPrefix(string(value), prefix) || len(value) != len(prefix)+sha256.Size*2 {
		return false
	}
	for _, character := range value[len(prefix):] {
		if (character < '0' || character > '9') && (character < 'a' || character > 'f') {
			return false
		}
	}
	return true
}

func treeSitterValidateArtifact(source []byte, artifact TreeSitterArtifact) error {
	if !treeSitterDigestValid(artifact.BundleDigest) {
		return fmt.Errorf("%w: child returned an invalid bundle digest", ErrTreeSitterProtocol)
	}
	if !treeSitterCoverageValid(artifact.Coverage) {
		return fmt.Errorf("%w: child returned unsupported coverage", ErrTreeSitterProtocol)
	}
	if len(artifact.Definitions) > treeSitterWorkerMaxDefinitions || len(artifact.References) > treeSitterWorkerMaxReferences || len(artifact.Chunks) > treeSitterWorkerMaxChunks || len(artifact.Diagnostics) > treeSitterWorkerMaxDiagnostics {
		return fmt.Errorf("%w: child exceeded fact count bounds", ErrTreeSitterProtocol)
	}
	if !utf8.Valid(source) {
		if artifact.Text != "" || len(artifact.Chunks) != 0 {
			return fmt.Errorf("%w: invalid UTF-8 source must not be emitted as text", ErrTreeSitterProtocol)
		}
	} else {
		expectedText, _ := goSafeText(source, treeSitterWorkerHardMaxInputBytes)
		if artifact.Text != expectedText {
			return fmt.Errorf("%w: child text did not match source bytes", ErrTreeSitterProtocol)
		}
		if err := treeSitterValidateChunks(source, artifact.Chunks); err != nil {
			return err
		}
	}

	lineStarts := goLineStarts(source)
	definitions := make(map[string]struct{}, len(artifact.Definitions))
	for _, definition := range artifact.Definitions {
		if definition.Kind == "" || definition.Name == "" || definition.SymbolKey == "" || definition.LocalKey == "" || !treeSitterBoundedText(definition.Kind, treeSitterWorkerMaxIdentifierBytes) || !treeSitterBoundedText(definition.Name, treeSitterWorkerMaxIdentifierBytes) || !treeSitterBoundedText(definition.SymbolKey, treeSitterWorkerMaxIdentifierBytes) || !treeSitterBoundedText(definition.LocalKey, treeSitterWorkerMaxIdentifierBytes) || !treeSitterSpanValid(source, lineStarts, definition.Span, false) || !treeSitterDefinitionNameSourceValid(source, definition) {
			return fmt.Errorf("%w: invalid definition", ErrTreeSitterProtocol)
		}
		if _, exists := definitions[definition.SymbolKey]; exists {
			return fmt.Errorf("%w: duplicate definition symbol key", ErrTreeSitterProtocol)
		}
		definitions[definition.SymbolKey] = struct{}{}
	}

	references := make(map[string]struct{}, len(artifact.References))
	referenceSites := make(map[string]struct{}, len(artifact.References))
	for _, reference := range artifact.References {
		if reference.Kind == "" || reference.SymbolKey == "" || reference.LocalKey == "" || reference.RawTarget == "" || reference.TargetKey != "" || !treeSitterReferenceKindValid(reference.Kind) || !treeSitterBoundedText(reference.SymbolKey, treeSitterWorkerMaxIdentifierBytes) || !treeSitterBoundedText(reference.LocalKey, treeSitterWorkerMaxIdentifierBytes) || !treeSitterBoundedText(reference.OwnerLocalKey, treeSitterWorkerMaxIdentifierBytes) || !treeSitterBoundedText(reference.RawTarget, treeSitterWorkerMaxIdentifierBytes) || !treeSitterReferenceResolutionValid(reference.Resolution) || !treeSitterSpanValid(source, lineStarts, reference.Span, false) || !treeSitterReferenceSiteIdentityValid(reference) {
			return fmt.Errorf("%w: invalid reference", ErrTreeSitterProtocol)
		}
		if _, exists := references[reference.SymbolKey]; exists {
			return fmt.Errorf("%w: duplicate reference symbol key", ErrTreeSitterProtocol)
		}
		if _, exists := referenceSites[reference.LocalKey]; exists {
			return fmt.Errorf("%w: duplicate reference site key", ErrTreeSitterProtocol)
		}
		references[reference.SymbolKey] = struct{}{}
		referenceSites[reference.LocalKey] = struct{}{}
	}

	for _, diagnostic := range artifact.Diagnostics {
		if diagnostic.Code == "" || !treeSitterBoundedText(diagnostic.Code, treeSitterWorkerMaxIdentifierBytes) || !treeSitterBoundedText(diagnostic.Message, treeSitterWorkerMaxDiagnosticBytes) || !treeSitterSpanValid(source, lineStarts, diagnostic.Span, true) {
			return fmt.Errorf("%w: invalid diagnostic", ErrTreeSitterProtocol)
		}
	}
	return nil
}

func treeSitterValidateChunks(source []byte, chunks []TreeSitterChunk) error {
	if len(source) == 0 {
		if len(chunks) != 0 {
			return fmt.Errorf("%w: empty source has chunks", ErrTreeSitterProtocol)
		}
		return nil
	}
	if len(chunks) == 0 {
		return fmt.Errorf("%w: nonempty source has no chunks", ErrTreeSitterProtocol)
	}
	lineStarts := goLineStarts(source)
	nextStart := 0
	for _, chunk := range chunks {
		if !treeSitterSpanValid(source, lineStarts, chunk.Span, false) || chunk.Span.ByteStart != int64(nextStart) || chunk.Span.ByteEnd-chunk.Span.ByteStart > treeSitterWorkerMaxChunkBytes {
			return fmt.Errorf("%w: invalid chunk span", ErrTreeSitterProtocol)
		}
		start, end := int(chunk.Span.ByteStart), int(chunk.Span.ByteEnd)
		if !utf8.Valid(source[start:end]) || chunk.Text != string(source[start:end]) || chunk.ContentDigest != goSourceDigest(source[start:end]) {
			return fmt.Errorf("%w: chunk did not exactly represent source bytes", ErrTreeSitterProtocol)
		}
		nextStart = end
	}
	if nextStart != len(source) {
		return fmt.Errorf("%w: chunks did not cover all source bytes", ErrTreeSitterProtocol)
	}
	return nil
}

func treeSitterBoundedText(value string, maximum int) bool {
	return utf8.ValidString(value) && len(value) <= maximum
}

func treeSitterSpanValid(source []byte, lineStarts []int, span IndexSpan, allowEmpty bool) bool {
	if allowEmpty && span == (IndexSpan{}) {
		return true
	}
	if span.ByteStart < 0 || span.ByteEnd < span.ByteStart || span.ByteEnd > int64(len(source)) || (!allowEmpty && span.ByteEnd == span.ByteStart) {
		return false
	}
	expected, valid := goSpanFromOffsets(lineStarts, len(source), int(span.ByteStart), int(span.ByteEnd))
	return valid && expected == span
}

func treeSitterCoverageValid(coverage IndexCoverageState) bool {
	switch coverage {
	case IndexCoverageComplete, IndexCoveragePartial, IndexCoverageUnavailable:
		return true
	default:
		return false
	}
}

func treeSitterReferenceResolutionValid(resolution IndexResolutionState) bool {
	switch resolution {
	case TreeSitterResolutionSyntaxOnly, TreeSitterResolutionPartial, TreeSitterResolutionUnresolved:
		return true
	default:
		return false
	}
}

// TreeSitterReferenceSiteKey derives the immutable occurrence identity for one
// parser-owned syntax site. Semantic spelling stays in the prefix; byte span
// distinguishes every concrete source occurrence.
func TreeSitterReferenceSiteKey(semanticKey string, span IndexSpan) string {
	return semanticKey + treeSitterReferenceSiteSuffix(span)
}

func treeSitterDefinitionNameSourceValid(source []byte, definition TreeSitterDefinition) bool {
	start, end := int(definition.Span.ByteStart), int(definition.Span.ByteEnd)
	if start < 0 || end < start || end > len(source) || len(definition.Name) > end-start {
		return false
	}
	for offset := start; offset+len(definition.Name) <= end; offset++ {
		matched := true
		for index := range len(definition.Name) {
			if source[offset+index] != definition.Name[index] {
				matched = false
				break
			}
		}
		if matched {
			return true
		}
	}
	return false
}

func treeSitterReferenceKindValid(kind string) bool {
	switch kind {
	case "import", "import_alias", "reexport", "reexport_alias", "export_alias", "call", "reference", "jsx_reference":
		return true
	default:
		return false
	}
}

func treeSitterReferenceSiteIdentityValid(reference TreeSitterReferenceSite) bool {
	suffix := treeSitterReferenceSiteSuffix(reference.Span)
	return len(reference.LocalKey) > len(suffix) && len(reference.SymbolKey) > len(suffix) && strings.HasSuffix(reference.LocalKey, suffix) && strings.HasSuffix(reference.SymbolKey, suffix)
}

func treeSitterReferenceSiteSuffix(span IndexSpan) string {
	return "@" + strconv.FormatInt(span.ByteStart, 10) + ":" + strconv.FormatInt(span.ByteEnd, 10)
}

func treeSitterFinalizeArtifact(source []byte, profileKey string, artifact TreeSitterArtifact) TreeSitterArtifact {
	artifact.Definitions = append([]TreeSitterDefinition(nil), artifact.Definitions...)
	artifact.References = append([]TreeSitterReferenceSite(nil), artifact.References...)
	artifact.Chunks = append([]TreeSitterChunk(nil), artifact.Chunks...)
	artifact.Diagnostics = append([]TreeSitterDiagnostic(nil), artifact.Diagnostics...)
	sort.Slice(artifact.Definitions, func(left, right int) bool {
		return treeSitterDefinitionLess(artifact.Definitions[left], artifact.Definitions[right])
	})
	sort.Slice(artifact.References, func(left, right int) bool {
		return treeSitterReferenceLess(artifact.References[left], artifact.References[right])
	})
	sort.Slice(artifact.Chunks, func(left, right int) bool {
		return treeSitterChunkLess(artifact.Chunks[left], artifact.Chunks[right])
	})
	sort.Slice(artifact.Diagnostics, func(left, right int) bool {
		return treeSitterDiagnosticLess(artifact.Diagnostics[left], artifact.Diagnostics[right])
	})

	contentDigest := goSourceDigest(source)
	artifact.Proof = IndexArtifactProof{
		ArtifactID:         treeSitterArtifactID(contentDigest, profileKey, artifact.Language, artifact.BundleDigest),
		ContentDigest:      contentDigest,
		FactsDigest:        treeSitterFactsDigest(profileKey, artifact),
		DefinitionCount:    uint64(len(artifact.Definitions)),
		ReferenceSiteCount: uint64(len(artifact.References)),
		ChunkCount:         uint64(len(artifact.Chunks)),
	}
	return artifact
}

func treeSitterArtifactID(contentDigest IndexDigest, profileKey string, language TreeSitterLanguage, bundleDigest IndexDigest) string {
	state := sha256.New()
	goWriteHashString(state, "uci-tree-sitter-artifact-id/v1")
	goWriteHashString(state, string(contentDigest))
	goWriteHashString(state, profileKey)
	goWriteHashString(state, string(language))
	goWriteHashString(state, string(bundleDigest))
	sum := state.Sum(nil)
	var identifier [16]byte
	copy(identifier[:], sum[:len(identifier)])
	identifier[6] = identifier[6]&0x0f | 0x50
	identifier[8] = identifier[8]&0x3f | 0x80
	encoded := hex.EncodeToString(identifier[:])
	return encoded[0:8] + "-" + encoded[8:12] + "-" + encoded[12:16] + "-" + encoded[16:20] + "-" + encoded[20:]
}

func treeSitterFactsDigest(profileKey string, artifact TreeSitterArtifact) IndexDigest {
	state := sha256.New()
	goWriteHashString(state, TreeSitterFactsExtractionContractRevision)
	goWriteHashString(state, TreeSitterWorkerProtocolVersion)
	goWriteHashString(state, profileKey)
	goWriteHashString(state, string(artifact.Language))
	goWriteHashString(state, string(artifact.BundleDigest))
	goWriteHashString(state, string(artifact.Coverage))
	goWriteHashString(state, artifact.Text)

	goWriteHashUint64(state, uint64(len(artifact.Definitions)))
	for _, definition := range artifact.Definitions {
		goWriteHashString(state, definition.Kind)
		goWriteHashString(state, definition.Name)
		goWriteHashString(state, definition.SymbolKey)
		goWriteHashString(state, definition.LocalKey)
		goWriteHashSpan(state, definition.Span)
	}
	goWriteHashUint64(state, uint64(len(artifact.References)))
	for _, reference := range artifact.References {
		goWriteHashString(state, reference.Kind)
		goWriteHashString(state, reference.SymbolKey)
		goWriteHashString(state, reference.LocalKey)
		goWriteHashString(state, reference.OwnerLocalKey)
		goWriteHashString(state, reference.RawTarget)
		goWriteHashString(state, reference.TargetKey)
		goWriteHashString(state, string(reference.Resolution))
		goWriteHashSpan(state, reference.Span)
	}
	goWriteHashUint64(state, uint64(len(artifact.Chunks)))
	for _, chunk := range artifact.Chunks {
		goWriteHashSpan(state, chunk.Span)
		goWriteHashString(state, chunk.Text)
		goWriteHashString(state, string(chunk.ContentDigest))
	}
	goWriteHashUint64(state, uint64(len(artifact.Diagnostics)))
	for _, diagnostic := range artifact.Diagnostics {
		goWriteHashString(state, diagnostic.Code)
		goWriteHashSpan(state, diagnostic.Span)
		goWriteHashString(state, diagnostic.Message)
	}
	var sum [sha256.Size]byte
	copy(sum[:], state.Sum(nil))
	return goDigestFromSum(sum)
}

func treeSitterDefinitionLess(left, right TreeSitterDefinition) bool {
	if comparison := goCompareSpans(left.Span, right.Span); comparison != 0 {
		return comparison < 0
	}
	if left.Kind != right.Kind {
		return left.Kind < right.Kind
	}
	if left.Name != right.Name {
		return left.Name < right.Name
	}
	if left.SymbolKey != right.SymbolKey {
		return left.SymbolKey < right.SymbolKey
	}
	return left.LocalKey < right.LocalKey
}

func treeSitterReferenceLess(left, right TreeSitterReferenceSite) bool {
	if comparison := goCompareSpans(left.Span, right.Span); comparison != 0 {
		return comparison < 0
	}
	if left.Kind != right.Kind {
		return left.Kind < right.Kind
	}
	if left.SymbolKey != right.SymbolKey {
		return left.SymbolKey < right.SymbolKey
	}
	if left.LocalKey != right.LocalKey {
		return left.LocalKey < right.LocalKey
	}
	return left.OwnerLocalKey < right.OwnerLocalKey
}

func treeSitterChunkLess(left, right TreeSitterChunk) bool {
	if comparison := goCompareSpans(left.Span, right.Span); comparison != 0 {
		return comparison < 0
	}
	if left.ContentDigest != right.ContentDigest {
		return left.ContentDigest < right.ContentDigest
	}
	return left.Text < right.Text
}

func treeSitterDiagnosticLess(left, right TreeSitterDiagnostic) bool {
	if comparison := goCompareSpans(left.Span, right.Span); comparison != 0 {
		return comparison < 0
	}
	if left.Code != right.Code {
		return left.Code < right.Code
	}
	return left.Message < right.Message
}
