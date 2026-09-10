package acceptance

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"unicode"
)

const operatorCodeRCMechanismFixturePath = "tests/fixtures/operator-code-rc-oracle/mechanism-oracle.json"

var operatorCodeRCReferencePins = map[string]operatorCodeRCReference{
	"socraticode": {
		RepositoryURL: "https://github.com/giancarloerra/SocratiCode",
		Commit:        "78a9eafa3b122c9c8768a3b85cb8c8166a571301",
		License:       "AGPL-3.0-only",
	},
	"graphify": {
		RepositoryURL: "https://github.com/Graphify-Labs/graphify",
		Commit:        "33362d969292b57eda82f3fbd9eb5f3f5bc9bbc2",
		License:       "Apache-2.0; NOTICE records historical MIT portions",
	},
}

var operatorCodeRCRequiredCases = map[string]string{
	"exact_identifier":      "exact_identifier",
	"russian_concept_query": "russian_concept_query",
	"dependency":            "dependency",
	"reverse_impact":        "reverse_impact",
	"worktree_switch":       "worktree_switch",
	"no_answer":             "no_answer",
}

type operatorCodeRCMechanismFixture struct {
	SchemaVersion      string                          `json:"schema_version"`
	References         []operatorCodeRCReference       `json:"references"`
	OracleCases        []operatorCodeRCOracleCase      `json:"oracle_cases"`
	EngramCoverage     []operatorCodeRCCoverage        `json:"engram_coverage"`
	DeferredProfiles   []operatorCodeRCDeferredProfile `json:"deferred_profiles"`
	OptionalComparator operatorCodeRCComparator        `json:"optional_comparator"`
	ExpansionPackets   []operatorCodeRCExpansionPacket `json:"expansion_packets"`
	Claims             []operatorCodeRCClaim           `json:"claims"`
}

type operatorCodeRCReference struct {
	ID                string `json:"id"`
	RepositoryURL     string `json:"repository_url"`
	Commit            string `json:"commit"`
	License           string `json:"license"`
	EvidenceRole      string `json:"evidence_role"`
	RuntimeDependency bool   `json:"runtime_dependency"`
}

type operatorCodeRCOracleCase struct {
	ID              string `json:"id"`
	Kind            string `json:"kind"`
	Question        string `json:"question"`
	ExpectedOutcome string `json:"expected_outcome"`
	SourceAnchor    string `json:"source_anchor"`
}

type operatorCodeRCCoverage struct {
	Language  string   `json:"language"`
	Relations []string `json:"relations"`
	Scope     string   `json:"scope"`
}

type operatorCodeRCDeferredProfile struct {
	Subject string `json:"subject"`
	Status  string `json:"status"`
}

type operatorCodeRCComparator struct {
	Availability     string `json:"availability"`
	ComparisonStatus string `json:"comparison_status"`
}

type operatorCodeRCExpansionPacket struct {
	ID                string   `json:"id"`
	SourceOracleCases []string `json:"source_oracle_cases"`
	AlignedCorpus     string   `json:"aligned_corpus"`
	LexicalBaseline   string   `json:"lexical_baseline"`
	IndependentJudge  string   `json:"independent_judge"`
}

type operatorCodeRCClaim struct {
	Kind              string `json:"kind"`
	Origin            string `json:"origin"`
	ExpansionPacketID string `json:"expansion_packet_id"`
}

