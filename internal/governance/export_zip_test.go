// Package governance — export_zip_test.go: T045 E1 contract test.
// RED phase: establishes the golden-file test for ZIP export format.
// Contract per plan §Export/Import Matrix E1:
//   - Input: 5 sample memories + edges + 1 candidate (in-memory fixture)
//   - Output: .zip with manifest.json + content/*.json + edges/*.json + candidates/*.json
//   - Manifest schema: schema_version, exported_at, source_project,
//     source_engram_version, memory_count, edge_count, candidate_count, checksums
package governance

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/thebtf/engram/pkg/models"
)

// TestExport_ZIP_E1Contract is the E1 golden-file contract test.
// Verifies the ZIP structure, manifest schema, and file presence.
func TestExport_ZIP_E1Contract(t *testing.T) {
	bundle := buildTestBundle()

	opts := ExportOptions{
		Format:            FormatZIP,
		SourceProject:     "engram",
		SourceEngramVersion: "test",
		IncludeEmbeddings: false,
	}

	data, err := Export(bundle, opts)
	require.NoError(t, err, "Export must not return error")
	require.NotEmpty(t, data, "Export must produce non-empty bytes")

	// Verify it's a valid ZIP.
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	require.NoError(t, err, "Export output must be a valid ZIP")

	files := make(map[string][]byte)
	for _, f := range zr.File {
		rc, openErr := f.Open()
		require.NoError(t, openErr, "must open zip entry %s", f.Name)
		buf := new(bytes.Buffer)
		_, _ = buf.ReadFrom(rc)
		_ = rc.Close()
		files[f.Name] = buf.Bytes()
	}

	// manifest.json must exist with required schema fields.
	manifestBytes, ok := files["manifest.json"]
	require.True(t, ok, "ZIP must contain manifest.json")

	var manifest map[string]any
	require.NoError(t, json.Unmarshal(manifestBytes, &manifest), "manifest.json must be valid JSON")

	assert.Equal(t, "1", manifest["schema_version"], "schema_version must be '1'")
	assert.NotEmpty(t, manifest["exported_at"], "exported_at must be present")
	assert.Equal(t, "engram", manifest["source_project"])
	assert.NotEmpty(t, manifest["source_engram_version"])
	assert.Equal(t, float64(5), manifest["memory_count"], "memory_count must be 5")
	assert.Equal(t, float64(2), manifest["edge_count"], "edge_count must match fixture")
	assert.Equal(t, float64(1), manifest["candidate_count"], "candidate_count must be 1")
	assert.NotNil(t, manifest["checksums"], "checksums must be present")

	// content/*.json: one file per memory.
	for i := 1; i <= 5; i++ {
		key := "content/memory_001.json"
		if i > 1 {
			// just check at least one is present — exact names vary
			break
		}
		_ = files[key]
	}
	// Count content/ files.
	contentCount := 0
	for name := range files {
		if len(name) > 8 && name[:8] == "content/" {
			contentCount++
		}
	}
	assert.Equal(t, 5, contentCount, "ZIP must have 5 content/*.json files (one per memory)")

	// edges/*.json: 2 edge files.
	edgeCount := 0
	for name := range files {
		if len(name) > 6 && name[:6] == "edges/" {
			edgeCount++
		}
	}
	assert.Equal(t, 2, edgeCount, "ZIP must have 2 edges/*.json files")

	// candidates/*.json: 1 candidate file.
	candidateCount := 0
	for name := range files {
		if len(name) > 11 && name[:11] == "candidates/" {
			candidateCount++
		}
	}
	assert.Equal(t, 1, candidateCount, "ZIP must have 1 candidates/*.json file")

	// No .bin embedding files when IncludeEmbeddings=false.
	for name := range files {
		assert.NotContains(t, name, ".bin", "no .bin embedding files when IncludeEmbeddings=false")
	}
}

// TestExport_ZIP_IncludeEmbeddings verifies .bin files present when opted in.
func TestExport_ZIP_IncludeEmbeddings(t *testing.T) {
	bundle := buildTestBundle()

	// Attach dummy embedding bytes to memories.
	for _, m := range bundle.Memories {
		m.EmbeddingBytes = []byte{0x01, 0x02, 0x03}
	}

	opts := ExportOptions{
		Format:              FormatZIP,
		SourceProject:       "engram",
		SourceEngramVersion: "test",
		IncludeEmbeddings:   true,
	}

	data, err := Export(bundle, opts)
	require.NoError(t, err)

	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	require.NoError(t, err)

	hasBin := false
	for _, f := range zr.File {
		if len(f.Name) > 4 && f.Name[len(f.Name)-4:] == ".bin" {
			hasBin = true
			break
		}
	}
	assert.True(t, hasBin, "ZIP must contain .bin files when IncludeEmbeddings=true")
}

// TestExport_ZIP_DeterministicOutput verifies two identical calls produce identical ZIPs
// (deterministic compression per T045 REFACTOR spec).
func TestExport_ZIP_DeterministicOutput(t *testing.T) {
	bundle := buildTestBundle()
	opts := ExportOptions{
		Format:              FormatZIP,
		SourceProject:       "engram",
		SourceEngramVersion: "test",
		// Fix exported_at for determinism.
		ExportedAt: time.Date(2026, 6, 11, 0, 0, 0, 0, time.UTC),
	}

	data1, err := Export(bundle, opts)
	require.NoError(t, err)

	data2, err := Export(bundle, opts)
	require.NoError(t, err)

	assert.Equal(t, data1, data2, "two identical Export calls must produce identical ZIP bytes")
}

// --- fixture helpers ---

// ExportBundle is the input to Export.
// Defined here for test use; canonical type lives in export.go.

// buildTestBundle builds a 5-memory + 2-edge + 1-candidate fixture.
func buildTestBundle() *ExportBundle {
	now := time.Now().UTC()
	memories := make([]*ExportableMemory, 5)
	for i := 0; i < 5; i++ {
		memories[i] = &ExportableMemory{
			Memory: &models.Memory{
				ID:      int64(i + 1),
				Content: "test memory content #" + itoa(i+1),
				Project: "engram",
				Tags:    []string{"test"},
			},
			ExportedAt: now,
		}
	}

	edges := []*ExportableEdge{
		{SourceID: 1, TargetID: 2, RelationType: "related_to", ExportedAt: now},
		{SourceID: 2, TargetID: 3, RelationType: "supersedes", ExportedAt: now},
	}

	candidates := []*ExportableCandidate{
		{
			CrystallizationCandidate: &models.CrystallizationCandidate{
				ID:              1,
				ProposedContent: "candidate proposed content",
				ProposedTier:    "semantic",
				Status:          models.CandidateStatusPending,
			},
			ExportedAt: now,
		},
	}

	return &ExportBundle{
		Memories:   memories,
		Edges:      edges,
		Candidates: candidates,
	}
}

func itoa(i int) string {
	if i < 10 {
		return string(rune('0' + i))
	}
	return "10+"
}
