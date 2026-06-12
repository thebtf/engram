package sdk

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/thebtf/engram/pkg/models"
)

// ---------------------------------------------------------------------------
// isSelfReferentialSummary
// ---------------------------------------------------------------------------

func TestIsSelfReferentialSummary_TrueWhenMetaPhrases(t *testing.T) {
	cases := []struct {
		name    string
		summary *models.ParsedSummary
	}{
		{
			name: "memory extraction role definition",
			summary: &models.ParsedSummary{
				Request:   "Memory extraction agent role definition",
				Completed: "No work has been completed yet.",
				Learned:   "Analyze tool executions and extract meaningful observations.",
				NextSteps: "Awaiting tool results from user session.",
			},
		},
		{
			name: "session initialization no work",
			summary: &models.ParsedSummary{
				Request:   "Session initialization",
				Completed: "No work completed yet.",
				Learned:   "Awaiting user input to begin.",
				NextSteps: "Waiting for the user to provide instructions.",
			},
		},
		{
			name: "hook mechanism",
			summary: &models.ParsedSummary{
				Request:   "Hook execution for session start",
				Completed: "Hook mechanism triggered successfully.",
				Learned:   "System hooks are functioning.",
			},
		},
		{
			name: "progress checkpoint",
			summary: &models.ParsedSummary{
				Request:   "Progress checkpoint for current session",
				Completed: "Responding to progress checkpoint request.",
				Learned:   "No technical learnings yet.",
			},
		},
		{
			name: "just begun",
			summary: &models.ParsedSummary{
				Request:   "Empty session just beginning",
				Completed: "Nothing completed yet.",
				Learned:   "No substantive work has been done.",
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.True(t, isSelfReferentialSummary(tc.summary))
		})
	}
}

func TestIsSelfReferentialSummary_FalseForRealWork(t *testing.T) {
	cases := []struct {
		name    string
		summary *models.ParsedSummary
	}{
		{
			name: "JWT auth bugfix",
			summary: &models.ParsedSummary{
				Request:   "Fix JWT validation in login handler",
				Completed: "Updated auth middleware to correctly validate expiry claims.",
				Learned:   "The library requires explicit algorithm in VerifyOptions.",
				NextSteps: "Add integration test for expired token path.",
			},
		},
		{
			name: "database migration",
			summary: &models.ParsedSummary{
				Request:   "Add user profile fields migration",
				Completed: "Created migration 004_add_profile.sql with three new columns.",
				Learned:   "SQLite ALTER TABLE is limited — recreating table was necessary.",
				NextSteps: "Test rollback script against staging.",
			},
		},
		{
			name: "REST API implementation",
			summary: &models.ParsedSummary{
				Request:   "Implement /users and /posts CRUD endpoints",
				Completed: "Created chi routes with handler stubs and tests.",
				Learned:   "chi middleware chaining keeps each handler clean.",
				NextSteps: "Add authentication guard middleware.",
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.False(t, isSelfReferentialSummary(tc.summary))
		})
	}
}

// ---------------------------------------------------------------------------
// hasMeaningfulContent
// ---------------------------------------------------------------------------

func TestHasMeaningfulContent_FalseForEmpty(t *testing.T) {
	assert.False(t, hasMeaningfulContent(""))
}

func TestHasMeaningfulContent_FalseForShort(t *testing.T) {
	assert.False(t, hasMeaningfulContent("Fixed a bug and committed."))
}

func TestHasMeaningfulContent_FalseForMetaMessages(t *testing.T) {
	cases := []string{
		`SessionStart:Callback hook success: Success
The memory agent is initialized and waiting for the user.
System-reminder about available tools and their usage.
No substantive work performed in this session yet.`,
		`This is the memory extraction agent role definition.
The system expects you to analyze tool executions and extract meaningful observations.
No work has been completed yet. Awaiting tool results from the user's session.`,
	}

	for _, c := range cases {
		assert.False(t, hasMeaningfulContent(c))
	}
}

