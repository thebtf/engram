// Package governance implements export/import of engram bundles (Milestone-F TG6).
//
// Export produces deterministic ZIP or JSONL bundles from memories, edges, and
// crystallization candidates. Import reconstructs them into a target store.
//
// Format routing:
//   - ZIP (default, FormatZIP): manifest.json + content/*.json + edges/*.json
//     + candidates/*.json; optional .bin embedding files.
//   - JSONL (FormatJSONL, T046): one memory per line in .jsonl + sidecar files.
//
// Determinism: ZIP file order is sorted by entry name; entries use deterministic
// timestamps derived from ExportedAt (or fixed ExportOptions.ExportedAt).
// This enables byte-stable round-trips for test golden-file comparisons.
package governance

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"sort"
	"time"

	"github.com/thebtf/engram/pkg/models"
)

// ExportFormat identifies the output format.
type ExportFormat string

const (
	// FormatZIP is the default ZIP bundle format (E1).
	FormatZIP ExportFormat = "zip"
	// FormatJSONL is the streaming JSONL format (E2, T046).
	FormatJSONL ExportFormat = "jsonl"
)

// ExportOptions controls Export behavior.
type ExportOptions struct {
	Format              ExportFormat
	SourceProject       string
	SourceEngramVersion string
	IncludeEmbeddings   bool
	// ExportedAt is the timestamp stamped into the manifest and entry metadata.
	// When zero, time.Now().UTC() is used. Set for deterministic test output.
	ExportedAt time.Time
}

// ExportBundle is the in-memory input to Export.
// Callers populate Memories, Edges, and Candidates from stores before calling Export.
type ExportBundle struct {
	Memories   []*ExportableMemory
	Edges      []*ExportableEdge
	Candidates []*ExportableCandidate
}

// ExportableMemory wraps a Memory with export metadata.
type ExportableMemory struct {
	*models.Memory
	ExportedAt     time.Time `json:"exported_at"`
	EmbeddingBytes []byte    `json:"-"` // omitted from JSON; included in .bin when IncludeEmbeddings=true
}

// ExportableEdge wraps a knowledge_edges row for export.
type ExportableEdge struct {
	SourceID     int64  `json:"source_id"`
	TargetID     int64  `json:"target_id"`
	RelationType string `json:"relation_type"`
	ExportedAt   time.Time `json:"exported_at"`
}

// ExportableCandidate wraps a CrystallizationCandidate for export.
type ExportableCandidate struct {
	*models.CrystallizationCandidate
	ExportedAt time.Time `json:"exported_at"`
}

// manifest is the internal manifest structure written to manifest.json.
type manifest struct {
	SchemaVersion       string         `json:"schema_version"`
	ExportedAt          string         `json:"exported_at"`
	SourceProject       string         `json:"source_project"`
	SourceEngramVersion string         `json:"source_engram_version"`
	MemoryCount         int            `json:"memory_count"`
	EdgeCount           int            `json:"edge_count"`
	CandidateCount      int            `json:"candidate_count"`
	Checksums           map[string]string `json:"checksums"`
}

// Export serializes an ExportBundle into the requested format.
// Returns the bundle as raw bytes (ZIP archive or JSONL text).
func Export(bundle *ExportBundle, opts ExportOptions) ([]byte, error) {
	if bundle == nil {
		return nil, fmt.Errorf("export: bundle must not be nil")
	}

	exportedAt := opts.ExportedAt
	if exportedAt.IsZero() {
		exportedAt = time.Now().UTC()
	}

	switch opts.Format {
	case FormatZIP, "":
		return exportZIP(bundle, opts, exportedAt)
	case FormatJSONL:
		return exportJSONL(bundle, opts, exportedAt)
	default:
		return nil, fmt.Errorf("export: unknown format %q (must be zip or jsonl)", opts.Format)
	}
}

