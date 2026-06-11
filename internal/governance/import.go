// Package governance — import.go: bundle import with auto-format detection (T047 + T048).
//
// Auto-detect priority order (per plan §Format auto-detect priority order):
//   1. Explicit Format param (ImportOptions.Format)
//   2. File extension (.zip → FormatZIP, .jsonl → FormatJSONL)
//      NOTE: file extension is passed as ImportOptions.Filename; callers set it.
//   3. Content signature (PK\x03\x04 → FormatZIP; '{' → FormatJSONL)
//   4. Error (unknown format)
//
// Write-lint integration note (TG5 present on main since f0ae3f3):
//   Import conflict tokens (res_<UUID>) are governance-level conflict handles
//   for audit tracking, distinct from write-lint Phase1/Phase2 tokens (MCP
//   store_memory resolution via TokenStore). When ImportStore.WriteMemory
//   returns ErrImportConflict, the import emits a conflict report entry and
//   issues a governance token — no silent overwrite EVER (EC-F8). Live-DB
//   ImportStore implementations may call the write-lint orchestrator's Phase1
//   for deeper duplicate detection; the import package is orchestrator-agnostic.
//
// When ImportStore is nil (unit tests without DB), Import uses an in-memory
// mock that detects duplicates by ID within the current import session.
package governance

import (
	"archive/zip"
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/thebtf/engram/pkg/models"
)

// ErrImportConflict signals a duplicate-detection conflict during import.
// Returned by ImportStore.WriteMemory; import continues and records the conflict.
var ErrImportConflict = errors.New("import_conflict")

// ImportOptions controls Import behavior.
type ImportOptions struct {
	// Format explicitly selects the parser. Empty = auto-detect.
	Format ExportFormat
	// Filename is the original file name, used as a fallback after explicit Format
	// and before content-signature detection. May be empty.
	Filename string
	// TargetProject is the project context for imported memories.
	TargetProject string
}

// ImportReport summarizes the result of an Import call.
type ImportReport struct {
	DetectedFormat            ExportFormat
	MemoriesRestored          int
	EdgesRestored             int
	CandidatesRestored        int
	ConflictCount             int
	ConflictResolutionTokens  []string
	Errors                    []string
}

// ImportStore is the write surface for import. Implementing types may be
// backed by live DB stores or in-memory mocks.
//
// Contract: return ErrImportConflict for duplicate IDs. Import records the
// conflict and issues a governance resolution token; it never silently
// overwrites (EC-F8). Live-DB implementors may additionally call the write-lint
// orchestrator's Phase1 for deeper duplicate/conflict detection.
type ImportStore interface {
	WriteMemory(mem *ExportableMemory) error
	WriteEdge(edge *ExportableEdge) error
	WriteCandidate(cand *ExportableCandidate) error
}

// mockImportStore is an in-memory ImportStore for unit tests.
// Detects conflicts by ID within the session.
type mockImportStore struct {
	memories   map[int64]*ExportableMemory
	edges      map[string]bool // "srcID:dstID:type" key
	candidates map[int64]*ExportableCandidate
}

func newMockImportStore() *mockImportStore {
	return &mockImportStore{
		memories:   make(map[int64]*ExportableMemory),
		edges:      make(map[string]bool),
		candidates: make(map[int64]*ExportableCandidate),
	}
}

func (m *mockImportStore) WriteMemory(mem *ExportableMemory) error {
	if mem == nil || mem.Memory == nil {
		return fmt.Errorf("WriteMemory: nil memory")
	}
	if _, exists := m.memories[mem.ID]; exists {
		return ErrImportConflict
	}
	m.memories[mem.ID] = mem
	return nil
}

func (m *mockImportStore) WriteEdge(edge *ExportableEdge) error {
	if edge == nil {
		return fmt.Errorf("WriteEdge: nil edge")
	}
	key := fmt.Sprintf("%d:%d:%s", edge.SourceID, edge.TargetID, edge.RelationType)
	if m.edges[key] {
		return ErrImportConflict
	}
	m.edges[key] = true
	return nil
}

func (m *mockImportStore) WriteCandidate(cand *ExportableCandidate) error {
	if cand == nil || cand.CrystallizationCandidate == nil {
		return fmt.Errorf("WriteCandidate: nil candidate")
	}
	if _, exists := m.candidates[cand.ID]; exists {
		return ErrImportConflict
	}
	m.candidates[cand.ID] = cand
	return nil
}

// --- Auto-detect ---