func TestHasMeaningfulContent_TrueForRealCode(t *testing.T) {
	cases := []string{
		`I've updated the handler.go file to fix the authentication bug.
The function validateToken() was not checking token expiry correctly.
I've added a check for exp claim and implemented proper error handling.
The changes have been tested and the build passes completely.`,
		`Updated database_manager.py to use connection pooling.
Changed the DB class to maintain a pool of connections.
def get_connection():
    return pool.acquire()
This reduces overhead by reusing existing connections instead.`,
	}

	for _, c := range cases {
		assert.True(t, hasMeaningfulContent(c))
	}
}

// ---------------------------------------------------------------------------
// shouldSkipTool
// ---------------------------------------------------------------------------

func TestShouldSkipTool_SkippedTools(t *testing.T) {
	skipped := []string{
		"TodoWrite", "Task", "TaskOutput",
		"Glob", "ListDir", "LS", "KillShell",
		"AskUserQuestion",
		"EnterPlanMode", "ExitPlanMode",
		"Skill", "SlashCommand",
		"Read", "Grep", "WebSearch",
	}
	for _, tool := range skipped {
		t.Run(tool, func(t *testing.T) {
			assert.True(t, shouldSkipTool(tool), "expected %q to be skipped", tool)
		})
	}
}

func TestShouldSkipTool_ProcessedTools(t *testing.T) {
	processed := []string{"Edit", "Write", "Bash", "WebFetch", "NotebookEdit", "UnknownTool"}
	for _, tool := range processed {
		t.Run(tool, func(t *testing.T) {
			assert.False(t, shouldSkipTool(tool), "expected %q NOT to be skipped", tool)
		})
	}
}

// ---------------------------------------------------------------------------
// shouldSkipTrivialOperation
// ---------------------------------------------------------------------------

func TestShouldSkipTrivialOperation_TooShortOutput(t *testing.T) {
	assert.True(t, shouldSkipTrivialOperation("Edit", "input", "short"))
}

func TestShouldSkipTrivialOperation_EditAlwaysInteresting(t *testing.T) {
	out := "Edit applied successfully. File modified with the requested changes and saved."
	assert.False(t, shouldSkipTrivialOperation("Edit", `{"file_path":"/handler.go"}`, out))
}

func TestShouldSkipTrivialOperation_WriteAlwaysInteresting(t *testing.T) {
	out := "File written successfully at /pkg/config/config.go with new content."
	assert.False(t, shouldSkipTrivialOperation("Write", `{"file_path":"/config.go"}`, out))
}

func TestShouldSkipTrivialOperation_BashInterestingCommands(t *testing.T) {
	interesting := []struct{ cmd, out string }{
		{"go build ./...", "Build completed. Binary output at ./bin/engram with size 12MB."},
		{"go test ./...", "ok  github.com/thebtf/engram/pkg/models  0.123s  coverage: 79.1%"},
		{"make build", "Compiling engram server binary with production flags done."},
		{"cargo test", "test result: ok. 45 passed; 0 failed; 0 ignored in tests."},
		{"pytest --tb=short", "=================== 12 passed, 1 warning in 0.34s ==================="},
	}
	for _, tc := range interesting {
		assert.False(t, shouldSkipTrivialOperation("Bash", `{"command":"`+tc.cmd+`"}`, tc.out),
			"expected Bash %q not to be skipped", tc.cmd)
	}
}

func TestShouldSkipTrivialOperation_BashBoringCommands(t *testing.T) {
	boring := []struct{ cmd, out string }{
		{"git status", "On branch main\nYour branch is up to date.\nnothing to commit, working tree clean"},
		{"ls -la", "total 64\ndrwxr-xr-x entries that are long enough to pass the length filter here"},
		{"pwd", "/home/user/projects/engram/some/long/path/that/is/more/than/fifty/chars"},
		{"echo hello", "Hello World output that is long enough to pass the length check here!"},
	}
	for _, tc := range boring {
		assert.True(t, shouldSkipTrivialOperation("Bash", `{"command":"`+tc.cmd+`"}`, tc.out),
			"expected Bash %q to be skipped", tc.cmd)
	}
}

