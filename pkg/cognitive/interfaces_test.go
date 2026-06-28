package cognitive

import (
	"go/ast"
	"go/parser"
	"go/token"
	"reflect"
	"sort"
	"testing"
)

// TestInterfaceCount_Exactly7 parses interfaces.go via go/parser and asserts
// that EXACTLY seven top-level interface declarations exist with the canonical
// names enumerated in T003 AC + arch ADR-010 plus ENG-MPL-1 T001's StatePlane
// product contract. This is the anti-stub guard:
// any drift below 7 (a missing interface) or above 7 (a stray addition)
// fails the count assertion AND the name-set assertion. Detecting both
// directions makes the test resistant to "loosen the test until green"
// symptom patches (AP-e).
func TestInterfaceCount_Exactly7(t *testing.T) {
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
		"HintEmitter",
		"StatePlane",
		"StateWriter",
	}

	if !reflect.DeepEqual(got, want) {
		t.Fatalf("interface set mismatch:\n  got:  %v\n  want: %v", got, want)
	}
	if len(got) != 7 {
		t.Fatalf("interface count: got %d, want 7", len(got))
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
