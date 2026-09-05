package acceptance

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path"
	"sort"
	"strings"

	"github.com/google/uuid"
)

const (
	uciProductTasksSchemaVersion    = "engram.uci-product-tasks/v1"
	uciProductBaselineSchemaVersion = "engram.uci-product-baseline/v1"
	uciProductResultSchemaVersion   = "engram.uci-product-result/v1"
	uciProductReportSchemaVersion   = "engram.uci-product-report/v1"
	uciProductInputPathEnv          = "ENGRAM_UCI_PRODUCT_INPUT"

	uciProductTopFiveLimit           = 5
	uciProductRelationPathLimit      = 8
	uciProductRelevantTopFiveMinimum = 10
	uciProductReducedReadingMinimum  = 9
	uciProductReasonMaximumBytes     = 80
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

// UCIProductTaskResultInput is the externally recorded evidence input. It is
// evidence accounting only. The runner never executes a task or derives an
// identity from the host.
type UCIProductTaskResultInput struct {
	SchemaVersion string                 `json:"schema_version"`
	Candidate     UCIProductCandidate    `json:"candidate"`
	Corpus        UCIProductCorpus       `json:"corpus"`
	Provider      UCIProductProvider     `json:"provider"`
	Results       []UCIProductTaskResult `json:"results"`
}

// UCIProductCandidate binds every recorded task result to one candidate.
type UCIProductCandidate struct {
	Commit          string                     `json:"commit"`
	Tree            string                     `json:"tree"`
	ArtifactDigests []UCIProductArtifactDigest `json:"artifact_digests"`
}

// UCIProductArtifactDigest identifies an installed candidate artifact without
// retaining the artifact bytes.
type UCIProductArtifactDigest struct {
	Name   string `json:"name"`
	Digest string `json:"digest"`
}

// UCIProductCorpus binds the result to the committed corpus and its dirty-view
// manifest, both pre-registered in product_tasks.json.
type UCIProductCorpus struct {
	Commit        string `json:"commit"`
	DirtyManifest string `json:"dirty_manifest"`
}

// UCIProductProvider records the exact provider identity that produced semantic
// evidence when semantic search was used.
type UCIProductProvider struct {
	Provider string `json:"provider"`
	Model    string `json:"model"`
	Profile  string `json:"profile"`
}

// UCIProductTaskResult records one task outcome. Nil scalar pointers are
// rejected so a zero observed count cannot be mistaken for an omitted count.
type UCIProductTaskResult struct {
	ID                       string                       `json:"id"`
	Question                 string                       `json:"question"`
	Outcome                  string                       `json:"outcome"`
	ExecutionModes           []string                     `json:"execution_modes"`
	ToolCount                *int                         `json:"tool_count"`
	ReadCount                *int                         `json:"read_count"`
	TopFive                  []UCIProductSourceCitation   `json:"top_five"`
	RelationEvidence         []UCIProductRelationEvidence `json:"relation_evidence,omitempty"`
	DirectReadUsed           *bool                        `json:"direct_read_used"`
	SemanticProviderEvidence *UCIProductSemanticEvidence  `json:"semantic_provider_evidence,omitempty"`
	Baseline                 UCIProductBaselineResult     `json:"baseline"`
	Reason                   string                       `json:"reason,omitempty"`
}

// UCIProductSourceCitation is a bounded, source-body-free citation. Its
// evidence reference selects a pre-registered contract anchor while Context
// identifies the immutable UCI source, checkout, view, generation, and profile
// that actually supplied the citation.
type UCIProductSourceCitation struct {
	Rank              int                  `json:"rank"`
	EvidenceReference string               `json:"evidence_reference"`
	Context           UCIProductContextRef `json:"context"`
	Path              string               `json:"path"`
	Span              UCIProductSpan       `json:"span"`
	Digest            string               `json:"digest"`
}

// UCIProductContextRef is the immutable context binding for a cited source.
// SpaceID is optional; every other ID is a canonical non-nil UUID.
type UCIProductContextRef struct {
	SpaceID           string `json:"space_id,omitempty"`
	SourceID          string `json:"source_id"`
	CheckoutID        string `json:"checkout_id"`
	ViewID            string `json:"view_id"`
	Generation        int64  `json:"generation"`
	AnalysisProfileID string `json:"analysis_profile_id"`
}

// UCIProductSpan locates a source citation without retaining source text.
// ByteEnd is exclusive; LineEnd is inclusive.
type UCIProductSpan struct {
	ByteStart int64 `json:"byte_start"`
	ByteEnd   int64 `json:"byte_end"`
	LineStart int64 `json:"line_start"`
	LineEnd   int64 `json:"line_end"`
}

type uciProductCitationBinding struct {
	EvidenceReference string
	Context           UCIProductContextRef
}

// UCIProductRelationEvidence records an optional bounded relation path when a
// cited contract requires relation-path evidence. It must identify the actual
// top-five citation context and rank it strengthens.
type UCIProductRelationEvidence struct {
	EvidenceReference string               `json:"evidence_reference"`
	CitationRank      int                  `json:"citation_rank"`
	CitationContext   UCIProductContextRef `json:"citation_context"`
	Path              []string             `json:"path"`
	Digest            string               `json:"digest"`
}

// UCIProductSemanticEvidence proves that semantic search used the bound
// provider profile and a provider-generated vector artifact, not keywords.
type UCIProductSemanticEvidence struct {
	Provider     string `json:"provider"`
	Model        string `json:"model"`
	Profile      string `json:"profile"`
	VectorDigest string `json:"vector_digest"`
}

// UCIProductBaselineResult preserves the observed baseline comparison for the
// same task. It intentionally records counts and correctness rather than a
// score.
type UCIProductBaselineResult struct {
	TaskID    string `json:"task_id"`
	Question  string `json:"question"`
	ToolCount *int   `json:"tool_count"`
	ReadCount *int   `json:"read_count"`
	Correct   *bool  `json:"correct"`
}

// UCIProductTaskReport is deterministic acceptance accounting over the
// supplied result. Its summaries retain every supplied outcome but never copy
// source bodies, paths, spans, or reasons into the report.
type UCIProductTaskReport struct {
	SchemaVersion              string                        `json:"schema_version"`
	Candidate                  UCIProductCandidate           `json:"candidate"`
	Corpus                     UCIProductCorpus              `json:"corpus"`
	Provider                   UCIProductProvider            `json:"provider"`
	Results                    []UCIProductTaskResultSummary `json:"results"`
	RelevantTopFiveCount       int                           `json:"relevant_top_five_count"`
	RelevantTopFiveMinimum     int                           `json:"relevant_top_five_minimum"`
	RelevantTopFiveDenominator int                           `json:"relevant_top_five_denominator"`
	ReducedReadingCount        int                           `json:"reduced_reading_count"`
	ReducedReadingMinimum      int                           `json:"reduced_reading_minimum"`
	ReducedReadingDenominator  int                           `json:"reduced_reading_denominator"`
	Passed                     bool                          `json:"passed"`
	Violations                 []UCIProductTaskViolation     `json:"violations"`
}

// UCIProductTaskResultSummary preserves the outcome and observed counters for
// each supplied task while keeping the report free of raw source evidence.
type UCIProductTaskResultSummary struct {
	ID                                      string `json:"id"`
	Question                                string `json:"question"`
	Outcome                                 string `json:"outcome"`
	ToolCount                               int    `json:"tool_count"`
	ReadCount                               int    `json:"read_count"`
	BaselineCorrect                         bool   `json:"baseline_correct"`
	TopFiveRelevant                         bool   `json:"top_five_relevant"`
	ReducedReadingWithoutReducedCorrectness bool   `json:"reduced_reading_without_reduced_correctness"`
}

// UCIProductTaskViolation identifies one deterministic validation failure.
type UCIProductTaskViolation struct {
	TaskID  string `json:"task_id,omitempty"`
	Code    string `json:"code"`
	Message string `json:"message"`
}

// RunUCIProductTaskCorpus reads one externally recorded exact-candidate result
// from ENGRAM_UCI_PRODUCT_INPUT and deterministically accounts for it. It does
// not run product tasks, infer identities, or manufacture a passing result.
func RunUCIProductTaskCorpus(ctx context.Context) (UCIProductTaskReport, error) {
	if err := ctx.Err(); err != nil {
		return UCIProductTaskReport{}, fmt.Errorf("UCI product result context: %w", err)
	}

	corpus, baseline, err := loadUCIProductTaskContracts()
	if err != nil {
		return UCIProductTaskReport{}, err
	}
	prerequisite := uciProductPrerequisiteFailure(corpus)
	inputPath := strings.TrimSpace(os.Getenv(uciProductInputPathEnv))
	if inputPath == "" {
		if prerequisite != "" {
			return UCIProductTaskReport{}, fmt.Errorf("UCI product task corpus RED prerequisite: %s; an externally recorded exact-candidate result is required via %s", prerequisite, uciProductInputPathEnv)
		}
		return UCIProductTaskReport{}, fmt.Errorf("UCI product task corpus RED prerequisite: an externally recorded exact-candidate result is required via %s; refusing to fabricate task outcomes", uciProductInputPathEnv)
	}

	contents, err := os.ReadFile(inputPath)
	if err != nil {
		return UCIProductTaskReport{}, fmt.Errorf("UCI product task corpus RED prerequisite: %s points to an unreadable externally recorded result", uciProductInputPathEnv)
	}
	if err := ctx.Err(); err != nil {
		return UCIProductTaskReport{}, fmt.Errorf("UCI product result context: %w", err)
	}

	var input UCIProductTaskResultInput
	if err := decodeUCIStrictJSON(contents, &input); err != nil {
		return UCIProductTaskReport{}, fmt.Errorf("strictly decode externally recorded UCI product result: %w", err)
	}
	if prerequisite != "" {
		return UCIProductTaskReport{}, fmt.Errorf("UCI product task corpus RED prerequisite: %s; refusing to bind a result to unresolved corpus or provider identities", prerequisite)
	}

	return validateUCIProductTaskResultInput(input, corpus, baseline)
}

func loadUCIProductTaskContracts() (uciProductTaskCorpus, uciProductBaseline, error) {
	var corpus uciProductTaskCorpus
	if err := decodeUCIStrictJSON(uciProductTasksJSON, &corpus); err != nil {
		return uciProductTaskCorpus{}, uciProductBaseline{}, fmt.Errorf("strictly decode UCI product task fixture: %w", err)
	}
	var baseline uciProductBaseline
	if err := decodeUCIStrictJSON(uciProductBaselineJSON, &baseline); err != nil {
		return uciProductTaskCorpus{}, uciProductBaseline{}, fmt.Errorf("strictly decode UCI product baseline fixture: %w", err)
	}
	if err := validateUCIProductTaskContracts(corpus, baseline); err != nil {
		return uciProductTaskCorpus{}, uciProductBaseline{}, fmt.Errorf("validate pre-registered UCI product task contracts: %w", err)
	}
	return corpus, baseline, nil
}

func decodeUCIStrictJSON(input []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(input))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var extra json.RawMessage
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return fmt.Errorf("input contains more than one JSON value")
		}
		return err
	}
	return nil
}