func TestShouldSkipTrivialOperation_ReadAlwaysSkipped(t *testing.T) {
	// Read is on the skip whitelist regardless of content
	out := "package main\n\nimport \"fmt\"\n\nfunc main() {\n\tfmt.Println(\"Hello World\")\n}"
	assert.True(t, shouldSkipTrivialOperation("Read", `{"file_path":"/main.go"}`, out))
}

func TestShouldSkipTrivialOperation_GrepAlwaysSkipped(t *testing.T) {
	out := "main.go:10:func main() {\nmain.go:11:\tfmt.Println(\"Hello\")\n}"
	assert.True(t, shouldSkipTrivialOperation("Grep", `{"pattern":"func main"}`, out))
}

// ---------------------------------------------------------------------------
// toJSONString
// ---------------------------------------------------------------------------

func TestToJSONString_Table(t *testing.T) {
	cases := []struct {
		name  string
		input any
		want  string
	}{
		{"nil", nil, ""},
		{"string passthrough", "hello world", "hello world"},
		{"int", 99, "99"},
		{"map", map[string]string{"k": "v"}, `{"k":"v"}`},
		{"slice", []string{"a", "b"}, `["a","b"]`},
		{"bool true", true, "true"},
		{"bool false", false, "false"},
		{"float", 2.71, "2.71"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, toJSONString(tc.input))
		})
	}
}

func TestToJSONString_NestedStruct(t *testing.T) {
	input := map[string]any{"outer": map[string]string{"inner": "value"}}
	result := toJSONString(input)
	assert.Contains(t, result, "inner")
	assert.Contains(t, result, "value")
}

// ---------------------------------------------------------------------------
// hashRequest
// ---------------------------------------------------------------------------

func TestHashRequest_Length(t *testing.T) {
	h := hashRequest("Read", "input.go", "some output content here")
	assert.Len(t, h, 16)
}

func TestHashRequest_Deterministic(t *testing.T) {
	h1 := hashRequest("Edit", "same input", "same output")
	h2 := hashRequest("Edit", "same input", "same output")
	assert.Equal(t, h1, h2)
}

func TestHashRequest_DifferentInputsDifferentHash(t *testing.T) {
	h1 := hashRequest("Bash", "go build ./...", "ok")
	h2 := hashRequest("Bash", "go test ./...", "PASS")
	assert.NotEqual(t, h1, h2)
}

func TestHashRequest_OutputTruncation(t *testing.T) {
	// Only the first 1000 chars of output are hashed
	base := string(make([]byte, 1000))
	h1 := hashRequest("Read", "file", base)
	h2 := hashRequest("Read", "file", base+"extra suffix beyond the 1000 char boundary")
	assert.Equal(t, h1, h2, "output beyond 1000 chars must not affect hash")
}

// ---------------------------------------------------------------------------
// safeResolvePath
// ---------------------------------------------------------------------------