// exportZIP produces a deterministic ZIP archive (E1 contract).
func exportZIP(bundle *ExportBundle, opts ExportOptions, exportedAt time.Time) ([]byte, error) {
	// Use a fixed mod-time for all entries to ensure byte-determinism.
	// Go's archive/zip uses the mod-time in the local file header; a fixed
	// time collapses the non-determinism.
	entryTime := exportedAt

	checksums := make(map[string]string)
	type entry struct {
		name string
		data []byte
	}
	var entries []entry

	// --- content/*.json (one per memory) ---
	for i, mem := range bundle.Memories {
		name := fmt.Sprintf("content/memory_%04d.json", i+1)
		data, err := json.Marshal(mem)
		if err != nil {
			return nil, fmt.Errorf("export: marshal memory %d: %w", mem.ID, err)
		}
		checksums[name] = sha256hex(data)
		entries = append(entries, entry{name, data})

		// Optional embedding .bin
		if opts.IncludeEmbeddings && len(mem.EmbeddingBytes) > 0 {
			binName := fmt.Sprintf("content/memory_%04d.bin", i+1)
			checksums[binName] = sha256hex(mem.EmbeddingBytes)
			entries = append(entries, entry{binName, mem.EmbeddingBytes})
		}
	}

	// --- edges/*.json (one per edge) ---
	for i, edge := range bundle.Edges {
		name := fmt.Sprintf("edges/edge_%04d.json", i+1)
		data, err := json.Marshal(edge)
		if err != nil {
			return nil, fmt.Errorf("export: marshal edge %d: %w", i, err)
		}
		checksums[name] = sha256hex(data)
		entries = append(entries, entry{name, data})
	}

	// --- candidates/*.json (one per candidate) ---
	for i, cand := range bundle.Candidates {
		name := fmt.Sprintf("candidates/candidate_%04d.json", i+1)
		data, err := json.Marshal(cand)
		if err != nil {
			return nil, fmt.Errorf("export: marshal candidate %d: %w", i, err)
		}
		checksums[name] = sha256hex(data)
		entries = append(entries, entry{name, data})
	}

	// Sort entries by name for deterministic ZIP ordering.
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].name < entries[j].name
	})

	// Build manifest after all checksums are computed.
	mf := manifest{
		SchemaVersion:       "1",
		ExportedAt:          exportedAt.UTC().Format(time.RFC3339),
		SourceProject:       opts.SourceProject,
		SourceEngramVersion: opts.SourceEngramVersion,
		MemoryCount:         len(bundle.Memories),
		EdgeCount:           len(bundle.Edges),
		CandidateCount:      len(bundle.Candidates),
		Checksums:           checksums,
	}
	manifestData, err := json.MarshalIndent(mf, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("export: marshal manifest: %w", err)
	}

	// Write ZIP. manifest.json goes first (before sorted content entries).
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)

	writeEntry := func(name string, data []byte) error {
		h := &zip.FileHeader{
			Name:     name,
			Method:   zip.Deflate,
			Modified: entryTime,
		}
		w, wErr := zw.CreateHeader(h)
		if wErr != nil {
			return fmt.Errorf("create zip entry %s: %w", name, wErr)
		}
		_, wErr = w.Write(data)
		return wErr
	}

	if err := writeEntry("manifest.json", manifestData); err != nil {
		return nil, fmt.Errorf("export: %w", err)
	}
	for _, e := range entries {
		if err := writeEntry(e.name, e.data); err != nil {
			return nil, fmt.Errorf("export: %w", err)
		}
	}

	if err := zw.Close(); err != nil {
		return nil, fmt.Errorf("export: close zip: %w", err)
	}

	return buf.Bytes(), nil
}

// exportJSONL produces the primary JSONL stream (one memory per line).
// The primary stream contains memories. Edges and candidates are in sidecars
// accessible via ExportWithSidecars. Format is streaming-friendly (no buffering).
func exportJSONL(bundle *ExportBundle, _ ExportOptions, _ time.Time) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)

	for _, mem := range bundle.Memories {
		if err := enc.Encode(mem); err != nil {
			return nil, fmt.Errorf("export JSONL: encode memory %d: %w", mem.ID, err)
		}
	}
	return buf.Bytes(), nil
}

// ExportWithSidecars returns the primary JSONL bytes plus a map of sidecar files.
// Keys are sidecar suffixes (".edges.jsonl", ".candidates.jsonl").
// Sidecars enable independent streaming import of edges and candidates.
func ExportWithSidecars(bundle *ExportBundle, opts ExportOptions) (map[string][]byte, error) {
	if bundle == nil {
		return nil, fmt.Errorf("ExportWithSidecars: bundle must not be nil")
	}

	exportedAt := opts.ExportedAt
	if exportedAt.IsZero() {
		exportedAt = time.Now().UTC()
	}

	primary, err := exportJSONL(bundle, opts, exportedAt)
	if err != nil {
		return nil, err
	}

	sidecars := map[string][]byte{
		"":                  primary, // primary stream
		".edges.jsonl":      nil,
		".candidates.jsonl": nil,
	}

	// .edges.jsonl
	var edgeBuf bytes.Buffer
	edgeEnc := json.NewEncoder(&edgeBuf)
	edgeEnc.SetEscapeHTML(false)
	for _, edge := range bundle.Edges {
		if err := edgeEnc.Encode(edge); err != nil {
			return nil, fmt.Errorf("ExportWithSidecars: encode edge: %w", err)
		}
	}
	sidecars[".edges.jsonl"] = edgeBuf.Bytes()

	// .candidates.jsonl
	var candBuf bytes.Buffer
	candEnc := json.NewEncoder(&candBuf)
	candEnc.SetEscapeHTML(false)
	for _, cand := range bundle.Candidates {
		if err := candEnc.Encode(cand); err != nil {
			return nil, fmt.Errorf("ExportWithSidecars: encode candidate: %w", err)
		}
	}
	sidecars[".candidates.jsonl"] = candBuf.Bytes()

	return sidecars, nil
}

// sha256hex returns the hex-encoded SHA-256 checksum of data.
func sha256hex(data []byte) string {
	h := sha256.Sum256(data)
	return fmt.Sprintf("%x", h)
}