// detectFormat implements the priority-order format detection.
//
// Priority order (per plan.md §Format auto-detect priority order + T047/T048 ACs):
//   1. Explicit Format param (always wins)
//   2. ZIP magic bytes PK\x03\x04 — unambiguous binary signature; wins over extension
//      (handles format collision: ZIP renamed to .jsonl → content sig wins; T048 AC)
//   3. File extension (.zip → FormatZIP, .jsonl → FormatJSONL)
//      (handles: JSONL renamed to .zip → extension wins; T048 AC)
//   4. JSONL content signature: leading '{' after no magic bytes matched
//   5. Error — cannot detect
func detectFormat(data []byte, opts ImportOptions) (ExportFormat, error) {
	// 1. Explicit param wins.
	if opts.Format != "" {
		return opts.Format, nil
	}

	// 2. ZIP magic bytes (PK\x03\x04) win over file extension.
	// This handles format collision: ZIP content renamed to .jsonl still routes to ZIP parser.
	if len(data) >= 4 && data[0] == 'P' && data[1] == 'K' && data[2] == 0x03 && data[3] == 0x04 {
		return FormatZIP, nil
	}

	// 3. File extension (after ZIP magic bytes to preserve collision semantics).
	if opts.Filename != "" {
		lower := strings.ToLower(opts.Filename)
		if strings.HasSuffix(lower, ".zip") {
			return FormatZIP, nil
		}
		if strings.HasSuffix(lower, ".jsonl") {
			return FormatJSONL, nil
		}
	}

	// 4. JSONL content signature: leading '{'.
	if len(data) > 0 && data[0] == '{' {
		return FormatJSONL, nil
	}

	return "", fmt.Errorf("import: cannot detect format from data (no explicit format, no ZIP magic bytes, no matching extension, no '{' signature)")
}

// --- Entry points ---

// Import parses a bundle from data using auto-format detection and an ephemeral
// in-memory store. Suitable for unit tests and dry-run scenarios where no DB is needed.
func Import(data []byte, opts ImportOptions) (*ImportReport, error) {
	return ImportWithStore(data, opts, newMockImportStore())
}

// ImportWithStore parses a bundle from data into the provided ImportStore.
// When store.WriteMemory returns ErrImportConflict, the conflict is recorded
// in the report and a resolution token is issued — no silent overwrite (EC-F8).
func ImportWithStore(data []byte, opts ImportOptions, store ImportStore) (*ImportReport, error) {
	if store == nil {
		return nil, fmt.Errorf("ImportWithStore: store must not be nil")
	}

	format, err := detectFormat(data, opts)
	if err != nil {
		return nil, err
	}

	report := &ImportReport{DetectedFormat: format}

	switch format {
	case FormatZIP:
		return importFromZIP(data, opts, store, report)
	case FormatJSONL:
		return importFromJSONL(data, opts, store, report)
	default:
		return nil, fmt.Errorf("import: unsupported format %q", format)
	}
}

// --- ZIP import (I1) ---

func importFromZIP(data []byte, _ ImportOptions, store ImportStore, report *ImportReport) (*ImportReport, error) {
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, fmt.Errorf("import ZIP: open: %w", err)
	}

	for _, f := range zr.File {
		if f.Name == "manifest.json" {
			continue // manifest is informational; we drive from content files
		}

		rc, openErr := f.Open()
		if openErr != nil {
			report.Errors = append(report.Errors, fmt.Sprintf("open %s: %v", f.Name, openErr))
			continue
		}
		var raw []byte
		buf := new(bytes.Buffer)
		_, _ = buf.ReadFrom(rc)
		_ = rc.Close()
		raw = buf.Bytes()

		if strings.HasPrefix(f.Name, "content/") && strings.HasSuffix(f.Name, ".json") {
			var mem ExportableMemory
			if decErr := json.Unmarshal(raw, &mem); decErr != nil {
				report.Errors = append(report.Errors, fmt.Sprintf("decode %s: %v", f.Name, decErr))
				continue
			}
			if writeErr := store.WriteMemory(&mem); writeErr != nil {
				if errors.Is(writeErr, ErrImportConflict) {
					report.ConflictCount++
					report.ConflictResolutionTokens = append(report.ConflictResolutionTokens, newResolutionToken())
				} else {
					report.Errors = append(report.Errors, fmt.Sprintf("write memory from %s: %v", f.Name, writeErr))
				}
				continue
			}
			report.MemoriesRestored++

		} else if strings.HasPrefix(f.Name, "edges/") && strings.HasSuffix(f.Name, ".json") {
			var edge ExportableEdge
			if decErr := json.Unmarshal(raw, &edge); decErr != nil {
				report.Errors = append(report.Errors, fmt.Sprintf("decode %s: %v", f.Name, decErr))
				continue
			}
			if writeErr := store.WriteEdge(&edge); writeErr != nil {
				if errors.Is(writeErr, ErrImportConflict) {
					report.ConflictCount++
					report.ConflictResolutionTokens = append(report.ConflictResolutionTokens, newResolutionToken())
				} else {
					report.Errors = append(report.Errors, fmt.Sprintf("write edge from %s: %v", f.Name, writeErr))
				}
				continue
			}
			report.EdgesRestored++

		} else if strings.HasPrefix(f.Name, "candidates/") && strings.HasSuffix(f.Name, ".json") {
			var cand ExportableCandidate
			if decErr := json.Unmarshal(raw, &cand); decErr != nil {
				report.Errors = append(report.Errors, fmt.Sprintf("decode %s: %v", f.Name, decErr))
				continue
			}
			if writeErr := store.WriteCandidate(&cand); writeErr != nil {
				if errors.Is(writeErr, ErrImportConflict) {
					report.ConflictCount++
					report.ConflictResolutionTokens = append(report.ConflictResolutionTokens, newResolutionToken())
				} else {
					report.Errors = append(report.Errors, fmt.Sprintf("write candidate from %s: %v", f.Name, writeErr))
				}
				continue
			}
			report.CandidatesRestored++
		}
		// .bin embedding files and unknown entries are skipped silently.
	}

	return report, nil
}