func validateUCIProductTaskContracts(corpus uciProductTaskCorpus, baseline uciProductBaseline) error {
	if corpus.SchemaVersion != uciProductTasksSchemaVersion {
		return fmt.Errorf("product task schema version is not accepted")
	}
	if err := validateUCIProductPrerequisiteRegistration(corpus); err != nil {
		return err
	}
	if len(corpus.Tasks) != len(uciProductTaskIDs) {
		return fmt.Errorf("product task count is not twelve")
	}

	counts := make(map[string]int, len(uciProductTaskCategories))
	seen := make(map[string]struct{}, len(corpus.Tasks))
	for _, task := range corpus.Tasks {
		wantCategory, knownID := uciProductTaskIDs[task.ID]
		if !knownID {
			return fmt.Errorf("product task has an unknown or unstable ID")
		}
		if _, duplicate := seen[task.ID]; duplicate {
			return fmt.Errorf("product task ID is duplicated")
		}
		if task.Category != wantCategory {
			return fmt.Errorf("product task category does not match its stable ID")
		}
		if err := validateUCIProductTaskContract(task); err != nil {
			return err
		}
		seen[task.ID] = struct{}{}
		counts[task.Category]++
	}
	for category, want := range uciProductTaskCategories {
		if counts[category] != want {
			return fmt.Errorf("product task category count does not match the pre-registration")
		}
	}

	if baseline.SchemaVersion != uciProductBaselineSchemaVersion {
		return fmt.Errorf("product baseline schema version is not accepted")
	}
	if len(baseline.Tasks) != len(corpus.Tasks) {
		return fmt.Errorf("product baseline task count does not match the corpus")
	}
	baselineByID := make(map[string]uciBaselineTask, len(baseline.Tasks))
	for _, task := range baseline.Tasks {
		if _, duplicate := baselineByID[task.ID]; duplicate {
			return fmt.Errorf("baseline task ID is duplicated")
		}
		if task.Method != "bounded_file_search_read" {
			return fmt.Errorf("baseline task method is not the declared bounded file search/read method")
		}
		baselineByID[task.ID] = task
	}
	for _, task := range corpus.Tasks {
		baselineTask, found := baselineByID[task.ID]
		if !found || baselineTask.Question != task.Question || baselineTask.Budget != task.Budget {
			return fmt.Errorf("baseline diverges from product task identity, question, or budget")
		}
	}
	return nil
}

