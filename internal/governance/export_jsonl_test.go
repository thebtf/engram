// Package governance — export_jsonl_test.go: T046 E2 contract test.
// RED phase: establishes the golden-file test for JSONL export format.
// Contract per plan §Export/Import Matrix E2:
//   - Input: 5 sample memories + edges + 1 candidate (same fixture as E1)
//   - Output: .jsonl with memory-per-line
//     + .edges.jsonl sidecar
//     + .candidates.jsonl sidecar
//   - Streaming-friendly: content written line-by-line
//
// Note: JSONL "bundle" is a map[string][]byte keyed by filename so the
// test can verify each sidecar file independently. The Export function
// returns the primary .jsonl as []byte for E2; sidecars are in ExportSidecars.
package governance

import (
	"bufio"
	"bytes"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestExport_JSONL_E2Contract is the E2 golden-file contract test.
// Verifies the primary JSONL stream and sidecar file structure.
func TestExport_JSONL_E2Contract(t *testing.T) {
	bundle := buildTestBundle() // reuse fixture from export_zip_test.go

	opts := ExportOptions{
		Format:              FormatJSONL,
		SourceProject:       "engram",
		SourceEngramVersion: "test",
		IncludeEmbeddings:   false,
	}

	data, err := Export(bundle, opts)
	require.NoError(t, err, "Export JSONL must not return error")
	require.NotEmpty(t, data, "Export JSONL must produce non-empty bytes")

	// Primary .jsonl: one JSON object per line, one per memory.
	scanner := bufio.NewScanner(bytes.NewReader(data))
	lineCount := 0
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			continue
		}
		var obj map[string]any
		require.NoError(t, json.Unmarshal([]byte(line), &obj),
			"each JSONL line must be valid JSON, got: %s", line)
		lineCount++
	}
	require.NoError(t, scanner.Err())
	assert.Equal(t, 5, lineCount, "primary JSONL must have exactly 5 lines (one per memory)")
}

// TestExport_JSONL_Sidecars verifies edges and candidates sidecars are populated.
func TestExport_JSONL_Sidecars(t *testing.T) {
	bundle := buildTestBundle()
	opts := ExportOptions{
		Format:              FormatJSONL,
		SourceProject:       "engram",
		SourceEngramVersion: "test",
	}

	sidecars, err := ExportWithSidecars(bundle, opts)
	require.NoError(t, err, "ExportWithSidecars must not return error")

	// edges sidecar.
	edgesData, ok := sidecars[".edges.jsonl"]
	require.True(t, ok, "sidecars must include .edges.jsonl")
	edgeScanner := bufio.NewScanner(bytes.NewReader(edgesData))
	edgeCount := 0
	for edgeScanner.Scan() {
		if edgeScanner.Text() == "" {
			continue
		}
		var obj map[string]any
		require.NoError(t, json.Unmarshal([]byte(edgeScanner.Text()), &obj))
		assert.NotNil(t, obj["source_id"])
		assert.NotNil(t, obj["target_id"])
		edgeCount++
	}
	assert.Equal(t, 2, edgeCount, ".edges.jsonl must have 2 edge lines")

	// candidates sidecar.
	candData, ok := sidecars[".candidates.jsonl"]
	require.True(t, ok, "sidecars must include .candidates.jsonl")
	candScanner := bufio.NewScanner(bytes.NewReader(candData))
	candCount := 0
	for candScanner.Scan() {
		if candScanner.Text() == "" {
			continue
		}
		var obj map[string]any
		require.NoError(t, json.Unmarshal([]byte(candScanner.Text()), &obj))
		candCount++
	}
	assert.Equal(t, 1, candCount, ".candidates.jsonl must have 1 candidate line")
}

// TestExport_JSONL_StreamingFriendly verifies that each memory line can be decoded
// without buffering the full output (streaming property).
func TestExport_JSONL_StreamingFriendly(t *testing.T) {
	bundle := buildTestBundle()
	opts := ExportOptions{
		Format:              FormatJSONL,
		SourceProject:       "engram",
		SourceEngramVersion: "test",
	}

	data, err := Export(bundle, opts)
	require.NoError(t, err)

	// Read line by line and decode without loading the full buffer.
	scanner := bufio.NewScanner(bytes.NewReader(data))
	decoded := 0
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var mem ExportableMemory
		require.NoError(t, json.Unmarshal(line, &mem), "line %d must decode to ExportableMemory", decoded+1)
		decoded++
	}
	assert.Equal(t, 5, decoded, "all 5 memories must be decodable line-by-line")
}

// TestExport_JSONL_FormatPriorityOverride verifies explicit format="jsonl" param
// overrides auto-detect (tested more thoroughly in import tests, but sanity here).
func TestExport_JSONL_FormatPriorityOverride(t *testing.T) {
	bundle := buildTestBundle()

	// Even if SourceProject could confuse detection, explicit FormatJSONL wins.
	opts := ExportOptions{
		Format:              FormatJSONL,
		SourceProject:       "my-project.zip", // sneaky name that contains ".zip"
		SourceEngramVersion: "test",
	}

	data, err := Export(bundle, opts)
	require.NoError(t, err, "explicit FormatJSONL must produce JSONL even when project name contains '.zip'")
	require.NotEmpty(t, data)

	// Must parse as JSONL, not as ZIP.
	line1 := bytes.SplitN(data, []byte("\n"), 2)[0]
	var obj map[string]any
	require.NoError(t, json.Unmarshal(line1, &obj), "first line must be valid JSON, not ZIP header")
}
