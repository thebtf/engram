package worker

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"github.com/thebtf/engram/internal/auth"
	gormstore "github.com/thebtf/engram/internal/db/gorm"
	"github.com/thebtf/engram/internal/grpcserver"
	"github.com/thebtf/engram/internal/mcp"
	"github.com/thebtf/engram/internal/projectidentity"
	"github.com/thebtf/engram/internal/taskcontext"
	"github.com/thebtf/engram/pkg/models"
	muxcontrol "github.com/thebtf/mcp-mux/muxcore/control"
	"github.com/thebtf/mcp-mux/muxcore/serverid"
	"golang.org/x/crypto/bcrypt"
	gormlib "gorm.io/gorm"
)

func TestUCITaskContextNativeUsefulTwoWorktrees(t *testing.T) {
	runNativeTaskContextFixture(t, nil)
}

func TestUCITaskContextNativeIndependentKnowledgeAndCode(t *testing.T) {
	runNativeTaskContextFixture(t, func(t *testing.T, fixture joinedNativeFixture) {
		invoke := func() taskcontext.Response {
			req := taskcontext.Request{Schema: 1, HostSessionID: "compound-fixture", Query: "Alpha", DeadlineUnixMS: time.Now().Add(470 * time.Millisecond).UnixMilli(), MaxBytes: 8000}
			raw, err := json.Marshal(req)
			require.NoError(t, err)
			command := exec.Command(fixture.binary, "task-context", "--json")
			command.Dir, command.Env, command.Stdin = fixture.first, fixture.environment, bytes.NewReader(raw)
			output, err := command.Output()
			require.NoError(t, err)
			var response taskcontext.Response
			require.NoError(t, json.Unmarshal(output, &response))
			require.NoError(t, response.Validate())
			return response
		}
		code := invoke()
		require.Equal(t, "ok", code.CodeStatus)
		require.NotNil(t, code.Packet)
		require.NotNil(t, code.Knowledge)
		require.Equal(t, "unavailable", code.Knowledge.RuleStatus)
		rules := gormstore.NewBehavioralRulesStore(fixture.store)
		_, err := rules.Create(t.Context(), &models.BehavioralRule{Project: &fixture.project, Content: "Do not delete the synthetic backup 😀", Priority: 10})
		require.NoError(t, err)
		fixture.server.SetBehavioralRulesStore(rules)
		t.Setenv("ENGRAM_CODE_INTEL_ENABLED", "false")
		knowledge := invoke()
		require.Equal(t, "ok", knowledge.Status)
		require.NotEqual(t, "ok", knowledge.CodeStatus)
		require.Nil(t, knowledge.Packet)
		require.Equal(t, "ok", knowledge.Knowledge.RuleStatus)
		require.Contains(t, knowledge.Knowledge.Items[0].Content, "Do not delete")
		t.Log("qualified native CLI/daemon/gRPC/MCP/PG: released code with unavailable knowledge; ready rule with code disabled")
	})
}