func validateUCIProductPrerequisiteRegistration(corpus uciProductTaskCorpus) error {
	switch corpus.CorpusManifest.Status {
	case "unresolved":
		if corpus.CorpusManifest.Commit != "" || corpus.CorpusManifest.DirtyManifest != "" {
			return fmt.Errorf("unresolved corpus manifest carries an invented identity")
		}
	case "resolved":
		if !validUCIProductGitRevision(corpus.CorpusManifest.Commit) || !validUCIProductDigest(corpus.CorpusManifest.DirtyManifest) {
			return fmt.Errorf("resolved corpus manifest lacks exact commit or dirty-manifest digest")
		}
	default:
		return fmt.Errorf("corpus manifest status is not accepted")
	}

	switch corpus.ProviderProfile.Status {
	case "unresolved":
		if corpus.ProviderProfile.Provider != "" || corpus.ProviderProfile.Model != "" || corpus.ProviderProfile.Profile != "" {
			return fmt.Errorf("unresolved provider profile carries an invented identity")
		}
	case "resolved":
		if !validUCIProductBoundedText(corpus.ProviderProfile.Provider, 200) || !validUCIProductBoundedText(corpus.ProviderProfile.Model, 200) || !validUCIProductBoundedText(corpus.ProviderProfile.Profile, 200) {
			return fmt.Errorf("resolved provider profile lacks an exact provider, model, or profile")
		}
	default:
		return fmt.Errorf("provider profile status is not accepted")
	}
	return nil
}

