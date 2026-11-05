package engramcore

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/thebtf/engram/internal/config"
	pb "github.com/thebtf/engram/proto/engram/v1"
	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"
	"google.golang.org/grpc"
)

func TestProjectContextPrimaryOriginRecovery(t *testing.T) {
	for _, tc := range []struct {
		name             string
		marker           string
		remote           string
		legacyStatus     int
		legacyBody       string
		repositoryStatus int
		repositoryBody   string
		noToken          bool
		wantAction       bool
	}{
		{name: "unresolved-with-alias", marker: `{"id":"abcdef","name":"legacy"}`, legacyBody: `{"status":"unresolved","reference":"urn:fixture:unresolved"}`, wantAction: true},
		{name: "unknown-reference-with-alias", marker: `{"id":"abcdef","name":"legacy"}`, legacyStatus: 404, legacyBody: `{"error":"PROJECT_ADOPTION_UNKNOWN_REFERENCE"}`, wantAction: true},
		{name: "name-only-with-alias", marker: `{"name":"legacy label"}`, wantAction: true},
		{name: "legacy-file-origin", marker: `{"id":"abcdef","name":"legacy"}`, legacyBody: `{"status":"resolved","project_id":"11111111-1111-4111-8111-111111111111","name":"Existing"}`, remote: "file:///D:/synthetic-repository", wantAction: true},
		{name: "legacy-drive-origin", marker: `{"id":"abcdef","name":"legacy"}`, legacyBody: `{"status":"resolved","project_id":"11111111-1111-4111-8111-111111111111","name":"Existing"}`, remote: "D:/synthetic-repository", wantAction: true},
		{name: "legacy-relative-origin", marker: `{"id":"abcdef","name":"legacy"}`, legacyBody: `{"status":"resolved","project_id":"11111111-1111-4111-8111-111111111111","name":"Existing"}`, remote: "some/repo.git", wantAction: true},
		{name: "legacy-unsupported-origin", marker: `{"id":"abcdef","name":"legacy"}`, legacyBody: `{"status":"resolved","project_id":"11111111-1111-4111-8111-111111111111","name":"Existing"}`, remote: "ftp://example.invalid/synthetic.git", wantAction: true},
		{name: "auth-disabled-alias", marker: `{"name":"legacy label"}`, noToken: true, wantAction: true},
		{name: "conflicting-mappings", marker: `{"id":"abcdef","name":"legacy"}`, legacyBody: `{"status":"resolved","project_id":"22222222-2222-4222-8222-222222222222","name":"Other"}`},
		{name: "other-legacy-404", marker: `{"id":"abcdef","name":"legacy"}`, legacyStatus: 404, legacyBody: `{"error":"PROJECT_ADOPTION_STALE"}`},
		{name: "origin-unavailable", marker: `{"name":"legacy label"}`, repositoryStatus: 503},
		{name: "origin-conflict", marker: `{"name":"legacy label"}`, repositoryBody: `{"status":"conflict"}`},
		{name: "origin-removed-binding", marker: `{"name":"legacy label"}`, repositoryBody: `{"status":"conflict","binding":{"id":"11111111-1111-4111-8111-111111111111","project_key":null,"identity_status":null,"match":"manual_alias","removed":true}}`},
		{name: "origin-migration-required", marker: `{"name":"legacy label"}`, repositoryBody: `{"status":"migration_required","binding":{"id":"abcdef","project_key":null,"identity_status":null,"match":"legacy_marker"}}`},
		{name: "origin-not-in-authorized-catalog", marker: `{"name":"legacy label"}`, repositoryBody: `{"status":"resolved","binding":{"id":"33333333-3333-4333-8333-333333333333","project_key":null,"identity_status":null,"match":"manual_alias"}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("ENGRAM_TLS_CA", "")
			t.Setenv("ENGRAM_TOKEN", "")
			root := t.TempDir()
			remote := tc.remote
			if remote == "" {
				remote = "https://example.test/Fixture/Repo.git"
			}
			for _, args := range [][]string{{"init", "--quiet", "--template="}, {"remote", "add", "origin", remote}} {
				command := exec.CommandContext(t.Context(), "git", append([]string{"-C", root}, args...)...)
				if output, err := command.CombinedOutput(); err != nil {
					t.Fatalf("git fixture: %v: %s", err, output)
				}
			}
			path := filepath.Join(root, ".engram-project")
			if err := os.WriteFile(path, []byte(tc.marker), 0o600); err != nil {
				t.Fatal(err)
			}
			child := filepath.Join(root, "nested")
			if err := os.Mkdir(child, 0o700); err != nil {
				t.Fatal(err)
			}
			grpcServer := grpc.NewServer()
			grpcBackend := &markerRecoveryBackend{}
			pb.RegisterEngramServiceServer(grpcServer, grpcBackend)
			server := httptest.NewServer(h2c.NewHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.ProtoMajor == 2 && strings.HasPrefix(r.Header.Get("Content-Type"), "application/grpc") {
					grpcServer.ServeHTTP(w, r)
					return
				}
				expected := "Bearer " + markerRecoveryToken
				if tc.noToken {
					expected = ""
				}
				if r.Method != http.MethodGet || r.Header.Get("Authorization") != expected {
					t.Error("wrong backend method or credentials")
					w.WriteHeader(http.StatusUnauthorized)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				switch {
				case r.URL.Path == "/api/projects":
					_, _ = w.Write([]byte(`{"items":[{"project_id":"` + markerRecoveryID + `","name":"Existing"},{"project_id":"` + markerRecoveryOtherID + `","name":"Other"}],"unresolved":[]}`))
				case r.URL.Path == "/api/projects/resolve" && r.URL.Query().Get("legacy_id") == "abcdef":
					if tc.legacyStatus != 0 {
						w.WriteHeader(tc.legacyStatus)
					}
					_, _ = w.Write([]byte(tc.legacyBody))
				case r.URL.Path == "/api/projects/resolve" && r.URL.Query().Get("repository_locator") == "example.test/Fixture/Repo":
					if tc.repositoryStatus != 0 {
						w.WriteHeader(tc.repositoryStatus)
					}
					body := tc.repositoryBody
					if body == "" {
						body = `{"status":"resolved","binding":{"id":"` + markerRecoveryID + `","project_key":null,"identity_status":null,"match":"manual_alias"}}`
					}
					_, _ = w.Write([]byte(body))
				default:
					t.Error("unexpected selector or backend route")
					w.WriteHeader(http.StatusBadRequest)
				}
			}), &http2.Server{}))
			t.Cleanup(func() { grpcServer.Stop(); server.Close() })
			dispatcher, _, caller := buildV3ContractDispatcher(t, "")
			caller.Cwd = child
			caller.Env[config.EnvServerURL] = server.URL
			if !tc.noToken {
				caller.Env[config.EnvWorkstationToken] = markerRecoveryToken
			}
			if tc.name == "name-only-with-alias" || tc.name == "origin-unavailable" {
				read := []byte(`{"jsonrpc":"2.0","id":10,"method":"tools/call","params":{"name":"recall_memory","arguments":{"query":"existing-memory-sentinel"}}}`)
				wire, err := dispatcher.HandleRequest(t.Context(), caller, read)
				if err != nil || !strings.Contains(string(wire), "PROJECT_CONTEXT_REQUIRED") {
					t.Fatalf("name-only read was not held: %s %v", wire, err)
				}
			}
			request := []byte(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"project_context","arguments":{"action":"repair"}}}`)
			raw, err := dispatcher.HandleRequest(t.Context(), caller, request)
			if err != nil {
				t.Fatal(err)
			}
			var reply struct {
				Error  any `json:"error"`
				Result struct {
					IsError bool `json:"isError"`
					Content []struct {
						Text string `json:"text"`
					} `json:"content"`
				} `json:"result"`
			}
			if err := json.Unmarshal(raw, &reply); err != nil {
				t.Fatal(err)
			}
			success := reply.Error == nil && !reply.Result.IsError && len(reply.Result.Content) == 1
			if success != (tc.wantAction || tc.name == "conflicting-mappings" || tc.name == "origin-conflict") {
				t.Fatalf("action result mismatch: %s", raw)
			}
			if tc.wantAction {
				var action struct {
					ProjectID        string `json:"project_id"`
					Target           string `json:"target"`
					TargetRootDigest string `json:"target_root_sha256"`
				}
				if err := json.Unmarshal([]byte(reply.Result.Content[0].Text), &action); err != nil || action.ProjectID != markerRecoveryID || action.Target != "../.engram-project" || action.TargetRootDigest == "" {
					t.Fatalf("wrong bounded worktree action: %s", raw)
				}
			}
			if strings.Contains(string(raw), root) {
				t.Fatal("caller root leaked")
			}
			if current, err := os.ReadFile(path); err != nil || string(current) != tc.marker {
				t.Fatal("handler changed marker")
			}
			if tc.name == "name-only-with-alias" {
				var action struct {
					Content      string `json:"marker_content"`
					Precondition struct {
						SHA256 string `json:"sha256"`
					} `json:"precondition"`
				}
				if err := json.Unmarshal([]byte(reply.Result.Content[0].Text), &action); err != nil || action.Content == "" || action.Precondition.SHA256 == "" {
					t.Fatalf("missing host action: %s", raw)
				}
				current, err := os.ReadFile(path)
				sum := sha256.Sum256(current)
				if err != nil || hex.EncodeToString(sum[:]) != action.Precondition.SHA256 {
					t.Fatal("stale host action")
				}
				if err := os.WriteFile(path, []byte(action.Content), 0o600); err != nil {
					t.Fatal(err)
				}
				read := []byte(`{"jsonrpc":"2.0","id":11,"method":"tools/call","params":{"name":"recall_memory","arguments":{"query":"existing-memory-sentinel"}}}`)
				wire, err := dispatcher.HandleRequest(t.Context(), caller, read)
				if err != nil || !strings.Contains(string(wire), "existing-memory-sentinel") {
					t.Fatalf("safe recall did not resume: %s %v", wire, err)
				}
				grpcBackend.mu.Lock()
				if len(grpcBackend.calls) != 1 || grpcBackend.calls[0].GetProject() != markerRecoveryID {
					t.Error("recall lost canonical binding")
				}
				grpcBackend.mu.Unlock()
			}
			if tc.name == "conflicting-mappings" || tc.name == "origin-conflict" {
				if !strings.Contains(string(raw), "host conversation") || !strings.Contains(string(raw), "user_input_required") || !strings.Contains(string(raw), "resume_action") {
					t.Fatalf("conflict lacks public same-conversation continuation: %s", raw)
				}
				call := func(id int, tool string, arguments any) ([]byte, bool) {
					t.Helper()
					request, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "method": "tools/call", "params": map[string]any{"name": tool, "arguments": arguments}})
					if err != nil {
						t.Fatal(err)
					}
					wire, err := dispatcher.HandleRequest(t.Context(), caller, request)
					if err != nil {
						t.Fatal(err)
					}
					var result struct {
						Error  any `json:"error"`
						Result struct {
							IsError bool `json:"isError"`
							Content []struct {
								Text string `json:"text"`
							} `json:"content"`
						} `json:"result"`
					}
					if err := json.Unmarshal(wire, &result); err != nil {
						t.Fatal(err)
					}
					if result.Error != nil || result.Result.IsError || len(result.Result.Content) != 1 {
						return wire, false
					}
					return []byte(result.Result.Content[0].Text), true
				}
				choicesRaw, ok := call(2, "project_context", map[string]string{"action": "choices"})
				if !ok {
					t.Fatalf("conflict choices unavailable: %s", choicesRaw)
				}
				var choices struct {
					Conflict                   bool   `json:"mapping_conflict"`
					OriginCandidatesIdentified bool   `json:"origin_candidates_identified"`
					Question                   string `json:"question"`
					Choices                    []struct {
						ProjectID    string `json:"project_id"`
						SelectionRef string `json:"selection_ref"`
						Provenance   string `json:"provenance"`
					} `json:"choices"`
				}
				if err := json.Unmarshal(choicesRaw, &choices); err != nil || !choices.Conflict || choices.Question == "" || len(choices.Choices) != 2 {
					t.Fatalf("missing public conflict options: %s", choicesRaw)
				}
				refs := make(map[string]string)
				selectedRef := ""
				for _, item := range choices.Choices {
					refs[item.Provenance] = item.SelectionRef
					if item.ProjectID == markerRecoveryID {
						selectedRef = item.SelectionRef
					}
				}
				if tc.name == "conflicting-mappings" && (refs["legacy_mapping"] == "" || refs["primary_origin"] == "") {
					t.Fatalf("missing conflict provenance: %s", choicesRaw)
				}
				if tc.name == "origin-conflict" && (choices.OriginCandidatesIdentified || refs["authorized_catalog"] == "" || !strings.Contains(choices.Question, "did not identify")) {
					t.Fatalf("origin ambiguity misrepresented: %s", choicesRaw)
				}
				if selectedRef == "" {
					t.Fatalf("selected catalog candidate missing: %s", choicesRaw)
				}
				args := map[string]string{"action": "repair", "selection_ref": selectedRef}
				changed := tc.marker + "\n"
				if err := os.WriteFile(path, []byte(changed), 0o600); err != nil {
					t.Fatal(err)
				}
				if wire, ok := call(3, "project_context", args); ok {
					t.Fatalf("stale conflict choice accepted: %s", wire)
				}
				if err := os.WriteFile(path, []byte(tc.marker), 0o600); err != nil {
					t.Fatal(err)
				}
				actionRaw, ok := call(4, "project_context", args)
				if !ok {
					t.Fatalf("explicit conflict choice refused: %s", actionRaw)
				}
				var action struct {
					ProjectID    string `json:"project_id"`
					Content      string `json:"marker_content"`
					Precondition struct {
						SHA256 string `json:"sha256"`
					} `json:"precondition"`
				}
				if err := json.Unmarshal(actionRaw, &action); err != nil || action.ProjectID != markerRecoveryID || action.Content == "" || action.Precondition.SHA256 == "" {
					t.Fatalf("invalid selected host action: %s", actionRaw)
				}
				current, err := os.ReadFile(path)
				sum := sha256.Sum256(current)
				if err != nil || hex.EncodeToString(sum[:]) != action.Precondition.SHA256 {
					t.Fatal("marker action is stale")
				}
				if err := os.WriteFile(path, []byte(action.Content), 0o600); err != nil {
					t.Fatal(err)
				}
				memory, ok := call(5, "recall_memory", map[string]string{"query": "existing-memory-sentinel"})
				if !ok || !strings.Contains(string(memory), "existing-memory-sentinel") {
					t.Fatalf("safe read did not resume: %s", memory)
				}
				grpcBackend.mu.Lock()
				if len(grpcBackend.calls) != 1 || grpcBackend.calls[0].GetProject() != markerRecoveryID {
					t.Error("safe read lost selected canonical binding")
				}
				grpcBackend.mu.Unlock()
			}
			if _, err := os.Lstat(filepath.Join(root, ".git", "engram-project.json")); !os.IsNotExist(err) {
				t.Fatalf("handler created derived cache: %v", err)
			}
		})
	}
}

