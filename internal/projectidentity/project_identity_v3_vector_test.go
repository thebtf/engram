package projectidentity

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

var (
	_ = ParseAnchorV3
	_ = DiscoverAnchorV3
	_ = NormalizeGitRemoteV3
	_ = BuildDescriptorV3
)

type projectIdentityV3Corpus struct {
	Contract        string `json:"contract"`
	IdentityVersion int    `json:"identity_version"`
	VectorCounts    struct {
		Accepted int `json:"accepted"`
		Refused  int `json:"refused"`
	} `json:"vector_counts"`
	Vectors []struct {
		ID    string `json:"id"`
		Input struct {
			Anchor     json.RawMessage `json:"anchor"`
			Descriptor json.RawMessage `json:"descriptor"`
			Remotes    []struct {
				Disposition string `json:"disposition"`
				Normalized  string `json:"normalized"`
			} `json:"remote_observations"`
		} `json:"input"`
	} `json:"vectors"`
}

func TestProjectIdentityV3DescriptorSeamUsesFrozenVectors(t *testing.T) {
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

	anchors, descriptors, normalized, omitted, refused := 0, 0, 0, 0, 0
	for _, vector := range corpus.Vectors {
		if vector.ID == "" {
			t.Fatal("frozen V3 vectors contain an unnamed case")
		}
		if string(vector.Input.Anchor) != "" && string(vector.Input.Anchor) != "null" {
			anchors++
		}
		if string(vector.Input.Descriptor) != "" && string(vector.Input.Descriptor) != "null" {
			descriptors++
		}
		for _, remote := range vector.Input.Remotes {
			switch remote.Disposition {
			case "":
				if remote.Normalized == "" {
					t.Fatalf("%s has a remote without a normalized value or disposition", vector.ID)
				}
				normalized++
			case "omitted":
				omitted++
			case "refused":
				refused++
			default:
				t.Fatalf("%s has unknown remote disposition %q", vector.ID, remote.Disposition)
			}
		}
	}
	if anchors == 0 || descriptors == 0 || normalized == 0 || omitted == 0 || refused == 0 {
		t.Fatalf("incomplete V3 helper coverage: anchors=%d descriptors=%d normalized=%d omitted=%d refused=%d", anchors, descriptors, normalized, omitted, refused)
	}
}
