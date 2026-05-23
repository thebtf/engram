package core

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"testing"
)

// forbiddenCoreInternalIdents enumerates the CORE-internal identifiers that
// MUST NOT appear in any pkg/cognitive .go file as identifier references.
// Comment text mentioning these names is fine (Go AST treats comments as
// CommentGroup, never as Ident), so the boundary invariant is purely
// code-level.
var forbiddenCoreInternalIdents = map[string]struct{}{
	// 4 main interfaces
	"SubsystemRegistry": {},
	"AttentionEventBus": {},
	"HintQueue":         {},
	"SubsystemMeter":    {},
	// 8 named DTOs / supporting interface
	"SubsystemInfo":          {},
	"SubsystemHealth":        {},
	"MetricsSnapshot":        {},
	"HistogramSummary":       {},
	"QueueStats":             {},
	"ProductMetricsProvider": {},
	"ProductMetricsWindow":   {},
	"ProductMetricsSnapshot": {},
	// Supporting types declared in ADR-010 alongside the interfaces; also
	// must not leak across the package boundary.
	"Subsystem":    {},
	"EventHandler": {},
	"Unsubscribe":  {},
	"Dependencies": {},
	// CORE-internal mirrors of public payloads. The public package owns
	// AttentionEvent and HintProposal; the CORE-internal *Payload variants
	// stay inside core and must not leak across the boundary.
	"AttentionEventPayload": {},
	"HintProposalPayload":   {},
}

// TestBoundaryInvariant_PkgCognitiveDoesNotReferenceCoreInternals walks every
// .go file under pkg/cognitive/ and asserts that no *ast.Ident.Name matches
// the forbidden CORE-internal identifier set. The boundary invariant is the
// architectural contract that makes parallel Wave-2 spec drafting safe: the
// public package signature must not depend on any CORE-internal symbol.
func TestBoundaryInvariant_PkgCognitiveDoesNotReferenceCoreInternals(t *testing.T) {
	// The test runs from internal/cognitive/core/ — pkg/cognitive lives at
	// ../../../pkg/cognitive. parser.ParseDir handles the .go-file collection;
	// we filter out *_test.go to keep the invariant scoped to source files
	// that Wave-2 consumers will import.
	pkgDir := filepath.Join("..", "..", "..", "pkg", "cognitive")

	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, pkgDir, func(fi fs.FileInfo) bool {
		name := fi.Name()
		return filepath.Ext(name) == ".go" && !endsWith(name, "_test.go")
	}, parser.ParseComments)
	if err != nil {
		t.Fatalf("parse %s: %v", pkgDir, err)
	}

	if len(pkgs) == 0 {
		t.Fatalf("no packages parsed under %s", pkgDir)
	}

	var violations []string
	for _, pkg := range pkgs {
		for path, file := range pkg.Files {
			ast.Inspect(file, func(n ast.Node) bool {
				id, ok := n.(*ast.Ident)
				if !ok {
					return true
				}
				if _, hit := forbiddenCoreInternalIdents[id.Name]; hit {
					pos := fset.Position(id.Pos())
					violations = append(violations, formatViolation(id.Name, path, pos.Line))
				}
				return true
			})
		}
	}

	if len(violations) > 0 {
		sort.Strings(violations)
		t.Fatalf("CORE-internal identifier leak in pkg/cognitive:\n  %s", joinLines(violations))
	}
}

// formatViolation keeps the test failure message compact.
func formatViolation(ident, path string, line int) string {
	return ident + " @ " + path + ":" + strconv.Itoa(line)
}

func endsWith(s, suffix string) bool {
	if len(suffix) > len(s) {
		return false
	}
	return s[len(s)-len(suffix):] == suffix
}

func joinLines(ss []string) string {
	out := ""
	for i, s := range ss {
		if i > 0 {
			out += "\n  "
		}
		out += s
	}
	return out
}

// TestSubsystemRegistry_SignatureShape pins the SubsystemRegistry method set
// to the 8 methods canonical for v7: the 6 from ADR-010 (Register, Enable,
// Disable, Get, List, Health) plus ResolvePolicy (spec FR-7 / Clarify C2)
// and TransitionToFailed (T015 dispatcher hook). Any addition or removal at
// the type level fails here, forcing an explicit ADR amendment.
func TestSubsystemRegistry_SignatureShape(t *testing.T) {
	assertInterfaceMethods(t, "SubsystemRegistry",
		reflect.TypeOf((*SubsystemRegistry)(nil)).Elem(),
		[]string{"Disable", "Enable", "Get", "Health", "List", "Register", "ResolvePolicy", "TransitionToFailed"})
}

// TestAttentionEventBus_SignatureShape pins Publish + Subscribe.
func TestAttentionEventBus_SignatureShape(t *testing.T) {
	assertInterfaceMethods(t, "AttentionEventBus",
		reflect.TypeOf((*AttentionEventBus)(nil)).Elem(),
		[]string{"Publish", "Subscribe"})
}

// TestHintQueue_SignatureShape pins Enqueue + Drain + Stats.
func TestHintQueue_SignatureShape(t *testing.T) {
	assertInterfaceMethods(t, "HintQueue",
		reflect.TypeOf((*HintQueue)(nil)).Elem(),
		[]string{"Drain", "Enqueue", "Stats"})
}

// TestSubsystemMeter_SignatureShape pins IncrCounter + ObserveHistogram +
// Snapshot. Per ADR-008 this interface owns generic counters only; product
// metric methods MUST NOT appear here.
func TestSubsystemMeter_SignatureShape(t *testing.T) {
	assertInterfaceMethods(t, "SubsystemMeter",
		reflect.TypeOf((*SubsystemMeter)(nil)).Elem(),
		[]string{"IncrCounter", "ObserveHistogram", "Snapshot"})
}

// TestProductMetricsProvider_SignatureShape pins ProductMetrics as the single
// method per ADR-008 design: CORE exposes the substrate interface; S5
// implements the concrete product metric calculation.
func TestProductMetricsProvider_SignatureShape(t *testing.T) {
	assertInterfaceMethods(t, "ProductMetricsProvider",
		reflect.TypeOf((*ProductMetricsProvider)(nil)).Elem(),
		[]string{"ProductMetrics"})
}

func assertInterfaceMethods(t *testing.T, name string, typ reflect.Type, want []string) {
	t.Helper()

	got := make([]string, 0, typ.NumMethod())
	for i := 0; i < typ.NumMethod(); i++ {
		got = append(got, typ.Method(i).Name)
	}
	sort.Strings(got)

	wantSorted := make([]string, len(want))
	copy(wantSorted, want)
	sort.Strings(wantSorted)

	if !reflect.DeepEqual(got, wantSorted) {
		t.Fatalf("%s method set:\n  got:  %v\n  want: %v", name, got, wantSorted)
	}
}