func validateUCIProductTaskContract(task uciProductTask) error {
	if !validUCIProductBoundedText(task.Question, 1_000) {
		return fmt.Errorf("product task has no user question")
	}
	if task.Budget.MaxTools < 1 || task.Budget.MaxReads < 1 {
		return fmt.Errorf("product task has an unbounded or empty tool/read budget")
	}
	if len(task.AllowedExecutionModes) == 0 {
		return fmt.Errorf("product task has no allowed execution mode")
	}
	seenModes := make(map[string]struct{}, len(task.AllowedExecutionModes))
	for _, mode := range task.AllowedExecutionModes {
		if !validUCIProductMode(mode) {
			return fmt.Errorf("product task has an unsupported execution mode")
		}
		if _, duplicate := seenModes[mode]; duplicate {
			return fmt.Errorf("product task repeats an allowed execution mode")
		}
		seenModes[mode] = struct{}{}
	}
	if len(task.Evidence.Required) == 0 || len(task.Evidence.Alternatives) == 0 {
		return fmt.Errorf("product task does not declare required and permitted evidence")
	}
	for _, evidence := range append(append([]uciEvidenceExpectation{}, task.Evidence.Required...), task.Evidence.Alternatives...) {
		if (evidence.Kind != "path_span" && evidence.Kind != "relation_path") || !validUCIProductBoundedText(evidence.Reference, 512) {
			return fmt.Errorf("product task has incomplete evidence contract")
		}
	}
	if !validUCIProductBoundedText(task.Outcomes.Success, 512) || !validUCIProductBoundedText(task.Outcomes.Partial, 512) || !validUCIProductBoundedText(task.Outcomes.Failure, 512) {
		return fmt.Errorf("product task does not define success, partial, and failure rules")
	}

	switch task.Category {
	case "conceptual":
		if len(task.ExactHints) != 0 || task.DirectReadEligible || task.SemanticRequirement != "provider_generated_vector_required" || !containsUCIProductMode(task.AllowedExecutionModes, "semantic_search") {
			return fmt.Errorf("conceptual product task permits a keyword shortcut or lacks provider semantic evidence")
		}
	case "exact":
		if !task.DirectReadEligible || task.SemanticRequirement != "not_required" || !containsUCIProductMode(task.AllowedExecutionModes, "direct_read") {
			return fmt.Errorf("exact product task forces graph or semantic ceremony instead of permitting direct read")
		}
	case "multi_hop", "code_doc_schema", "worktree_comparison", "post_increment_restart":
		if task.DirectReadEligible || task.SemanticRequirement != "not_required" {
			return fmt.Errorf("non-exact product task has an incompatible direct-read or semantic contract")
		}
	default:
		return fmt.Errorf("product task category is not accepted")
	}
	return nil
}

func uciProductPrerequisiteFailure(corpus uciProductTaskCorpus) string {
	var failures []string
	if corpus.CorpusManifest.Status != "resolved" || !validUCIProductGitRevision(corpus.CorpusManifest.Commit) || !validUCIProductDigest(corpus.CorpusManifest.DirtyManifest) {
		failures = append(failures, "pre-registered corpus manifest is unresolved")
	}
	if corpus.ProviderProfile.Status != "resolved" || !validUCIProductBoundedText(corpus.ProviderProfile.Provider, 200) || !validUCIProductBoundedText(corpus.ProviderProfile.Model, 200) || !validUCIProductBoundedText(corpus.ProviderProfile.Profile, 200) {
		failures = append(failures, "pre-registered provider profile is unresolved")
	}
	return strings.Join(failures, "; ")
}

func validateUCIProductTaskResultInput(input UCIProductTaskResultInput, corpus uciProductTaskCorpus, baseline uciProductBaseline) (UCIProductTaskReport, error) {
	report := UCIProductTaskReport{
		SchemaVersion:              uciProductReportSchemaVersion,
		RelevantTopFiveMinimum:     uciProductRelevantTopFiveMinimum,
		RelevantTopFiveDenominator: len(corpus.Tasks),
		ReducedReadingMinimum:      uciProductReducedReadingMinimum,
		ReducedReadingDenominator:  len(corpus.Tasks),
	}
	violations := uciProductViolationCollector{}

	if input.SchemaVersion != uciProductResultSchemaVersion {
		violations.add("", "unsupported_schema", "result schema is not the accepted UCI product result schema")
	}
	bindingStart := len(violations.values)
	validateUCIProductResultBinding(input, corpus, &violations)
	if len(violations.values) == bindingStart {
		report.Candidate = input.Candidate
		report.Corpus = input.Corpus
		report.Provider = input.Provider
	}

	if len(input.Results) != len(corpus.Tasks) {
		violations.add("", "result_count", "result does not retain exactly the twelve pre-registered tasks")
	}

	contractsByID := make(map[string]uciProductTask, len(corpus.Tasks))
	for _, task := range corpus.Tasks {
		contractsByID[task.ID] = task
	}
	baselineByID := make(map[string]uciBaselineTask, len(baseline.Tasks))
	for _, task := range baseline.Tasks {
		baselineByID[task.ID] = task
	}

	seen := make(map[string]struct{}, len(input.Results))
	for _, result := range input.Results {
		task, knownTask := contractsByID[result.ID]
		if !knownTask {
			violations.add("", "unexpected_task", "result includes a task outside the pre-registered corpus")
			report.Results = append(report.Results, UCIProductTaskResultSummary{})
			continue
		}
		if _, duplicate := seen[result.ID]; duplicate {
			violations.add(task.ID, "duplicate_task", "result repeats a pre-registered task")
			summary, _, _ := validateUCIProductTaskResult(result, task, baselineByID[task.ID], input.Provider, &violations)
			report.Results = append(report.Results, summary)
			continue
		}
		seen[result.ID] = struct{}{}

		summary, relevant, reducedReading := validateUCIProductTaskResult(result, task, baselineByID[task.ID], input.Provider, &violations)
		report.Results = append(report.Results, summary)
		if relevant {
			report.RelevantTopFiveCount++
		}
		if reducedReading {
			report.ReducedReadingCount++
		}
	}
	for _, task := range corpus.Tasks {
		if _, found := seen[task.ID]; !found {
			violations.add(task.ID, "missing_task", "result omits a pre-registered task")
		}
	}

	if report.RelevantTopFiveCount < report.RelevantTopFiveMinimum {
		violations.add("", "relevant_top_five_threshold", "fewer than ten tasks have relevant, contract-bound top-five evidence")
	}
	if report.ReducedReadingCount < report.ReducedReadingMinimum {
		violations.add("", "reduced_reading_threshold", "fewer than nine tasks reduce reads without reducing correctness")
	}

	report.Violations = violations.sorted()
	report.Passed = len(report.Violations) == 0
	if !report.Passed {
		return report, fmt.Errorf("externally recorded UCI product result is not accepted: %s", uciProductViolationCodes(report.Violations))
	}
	return report, nil
}