func TestProjectContextBackendAuthorizationRefusal(t *testing.T) {
	for _, phase := range []string{"historical", "repository", "catalog"} {
		for _, code := range []int{http.StatusUnauthorized, http.StatusForbidden} {
			t.Run(fmt.Sprintf("%s-%d", phase, code), func(t *testing.T) {
				t.Setenv("ENGRAM_TLS_CA", "")
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.Header.Get("Authorization") != "Bearer "+markerRecoveryToken {
						w.WriteHeader(http.StatusForbidden)
						return
					}
					switch {
					case r.URL.Path == "/api/projects/resolve" && r.URL.Query().Get("legacy_id") == "abcdef":
						if phase == "historical" {
							w.WriteHeader(code)
							return
						}
						_, _ = w.Write([]byte(`{"status":"resolved","project_id":"` + markerRecoveryID + `","name":"Existing"}`))
					case r.URL.Path == "/api/projects/resolve" && r.URL.Query().Get("repository_locator") == "example.test/Synthetic/Repo":
						if phase == "repository" {
							w.WriteHeader(code)
							return
						}
						t.Error("unexpected origin request")
					case r.URL.Path == "/api/projects":
						if phase == "catalog" {
							w.WriteHeader(code)
							return
						}
						_, _ = w.Write([]byte(`{"items":[],"unresolved":[]}`))
					default:
						t.Error("unexpected recovery route")
						w.WriteHeader(http.StatusBadRequest)
					}
				}))
				defer server.Close()
				dispatcher, _, caller := buildV3ContractDispatcher(t, "")
				caller.Env[config.EnvServerURL] = server.URL
				caller.Env[config.EnvWorkstationToken] = markerRecoveryToken
				if out, err := exec.CommandContext(t.Context(), "git", "-C", caller.Cwd, "init", "--quiet", "--template=").CombinedOutput(); err != nil {
					t.Fatalf("git: %v %s", err, out)
				}
				marker := `{"id":"abcdef","name":"legacy"}`
				if phase == "repository" {
					marker = `{"name":"legacy"}`
					if out, err := exec.CommandContext(t.Context(), "git", "-C", caller.Cwd, "remote", "add", "origin", "https://example.test/Synthetic/Repo.git").CombinedOutput(); err != nil {
						t.Fatalf("git origin: %v %s", err, out)
					}
				}
				if err := os.WriteFile(filepath.Join(caller.Cwd, ".engram-project"), []byte(marker), 0o600); err != nil {
					t.Fatal(err)
				}
				raw, err := dispatcher.HandleRequest(t.Context(), caller, []byte(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"project_context","arguments":{"action":"repair"}}}`))
				if err != nil {
					t.Fatal(err)
				}
				want := "PROJECT_AUTH_REQUIRED"
				if code == http.StatusForbidden {
					want = "PROJECT_ACCESS_DENIED"
				}
				if !strings.Contains(string(raw), want) || !strings.Contains(string(raw), "keycard") || !strings.Contains(string(raw), "host conversation") || strings.Contains(string(raw), markerRecoveryToken) || strings.Contains(string(raw), caller.Cwd) {
					t.Fatalf("authorization refusal lacks safe actionable guidance: %s", raw)
				}
			})
		}
	}
}
