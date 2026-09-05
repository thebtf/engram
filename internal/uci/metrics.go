package uci

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

const (
	// UCISLOReportSchemaVersion identifies the acceptance-only SLO report schema.
	UCISLOReportSchemaVersion = "engram.uci-slo-report/v1"
	// UCISLOMinimumHealthySamples is the documented minimum healthy percentile denominator.
	UCISLOMinimumHealthySamples = 100

	uciSLOMaximumLinesOfCode     = 1_000_000
	uciSLOMinimumActiveWorktrees = 5
	uciSLOMaximumLANRTT          = 50 * time.Millisecond
	uciSLOMaximumChangedFiles    = 20
	uciSLOMaximumChangedBytes    = 1 << 20
)

// UCISLOArtifactDigest identifies one installed candidate artifact without
// retaining its bytes.
type UCISLOArtifactDigest struct {
	Name   string `json:"name"`
	Digest string `json:"digest"`
}

// UCISLOCandidate identifies the exact candidate whose samples were observed.
type UCISLOCandidate struct {
	Branch          string                 `json:"branch"`
	Commit          string                 `json:"commit"`
	Tree            string                 `json:"tree"`
	ArtifactDigests []UCISLOArtifactDigest `json:"artifact_digests"`
}

// UCISLOHost identifies the machine that ran the recorded samples.
type UCISLOHost struct {
	ID string `json:"id"`
}

// UCISLODatabase identifies the database and its measured data size.
type UCISLODatabase struct {
	ID            string `json:"id"`
	Version       string `json:"version"`
	DataSizeBytes int64  `json:"data_size_bytes"`
}

// UCISLOCorpus identifies the fixed corpus used for the sample set.
type UCISLOCorpus struct {
	ID             string `json:"id"`
	ManifestDigest string `json:"manifest_digest"`
}

// UCISLOProvider identifies the provider/model state observed for the sample set.
type UCISLOProvider struct {
	ID     string `json:"id"`
	Model  string `json:"model"`
	Status string `json:"status"`
}

// UCISLOEnvironment is the homogeneous non-candidate identity for samples.
type UCISLOEnvironment struct {
	Host     UCISLOHost     `json:"host"`
	Database UCISLODatabase `json:"database"`
	Corpus   UCISLOCorpus   `json:"corpus"`
	Provider UCISLOProvider `json:"provider"`
}

// UCISLOProfile is the documented healthy first-release acceptance profile.
type UCISLOProfile struct {
	ID                        string        `json:"id"`
	TextFileCount             int           `json:"text_file_count"`
	LinesOfCode               int           `json:"lines_of_code"`
	ActiveWorktreeCount       int           `json:"active_worktree_count"`
	InactiveRegistrationCount int           `json:"inactive_registration_count"`
	LANRTT                    time.Duration `json:"lan_rtt"`
	ChangedFileCount          int           `json:"changed_file_count"`
	ChangedBytes              int64         `json:"changed_bytes"`
}

// UCISLOSampleIdentity repeats the exact candidate and environment identity on
// every raw sample so mixed sources cannot be silently aggregated.
type UCISLOSampleIdentity struct {
	Candidate   UCISLOCandidate   `json:"candidate"`
	Environment UCISLOEnvironment `json:"environment"`
}

// UCISLOSample is one externally recorded observation. Outcome is one of
// healthy, degraded, unavailable, or failed. ResultStatus and Coverage retain
// the closed product result state that explains the classification.
type UCISLOSample struct {
	ID                 string               `json:"id,omitempty"`
	Identity           UCISLOSampleIdentity `json:"identity"`
	Outcome            string               `json:"outcome"`
	Warmth             string               `json:"warmth"`
	ProfileID          string               `json:"profile_id"`
	ContextID          string               `json:"context_id"`
	ResultStatus       string               `json:"result_status"`
	Coverage           string               `json:"coverage"`
	Reason             string               `json:"reason,omitempty"`
	DegradationReasons []string             `json:"degradation_reasons,omitempty"`
	Waited             time.Duration        `json:"waited"`
	Latency            time.Duration        `json:"latency"`
}

// UCISLOGateInput is an explicit raw input partition. Thresholds are not
// supplied by the input: CalculateUCISLOReport derives them from the accepted
// UCI-1 spec.
type UCISLOGateInput struct {
	Name          string         `json:"name"`
	Operation     string         `json:"operation"`
	Warmth        string         `json:"warmth"`
	WaitBound     time.Duration  `json:"wait_bound"`
	ProfileID     string         `json:"profile_id"`
	ContextID     string         `json:"context_id"`
	MaxGraphDepth int            `json:"max_graph_depth,omitempty"`
	QueryCaps     map[string]int `json:"query_caps,omitempty"`
	Samples       []UCISLOSample `json:"samples"`
}

// UCISLOColdObservation preserves cold observations without mixing them into a
// warm percentile population.
type UCISLOColdObservation struct {
	Operation   string         `json:"operation"`
	Warmth      string         `json:"warmth"`
	ProfileID   string         `json:"profile_id,omitempty"`
	ContextID   string         `json:"context_id,omitempty"`
	WaitBound   time.Duration  `json:"wait_bound,omitempty"`
	SampleCount int            `json:"sample_count"`
	Samples     []UCISLOSample `json:"samples,omitempty"`
}

// UCISLOMeasurementInput is a fixed, externally recorded measurement fixture.
// It deliberately has no clock, runner, or provider execution fields.
type UCISLOMeasurementInput struct {
	SchemaVersion    string                  `json:"schema_version"`
	Candidate        UCISLOCandidate         `json:"candidate"`
	Environment      UCISLOEnvironment       `json:"environment"`
	Profile          UCISLOProfile           `json:"profile"`
	Gates            []UCISLOGateInput       `json:"gates"`
	ColdObservations []UCISLOColdObservation `json:"cold_observations"`
}

