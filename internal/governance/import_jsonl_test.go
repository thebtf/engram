// Package governance — import_jsonl_test.go: T048 I2 contract test.
// RED phase: establishes the golden-file test for JSONL import auto-detect.
// Contract per plan §Export/Import Matrix I2:
//   - Input: E2 output bundle (.jsonl), clean test DB
//   - Auto-detect JSONL by extension .jsonl OR content signature '{'
//   - Same write-lint surface integration as I1
//   - Format-priority edge case: ZIP renamed to .jsonl → content signature wins → ZIP parser
//   - JSONL renamed to .zip → extension wins (per priority order)
//   - Explicit format param overrides auto-detect
package governance

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestImport_JSONL_I2Contract is the I2 golden-file contract test.
// Verifies JSONL auto-detect and round-trip restore.
func TestImport_JSONL_I2Contract(t *testing.T) {
	bundle := buildTestBundle()
	opts := ExportOptions{
		Format:              FormatJSONL,
		SourceProject:       "engram",
		SourceEngramVersion: "test",
		ExportedAt:          time.Date(2026, 6, 11, 0, 0, 0, 0, time.UTC),
	}

	jsonlData, err := Export(bundle, opts)
	require.NoError(t, err)

	// Auto-detect from '{' content signature (no filename).
	report, importErr := Import(jsonlData, ImportOptions{
		TargetProject: "engram-import-test",
		Format:        "", // auto-detect
	})
	require.NoError(t, importErr, "JSONL import must not return error")
	require.NotNil(t, report)

	assert.Equal(t, 5, report.MemoriesRestored, "must restore 5 memories from JSONL")
	assert.Equal(t, 0, report.ConflictCount)
	assert.Equal(t, FormatJSONL, report.DetectedFormat, "detected format must be jsonl")
}

// TestImport_JSONL_WithSidecars verifies edges and candidates imported from sidecars.
func TestImport_JSONL_WithSidecars(t *testing.T) {
	bundle := buildTestBundle()
	opts := ExportOptions{
		Format:              FormatJSONL,
		SourceProject:       "engram",
		SourceEngramVersion: "test",
	}

	sidecars, err := ExportWithSidecars(bundle, opts)
	require.NoError(t, err)

	primary := sidecars[""]
	edgesSidecar := sidecars[".edges.jsonl"]
	candSidecar := sidecars[".candidates.jsonl"]

	require.NotEmpty(t, primary)
	require.NotEmpty(t, edgesSidecar)
	require.NotEmpty(t, candSidecar)

	store := newMockImportStore()

	// Import primary.
	report, importErr := ImportWithStore(primary, ImportOptions{
		TargetProject: "engram",
		Format:        FormatJSONL,
	}, store)
	require.NoError(t, importErr)
	assert.Equal(t, 5, report.MemoriesRestored)

	// Import sidecars.
	require.NoError(t, importEdgesSidecarJSONL(edgesSidecar, store, report))
	require.NoError(t, importCandidatesSidecarJSONL(candSidecar, store, report))

	assert.Equal(t, 2, report.EdgesRestored, "must restore 2 edges from sidecar")
	assert.Equal(t, 1, report.CandidatesRestored, "must restore 1 candidate from sidecar")
}

// TestImport_JSONL_ContentSignatureWins verifies that ZIP content renamed to .jsonl
// is detected as ZIP via content signature (priority: explicit > extension > signature).
// The content signature (PK\x03\x04) wins over the .jsonl extension.
func TestImport_JSONL_ContentSignatureWins(t *testing.T) {
	// Build a ZIP bundle.
	bundle := buildTestBundle()
	opts := ExportOptions{
		Format:              FormatZIP,
		SourceProject:       "engram",
		SourceEngramVersion: "test",
	}
	zipData, err := Export(bundle, opts)
	require.NoError(t, err)

	// Import ZIP data with filename having .jsonl extension.
	// Content signature PK\x03\x04 must WIN over the .jsonl extension.
	// Expected behavior: routed to ZIP parser; import succeeds.
	report, importErr := Import(zipData, ImportOptions{
		TargetProject: "engram",
		Filename:      "my_export.jsonl", // wrong extension — content signature wins
	})
	require.NoError(t, importErr,
		"ZIP data with .jsonl extension must still succeed via content signature detection")
	assert.Equal(t, FormatZIP, report.DetectedFormat,
		"content signature PK\\x03\\x04 must win over .jsonl extension")
	assert.Equal(t, 5, report.MemoriesRestored)
}

// TestImport_JSONL_ExtensionWinsOverContentSignature verifies that JSONL content
// renamed to .zip is detected as JSONL via... wait — per priority order,
// extension wins over content signature. So JSONL data + .zip extension → FormatZIP
// (format is routed to ZIP parser → fails because content is not valid ZIP).
// This is correct behavior: extension overrides content signature per the plan spec.
// (Only when explicit format IS set does it override extension.)
func TestImport_JSONL_ZipExtension_RoutesToZIPParser(t *testing.T) {
	bundle := buildTestBundle()
	opts := ExportOptions{
		Format:              FormatJSONL,
		SourceProject:       "engram",
		SourceEngramVersion: "test",
	}
	jsonlData, err := Export(bundle, opts)
	require.NoError(t, err)

	// JSONL data with .zip extension → extension (.zip) wins → ZIP parser → fails.
	_, importErr := Import(jsonlData, ImportOptions{
		TargetProject: "engram",
		Filename:      "my_export.zip", // wrong extension; routes to ZIP parser
	})
	assert.Error(t, importErr,
		"JSONL data with .zip extension must fail (ZIP parser rejects JSONL content)")
}

// TestImport_JSONL_ExplicitFormatOverridesExtension verifies explicit format param
// overrides the filename extension (highest priority).
func TestImport_JSONL_ExplicitFormatOverridesExtension(t *testing.T) {
	bundle := buildTestBundle()
	opts := ExportOptions{
		Format:              FormatJSONL,
		SourceProject:       "engram",
		SourceEngramVersion: "test",
	}
	jsonlData, err := Export(bundle, opts)
	require.NoError(t, err)

	// Explicit FormatJSONL overrides the .zip extension.
	report, importErr := Import(jsonlData, ImportOptions{
		TargetProject: "engram",
		Filename:      "my_export.zip",  // .zip extension
		Format:        FormatJSONL,      // explicit overrides
	})
	require.NoError(t, importErr,
		"explicit FormatJSONL must override .zip extension")
	assert.Equal(t, FormatJSONL, report.DetectedFormat)
	assert.Equal(t, 5, report.MemoriesRestored)
}
