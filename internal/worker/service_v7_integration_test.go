package worker

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"runtime"
	"sort"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/thebtf/engram/internal/auth"
	"github.com/thebtf/engram/internal/cognitive/core"
	"github.com/thebtf/engram/internal/cognitive/s1state"
	"github.com/thebtf/engram/internal/cognitive/s2meta"
	"github.com/thebtf/engram/internal/cognitive/s4directives"
	"github.com/thebtf/engram/internal/cognitive/s5"
	"github.com/thebtf/engram/internal/cognitive/s6"
	gormdb "github.com/thebtf/engram/internal/db/gorm"
	"github.com/thebtf/engram/internal/mcp"
	"github.com/thebtf/engram/pkg/cognitive"
	"github.com/thebtf/engram/pkg/models"
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

// activateFromFlags mirrors the shared worker flag-gated CORE fallback activation.
func activateFromFlags(t *testing.T, registry core.SubsystemRegistry, cfg core.FlagConfig) {
	t.Helper()
	if err := enableFlaggedCoreNoOps(registry, cfg); err != nil {
		t.Fatalf("enableFlaggedCoreNoOps: %v", err)
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

type fakeS2MetaIndex struct {
	queries []gormdb.MetaIndexQuery
	hits    []gormdb.MetaIndexHit
}

var _ s2meta.MetaIndex = (*fakeS2MetaIndex)(nil)

func (f *fakeS2MetaIndex) QueryMetaIndex(ctx context.Context, query gormdb.MetaIndexQuery) ([]gormdb.MetaIndexHit, error) {
	f.queries = append(f.queries, query)
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return append([]gormdb.MetaIndexHit(nil), f.hits...), nil
}

func TestShouldRegisterRealS2CandidateProposer(t *testing.T) {
	tests := []struct {
		name string
		plug string
		s2   string
		want bool
	}{
		{name: "master off s2 off", plug: "", s2: "", want: false},
		{name: "master off s2 on", plug: "", s2: "true", want: false},
		{name: "master on s2 off", plug: "true", s2: "", want: false},
		{name: "master on s2 on", plug: "true", s2: "true", want: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("ENGRAM_V7_PLUG_ENABLED", tt.plug)
			t.Setenv("ENGRAM_V7_S2_METAMEM", tt.s2)

			got := shouldRegisterRealS2CandidateProposer(core.LoadFlagConfigFromEnv())
			if got != tt.want {
				t.Fatalf("shouldRegisterRealS2CandidateProposer() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestPlatformWiring_FlagOn_S2RealCandidateProposerReplacesNoOp(t *testing.T) {
	t.Setenv("ENGRAM_V7_PLUG_ENABLED", "true")
	t.Setenv("ENGRAM_V7_S2_METAMEM", "true")
	t.Setenv("ENGRAM_V7_S1_STATE", "")
	t.Setenv("ENGRAM_V7_S3_AMBIENT", "")
	t.Setenv("ENGRAM_V7_S4A_DIRECTIVES_CAPTURE", "")

	registry := core.NewRegistry()
	if err := core.RegisterNoOps(registry); err != nil {
		t.Fatalf("RegisterNoOps: %v", err)
	}
	activateFromFlags(t, registry, core.LoadFlagConfigFromEnv())

	idx := &fakeS2MetaIndex{hits: []gormdb.MetaIndexHit{{
		ID:        501,
		Title:     "Ambient candidate handoff lesson",
		Tags:      []string{"ambient", "handoff"},
		CreatedAt: time.Unix(1700010000, 0).UTC(),
		Score:     0.92,
		Source:    "s2.meta_index",
		Reason:    "tag:handoff",
	}}}
	if err := registerS2CandidateProposerSubsystem(registry, idx); err != nil {
		t.Fatalf("registerS2CandidateProposerSubsystem: %v", err)
	}

	resolver, ok := registry.(interface {
		ResolveImpls(interfaceName string) []core.Subsystem
	})
	if !ok {
		t.Fatalf("registry does not expose ResolveImpls")
	}
	impls := resolver.ResolveImpls("CandidateProposer")
	if len(impls) != 1 {
		t.Fatalf("ResolveImpls(CandidateProposer): got %d impls %v, want exactly the real S2 proposer", len(impls), namesOfSubsystems(impls))
	}
	if got := impls[0].Name(); got != "engram.s2.meta_memory" {
		t.Fatalf("resolved CandidateProposer = %q, want real S2 proposer instead of CORE NoOp", got)
	}

	dispatcher := core.NewSubsystemDispatcher(registry, core.NewLocalMeter())
	var proposals []cognitive.HintProposal
	if err := core.Dispatch[cognitive.CandidateProposer](
		context.Background(),
		dispatcher,
		"CandidateProposer",
		func(p cognitive.CandidateProposer) error {
			var err error
			proposals, err = p.Propose(context.Background(), cognitive.AttentionEvent{
				Type:    "user_prompt_submit",
				Project: "engram",
				Payload: map[string]interface{}{"text": "handoff lesson"},
			}, 1)
			return err
		},
	); err != nil {
		t.Fatalf("Dispatch(CandidateProposer): %v", err)
	}
	if len(proposals) != 1 {
		t.Fatalf("real S2 CandidateProposer returned %d proposals, want 1 meta-memory candidate", len(proposals))
	}
	if proposals[0].Title != "Ambient candidate handoff lesson" || proposals[0].Source != "s2.meta_index" {
		t.Fatalf("proposal = %#v, want meta-index title/source from real S2 proposer", proposals[0])
	}
}

func TestPlatformWiring_FlagOff_S2CandidateProposerStaysNoOp(t *testing.T) {
	t.Setenv("ENGRAM_V7_PLUG_ENABLED", "true")
	t.Setenv("ENGRAM_V7_S2_METAMEM", "")
	t.Setenv("ENGRAM_V7_S1_STATE", "")
	t.Setenv("ENGRAM_V7_S3_AMBIENT", "")
	t.Setenv("ENGRAM_V7_S4A_DIRECTIVES_CAPTURE", "")

	cfg := core.LoadFlagConfigFromEnv()
	if shouldRegisterRealS2CandidateProposer(cfg) {
		t.Fatalf("shouldRegisterRealS2CandidateProposer = true, want false when S2 flag is disabled")
	}
	registry := core.NewRegistry()
	if err := core.RegisterNoOps(registry); err != nil {
		t.Fatalf("RegisterNoOps: %v", err)
	}
	activateFromFlags(t, registry, cfg)

	resolver, ok := registry.(interface {
		ResolveImpls(interfaceName string) []core.Subsystem
	})
	if !ok {
		t.Fatalf("registry does not expose ResolveImpls")
	}
	impls := resolver.ResolveImpls("CandidateProposer")
	if len(impls) != 1 {
		t.Fatalf("ResolveImpls(CandidateProposer): got %d impls %v, want the CORE NoOp fallback when S2 is disabled", len(impls), namesOfSubsystems(impls))
	}
	if got := impls[0].Name(); got != "core.noop.candidate_proposer" {
		t.Fatalf("resolved CandidateProposer = %q, want CORE NoOp fallback while S2 is disabled", got)
	}
}

func TestPlatformWiring_T014_S2ToggleMatrixControlsRealProposerAndSiblings(t *testing.T) {
	tests := []struct {
		name               string
		masterFlag         string
		s2Flag             string
		wantRealRegistered bool
		wantCandidateNames []string
		wantRealProposal   bool
	}{
		{
			name:               "master disabled suppresses real s2 even when s2 flag is set",
			masterFlag:         "false",
			s2Flag:             "true",
			wantRealRegistered: false,
			wantCandidateNames: []string{},
		},
		{
			name:               "s2 disabled keeps core candidate noop fallback",
			masterFlag:         "true",
			s2Flag:             "false",
			wantRealRegistered: false,
			wantCandidateNames: []string{"core.noop.candidate_proposer"},
		},
		{
			name:               "master and s2 enabled replace noop with real proposer only",
			masterFlag:         "true",
			s2Flag:             "true",
			wantRealRegistered: true,
			wantCandidateNames: []string{"engram.s2.meta_memory"},
			wantRealProposal:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("ENGRAM_V7_PLUG_ENABLED", tt.masterFlag)
			t.Setenv("ENGRAM_V7_S2_METAMEM", tt.s2Flag)
			t.Setenv("ENGRAM_V7_S1_STATE", "false")
			t.Setenv("ENGRAM_V7_S3_AMBIENT", "false")
			t.Setenv("ENGRAM_V7_S4A_DIRECTIVES_CAPTURE", "false")
			t.Setenv("ENGRAM_V7_S4B_DIRECTIVES_SURFACING", "false")
			t.Setenv("ENGRAM_V7_S5_TELEMETRY", "false")
			t.Setenv("ENGRAM_V7_S6_OUTCOME", "false")

			cfg := core.LoadFlagConfigFromEnv()
			if got := shouldRegisterRealS2CandidateProposer(cfg); got != tt.wantRealRegistered {
				t.Fatalf("shouldRegisterRealS2CandidateProposer() = %v, want %v", got, tt.wantRealRegistered)
			}

			registry := core.NewRegistry()
			if err := core.RegisterNoOps(registry); err != nil {
				t.Fatalf("RegisterNoOps: %v", err)
			}
			activateFromFlags(t, registry, cfg)

			idx := &fakeS2MetaIndex{hits: []gormdb.MetaIndexHit{{
				ID:        901,
				Title:     "T014 real S2 proposer hit",
				Tags:      []string{"t014", "s2"},
				CreatedAt: time.Unix(1700020000, 0).UTC(),
				Score:     0.99,
				Source:    "s2.meta_index",
				Reason:    "toggle contract",
			}}}
			if tt.wantRealRegistered {
				if err := registerS2CandidateProposerSubsystem(registry, idx); err != nil {
					t.Fatalf("registerS2CandidateProposerSubsystem: %v", err)
				}
			}

			resolver, ok := registry.(interface {
				ResolveImpls(interfaceName string) []core.Subsystem
			})
			if !ok {
				t.Fatalf("registry does not expose ResolveImpls")
			}
			candidateImpls := resolver.ResolveImpls("CandidateProposer")
			if got := namesOfSubsystems(candidateImpls); !reflect.DeepEqual(got, tt.wantCandidateNames) {
				t.Fatalf("ResolveImpls(CandidateProposer) = %v, want %v", got, tt.wantCandidateNames)
			}
			for _, iface := range []string{"HintEmitter", "StateWriter", "AttentionEventWriter", "DirectiveDistiller"} {
				if got := resolver.ResolveImpls(iface); len(got) != 0 {
					t.Fatalf("ResolveImpls(%s) = %v, want no sibling milestone activation from S2 flags", iface, namesOfSubsystems(got))
				}
			}

			if len(candidateImpls) == 0 {
				return
			}
			dispatcher := core.NewSubsystemDispatcher(registry, core.NewLocalMeter())
			var proposals []cognitive.HintProposal
			if err := core.Dispatch[cognitive.CandidateProposer](
				context.Background(),
				dispatcher,
				"CandidateProposer",
				func(p cognitive.CandidateProposer) error {
					var err error
					proposals, err = p.Propose(context.Background(), cognitive.AttentionEvent{
						Type:    "user_prompt_submit",
						Project: "engram",
						Payload: map[string]interface{}{"text": "T014 real S2 proposer hit"},
					}, 1)
					return err
				},
			); err != nil {
				t.Fatalf("Dispatch(CandidateProposer): %v", err)
			}

			if tt.wantRealProposal {
				require.Len(t, proposals, 1, "real S2 proposer must return meta-index proposals, not a stubbed empty list")
				require.Equal(t, "T014 real S2 proposer hit", proposals[0].Title)
				require.Equal(t, "s2.meta_index", proposals[0].Source)
				require.Len(t, idx.queries, 1, "real proposer must query the S2 meta index exactly once")
			} else {
				require.NotNil(t, proposals, "NoOp fallback must return an empty, iterable proposal slice")
				require.Empty(t, proposals, "NoOp fallback must preserve baseline by returning no proposals")
				require.Empty(t, idx.queries, "flag-off fallback must not query the real S2 meta index")
			}
		})
	}
}

type fakeS4ADirectiveStore struct {
	records []cognitive.AttentionEventRecord
}

func (f *fakeS4ADirectiveStore) Create(_ context.Context, event cognitive.AttentionEventRecord) (*s4directives.StoredAttentionEvent, error) {
	f.records = append(f.records, event)
	return &s4directives.StoredAttentionEvent{
		ID:             int64(len(f.records)),
		Project:        event.Project,
		SessionID:      event.SessionID,
		SourceTurnHash: event.SourceTurnHash,
		DerivedIntent:  event.DerivedIntent,
		AgentConfirmed: event.AgentConfirmed,
		Horizon:        event.Horizon,
		PrivacyClass:   event.PrivacyClass,
		CreatedAt:      time.Unix(1700030000, 0).UTC(),
	}, nil
}

func TestShouldRegisterRealS4ADirectives(t *testing.T) {
	tests := []struct {
		name string
		plug string
		s4a  string
		want bool
	}{
		{name: "master off s4a off", plug: "", s4a: "", want: false},
		{name: "master off s4a on", plug: "", s4a: "true", want: false},
		{name: "master on s4a off", plug: "true", s4a: "", want: false},
		{name: "master on s4a on", plug: "true", s4a: "true", want: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("ENGRAM_V7_PLUG_ENABLED", tt.plug)
			t.Setenv("ENGRAM_V7_S4A_DIRECTIVES_CAPTURE", tt.s4a)

			got := shouldRegisterRealS4ADirectives(core.LoadFlagConfigFromEnv())
			if got != tt.want {
				t.Fatalf("shouldRegisterRealS4ADirectives() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestPlatformWiring_FlagOn_S4ARealDirectivesReplaceNoOps(t *testing.T) {
	t.Setenv("ENGRAM_V7_PLUG_ENABLED", "true")
	t.Setenv("ENGRAM_V7_S1_STATE", "false")
	t.Setenv("ENGRAM_V7_S2_METAMEM", "false")
	t.Setenv("ENGRAM_V7_S3_AMBIENT", "false")
	t.Setenv("ENGRAM_V7_S4A_DIRECTIVES_CAPTURE", "true")
	t.Setenv("ENGRAM_V7_S4B_DIRECTIVES_SURFACING", "false")
	t.Setenv("ENGRAM_V7_S5_TELEMETRY", "false")
	t.Setenv("ENGRAM_V7_S6_OUTCOME", "false")

	registry := core.NewRegistry()
	if err := core.RegisterNoOps(registry); err != nil {
		t.Fatalf("RegisterNoOps: %v", err)
	}
	activateFromFlags(t, registry, core.LoadFlagConfigFromEnv())

	store := &fakeS4ADirectiveStore{}
	if err := registerS4ADirectivesSubsystem(registry, s4directives.NewService(store)); err != nil {
		t.Fatalf("registerS4ADirectivesSubsystem: %v", err)
	}

	resolver, ok := registry.(interface {
		ResolveImpls(interfaceName string) []core.Subsystem
	})
	if !ok {
		t.Fatalf("registry does not expose ResolveImpls")
	}
	for _, iface := range []string{"AttentionEventWriter", "DirectiveDistiller"} {
		impls := resolver.ResolveImpls(iface)
		if got := namesOfSubsystems(impls); !reflect.DeepEqual(got, []string{"engram.s4a.directives_capture"}) {
			t.Fatalf("ResolveImpls(%s) = %v, want real S4a directive subsystem only", iface, got)
		}
	}
	for _, iface := range []string{"StateWriter", "HintEmitter"} {
		if got := resolver.ResolveImpls(iface); len(got) != 0 {
			t.Fatalf("ResolveImpls(%s) = %v, want no sibling milestone activation from S4a flags", iface, namesOfSubsystems(got))
		}
	}
	if got := namesOfSubsystems(resolver.ResolveImpls("CandidateProposer")); !reflect.DeepEqual(got, []string{"core.noop.candidate_proposer"}) {
		t.Fatalf("ResolveImpls(CandidateProposer) = %v, want master-only CORE fallback without S2 real proposer", got)
	}

	dispatcher := core.NewSubsystemDispatcher(registry, core.NewLocalMeter())
	if err := core.Dispatch[cognitive.AttentionEventWriter](
		context.Background(),
		dispatcher,
		"AttentionEventWriter",
		func(w cognitive.AttentionEventWriter) error {
			return w.WriteAttentionEvent(context.Background(), cognitive.AttentionEventRecord{
				Project:        "engram",
				SessionID:      "session-1",
				SourceTurnHash: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
				DerivedIntent:  "keep release notes short",
				AgentConfirmed: true,
				Horizon:        "project",
				PrivacyClass:   "internal",
			})
		},
	); err != nil {
		t.Fatalf("Dispatch(AttentionEventWriter): %v", err)
	}
	require.Len(t, store.records, 1, "real S4a writer must persist through the configured store")
	require.Equal(t, "keep release notes short", store.records[0].DerivedIntent)

	var distilled cognitive.Distilled
	if err := core.Dispatch[cognitive.DirectiveDistiller](
		context.Background(),
		dispatcher,
		"DirectiveDistiller",
		func(d cognitive.DirectiveDistiller) error {
			var err error
			distilled, err = d.Distill(context.Background(), cognitive.RawSignal{
				Text:       " keep release notes short ",
				SourceHash: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
				Context: map[string]string{
					"horizon":       "project",
					"privacy_class": "internal",
				},
			})
			return err
		},
	); err != nil {
		t.Fatalf("Dispatch(DirectiveDistiller): %v", err)
	}
	require.NotEmpty(t, distilled.Intent, "real S4a distiller must return a non-empty intent instead of the CORE NoOp zero value")
	require.Equal(t, "project", distilled.Horizon)
	require.Equal(t, "internal", distilled.Privacy)
}

func TestPlatformWiring_FlagOff_S4ADirectivesStayNoOp(t *testing.T) {
	t.Setenv("ENGRAM_V7_PLUG_ENABLED", "true")
	t.Setenv("ENGRAM_V7_S1_STATE", "false")
	t.Setenv("ENGRAM_V7_S2_METAMEM", "false")
	t.Setenv("ENGRAM_V7_S3_AMBIENT", "false")
	t.Setenv("ENGRAM_V7_S4A_DIRECTIVES_CAPTURE", "false")

	cfg := core.LoadFlagConfigFromEnv()
	if shouldRegisterRealS4ADirectives(cfg) {
		t.Fatalf("shouldRegisterRealS4ADirectives = true, want false when S4a flag is disabled")
	}
	registry := core.NewRegistry()
	if err := core.RegisterNoOps(registry); err != nil {
		t.Fatalf("RegisterNoOps: %v", err)
	}
	activateFromFlags(t, registry, cfg)

	resolver, ok := registry.(interface {
		ResolveImpls(interfaceName string) []core.Subsystem
	})
	if !ok {
		t.Fatalf("registry does not expose ResolveImpls")
	}
	for _, iface := range []string{"AttentionEventWriter", "DirectiveDistiller"} {
		if got := resolver.ResolveImpls(iface); len(got) != 0 {
			t.Fatalf("ResolveImpls(%s) = %v, want no S4a implementation when the S4a flag is disabled", iface, namesOfSubsystems(got))
		}
	}
}

func TestRegisterS4ADirectivesSubsystemRejectsNilService(t *testing.T) {
	registry := core.NewRegistry()
	if err := core.RegisterNoOps(registry); err != nil {
		t.Fatalf("RegisterNoOps: %v", err)
	}

	err := registerS4ADirectivesSubsystem(registry, nil)
	require.ErrorIs(t, err, s4directives.ErrNoService)
}

func newServiceWithV7FlagEnv(t *testing.T, flags map[string]string) *Service {
	t.Helper()
	t.Setenv("ENGRAM_AUTH_DISABLED", "true")
	t.Setenv("ENGRAM_AUTH_ADMIN_TOKEN", "")
	for _, envVar := range []string{
		"ENGRAM_V7_PLUG_ENABLED",
		"ENGRAM_V7_S1_STATE",
		"ENGRAM_V7_S2_METAMEM",
		"ENGRAM_V7_S3_AMBIENT",
		"ENGRAM_V7_S4A_DIRECTIVES_CAPTURE",
		"ENGRAM_V7_S4B_DIRECTIVES_SURFACING",
		"ENGRAM_V7_S5_TELEMETRY",
		"ENGRAM_V7_S6_OUTCOME",
	} {
		t.Setenv(envVar, "false")
	}
	for envVar, value := range flags {
		t.Setenv(envVar, value)
	}

	svc, err := NewService("test-s5-red", nil)
	require.NoError(t, err)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		require.NoError(t, svc.Shutdown(ctx))
	})
	return svc
}

func resolvedProductMetricProviders(t *testing.T, registry core.SubsystemRegistry) []core.Subsystem {
	t.Helper()
	resolver, ok := registry.(interface {
		ResolveImpls(interfaceName string) []core.Subsystem
	})
	if !ok {
		t.Fatalf("registry does not expose ResolveImpls")
	}
	return resolver.ResolveImpls("ProductMetricsProvider")
}

func TestPlatformWiring_FlagOff_S5ProductMetricsProviderStaysAbsentAndEndpoint404(t *testing.T) {
	svc := newServiceWithV7FlagEnv(t, map[string]string{
		"ENGRAM_V7_PLUG_ENABLED":           "true",
		"ENGRAM_V7_S5_TELEMETRY":           "false",
		"ENGRAM_V7_S2_METAMEM":             "false",
		"ENGRAM_V7_S4A_DIRECTIVES_CAPTURE": "false",
	})

	impls := resolvedProductMetricProviders(t, svc.cognitiveRegistry)
	require.Empty(t, impls, "S5-disabled worker boot must preserve current no-provider registry behavior")

	w := httptest.NewRecorder()
	r := newRequestWithSource(http.MethodGet, "/api/stats/v7/product", auth.SourceClient)
	svc.handleStatsV7Product(w, r)
	require.Equal(t, http.StatusNotFound, w.Code, w.Body.String())
	require.Contains(t, w.Body.String(), "s5-telemetry not enabled")
}

func TestPlatformWiring_FlagOn_S5RealProductMetricsProviderRegistersWithSiblingsOff(t *testing.T) {
	svc := newServiceWithV7FlagEnv(t, map[string]string{
		"ENGRAM_V7_PLUG_ENABLED":             "true",
		"ENGRAM_V7_S1_STATE":                 "false",
		"ENGRAM_V7_S2_METAMEM":               "false",
		"ENGRAM_V7_S3_AMBIENT":               "false",
		"ENGRAM_V7_S4A_DIRECTIVES_CAPTURE":   "false",
		"ENGRAM_V7_S4B_DIRECTIVES_SURFACING": "false",
		"ENGRAM_V7_S5_TELEMETRY":             "true",
		"ENGRAM_V7_S6_OUTCOME":               "false",
	})

	impls := resolvedProductMetricProviders(t, svc.cognitiveRegistry)
	require.Len(t, impls, 1, "master+S5 flags must register exactly one real ProductMetricsProvider without sibling subsystem flags")
	require.Equal(t, []string{"engram.s5.product_metrics"}, namesOfSubsystems(impls))
	_, ok := impls[0].(core.ProductMetricsProvider)
	require.True(t, ok, "registered S5 subsystem must satisfy core.ProductMetricsProvider")
}

func TestProductStats_S5OnlyFlagsReturnProductSnapshotWithoutSiblingSubsystems(t *testing.T) {
	svc := newServiceWithV7FlagEnv(t, map[string]string{
		"ENGRAM_V7_PLUG_ENABLED":             "true",
		"ENGRAM_V7_S1_STATE":                 "false",
		"ENGRAM_V7_S2_METAMEM":               "false",
		"ENGRAM_V7_S3_AMBIENT":               "false",
		"ENGRAM_V7_S4A_DIRECTIVES_CAPTURE":   "false",
		"ENGRAM_V7_S4B_DIRECTIVES_SURFACING": "false",
		"ENGRAM_V7_S5_TELEMETRY":             "true",
		"ENGRAM_V7_S6_OUTCOME":               "false",
	})

	w := httptest.NewRecorder()
	r := newRequestWithSource(http.MethodGet, "/api/stats/v7/product", auth.SourceClient)
	svc.handleStatsV7Product(w, r)
	require.Equal(t, http.StatusOK, w.Code, "S5-only enablement must not require S1/S2/S3/S4/S6 flags; body=%s", w.Body.String())

	var snap core.ProductMetricsSnapshot
	require.NoError(t, json.NewDecoder(w.Body).Decode(&snap))
	require.NotNil(t, snap.Metrics, "real S5 provider must return an explicit metrics map, even before product samples exist")
	require.NotNil(t, snap.Readiness, "real S5 provider must return explicit readiness evidence even before product samples exist")
	require.Len(t, snap.Readiness, len(s5.CanonicalMetricKeys()), "S5-only route must expose readiness for every canonical metric")
	require.Equal(t, uint64(30), snap.Readiness[s5.MetricHintPrecision].ThresholdN, "production path must apply S5-owned hint_precision default threshold")
	require.Equal(t, uint64(20), snap.Readiness[s5.MetricAcceptedHintAction].ThresholdN, "production path must apply S5-owned accepted_hint_action default threshold")
}

type fakeS6OutcomeStore struct {
	queries  []fakeS6OutcomeQuery
	memories []*models.Memory
}

type fakeS6OutcomeQuery struct {
	project string
	limit   int
}

var _ s6.OutcomeStore = (*fakeS6OutcomeStore)(nil)

func (f *fakeS6OutcomeStore) ListOutcomeCandidates(ctx context.Context, project string, limit int) ([]*models.Memory, error) {
	f.queries = append(f.queries, fakeS6OutcomeQuery{project: project, limit: limit})
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return append([]*models.Memory(nil), f.memories...), nil
}

func TestShouldRegisterRealS6OutcomeProposer(t *testing.T) {
	tests := []struct {
		name string
		plug string
		s6   string
		want bool
	}{
		{name: "master off s6 off", plug: "", s6: "", want: false},
		{name: "master off s6 on", plug: "", s6: "true", want: false},
		{name: "master on s6 off", plug: "true", s6: "", want: false},
		{name: "master on s6 on", plug: "true", s6: "true", want: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("ENGRAM_V7_PLUG_ENABLED", tt.plug)
			t.Setenv("ENGRAM_V7_S6_OUTCOME", tt.s6)

			got := shouldRegisterRealS6OutcomeProposer(core.LoadFlagConfigFromEnv())
			if got != tt.want {
				t.Fatalf("shouldRegisterRealS6OutcomeProposer() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestPlatformWiring_T005_S6FlagMatrixControlsOutcomeProposerAndSiblings(t *testing.T) {
	tests := []struct {
		name               string
		masterFlag         string
		s6Flag             string
		wantRealRegistered bool
		wantCandidateNames []string
		wantRealProposal   bool
	}{
		{
			name:               "master disabled suppresses real s6 even when s6 flag is set",
			masterFlag:         "false",
			s6Flag:             "true",
			wantRealRegistered: false,
			wantCandidateNames: []string{},
		},
		{
			name:               "s6 disabled keeps core candidate noop fallback",
			masterFlag:         "true",
			s6Flag:             "false",
			wantRealRegistered: false,
			wantCandidateNames: []string{"core.noop.candidate_proposer"},
		},
		{
			name:               "master and s6 enabled replace noop with real outcome proposer only",
			masterFlag:         "true",
			s6Flag:             "true",
			wantRealRegistered: true,
			wantCandidateNames: []string{"engram.s6.outcome_policy"},
			wantRealProposal:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("ENGRAM_V7_PLUG_ENABLED", tt.masterFlag)
			t.Setenv("ENGRAM_V7_S1_STATE", "false")
			t.Setenv("ENGRAM_V7_S2_METAMEM", "false")
			t.Setenv("ENGRAM_V7_S3_AMBIENT", "false")
			t.Setenv("ENGRAM_V7_S4A_DIRECTIVES_CAPTURE", "false")
			t.Setenv("ENGRAM_V7_S4B_DIRECTIVES_SURFACING", "false")
			t.Setenv("ENGRAM_V7_S5_TELEMETRY", "false")
			t.Setenv("ENGRAM_V7_S6_OUTCOME", tt.s6Flag)

			cfg := core.LoadFlagConfigFromEnv()
			if got := shouldRegisterRealS6OutcomeProposer(cfg); got != tt.wantRealRegistered {
				t.Fatalf("shouldRegisterRealS6OutcomeProposer() = %v, want %v", got, tt.wantRealRegistered)
			}

			registry := core.NewRegistry()
			if err := core.RegisterNoOps(registry); err != nil {
				t.Fatalf("RegisterNoOps: %v", err)
			}
			activateFromFlags(t, registry, cfg)

			store := &fakeS6OutcomeStore{memories: []*models.Memory{{
				ID:        601,
				Project:   "engram",
				Content:   "S6 outcome policy handoff\nFull memory body must stay out of the proposal.",
				Tags:      []string{"s6", "outcome"},
				CreatedAt: time.Unix(1700040000, 0).UTC(),
				TsAlpha:   8,
				TsBeta:    2,
			}}}
			if tt.wantRealRegistered {
				if err := registerS6OutcomeProposerSubsystem(registry, store); err != nil {
					t.Fatalf("registerS6OutcomeProposerSubsystem: %v", err)
				}
			}

			resolver, ok := registry.(interface {
				ResolveImpls(interfaceName string) []core.Subsystem
			})
			if !ok {
				t.Fatalf("registry does not expose ResolveImpls")
			}
			candidateImpls := resolver.ResolveImpls("CandidateProposer")
			if got := namesOfSubsystems(candidateImpls); !reflect.DeepEqual(got, tt.wantCandidateNames) {
				t.Fatalf("ResolveImpls(CandidateProposer) = %v, want %v", got, tt.wantCandidateNames)
			}
			for _, iface := range []string{"HintEmitter", "StateWriter", "AttentionEventWriter", "DirectiveDistiller", "ProductMetricsProvider"} {
				if got := resolver.ResolveImpls(iface); len(got) != 0 {
					t.Fatalf("ResolveImpls(%s) = %v, want no sibling milestone activation from S6 flags", iface, namesOfSubsystems(got))
				}
			}

			if len(candidateImpls) == 0 {
				return
			}
			dispatcher := core.NewSubsystemDispatcher(registry, core.NewLocalMeter())
			var proposals []cognitive.HintProposal
			if err := core.Dispatch[cognitive.CandidateProposer](
				context.Background(),
				dispatcher,
				"CandidateProposer",
				func(p cognitive.CandidateProposer) error {
					var err error
					proposals, err = p.Propose(context.Background(), cognitive.AttentionEvent{
						Type:    "user_prompt_submit",
						Project: "engram",
						Payload: map[string]interface{}{"text": "S6 outcome policy handoff"},
					}, 1)
					return err
				},
			); err != nil {
				t.Fatalf("Dispatch(CandidateProposer): %v", err)
			}

			if tt.wantRealProposal {
				require.Len(t, proposals, 1, "real S6 proposer must return outcome-ranked proposals, not a stubbed empty list")
				require.Equal(t, "Memory 601", proposals[0].Title)
				require.Equal(t, "s6.outcome_policy", proposals[0].Source)
				require.Len(t, store.queries, 1, "real S6 proposer must query the outcome store exactly once")
				require.Equal(t, "engram", store.queries[0].project)
				require.Equal(t, 1, store.queries[0].limit)
			} else {
				require.NotNil(t, proposals, "NoOp fallback must return an empty, iterable proposal slice")
				require.Empty(t, proposals, "flag-off fallback must preserve baseline by returning no proposals")
				require.Empty(t, store.queries, "flag-off fallback must not query the S6 outcome store")
			}
		})
	}
}

func namesOfSubsystems(impls []core.Subsystem) []string {
	names := make([]string, 0, len(impls))
	for _, impl := range impls {
		names = append(names, impl.Name())
	}
	return names
}
