// Package governance — import_zip_test.go: T047 I1 contract test.
// RED phase: establishes the golden-file test for ZIP import auto-detect.
// Contract per plan §Export/Import Matrix I1:
//   - Input: E1 output bundle (ZIP), clean test DB
//   - Auto-detect: by file extension .zip OR content signature PK\x03\x04
//   - Output: all memories + edges + candidates restored; import report with
//     conflict count + resolution tokens issued
//   - Write-lint seam: TG5 absent → direct write with conflict REPORT (no silent overwrite)
//   - EC-F8: NOT silent overwrite EVER
package governance

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestImport_ZIP_I1Contract is the I1 golden-file contract test.
// Verifies auto-detect and round-trip restore from a ZIP bundle.
func TestImport_ZIP_I1Contract(t *testing.T) {
	// Build and export a bundle, then import it back.
	bundle := buildTestBundle()
	opts := ExportOptions{
		Format:              FormatZIP,
		SourceProject:       "engram",
		SourceEngramVersion: "test",
		ExportedAt:          time.Date(2026, 6, 11, 0, 0, 0, 0, time.UTC),
	}

	zipData, err := Export(bundle, opts)
	require.NoError(t, err, "export must succeed")

	// Auto-detect from content signature (no file extension — raw bytes).
	report, importErr := Import(zipData, ImportOptions{
		TargetProject: "engram-import-test",
		Format:        "", // auto-detect from signature
	})
	require.NoError(t, importErr, "import must not return error")
	require.NotNil(t, report)

	assert.Equal(t, 5, report.MemoriesRestored, "must restore 5 memories")
	assert.Equal(t, 2, report.EdgesRestored, "must restore 2 edges")
	assert.Equal(t, 1, report.CandidatesRestored, "must restore 1 candidate")
	assert.Equal(t, 0, report.ConflictCount, "no conflicts in clean import")
	assert.Empty(t, report.Errors, "no errors in clean import")
	assert.Equal(t, FormatZIP, report.DetectedFormat, "detected format must be zip")
}

// TestImport_ZIP_AutoDetect_ContentSignature verifies the PK\x03\x04 magic byte detection.
func TestImport_ZIP_AutoDetect_ContentSignature(t *testing.T) {
	bundle := buildTestBundle()
	opts := ExportOptions{
		Format:              FormatZIP,
		SourceProject:       "engram",
		SourceEngramVersion: "test",
	}

	zipData, err := Export(bundle, opts)
	require.NoError(t, err)

	// Verify the magic bytes are present.
	require.GreaterOrEqual(t, len(zipData), 4, "ZIP data must have at least 4 bytes")
	assert.Equal(t, byte('P'), zipData[0])
	assert.Equal(t, byte('K'), zipData[1])
	assert.Equal(t, byte(0x03), zipData[2])
	assert.Equal(t, byte(0x04), zipData[3])

	// Import without specifying format — auto-detect must route to ZIP.
	report, importErr := Import(zipData, ImportOptions{TargetProject: "engram"})
	require.NoError(t, importErr)
	assert.Equal(t, FormatZIP, report.DetectedFormat)
}

// TestImport_ZIP_NoSilentOverwrite verifies EC-F8: import conflict is reported,
// not silently overwritten. When the same memory IDs are imported twice, the
// second import reports conflicts rather than overwriting.
func TestImport_ZIP_NoSilentOverwrite(t *testing.T) {
	bundle := buildTestBundle()
	opts := ExportOptions{
		Format:              FormatZIP,
		SourceProject:       "engram",
		SourceEngramVersion: "test",
	}

	zipData, err := Export(bundle, opts)
	require.NoError(t, err)

	target := newMockImportStore()

	// First import: no conflicts.
	report1, err := ImportWithStore(zipData, ImportOptions{TargetProject: "engram"}, target)
	require.NoError(t, err)
	assert.Equal(t, 0, report1.ConflictCount)

	// Second import of same bundle: conflicts detected.
	report2, err := ImportWithStore(zipData, ImportOptions{TargetProject: "engram"}, target)
	require.NoError(t, err, "import with conflicts must return import report, not error")
	assert.Greater(t, report2.ConflictCount, 0,
		"second import of same bundle must report conflicts (EC-F8 no silent overwrite)")
	assert.NotEmpty(t, report2.ConflictResolutionTokens,
		"conflicts must issue resolution tokens for manual review")
}

// TestImport_ZIP_AutoDetectPriorityOrder verifies the auto-detect priority:
// explicit param > extension > content signature.
func TestImport_ZIP_AutoDetectPriorityOrder(t *testing.T) {
	bundle := buildTestBundle()
	opts := ExportOptions{
		Format:              FormatZIP,
		SourceProject:       "engram",
		SourceEngramVersion: "test",
	}
	zipData, err := Export(bundle, opts)
	require.NoError(t, err)

	// Explicit param wins even if content is ZIP.
	// Importing ZIP as JSONL must fail (parse error, not success).
	_, importErr := Import(zipData, ImportOptions{
		TargetProject: "engram",
		Format:        FormatJSONL, // explicit overrides content detection
	})
	// JSONL parser will fail on ZIP binary content.
	assert.Error(t, importErr,
		"importing ZIP with explicit FormatJSONL must fail (parse error expected)")
}
