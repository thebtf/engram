package cognitive

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"reflect"
	"sort"
	"testing"
)

// TestInterfaceCount_Exactly10 parses interfaces.go via go/parser and asserts
// that EXACTLY ten top-level interface declarations exist with the canonical
// names enumerated in T003 AC + arch ADR-010 plus ENG-MPL-1 T001's StatePlane
// and CR-002 T001 ExperienceProvider plus CR-003 T001 ForgettingClassifier
// plus CR-004 T001 TemporalTruthProvider product contracts. This is the anti-stub guard: any drift below 10 (a missing interface) or above 10 (a stray addition)
// fails the count assertion AND the name-set assertion. Detecting both
// directions makes the test resistant to "loosen the test until green"
// symptom patches (AP-e).
func TestInterfaceCount_Exactly10(t *testing.T) {
	const path = "interfaces.go"

	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, nil, parser.ParseComments)
	if err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}

	var got []string
	for _, decl := range file.Decls {
		gen, ok := decl.(*ast.GenDecl)
		if !ok || gen.Tok != token.TYPE {
			continue
		}
		for _, spec := range gen.Specs {
			ts, ok := spec.(*ast.TypeSpec)
			if !ok {
				continue
			}
			if _, ok := ts.Type.(*ast.InterfaceType); !ok {
				continue
			}
			got = append(got, ts.Name.Name)
		}
	}
	sort.Strings(got)

	want := []string{
		"AttentionEventSource",
		"AttentionEventWriter",
		"CandidateProposer",
		"DirectiveDistiller",
		"ExperienceProvider",
		"ForgettingClassifier",
		"HintEmitter",
		"StatePlane",
		"StateWriter",
		"TemporalTruthProvider",
	}

	if !reflect.DeepEqual(got, want) {
		t.Fatalf("interface set mismatch:\n  got:  %v\n  want: %v", got, want)
	}
	if len(got) != 10 {
		t.Fatalf("interface count: got %d, want 10", len(got))
	}
}

func TestExperienceProvider_OneMethod(t *testing.T) {
	typ := reflect.TypeOf((*ExperienceProvider)(nil)).Elem()

	if got, want := typ.NumMethod(), 1; got != want {
		t.Fatalf("ExperienceProvider.NumMethod: got %d, want %d", got, want)
	}
	method := typ.Method(0)
	if got, want := method.Name, "QueryExperience"; got != want {
		t.Fatalf("ExperienceProvider method name: got %q, want %q", got, want)
	}
}

func TestForgettingClassifier_OneMethod(t *testing.T) {
	typ := reflect.TypeOf((*ForgettingClassifier)(nil)).Elem()

	if got, want := typ.NumMethod(), 1; got != want {
		t.Fatalf("ForgettingClassifier.NumMethod: got %d, want %d", got, want)
	}
	method := typ.Method(0)
	if got, want := method.Name, "ClassifyForgetting"; got != want {
		t.Fatalf("ForgettingClassifier method name: got %q, want %q", got, want)
	}
}

func TestTemporalTruthProvider_OneMethod(t *testing.T) {
	typ := reflect.TypeOf((*TemporalTruthProvider)(nil)).Elem()

	if got, want := typ.NumMethod(), 1; got != want {
		t.Fatalf("TemporalTruthProvider.NumMethod: got %d, want %d", got, want)
	}
	method := typ.Method(0)
	if got, want := method.Name, "QueryTemporalTruth"; got != want {
		t.Fatalf("TemporalTruthProvider method name: got %q, want %q", got, want)
	}
}

// TestStateWriter_TwoMethods enforces ADR-010's split decision: StateWriter
// owns exactly two surfaces (session + project) so S1 can implement both
// without fake-method dead weight. A combined three-method StateWriter
// (the rejected pre-Fix-1 shape, which folded AttentionEvent writes in)
// would fail here.
func TestStateWriter_TwoMethods(t *testing.T) {
	typ := reflect.TypeOf((*StateWriter)(nil)).Elem()

	if got, want := typ.NumMethod(), 2; got != want {
		t.Fatalf("StateWriter.NumMethod: got %d, want %d", got, want)
	}

	got := []string{typ.Method(0).Name, typ.Method(1).Name}
	sort.Strings(got)
	want := []string{"WriteProjectState", "WriteSessionState"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("StateWriter method names: got %v, want %v", got, want)
	}
}