func validateUCIProductResultBinding(input UCIProductTaskResultInput, corpus uciProductTaskCorpus, violations *uciProductViolationCollector) {
	if !validUCIProductGitRevision(input.Candidate.Commit) {
		violations.add("", "candidate_commit", "candidate commit is not an exact revision")
	}
	if !validUCIProductGitRevision(input.Candidate.Tree) {
		violations.add("", "candidate_tree", "candidate tree is not an exact revision")
	}
	if len(input.Candidate.ArtifactDigests) == 0 {
		violations.add("", "candidate_artifacts", "candidate omits installed artifact digests")
	}
	artifactNames := make(map[string]struct{}, len(input.Candidate.ArtifactDigests))
	for _, artifact := range input.Candidate.ArtifactDigests {
		if !validUCIProductArtifactName(artifact.Name) || !validUCIProductDigest(artifact.Digest) {
			violations.add("", "candidate_artifact", "candidate has an incomplete installed artifact digest")
			continue
		}
		if _, duplicate := artifactNames[artifact.Name]; duplicate {
			violations.add("", "candidate_artifact_duplicate", "candidate repeats an installed artifact identity")
			continue
		}
		artifactNames[artifact.Name] = struct{}{}
	}

	if input.Corpus.Commit != corpus.CorpusManifest.Commit || !validUCIProductGitRevision(input.Corpus.Commit) {
		violations.add("", "corpus_commit_binding", "result corpus commit does not match the pre-registered corpus")
	}
	if input.Corpus.DirtyManifest != corpus.CorpusManifest.DirtyManifest || !validUCIProductDigest(input.Corpus.DirtyManifest) {
		violations.add("", "corpus_dirty_manifest_binding", "result dirty manifest does not match the pre-registered corpus")
	}

	if input.Provider.Provider != corpus.ProviderProfile.Provider || !validUCIProductBoundedText(input.Provider.Provider, 200) {
		violations.add("", "provider_binding", "result provider does not match the pre-registered provider profile")
	}
	if input.Provider.Model != corpus.ProviderProfile.Model || !validUCIProductBoundedText(input.Provider.Model, 200) {
		violations.add("", "model_binding", "result model does not match the pre-registered provider profile")
	}
	if input.Provider.Profile != corpus.ProviderProfile.Profile || !validUCIProductBoundedText(input.Provider.Profile, 200) {
		violations.add("", "profile_binding", "result profile does not match the pre-registered provider profile")
	}
}

func validateUCIProductTaskResult(result UCIProductTaskResult, task uciProductTask, baseline uciBaselineTask, provider UCIProductProvider, violations *uciProductViolationCollector) (UCIProductTaskResultSummary, bool, bool) {
	start := len(violations.values)
	summary := UCIProductTaskResultSummary{ID: task.ID}

	if result.Question != task.Question {
		violations.add(task.ID, "question_binding", "result question does not match the pre-registered task")
	} else {
		summary.Question = task.Question
	}
	outcomeValid := validUCIProductOutcome(result.Outcome)
	if !outcomeValid {
		violations.add(task.ID, "outcome", "result outcome must be success, partial, or failure")
	} else {
		summary.Outcome = result.Outcome
	}

	toolCountValid := validateUCIProductCount(task.ID, "tool_count", result.ToolCount, task.Budget.MaxTools, violations)
	if toolCountValid {
		summary.ToolCount = *result.ToolCount
	}
	readCountValid := validateUCIProductCount(task.ID, "read_count", result.ReadCount, task.Budget.MaxReads, violations)
	if readCountValid {
		summary.ReadCount = *result.ReadCount
	}

	validateUCIProductExecutionModes(result, task, provider, outcomeValid, violations)
	if outcomeValid {
		switch result.Outcome {
		case "success":
			if result.Reason != "" {
				violations.add(task.ID, "success_reason", "successful result must not carry a degraded or failure reason")
			}
		case "partial", "failure":
			if !validUCIProductReason(result.Reason) {
				violations.add(task.ID, "outcome_reason", "partial or failed result must retain a bounded reason code")
			}
		}
	}

	evidenceSatisfied := validateUCIProductEvidence(result, task, violations)
	if outcomeValid && result.Outcome == "success" && !evidenceSatisfied {
		violations.add(task.ID, "required_evidence", "successful result lacks pre-registered required or permitted alternative evidence")
	}

	baselineValid, baselineCorrect, baselineReads := validateUCIProductBaselineResult(result.Baseline, task, baseline, violations)
	if baselineValid {
		summary.BaselineCorrect = baselineCorrect
	}

	validForCredit := len(violations.values) == start
	relevant := validForCredit && result.Outcome == "success" && evidenceSatisfied
	reducedReading := relevant && baselineValid && baselineCorrect && readCountValid && *result.ReadCount < baselineReads
	summary.TopFiveRelevant = relevant
	summary.ReducedReadingWithoutReducedCorrectness = reducedReading

	return summary, relevant, reducedReading
}