// UCISLOPercentileDisclosure explains exactly which observations entered a
// percentile. A passing UCI-1 gate always uses all healthy samples and omits
// none of them.
type UCISLOPercentileDisclosure struct {
	Population         string   `json:"population"`
	InputSampleCount   int      `json:"input_sample_count"`
	OmittedSampleCount int      `json:"omitted_sample_count"`
	OmissionReasons    []string `json:"omission_reasons,omitempty"`
}

// UCISLOGateReport is the deterministic accounting result for one accepted
// warm gate.
type UCISLOGateReport struct {
	Name                 string                     `json:"name"`
	Operation            string                     `json:"operation"`
	Warmth               string                     `json:"warmth"`
	Threshold            time.Duration              `json:"threshold"`
	WaitBound            time.Duration              `json:"wait_bound"`
	ProfileID            string                     `json:"profile_id"`
	ContextID            string                     `json:"context_id"`
	MaxGraphDepth        int                        `json:"max_graph_depth,omitempty"`
	QueryCaps            map[string]int             `json:"query_caps,omitempty"`
	SampleCount          int                        `json:"sample_count"`
	Samples              []UCISLOSample             `json:"samples"`
	HealthySampleCount   int                        `json:"healthy_sample_count"`
	HealthyLatencyInputs []time.Duration            `json:"healthy_latency_inputs"`
	P95                  time.Duration              `json:"p95"`
	Max                  time.Duration              `json:"max"`
	FailureCount         int                        `json:"failure_count"`
	DegradedCount        int                        `json:"degraded_count"`
	UnavailableCount     int                        `json:"unavailable_count"`
	DegradedReasons      []string                   `json:"degraded_reasons,omitempty"`
	UnavailableReasons   []string                   `json:"unavailable_reasons,omitempty"`
	Percentile           UCISLOPercentileDisclosure `json:"percentile"`
	Passed               bool                       `json:"passed"`
}

// UCISLOReport is an acceptance-accounting artifact. It reports supplied
// evidence; it is never runtime authority or a substitute for a measurement.
type UCISLOReport struct {
	SchemaVersion    string                  `json:"schema_version"`
	Candidate        UCISLOCandidate         `json:"candidate"`
	Environment      UCISLOEnvironment       `json:"environment"`
	Profile          UCISLOProfile           `json:"profile"`
	Gates            []UCISLOGateReport      `json:"gates"`
	ColdObservations []UCISLOColdObservation `json:"cold_observations"`
	Passed           bool                    `json:"passed"`
}

// UCISLOViolation is a deterministic validation finding for a report.
type UCISLOViolation struct {
	Code    string `json:"code"`
	Gate    string `json:"gate,omitempty"`
	Message string `json:"message"`
}

type uciSLOGateDefinition struct {
	Name          string
	Operation     string
	Threshold     time.Duration
	MaxGraphDepth int
}

var uciSLOExpectedGateNames = [...]string{
	"graph.small_explain_neighbors_impact",
	"search.query_embedding",
	"search.server_retrieval",
	"update.embedding_readiness",
	"update.structural_fts",
}

func uciSLOGateDefinitionFor(name string) (uciSLOGateDefinition, bool) {
	switch name {
	case "update.structural_fts":
		return uciSLOGateDefinition{Name: name, Operation: "update", Threshold: 2 * time.Second}, true
	case "update.embedding_readiness":
		return uciSLOGateDefinition{Name: name, Operation: "update", Threshold: 10 * time.Second}, true
	case "search.server_retrieval":
		return uciSLOGateDefinition{Name: name, Operation: "search", Threshold: 800 * time.Millisecond}, true
	case "search.query_embedding":
		return uciSLOGateDefinition{Name: name, Operation: "search", Threshold: 2500 * time.Millisecond}, true
	case "graph.small_explain_neighbors_impact":
		return uciSLOGateDefinition{Name: name, Operation: "graph", Threshold: time.Second, MaxGraphDepth: 4}, true
	default:
		return uciSLOGateDefinition{}, false
	}
}

// CalculateUCISLOReport performs pure, deterministic accounting over an
// explicit homogeneous input. It neither performs benchmark work nor creates
// samples, identities, or provider state.
func CalculateUCISLOReport(input UCISLOMeasurementInput) (UCISLOReport, error) {
	if err := validateUCISLOMeasurementInput(input); err != nil {
		return UCISLOReport{}, err
	}

	report := UCISLOReport{
		SchemaVersion:    UCISLOReportSchemaVersion,
		Candidate:        canonicalUCISLOCandidate(input.Candidate),
		Environment:      input.Environment,
		Profile:          input.Profile,
		Gates:            make([]UCISLOGateReport, 0, len(input.Gates)),
		ColdObservations: make([]UCISLOColdObservation, 0, len(input.ColdObservations)),
	}

	for _, gateInput := range input.Gates {
		definition, _ := uciSLOGateDefinitionFor(gateInput.Name)
		report.Gates = append(report.Gates, calculateUCISLOGateReport(gateInput, definition))
	}
	sort.Slice(report.Gates, func(i, j int) bool {
		return report.Gates[i].Name < report.Gates[j].Name
	})

	for _, observation := range input.ColdObservations {
		copy := cloneUCISLOColdObservation(observation)
		copy.SampleCount = len(copy.Samples)
		sortUCISLOSamples(copy.Samples)
		report.ColdObservations = append(report.ColdObservations, copy)
	}
	sort.Slice(report.ColdObservations, func(i, j int) bool {
		left, right := report.ColdObservations[i], report.ColdObservations[j]
		if left.Operation != right.Operation {
			return left.Operation < right.Operation
		}
		if left.ContextID != right.ContextID {
			return left.ContextID < right.ContextID
		}
		return left.ProfileID < right.ProfileID
	})

	report.Passed = uciSLOReportPasses(report)
	return report, nil
}