// TestStatePlane_FiveMethods enforces ENG-MPL-1 T001: the native state plane
// has one agent-owned interface with both write and read surfaces, including a
// bounded resume packet read.
func TestStatePlane_FiveMethods(t *testing.T) {
	typ := reflect.TypeOf((*StatePlane)(nil)).Elem()

	if got, want := typ.NumMethod(), 5; got != want {
		t.Fatalf("StatePlane.NumMethod: got %d, want %d", got, want)
	}

	got := []string{}
	for i := 0; i < typ.NumMethod(); i++ {
		got = append(got, typ.Method(i).Name)
	}
	sort.Strings(got)
	want := []string{"ReadProjectState", "ReadResumePacket", "ReadSessionState", "WriteProjectState", "WriteSessionState"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("StatePlane method names: got %v, want %v", got, want)
	}
}

func TestStatePlane_ReadResumePacketSignature(t *testing.T) {
	typ := reflect.TypeOf((*StatePlane)(nil)).Elem()
	method, ok := typ.MethodByName("ReadResumePacket")
	if !ok {
		t.Fatalf("StatePlane missing ReadResumePacket")
	}

	if got, want := method.Type.NumIn(), 2; got != want {
		t.Fatalf("ReadResumePacket.NumIn: got %d, want %d", got, want)
	}
	if got, want := method.Type.In(0), reflect.TypeOf((*context.Context)(nil)).Elem(); got != want {
		t.Fatalf("ReadResumePacket arg0: got %s, want %s", got, want)
	}
	if got, want := method.Type.In(1), reflect.TypeOf(ResumePacketRequest{}); got != want {
		t.Fatalf("ReadResumePacket arg1: got %s, want %s", got, want)
	}

	if got, want := method.Type.NumOut(), 2; got != want {
		t.Fatalf("ReadResumePacket.NumOut: got %d, want %d", got, want)
	}
	if got, want := method.Type.Out(0), reflect.TypeOf(ResumePacket{}); got != want {
		t.Fatalf("ReadResumePacket result0: got %s, want %s", got, want)
	}
	if got, want := method.Type.Out(1), reflect.TypeOf((*error)(nil)).Elem(); got != want {
		t.Fatalf("ReadResumePacket result1: got %s, want %s", got, want)
	}
}

// TestAttentionEventWriter_OneMethod enforces the ADR-010 separation between
// S1's state surface and S4a's directive-capture surface: AttentionEventWriter
// is a single-method interface so S4a can implement it without StateWriter
// pollution.
func TestAttentionEventWriter_OneMethod(t *testing.T) {
	typ := reflect.TypeOf((*AttentionEventWriter)(nil)).Elem()

	if got, want := typ.NumMethod(), 1; got != want {
		t.Fatalf("AttentionEventWriter.NumMethod: got %d, want %d", got, want)
	}
	if got, want := typ.Method(0).Name, "WriteAttentionEvent"; got != want {
		t.Fatalf("AttentionEventWriter method name: got %q, want %q", got, want)
	}
}

// TestSingleMethodInterfaces pins method count == 1 for every cross-subsystem
// interface that ADR-010 defines with a single method. A phantom second
// method silently added during SG-2 work (rebase mishap, AI worker drift)
// would not be caught by TestInterfaceCount_Exactly6 alone — that test pins
// the SET of interface names, not their internal shape. This test closes
// the gap for the four single-method contracts.
func TestSingleMethodInterfaces(t *testing.T) {
	cases := []struct {
		name string
		typ  reflect.Type
		want string
	}{
		{"AttentionEventSource", reflect.TypeOf((*AttentionEventSource)(nil)).Elem(), "EventsProduced"},
		{"CandidateProposer", reflect.TypeOf((*CandidateProposer)(nil)).Elem(), "Propose"},
		{"HintEmitter", reflect.TypeOf((*HintEmitter)(nil)).Elem(), "Render"},
		{"DirectiveDistiller", reflect.TypeOf((*DirectiveDistiller)(nil)).Elem(), "Distill"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := c.typ.NumMethod(); got != 1 {
				t.Fatalf("%s.NumMethod: got %d, want 1", c.name, got)
			}
			if got := c.typ.Method(0).Name; got != c.want {
				t.Fatalf("%s method name: got %q, want %q", c.name, got, c.want)
			}
		})
	}
}
