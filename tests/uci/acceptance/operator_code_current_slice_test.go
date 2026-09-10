package acceptance

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"strings"
	"testing"
)

const operatorCodeCurrentSliceFixtureID = "operator-code-current-slice/v1"

type operatorCodeCurrentSliceFixture struct {
	FixtureID          string                                      `json:"fixture_id"`
	RATupleInputs      []operatorCodeCurrentSliceTupleInput        `json:"r_a_tuple_inputs"`
	SourceFiles        []operatorCodeCurrentSliceSourceFile        `json:"source_files"`
	SupportedRelations []operatorCodeCurrentSliceRelation          `json:"supported_relations"`
	DeferredRelations  []operatorCodeCurrentSliceDeferredRelation  `json:"deferred_relations"`
	Limitations        []operatorCodeCurrentSliceLimitation        `json:"limitation_labels"`
	SameView           operatorCodeCurrentSliceSameViewExpectation `json:"same_view_expectation"`
	Watcher            operatorCodeCurrentSliceWatcherObservation  `json:"watcher_observation"`
}

type operatorCodeCurrentSliceTupleInput struct {
	Name           string `json:"name"`
	RecordingOwner string `json:"recording_owner"`
	Requirement    string `json:"requirement"`
}

type operatorCodeCurrentSliceSourceFile struct {
	Path     string `json:"path"`
	Language string `json:"language"`
	SHA256   string `json:"sha256"`
}

type operatorCodeCurrentSliceRelation struct {
	Language string `json:"language"`
	Path     string `json:"path"`
	From     string `json:"from"`
	Relation string `json:"relation"`
	To       string `json:"to"`
}

type operatorCodeCurrentSliceDeferredRelation struct {
	Language string `json:"language"`
	Relation string `json:"relation"`
	Reason   string `json:"reason"`
}

type operatorCodeCurrentSliceLimitation struct {
	Category string `json:"category"`
	Label    string `json:"label"`
}

type operatorCodeCurrentSliceSameViewExpectation struct {
	Semantic  operatorCodeCurrentSliceSemanticExpectation  `json:"semantic"`
	Graph     operatorCodeCurrentSliceGraphExpectation     `json:"graph"`
	ExactRead operatorCodeCurrentSliceExactReadExpectation `json:"exact_read"`
}

type operatorCodeCurrentSliceSemanticExpectation struct {
	ViewID string `json:"view_id"`
	Mode   string `json:"mode"`
	Query  string `json:"query"`
}

type operatorCodeCurrentSliceGraphExpectation struct {
	ViewID   string `json:"view_id"`
	Relation string `json:"relation"`
	From     string `json:"from"`
	To       string `json:"to"`
}

type operatorCodeCurrentSliceExactReadExpectation struct {
	ViewID              string `json:"view_id"`
	Path                string `json:"path"`
	Source              string `json:"source"`
	WorkingCopyFallback bool   `json:"working_copy_fallback"`
}

type operatorCodeCurrentSliceWatcherObservation struct {
	Trigger               string                               `json:"trigger"`
	Mechanism             string                               `json:"mechanism"`
	ManualViewPublication bool                                 `json:"manual_view_publication"`
	A                     operatorCodeCurrentSliceAObservation `json:"a"`
	B                     operatorCodeCurrentSliceBObservation `json:"b"`
}

type operatorCodeCurrentSliceAObservation struct {
	Before                                 operatorCodeCurrentSliceASnapshot `json:"before"`
	After                                  operatorCodeCurrentSliceASnapshot `json:"after"`
	OldViewReadableUntilExplicitTransition bool                              `json:"old_view_readable_until_explicit_transition"`
}

type operatorCodeCurrentSliceASnapshot struct {
	ViewID       string `json:"view_id"`
	TargetValue  string `json:"target_value"`
	TargetSHA256 string `json:"target_sha256"`
	CallerSHA256 string `json:"caller_sha256"`
}

