package acceptance

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"fmt"
	"io"
	"reflect"
	"slices"
	"sort"
	"strings"
	"testing"
)

const (
	uciProductTasksSchemaVersion    = "engram.uci-product-tasks/v1"
	uciProductBaselineSchemaVersion = "engram.uci-product-baseline/v1"
)

//go:embed product_tasks.json
var uciProductTasksJSON []byte

//go:embed product_baseline.json
var uciProductBaselineJSON []byte

type uciProductTaskCorpus struct {
	SchemaVersion   string                  `json:"schema_version"`
	CorpusManifest  uciCorpusPrerequisite   `json:"corpus_manifest"`
	ProviderProfile uciProviderPrerequisite `json:"provider_profile"`
	Tasks           []uciProductTask        `json:"tasks"`
}

type uciCorpusPrerequisite struct {
	Status        string `json:"status"`
	Commit        string `json:"commit"`
	DirtyManifest string `json:"dirty_manifest"`
}

type uciProviderPrerequisite struct {
	Status   string `json:"status"`
	Provider string `json:"provider"`
	Model    string `json:"model"`
	Profile  string `json:"profile"`
}

type uciProductTask struct {
	ID                    string              `json:"id"`
	Category              string              `json:"category"`
	Question              string              `json:"question"`
	ExactHints            []string            `json:"exact_hints"`
	AllowedExecutionModes []string            `json:"allowed_execution_modes"`
	DirectReadEligible    bool                `json:"direct_read_eligible"`
	SemanticRequirement   string              `json:"semantic_requirement"`
	Evidence              uciEvidenceContract `json:"evidence"`
	Budget                uciToolReadBudget   `json:"budget"`
	Outcomes              uciOutcomeRules     `json:"outcomes"`
}

type uciEvidenceContract struct {
	Required     []uciEvidenceExpectation `json:"required"`
	Alternatives []uciEvidenceExpectation `json:"alternatives"`
}

type uciEvidenceExpectation struct {
	Kind      string `json:"kind"`
	Reference string `json:"reference"`
}

type uciToolReadBudget struct {
	MaxTools int `json:"max_tools"`
	MaxReads int `json:"max_reads"`
}

type uciOutcomeRules struct {
	Success string `json:"success"`
	Partial string `json:"partial"`
	Failure string `json:"failure"`
}

type uciProductBaseline struct {
	SchemaVersion string            `json:"schema_version"`
	Tasks         []uciBaselineTask `json:"tasks"`
}

type uciBaselineTask struct {
	ID       string            `json:"id"`
	Question string            `json:"question"`
	Method   string            `json:"method"`
	Budget   uciToolReadBudget `json:"budget"`
}

var uciProductTaskCategories = map[string]int{
	"conceptual":             4,
	"exact":                  2,
	"multi_hop":              2,
	"code_doc_schema":        2,
	"worktree_comparison":    1,
	"post_increment_restart": 1,
}

var uciProductTaskIDs = map[string]string{
	"UCI-P01": "conceptual", "UCI-P02": "conceptual", "UCI-P03": "conceptual", "UCI-P04": "conceptual",
	"UCI-P05": "exact", "UCI-P06": "exact",
	"UCI-P07": "multi_hop", "UCI-P08": "multi_hop",
	"UCI-P09": "code_doc_schema", "UCI-P10": "code_doc_schema",
	"UCI-P11": "worktree_comparison", "UCI-P12": "post_increment_restart",
}

func TestUCIProductTaskCorpus(t *testing.T) {
	tasks := decodeUCIProductTaskCorpus(t, uciProductTasksJSON)
	baseline := decodeUCIProductBaseline(t, uciProductBaselineJSON)

	validateUCIProductTaskCorpus(t, tasks)
	validateUCIProductBaseline(t, tasks, baseline)

	if tasks.CorpusManifest.Status != "unresolved" || tasks.CorpusManifest.Commit != "" || tasks.CorpusManifest.DirtyManifest != "" {
		t.Fatalf("UCI product corpus must retain unresolved corpus manifest placeholders before measurement: %#v", tasks.CorpusManifest)
	}
	if tasks.ProviderProfile.Status != "unresolved" || tasks.ProviderProfile.Provider != "" || tasks.ProviderProfile.Model != "" || tasks.ProviderProfile.Profile != "" {
		t.Fatalf("UCI product corpus must retain unresolved provider/profile placeholders before measurement: %#v", tasks.ProviderProfile)
	}

	t.Fatal("UCI product corpus RED: unresolved corpus manifest and provider/profile prerequisites; no measured task result may be claimed")
}

func decodeUCIProductTaskCorpus(t *testing.T, input []byte) uciProductTaskCorpus {
	t.Helper()
	var corpus uciProductTaskCorpus
	decodeUCIStrictJSON(t, input, &corpus)
	return corpus
}

func decodeUCIProductBaseline(t *testing.T, input []byte) uciProductBaseline {
	t.Helper()
	var baseline uciProductBaseline
	decodeUCIStrictJSON(t, input, &baseline)
	return baseline
}

func decodeUCIStrictJSON(t *testing.T, input []byte, target any) {
	t.Helper()
	decoder := json.NewDecoder(bytes.NewReader(input))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		t.Fatalf("strictly decode UCI product fixture: %v", err)
	}
	var extra json.RawMessage
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			t.Fatal("UCI product fixture contains more than one JSON value")
		}
		t.Fatalf("finish decoding UCI product fixture: %v", err)
	}
}