func TestOperatorCodeRCMechanism(t *testing.T) {
	root := operatorCodeRCRepositoryRoot(t)
	fixture := loadOperatorCodeRCMechanismFixture(t)
	if err := validateOperatorCodeRCMechanismFixture(fixture); err != nil {
		t.Fatalf("validate bounded R-C source-oracle fixture: %v", err)
	}
	validateOperatorCodeRCSourceAnchors(t, root, fixture.OracleCases)

	t.Run("unavailable comparator is not a verdict", func(t *testing.T) {
		if fixture.OptionalComparator.Availability != "unavailable" {
			t.Fatalf("optional comparator availability = %q, want unavailable", fixture.OptionalComparator.Availability)
		}
		if fixture.OptionalComparator.ComparisonStatus != "parity_not_checked" {
			t.Fatalf("optional comparator status = %q, want parity_not_checked", fixture.OptionalComparator.ComparisonStatus)
		}
	})

	for _, claimKind := range []string{"benchmark", "parity", "support"} {
		claimKind := claimKind
		t.Run("transferred "+claimKind+" claim needs an expansion packet", func(t *testing.T) {
			candidate := fixture
			candidate.Claims = append([]operatorCodeRCClaim(nil), fixture.Claims...)
			candidate.Claims = append(candidate.Claims, operatorCodeRCClaim{
				Kind:   claimKind,
				Origin: "reference_transfer",
			})
			if err := validateOperatorCodeRCMechanismFixture(candidate); err == nil {
				t.Fatalf("transferred %s claim without an expansion packet was accepted", claimKind)
			}
		})
	}
}

func loadOperatorCodeRCMechanismFixture(t *testing.T) operatorCodeRCMechanismFixture {
	t.Helper()

	encoded, err := os.ReadFile(filepath.Join(operatorCodeRCRepositoryRoot(t), operatorCodeRCMechanismFixturePath))
	if err != nil {
		t.Fatalf("read R-C source-oracle fixture: %v", err)
	}

	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	var fixture operatorCodeRCMechanismFixture
	if err := decoder.Decode(&fixture); err != nil {
		t.Fatalf("decode R-C source-oracle fixture: %v", err)
	}
	var extra json.RawMessage
	if err := decoder.Decode(&extra); err == nil {
		t.Fatal("R-C source-oracle fixture contains more than one JSON value")
	} else if err != io.EOF {
		t.Fatalf("finish R-C source-oracle fixture: %v", err)
	}
	return fixture
}

func operatorCodeRCRepositoryRoot(t *testing.T) string {
	t.Helper()

	workingDirectory, err := os.Getwd()
	if err != nil {
		t.Fatal("locate operator-code R-C acceptance package")
	}
	return filepath.Clean(filepath.Join(workingDirectory, "..", "..", ".."))
}

func validateOperatorCodeRCSourceAnchors(t *testing.T, root string, cases []operatorCodeRCOracleCase) {
	t.Helper()

	requiredTerms := map[string][]string{
		"exact_identifier":      {"TreeSitterIndexAdmissionArtifactProfile"},
		"russian_concept_query": {"CheckoutID"},
		"dependency":            {"caller.ts", "bridge.ts", "remote.ts"},
		"reverse_impact":        {"GraphDirectionIncoming"},
		"worktree_switch":       {"CheckoutID"},
	}
	for _, oracleCase := range cases {
		if oracleCase.ID == "no_answer" {
			continue
		}
		if filepath.IsAbs(oracleCase.SourceAnchor) || oracleCase.SourceAnchor == "." || strings.HasPrefix(filepath.Clean(oracleCase.SourceAnchor), "..") {
			t.Fatalf("source-oracle case %q has an unsafe source anchor %q", oracleCase.ID, oracleCase.SourceAnchor)
		}
		contents, err := os.ReadFile(filepath.Join(root, oracleCase.SourceAnchor))
		if err != nil {
			t.Fatalf("read source-oracle anchor for %q: %v", oracleCase.ID, err)
		}
		for _, term := range requiredTerms[oracleCase.ID] {
			if !bytes.Contains(contents, []byte(term)) {
				t.Fatalf("source-oracle anchor %q for %q does not contain %q", oracleCase.SourceAnchor, oracleCase.ID, term)
			}
		}
	}
}

