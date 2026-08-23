package projectidentity

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

type projectIdentityV3Corpus struct {
	Contract        string `json:"contract"`
	IdentityVersion int    `json:"identity_version"`
	VectorCounts    struct {
		Accepted int `json:"accepted"`
		Refused  int `json:"refused"`
	} `json:"vector_counts"`
	Vectors []projectIdentityV3Vector `json:"vectors"`
}

type projectIdentityV3Vector struct {
	ID    string `json:"id"`
	Input struct {
		Anchor     json.RawMessage `json:"anchor"`
		Descriptor json.RawMessage `json:"descriptor"`
		Remotes    []struct {
			Form        string `json:"form"`
			Source      string `json:"source"`
			Normalized  string `json:"normalized"`
			Disposition string `json:"disposition"`
		} `json:"remote_observations"`
	} `json:"input"`
	Expected struct {
		Outcome                  ResolutionOutcomeV3 `json:"outcome"`
		CanonicalKeyMayBeExposed bool                `json:"canonical_key_may_be_exposed"`
		CanonicalProjectKey      ProjectKeyV3        `json:"canonical_project_key"`
		ScopedMutationPermitted  bool                `json:"scoped_mutation_permitted"`
		BindingEstablished       bool                `json:"binding_established"`
	} `json:"expected"`
}

func TestProjectIdentityV3DescriptorSeamUsesFrozenVectors(t *testing.T) {
	corpus := loadProjectIdentityV3Corpus(t)
	valid := corpus.vector(t, "remote-https-normalizes")

	anchor, err := ParseAnchorV3(valid.Input.Anchor)
	if err != nil {
		t.Fatalf("parse valid anchor: %v", err)
	}
	assertProjectIdentityV3JSON(t, valid.ID+" anchor", valid.Input.Anchor, anchor)

	for _, id := range []string{"anchor-unknown-field-is-invalid", "anchor-malformed-project-id-is-invalid"} {
		vector := corpus.vector(t, id)
		if _, err := ParseAnchorV3(vector.Input.Anchor); err == nil {
			t.Fatalf("%s: malformed anchor was accepted", vector.ID)
		}
	}

	normalized := valid.Input.Remotes[0]
	remote, disposition, err := NormalizeGitRemoteV3(normalized.Source)
	if err != nil {
		t.Fatalf("%s: normalize remote: %v", valid.ID, err)
	}
	if remote != normalized.Normalized || string(disposition) != "normalized" {
		t.Fatalf("%s: remote = (%q, %q), want (%q, %q)", valid.ID, remote, disposition, normalized.Normalized, "normalized")
	}

	for _, test := range []struct {
		vectorID    string
		raw         string
		disposition string
	}{
		{"remote-local-is-omitted", filepath.Join(t.TempDir(), "local-remote"), "omitted"},
		{"credential-bearing-remote-is-refused-without-raw-persistence", "https://fixture-user:fixture-password@git.example.test/Platform/Widget.git", "refused"},
	} {
		vector := corpus.vector(t, test.vectorID)
		remote, disposition, err := NormalizeGitRemoteV3(test.raw)
		if err != nil {
			t.Fatalf("%s: normalize remote: %v", vector.ID, err)
		}
		if remote != "" || string(disposition) != test.disposition {
			t.Fatalf("%s: remote = (%q, %q), want (%q, %q)", vector.ID, remote, disposition, "", test.disposition)
		}
	}

	var expectedDescriptor struct {
		ClientInstanceID string `json:"client_instance_id"`
	}
	if err := json.Unmarshal(valid.Input.Descriptor, &expectedDescriptor); err != nil {
		t.Fatalf("%s: decode descriptor: %v", valid.ID, err)
	}
	if expectedDescriptor.ClientInstanceID == "" {
		t.Fatalf("%s: fixture has no explicit client instance ID", valid.ID)
	}
	descriptor, err := BuildDescriptorV3(anchor, []string{remote}, []LegacyIdentifierV3{}, expectedDescriptor.ClientInstanceID)
	if err != nil {
		t.Fatalf("%s: build descriptor: %v", valid.ID, err)
	}
	assertProjectIdentityV3JSON(t, valid.ID+" descriptor", valid.Input.Descriptor, descriptor)
	if _, err := BuildDescriptorV3(anchor, []string{remote}, []LegacyIdentifierV3{}, ""); err == nil {
		t.Fatalf("%s: descriptor accepted an empty client instance ID", valid.ID)
	}

	parent := t.TempDir()
	selectedRoot := filepath.Join(parent, "selected")
	if err := os.Mkdir(selectedRoot, 0o700); err != nil {
		t.Fatalf("make selected root: %v", err)
	}
	if err := os.WriteFile(filepath.Join(parent, ".engram-project"), valid.Input.Anchor, 0o600); err != nil {
		t.Fatalf("write parent anchor: %v", err)
	}
	if _, err := DiscoverAnchorV3(selectedRoot, "directory"); err == nil {
		t.Fatal("directory discovery searched upward from the selected root")
	}
}

func loadProjectIdentityV3Corpus(t *testing.T) projectIdentityV3Corpus {
	t.Helper()
	bytes, err := os.ReadFile(filepath.Join("..", "..", "contracts", "testdata", "project_identity_v3_vectors.json"))
	if err != nil {
		t.Fatalf("read frozen V3 vectors: %v", err)
	}

	var corpus projectIdentityV3Corpus
	if err := json.Unmarshal(bytes, &corpus); err != nil {
		t.Fatalf("decode frozen V3 vectors: %v", err)
	}
	if corpus.Contract != "project_identity_v3" || corpus.IdentityVersion != 3 {
		t.Fatalf("unexpected vector contract %q version %d", corpus.Contract, corpus.IdentityVersion)
	}
	if len(corpus.Vectors) != corpus.VectorCounts.Accepted+corpus.VectorCounts.Refused {
		t.Fatalf("vector count = %d, want %d accepted + %d refused", len(corpus.Vectors), corpus.VectorCounts.Accepted, corpus.VectorCounts.Refused)
	}
	return corpus
}

func (corpus projectIdentityV3Corpus) vector(t *testing.T, id string) projectIdentityV3Vector {
	t.Helper()
	for _, vector := range corpus.Vectors {
		if vector.ID == id {
			return vector
		}
	}
	t.Fatalf("frozen V3 vectors are missing %q", id)
	return projectIdentityV3Vector{}
}

func assertProjectIdentityV3JSON(t *testing.T, label string, want json.RawMessage, got any) {
	t.Helper()
	var expected any
	if err := json.Unmarshal(want, &expected); err != nil {
		t.Fatalf("%s: decode expected JSON: %v", label, err)
	}
	actualBytes, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("%s: encode actual JSON: %v", label, err)
	}
	var actual any
	if err := json.Unmarshal(actualBytes, &actual); err != nil {
		t.Fatalf("%s: decode actual JSON: %v", label, err)
	}
	if !jsonEqual(expected, actual) {
		t.Fatalf("%s: JSON = %s, want %s", label, actualBytes, want)
	}
}

func jsonEqual(left, right any) bool {
	leftBytes, _ := json.Marshal(left)
	rightBytes, _ := json.Marshal(right)
	return string(leftBytes) == string(rightBytes)
}