func validateUCISLOMeasurementInput(input UCISLOMeasurementInput) error {
	if input.SchemaVersion != UCISLOReportSchemaVersion {
		return fmt.Errorf("uci SLO input: unsupported schema %q", input.SchemaVersion)
	}
	if !uciSLOCandidateComplete(input.Candidate) {
		return fmt.Errorf("uci SLO input: candidate identity is incomplete")
	}
	if !uciSLOEnvironmentComplete(input.Environment) {
		return fmt.Errorf("uci SLO input: environment identity is incomplete")
	}
	if input.Environment.Provider.Status != "healthy" {
		return fmt.Errorf("uci SLO input: provider status %q is not an accepted healthy profile", input.Environment.Provider.Status)
	}
	if !uciSLOProfileAccepted(input.Profile) {
		return fmt.Errorf("uci SLO input: profile %q is outside the accepted healthy profile", input.Profile.ID)
	}
	if len(input.Gates) != len(uciSLOExpectedGateNames) {
		return fmt.Errorf("uci SLO input: got %d warm gates, want %d", len(input.Gates), len(uciSLOExpectedGateNames))
	}

	seenGates := make(map[string]struct{}, len(input.Gates))
	seenSampleIDs := make(map[string]struct{})
	contextID := ""
	for _, gate := range input.Gates {
		definition, found := uciSLOGateDefinitionFor(gate.Name)
		if !found {
			return fmt.Errorf("uci SLO input: unknown gate %q", gate.Name)
		}
		if _, duplicate := seenGates[gate.Name]; duplicate {
			return fmt.Errorf("uci SLO input: duplicate gate %q", gate.Name)
		}
		seenGates[gate.Name] = struct{}{}
		if gate.Operation != definition.Operation || gate.Warmth != "warm" {
			return fmt.Errorf("uci SLO input: gate %q has operation %q and warmth %q", gate.Name, gate.Operation, gate.Warmth)
		}
		if gate.WaitBound <= 0 {
			return fmt.Errorf("uci SLO input: gate %q has an unbounded wait", gate.Name)
		}
		if gate.ProfileID != input.Profile.ID || strings.TrimSpace(gate.ContextID) == "" {
			return fmt.Errorf("uci SLO input: gate %q does not identify the accepted profile and context", gate.Name)
		}
		if contextID == "" {
			contextID = gate.ContextID
		} else if gate.ContextID != contextID {
			return fmt.Errorf("uci SLO input: gate %q has mixed context %q", gate.Name, gate.ContextID)
		}
		if definition.MaxGraphDepth != 0 && (gate.MaxGraphDepth <= 0 || gate.MaxGraphDepth > definition.MaxGraphDepth || len(gate.QueryCaps) == 0) {
			return fmt.Errorf("uci SLO input: graph gate %q lacks bounded graph caps", gate.Name)
		}
		for sampleIndex, sample := range gate.Samples {
			if !validUCISLOSampleID(sample.ID) {
				return fmt.Errorf("uci SLO input: gate %q sample %d has no bounded identity", gate.Name, sampleIndex)
			}
			if _, duplicate := seenSampleIDs[sample.ID]; duplicate {
				return fmt.Errorf("uci SLO input: duplicate sample ID %q", sample.ID)
			}
			seenSampleIDs[sample.ID] = struct{}{}
			if err := validateUCISLOSample(sample, input.Candidate, input.Environment, gate.ProfileID, gate.ContextID, gate.Warmth); err != nil {
				return fmt.Errorf("uci SLO input: gate %q sample %d: %w", gate.Name, sampleIndex, err)
			}
		}
	}
	for _, name := range uciSLOExpectedGateNames {
		if _, found := seenGates[name]; !found {
			return fmt.Errorf("uci SLO input: missing gate %q", name)
		}
	}

	if len(input.ColdObservations) == 0 {
		return fmt.Errorf("uci SLO input: missing separately labelled cold observations")
	}
	for observationIndex, observation := range input.ColdObservations {
		if len(observation.Samples) == 0 {
			return fmt.Errorf("uci SLO input: cold observation %d has no retained sample evidence", observationIndex)
		}
		if observation.Warmth != "cold" || strings.TrimSpace(observation.Operation) == "" {
			return fmt.Errorf("uci SLO input: cold observation %d is not labelled cold with an operation", observationIndex)
		}
		if observation.ProfileID != input.Profile.ID || observation.ContextID != contextID {
			return fmt.Errorf("uci SLO input: cold observation %d has mixed profile or context", observationIndex)
		}
		if observation.WaitBound <= 0 {
			return fmt.Errorf("uci SLO input: cold observation %d has an unbounded wait", observationIndex)
		}
		for sampleIndex, sample := range observation.Samples {
			if !validUCISLOSampleID(sample.ID) {
				return fmt.Errorf("uci SLO input: cold observation %d sample %d has no bounded identity", observationIndex, sampleIndex)
			}
			if _, duplicate := seenSampleIDs[sample.ID]; duplicate {
				return fmt.Errorf("uci SLO input: duplicate sample ID %q", sample.ID)
			}
			seenSampleIDs[sample.ID] = struct{}{}
			if err := validateUCISLOSample(sample, input.Candidate, input.Environment, observation.ProfileID, observation.ContextID, observation.Warmth); err != nil {
				return fmt.Errorf("uci SLO input: cold observation %d sample %d: %w", observationIndex, sampleIndex, err)
			}
		}
	}
	return nil
}