type operatorCodeCurrentSliceBObservation struct {
	Before operatorCodeCurrentSliceBSnapshot `json:"before"`
	After  operatorCodeCurrentSliceBSnapshot `json:"after"`
}

type operatorCodeCurrentSliceBSnapshot struct {
	ViewID              string `json:"view_id"`
	TargetValue         string `json:"target_value"`
	TargetSHA256        string `json:"target_sha256"`
	MembershipSnapshot  string `json:"membership_snapshot"`
	EdgeSnapshot        string `json:"edge_snapshot"`
	ExactSourceSnapshot string `json:"exact_source_snapshot"`
}

// TestOperatorCodeCurrentSliceFixture pins the bounded mechanism contract that
// S2 consumes. It deliberately proves fixture semantics and declared limits,
// not live integration: the real Go/PostgreSQL/browser execution belongs to
// the later S2 cross-surface acceptance owner.
func TestOperatorCodeCurrentSliceFixture(t *testing.T) {
	fixtureRoot, fixture := loadOperatorCodeCurrentSliceFixture(t)
	if err := validateOperatorCodeCurrentSliceFixture(fixtureRoot, fixture); err != nil {
		t.Fatalf("current-slice fixture is invalid: %v", err)
	}

	for _, omitted := range []string{"semantic", "graph", "view"} {
		t.Run("rejects omitted "+omitted+" limitation", func(t *testing.T) {
			mutated := cloneOperatorCodeCurrentSliceFixture(t, fixture)
			mutated.Limitations = removeOperatorCodeCurrentSliceLimitation(mutated.Limitations, omitted)
			if err := validateOperatorCodeCurrentSliceFixture(fixtureRoot, mutated); err == nil || !strings.Contains(err.Error(), "missing limitation category "+omitted) {
				t.Fatalf("omitting %q limitation error = %v, want the omission rejected", omitted, err)
			}
		})
	}

	t.Run("rejects graph and exact read from different Views", func(t *testing.T) {
		mutated := cloneOperatorCodeCurrentSliceFixture(t, fixture)
		mutated.SameView.Graph.ViewID = "view-b-isolated"
		if err := validateOperatorCodeCurrentSliceFixture(fixtureRoot, mutated); err == nil || !strings.Contains(err.Error(), "same-View expectation") {
			t.Fatalf("cross-View graph error = %v, want same-View rejection", err)
		}
	})

	t.Run("rejects A update confused with B", func(t *testing.T) {
		mutated := cloneOperatorCodeCurrentSliceFixture(t, fixture)
		mutated.Watcher.A.After = operatorCodeCurrentSliceASnapshot{
			ViewID:       mutated.Watcher.B.After.ViewID,
			TargetValue:  mutated.Watcher.B.After.TargetValue,
			TargetSHA256: mutated.Watcher.B.After.TargetSHA256,
			CallerSHA256: mutated.Watcher.A.Before.CallerSHA256,
		}
		if err := validateOperatorCodeCurrentSliceFixture(fixtureRoot, mutated); err == nil || !strings.Contains(err.Error(), "A/B isolation") {
			t.Fatalf("A/B-confused watcher error = %v, want isolation rejection", err)
		}
	})
}

func loadOperatorCodeCurrentSliceFixture(t *testing.T) (string, operatorCodeCurrentSliceFixture) {
	t.Helper()
	_, sourcePath, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve current-slice fixture test path")
	}
	root := filepath.Clean(filepath.Join(filepath.Dir(sourcePath), "..", "..", ".."))
	fixtureRoot := filepath.Join(root, "tests", "fixtures", "operator-code-current-slice")
	encoded, err := os.ReadFile(filepath.Join(fixtureRoot, "manifest.json"))
	if err != nil {
		t.Fatalf("read current-slice fixture manifest: %v", err)
	}
	var fixture operatorCodeCurrentSliceFixture
	if err := json.Unmarshal(encoded, &fixture); err != nil {
		t.Fatalf("decode current-slice fixture manifest: %v", err)
	}
	return fixtureRoot, fixture
}

