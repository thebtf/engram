package worker

import (
	"context"
	"errors"
	"reflect"
	"runtime"
	"sort"
	"testing"
	"time"

	"github.com/thebtf/engram/internal/cognitive/core"
	"github.com/thebtf/engram/internal/cognitive/s1state"
	"github.com/thebtf/engram/internal/mcp"
	"github.com/thebtf/engram/pkg/cognitive"
)

// TestServiceFields_All4CoreAccessible reflects on the Service struct and
// asserts that the four explicit CORE fields plus the flagConfig field exist
// with their expected types (post-tasks-review Fix #3). Catches accidental
// removal during downstream refactors.
func TestServiceFields_All4CoreAccessible(t *testing.T) {
	// Use the pointer-to-T pattern to avoid the lock-copy vet warning that
	// fires on reflect.TypeOf(Service{}) — Service embeds sync.WaitGroup.
	typ := reflect.TypeOf((*Service)(nil)).Elem()

	wantFields := map[string]string{
		"cognitiveRegistry":       "core.SubsystemRegistry",
		"cognitiveMeter":          "core.SubsystemMeter",
		"cognitiveQueue":          "core.HintQueue",
		"cognitiveBus":            "core.AttentionEventBus",
		"cognitiveQueueLifecycle": "worker.lifecycleQueue",
		"flagConfig":              "core.FlagConfig",
	}

	for fieldName, wantTypeSuffix := range wantFields {
		f, ok := typ.FieldByName(fieldName)
		if !ok {
			t.Errorf("Service missing field %q", fieldName)
			continue
		}
		got := f.Type.String()
		// reflect prints package-qualified type names like
		// "cognitivecore.SubsystemRegistry" because we aliased the import.
		// Match by suffix so the assertion stays robust against alias changes.
		if !hasTypeSuffix(got, wantTypeSuffix) {
			t.Errorf("field %q: got type %q, want suffix %q", fieldName, got, wantTypeSuffix)
		}
	}
}

// hasTypeSuffix compares the type-name part (everything after the LAST '.')
// of actual to the type-name part of suffix. Both arguments are expected to
// be of the form `pkg.TypeName` or just `TypeName`. The strict last-dot
// split avoids false positives from substring matches against unrelated
// types whose names happen to share a tail with the target.
func hasTypeSuffix(actual, suffix string) bool {
	return lastDotPart(actual) == lastDotPart(suffix)
}

// lastDotPart returns the substring after the final '.' in s, or s itself
// when no '.' is present. Equivalent to filepath.Ext-style splitting but
// without importing the path package for a one-line helper.
func lastDotPart(s string) string {
	for i := len(s) - 1; i >= 0; i-- {
		if s[i] == '.' {
			return s[i+1:]
		}
	}
	return s
}

type fakeRegistryStateWriter struct {
	sessionCalls int
	lastSession  cognitive.SessionStateSlots
	lastID       string
}

func (f *fakeRegistryStateWriter) WriteSessionState(_ context.Context, sessionID string, slots cognitive.SessionStateSlots) error {
	f.sessionCalls++
	f.lastID = sessionID
	f.lastSession = slots
	return nil
}

func (f *fakeRegistryStateWriter) WriteProjectState(_ context.Context, _ string, _ cognitive.ProjectStateRecord) error {
	return nil
}

type fakeAdvertisedStatePlane struct{}

func (fakeAdvertisedStatePlane) WriteSessionState(context.Context, string, cognitive.SessionStateSlots) error {
	return nil
}

func (fakeAdvertisedStatePlane) WriteProjectState(context.Context, string, cognitive.ProjectStateRecord) error {
	return nil
}

func (fakeAdvertisedStatePlane) ReadSessionState(context.Context, string) (cognitive.SessionStateSlots, error) {
	return cognitive.SessionStateSlots{}, nil
}

func (fakeAdvertisedStatePlane) ReadProjectState(context.Context, string) (cognitive.ProjectStateRecord, error) {
	return cognitive.ProjectStateRecord{UpdatedBy: "agent"}, nil
}

func (fakeAdvertisedStatePlane) ReadResumePacket(context.Context, cognitive.ResumePacketRequest) (cognitive.ResumePacket, error) {
	return cognitive.ResumePacket{Source: cognitive.StatePacketSourceNative}, nil
}

func listedToolNames(tools []mcp.Tool) []string {
	names := make([]string, 0, len(tools))
	for _, tool := range tools {
		names = append(names, tool.Name)
	}
	return names
}