func validateUCISLOSample(sample UCISLOSample, candidate UCISLOCandidate, environment UCISLOEnvironment, profileID, contextID, warmth string) error {
	if sample.Warmth != warmth || sample.ProfileID != profileID || sample.ContextID != contextID {
		return fmt.Errorf("mixed warmth, profile, or context")
	}
	if !uciSLOCandidateEqual(sample.Identity.Candidate, candidate) || !uciSLOEnvironmentEqual(sample.Identity.Environment, environment) {
		return fmt.Errorf("mixed candidate or environment identity")
	}
	if sample.Latency < 0 || sample.Waited < 0 {
		return fmt.Errorf("negative duration")
	}
	if strings.TrimSpace(sample.ResultStatus) == "" || strings.TrimSpace(sample.Coverage) == "" {
		return fmt.Errorf("missing result status or coverage")
	}
	switch sample.Outcome {
	case "healthy":
		if sample.Latency <= 0 {
			return fmt.Errorf("healthy outcome has no positive latency")
		}
		if uciSLOSampleIndicatesDegradation(sample) {
			return fmt.Errorf("healthy outcome carries degradation")
		}
	case "degraded", "unavailable":
		if len(uciSLOSampleReasons(sample)) == 0 {
			return fmt.Errorf("%s outcome has no reason", sample.Outcome)
		}
	case "failed":
		// Failures remain raw observations and are counted separately.
	default:
		return fmt.Errorf("unsupported outcome %q", sample.Outcome)
	}
	return nil
}

func validUCISLOSampleID(value string) bool {
	return len(value) <= 256 && validIndexText(value)
}

func calculateUCISLOGateReport(input UCISLOGateInput, definition uciSLOGateDefinition) UCISLOGateReport {
	report := UCISLOGateReport{
		Name:          input.Name,
		Operation:     input.Operation,
		Warmth:        input.Warmth,
		Threshold:     definition.Threshold,
		WaitBound:     input.WaitBound,
		ProfileID:     input.ProfileID,
		ContextID:     input.ContextID,
		MaxGraphDepth: input.MaxGraphDepth,
		QueryCaps:     cloneUCISLOQueryCaps(input.QueryCaps),
		Samples:       cloneUCISLOSamples(input.Samples),
	}
	sortUCISLOSamples(report.Samples)
	report.SampleCount = len(report.Samples)

	for _, sample := range report.Samples {
		switch sample.Outcome {
		case "healthy":
			report.HealthyLatencyInputs = append(report.HealthyLatencyInputs, sample.Latency)
		case "degraded":
			report.DegradedCount++
			report.DegradedReasons = append(report.DegradedReasons, uciSLOSampleReasons(sample)...)
		case "unavailable":
			report.UnavailableCount++
			report.UnavailableReasons = append(report.UnavailableReasons, uciSLOSampleReasons(sample)...)
		case "failed":
			report.FailureCount++
		}
	}

	sort.Slice(report.HealthyLatencyInputs, func(i, j int) bool {
		return report.HealthyLatencyInputs[i] < report.HealthyLatencyInputs[j]
	})
	report.HealthySampleCount = len(report.HealthyLatencyInputs)
	report.DegradedReasons = uniqueSortedUCISLOStrings(report.DegradedReasons)
	report.UnavailableReasons = uniqueSortedUCISLOStrings(report.UnavailableReasons)
	report.Percentile = UCISLOPercentileDisclosure{
		Population:       "all_healthy_samples",
		InputSampleCount: report.HealthySampleCount,
	}
	if report.HealthySampleCount > 0 {
		report.P95 = uciSLONearestRankP95(report.HealthyLatencyInputs)
		report.Max = report.HealthyLatencyInputs[report.HealthySampleCount-1]
	}
	report.Passed = uciSLOGatePasses(report, definition)
	return report
}

func uciSLONearestRankP95(sorted []time.Duration) time.Duration {
	if len(sorted) == 0 {
		return 0
	}
	rank := (95*len(sorted) + 99) / 100
	return sorted[rank-1]
}

// ValidateUCISLOReport validates identity, homogeneous partitions, disclosed
// denominator accounting, accepted thresholds, and deterministic report order.
func ValidateUCISLOReport(report UCISLOReport) []UCISLOViolation {
	violations := newUCISLOViolationCollector()
	if report.SchemaVersion != UCISLOReportSchemaVersion {
		violations.add("unsupported_schema", "", "report schema is not the accepted UCI SLO schema")
	}
	if !uciSLOCandidateComplete(report.Candidate) {
		violations.add("candidate_identity_missing", "", "report omits exact candidate identity")
	}
	if !uciSLOEnvironmentComplete(report.Environment) {
		violations.add("environment_identity_missing", "", "report omits host, database, corpus, or provider identity")
	}
	if !uciSLOProfileAccepted(report.Profile) {
		violations.add("profile_not_accepted", "", "report profile is outside the accepted healthy profile")
	}
	if report.Environment.Provider.Status != "healthy" {
		violations.add("provider_not_healthy", "", "report provider is not healthy")
	}
	if !uciSLOArtifactDigestsSorted(report.Candidate.ArtifactDigests) {
		violations.add("nondeterministic_order", "", "candidate artifact digests are not deterministically ordered")
	}
	if !sort.SliceIsSorted(report.Gates, func(i, j int) bool { return report.Gates[i].Name < report.Gates[j].Name }) {
		violations.add("nondeterministic_order", "", "gates are not deterministically ordered")
	}

	seenGates := make(map[string]struct{}, len(report.Gates))
	seenSampleIDs := make(map[string]struct{})
	contextID := ""
	for _, gate := range report.Gates {
		definition, known := uciSLOGateDefinitionFor(gate.Name)
		if !known {
			violations.add("unexpected_gate", gate.Name, "gate is not in the accepted UCI SLO profile")
			continue
		}
		if _, duplicate := seenGates[gate.Name]; duplicate {
			violations.add("duplicate_gate", gate.Name, "report contains the gate more than once")
		}
		seenGates[gate.Name] = struct{}{}
		if contextID == "" && strings.TrimSpace(gate.ContextID) != "" {
			contextID = gate.ContextID
		} else if contextID != "" && gate.ContextID != contextID {
			violations.add("mixed_context", gate.Name, "gate context differs from the homogeneous report context")
		}
		validateUCISLOGateReport(violations, report, gate, definition, seenSampleIDs)
	}
	for _, name := range uciSLOExpectedGateNames {
		if _, found := seenGates[name]; !found {
			violations.add("missing_gate", name, "report omits an accepted warm SLO gate")
		}
	}

	validateUCISLOColdObservations(violations, report, contextID, seenSampleIDs)
	if report.Passed != uciSLOReportPasses(report) {
		violations.add("report_verdict_mismatch", "", "report pass verdict does not match its disclosed inputs")
	}
	return violations.values()
}