func cloneOperatorCodeCurrentSliceFixture(t *testing.T, fixture operatorCodeCurrentSliceFixture) operatorCodeCurrentSliceFixture {
	t.Helper()
	encoded, err := json.Marshal(fixture)
	if err != nil {
		t.Fatalf("encode current-slice fixture clone: %v", err)
	}
	var cloned operatorCodeCurrentSliceFixture
	if err := json.Unmarshal(encoded, &cloned); err != nil {
		t.Fatalf("decode current-slice fixture clone: %v", err)
	}
	return cloned
}

func removeOperatorCodeCurrentSliceLimitation(limitations []operatorCodeCurrentSliceLimitation, category string) []operatorCodeCurrentSliceLimitation {
	filtered := make([]operatorCodeCurrentSliceLimitation, 0, len(limitations))
	for _, limitation := range limitations {
		if limitation.Category != category {
			filtered = append(filtered, limitation)
		}
	}
	return filtered
}

func validateOperatorCodeCurrentSliceFixture(fixtureRoot string, fixture operatorCodeCurrentSliceFixture) error {
	if fixture.FixtureID != operatorCodeCurrentSliceFixtureID {
		return fmt.Errorf("fixture_id = %q, want %q", fixture.FixtureID, operatorCodeCurrentSliceFixtureID)
	}
	if err := validateOperatorCodeCurrentSliceTuple(fixture.RATupleInputs); err != nil {
		return err
	}
	sources, err := validateOperatorCodeCurrentSliceSources(fixtureRoot, fixture.SourceFiles)
	if err != nil {
		return err
	}
	if err := validateOperatorCodeCurrentSliceRelations(fixture.SupportedRelations, fixture.DeferredRelations); err != nil {
		return err
	}
	if err := validateOperatorCodeCurrentSliceLimitations(fixture.Limitations); err != nil {
		return err
	}
	if err := validateOperatorCodeCurrentSliceSameView(fixture.SameView); err != nil {
		return err
	}
	return validateOperatorCodeCurrentSliceWatcher(fixture.Watcher, sources, fixture.SameView.Semantic.ViewID)
}

func validateOperatorCodeCurrentSliceTuple(inputs []operatorCodeCurrentSliceTupleInput) error {
	expected := []string{"server_commit", "daemon_build", "parser_bundle_digest", "resolver_profile", "console_commit", "fixture_identity"}
	if len(inputs) != len(expected) {
		return fmt.Errorf("R-A tuple inputs = %d, want %d exact inputs", len(inputs), len(expected))
	}
	seen := make(map[string]bool, len(inputs))
	for _, input := range inputs {
		if input.Name == "" || input.RecordingOwner != "s2-integration" || input.Requirement == "" {
			return fmt.Errorf("invalid R-A tuple input %#v", input)
		}
		if seen[input.Name] {
			return fmt.Errorf("duplicate R-A tuple input %q", input.Name)
		}
		seen[input.Name] = true
	}
	for _, name := range expected {
		if !seen[name] {
			return fmt.Errorf("missing R-A tuple input %q", name)
		}
	}
	return nil
}

