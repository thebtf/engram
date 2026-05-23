package core

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/thebtf/engram/pkg/cognitive"
)

// TestNormalizedByteIdentity_v6_3_0_Baseline is the FR-9 byte-identity
// release-blocker gate. It loads the committed v6.3.0 baseline fixtures
// (synthetic for now, real-captured per T021 in CI) and asserts that:
//
//	(a) the fixtures are non-empty and start with `{`
//	(b) the fixtures contain ZERO occurrences of any VolatileFields key at
//	    any depth (i.e., they are already normalized — a fixture that
//	    snuck a volatile field through would fail downstream comparisons)
//	(c) re-applying pkg/cognitive.NormalizeForDiff to the fixture bytes
//	    is idempotent — the second normalization byte-equals the first.
//	    This is the load-bearing property: when the v7 plug-linked-master-off
//	    binary produces output that NormalizeForDiff turns into the fixture,
//	    re-normalizing it cannot drift.
//	(d) the fixtures stay under 50 KB (NFR-2-adjacent payload-size budget)
//
// Together these form the FR-9 gate machinery. The actual v7-master-off
// vs v6.3.0 binary comparison is a CI integration test (T021 / TD-004)
// that compares plug-linked-master-off runtime output to these fixtures.
func TestNormalizedByteIdentity_v6_3_0_Baseline(t *testing.T) {
	fixtures := []string{
		"session_start_response.json",
		"tools_list_response.json",
	}

	for _, name := range fixtures {
		name := name
		t.Run(name, func(t *testing.T) {
			path := filepath.Join("testdata", "v6_3_0_baseline", name)
			raw, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read fixture %s: %v", path, err)
			}

			// (a) non-empty + opens with '{'
			if len(raw) == 0 {
				t.Fatalf("fixture %s is empty — captured payload was lost", path)
			}
			// Strip trailing newline before the opening-brace check; the
			// README convention requires the file to END with a newline but
			// the JSON body still begins with '{'.
			trimmed := bytes.TrimRight(raw, "\r\n")
			if len(trimmed) == 0 || trimmed[0] != '{' {
				t.Fatalf("fixture %s does not start with '{'; got %q", path, raw[:min(40, len(raw))])
			}

			// (b) no volatile keys at any depth — string scan is sufficient
			//     because VolatileFields are JSON object key tokens like
			//     `"generated_at":` which cannot legitimately appear as
			//     value substrings in a normalized payload.
			body := string(trimmed)
			for _, vf := range cognitive.VolatileFields {
				token := "\"" + vf + "\""
				if strings.Contains(body, token) {
					t.Errorf("fixture %s contains volatile key %q — capture pipeline must strip it", path, token)
				}
			}

			// (c) idempotence: NormalizeForDiff(fixture) == fixture (modulo
			//     trailing newline). Two consecutive normalizations must
			//     also byte-equal.
			normalized1, err := cognitive.NormalizeForDiff(trimmed)
			if err != nil {
				t.Fatalf("NormalizeForDiff first pass: %v", err)
			}
			if !bytes.Equal(normalized1, trimmed) {
				// Surface a short prefix diff for diagnostics.
				maxLen := 200
				if len(normalized1) < maxLen {
					maxLen = len(normalized1)
				}
				gotPrefix := normalized1[:maxLen]
				wantPrefix := trimmed[:min(maxLen, len(trimmed))]
				t.Errorf("fixture %s: NormalizeForDiff output drifts from fixture\n  got prefix: %s\n  want prefix: %s",
					path, gotPrefix, wantPrefix)
			}
			normalized2, err := cognitive.NormalizeForDiff(normalized1)
			if err != nil {
				t.Fatalf("NormalizeForDiff second pass: %v", err)
			}
			if !bytes.Equal(normalized1, normalized2) {
				t.Errorf("fixture %s: NormalizeForDiff is not idempotent across two passes", path)
			}

			// (d) ≤ 50 KB
			const maxBytes = 50 * 1024
			if len(raw) > maxBytes {
				t.Errorf("fixture %s exceeds 50 KB budget: got %d bytes", path, len(raw))
			}
		})
	}
}

// TestNormalizedByteIdentity_NFR1_MasterOnAllOff verifies the NFR-1 sister
// property: when the master flag is ON but every subsystem flag is OFF
// (only NoOps registered+enabled), normalising a representative payload
// produces the SAME bytes as the master-off baseline. This is the
// "plug-linked-master-on-all-noops equals plug-linked-master-off" check.
//
// Implementation: we don't have a live runtime here; the property is
// instead expressed at the normalize level — given a deterministic input,
// normalize produces the same output regardless of which side of the
// flag we're on. The test asserts this by normalizing the fixture twice
// and confirming bit-equality. The runtime equivalence is exercised by
// the chaos test (T016) and the integration test scaffolded by T021.
func TestNormalizedByteIdentity_NFR1_MasterOnAllOff(t *testing.T) {
	path := filepath.Join("testdata", "v6_3_0_baseline", "session_start_response.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	trimmed := bytes.TrimRight(raw, "\r\n")

	first, err := cognitive.NormalizeForDiff(trimmed)
	if err != nil {
		t.Fatalf("NormalizeForDiff first: %v", err)
	}

	// Decode + re-encode through a parallel JSON path to simulate a
	// different code path producing the same logical structure. If
	// NormalizeForDiff is truly bit-stable, the round-trip output should
	// match `first` exactly.
	var parsed any
	if err := json.Unmarshal(trimmed, &parsed); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	repacked, err := json.Marshal(parsed)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	second, err := cognitive.NormalizeForDiff(repacked)
	if err != nil {
		t.Fatalf("NormalizeForDiff second: %v", err)
	}

	if !bytes.Equal(first, second) {
		t.Errorf("NFR-1: normalized bytes differ between direct and round-tripped inputs\n  first len: %d\n  second len: %d",
			len(first), len(second))
	}
}

// min is a small helper to keep test code Go-1.20-compatible (built-in min
// is Go 1.21+; using a local helper keeps the test compile-stable).
func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