func validateUCISLOGateReport(violations *uciSLOViolationCollector, report UCISLOReport, gate UCISLOGateReport, definition uciSLOGateDefinition, seenSampleIDs map[string]struct{}) {
	if gate.Operation != definition.Operation || gate.Warmth != "warm" {
		violations.add("gate_partition_mismatch", gate.Name, "gate operation or warmth differs from the accepted definition")
	}
	if gate.Threshold != definition.Threshold {
		violations.add("threshold_mismatch", gate.Name, "gate threshold differs from the accepted spec")
	}
	if gate.WaitBound <= 0 {
		violations.add("unbounded_wait", gate.Name, "gate does not disclose a positive bounded wait")
	}
	if strings.TrimSpace(gate.ProfileID) == "" || gate.ProfileID != report.Profile.ID {
		violations.add("mixed_profile", gate.Name, "gate profile differs from the report profile")
	}
	if strings.TrimSpace(gate.ContextID) == "" {
		violations.add("context_identity_missing", gate.Name, "gate omits an immutable context identity")
	}
	if definition.MaxGraphDepth != 0 && (gate.MaxGraphDepth <= 0 || gate.MaxGraphDepth > definition.MaxGraphDepth || len(gate.QueryCaps) == 0) {
		violations.add("unbounded_graph", gate.Name, "graph gate lacks the accepted depth and cap bounds")
	}
	if gate.SampleCount != len(gate.Samples) {
		violations.add("sample_count_mismatch", gate.Name, "reported sample count differs from retained raw samples")
	}
	if !sort.SliceIsSorted(gate.Samples, func(i, j int) bool { return uciSLOSampleLess(gate.Samples[i], gate.Samples[j]) }) {
		violations.add("nondeterministic_order", gate.Name, "raw samples are not deterministically ordered")
	}

	healthyLatencies := make([]time.Duration, 0, len(gate.Samples))
	degradedReasons := make([]string, 0, len(gate.Samples))
	unavailableReasons := make([]string, 0, len(gate.Samples))
	degradedCount := 0
	unavailableCount := 0
	failureCount := 0
	waitBoundObserved := true
	for _, sample := range gate.Samples {
		if !validUCISLOSampleID(sample.ID) {
			violations.add("sample_identity_missing", gate.Name, "sample omits a bounded unique ID")
		} else if _, duplicate := seenSampleIDs[sample.ID]; duplicate {
			violations.add("duplicate_sample_id", gate.Name, "sample ID is repeated in the report")
		} else {
			seenSampleIDs[sample.ID] = struct{}{}
		}
		if sample.Warmth != gate.Warmth {
			violations.add("mixed_warmth", gate.Name, "sample warmth differs from its gate")
		}
		if sample.ProfileID != gate.ProfileID {
			violations.add("mixed_profile", gate.Name, "sample profile differs from its gate")
		}
		if sample.ContextID != gate.ContextID {
			violations.add("mixed_context", gate.Name, "sample context differs from its gate")
		}
		validateUCISLOSampleIdentity(violations, gate.Name, report, sample)
		if sample.Waited > gate.WaitBound {
			waitBoundObserved = false
			violations.add("wait_bound_exceeded", gate.Name, "sample waited beyond the disclosed bound")
		}
		if sample.Latency < 0 || sample.Waited < 0 {
			violations.add("negative_duration", gate.Name, "sample records a negative duration")
		}
		switch sample.Outcome {
		case "healthy":
			if sample.Latency <= 0 {
				violations.add("healthy_latency_nonpositive", gate.Name, "healthy sample has no positive latency")
			}
			if uciSLOSampleIndicatesDegradation(sample) {
				violations.add("degraded_sample_counted_healthy", gate.Name, "healthy outcome carries degraded result state, coverage, or reason")
			}
			healthyLatencies = append(healthyLatencies, sample.Latency)
		case "degraded":
			degradedCount++
			if len(uciSLOSampleReasons(sample)) == 0 {
				violations.add("degraded_reason_missing", gate.Name, "degraded sample has no reason")
			}
			degradedReasons = append(degradedReasons, uciSLOSampleReasons(sample)...)
		case "unavailable":
			unavailableCount++
			if len(uciSLOSampleReasons(sample)) == 0 {
				violations.add("unavailable_reason_missing", gate.Name, "unavailable sample has no reason")
			}
			unavailableReasons = append(unavailableReasons, uciSLOSampleReasons(sample)...)
		case "failed":
			failureCount++
		default:
			violations.add("unsupported_outcome", gate.Name, "sample outcome is not closed")
		}
	}
	sort.Slice(healthyLatencies, func(i, j int) bool { return healthyLatencies[i] < healthyLatencies[j] })
	if gate.HealthySampleCount != len(healthyLatencies) {
		violations.add("healthy_sample_count_mismatch", gate.Name, "healthy sample count differs from retained healthy outcomes")
		if gate.HealthySampleCount > len(healthyLatencies) {
			violations.add("degraded_sample_counted_healthy", gate.Name, "reported healthy count exceeds healthy raw outcomes")
		}
	}
	if gate.HealthySampleCount < UCISLOMinimumHealthySamples {
		violations.add("minimum_healthy_samples", gate.Name, "healthy percentile denominator is below 100")
	}
	if gate.DegradedCount != degradedCount {
		violations.add("degraded_count_mismatch", gate.Name, "degraded count differs from retained raw outcomes")
	}
	if gate.UnavailableCount != unavailableCount {
		violations.add("unavailable_count_mismatch", gate.Name, "unavailable count differs from retained raw outcomes")
	}
	if gate.FailureCount != failureCount {
		violations.add("failure_count_mismatch", gate.Name, "failure count differs from retained raw outcomes")
	}
	if !uciSLODurationSlicesEqual(gate.HealthyLatencyInputs, healthyLatencies) || !sort.SliceIsSorted(gate.HealthyLatencyInputs, func(i, j int) bool { return gate.HealthyLatencyInputs[i] < gate.HealthyLatencyInputs[j] }) {
		violations.add("healthy_latency_input_mismatch", gate.Name, "percentile inputs do not equal every sorted healthy latency")
	}
	if len(healthyLatencies) == 0 {
		if gate.P95 != 0 || gate.Max != 0 {
			violations.add("zero_denominator_percentile", gate.Name, "zero healthy denominator cannot report a percentile or maximum")
		}
	} else {
		if gate.P95 != uciSLONearestRankP95(healthyLatencies) {
			violations.add("p95_mismatch", gate.Name, "p95 is not nearest-rank over every healthy latency")
		}
		if gate.Max != healthyLatencies[len(healthyLatencies)-1] {
			violations.add("max_mismatch", gate.Name, "max is not the largest healthy latency")
		}
	}
	validateUCISLOPercentileDisclosure(violations, gate, len(healthyLatencies))
	if !uciSLOReasonDisclosureComplete(gate.DegradedReasons, degradedReasons) {
		violations.add("degraded_reason_undisclosed", gate.Name, "degraded reason summary omits a raw sample reason")
	}
	if !uciSLOReasonDisclosureComplete(gate.UnavailableReasons, unavailableReasons) {
		violations.add("unavailable_reason_undisclosed", gate.Name, "unavailable reason summary omits a raw sample reason")
	}
	if !sort.StringsAreSorted(gate.DegradedReasons) || !sort.StringsAreSorted(gate.UnavailableReasons) {
		violations.add("nondeterministic_order", gate.Name, "reason summaries are not deterministically ordered")
	}
	if gate.Passed != uciSLOGatePasses(gate, definition) || !waitBoundObserved && gate.Passed {
		violations.add("gate_verdict_mismatch", gate.Name, "gate pass verdict does not match its disclosed inputs")
	}
}