func validateUCIProductCount(taskID, field string, value *int, maximum int, violations *uciProductViolationCollector) bool {
	if value == nil {
		violations.add(taskID, field+"_missing", "result omits an observed "+field)
		return false
	}
	if *value < 0 || *value > maximum {
		violations.add(taskID, field+"_budget", "result "+field+" is outside the pre-registered budget")
		return false
	}
	return true
}

func validateUCIProductExecutionModes(result UCIProductTaskResult, task uciProductTask, provider UCIProductProvider, outcomeValid bool, violations *uciProductViolationCollector) {
	modes := make(map[string]struct{}, len(result.ExecutionModes))
	if result.ExecutionModes == nil {
		violations.add(task.ID, "execution_modes_missing", "result omits actual execution modes")
	}
	allowed := make(map[string]struct{}, len(task.AllowedExecutionModes))
	for _, mode := range task.AllowedExecutionModes {
		allowed[mode] = struct{}{}
	}
	for _, mode := range result.ExecutionModes {
		if _, permitted := allowed[mode]; !permitted {
			violations.add(task.ID, "execution_mode", "result uses an execution mode not permitted by the task")
			continue
		}
		if _, duplicate := modes[mode]; duplicate {
			violations.add(task.ID, "execution_mode_duplicate", "result repeats an actual execution mode")
			continue
		}
		modes[mode] = struct{}{}
	}
	if outcomeValid && result.Outcome == "success" && len(modes) == 0 {
		violations.add(task.ID, "execution_mode_missing", "successful result records no actual execution mode")
	}

	_, directReadMode := modes["direct_read"]
	if result.DirectReadUsed == nil {
		violations.add(task.ID, "direct_read_missing", "result omits direct-read eligibility use")
	} else {
		if *result.DirectReadUsed != directReadMode {
			violations.add(task.ID, "direct_read_binding", "direct-read use does not match the recorded execution modes")
		}
		if *result.DirectReadUsed && !task.DirectReadEligible {
			violations.add(task.ID, "direct_read_ineligible", "result claims direct read for an ineligible task")
		}
	}

	_, semanticSearch := modes["semantic_search"]
	if outcomeValid && result.Outcome == "success" && task.SemanticRequirement == "provider_generated_vector_required" && !semanticSearch {
		violations.add(task.ID, "semantic_mode_required", "successful semantic task records no semantic search mode")
	}
	semanticEvidenceRequired := outcomeValid && result.Outcome == "success" && (semanticSearch || task.SemanticRequirement == "provider_generated_vector_required")
	if semanticEvidenceRequired {
		if result.SemanticProviderEvidence == nil {
			violations.add(task.ID, "semantic_evidence_required", "semantic result lacks provider-generated vector evidence")
		} else {
			validateUCIProductSemanticEvidence(task.ID, *result.SemanticProviderEvidence, provider, violations)
		}
	} else if result.SemanticProviderEvidence != nil {
		if !semanticSearch {
			violations.add(task.ID, "semantic_evidence_unbound", "semantic provider evidence is present without semantic search")
		} else {
			validateUCIProductSemanticEvidence(task.ID, *result.SemanticProviderEvidence, provider, violations)
		}
	}
}

func validateUCIProductSemanticEvidence(taskID string, evidence UCIProductSemanticEvidence, provider UCIProductProvider, violations *uciProductViolationCollector) {
	if evidence.Provider != provider.Provider || !validUCIProductBoundedText(evidence.Provider, 200) {
		violations.add(taskID, "semantic_provider_binding", "semantic evidence provider does not match the result binding")
	}
	if evidence.Model != provider.Model || !validUCIProductBoundedText(evidence.Model, 200) {
		violations.add(taskID, "semantic_model_binding", "semantic evidence model does not match the result binding")
	}
	if evidence.Profile != provider.Profile || !validUCIProductBoundedText(evidence.Profile, 200) {
		violations.add(taskID, "semantic_profile_binding", "semantic evidence profile does not match the result binding")
	}
	if !validUCIProductDigest(evidence.VectorDigest) {
		violations.add(taskID, "semantic_vector_digest", "semantic evidence lacks a provider-generated vector digest")
	}
}

