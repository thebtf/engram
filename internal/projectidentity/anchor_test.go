package projectidentity

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const validRepositoryAnchorV3 = `{"version":3,"project_id":"11111111-1111-4111-8111-111111111111","name":"widget-service","scope":"repository"}`

func TestParseAnchorV3StrictValidation(t *testing.T) {
	anchor, err := ParseAnchorV3([]byte(validRepositoryAnchorV3))
	if err != nil {
		t.Fatalf("parse valid anchor: %v", err)
	}
	if anchor.ProjectID != "11111111-1111-4111-8111-111111111111" || anchor.Name != "widget-service" || anchor.Scope != "repository" {
		t.Fatalf("anchor = %#v", anchor)
	}

	for _, raw := range []string{
		`{}`,
		`{"version":3,"project_id":"11111111-1111-4111-8111-111111111111","name":"widget-service","scope":"repository","unexpected":true}`,
		`{"version":3,"project_id":"not-a-uuid","name":"widget-service","scope":"repository"}`,
		`{"version":2,"project_id":"11111111-1111-4111-8111-111111111111","name":"widget-service","scope":"repository"}`,
		`{"version":3,"project_id":"11111111-1111-4111-8111-111111111111","name":"","scope":"repository"}`,
		`{"version":3,"project_id":"11111111-1111-4111-8111-111111111111","name":"widget-service","scope":"inferred"}`,
		validRepositoryAnchorV3 + ` true`,
	} {
		if _, err := ParseAnchorV3([]byte(raw)); err == nil {
			t.Fatalf("ParseAnchorV3 accepted %s", raw)
		}
	}
}

func TestDiscoverAnchorV3UsesOnlySelectedRoot(t *testing.T) {
	directoryRoot := t.TempDir()
	if err := os.WriteFile(filepath.Join(directoryRoot, anchorFilenameV3), []byte(`{"version":3,"project_id":"22222222-2222-4222-8222-222222222222","name":"directory-workspace","scope":"directory"}`), 0o600); err != nil {
		t.Fatalf("write directory anchor: %v", err)
	}
	if _, err := DiscoverAnchorV3(directoryRoot, "directory"); err != nil {
		t.Fatalf("discover explicit directory anchor: %v", err)
	}

	selectedRoot := filepath.Join(directoryRoot, "nested")
	if err := os.Mkdir(selectedRoot, 0o700); err != nil {
		t.Fatalf("make nested root: %v", err)
	}
	if _, err := DiscoverAnchorV3(selectedRoot, "directory"); err == nil {
		t.Fatal("directory discovery searched upward")
	}
}

func TestDiscoverAnchorV3RepositoryRequiresTrackedSelectedRoot(t *testing.T) {
	root := t.TempDir()
	runGit(t, root, "init")
	if err := os.WriteFile(filepath.Join(root, anchorFilenameV3), []byte(validRepositoryAnchorV3), 0o600); err != nil {
		t.Fatalf("write repository anchor: %v", err)
	}
	runGit(t, root, "add", anchorFilenameV3)
	if _, err := DiscoverAnchorV3(root, "repository"); err != nil {
		t.Fatalf("discover tracked repository anchor: %v", err)
	}

	nested := filepath.Join(root, "nested")
	if err := os.Mkdir(nested, 0o700); err != nil {
		t.Fatalf("make nested directory: %v", err)
	}
	if _, err := DiscoverAnchorV3(nested, "repository"); err == nil {
		t.Fatal("repository discovery accepted a non-root selection")
	}

	untrackedRoot := t.TempDir()
	runGit(t, untrackedRoot, "init")
	if err := os.WriteFile(filepath.Join(untrackedRoot, anchorFilenameV3), []byte(validRepositoryAnchorV3), 0o600); err != nil {
		t.Fatalf("write untracked repository anchor: %v", err)
	}
	if _, err := DiscoverAnchorV3(untrackedRoot, "repository"); err == nil {
		t.Fatal("repository discovery accepted an untracked anchor")
	}
}