func validateUCISLOPercentileDisclosure(violations *uciSLOViolationCollector, gate UCISLOGateReport, healthyCount int) {
	if gate.Percentile.Population != "all_healthy_samples" ||
		gate.Percentile.InputSampleCount != healthyCount ||
		gate.Percentile.OmittedSampleCount != 0 ||
		len(gate.Percentile.OmissionReasons) != 0 {
		violations.add("partial_percentile_disclosure", gate.Name, "percentile does not disclose the complete healthy population")
	}
	if gate.Percentile.OmittedSampleCount > 0 && len(gate.Percentile.OmissionReasons) == 0 {
		violations.add("undisclosed_partial_percentile", gate.Name, "omitted percentile observations have no disclosure reason")
	}
	if !sort.StringsAreSorted(gate.Percentile.OmissionReasons) {
		violations.add("nondeterministic_order", gate.Name, "percentile omission reasons are not deterministically ordered")
	}
}

func validateUCISLOSampleIdentity(violations *uciSLOViolationCollector, gateName string, report UCISLOReport, sample UCISLOSample) {
	if !uciSLOCandidateComplete(sample.Identity.Candidate) || !uciSLOEnvironmentComplete(sample.Identity.Environment) {
		violations.add("sample_identity_missing", gateName, "sample omits candidate or environment identity")
		return
	}
	if !uciSLOCandidateEqual(sample.Identity.Candidate, report.Candidate) {
		violations.add("mixed_candidate", gateName, "sample candidate differs from the report candidate")
	}
	if sample.Identity.Environment.Host != report.Environment.Host {
		violations.add("mixed_host", gateName, "sample host differs from the report host")
	}
	if sample.Identity.Environment.Database != report.Environment.Database {
		violations.add("mixed_database", gateName, "sample database differs from the report database")
	}
	if sample.Identity.Environment.Corpus != report.Environment.Corpus {
		violations.add("mixed_corpus", gateName, "sample corpus differs from the report corpus")
	}
	if sample.Identity.Environment.Provider != report.Environment.Provider {
		violations.add("mixed_provider", gateName, "sample provider differs from the report provider")
	}
}

func validateUCISLOColdObservations(violations *uciSLOViolationCollector, report UCISLOReport, contextID string, seenSampleIDs map[string]struct{}) {
	if len(report.ColdObservations) == 0 {
		violations.add("cold_observations_missing", "", "report omits separately labelled cold observations")
		return
	}
	if !sort.SliceIsSorted(report.ColdObservations, func(i, j int) bool {
		left, right := report.ColdObservations[i], report.ColdObservations[j]
		if left.Operation != right.Operation {
			return left.Operation < right.Operation
		}
		if left.ContextID != right.ContextID {
			return left.ContextID < right.ContextID
		}
		return left.ProfileID < right.ProfileID
	}) {
		violations.add("nondeterministic_order", "", "cold observations are not deterministically ordered")
	}
	for _, observation := range report.ColdObservations {
		if observation.Warmth != "cold" || strings.TrimSpace(observation.Operation) == "" {
			violations.add("cold_observation_invalid", observation.Operation, "cold observation is not labelled cold with an operation")
		}
		if observation.ProfileID != report.Profile.ID {
			violations.add("mixed_profile", observation.Operation, "cold observation profile differs from the report profile")
		}
		if observation.ContextID != contextID {
			violations.add("mixed_context", observation.Operation, "cold observation context differs from the warm report context")
		}
		if observation.WaitBound <= 0 {
			violations.add("unbounded_wait", observation.Operation, "cold observation does not disclose a positive bounded wait")
		}
		if observation.SampleCount != len(observation.Samples) {
			violations.add("sample_count_mismatch", observation.Operation, "cold observation count differs from retained samples")
		}
		if !sort.SliceIsSorted(observation.Samples, func(i, j int) bool { return uciSLOSampleLess(observation.Samples[i], observation.Samples[j]) }) {
			violations.add("nondeterministic_order", observation.Operation, "cold samples are not deterministically ordered")
		}
		for _, sample := range observation.Samples {
			if !validUCISLOSampleID(sample.ID) {
				violations.add("sample_identity_missing", observation.Operation, "cold sample omits a bounded unique ID")
			} else if _, duplicate := seenSampleIDs[sample.ID]; duplicate {
				violations.add("duplicate_sample_id", observation.Operation, "sample ID is repeated in the report")
			} else {
				seenSampleIDs[sample.ID] = struct{}{}
			}
			if sample.Warmth != observation.Warmth {
				violations.add("mixed_warmth", observation.Operation, "cold sample warmth differs from its observation")
			}
			if sample.ProfileID != observation.ProfileID {
				violations.add("mixed_profile", observation.Operation, "cold sample profile differs from its observation")
			}
			if sample.ContextID != observation.ContextID {
				violations.add("mixed_context", observation.Operation, "cold sample context differs from its observation")
			}
			validateUCISLOSampleIdentity(violations, observation.Operation, report, sample)
			if sample.Waited > observation.WaitBound {
				violations.add("wait_bound_exceeded", observation.Operation, "cold sample waited beyond the disclosed bound")
			}
		}
	}
}