func validateUCIProductTaskCorpus(t *testing.T, corpus uciProductTaskCorpus) {
	t.Helper()
	if corpus.SchemaVersion != uciProductTasksSchemaVersion {
		t.Fatalf("product task schema version = %q, want %q", corpus.SchemaVersion, uciProductTasksSchemaVersion)
	}
	if len(corpus.Tasks) != len(uciProductTaskIDs) {
		t.Fatalf("product task count = %d, want %d", len(corpus.Tasks), len(uciProductTaskIDs))
	}

	counts := make(map[string]int, len(uciProductTaskCategories))
	seen := make(map[string]uciProductTask, len(corpus.Tasks))
	for _, task := range corpus.Tasks {
		wantCategory, knownID := uciProductTaskIDs[task.ID]
		if !knownID {
			t.Fatalf("unknown or unstable product task ID %q", task.ID)
		}
		if _, duplicate := seen[task.ID]; duplicate {
			t.Fatalf("duplicate product task ID %q", task.ID)
		}
		if task.Category != wantCategory {
			t.Fatalf("product task %s category = %q, want %q", task.ID, task.Category, wantCategory)
		}
		seen[task.ID] = task
		counts[task.Category]++
		validateUCIProductTask(t, task)
	}
	if !reflect.DeepEqual(counts, uciProductTaskCategories) {
		t.Fatalf("product task category counts = %v, want %v", sortedUCIProductTaskCounts(counts), sortedUCIProductTaskCounts(uciProductTaskCategories))
	}
}

func validateUCIProductTask(t *testing.T, task uciProductTask) {
	t.Helper()
	if strings.TrimSpace(task.Question) == "" {
		t.Fatalf("product task %s has no user question", task.ID)
	}
	if task.Budget.MaxTools < 1 || task.Budget.MaxReads < 1 {
		t.Fatalf("product task %s has unbounded or empty tool/read budget: %#v", task.ID, task.Budget)
	}
	if len(task.AllowedExecutionModes) == 0 {
		t.Fatalf("product task %s has no allowed execution mode", task.ID)
	}
	if len(task.Evidence.Required) == 0 || len(task.Evidence.Alternatives) == 0 {
		t.Fatalf("product task %s must declare required evidence and permitted alternatives", task.ID)
	}
	for _, evidence := range append(append([]uciEvidenceExpectation{}, task.Evidence.Required...), task.Evidence.Alternatives...) {
		if strings.TrimSpace(evidence.Kind) == "" || strings.TrimSpace(evidence.Reference) == "" {
			t.Fatalf("product task %s has incomplete evidence contract: %#v", task.ID, evidence)
		}
	}
	if strings.TrimSpace(task.Outcomes.Success) == "" || strings.TrimSpace(task.Outcomes.Partial) == "" || strings.TrimSpace(task.Outcomes.Failure) == "" {
		t.Fatalf("product task %s must define success, partial, and failure rules", task.ID)
	}

	if task.Category == "conceptual" {
		if len(task.ExactHints) != 0 || task.DirectReadEligible || task.SemanticRequirement != "provider_generated_vector_required" || !containsUCIProductMode(task.AllowedExecutionModes, "semantic_search") {
			t.Fatalf("conceptual product task %s permits a keyword shortcut or lacks required provider semantic evidence", task.ID)
		}
		return
	}
	if task.Category == "exact" {
		if !task.DirectReadEligible || task.SemanticRequirement != "not_required" || !containsUCIProductMode(task.AllowedExecutionModes, "direct_read") {
			t.Fatalf("exact product task %s forces graph/semantic ceremony instead of permitting direct read", task.ID)
		}
	}
}

func validateUCIProductBaseline(t *testing.T, corpus uciProductTaskCorpus, baseline uciProductBaseline) {
	t.Helper()
	if baseline.SchemaVersion != uciProductBaselineSchemaVersion {
		t.Fatalf("product baseline schema version = %q, want %q", baseline.SchemaVersion, uciProductBaselineSchemaVersion)
	}
	if len(baseline.Tasks) != len(corpus.Tasks) {
		t.Fatalf("product baseline task count = %d, want %d", len(baseline.Tasks), len(corpus.Tasks))
	}
	byID := make(map[string]uciBaselineTask, len(baseline.Tasks))
	for _, task := range baseline.Tasks {
		if _, duplicate := byID[task.ID]; duplicate {
			t.Fatalf("duplicate baseline task ID %q", task.ID)
		}
		if task.Method != "bounded_file_search_read" {
			t.Fatalf("baseline task %s method = %q, want ordinary bounded file search/read", task.ID, task.Method)
		}
		byID[task.ID] = task
	}
	for _, task := range corpus.Tasks {
		baselineTask, found := byID[task.ID]
		if !found || baselineTask.Question != task.Question || baselineTask.Budget != task.Budget {
			t.Fatalf("baseline diverges from product task identity/question/budget for %s: %#v", task.ID, baselineTask)
		}
	}
}

func containsUCIProductMode(modes []string, want string) bool {
	return slices.Contains(modes, want)
}

func sortedUCIProductTaskCounts(counts map[string]int) []string {
	values := make([]string, 0, len(counts))
	for category, count := range counts {
		values = append(values, fmt.Sprintf("%s=%d", category, count))
	}
	sort.Strings(values)
	return values
}