func validateOperatorCodeCurrentSliceSources(fixtureRoot string, sourceFiles []operatorCodeCurrentSliceSourceFile) (map[string]string, error) {
	expectedLanguages := map[string]string{
		"go/current_slice.go":                          "go",
		"worktrees/a-before/src/target.ts":             "typescript",
		"worktrees/a-before/src/relay.ts":              "typescript",
		"worktrees/a-before/src/CurrentSlicePanel.tsx": "tsx",
		"worktrees/a-after/src/target.ts":              "typescript",
		"worktrees/a-after/src/relay.ts":               "typescript",
		"worktrees/a-after/src/CurrentSlicePanel.tsx":  "tsx",
		"worktrees/b/src/target.ts":                    "typescript",
		"worktrees/b/src/relay.ts":                     "typescript",
		"worktrees/b/src/CurrentSlicePanel.tsx":        "tsx",
	}
	if len(sourceFiles) != len(expectedLanguages) {
		return nil, fmt.Errorf("source files = %d, want %d", len(sourceFiles), len(expectedLanguages))
	}

	sources := make(map[string]string, len(sourceFiles))
	for _, source := range sourceFiles {
		language, expected := expectedLanguages[source.Path]
		if !expected || source.Language != language || source.SHA256 == "" {
			return nil, fmt.Errorf("unsupported source fixture %#v", source)
		}
		if _, duplicate := sources[source.Path]; duplicate {
			return nil, fmt.Errorf("duplicate source fixture %q", source.Path)
		}
		contents, err := os.ReadFile(filepath.Join(fixtureRoot, filepath.FromSlash(source.Path)))
		if err != nil {
			return nil, fmt.Errorf("read source fixture %q: %w", source.Path, err)
		}
		digest := sha256.Sum256(contents)
		actual := hex.EncodeToString(digest[:])
		if actual != source.SHA256 {
			return nil, fmt.Errorf("source fixture %q digest = %s, want %s", source.Path, actual, source.SHA256)
		}
		sources[source.Path] = string(contents)
	}
	for path := range expectedLanguages {
		if _, found := sources[path]; !found {
			return nil, fmt.Errorf("missing source fixture %q", path)
		}
	}

	if err := validateOperatorCodeCurrentSliceGoSource(sources["go/current_slice.go"]); err != nil {
		return nil, err
	}
	for _, root := range []string{"worktrees/a-before", "worktrees/a-after", "worktrees/b"} {
		if err := validateOperatorCodeCurrentSliceTypeScriptChain(sources, root); err != nil {
			return nil, err
		}
	}
	return sources, nil
}

func validateOperatorCodeCurrentSliceGoSource(source string) error {
	file, err := parser.ParseFile(token.NewFileSet(), "current_slice.go", source, 0)
	if err != nil {
		return fmt.Errorf("parse Go relation fixture: %w", err)
	}
	for _, declaration := range file.Decls {
		function, ok := declaration.(*ast.FuncDecl)
		if !ok || function.Name.Name != "SemanticEntry" || function.Body == nil {
			continue
		}
		found := false
		ast.Inspect(function.Body, func(node ast.Node) bool {
			call, ok := node.(*ast.CallExpr)
			if !ok {
				return true
			}
			identifier, ok := call.Fun.(*ast.Ident)
			if ok && identifier.Name == "ExactReadTarget" {
				found = true
			}
			return true
		})
		if found {
			return nil
		}
	}
	return fmt.Errorf("Go fixture omits SemanticEntry calls ExactReadTarget relation")
}

func validateOperatorCodeCurrentSliceTypeScriptChain(sources map[string]string, root string) error {
	target := sources[root+"/src/target.ts"]
	relay := sources[root+"/src/relay.ts"]
	panel := sources[root+"/src/CurrentSlicePanel.tsx"]
	if !strings.Contains(target, "export function readCurrentSlice(): string") {
		return fmt.Errorf("%s target omits exported readCurrentSlice", root)
	}
	if !strings.Contains(relay, "export { readCurrentSlice as readCurrentSliceAlias } from \"./target\"") {
		return fmt.Errorf("%s relay omits bounded TypeScript re-export alias", root)
	}
	if !strings.Contains(panel, "import { readCurrentSliceAlias as renderCurrentSlice } from \"./relay\"") || !strings.Contains(panel, "return renderCurrentSlice()") {
		return fmt.Errorf("%s panel omits bounded TSX import-alias call chain", root)
	}
	return nil
}

