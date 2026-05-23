package cognitive

import (
	"go/ast"
	"go/parser"
	"go/token"
	"reflect"
	"sort"
	"testing"
)

// TestInterfaceCount_Exactly6 parses interfaces.go via go/parser and asserts
// that EXACTLY six top-level interface declarations exist with the canonical
// names enumerated in T003 AC + arch ADR-010. This is the anti-stub guard:
// any drift below 6 (a missing interface) or above 6 (a stray addition)
// fails the count assertion AND the name-set assertion. Detecting both
// directions makes the test resistant to "loosen the test until green"
// symptom patches (AP-e).
func TestInterfaceCount_Exactly6(t *testing.T) {
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
		"StateWriter",
	}

	if !reflect.DeepEqual(got, want) {
		t.Fatalf("interface set mismatch:\n  got:  %v\n  want: %v", got, want)
	}
	if len(got) != 6 {
		t.Fatalf("interface count: got %d, want 6", len(got))
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