// --- JSONL import (I2, T048) ---

func importFromJSONL(data []byte, _ ImportOptions, store ImportStore, report *ImportReport) (*ImportReport, error) {
	scanner := bufio.NewScanner(bytes.NewReader(data))
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		// Validate this is valid JSON.
		var check map[string]any
		if err := json.Unmarshal(line, &check); err != nil {
			return nil, fmt.Errorf("import JSONL: invalid JSON line: %w", err)
		}

		var mem ExportableMemory
		if err := json.Unmarshal(line, &mem); err != nil {
			report.Errors = append(report.Errors, fmt.Sprintf("decode memory line: %v", err))
			continue
		}
		if writeErr := store.WriteMemory(&mem); writeErr != nil {
			if errors.Is(writeErr, ErrImportConflict) {
				report.ConflictCount++
				report.ConflictResolutionTokens = append(report.ConflictResolutionTokens, newResolutionToken())
			} else {
				report.Errors = append(report.Errors, fmt.Sprintf("write memory: %v", writeErr))
			}
			continue
		}
		report.MemoriesRestored++
	}
	return report, scanner.Err()
}

// newResolutionToken generates a unique resolution token for conflicting imports.
// Tokens are UUIDs that callers can use to reference specific conflicts in audit logs.
func newResolutionToken() string {
	return "res_" + uuid.New().String()
}

// importEdgesSidecarJSONL parses a .edges.jsonl sidecar into the store.
// Called by the JSONL importer when a sidecar is provided alongside the primary .jsonl.
func importEdgesSidecarJSONL(data []byte, store ImportStore, report *ImportReport) error {
	scanner := bufio.NewScanner(bytes.NewReader(data))
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var edge ExportableEdge
		if err := json.Unmarshal(line, &edge); err != nil {
			report.Errors = append(report.Errors, fmt.Sprintf("decode edge sidecar line: %v", err))
			continue
		}
		if writeErr := store.WriteEdge(&edge); writeErr != nil {
			if errors.Is(writeErr, ErrImportConflict) {
				report.ConflictCount++
				report.ConflictResolutionTokens = append(report.ConflictResolutionTokens, newResolutionToken())
			} else {
				report.Errors = append(report.Errors, fmt.Sprintf("write edge from sidecar: %v", writeErr))
			}
			continue
		}
		report.EdgesRestored++
	}
	return scanner.Err()
}

// importCandidatesSidecarJSONL parses a .candidates.jsonl sidecar into the store.
func importCandidatesSidecarJSONL(data []byte, store ImportStore, report *ImportReport) error {
	scanner := bufio.NewScanner(bytes.NewReader(data))
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var cand ExportableCandidate
		if err := json.Unmarshal(line, &cand); err != nil {
			report.Errors = append(report.Errors, fmt.Sprintf("decode candidate sidecar line: %v", err))
			continue
		}
		if writeErr := store.WriteCandidate(&cand); writeErr != nil {
			if errors.Is(writeErr, ErrImportConflict) {
				report.ConflictCount++
				report.ConflictResolutionTokens = append(report.ConflictResolutionTokens, newResolutionToken())
			} else {
				report.Errors = append(report.Errors, fmt.Sprintf("write candidate from sidecar: %v", writeErr))
			}
			continue
		}
		report.CandidatesRestored++
	}
	return scanner.Err()
}

// Ensure models import is used (for ExportableMemory.Memory type).
var _ = (*models.Memory)(nil)
var _ = time.Now