func TestSafeResolvePath_Table(t *testing.T) {
	tmpDir := t.TempDir()

	cases := []struct {
		name     string
		path     string
		cwd      string
		wantOk   bool
		wantPath string
	}{
		{
			name:     "relative in cwd",
			path:     "config.go",
			cwd:      tmpDir,
			wantOk:   true,
			wantPath: filepath.Join(tmpDir, "config.go"),
		},
		{
			name:     "nested relative",
			path:     "pkg/models/obs.go",
			cwd:      tmpDir,
			wantOk:   true,
			wantPath: filepath.Join(tmpDir, "pkg", "models", "obs.go"),
		},
		{
			name:   "parent dir traversal",
			path:   "../etc/passwd",
			cwd:    tmpDir,
			wantOk: false,
		},
		{
			name:   "deep traversal",
			path:   "../../etc/shadow",
			cwd:    tmpDir,
			wantOk: false,
		},
		{
			name:   "hidden traversal",
			path:   filepath.Join("valid", "..", "..", "..", "etc", "shadow"),
			cwd:    tmpDir,
			wantOk: false,
		},
		{
			name:   "just parent",
			path:   "..",
			cwd:    tmpDir,
			wantOk: false,
		},
		{
			name:     "absolute inside cwd",
			path:     filepath.Join(tmpDir, "inside.go"),
			cwd:      tmpDir,
			wantOk:   true,
			wantPath: filepath.Join(tmpDir, "inside.go"),
		},
		{
			name:   "absolute outside cwd",
			path:   filepath.Join(os.TempDir(), "outside", "file.go"),
			cwd:    tmpDir,
			wantOk: false,
		},
		{
			name:     "absolute no cwd",
			path:     filepath.Join(tmpDir, "abs", "path.go"),
			cwd:      "",
			wantOk:   true,
			wantPath: filepath.Join(tmpDir, "abs", "path.go"),
		},
		{
			name:     "relative no cwd",
			path:     filepath.Join("rel", "path.go"),
			cwd:      "",
			wantOk:   true,
			wantPath: filepath.Join("rel", "path.go"),
		},
		{
			name:     "dot prefix",
			path:     "./handler.go",
			cwd:      tmpDir,
			wantOk:   true,
			wantPath: filepath.Join(tmpDir, "handler.go"),
		},
		{
			name:     "cwd equals path",
			path:     tmpDir,
			cwd:      tmpDir,
			wantOk:   true,
			wantPath: tmpDir,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := safeResolvePath(tc.path, tc.cwd)
			assert.Equal(t, tc.wantOk, ok)
			if tc.wantPath != "" && ok {
				assert.Equal(t, tc.wantPath, got)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// captureFileMtimes
// ---------------------------------------------------------------------------

func TestCaptureFileMtimes_ExistingFiles(t *testing.T) {
	dir := t.TempDir()
	f1 := filepath.Join(dir, "a.go")
	f2 := filepath.Join(dir, "b.go")
	_ = os.WriteFile(f1, []byte("a"), 0644)
	_ = os.WriteFile(f2, []byte("b"), 0644)

	mtimes := captureFileMtimes([]string{f1}, []string{f2}, "")
	assert.Len(t, mtimes, 2)
	assert.Greater(t, mtimes[f1], int64(0))
	assert.Greater(t, mtimes[f2], int64(0))
}

func TestCaptureFileMtimes_NonexistentIgnored(t *testing.T) {
	mtimes := captureFileMtimes([]string{"/no/such/file.go"}, nil, "")
	assert.Empty(t, mtimes)
}

func TestCaptureFileMtimes_RelativeWithCwd(t *testing.T) {
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, "rel.go"), []byte("x"), 0644)

	mtimes := captureFileMtimes([]string{"rel.go"}, nil, dir)
	assert.Len(t, mtimes, 1)
	assert.Contains(t, mtimes, "rel.go")
}

func TestCaptureFileMtimes_DuplicatePaths(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "shared.go")
	_ = os.WriteFile(f, []byte("x"), 0644)

	// Same file in both read and modified → should deduplicate
	mtimes := captureFileMtimes([]string{f}, []string{f}, "")
	assert.Len(t, mtimes, 1)
}

func TestCaptureFileMtimes_NilInputs(t *testing.T) {
	mtimes := captureFileMtimes(nil, nil, "")
	assert.Empty(t, mtimes)
}

// ---------------------------------------------------------------------------
// GetFileMtimes
// ---------------------------------------------------------------------------

func TestGetFileMtimes_ExistingFile(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "test.go")
	_ = os.WriteFile(f, []byte("pkg"), 0644)

	mtimes := GetFileMtimes([]string{f}, "")
	assert.Len(t, mtimes, 1)
	assert.Greater(t, mtimes[f], int64(0))
}

// ---------------------------------------------------------------------------
// GetFileContent
// ---------------------------------------------------------------------------