func runNativeTaskContextFixture(t *testing.T, consume func(*testing.T, joinedNativeFixture)) {
	binary := os.Getenv("ENGRAM_TASK_CONTEXT_TEST_BINARY")
	if binary == "" {
		t.Skip("requires this candidate's built CLI")
	}
	t.Setenv("ENGRAM_CODE_INTEL_ENABLED", "true")
	store := openWorkerUCIContextCompositionStore(t)
	server := mcp.NewServer(mcp.ServerOptions{Version: "native-task-vertical-fixture"})
	composition, err := composeUCIContext(true, store.GetDB(), server, workerUCISemanticConfig())
	require.NoError(t, err)
	fixtures := []workerUCIApplicationFixture{newWorkerUCIApplicationFixture(t, composition), newWorkerUCIApplicationFixture(t, composition)}
	project := uuid.NewString()
	require.NoError(t, store.GetDB().Exec("INSERT INTO projects (id, display_name) VALUES (?, ?)", project, "Native task fixture").Error)
	tokens := gormstore.NewTokenStore(store)
	validator := auth.NewValidator("", tokens)
	entropy := make([]byte, auth.TokenBodyLen/2)
	_, err = rand.Read(entropy)
	require.NoError(t, err)
	token := auth.TokenRawPrefix + hex.EncodeToString(entropy[:])
	hash, err := bcrypt.GenerateFromPassword([]byte(token), bcrypt.MinCost)
	require.NoError(t, err)
	_, err = tokens.CreateWithPrincipal(t.Context(), gormstore.TokenCreatePrincipalInput{Name: "native-task-fixture", TokenHash: string(hash), TokenPrefix: token[len(auth.TokenRawPrefix) : len(auth.TokenRawPrefix)+auth.TokenPrefixLen], Scope: "read-write", Principal: fixtures[0].principal, PrincipalKind: string(auth.PrincipalKindAgent)})
	require.NoError(t, err)
	identity, err := validator.Validate(t.Context(), token)
	require.NoError(t, err)
	root := t.TempDir()
	first := filepath.Join(root, "main")
	second := filepath.Join(root, "linked")
	require.NoError(t, os.Mkdir(first, 0o700))
	git := func(args ...string) {
		t.Helper()
		command := exec.Command("git", append([]string{"-C", first}, args...)...)
		output, err := command.CombinedOutput()
		require.NoError(t, err, "git fixture: %s", output)
	}
	git("init", "--quiet", "--template=")
	marker, err := json.Marshal(map[string]any{"version": 3, "project_id": project, "name": "native task fixture", "scope": "repository"})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(first, ".engram-project"), marker, 0o600))
	require.NoError(t, os.Mkdir(filepath.Join(first, "internal"), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(first, "internal", "alpha.go"), []byte("func Alpha() { Beta(); _ = \"current-published-body SearchNeedle\" }\n"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(first, "internal", "beta.go"), []byte("func Beta() { _ = \"beta SearchNeedle\" }\n"), 0o600))
	git("add", ".")
	git("-c", "user.name=Task Fixture", "-c", "user.email=fixture@example.test", "commit", "--quiet", "-m", "synthetic native task fixture")
	git("worktree", "add", "--quiet", "--detach", second, "HEAD")
	for index, cwd := range []string{first, second} {
		_, locator, err := projectidentity.NativeGitContext(t.Context(), cwd)
		require.NoError(t, err)
		require.NoError(t, store.GetDB().Model(&gormstore.UCISource{}).Where("source_id = ?", fixtures[index].source.SourceID).Update("project_id", project).Error)
		require.NoError(t, store.GetDB().Model(&gormstore.UCICheckout{}).Where("checkout_id = ?", fixtures[index].checkout.CheckoutID).Updates(map[string]any{"registration_profile_id": fixtures[index].profile.ProfileID, "locator_ref": locator, "owner_principal": identity.Principal, "workstation_id": identity.WorkstationID()}).Error)
	}
	adapter := &mcpHandlerAdapter{mcpServer: server}
	backend, internal := grpcserver.New(adapter, validator)
	internal.SetDB(store.GetDB())
	internal.SetUCITransport(composition.transport)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	go func() { _ = backend.Serve(listener) }()
	t.Cleanup(func() { backend.Stop(); _ = listener.Close() })
	for _, part := range []string{"home", "temp", "installation", "cache"} {
		require.NoError(t, os.Mkdir(filepath.Join(root, part), 0o700))
	}
	instance := "native-task-" + uuid.NewString()
	digest := sha256.Sum256([]byte(instance))
	namespace := "engram-" + hex.EncodeToString(digest[:16])
	controlPath := serverid.DaemonControlPath(filepath.Join(root, "temp"), namespace)
	environment := make([]string, 0)
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		key = strings.ToUpper(key)
		if strings.HasPrefix(key, "ENGRAM_") || strings.HasPrefix(key, "MCP_MUX_") || strings.HasPrefix(key, "CLAUDE_") || key == "HOME" || key == "USERPROFILE" || key == "APPDATA" || key == "LOCALAPPDATA" || key == "TEMP" || key == "TMP" || key == "TMPDIR" || key == "XDG_CACHE_HOME" {
			continue
		}
		environment = append(environment, entry)
	}
	environment = append(environment, "ENGRAM_URL=http://"+listener.Addr().String(), "ENGRAM_TOKEN="+token, "ENGRAM_CLIENT_INSTANCE_ID="+instance, "ENGRAM_DATA_DIR="+filepath.Join(root, "installation"), "ENGRAM_CODE_INTEL_ENABLED=true", "HOME="+filepath.Join(root, "home"), "USERPROFILE="+filepath.Join(root, "home"), "TEMP="+filepath.Join(root, "temp"), "TMP="+filepath.Join(root, "temp"), "TMPDIR="+filepath.Join(root, "temp"), "APPDATA="+filepath.Join(root, "cache"), "LOCALAPPDATA="+filepath.Join(root, "cache"), "XDG_CACHE_HOME="+filepath.Join(root, "cache"))
	daemon := exec.Command(binary, "--muxcore-daemon")
	daemon.Dir = first
	daemon.Env = environment
	var daemonLog bytes.Buffer
	daemon.Stderr = &daemonLog
	require.NoError(t, daemon.Start())
	done := make(chan error, 1)
	go func() { done <- daemon.Wait() }()
	t.Cleanup(func() {
		reply, err := muxcontrol.SendWithTimeout(controlPath, muxcontrol.Request{Cmd: "shutdown", DrainTimeoutMs: 1000}, 2*time.Second)
		if err != nil || reply == nil || !reply.OK {
			t.Errorf("exact owned daemon shutdown failed: %v", err)
			return
		}
		select {
		case err := <-done:
			require.NoError(t, err)
		case <-time.After(5 * time.Second):
			t.Error("owned daemon did not exit")
		}
	})
	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()
	tick := time.NewTicker(30 * time.Millisecond)
	defer tick.Stop()
	for {
		if _, err := os.Stat(controlPath + ".marker.json"); err == nil {
			break
		}
		select {
		case <-tick.C:
		case <-timer.C:
			t.Fatal("owned native daemon not ready")
		}
	}
	if consume != nil {
		consume(t, joinedNativeFixture{
			binary: binary, root: root, first: first, second: second, project: project,
			token: token, controlPath: controlPath, environment: environment,
			fixtures: fixtures, store: store, composition: composition, server: server, daemonPID: daemon.Process.Pid,
		})
		return
	}
	var before []gormstore.UCICheckout
	require.NoError(t, store.GetDB().Order("checkout_id").Find(&before).Error)
	catalogBefore := taskContextCatalogSnapshot(t, store.GetDB())
	invoke := func(cwd string) (taskcontext.Response, time.Duration, []byte) {
		t.Helper()
		request := taskcontext.Request{Schema: 1, HostSessionID: "real-fixture-host", Query: "Alpha", DeadlineUnixMS: time.Now().Add(470 * time.Millisecond).UnixMilli(), MaxBytes: 8000}
		raw, err := json.Marshal(request)
		require.NoError(t, err)
		command := exec.Command(binary, "task-context", "--json")
		command.Dir, command.Env, command.Stdin = cwd, environment, bytes.NewReader(raw)
		var stderr bytes.Buffer
		command.Stderr = &stderr
		started := time.Now()
		output, err := command.Output()
		elapsed := time.Since(started)
		require.NoError(t, err, "native stderr=%s", stderr.String())
		var response taskcontext.Response
		require.NoError(t, json.Unmarshal(output, &response))
		return response, elapsed, output
	}
	cold, coldElapsed, coldOutput := invoke(first)
	t.Logf("NATIVE_COLD_OWNER elapsed_ms=%.3f bytes=%d status=%s useful=%t", float64(coldElapsed.Microseconds())/1000, len(coldOutput), cold.Status, cold.Packet != nil)
	require.NoError(t, cold.Validate())
	if coldElapsed >= 500*time.Millisecond {
		t.Errorf("cold native request exceeded total callback budget: %s", coldElapsed)
	}
	exposures := workerUCIApplicationExposureCount(t, store.GetDB())
	for index, cwd := range []string{first, second} {
		t.Run([]string{"warm_main", "warm_linked"}[index], func(t *testing.T) {
			response, elapsed, output := invoke(cwd)
			t.Logf("NATIVE_USEFUL sample=%d elapsed_ms=%.3f bytes=%d status=%s useful=%t", index, float64(elapsed.Microseconds())/1000, len(output), response.Status, response.Packet != nil && response.Packet.Source != nil && response.Packet.Calls != nil)
			require.Equal(t, "ok", response.Status, "response=%s", output)
			require.NoError(t, response.Validate())
			require.Equal(t, project, response.Project.Binding.ID)
			require.Equal(t, fixtures[index].current.Context.ViewID, (*response.Packet.Search.Contexts)[0].ViewID)
			require.NotEmpty(t, response.Packet.Calls.Graph.Edges)
			require.Contains(t, (*response.Packet.Source.Items)[0].Excerpt, "current-published-body")
			require.LessOrEqual(t, len(output), 8000)
			if elapsed >= 500*time.Millisecond {
				t.Errorf("native useful sample %d exceeded callback budget: %s", index, elapsed)
			}
			require.NotContains(t, string(output), root)
			require.NotContains(t, string(output), token)
			require.NotContains(t, string(output), "context_handle")
			actual, err := os.ReadFile(filepath.Join(cwd, ".engram-project"))
			require.NoError(t, err)
			require.Equal(t, marker, actual)
		})
	}
	if got := workerUCIApplicationExposureCount(t, store.GetDB()); got != exposures+6 {
		t.Errorf("warm exposure count = %d, want %d", got, exposures+6)
	}
	var after []gormstore.UCICheckout
	require.NoError(t, store.GetDB().Order("checkout_id").Find(&after).Error)
	require.Equal(t, before, after)
	require.Equal(t, catalogBefore, taskContextCatalogSnapshot(t, store.GetDB()), "task reads mutated catalog/index state")
	_, err = os.Stat(filepath.Join(first, ".git", "engram-project.json"))
	require.True(t, os.IsNotExist(err))
	owners, err := muxcontrol.SendWithTimeout(controlPath, muxcontrol.Request{Cmd: "list_owners"}, time.Second)
	require.NoError(t, err)
	require.True(t, owners.OK)
	var listed muxcontrol.ListOwnersResponse
	require.NoError(t, json.Unmarshal(owners.Data, &listed))
	require.Len(t, listed.Owners, 1)
	require.Zero(t, listed.Owners[0].UpstreamPID, "native SessionHandler spawned an upstream process")
	beforeCancelExposures := workerUCIApplicationExposureCount(t, store.GetDB())
	observer, err := sql.Open("pgx", os.Getenv("DATABASE_DSN"))
	require.NoError(t, err)
	defer observer.Close()
	observer.SetMaxOpenConns(2)
	// Delay the actual request-owned SELECT context in PostgreSQL, not a mock read.
	callback := store.GetDB().Callback().Query()
	require.NoError(t, callback.Before("gorm:query").Register("native-task-delay-sql", func(query *gormlib.DB) {
		if query.Statement.TableExpr != nil && strings.Contains(query.Statement.TableExpr.SQL, "ci_checkouts") {
			query.AddError(query.Session(&gormlib.Session{NewDB: true}).WithContext(query.Statement.Context).Exec("SELECT pg_sleep(30)").Error)
		}
	}))
	t.Cleanup(func() { _ = callback.Remove("native-task-delay-sql") })
	request := taskcontext.Request{Schema: 1, HostSessionID: "cancel-fixture-host", Query: "Alpha", DeadlineUnixMS: time.Now().Add(470 * time.Millisecond).UnixMilli(), MaxBytes: 8000}
	raw, err := json.Marshal(request)
	require.NoError(t, err)
	child := exec.Command(binary, "task-context", "--json")
	child.Dir, child.Env, child.Stdin = first, environment, bytes.NewReader(raw)
	var cancelOutput, cancelError bytes.Buffer
	child.Stdout, child.Stderr = &cancelOutput, &cancelError
	require.NoError(t, child.Start())
	childDone := make(chan error, 1)
	go func() { childDone <- child.Wait() }()
	waited := false
	defer func() {
		if !waited {
			_ = child.Process.Kill()
			<-childDone
		}
	}()
	var waitingPID int
	until := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(until) {
		err = observer.QueryRowContext(t.Context(), "SELECT COALESCE((SELECT pid FROM pg_stat_activity WHERE datname=current_database() AND state='active' AND wait_event='PgSleep' AND query='SELECT pg_sleep(30)' LIMIT 1),0)").Scan(&waitingPID)
		require.NoError(t, err)
		if waitingPID != 0 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if waitingPID == 0 {
		select {
		case err := <-childDone:
			waited = true
			t.Fatalf("native child did not reach blocked SQL: exit=%v output=%s stderr=%s", err, cancelOutput.String(), cancelError.String())
		default:
			t.Fatal("native child did not reach blocked SQL before observer deadline")
		}
	}
	cancelledAt := time.Now()
	require.NoError(t, child.Process.Kill(), "kill only the exact request-owned native child")
	childErr := <-childDone
	waited = true
	require.Error(t, childErr)
	var pending int
	for {
		err = observer.QueryRowContext(t.Context(), "SELECT count(*) FROM pg_stat_activity WHERE pid=$1 AND state='active' AND wait_event='PgSleep'", waitingPID).Scan(&pending)
		require.NoError(t, err)
		if pending == 0 || time.Since(cancelledAt) >= 500*time.Millisecond {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	require.Zero(t, pending, "session disconnect did not cancel its PostgreSQL read")
	require.Less(t, time.Since(cancelledAt), 500*time.Millisecond)
	sqlCancelElapsed := time.Since(cancelledAt)
	require.NoError(t, callback.Remove("native-task-delay-sql"))
	require.Equal(t, beforeCancelExposures, workerUCIApplicationExposureCount(t, store.GetDB()), "cancelled read released evidence")
	request.DeadlineUnixMS = time.Now().Add(470 * time.Millisecond).UnixMilli()
	raw, err = json.Marshal(request)
	require.NoError(t, err)
	next := exec.Command(binary, "task-context", "--json")
	next.Dir, next.Env, next.Stdin = first, environment, bytes.NewReader(raw)
	output, err := next.Output()
	require.NoError(t, err)
	var recovered taskcontext.Response
	require.NoError(t, json.Unmarshal(output, &recovered))
	require.Equal(t, "ok", recovered.Status, "shared daemon/pool did not survive request cancellation: %s", output)
	t.Logf("NATIVE_SQL_CANCEL_PASS elapsed_ms=%.3f pending=0 next=ok daemon_pid=%d", float64(sqlCancelElapsed.Microseconds())/1000, daemon.Process.Pid)
	require.Equal(t, catalogBefore, taskContextCatalogSnapshot(t, store.GetDB()), "cancellation or next request mutated catalog/index state")
}
