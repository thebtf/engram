package projectidentity

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestApprovedProjectSeedBindings(t *testing.T) {
	root := t.TempDir()
	storage := t.TempDir()
	marker := []byte(`{"id":"abcdef","name":"synthetic"}`)
	v2 := []byte(`{"fixture":"v2"}`)
	for name, value := range map[string][]byte{".engram-project": marker, ".engram-project-v2.json": v2} {
		if err := os.WriteFile(filepath.Join(root, name), value, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	state := ProjectMarkerState{Target: ".engram-project", TargetRootDigest: markerEvidenceDigest([]byte(root)), MarkerDigest: markerEvidenceDigest(marker), Present: true}
	const server = "https://fixture.example"
	entry := approvedProjectSeed{RootSHA256: state.TargetRootDigest, ServerSHA256: markerEvidenceDigest([]byte(server)), MarkerSHA256: state.MarkerDigest, V2SHA256: markerEvidenceDigest(v2), ProjectID: "11111111-1111-4111-8111-111111111111", ApprovalSHA256: strings.Repeat("a", 64)}
	seedPath := filepath.Join(storage, projectRecoverySeedFilename)
	publish := func(entries []approvedProjectSeed) {
		t.Helper()
		raw, err := json.Marshal(map[string]any{"version": 1, "entries": entries})
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(seedPath, raw, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	publish([]approvedProjectSeed{entry})
	resolve := func(dir, url string, s ProjectMarkerState) (Project, bool, error) {
		return ResolveApprovedProjectSeed(context.Background(), dir, url, root, s)
	}
	project, found, err := resolve(storage, server, state)
	if err != nil || !found || project.ProjectID != entry.ProjectID {
		t.Fatalf("valid seed: %v %v %v", project, found, err)
	}
	target, digest, err := RecoveryV2Precondition(root, state)
	if err != nil || target != ".engram-project-v2.json" || digest != entry.V2SHA256 {
		t.Fatal("bad v2 precondition")
	}
	if _, found, err := resolve(storage, server+"/other", state); found || err == nil {
		t.Fatal("wrong server accepted")
	}
	stale := state
	stale.MarkerDigest = strings.Repeat("b", 64)
	if _, found, err := resolve(storage, server, stale); found || err == nil {
		t.Fatal("stale marker accepted")
	}
	wrongRoot := state
	wrongRoot.TargetRootDigest = strings.Repeat("c", 64)
	if _, found, err := resolve(storage, server, wrongRoot); found || err != nil {
		t.Fatal("wrong root selected")
	}
	if err := os.WriteFile(filepath.Join(root, ".engram-project-v2.json"), []byte("changed"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, found, err := resolve(storage, server, state); found || err == nil {
		t.Fatal("stale v2 accepted")
	}
	if err := os.WriteFile(filepath.Join(root, ".engram-project-v2.json"), v2, 0o600); err != nil {
		t.Fatal(err)
	}
	publish([]approvedProjectSeed{entry, entry})
	if _, found, err := resolve(storage, server, state); found || err == nil {
		t.Fatal("duplicate accepted")
	}
	if err := os.WriteFile(seedPath, []byte(`{"version":1,"VERSION":1,"entries":[]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, found, err := resolve(storage, server, state); found || err == nil {
		t.Fatal("case-variant duplicate accepted")
	}
	if _, found, err := resolve(filepath.Join(storage, "missing"), server, state); found || err != nil {
		t.Fatal("optional absence blocked")
	}
	if _, found, err := resolve("relative", server, state); found || err == nil {
		t.Fatal("relative storage accepted")
	}
	linkDir := t.TempDir()
	link := filepath.Join(linkDir, projectRecoverySeedFilename)
	if err := os.Symlink(seedPath, link); err == nil {
		if _, found, err := resolve(linkDir, server, state); found || err == nil {
			t.Fatal("symlink seed accepted")
		}
	} else {
		t.Logf("symlink fixture unavailable: %v", err)
	}
	current, err := os.ReadFile(filepath.Join(root, ".engram-project"))
	if err != nil || string(current) != string(marker) {
		t.Fatal("seed loader changed marker")
	}
}