func validateOperatorCodeCurrentSliceRelations(supported []operatorCodeCurrentSliceRelation, deferred []operatorCodeCurrentSliceDeferredRelation) error {
	expectedSupported := []operatorCodeCurrentSliceRelation{
		{Language: "go", Path: "go/current_slice.go", From: "SemanticEntry", Relation: "calls", To: "ExactReadTarget"},
		{Language: "typescript", Path: "worktrees/a-before/src/relay.ts", From: "readCurrentSliceAlias", Relation: "exports", To: "readCurrentSlice"},
		{Language: "tsx", Path: "worktrees/a-before/src/CurrentSlicePanel.tsx", From: "renderCurrentSlice", Relation: "imports", To: "readCurrentSliceAlias"},
		{Language: "tsx", Path: "worktrees/a-before/src/CurrentSlicePanel.tsx", From: "CurrentSlicePanel", Relation: "calls", To: "renderCurrentSlice"},
	}
	if !reflect.DeepEqual(sortedOperatorCodeCurrentSliceRelations(supported), sortedOperatorCodeCurrentSliceRelations(expectedSupported)) {
		return fmt.Errorf("supported relations = %#v, want the declared Go and bounded TS/TSX chain", supported)
	}

	expectedDeferred := []string{
		"csharp|generic_relation_support",
		"go|dynamic_dispatch",
		"python|generic_relation_support",
		"typescript|ambiguous_barrel_origin",
		"typescript|cyclic_reexport_origin",
		"vue|generic_relation_support",
	}
	actualDeferred := make([]string, 0, len(deferred))
	for _, relation := range deferred {
		if relation.Language == "" || relation.Relation == "" || relation.Reason == "" {
			return fmt.Errorf("invalid deferred relation %#v", relation)
		}
		actualDeferred = append(actualDeferred, relation.Language+"|"+relation.Relation)
	}
	sort.Strings(actualDeferred)
	if !reflect.DeepEqual(actualDeferred, expectedDeferred) {
		return fmt.Errorf("deferred relations = %#v, want explicit bounded deferrals", actualDeferred)
	}
	return nil
}

func sortedOperatorCodeCurrentSliceRelations(relations []operatorCodeCurrentSliceRelation) []operatorCodeCurrentSliceRelation {
	copy := append([]operatorCodeCurrentSliceRelation(nil), relations...)
	sort.Slice(copy, func(left, right int) bool {
		return operatorCodeCurrentSliceRelationKey(copy[left]) < operatorCodeCurrentSliceRelationKey(copy[right])
	})
	return copy
}

func operatorCodeCurrentSliceRelationKey(relation operatorCodeCurrentSliceRelation) string {
	return strings.Join([]string{relation.Language, relation.Path, relation.From, relation.Relation, relation.To}, "|")
}

func validateOperatorCodeCurrentSliceLimitations(limitations []operatorCodeCurrentSliceLimitation) error {
	expected := map[string]string{
		"semantic":  "semantic_requires_complete_selected_view_embeddings",
		"retrieval": "lexical_or_degraded_not_semantic",
		"coverage":  "bounded_or_partial_graph_not_empty",
		"graph":     "dynamic_dispatch_and_reflection_remain_unresolved",
		"view":      "exact_read_uses_persisted_selected_view_bytes",
	}
	seen := make(map[string]string, len(limitations))
	for _, limitation := range limitations {
		if limitation.Category == "" || limitation.Label == "" {
			return fmt.Errorf("invalid limitation %#v", limitation)
		}
		if _, duplicate := seen[limitation.Category]; duplicate {
			return fmt.Errorf("duplicate limitation category %q", limitation.Category)
		}
		seen[limitation.Category] = limitation.Label
	}
	for category, label := range expected {
		actual, found := seen[category]
		if !found {
			return fmt.Errorf("missing limitation category %s", category)
		}
		if actual != label {
			return fmt.Errorf("limitation %s = %q, want %q", category, actual, label)
		}
	}
	if len(seen) != len(expected) {
		return fmt.Errorf("limitation categories = %#v, want only the declared current-slice labels", seen)
	}
	return nil
}