func TestPlatformWiring_FlagOn_S1RealStateWriterDispatchesNonNoop(t *testing.T) {
	t.Setenv("ENGRAM_V7_PLUG_ENABLED", "true")
	t.Setenv("ENGRAM_V7_S1_STATE", "true")

	cfg := core.LoadFlagConfigFromEnv()
	registry := core.NewRegistry()
	if err := core.RegisterNoOps(registry); err != nil {
		t.Fatalf("RegisterNoOps: %v", err)
	}
	activateFromFlags(t, registry, cfg)

	writer := &fakeRegistryStateWriter{}
	if err := registerS1StateWriterSubsystem(registry, writer); err != nil {
		t.Fatalf("registerS1StateWriterSubsystem: %v", err)
	}

	dispatcher := core.NewSubsystemDispatcher(registry, core.NewLocalMeter())
	want := cognitive.SessionStateSlots{Execution: map[string]interface{}{"next_action": "resume from S1"}}
	if err := core.Dispatch[cognitive.StateWriter](
		context.Background(),
		dispatcher,
		"StateWriter",
		func(w cognitive.StateWriter) error {
			return w.WriteSessionState(context.Background(), "session-1", want)
		},
	); err != nil {
		t.Fatalf("Dispatch(StateWriter): %v", err)
	}
	if writer.sessionCalls != 1 {
		t.Fatalf("real StateWriter session calls: got %d, want 1", writer.sessionCalls)
	}
	if writer.lastID != "session-1" {
		t.Fatalf("real StateWriter session id: got %q, want %q", writer.lastID, "session-1")
	}
	if !reflect.DeepEqual(writer.lastSession, want) {
		t.Fatalf("real StateWriter slots: got %#v, want %#v", writer.lastSession, want)
	}
}

func TestRegisterS1StateWriterSubsystemRejectsTypedNilWriter(t *testing.T) {
	registry := core.NewRegistry()
	var writer *fakeRegistryStateWriter

	err := registerS1StateWriterSubsystem(registry, writer)
	if !errors.Is(err, s1state.ErrNoWriter) {
		t.Fatalf("registerS1StateWriterSubsystem error = %v, want %v", err, s1state.ErrNoWriter)
	}
}