func validateUCIProductEvidence(result UCIProductTaskResult, task uciProductTask, violations *uciProductViolationCollector) bool {
	if result.TopFive == nil {
		violations.add(task.ID, "top_five_missing", "result omits ranked top-five citations")
		return false
	}
	if len(result.TopFive) > uciProductTopFiveLimit {
		violations.add(task.ID, "top_five_limit", "result exceeds the top-five citation limit")
	}
	if result.Outcome == "success" && len(result.TopFive) == 0 {
		violations.add(task.ID, "top_five_empty", "successful result omits ranked source citations")
	}

	expectedKinds := make(map[string]string, len(task.Evidence.Required)+len(task.Evidence.Alternatives))
	for _, evidence := range task.Evidence.Required {
		expectedKinds[evidence.Reference] = evidence.Kind
	}
	for _, evidence := range task.Evidence.Alternatives {
		expectedKinds[evidence.Reference] = evidence.Kind
	}

	validCitations := make(map[int]UCIProductSourceCitation, len(result.TopFive))
	seenCitations := make(map[uciProductCitationBinding]struct{}, len(result.TopFive))
	for index, citation := range result.TopFive {
		citationValid := true
		if citation.Rank != index+1 {
			violations.add(task.ID, "citation_rank", "top-five citations must have contiguous ranks beginning at one")
			citationValid = false
		}
		if _, permitted := expectedKinds[citation.EvidenceReference]; !permitted {
			violations.add(task.ID, "citation_evidence_reference", "citation evidence reference is not permitted by the task evidence contract")
			citationValid = false
		}
		binding := uciProductCitationBinding{EvidenceReference: citation.EvidenceReference, Context: citation.Context}
		if _, duplicate := seenCitations[binding]; duplicate {
			violations.add(task.ID, "citation_duplicate", "top-five citations repeat an evidence reference and immutable context")
			citationValid = false
		}
		seenCitations[binding] = struct{}{}
		if !validUCIProductContextRef(citation.Context) {
			violations.add(task.ID, "citation_context", "citation context must be a canonical immutable UCI context reference")
			citationValid = false
		}
		if !validUCIProductRelativePath(citation.Path) || !validUCIProductSpan(citation.Span) || !validUCIProductDigest(citation.Digest) {
			violations.add(task.ID, "citation_shape", "citation must contain a relative path, ordered numeric span, and exact digest")
			citationValid = false
		}
		if citationValid {
			validCitations[citation.Rank] = citation
		}
	}

	validRelations := make(map[int]struct{}, len(result.RelationEvidence))
	seenRelationRanks := make(map[int]struct{}, len(result.RelationEvidence))
	if len(result.RelationEvidence) > uciProductTopFiveLimit {
		violations.add(task.ID, "relation_evidence_limit", "relation evidence exceeds the bounded top-five limit")
	}
	for _, relation := range result.RelationEvidence {
		relationValid := true
		if expectedKinds[relation.EvidenceReference] != "relation_path" {
			violations.add(task.ID, "relation_evidence_reference", "relation evidence is not permitted by the task evidence contract")
			relationValid = false
		}
		if relation.CitationRank < 1 || relation.CitationRank > uciProductTopFiveLimit {
			violations.add(task.ID, "relation_rank", "relation evidence must identify a positive top-five citation rank")
			relationValid = false
		}
		citation, cited := validCitations[relation.CitationRank]
		if !cited {
			violations.add(task.ID, "relation_without_citation", "relation evidence must bind a valid top-five citation")
			relationValid = false
		} else {
			if citation.EvidenceReference != relation.EvidenceReference {
				violations.add(task.ID, "relation_reference_binding", "relation evidence reference does not match its cited source")
				relationValid = false
			}
			if citation.Context != relation.CitationContext {
				violations.add(task.ID, "relation_context_binding", "relation evidence context does not match its cited source")
				relationValid = false
			}
		}
		if _, duplicate := seenRelationRanks[relation.CitationRank]; duplicate {
			violations.add(task.ID, "relation_duplicate", "relation evidence repeats a cited top-five rank")
			relationValid = false
		}
		seenRelationRanks[relation.CitationRank] = struct{}{}
		if !validUCIProductContextRef(relation.CitationContext) {
			violations.add(task.ID, "relation_context", "relation evidence must retain a canonical cited context reference")
			relationValid = false
		}
		if len(relation.Path) < 2 || len(relation.Path) > uciProductRelationPathLimit || !validUCIProductDigest(relation.Digest) {
			violations.add(task.ID, "relation_shape", "relation evidence must contain a bounded path and exact digest")
			relationValid = false
		}
		for _, hop := range relation.Path {
			if !validUCIProductRelationHop(hop) {
				violations.add(task.ID, "relation_path", "relation evidence contains an unbounded or private path hop")
				relationValid = false
				break
			}
		}
		if relationValid {
			validRelations[relation.CitationRank] = struct{}{}
		}
	}

	for rank, citation := range validCitations {
		switch expectedKinds[citation.EvidenceReference] {
		case "path_span":
			return true
		case "relation_path":
			if _, boundedRelation := validRelations[rank]; boundedRelation {
				return true
			}
		}
	}
	return false
}

func validateUCIProductBaselineResult(result UCIProductBaselineResult, task uciProductTask, baseline uciBaselineTask, violations *uciProductViolationCollector) (bool, bool, int) {
	valid := true
	if result.TaskID != task.ID || result.Question != task.Question {
		violations.add(task.ID, "baseline_task_binding", "baseline result does not identify the same task and question")
		valid = false
	}
	if result.ToolCount == nil || *result.ToolCount < 0 || (result.ToolCount != nil && *result.ToolCount > baseline.Budget.MaxTools) {
		violations.add(task.ID, "baseline_tool_count", "baseline tool count is missing or outside the pre-registered baseline budget")
		valid = false
	}
	if result.ReadCount == nil || *result.ReadCount < 0 || (result.ReadCount != nil && *result.ReadCount > baseline.Budget.MaxReads) {
		violations.add(task.ID, "baseline_read_count", "baseline read count is missing or outside the pre-registered baseline budget")
		valid = false
	}
	if result.Correct == nil {
		violations.add(task.ID, "baseline_correctness", "baseline correctness is missing")
		valid = false
	}
	if !valid {
		return false, false, 0
	}
	return true, *result.Correct, *result.ReadCount
}