func validateOperatorCodeCurrentSliceSameView(expectation operatorCodeCurrentSliceSameViewExpectation) error {
	viewID := expectation.Semantic.ViewID
	if viewID == "" || expectation.Semantic.Mode != "semantic" || expectation.Semantic.Query == "" {
		return fmt.Errorf("semantic expectation is incomplete %#v", expectation.Semantic)
	}
	if expectation.Graph.ViewID != viewID || expectation.ExactRead.ViewID != viewID {
		return fmt.Errorf("same-View expectation differs: semantic=%q graph=%q exact_read=%q", viewID, expectation.Graph.ViewID, expectation.ExactRead.ViewID)
	}
	if expectation.Graph.Relation != "calls" || expectation.Graph.From != "SemanticEntry" || expectation.Graph.To != "ExactReadTarget" {
		return fmt.Errorf("graph expectation = %#v, want declared Go calls relation", expectation.Graph)
	}
	if expectation.ExactRead.Path != "go/current_slice.go" || expectation.ExactRead.Source != "persisted_selected_view" || expectation.ExactRead.WorkingCopyFallback {
		return fmt.Errorf("exact-read expectation = %#v, want persisted selected-View bytes without disk fallback", expectation.ExactRead)
	}
	return nil
}

func validateOperatorCodeCurrentSliceWatcher(watcher operatorCodeCurrentSliceWatcherObservation, sources map[string]string, initialViewID string) error {
	if watcher.Trigger != "saved_write" || watcher.Mechanism != "normal_watcher_reconcile" || watcher.ManualViewPublication {
		return fmt.Errorf("watcher observation = %#v, want saved normal watcher reconciliation", watcher)
	}
	if watcher.A.Before.ViewID != initialViewID || watcher.A.Before.ViewID == watcher.A.After.ViewID || watcher.A.Before.TargetValue == watcher.A.After.TargetValue || watcher.A.Before.TargetSHA256 == watcher.A.After.TargetSHA256 {
		return fmt.Errorf("A watcher observation does not declare a distinct updated View %#v", watcher.A)
	}
	if watcher.A.Before.CallerSHA256 == "" || watcher.A.Before.CallerSHA256 != watcher.A.After.CallerSHA256 || !watcher.A.OldViewReadableUntilExplicitTransition {
		return fmt.Errorf("A watcher observation does not retain unchanged caller and old View %#v", watcher.A)
	}
	if watcher.A.After.ViewID == watcher.B.After.ViewID || watcher.A.After.TargetValue == watcher.B.After.TargetValue || watcher.A.After.TargetSHA256 == watcher.B.After.TargetSHA256 {
		return fmt.Errorf("A/B isolation confused changed A with unchanged B")
	}
	if watcher.A.Before.TargetSHA256 != operatorCodeCurrentSliceSourceDigest(sources["worktrees/a-before/src/target.ts"]) || watcher.A.After.TargetSHA256 != operatorCodeCurrentSliceSourceDigest(sources["worktrees/a-after/src/target.ts"]) || watcher.A.Before.CallerSHA256 != operatorCodeCurrentSliceSourceDigest(sources["worktrees/a-before/src/CurrentSlicePanel.tsx"]) {
		return fmt.Errorf("A watcher observation is not bound to its declared fixture sources")
	}
	if !reflect.DeepEqual(watcher.B.Before, watcher.B.After) {
		return fmt.Errorf("B isolation changed across A watcher observation: before=%#v after=%#v", watcher.B.Before, watcher.B.After)
	}
	if watcher.B.Before.ViewID == "" || watcher.B.Before.TargetValue == "" || watcher.B.Before.MembershipSnapshot == "" || watcher.B.Before.EdgeSnapshot == "" || watcher.B.Before.ExactSourceSnapshot == "" {
		return fmt.Errorf("B isolation observation is incomplete %#v", watcher.B.Before)
	}
	if watcher.B.Before.TargetSHA256 != operatorCodeCurrentSliceSourceDigest(sources["worktrees/b/src/target.ts"]) {
		return fmt.Errorf("B isolation observation is not bound to its target source")
	}
	return nil
}

func operatorCodeCurrentSliceSourceDigest(source string) string {
	digest := sha256.Sum256([]byte(source))
	return hex.EncodeToString(digest[:])
}