func TestShouldRegisterRealS1StateWriter(t *testing.T) {
	tests := []struct {
		name string
		plug string
		s1   string
		want bool
	}{
		{name: "master off s1 off", plug: "", s1: "", want: false},
		{name: "master off s1 on", plug: "", s1: "true", want: false},
		{name: "master on s1 off", plug: "true", s1: "", want: false},
		{name: "master on s1 on", plug: "true", s1: "true", want: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("ENGRAM_V7_PLUG_ENABLED", tt.plug)
			t.Setenv("ENGRAM_V7_S1_STATE", tt.s1)

			got := shouldRegisterRealS1StateWriter(core.LoadFlagConfigFromEnv())
			if got != tt.want {
				t.Fatalf("shouldRegisterRealS1StateWriter() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestPlatformWiring_FlagOff_StateToolsRemainAdvertisedWhenNativeStoreExists(t *testing.T) {
	t.Setenv("ENGRAM_V7_PLUG_ENABLED", "")
	t.Setenv("ENGRAM_V7_S1_STATE", "")

	srv := mcp.NewServer(mcp.ServerOptions{Version: "test"})
	srv.SetStateStore(fakeAdvertisedStatePlane{})
	names := listedToolNames(srv.ListTools())

	if !contains(names, "get_state") {
		t.Fatalf("ListTools missing get_state with native state store wired and S1 flag off: %v", names)
	}
	if !contains(names, "set_state") {
		t.Fatalf("ListTools missing set_state with native state store wired and S1 flag off: %v", names)
	}
}

func contains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

// TestPlatformWiring_NoOpsRegisteredWithCanonicalNames exercises the wiring
// shape NewService applies: build the four CORE primitives, RegisterNoOps,
// then assert the registry lists exactly the five canonical NoOp names.
// This is the unit-level proof for T014 wiring without spinning up the full
// Service (which requires DB, config, and ENGRAM_AUTH env state).
func TestPlatformWiring_NoOpsRegisteredWithCanonicalNames(t *testing.T) {
	meter := core.NewLocalMeter()
	bus := core.NewAttentionEventBus(meter)
	queue := core.NewHintQueue()
	registry := core.NewRegistry()

	if err := core.RegisterNoOps(registry); err != nil {
		t.Fatalf("RegisterNoOps: %v", err)
	}

	infos := registry.List()
	got := make([]string, 0, len(infos))
	for _, info := range infos {
		got = append(got, info.Name)
	}
	sort.Strings(got)

	want := []string{
		"core.noop.attention_event_writer",
		"core.noop.candidate_proposer",
		"core.noop.directive_distiller",
		"core.noop.hint_emitter",
		"core.noop.state_writer",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("NoOp names:\n  got:  %v\n  want: %v", got, want)
	}

	// Keep bus/queue alive references so the test exercises full wiring shape.
	_ = bus
	_ = queue
}

// TestPlatformWiring_HintQueueLifecycle exercises the Start/Stop discipline
// the worker package depends on via the unexported lifecycleQueue interface.
// Runs through the same conversion NewService applies and verifies the
// goroutine count returns to baseline after Stop.
func TestPlatformWiring_HintQueueLifecycle(t *testing.T) {
	queue := core.NewHintQueue()
	lq, ok := any(queue).(lifecycleQueue)
	if !ok {
		t.Fatal("core.NewHintQueue() does not satisfy worker.lifecycleQueue")
	}

	runtime.GC()
	time.Sleep(10 * time.Millisecond)
	baseline := runtime.NumGoroutine()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := lq.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if err := lq.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}

	// Grace window for the sweeper goroutine to drain.
	time.Sleep(100 * time.Millisecond)
	runtime.GC()

	got := runtime.NumGoroutine()
	// Allow ±3 slack for test-runner and GC goroutines.
	if got > baseline+3 {
		t.Errorf("goroutine leak: baseline=%d after-stop=%d (slack ±3)", baseline, got)
	}
}

// TestPlatformWiring_FlagOff_NoOpsRegisteredButDisabled verifies that with
// ENGRAM_V7_PLUG_ENABLED unset, NoOps still appear in the registry (state
// "registered") and FlagConfig.IsPlugEnabled returns false. This proves the
// Enable-gating discipline: registration is unconditional, but flipping
// subsystems to "enabled" depends on the flag.
func TestPlatformWiring_FlagOff_NoOpsRegisteredButDisabled(t *testing.T) {
	// Ensure no master flag in effect during this test.
	t.Setenv("ENGRAM_V7_PLUG_ENABLED", "")

	cfg := core.LoadFlagConfigFromEnv()
	if cfg.IsPlugEnabled() {
		t.Fatalf("IsPlugEnabled: got true, want false (env unset)")
	}

	registry := core.NewRegistry()
	if err := core.RegisterNoOps(registry); err != nil {
		t.Fatalf("RegisterNoOps: %v", err)
	}

	infos := registry.List()
	if got := len(infos); got != 5 {
		t.Fatalf("registered count: got %d, want 5", got)
	}
	for _, info := range infos {
		if info.State == "enabled" {
			t.Errorf("subsystem %q is enabled despite master flag off; state=%q",
				info.Name, info.State)
		}
	}
}

// activateFromFlags mirrors the FR-5 per-subsystem gating logic implemented in
// NewService. Tests use it to exercise the same decision table without
// constructing a full Service. The mapping below MUST stay in sync with
// service.go's noopsBySubsystem.
func activateFromFlags(t *testing.T, registry core.SubsystemRegistry, cfg core.FlagConfig) {
	t.Helper()
	if !cfg.IsPlugEnabled() {
		return
	}
	mapping := map[string][]string{
		"s1":  {"core.noop.state_writer"},
		"s2":  {"core.noop.candidate_proposer"},
		"s3":  {"core.noop.hint_emitter"},
		"s4a": {"core.noop.attention_event_writer", "core.noop.directive_distiller"},
	}
	for subName, noops := range mapping {
		if !cfg.IsSubsystemEnabled(subName) {
			continue
		}
		for _, n := range noops {
			if err := registry.Enable(n); err != nil {
				t.Fatalf("Enable(%s) for subsystem %s: %v", n, subName, err)
			}
		}
	}
}

// TestPlatformWiring_FlagOn_AllSubsystems_NoOpsActivated mirrors the
// "all subsystem flags enabled" path: with master + s1 + s2 + s3 + s4a all
// true, the four owner-mapped NoOps reach "enabled" and ResolveImpls returns
// them for each cross-subsystem interface.
func TestPlatformWiring_FlagOn_AllSubsystems_NoOpsActivated(t *testing.T) {
	t.Setenv("ENGRAM_V7_PLUG_ENABLED", "true")
	t.Setenv("ENGRAM_V7_S1_STATE", "true")
	t.Setenv("ENGRAM_V7_S2_METAMEM", "true")
	t.Setenv("ENGRAM_V7_S3_AMBIENT", "true")
	t.Setenv("ENGRAM_V7_S4A_DIRECTIVES_CAPTURE", "true")

	cfg := core.LoadFlagConfigFromEnv()
	registry := core.NewRegistry()
	if err := core.RegisterNoOps(registry); err != nil {
		t.Fatalf("RegisterNoOps: %v", err)
	}
	activateFromFlags(t, registry, cfg)

	infos := registry.List()
	enabled := 0
	for _, info := range infos {
		if info.State == "enabled" {
			enabled++
		}
	}
	// 5 NoOps mapped across 4 subsystems (s4a owns 2): all reach enabled.
	if enabled != 5 {
		t.Errorf("enabled NoOp count: got %d, want 5", enabled)
	}

	type implsResolver interface {
		ResolveImpls(interfaceName string) []core.Subsystem
	}
	resolver, ok := registry.(implsResolver)
	if !ok {
		t.Fatalf("registry does not expose ResolveImpls")
	}
	for _, iface := range []string{
		"CandidateProposer",
		"HintEmitter",
		"StateWriter",
		"AttentionEventWriter",
		"DirectiveDistiller",
	} {
		impls := resolver.ResolveImpls(iface)
		if len(impls) == 0 {
			t.Errorf("ResolveImpls(%s) returned no impls; expected at least the NoOp", iface)
		}
	}
}

// TestPlatformWiring_FlagOn_OnlyS2_OthersStayRegistered pins the spec FR-5
// per-subsystem invariant the previous PM review flagged: with master ON and
// only ENGRAM_V7_S2_METAMEM=true, the S2-owned NoOp (candidate_proposer)
// reaches "enabled" and ResolveImpls("CandidateProposer") returns it, but
// the other 4 NoOps stay in "registered" state and ResolveImpls for their
// interfaces returns empty.
func TestPlatformWiring_FlagOn_OnlyS2_OthersStayRegistered(t *testing.T) {
	t.Setenv("ENGRAM_V7_PLUG_ENABLED", "true")
	t.Setenv("ENGRAM_V7_S2_METAMEM", "true")
	// Explicitly ensure other per-subsystem flags are unset.
	t.Setenv("ENGRAM_V7_S1_STATE", "")
	t.Setenv("ENGRAM_V7_S3_AMBIENT", "")
	t.Setenv("ENGRAM_V7_S4A_DIRECTIVES_CAPTURE", "")
	t.Setenv("ENGRAM_V7_S4B_DIRECTIVES_SURFACING", "")
	t.Setenv("ENGRAM_V7_S5_TELEMETRY", "")
	t.Setenv("ENGRAM_V7_S6_OUTCOME", "")

	cfg := core.LoadFlagConfigFromEnv()
	registry := core.NewRegistry()
	if err := core.RegisterNoOps(registry); err != nil {
		t.Fatalf("RegisterNoOps: %v", err)
	}
	activateFromFlags(t, registry, cfg)

	stateByName := map[string]string{}
	for _, info := range registry.List() {
		stateByName[info.Name] = info.State
	}
	// Only S2 NoOp (candidate_proposer) enabled.
	if got := stateByName["core.noop.candidate_proposer"]; got != "enabled" {
		t.Errorf("core.noop.candidate_proposer state: got %q, want %q (S2 flag is set)", got, "enabled")
	}
	for _, name := range []string{
		"core.noop.hint_emitter",
		"core.noop.state_writer",
		"core.noop.attention_event_writer",
		"core.noop.directive_distiller",
	} {
		if got := stateByName[name]; got == "enabled" {
			t.Errorf("%s state: got %q, want NOT enabled (its subsystem flag is unset)", name, got)
		}
	}

	type implsResolver interface {
		ResolveImpls(interfaceName string) []core.Subsystem
	}
	resolver, ok := registry.(implsResolver)
	if !ok {
		t.Fatalf("registry does not expose ResolveImpls — concrete type changed?")
	}
	if got := len(resolver.ResolveImpls("CandidateProposer")); got != 1 {
		t.Errorf("ResolveImpls(CandidateProposer): got %d, want 1 (the S2 NoOp)", got)
	}
	for _, iface := range []string{"HintEmitter", "StateWriter", "AttentionEventWriter", "DirectiveDistiller"} {
		if got := len(resolver.ResolveImpls(iface)); got != 0 {
			t.Errorf("ResolveImpls(%s): got %d, want 0 (its subsystem flag is unset)", iface, got)
		}
	}
}