func uciSLOGatePasses(gate UCISLOGateReport, definition uciSLOGateDefinition) bool {
	if gate.HealthySampleCount < UCISLOMinimumHealthySamples || gate.P95 > definition.Threshold || gate.WaitBound <= 0 {
		return false
	}
	for _, sample := range gate.Samples {
		if sample.Waited > gate.WaitBound {
			return false
		}
	}
	return true
}

func uciSLOReportPasses(report UCISLOReport) bool {
	if !uciSLOCandidateComplete(report.Candidate) || !uciSLOEnvironmentComplete(report.Environment) || !uciSLOProfileAccepted(report.Profile) || report.Environment.Provider.Status != "healthy" || len(report.ColdObservations) == 0 {
		return false
	}
	seen := make(map[string]struct{}, len(report.Gates))
	for _, gate := range report.Gates {
		definition, found := uciSLOGateDefinitionFor(gate.Name)
		if !found || !uciSLOGatePasses(gate, definition) {
			return false
		}
		if _, duplicate := seen[gate.Name]; duplicate {
			return false
		}
		seen[gate.Name] = struct{}{}
	}
	for _, name := range uciSLOExpectedGateNames {
		if _, found := seen[name]; !found {
			return false
		}
	}
	return true
}

func uciSLOCandidateComplete(candidate UCISLOCandidate) bool {
	if strings.TrimSpace(candidate.Branch) == "" || strings.TrimSpace(candidate.Commit) == "" || strings.TrimSpace(candidate.Tree) == "" || len(candidate.ArtifactDigests) == 0 {
		return false
	}
	for _, artifact := range candidate.ArtifactDigests {
		if strings.TrimSpace(artifact.Name) == "" || strings.TrimSpace(artifact.Digest) == "" {
			return false
		}
	}
	return true
}

func uciSLOEnvironmentComplete(environment UCISLOEnvironment) bool {
	return strings.TrimSpace(environment.Host.ID) != "" &&
		strings.TrimSpace(environment.Database.ID) != "" &&
		strings.TrimSpace(environment.Database.Version) != "" &&
		environment.Database.DataSizeBytes > 0 &&
		strings.TrimSpace(environment.Corpus.ID) != "" &&
		strings.TrimSpace(environment.Corpus.ManifestDigest) != "" &&
		strings.TrimSpace(environment.Provider.ID) != "" &&
		strings.TrimSpace(environment.Provider.Model) != "" &&
		strings.TrimSpace(environment.Provider.Status) != ""
}

func uciSLOProfileAccepted(profile UCISLOProfile) bool {
	return strings.TrimSpace(profile.ID) != "" &&
		profile.TextFileCount > 0 &&
		profile.LinesOfCode > 0 && profile.LinesOfCode <= uciSLOMaximumLinesOfCode &&
		profile.ActiveWorktreeCount >= uciSLOMinimumActiveWorktrees &&
		profile.InactiveRegistrationCount > 0 &&
		profile.LANRTT >= 0 && profile.LANRTT <= uciSLOMaximumLANRTT &&
		profile.ChangedFileCount > 0 && profile.ChangedFileCount <= uciSLOMaximumChangedFiles &&
		profile.ChangedBytes > 0 && profile.ChangedBytes <= uciSLOMaximumChangedBytes
}

func canonicalUCISLOCandidate(candidate UCISLOCandidate) UCISLOCandidate {
	copy := candidate
	copy.ArtifactDigests = append([]UCISLOArtifactDigest(nil), candidate.ArtifactDigests...)
	sort.Slice(copy.ArtifactDigests, func(i, j int) bool {
		left, right := copy.ArtifactDigests[i], copy.ArtifactDigests[j]
		if left.Name != right.Name {
			return left.Name < right.Name
		}
		return left.Digest < right.Digest
	})
	return copy
}

func uciSLOCandidateEqual(left, right UCISLOCandidate) bool {
	left = canonicalUCISLOCandidate(left)
	right = canonicalUCISLOCandidate(right)
	if left.Branch != right.Branch || left.Commit != right.Commit || left.Tree != right.Tree || len(left.ArtifactDigests) != len(right.ArtifactDigests) {
		return false
	}
	for index := range left.ArtifactDigests {
		if left.ArtifactDigests[index] != right.ArtifactDigests[index] {
			return false
		}
	}
	return true
}

func uciSLOEnvironmentEqual(left, right UCISLOEnvironment) bool {
	return left == right
}

func uciSLOArtifactDigestsSorted(values []UCISLOArtifactDigest) bool {
	return sort.SliceIsSorted(values, func(i, j int) bool {
		if values[i].Name != values[j].Name {
			return values[i].Name < values[j].Name
		}
		return values[i].Digest < values[j].Digest
	})
}

