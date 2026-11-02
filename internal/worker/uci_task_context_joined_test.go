package worker

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	gormstore "github.com/thebtf/engram/internal/db/gorm"
	"github.com/thebtf/engram/internal/taskcontext"
	"github.com/thebtf/engram/internal/uci"
	muxcontrol "github.com/thebtf/mcp-mux/muxcore/control"
	gormlib "gorm.io/gorm"
)

func TestUCITaskContextJoinedProductionCallback(t *testing.T) {
	runNativeTaskContextFixture(t, true)
}

type joinedNativeFixture struct {
	binary, root, first, second, project, token, controlPath string
	environment                                              []string
	fixtures                                                 []workerUCIApplicationFixture
	store                                                    *gormstore.Store
	composition                                              *uciContextComposition
	daemonPID                                                int
}

type joinedConsumerFrame struct {
	Event             string              `json:"event"`
	Name              string              `json:"name"`
	Failed            bool                `json:"failed"`
	Error             string              `json:"error"`
	Useful            bool                `json:"useful"`
	Processes         int                 `json:"processes"`
	NativeStatus      string              `json:"native_status"`
	CallbackElapsedMS float64             `json:"callback_elapsed_ms"`
	ChildElapsedMS    float64             `json:"child_closed_elapsed_ms"`
	Packet            *taskcontext.Packet `json:"packet"`
}