func TestNormalizeGitRemoteV3Dispositions(t *testing.T) {
	for _, test := range []struct {
		raw         string
		normalized  string
		disposition RemoteDisposition
	}{
		{"HTTPS://GIT.EXAMPLE.TEST/Platform//Widget.git", "git.example.test/Platform/Widget", RemoteNormalizedV3},
		{"ssh://git@GIT.EXAMPLE.TEST:2222/Platform//CaseSensitive.git", "git.example.test:2222/Platform/CaseSensitive", RemoteNormalizedV3},
		{"git@GIT.EXAMPLE.TEST:Platform/Widget.git", "git.example.test/Platform/Widget", RemoteNormalizedV3},
		{filepath.Join(t.TempDir(), "local-remote"), "", RemoteOmittedV3},
		{"file:///tmp/repository", "", RemoteOmittedV3},
		{"not a remote", "", RemoteOmittedV3},
		{"https://fixture-user:fixture-password@git.example.test/Platform/Widget.git", "", RemoteRefusedV3},
	} {
		normalized, disposition, err := NormalizeGitRemoteV3(test.raw)
		if err != nil {
			t.Fatalf("normalize remote: %v", err)
		}
		if normalized != test.normalized || disposition != test.disposition {
			t.Fatalf("NormalizeGitRemoteV3(%q) = (%q, %q), want (%q, %q)", test.raw, normalized, disposition, test.normalized, test.disposition)
		}
	}
}

func TestBuildDescriptorV3ValidatesOpaqueEvidence(t *testing.T) {
	anchor, err := ParseAnchorV3([]byte(validRepositoryAnchorV3))
	if err != nil {
		t.Fatalf("parse anchor: %v", err)
	}
	legacy := []LegacyIdentifierV3{{
		Scheme:     LegacyIdentifierSchemeV3("binding_v2"),
		Value:      "legacy-widget-42",
		Provenance: LegacyIdentifierProvenanceV3("migrated_client_configuration"),
	}}
	descriptor, err := BuildDescriptorV3(anchor, []string{"git.example.test/Platform/Widget"}, legacy, "fixture-install")
	if err != nil {
		t.Fatalf("build descriptor: %v", err)
	}
	if descriptor.Version != 3 || descriptor.AnchorProjectID != anchor.ProjectID || descriptor.Name != anchor.Name || descriptor.Scope != anchor.Scope {
		t.Fatalf("descriptor = %#v", descriptor)
	}
	encoded, err := json.Marshal(descriptor)
	if err != nil {
		t.Fatalf("marshal descriptor: %v", err)
	}
	if strings.Contains(string(encoded), "project_key") {
		t.Fatalf("descriptor exposed a client project key: %s", encoded)
	}
	emptyDescriptor, err := BuildDescriptorV3(anchor, nil, nil, "fixture-install")
	if err != nil {
		t.Fatalf("build empty descriptor: %v", err)
	}
	emptyEncoded, err := json.Marshal(emptyDescriptor)
	if err != nil {
		t.Fatalf("marshal empty descriptor: %v", err)
	}
	if strings.Contains(string(emptyEncoded), ":null") {
		t.Fatalf("descriptor encoded absent evidence as null: %s", emptyEncoded)
	}

	for _, test := range []struct {
		remotes []string
		legacy  []LegacyIdentifierV3
		client  string
	}{
		{client: ""},
		{remotes: []string{"https://git.example.test/Platform/Widget.git"}, client: "fixture-install"},
		{legacy: []LegacyIdentifierV3{{Value: "legacy", Provenance: "operator_import"}}, client: "fixture-install"},
	} {
		if _, err := BuildDescriptorV3(anchor, test.remotes, test.legacy, test.client); err == nil {
			t.Fatal("BuildDescriptorV3 accepted invalid descriptor evidence")
		}
	}
}

func runGit(t *testing.T, root string, args ...string) {
	t.Helper()
	command := exec.Command("git", append([]string{"-C", root}, args...)...)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v: %s", strings.Join(args, " "), err, output)
	}
}