func validateOperatorCodeRCMechanismFixture(fixture operatorCodeRCMechanismFixture) error {
	if fixture.SchemaVersion != "engram.operator-code-rc-oracle/v1" {
		return fmt.Errorf("schema_version = %q", fixture.SchemaVersion)
	}
	if err := validateOperatorCodeRCReferences(fixture.References); err != nil {
		return err
	}
	if err := validateOperatorCodeRCOracleCases(fixture.OracleCases); err != nil {
		return err
	}
	if err := validateOperatorCodeRCCoverage(fixture.EngramCoverage); err != nil {
		return err
	}
	if err := validateOperatorCodeRCDeferredProfiles(fixture.DeferredProfiles); err != nil {
		return err
	}
	if fixture.OptionalComparator.Availability != "unavailable" || fixture.OptionalComparator.ComparisonStatus != "parity_not_checked" {
		return fmt.Errorf("optional comparator must be unavailable with parity_not_checked, got availability=%q status=%q", fixture.OptionalComparator.Availability, fixture.OptionalComparator.ComparisonStatus)
	}
	return validateOperatorCodeRCClaims(fixture.Claims, fixture.ExpansionPackets)
}

func validateOperatorCodeRCReferences(references []operatorCodeRCReference) error {
	if len(references) != len(operatorCodeRCReferencePins) {
		return fmt.Errorf("reference metadata count = %d, want %d", len(references), len(operatorCodeRCReferencePins))
	}
	seen := make(map[string]struct{}, len(references))
	for _, reference := range references {
		if _, duplicate := seen[reference.ID]; duplicate {
			return fmt.Errorf("reference metadata repeats %q", reference.ID)
		}
		seen[reference.ID] = struct{}{}
		want, known := operatorCodeRCReferencePins[reference.ID]
		if !known {
			return fmt.Errorf("reference metadata contains unknown reference %q", reference.ID)
		}
		if reference.RepositoryURL != want.RepositoryURL || reference.Commit != want.Commit || reference.License != want.License {
			return fmt.Errorf("reference metadata for %q does not preserve its pinned repository, commit, and license", reference.ID)
		}
		if reference.EvidenceRole != "source_metadata" || reference.RuntimeDependency {
			return fmt.Errorf("reference %q must remain evidence metadata rather than a runtime dependency", reference.ID)
		}
	}
	return nil
}

func validateOperatorCodeRCOracleCases(cases []operatorCodeRCOracleCase) error {
	if len(cases) != len(operatorCodeRCRequiredCases) {
		return fmt.Errorf("source-oracle case count = %d, want %d", len(cases), len(operatorCodeRCRequiredCases))
	}
	seen := make(map[string]struct{}, len(cases))
	for _, oracleCase := range cases {
		wantKind, known := operatorCodeRCRequiredCases[oracleCase.ID]
		if !known || wantKind != oracleCase.Kind {
			return fmt.Errorf("source-oracle case %q has unsupported kind %q", oracleCase.ID, oracleCase.Kind)
		}
		if _, duplicate := seen[oracleCase.ID]; duplicate {
			return fmt.Errorf("source-oracle case repeats %q", oracleCase.ID)
		}
		seen[oracleCase.ID] = struct{}{}
		if strings.TrimSpace(oracleCase.Question) != oracleCase.Question || oracleCase.Question == "" || oracleCase.ExpectedOutcome == "" || oracleCase.SourceAnchor == "" {
			return fmt.Errorf("source-oracle case %q has an empty or non-canonical question, outcome, or anchor", oracleCase.ID)
		}
	}
	if !containsOperatorCodeRCCyrillic(caseByID(cases, "russian_concept_query").Question) {
		return fmt.Errorf("Russian conceptual query does not contain Cyrillic text")
	}
	if caseByID(cases, "no_answer").ExpectedOutcome != "explicit_no_answer" || caseByID(cases, "no_answer").SourceAnchor != "none" {
		return fmt.Errorf("no-answer case must require an explicit no-answer rather than a fabricated source")
	}
	return nil
}

