package engramcore

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/thebtf/engram/internal/auditcontext"
	"github.com/thebtf/engram/internal/config"
	"github.com/thebtf/engram/internal/projectidentity"
	pb "github.com/thebtf/engram/proto/engram/v1"
	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

const (
	markerRecoveryID      = "11111111-1111-4111-8111-111111111111"
	markerRecoveryOtherID = "22222222-2222-4222-8222-222222222222"
	markerRecoveryToken   = "fixture-marker-recovery-token"
)

type markerRecoveryBackend struct {
	pb.UnimplementedEngramServiceServer
	mu          sync.Mutex
	reads       int
	legacyReads int
	unresolved  bool
	invalidHTTP int
	calls       []*pb.CallToolRequest
	auth        []string
}

func (b *markerRecoveryBackend) Initialize(context.Context, *pb.InitializeRequest) (*pb.InitializeResponse, error) {
	return &pb.InitializeResponse{}, nil
}

func (b *markerRecoveryBackend) CallTool(ctx context.Context, req *pb.CallToolRequest) (*pb.CallToolResponse, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.calls = append(b.calls, req)
	md, _ := metadata.FromIncomingContext(ctx)
	b.auth = append(b.auth, md.Get("authorization")...)
	if req.GetToolName() != "recall_memory" {
		return nil, status.Error(codes.Unimplemented, "unexpected tool forwarded to backend")
	}
	return &pb.CallToolResponse{ContentJson: []byte(`{"content":[{"type":"text","text":"existing-memory-sentinel"}]}`)}, nil
}

func (b *markerRecoveryBackend) serve(w http.ResponseWriter, r *http.Request) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if r.Method != http.MethodGet || r.Header.Get("Authorization") != "Bearer "+markerRecoveryToken {
		b.invalidHTTP++
		http.Error(w, "unauthorized or mutating request", http.StatusForbidden)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	switch {
	case r.URL.Path == "/api/projects/resolve" && r.URL.Query().Get("legacy_id") == "abcdef":
		b.reads++
		b.legacyReads++
		if b.unresolved {
			_, _ = w.Write([]byte(`{"status":"unresolved","reference":"urn:engram:migration:unresolved:synthetic-fixture","reason":"Existing membership is ambiguous"}`))
		} else {
			_, _ = w.Write([]byte(`{"status":"resolved","project_id":"` + markerRecoveryID + `","name":"Existing project"}`))
		}
	case r.URL.Path == "/api/projects" && r.URL.RawQuery == "":
		b.reads++
		_, _ = w.Write([]byte(`{"items":[{"project_id":"` + markerRecoveryID + `","name":"Existing project"},{"project_id":"` + markerRecoveryOtherID + `","name":"Other project"}],"unresolved":[]}`))
	default:
		b.invalidHTTP++
		http.NotFound(w, r)
	}
}

func TestProjectMarkerRecoveryExistingProjectSameSession(t *testing.T) {
	markerRecoveryExistingProjectSameSession(t, false)
}

func TestProjectMarkerRecoveryUnresolvedHistoricalMembership(t *testing.T) {
	markerRecoveryExistingProjectSameSession(t, true)
}