func TestGetFileContent_ReadsExisting(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "read.go")
	_ = os.WriteFile(f, []byte("package main"), 0644)

	content, ok := GetFileContent(f, "")
	assert.True(t, ok)
	assert.Equal(t, "package main", content)
}

func TestGetFileContent_ReturnsFalseForMissing(t *testing.T) {
	_, ok := GetFileContent("/no/such/file.go", "")
	assert.False(t, ok)
}

func TestGetFileContent_TruncatesLongContent(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "big.go")
	_ = os.WriteFile(f, []byte(string(make([]byte, 3000))), 0644)

	content, ok := GetFileContent(f, "")
	assert.True(t, ok)
	assert.Contains(t, content, "[truncated]")
	assert.LessOrEqual(t, len(content), 2100)
}

func TestGetFileContent_RelativePathWithCwd(t *testing.T) {
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, "rel.go"), []byte("package rel"), 0644)

	content, ok := GetFileContent("rel.go", dir)
	assert.True(t, ok)
	assert.Equal(t, "package rel", content)
}

// ---------------------------------------------------------------------------
// RequestDeduplicator
// ---------------------------------------------------------------------------

func TestRequestDeduplicator_NewCreated(t *testing.T) {
	d := NewRequestDeduplicator(300, 500)
	assert.NotNil(t, d)
	assert.Equal(t, int64(300), d.ttlSecs)
	assert.Equal(t, 500, d.maxSize)
	assert.NotNil(t, d.seen)
}

func TestRequestDeduplicator_UnseenNotDuplicate(t *testing.T) {
	d := NewRequestDeduplicator(300, 100)
	assert.False(t, d.IsDuplicate("fresh-hash"))
}

func TestRequestDeduplicator_AfterRecordIsDuplicate(t *testing.T) {
	d := NewRequestDeduplicator(300, 100)
	d.Record("my-hash")
	assert.True(t, d.IsDuplicate("my-hash"))
}

func TestRequestDeduplicator_RecordStored(t *testing.T) {
	d := NewRequestDeduplicator(300, 100)
	d.Record("stored-hash")
	d.mu.RLock()
	_, ok := d.seen["stored-hash"]
	d.mu.RUnlock()
	assert.True(t, ok)
}

func TestRequestDeduplicator_EvictionOnFullCapacity(t *testing.T) {
	d := NewRequestDeduplicator(0, 2) // TTL=0 → all entries are "old"
	d.Record("h1")
	d.Record("h2")
	d.Record("h3") // triggers eviction
	d.mu.RLock()
	sz := len(d.seen)
	d.mu.RUnlock()
	assert.LessOrEqual(t, sz, 3)
}

func TestRequestDeduplicator_ExpiredNotDuplicate(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping time-dependent test in short mode")
	}
	d := NewRequestDeduplicator(1, 100)
	d.Record("expiry-hash")
	assert.True(t, d.IsDuplicate("expiry-hash"))

	time.Sleep(2500 * time.Millisecond)

	assert.False(t, d.IsDuplicate("expiry-hash"))
}

// ---------------------------------------------------------------------------
// Processor — callbacks and lifecycle
// ---------------------------------------------------------------------------

func TestProcessor_SetBroadcastFunc(t *testing.T) {
	p := &Processor{}
	assert.Nil(t, p.broadcastFunc)

	var gotEvent map[string]any
	p.SetBroadcastFunc(func(e map[string]any) { gotEvent = e })
	p.broadcast(map[string]any{"key": "val"})

	assert.Equal(t, "val", gotEvent["key"])
}

func TestProcessor_BroadcastNilFuncNoPanic(t *testing.T) {
	p := &Processor{}
	// Must not panic when broadcastFunc is nil
	assert.NotPanics(t, func() {
		p.broadcast(map[string]any{"type": "test"})
	})
}

