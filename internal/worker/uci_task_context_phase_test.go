package worker

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/thebtf/engram/internal/taskcontext"
)

// This direct Go/native observation is not the production JS callback acceptance
// matrix. Internal phase measurements require the separately retained diagnostic
// patch; no production phase logger or startup flag is kept for this probe.
func TestUCITaskContextD1PhaseDiscriminator(t *testing.T) {
	runNativeTaskContextFixture(t, func(t *testing.T, fixture joinedNativeFixture) {
		logPath := os.Getenv("ENGRAM_TASK_CONTEXT_PHASE_REQUEST_LOG")
		file, err := os.OpenFile(logPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		require.NoError(t, err)
		defer file.Close()
		encoder := json.NewEncoder(file)
		before := taskContextCatalogSnapshot(t, fixture.store.GetDB())
		for index, root := range []int{0, 1, 0, 1, 0, 1} {
			cwd := fixture.first
			if root == 1 {
				cwd = fixture.second
			}
			request := taskcontext.Request{Schema: 1, HostSessionID: "d1-phase-host", Query: "Alpha", DeadlineUnixMS: time.Now().Add(470 * time.Millisecond).UnixMilli(), MaxBytes: 8000}
			raw, err := json.Marshal(request)
			require.NoError(t, err)
			command := exec.Command(fixture.binary, "task-context", "--json")
			command.Dir, command.Env, command.Stdin = cwd, fixture.environment, bytes.NewReader(raw)
			started := time.Now()
			output, err := command.Output()
			elapsed := time.Since(started)
			require.NoError(t, err)
			var response taskcontext.Response
			require.NoError(t, json.Unmarshal(output, &response))
			require.NoError(t, response.Validate())
			observation := map[string]any{"index": index, "root_index": root, "elapsed_ms": float64(elapsed.Microseconds()) / 1000, "status": response.Status, "reason": response.ReasonCode, "bytes": len(output), "useful": response.Packet != nil && response.Packet.Source != nil && response.Packet.Calls != nil}
			require.NoError(t, encoder.Encode(observation))
			t.Logf("D1_PHASE_REQUEST index=%d root=%d elapsed_ms=%.3f status=%s bytes=%d", index, root, float64(elapsed.Microseconds())/1000, response.Status, len(output))
		}
		require.Equal(t, before, taskContextCatalogSnapshot(t, fixture.store.GetDB()))
	})
}

// This fixed discriminator uses the real registered JS/hash/controller/execFile
// path. It omits only the no-match and separate late cases from the joined matrix.
func TestUCITaskContextJoinedPhaseDiscriminator(t *testing.T) {
	runNativeTaskContextFixture(t, runJoinedNativeTaskContextDiscriminator)
}