func runJoinedNativeTaskContext(t *testing.T, fixture joinedNativeFixture) {
	t.Helper()
	logPath := os.Getenv("ENGRAM_TASK_CONTEXT_JOINED_LOG")
	require.True(t, filepath.IsAbs(logPath), "joined proof requires its unique absolute raw-log path")
	rawLog, err := os.OpenFile(logPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	require.NoError(t, err)
	defer rawLog.Close()
	binary, err := os.Open(fixture.binary)
	require.NoError(t, err)
	digest := sha256.New()
	_, err = io.Copy(digest, binary)
	require.NoError(t, err)
	stat, err := binary.Stat()
	require.NoError(t, err)
	require.NoError(t, binary.Close())
	sha := hex.EncodeToString(digest.Sum(nil))
	cwd, err := os.Getwd()
	require.NoError(t, err)
	sourceRoot := filepath.Clean(filepath.Join(cwd, "..", ".."))
	probe := filepath.Join(sourceRoot, "tests", "fixtures", "native-task-context-consumer.cjs")
	provider := &taskContextProviderSpy{}
	fixture.composition.application.semanticService = uci.NewSemanticService(uci.VectorProfile{}, provider, fixture.composition.projectionStore, fixture.composition.projectionStore, fixture.composition.projectionStore)
	catalogBefore := taskContextCatalogSnapshot(t, fixture.store.GetDB())
	observer, err := sql.Open("pgx", os.Getenv("DATABASE_DSN"))
	require.NoError(t, err)
	defer observer.Close()
	observer.SetMaxOpenConns(2)
	callback := fixture.store.GetDB().Callback().Query()
	delayed := false
	setDelay := func(enabled bool) {
		t.Helper()
		if delayed {
			require.NoError(t, callback.Remove("joined-native-delay-sql"))
			delayed = false
		}
		if enabled {
			require.NoError(t, callback.Before("gorm:query").Register("joined-native-delay-sql", func(query *gormlib.DB) {
				if query.Statement.TableExpr != nil && strings.Contains(query.Statement.TableExpr.SQL, "ci_checkouts") {
					query.AddError(query.Session(&gormlib.Session{NewDB: true}).WithContext(query.Statement.Context).Exec("SELECT pg_sleep(30)").Error)
				}
			}))
			delayed = true
		}
	}
	defer func() {
		if delayed {
			_ = callback.Remove("joined-native-delay-sql")
		}
	}()
	node, err := exec.LookPath("node")
	require.NoError(t, err)
	command := exec.Command(node, probe)
	command.Dir = sourceRoot
	command.Env = fixture.environment
	stdin, err := command.StdinPipe()
	require.NoError(t, err)
	stdout, err := command.StdoutPipe()
	require.NoError(t, err)
	var stderr bytes.Buffer
	command.Stderr = &stderr
	require.NoError(t, command.Start())
	frames := make(chan joinedConsumerFrame, 64)
	done := make(chan struct{})
	var processErr, errorReading error
	go func() {
		scanner := bufio.NewScanner(stdout)
		scanner.Buffer(make([]byte, 4096), 1<<20)
		for scanner.Scan() {
			line := append([]byte(nil), scanner.Bytes()...)
			if bytes.Contains(line, []byte(fixture.token)) {
				line = bytes.ReplaceAll(line, []byte(fixture.token), []byte("[REDACTED]"))
				errorReading = io.ErrUnexpectedEOF
			}
			_, _ = rawLog.Write(append(line, '\n'))
			var frame joinedConsumerFrame
			if err := json.Unmarshal(line, &frame); err != nil {
				errorReading = err
				continue
			}
			frames <- frame
		}
		if err := scanner.Err(); err != nil {
			errorReading = err
		}
		processErr = command.Wait()
		close(done)
	}()
	defer func() {
		_ = stdin.Close()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			_ = command.Process.Kill()
			<-done
		}
		if stderr.Len() > 0 {
			_, _ = rawLog.Write([]byte(strings.ReplaceAll(stderr.String(), fixture.token, "[REDACTED]")))
		}
	}()
	encoder := json.NewEncoder(stdin)
	send := func(value any) { t.Helper(); require.NoError(t, encoder.Encode(value)) }
	await := func(event, name string, timeout time.Duration) joinedConsumerFrame {
		t.Helper()
		timer := time.NewTimer(timeout)
		defer timer.Stop()
		for {
			select {
			case frame := <-frames:
				if frame.Event == "error" {
					t.Fatalf("joined consumer failed: %s", frame.Error)
				}
				if frame.Event == event && (name == "" || frame.Name == name) {
					return frame
				}
			case <-done:
				t.Fatalf("joined consumer exited before %s/%s: %v", event, name, processErr)
			case <-timer.C:
				t.Fatalf("joined consumer did not finish %s/%s", event, name)
			}
		}
	}
	contexts := make([]map[string]any, 0, 2)
	for _, fixture := range fixture.fixtures {
		ref := fixture.current.Context
		contexts = append(contexts, map[string]any{"source_id": ref.SourceID, "checkout_id": ref.CheckoutID, "view_id": ref.ViewID, "generation": ref.Generation, "profile_id": ref.AnalysisProfileID})
	}
	send(map[string]any{"type": "setup", "binary": fixture.binary, "sha256": sha, "size": stat.Size(), "pluginRoot": filepath.Join(sourceRoot, "plugin", "engram"), "pluginData": filepath.Join(fixture.root, "installation"), "first": fixture.first, "second": fixture.second, "contexts": contexts})
	await("ready", "", 5*time.Second)
	t.Logf("JOINED_INPUT binary_sha256=%s node_pid=%d daemon_pid=%d source_tree=%s trust=production_hashFile_with_owned_artifact_selection host=emulated_registered_callback", sha, command.Process.Pid, fixture.daemonPID, os.Getenv("ENGRAM_TASK_CONTEXT_JOINED_SOURCE_TREE"))
	check := func(name string, rootIndex int, prompt, expect string) joinedConsumerFrame {
		t.Helper()
		send(map[string]any{"type": "invoke", "name": name, "rootIndex": rootIndex, "session": "joined-" + name, "prompt": prompt, "expect": expect})
		frame := await("result", name, 3*time.Second)
		if frame.Failed {
			t.Errorf("joined %s failed: %s", name, frame.Error)
		}
		t.Logf("JOINED_SAMPLE name=%s callback_ms=%.3f child_closed_ms=%.3f useful=%t native=%s processes=%d failed=%t", name, frame.CallbackElapsedMS, frame.ChildElapsedMS, frame.Useful, frame.NativeStatus, frame.Processes, frame.Failed)
		if frame.Useful {
			require.NotNil(t, frame.Packet)
			response := taskcontext.Closed("ok", "OK")
			response.Packet = frame.Packet
			require.NoError(t, response.Validate())
			require.Equal(t, fixture.fixtures[rootIndex].current.Context.ViewID, (*frame.Packet.Search.Contexts)[0].ViewID)
		}
		return frame
	}
	for _, sample := range []struct {
		name  string
		root  int
		query string
	}{{"cold", 0, "Alpha"}, {"warm_main", 0, "SearchNeedle Alpha"}, {"warm_linked", 1, "Alpha"}} {
		before := workerUCIApplicationExposureCount(t, fixture.store.GetDB())
		frame := check(sample.name, sample.root, sample.query, "useful")
		if frame.Useful {
			require.Equal(t, before+3, workerUCIApplicationExposureCount(t, fixture.store.GetDB()), "actual emitted packet lacks its three durable releases")
		}
	}
	beforeEmpty := workerUCIApplicationExposureCount(t, fixture.store.GetDB())
	empty := check("empty", 0, "NoSuchSymbol4f7", "empty")
	require.Equal(t, "empty", empty.NativeStatus)
	require.Equal(t, beforeEmpty+1, workerUCIApplicationExposureCount(t, fixture.store.GetDB()))
	setDelay(true)
	beforeLate := workerUCIApplicationExposureCount(t, fixture.store.GetDB())
	late := check("late", 0, "Alpha", "deadline")
	require.False(t, late.Useful)
	setDelay(false)
	lateStoppedAt := time.Now()
	var latePending int
	for {
		err = observer.QueryRowContext(t.Context(), "SELECT count(*) FROM pg_stat_activity WHERE datname=current_database() AND state='active' AND wait_event='PgSleep' AND query='SELECT pg_sleep(30)'").Scan(&latePending)
		require.NoError(t, err)
		if latePending == 0 || time.Since(lateStoppedAt) >= 500*time.Millisecond {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	require.Zero(t, latePending, "late callback left SQL running")
	t.Logf("JOINED_LATE pending=0 observed_after_callback_ms=%.3f", float64(time.Since(lateStoppedAt).Microseconds())/1000)
	require.Equal(t, beforeLate, workerUCIApplicationExposureCount(t, fixture.store.GetDB()))
	setDelay(true)
	beforeCancel := workerUCIApplicationExposureCount(t, fixture.store.GetDB())
	send(map[string]any{"type": "invoke", "name": "cancel", "rootIndex": 0, "session": "joined-cancel", "prompt": "Alpha", "expect": "cancel"})
	await("native_spawned", "cancel", time.Second)
	var waitingPID int
	until := time.Now().Add(350 * time.Millisecond)
	for time.Now().Before(until) {
		err = observer.QueryRowContext(t.Context(), "SELECT COALESCE((SELECT pid FROM pg_stat_activity WHERE datname=current_database() AND state='active' AND wait_event='PgSleep' AND query='SELECT pg_sleep(30)' LIMIT 1),0)").Scan(&waitingPID)
		require.NoError(t, err)
		if waitingPID != 0 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	require.NotZero(t, waitingPID, "joined cancellation never reached actual delayed SQL")
	cancelledAt := time.Now()
	send(map[string]string{"type": "supersede"})
	cancelled := await("result", "cancel", time.Second)
	require.False(t, cancelled.Useful)
	if cancelled.Failed {
		t.Errorf("joined cancel failed: %s", cancelled.Error)
	}
	var pending int
	for {
		err = observer.QueryRowContext(t.Context(), "SELECT count(*) FROM pg_stat_activity WHERE pid=$1 AND state='active' AND wait_event='PgSleep'", waitingPID).Scan(&pending)
		require.NoError(t, err)
		if pending == 0 || time.Since(cancelledAt) >= 500*time.Millisecond {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	require.Zero(t, pending, "cancelled JS callback retained SQL")
	t.Logf("JOINED_CANCEL sql_stop_and_callback_ms=%.3f pending=0", float64(time.Since(cancelledAt).Microseconds())/1000)
	setDelay(false)
	require.Equal(t, beforeCancel, workerUCIApplicationExposureCount(t, fixture.store.GetDB()))
	check("next", 0, "Alpha", "useful")
	require.Equal(t, catalogBefore, taskContextCatalogSnapshot(t, fixture.store.GetDB()), "joined callbacks mutated catalog/index state")
	require.Zero(t, provider.calls.Load())
	owners, err := muxcontrol.SendWithTimeout(fixture.controlPath, muxcontrol.Request{Cmd: "list_owners"}, time.Second)
	require.NoError(t, err)
	require.True(t, owners.OK)
	var listed muxcontrol.ListOwnersResponse
	require.NoError(t, json.Unmarshal(owners.Data, &listed))
	require.Len(t, listed.Owners, 1)
	require.Zero(t, listed.Owners[0].UpstreamPID)
	send(map[string]string{"type": "finish"})
	await("finished", "", time.Second)
	require.NoError(t, stdin.Close())
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("joined consumer process did not terminate")
	}
	require.NoError(t, processErr)
	require.NoError(t, errorReading)
	t.Logf("JOINED_NATIVE_CONSUMER_END scenario_success=%t invariants=checked no_provider no_catalog_change one_native_process_per_callback no_resident_upstream; loaded_OMP_and_installed_policy_CAS_not_exercised", !t.Failed())
}