func TestProcessor_SetSyncObservationFunc(t *testing.T) {
	p := &Processor{}
	var called bool
	p.SetSyncObservationFunc(func(*models.Observation) { called = true })
	assert.NotNil(t, p.syncObservationFunc)
	p.syncObservationFunc(&models.Observation{})
	assert.True(t, called)
}

func TestProcessor_SetSyncSummaryFunc(t *testing.T) {
	p := &Processor{}
	var called bool
	p.SetSyncSummaryFunc(func(*models.SessionSummary) { called = true })
	assert.NotNil(t, p.syncSummaryFunc)
	p.syncSummaryFunc(&models.SessionSummary{})
	assert.True(t, called)
}

func TestProcessor_IsAvailable(t *testing.T) {
	p := &Processor{}
	assert.True(t, p.IsAvailable())
}

// ---------------------------------------------------------------------------
// Vector sync worker pool
// ---------------------------------------------------------------------------

func TestProcessor_VectorSyncWorkers_ProcessItems(t *testing.T) {
	var mu sync.Mutex
	var synced []*models.Observation

	p := &Processor{
		vectorSyncChan: make(chan *models.Observation, MaxVectorSyncWorkers*2),
		vectorSyncDone: make(chan struct{}),
		syncObservationFunc: func(o *models.Observation) {
			mu.Lock()
			synced = append(synced, o)
			mu.Unlock()
		},
	}

	p.StartVectorSyncWorkers()
	p.vectorSyncChan <- &models.Observation{SDKSessionID: "s1"}
	p.vectorSyncChan <- &models.Observation{SDKSessionID: "s2"}
	time.Sleep(50 * time.Millisecond)
	p.StopVectorSyncWorkers()

	mu.Lock()
	assert.Len(t, synced, 2)
	mu.Unlock()
}

func TestProcessor_VectorSyncWorkers_DrainOnStop(t *testing.T) {
	var mu sync.Mutex
	count := 0

	p := &Processor{
		vectorSyncChan: make(chan *models.Observation, 10),
		vectorSyncDone: make(chan struct{}),
		syncObservationFunc: func(*models.Observation) {
			mu.Lock()
			count++
			mu.Unlock()
		},
	}

	for i := 0; i < 5; i++ {
		p.vectorSyncChan <- &models.Observation{}
	}
	p.StartVectorSyncWorkers()
	p.StopVectorSyncWorkers()

	mu.Lock()
	assert.Equal(t, 5, count)
	mu.Unlock()
}

func TestProcessor_VectorSyncWorkers_NilSyncFuncNoPanic(t *testing.T) {
	p := &Processor{
		vectorSyncChan: make(chan *models.Observation, 10),
		vectorSyncDone: make(chan struct{}),
	}
	p.StartVectorSyncWorkers()
	p.vectorSyncChan <- &models.Observation{}
	time.Sleep(50 * time.Millisecond)
	assert.NotPanics(t, p.StopVectorSyncWorkers)
}

// ---------------------------------------------------------------------------
// Constants and package-level variables
// ---------------------------------------------------------------------------

func TestMaxVectorSyncWorkers_Value(t *testing.T) {
	assert.Equal(t, 8, MaxVectorSyncWorkers)
}

func TestObservationTypes_Contents(t *testing.T) {
	want := []string{"bugfix", "feature", "refactor", "change", "discovery", "decision"}
	assert.Equal(t, want, ObservationTypes)
}

func TestObservationConcepts_Contents(t *testing.T) {
	want := []string{
		"how-it-works", "why-it-exists", "what-changed", "problem-solution",
		"gotcha", "pattern", "trade-off",
	}
	assert.Equal(t, want, ObservationConcepts)
}

// ---------------------------------------------------------------------------
// Type assertions for function types
// ---------------------------------------------------------------------------

func TestFuncTypes_Assignable(t *testing.T) {
	var _ BroadcastFunc = func(map[string]any) {}
	var _ SyncObservationFunc = func(*models.Observation) {}
	var _ SyncSummaryFunc = func(*models.SessionSummary) {}
}
