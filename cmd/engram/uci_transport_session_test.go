package main

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/thebtf/engram/internal/auditcontext"
	"github.com/thebtf/engram/internal/module/dispatcher"
	muxcore "github.com/thebtf/mcp-mux/muxcore"
)

func TestMuxcoreInstallationNamespaceIsolatesTwoClientProcesses(t *testing.T) {
	root := t.TempDir()
	for _, key := range []string{"TMPDIR", "TMP", "TEMP"} {
		t.Setenv(key, root)
	}
	paths := make(map[string]string)
	for _, id := range []string{"operator-install-alpha", "operator-install-beta", "operator-install-alpha"} {
		cmd := exec.Command(os.Args[0], "-test.run=^TestMuxcoreInstallationNamespaceProcess$")
		cmd.Env = append(os.Environ(), "ENGRAM_NAMESPACE_TEST_HELPER=1", "ENGRAM_CLIENT_INSTANCE_ID="+id, "ENGRAM_DATA_DIR="+filepath.Join(root, id))
		output, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("installation %q: %v: %s", id, err, output)
		}
		path, _, _ := strings.Cut(string(output), "\n")
		if prior := paths[id]; prior != "" && prior != path {
			t.Fatalf("same installation changed daemon control path: %q != %q", prior, path)
		}
		paths[id] = path
	}
	if paths["operator-install-alpha"] == paths["operator-install-beta"] {
		t.Fatalf("distinct installations share daemon control path: %q", paths["operator-install-alpha"])
	}
}

func TestMuxcoreInstallationNamespaceProcess(t *testing.T) {
	if os.Getenv("ENGRAM_NAMESPACE_TEST_HELPER") == "" {
		return
	}
	if err := configureMuxcoreInstallation(); err != nil {
		t.Fatal(err)
	}
	if cfg := muxcoreShimConfig(); cfg.Namespace != muxcoreDaemonConfig(nil).Namespace {
		t.Fatal("shim and daemon selected different namespaces")
	}
	if _, err := os.Stdout.WriteString(muxcoreDaemonMarkerPath() + "\n"); err != nil {
		t.Fatal(err)
	}
}

func TestMuxcoreDaemonConfigAuthorizesFreshTransportTags(t *testing.T) {
	tags := []string{"transport-tag-one", "transport-tag-two"}
	index := 0
	cfg := muxcoreDaemonConfigWithSessionGenerator(&dispatcher.Dispatcher{}, func() (string, error) {
		tag := tags[index]
		index++
		return tag, nil
	})
	if cfg.AuthorizeSession == nil {
		t.Fatal("daemon configuration omitted AuthorizeSession")
	}
	first := cfg.AuthorizeSession(context.Background(), muxcore.ConnInfo{}, muxcore.ProjectContext{ID: "same-host-project"})
	second := cfg.AuthorizeSession(context.Background(), muxcore.ConnInfo{}, muxcore.ProjectContext{ID: "same-host-project"})
	if first.Decision != muxcore.AuthAllow || second.Decision != muxcore.AuthAllow {
		t.Fatalf("authorization decisions = %#v, %#v; want both allow", first, second)
	}
	if first.TenantID != tags[0] || second.TenantID != tags[1] || first.TenantID == second.TenantID {
		t.Fatalf("transport tags = %q, %q; want distinct generated tags", first.TenantID, second.TenantID)
	}
	if !auditcontext.ValidUCITransportSession(first.TenantID) || !auditcontext.ValidUCITransportSession(second.TenantID) {
		t.Fatalf("generated tags are not valid transport identities: %#v, %#v", first, second)
	}
}

func TestMuxcoreDaemonConfigDeniesTransportEntropyFailure(t *testing.T) {
	cfg := muxcoreDaemonConfigWithSessionGenerator(&dispatcher.Dispatcher{}, func() (string, error) {
		return "", errors.New("entropy unavailable")
	})
	verdict := cfg.AuthorizeSession(context.Background(), muxcore.ConnInfo{}, muxcore.ProjectContext{})
	if verdict.Decision != muxcore.AuthDeny || verdict.Reason != uciTransportSessionUnavailableReason || verdict.TenantID != "" {
		t.Fatalf("entropy failure verdict = %#v", verdict)
	}
}

func TestNewUCITransportSessionProducesValidOpaqueTag(t *testing.T) {
	tag, err := newUCITransportSession()
	if err != nil {
		t.Fatalf("newUCITransportSession: %v", err)
	}
	if !auditcontext.ValidUCITransportSession(tag) {
		t.Fatalf("generated invalid transport tag %q", tag)
	}
}