func validateOperatorCodeRCCoverage(coverage []operatorCodeRCCoverage) error {
	want := map[string]struct {
		relations []string
		scope     string
	}{
		"go":         {relations: []string{"calls"}, scope: "frozen_current_fixture"},
		"typescript": {relations: []string{"calls", "exports", "imports"}, scope: "bounded_alias_reexport_fixture"},
		"tsx":        {relations: []string{"calls", "exports", "imports"}, scope: "bounded_alias_reexport_fixture"},
	}
	if len(coverage) != len(want) {
		return fmt.Errorf("Engram coverage count = %d, want %d", len(coverage), len(want))
	}
	for _, entry := range coverage {
		expected, known := want[entry.Language]
		if !known || entry.Scope != expected.scope || !sameOperatorCodeRCStrings(entry.Relations, expected.relations) {
			return fmt.Errorf("Engram coverage for %q is not the declared bounded current slice", entry.Language)
		}
		delete(want, entry.Language)
	}
	if len(want) != 0 {
		return fmt.Errorf("Engram coverage is missing %s", strings.Join(sortedOperatorCodeRCKeys(want), ", "))
	}
	return nil
}

func validateOperatorCodeRCDeferredProfiles(profiles []operatorCodeRCDeferredProfile) error {
	want := map[string]struct{}{"nvmd-ai": {}, "NovaScript": {}, "C#": {}, "Vue": {}}
	if len(profiles) != len(want) {
		return fmt.Errorf("deferred profile count = %d, want %d", len(profiles), len(want))
	}
	for _, profile := range profiles {
		if _, known := want[profile.Subject]; !known || profile.Status != "deferred" {
			return fmt.Errorf("deferred profile %#v is not an explicitly deferred out-of-slice target", profile)
		}
		delete(want, profile.Subject)
	}
	if len(want) != 0 {
		return fmt.Errorf("missing deferred profiles: %s", strings.Join(sortedOperatorCodeRCKeys(want), ", "))
	}
	return nil
}

func validateOperatorCodeRCClaims(claims []operatorCodeRCClaim, packets []operatorCodeRCExpansionPacket) error {
	packetIDs := make(map[string]struct{}, len(packets))
	for _, packet := range packets {
		if packet.ID == "" || !sameOperatorCodeRCStrings(packet.SourceOracleCases, sortedOperatorCodeRCKeys(operatorCodeRCRequiredCases)) || packet.AlignedCorpus == "" || packet.LexicalBaseline == "" || packet.IndependentJudge == "" {
			return fmt.Errorf("expansion packet %q is incomplete", packet.ID)
		}
		if _, duplicate := packetIDs[packet.ID]; duplicate {
			return fmt.Errorf("expansion packet repeats %q", packet.ID)
		}
		packetIDs[packet.ID] = struct{}{}
	}
	for _, claim := range claims {
		if claim.Kind == "" || claim.Origin == "" {
			return fmt.Errorf("claim has empty kind or origin")
		}
		if claim.Origin != "reference_transfer" {
			continue
		}
		switch claim.Kind {
		case "benchmark", "parity", "support":
			if _, exists := packetIDs[claim.ExpansionPacketID]; !exists || claim.ExpansionPacketID == "" {
				return fmt.Errorf("transferred %s claim requires a declared expansion packet", claim.Kind)
			}
		}
	}
	return nil
}

func caseByID(cases []operatorCodeRCOracleCase, id string) operatorCodeRCOracleCase {
	for _, oracleCase := range cases {
		if oracleCase.ID == id {
			return oracleCase
		}
	}
	return operatorCodeRCOracleCase{}
}

func containsOperatorCodeRCCyrillic(value string) bool {
	return strings.IndexFunc(value, func(r rune) bool { return unicode.Is(unicode.Cyrillic, r) }) >= 0
}

func sameOperatorCodeRCStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	left = append([]string(nil), left...)
	right = append([]string(nil), right...)
	sort.Strings(left)
	sort.Strings(right)
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func sortedOperatorCodeRCKeys[V any](values map[string]V) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