func cloneUCISLOQueryCaps(caps map[string]int) map[string]int {
	if caps == nil {
		return nil
	}
	copy := make(map[string]int, len(caps))
	for key, value := range caps {
		copy[key] = value
	}
	return copy
}

func cloneUCISLOSamples(samples []UCISLOSample) []UCISLOSample {
	copy := make([]UCISLOSample, len(samples))
	for index, sample := range samples {
		copy[index] = cloneUCISLOSample(sample)
	}
	return copy
}

func cloneUCISLOSample(sample UCISLOSample) UCISLOSample {
	copy := sample
	copy.Identity.Candidate = canonicalUCISLOCandidate(sample.Identity.Candidate)
	copy.DegradationReasons = append([]string(nil), sample.DegradationReasons...)
	sort.Strings(copy.DegradationReasons)
	return copy
}

func cloneUCISLOColdObservation(observation UCISLOColdObservation) UCISLOColdObservation {
	copy := observation
	copy.Samples = cloneUCISLOSamples(observation.Samples)
	return copy
}

func sortUCISLOSamples(samples []UCISLOSample) {
	sort.Slice(samples, func(i, j int) bool {
		return uciSLOSampleLess(samples[i], samples[j])
	})
}

func uciSLOSampleLess(left, right UCISLOSample) bool {
	for _, pair := range [][2]string{
		{left.Outcome, right.Outcome},
		{left.Warmth, right.Warmth},
		{left.ProfileID, right.ProfileID},
		{left.ContextID, right.ContextID},
		{left.ResultStatus, right.ResultStatus},
		{left.Coverage, right.Coverage},
		{left.Reason, right.Reason},
		{left.ID, right.ID},
	} {
		if pair[0] != pair[1] {
			return pair[0] < pair[1]
		}
	}
	if left.Latency != right.Latency {
		return left.Latency < right.Latency
	}
	if left.Waited != right.Waited {
		return left.Waited < right.Waited
	}
	leftIdentity := uciSLOSampleIdentityKey(left.Identity)
	rightIdentity := uciSLOSampleIdentityKey(right.Identity)
	if leftIdentity != rightIdentity {
		return leftIdentity < rightIdentity
	}
	return strings.Join(left.DegradationReasons, "\x00") < strings.Join(right.DegradationReasons, "\x00")
}

func uciSLOSampleIdentityKey(identity UCISLOSampleIdentity) string {
	candidate := canonicalUCISLOCandidate(identity.Candidate)
	artifacts := make([]string, 0, len(candidate.ArtifactDigests))
	for _, artifact := range candidate.ArtifactDigests {
		artifacts = append(artifacts, artifact.Name+"="+artifact.Digest)
	}
	environment := identity.Environment
	return strings.Join([]string{
		candidate.Branch,
		candidate.Commit,
		candidate.Tree,
		strings.Join(artifacts, "\x1f"),
		environment.Host.ID,
		environment.Database.ID,
		environment.Database.Version,
		fmt.Sprintf("%d", environment.Database.DataSizeBytes),
		environment.Corpus.ID,
		environment.Corpus.ManifestDigest,
		environment.Provider.ID,
		environment.Provider.Model,
		environment.Provider.Status,
	}, "\x1e")
}

func uciSLOSampleIndicatesDegradation(sample UCISLOSample) bool {
	if len(uciSLOSampleReasons(sample)) != 0 {
		return true
	}
	switch strings.ToLower(strings.TrimSpace(sample.ResultStatus)) {
	case "degraded", "partial", "stale", "offline", "unsupported", "unavailable", "failed", "error":
		return true
	}
	switch strings.ToLower(strings.TrimSpace(sample.Coverage)) {
	case "degraded", "partial", "unknown", "offline", "unsupported", "unavailable", "failed":
		return true
	}
	return false
}

func uciSLOSampleReasons(sample UCISLOSample) []string {
	reasons := make([]string, 0, len(sample.DegradationReasons)+1)
	if reason := strings.TrimSpace(sample.Reason); reason != "" {
		reasons = append(reasons, reason)
	}
	for _, reason := range sample.DegradationReasons {
		if reason = strings.TrimSpace(reason); reason != "" {
			reasons = append(reasons, reason)
		}
	}
	return uniqueSortedUCISLOStrings(reasons)
}

func uniqueSortedUCISLOStrings(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	copy := append([]string(nil), values...)
	sort.Strings(copy)
	output := copy[:0]
	for _, value := range copy {
		if len(output) == 0 || output[len(output)-1] != value {
			output = append(output, value)
		}
	}
	return output
}

func uciSLODurationSlicesEqual(left, right []time.Duration) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func uciSLOReasonDisclosureComplete(reported, observed []string) bool {
	for _, reason := range uniqueSortedUCISLOStrings(observed) {
		found := false
		for _, disclosed := range reported {
			if reason == disclosed {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

type uciSLOViolationCollector struct {
	valuesByKey map[string]UCISLOViolation
}

func newUCISLOViolationCollector() *uciSLOViolationCollector {
	return &uciSLOViolationCollector{valuesByKey: make(map[string]UCISLOViolation)}
}

func (collector *uciSLOViolationCollector) add(code, gate, message string) {
	key := code + "\x00" + gate
	if _, found := collector.valuesByKey[key]; !found {
		collector.valuesByKey[key] = UCISLOViolation{Code: code, Gate: gate, Message: message}
	}
}

func (collector *uciSLOViolationCollector) values() []UCISLOViolation {
	values := make([]UCISLOViolation, 0, len(collector.valuesByKey))
	for _, violation := range collector.valuesByKey {
		values = append(values, violation)
	}
	sort.Slice(values, func(i, j int) bool {
		if values[i].Code != values[j].Code {
			return values[i].Code < values[j].Code
		}
		if values[i].Gate != values[j].Gate {
			return values[i].Gate < values[j].Gate
		}
		return values[i].Message < values[j].Message
	})
	return values
}