func markerRecoveryExistingProjectSameSession(t *testing.T, unresolved bool) {
	t.Helper()
	home := t.TempDir()
	for _, key := range []string{"HOME", "USERPROFILE", "XDG_CONFIG_HOME", "APPDATA", "LOCALAPPDATA"} {
		t.Setenv(key, home)
	}
	t.Setenv("ENGRAM_TLS_CA", "")
	backend := &markerRecoveryBackend{unresolved: unresolved}
	grpcServer := grpc.NewServer()
	pb.RegisterEngramServiceServer(grpcServer, backend)
	server := httptest.NewServer(h2c.NewHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.ProtoMajor == 2 && strings.HasPrefix(r.Header.Get("Content-Type"), "application/grpc") {
			grpcServer.ServeHTTP(w, r)
			return
		}
		backend.serve(w, r)
	}), &http2.Server{}))
	t.Cleanup(func() { grpcServer.Stop(); server.Close() })
	dispatcher, _, caller := buildV3ContractDispatcher(t, "")
	caller.Env[config.EnvServerURL] = server.URL
	caller.Env[config.EnvWorkstationToken] = markerRecoveryToken
	caller.Env[config.EnvClaudeSessionID] = "synthetic-marker-recovery-session"
	if output, err := exec.CommandContext(t.Context(), "git", "-C", caller.Cwd, "init", "--quiet", "--template=").CombinedOutput(); err != nil {
		t.Fatalf("initialize isolated Git fixture: %v: %s", err, output)
	}
	cache := filepath.Join(caller.Cwd, ".git", "engram-project.json")
	if _, err := os.Lstat(cache); !os.IsNotExist(err) {
		t.Fatalf("fixture cache is not absent: %v", err)
	}
	marker := filepath.Join(caller.Cwd, ".engram-project")
	legacy := []byte(`{"id":"abcdef","name":"historical project"}`)
	if err := os.WriteFile(marker, legacy, 0o600); err != nil {
		t.Fatal(err)
	}
	retained := filepath.Join(caller.Cwd, "retained-data.json")
	if err := os.WriteFile(retained, []byte("existing data"), 0o600); err != nil {
		t.Fatal(err)
	}

	callCtx := auditcontext.WithUCITransportSession(t.Context(), "synthetic-transport-session-a")
	call := func(id int, name string, args json.RawMessage) ([]byte, bool) {
		t.Helper()
		request, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "method": "tools/call", "params": map[string]any{"name": name, "arguments": args}})
		if err != nil {
			t.Fatal(err)
		}
		raw, err := dispatcher.HandleRequest(callCtx, caller, request)
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
		if strings.Contains(string(raw), markerRecoveryToken) || strings.Contains(string(raw), caller.Cwd) {
			t.Fatal("response disclosed caller private data")
		}
		if reply.Error != nil || reply.Result.IsError || len(reply.Result.Content) != 1 {
			return raw, false
		}
		return []byte(reply.Result.Content[0].Text), true
	}

	for index, request := range [][]byte{jsonrpcInitReq(1), jsonrpcListReq(2)} {
		raw, err := dispatcher.HandleRequest(t.Context(), caller, request)
		if err != nil {
			t.Fatal(err)
		}
		var reply struct {
			Error  any `json:"error"`
			Result struct {
				Tools []struct {
					Name        string `json:"name"`
					Description string `json:"description"`
				} `json:"tools"`
			} `json:"result"`
		}
		if err := json.Unmarshal(raw, &reply); err != nil || reply.Error != nil {
			t.Fatalf("initialization/discovery failed: %s (%v)", raw, err)
		}
		if index == 1 {
			found := false
			for _, tool := range reply.Result.Tools {
				if tool.Name == "project_context" {
					found = true
					assertMarkerActionGuidance(t, tool.Description)
					if strings.Contains(tool.Description, caller.Cwd) || strings.Contains(tool.Description, markerRecoveryToken) {
						t.Error("tool description disclosed caller private data")
					}
				}
			}
			if !found {
				t.Error("project_context missing from caller-visible discovery")
			}
		}
	}
	assertMarkerActionGuidance(t, projectidentity.RecoveryGuidance("PROJECT_MIGRATION_REQUIRED"))
	original := json.RawMessage(`{"query":"existing-memory-sentinel"}`)
	raw, ok := call(3, "recall_memory", original)
	if unresolved && ok {
		t.Fatalf("unresolved historical membership must be held: %s", raw)
	}
	if !unresolved && (!ok || !strings.Contains(string(raw), "existing-memory-sentinel")) {
		t.Fatalf("unique historical mapping must resume the original read: %s", raw)
	}
	if !unresolved {
		direct, ok := call(31, "project_context", json.RawMessage(`{"action":"repair"}`))
		if !ok || !strings.Contains(string(direct), markerRecoveryID) {
			t.Fatalf("confirmed mapping should produce an action without choosing from catalog: %s", direct)
		}
	}
	choicesRaw, ok := call(4, "project_context", json.RawMessage(`{"action":"choices"}`))
	if !ok {
		t.Fatalf("authenticated existing-project choices unavailable: %s", choicesRaw)
	}
	var choices struct {
		Choices []struct {
			ProjectID    string `json:"project_id"`
			SelectionRef string `json:"selection_ref"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(choicesRaw, &choices); err != nil {
		t.Fatalf("decode choices: %v", err)
	}
	if len(choices.Choices) != 2 {
		t.Fatalf("ambiguous catalog must retain both candidates: %s", choicesRaw)
	}
	if choices.Choices[0].ProjectID == choices.Choices[1].ProjectID || choices.Choices[0].SelectionRef == "" || choices.Choices[1].SelectionRef == "" || choices.Choices[0].SelectionRef == choices.Choices[1].SelectionRef {
		t.Fatal("ambiguous current projects require distinct explicit caller-bound choices")
	}
	var ref string
	otherFound := false
	for _, choice := range choices.Choices {
		if choice.ProjectID == markerRecoveryID {
			ref = choice.SelectionRef
		}
		if choice.ProjectID == markerRecoveryOtherID {
			otherFound = true
		}
	}
	if ref == "" || !otherFound {
		t.Fatal("authorized existing canonical choices are incomplete")
	}
	if current, err := os.ReadFile(marker); err != nil || string(current) != string(legacy) {
		t.Fatal("choices changed marker before explicit selection")
	}
	backend.mu.Lock()
	before := len(backend.calls)
	backend.mu.Unlock()
	wantBefore := 1
	if unresolved {
		wantBefore = 0
	}
	if before != wantBefore {
		t.Fatal("unexpected tool call during initial read or choices")
	}
	if raw, err := os.ReadFile(retained); err != nil || string(raw) != "existing data" {
		t.Fatal("discovery or choices modified retained historical data")
	}
	if unresolved {
		if raw, ok := call(50, "project_context", json.RawMessage(`{"action":"repair"}`)); ok {
			t.Fatalf("unresolved historical membership cannot select automatically: %s", raw)
		}
	}
	args, err := json.Marshal(map[string]string{"action": "repair", "selection_ref": ref})
	if err != nil {
		t.Fatal(err)
	}
	otherRoot := t.TempDir()
	if output, err := exec.CommandContext(t.Context(), "git", "-C", otherRoot, "init", "--quiet", "--template=").CombinedOutput(); err != nil {
		t.Fatalf("initialize isolated cross-root Git fixture: %v: %s", err, output)
	}
	caller.Cwd = otherRoot
	if raw, ok := call(51, "project_context", args); ok {
		t.Fatalf("selection reference was accepted for another root: %s", raw)
	}
	if _, err := os.Lstat(filepath.Join(otherRoot, ".engram-project")); !os.IsNotExist(err) {
		t.Fatalf("rejected cross-root selection changed other root: %v", err)
	}
	caller.Cwd = filepath.Dir(marker)
	callCtx = auditcontext.WithUCITransportSession(t.Context(), "synthetic-transport-session-b")
	if raw, ok := call(52, "project_context", args); ok {
		t.Fatalf("choice accepted in another transport session: %s", raw)
	}
	callCtx = auditcontext.WithUCITransportSession(t.Context(), "synthetic-transport-session-a")
	changed := append(append([]byte(nil), legacy...), '\n')
	if err := os.WriteFile(marker, changed, 0o600); err != nil {
		t.Fatal(err)
	}
	if raw, ok := call(53, "project_context", args); ok {
		t.Fatalf("stale marker choice accepted: %s", raw)
	}
	if current, err := os.ReadFile(marker); err != nil || string(current) != string(changed) {
		t.Fatal("stale action changed marker")
	}
	if err := os.WriteFile(marker, legacy, 0o600); err != nil {
		t.Fatal(err)
	}
	if !unresolved {
		args = json.RawMessage(`{"action":"repair"}`)
	}
	actionRaw, ok := call(5, "project_context", args)
	if !ok {
		t.Fatalf("host marker action refused: %s", actionRaw)
	}
	var action struct {
		ProjectID    string `json:"project_id"`
		Target       string `json:"target"`
		RootDigest   string `json:"caller_root_sha256"`
		Content      string `json:"marker_content"`
		Instructions string `json:"instructions"`
		Precondition struct {
			State  string `json:"state"`
			SHA256 string `json:"sha256"`
		} `json:"precondition"`
		HistoricalChanged bool `json:"historical_membership_changed"`
	}
	if err := json.Unmarshal(actionRaw, &action); err != nil {
		t.Fatal(err)
	}
	assertMarkerActionGuidance(t, action.Instructions)
	current, err := os.ReadFile(marker)
	if err != nil || string(current) != string(legacy) {
		t.Fatal("MCP handler wrote marker instead of returning a host action")
	}
	markerHash := sha256.Sum256(current)
	rootHash := sha256.Sum256([]byte(caller.Cwd))
	if action.ProjectID != markerRecoveryID || action.Target != ".engram-project" || action.RootDigest != hex.EncodeToString(rootHash[:]) || action.Precondition.State != "sha256" || action.Precondition.SHA256 != hex.EncodeToString(markerHash[:]) || action.HistoricalChanged {
		t.Fatalf("action is not bound to caller marker and canonical project: %s", actionRaw)
	}
	var portable struct {
		ProjectID string `json:"project_id"`
	}
	if err := json.Unmarshal([]byte(action.Content), &portable); err != nil || portable.ProjectID != markerRecoveryID {
		t.Fatal("action lacks valid existing canonical marker content")
	}
	// This is the test-owned host applying the returned precondition and bytes.
	if err := os.WriteFile(marker, []byte(action.Content), 0o600); err != nil {
		t.Fatal(err)
	}
	if raw, err := os.ReadFile(retained); err != nil || string(raw) != "existing data" {
		t.Fatal("repair modified retained data")
	}
	result, ok := call(6, "recall_memory", original)
	if !ok || !strings.Contains(string(result), "existing-memory-sentinel") {
		t.Fatalf("same-session original read did not resume: %s", result)
	}
	if _, err := os.Lstat(cache); !os.IsNotExist(err) {
		t.Fatalf("repair/read created derived common cache: %v", err)
	}
	dispatcher, _, _ = buildV3ContractDispatcher(t, "")
	restarted, ok := call(7, "recall_memory", original)
	if !ok || !strings.Contains(string(restarted), "existing-memory-sentinel") {
		t.Fatalf("fresh dispatcher required another repair: %s", restarted)
	}
	backend.mu.Lock()
	defer backend.mu.Unlock()
	if backend.legacyReads == 0 || backend.invalidHTTP != 0 {
		t.Fatalf("repair must use authenticated historical lookup and read-only resolution, lookups=%d reads=%d rejected=%d", backend.legacyReads, backend.reads, backend.invalidHTTP)
	}
	wantCalls := 3
	if unresolved {
		wantCalls = 2
	}
	if len(backend.calls) != wantCalls || len(backend.auth) != wantCalls {
		t.Fatalf("each successful read must be forwarded once: calls=%d", len(backend.calls))
	}
	for i, request := range backend.calls {
		if request.GetProject() != markerRecoveryID || request.GetToolName() != "recall_memory" || request.GetSessionId() != "synthetic-marker-recovery-session" || string(request.GetArgumentsJson()) != string(original) || backend.auth[i] != "Bearer "+markerRecoveryToken {
			t.Fatal("read lost its canonical project, arguments or caller authentication")
		}
	}
}

func assertMarkerActionGuidance(t *testing.T, instructions string) {
	t.Helper()
	if len(instructions) > 3072 {
		t.Errorf("marker action instructions exceed 3072 bytes: %d", len(instructions))
	}
	sentences := strings.Split(strings.ToLower(instructions), ". ")
	for _, requirement := range []struct {
		name   string
		groups [][]string
	}{
		{"caller fingerprint input and comparison", [][]string{{"caller_root_sha256"}, {"sha-256", "sha256"}, {"exact"}, {"caller cwd"}, {"utf-8", "utf8"}, {"compare"}, {"returned"}}},
		{"target fingerprint input and comparison", [][]string{{"target_root_sha256"}, {"sha-256", "sha256"}, {"utf-8", "utf8"}, {"absolute"}, {"target root"}, {"compare"}, {"returned"}}},
		{"target root derivation", [][]string{{"target root"}, {"os-native", "os native", "platform-native", "platform native"}, {"absolute"}, {"clean"}, {"walk"}, {"spelling"}, {"nearest"}, {"marker"}, {"worktree"}}},
		{"walked candidate rather than Git spelling", [][]string{{"hash"}, {"walked"}, {"candidate"}, {"not"}, {"spell"}, {"git"}, {"root"}}},
		{"relative action target", [][]string{{"slash-normalized", "slash normalized"}, {"relative"}, {"marker"}, {"target"}, {"caller cwd"}, {"separators"}, {"os-native", "os native", "platform-native", "platform native"}, {"do not hash", "never hash"}}},
		{"marker precondition digest comparison", [][]string{{"marker"}, {"precondition.state=sha256"}, {"sha-256", "sha256"}, {"raw"}, {"bytes"}, {"compare"}, {"precondition.sha256"}}},
		{"absent marker precondition", [][]string{{"precondition.state=absent"}, {"marker"}, {"absent", "absence"}, {"verify", "check"}}},
		{"v2 precondition digest comparison", [][]string{{"v2_precondition"}, {"sha-256", "sha256"}, {"raw"}, {"v2"}, {"bytes"}, {"compare"}, {"v2_precondition.sha256"}}},
		{"fingerprint normalization prohibitions", [][]string{{"do not", "never"}, {"newline"}, {"case"}, {"slash"}, {"symlink"}, {"fingerprint"}, {"after derivation"}}},
		{"fingerprints are not authority", [][]string{{"fingerprint"}, {"freshness"}, {"not"}, {"identity"}, {"authority"}}},
		{"filesystem guard and stale input", [][]string{{"guard"}, {"denial"}, {"stale"}}},
		{"safe retry only", [][]string{{"safe read"}, {"never"}, {"mutation"}}},
	} {
		found := false
		for _, sentence := range sentences {
			matched := true
			for _, alternatives := range requirement.groups {
				present := false
				for _, term := range alternatives {
					if strings.Contains(sentence, term) {
						present = true
						break
					}
				}
				if !present {
					matched = false
					break
				}
			}
			if matched {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("marker action guidance does not associate %s", requirement.name)
		}
	}
}