func validUCIProductMode(mode string) bool {
	switch mode {
	case "semantic_search", "graph", "bounded_read", "direct_read", "bounded_file_search_read":
		return true
	default:
		return false
	}
}

func validUCIProductOutcome(outcome string) bool {
	switch outcome {
	case "success", "partial", "failure":
		return true
	default:
		return false
	}
}

func containsUCIProductMode(modes []string, want string) bool {
	for _, mode := range modes {
		if mode == want {
			return true
		}
	}
	return false
}

func validUCIProductGitRevision(value string) bool {
	return validUCIProductHex(value, 40, 64)
}

func validUCIProductDigest(value string) bool {
	if strings.HasPrefix(value, "sha256:") {
		value = strings.TrimPrefix(value, "sha256:")
		return validUCIProductHex(value, 64)
	}
	return validUCIProductHex(value, 40, 64)
}

func validUCIProductHex(value string, lengths ...int) bool {
	matchedLength := false
	for _, length := range lengths {
		if len(value) == length {
			matchedLength = true
			break
		}
	}
	if !matchedLength {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func validUCIProductArtifactName(value string) bool {
	return validUCIProductBoundedText(value, 200) && !uciProductAbsolutePath(value)
}

func validUCIProductRelativePath(value string) bool {
	if !validUCIProductBoundedText(value, 512) || uciProductAbsolutePath(value) || strings.Contains(value, "\\") {
		return false
	}
	cleaned := path.Clean(value)
	return cleaned == value && cleaned != "." && cleaned != ".." && !strings.HasPrefix(cleaned, "../")
}

func validUCIProductSpan(span UCIProductSpan) bool {
	return span.ByteStart >= 0 && span.ByteEnd > span.ByteStart && span.LineStart >= 1 && span.LineEnd >= span.LineStart
}

func validUCIProductContextRef(reference UCIProductContextRef) bool {
	if !validUCIProductUUID(reference.SourceID) || !validUCIProductUUID(reference.CheckoutID) || !validUCIProductUUID(reference.ViewID) || !validUCIProductUUID(reference.AnalysisProfileID) || reference.Generation <= 0 {
		return false
	}
	return reference.SpaceID == "" || validUCIProductUUID(reference.SpaceID)
}

func validUCIProductUUID(value string) bool {
	if len(value) != 36 {
		return false
	}
	parsed, err := uuid.Parse(value)
	return err == nil && parsed != uuid.Nil && parsed.String() == value
}

func validUCIProductRelationHop(value string) bool {
	return validUCIProductBoundedText(value, 200) && !uciProductAbsolutePath(value)
}

func validUCIProductBoundedText(value string, maximumBytes int) bool {
	return value != "" && value == strings.TrimSpace(value) && len(value) <= maximumBytes && !strings.ContainsAny(value, "\r\n\x00")
}

func validUCIProductReason(value string) bool {
	if !validUCIProductBoundedText(value, uciProductReasonMaximumBytes) {
		return false
	}
	for _, character := range value {
		if (character >= 'a' && character <= 'z') || (character >= '0' && character <= '9') || character == '_' || character == '-' || character == '.' {
			continue
		}
		return false
	}
	return true
}

func uciProductAbsolutePath(value string) bool {
	if path.IsAbs(value) || strings.HasPrefix(value, `\\`) || strings.HasPrefix(value, "//") {
		return true
	}
	return len(value) >= 3 && ((value[0] >= 'a' && value[0] <= 'z') || (value[0] >= 'A' && value[0] <= 'Z')) && value[1] == ':' && (value[2] == '/' || value[2] == '\\')
}

type uciProductViolationCollector struct {
	values []UCIProductTaskViolation
}

func (collector *uciProductViolationCollector) add(taskID, code, message string) {
	collector.values = append(collector.values, UCIProductTaskViolation{
		TaskID:  taskID,
		Code:    code,
		Message: message,
	})
}

func (collector *uciProductViolationCollector) sorted() []UCIProductTaskViolation {
	values := append([]UCIProductTaskViolation(nil), collector.values...)
	sort.Slice(values, func(left, right int) bool {
		if values[left].TaskID != values[right].TaskID {
			return values[left].TaskID < values[right].TaskID
		}
		if values[left].Code != values[right].Code {
			return values[left].Code < values[right].Code
		}
		return values[left].Message < values[right].Message
	})
	return values
}

func uciProductViolationCodes(violations []UCIProductTaskViolation) string {
	codes := make([]string, 0, len(violations))
	for _, violation := range violations {
		if violation.TaskID == "" {
			codes = append(codes, violation.Code)
			continue
		}
		codes = append(codes, violation.TaskID+"/"+violation.Code)
	}
	return strings.Join(codes, ", ")
}
