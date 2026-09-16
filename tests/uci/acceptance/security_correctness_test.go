package acceptance

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const (
	uciSecurityCorrectnessChildTimeout   = 2 * time.Minute
	uciSecurityCorrectnessCommandTimeout = uciSecurityCorrectnessChildTimeout + 15*time.Second
	uciSecurityCorrectnessScenarioFile   = "specs/010-unified-code-intelligence/acceptance/scenarios.json"
)

type uciSecurityCorrectnessGroup struct {
	name        string
	scenarioIDs []string
	packagePath string
	testPattern string
}

var uciSecurityCorrectnessAssignedScenarioIDs = []string{
	"U01", "U02", "U03", "U04", "U05", "U06", "U07", "U08", "U09", "U10",
	"U11", "U12", "U13", "U14", "U15", "U16", "U17", "U18", "U19", "U20",
	"U21", "U22", "U23", "U24", "U25",
	"U36", "U37", "U38", "U39", "U40",
	"U42", "U43", "U44", "U45", "U46",
}

var uciSecurityCorrectnessGroups = []uciSecurityCorrectnessGroup{
	{
		name:        "handler-lifecycle",
		scenarioIDs: []string{"U07", "U08", "U10", "U11", "U12", "U25"},
		packagePath: "./internal/handlers/codeintel",
		testPattern: uciSecurityCorrectnessPattern(
			"TestUCILocalRegistryCoalescesDirtyStateAndTracksReconciliation",
			"TestUCILocalRegistryRecoversCurrentStateAcrossCrashWithoutEventRetention",
			"TestUCILocalRegistryUpdatesOnlyLocalRootPathForVerifiedMove",
			"TestUCILocalRegistryRefusesMismatchedEvidenceForSameServerIncarnation",
			"TestUCIPreparedIndexForwardsDirtyAndUnbornWorktreeObservations",
			"TestUCIPreparedIndexPublishesTreeSitterFactsWithPinnedProfile",
		),
	},
	{
		name:        "persistence-lifecycle",
		scenarioIDs: []string{"U03", "U04", "U05", "U06", "U17", "U18", "U24"},
		packagePath: "./internal/db/gorm",
		testPattern: uciSecurityCorrectnessPattern(
			"TestUCIPublishStagingInvisibleAndHistoricalIntervals",
			"TestUCIPublishIncompletePartsAndEOF",
			"TestUCIReplayFinalizeAfterLostACKAndLaterPublish",
			"TestUCILeaseExpiryTakeoverAndStaleWriter",
			"TestUCIProjectionStoreSemanticMethodsKeepVectorsScopedAndCovered",
			"TestUCIVersionedReadStoreReportsPartialAndUnavailableCoverage",
			"TestUCIEmbeddingReusesUnchangedInputAcrossCheckouts",
		),
	},
	{
		name:        "uci-core",
		scenarioIDs: []string{"U02", "U13", "U14", "U15", "U16", "U19", "U20", "U21", "U37", "U38", "U39"},
		packagePath: "./internal/uci",
		testPattern: uciSecurityCorrectnessPattern(
			"TestUCIContextResolverKeepsClientBindingsIsolated",
			"TestUCIScannerUsesGitNULCandidateCensusAndNestedIgnores",
			"TestUCIScannerRefusesReparseEscapesAndUnsupportedTypes",
			"TestUCIScannerCensusCannotAuthorizeDeleteAllByAccident",
			"TestUCIVectorProfileCacheReusesOnlyExactCompatibleInput",
			"TestUCISemanticProviderFailuresRemainLexicalAndVisible",
			"TestUCIReconcileChangedCalleeReResolvesUnchangedCallerAtomically",
			"TestUCIGraphReportsAmbiguityUnresolvedSitesAndChangedCalleeInvalidation",
			"TestUCIGoExtractionReturnsFreshPartialFactsForMalformedSource",
			"TestUCIScannerPreservesWindowsUnicodeCaseAndCRLFBytes",
			"TestUCIScannerExcludesNestedRepositoriesAndDeclaredSubmodules",
			"TestUCIScannerExcludesOversizedSecretAndProtectedFiles",
		),
	},
	{
		name:        "mcp-boundaries",
		scenarioIDs: []string{"U01", "U09", "U23", "U36", "U40", "U43", "U44"},
		packagePath: "./internal/mcp",
		testPattern: uciSecurityCorrectnessPattern(
			"TestUCICodeIntelCompatibilitySearchAndStatusKeepClientContextsDistinct",
			"TestUCICodeIntelStatusRejectsContinuousCheckoutViewDrift",
			"TestHandleToolsCall_ArgumentsNeverReachErrorLogs",
			"TestUCICodebaseContextRejectsCrossClientHandleAndUnboundDefaultReuse",
			"TestUCIFreshnessAfterBarrierTimeoutUsesBoundedStaleResult",
			"TestUCINilExposureRecorderSuppressesAuthorizedResultAndReportsUnavailable",
			"TestUCIExposureSearchAppendsOneOpaqueReceiptAndExactRetryReusesIt",
			"TestUCICompletionAcceptsVerifiedCallbackAndKeepsMismatchClosed",
		),
	},
	{
		name:        "acceptance-authorization-and-rollback",
		scenarioIDs: []string{"U22", "U42", "U45", "U46"},
		packagePath: "./tests/uci/acceptance",
		testPattern: uciSecurityCorrectnessPattern(
			"TestUCIAuthorizationMatrix",
			"TestUCIMigrationRollbackLegacyOnlyAcceptance",
		),
	},
}

func TestUCISecurityCorrectness(t *testing.T) {
	root := uciSecurityCorrectnessRoot(t)
	scenarioGroups := uciSecurityCorrectnessValidateMatrix(t, root)
	executedGroups := make(map[string]bool, len(uciSecurityCorrectnessGroups))

	for _, group := range uciSecurityCorrectnessGroups {
		group := group
		t.Run(group.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), uciSecurityCorrectnessCommandTimeout)
			defer cancel()

			command := exec.CommandContext(
				ctx,
				"go",
				"test",
				group.packagePath,
				"-run",
				group.testPattern,
				"-count=1",
				"-timeout="+uciSecurityCorrectnessChildTimeout.String(),
				"-json",
			)
			command.Dir = root
			command.Env = os.Environ()
			output, err := command.CombinedOutput()
			executedGroups[group.name] = true

			scenarios := strings.Join(group.scenarioIDs, ", ")
			if ctx.Err() != nil {
				t.Errorf("security behavioral group %q for scenarios %s exceeded %s", group.name, scenarios, uciSecurityCorrectnessCommandTimeout)
				return
			}
			if err != nil {
				t.Errorf("security behavioral group %q for scenarios %s failed: %v", group.name, scenarios, err)
				return
			}
			if !uciSecurityCorrectnessRanBehavioralTest(output) {
				t.Errorf("security behavioral group %q for scenarios %s ran no behavioral test", group.name, scenarios)
			}
		})
	}

	for _, scenarioID := range uciSecurityCorrectnessAssignedScenarioIDs {
		if !executedGroups[scenarioGroups[scenarioID]] {
			t.Errorf("assigned scenario %q has no executed behavioral group", scenarioID)
		}
	}
}

func uciSecurityCorrectnessPattern(testNames ...string) string {
	return "^(" + strings.Join(testNames, "|") + ")$"
}

func uciSecurityCorrectnessRoot(t *testing.T) string {
	t.Helper()

	workingDirectory, err := os.Getwd()
	if err != nil {
		t.Fatal("locate UCI acceptance package")
	}
	return filepath.Clean(filepath.Join(workingDirectory, "..", "..", ".."))
}

func uciSecurityCorrectnessValidateMatrix(t *testing.T, root string) map[string]string {
	t.Helper()

	expectedIDs := make(map[string]struct{}, len(uciSecurityCorrectnessAssignedScenarioIDs))
	for _, scenarioID := range uciSecurityCorrectnessAssignedScenarioIDs {
		if _, duplicate := expectedIDs[scenarioID]; duplicate {
			t.Fatalf("assigned scenario set repeats %q", scenarioID)
		}
		expectedIDs[scenarioID] = struct{}{}
	}
	if len(expectedIDs) != 35 {
		t.Fatalf("assigned scenario set contains %d IDs, want 35", len(expectedIDs))
	}

	plannedIDs := uciSecurityCorrectnessPlannedScenarioIDs(t, root)
	for _, scenarioID := range uciSecurityCorrectnessAssignedScenarioIDs {
		if _, exists := plannedIDs[scenarioID]; !exists {
			t.Fatalf("assigned scenario %q is absent from scenarios.json", scenarioID)
		}
	}

	groupNames := make(map[string]struct{}, len(uciSecurityCorrectnessGroups))
	scenarioGroups := make(map[string]string, len(uciSecurityCorrectnessAssignedScenarioIDs))
	for _, group := range uciSecurityCorrectnessGroups {
		if group.name == "" || group.packagePath == "" || group.testPattern == "" {
			t.Fatal("security behavioral group has an empty name, package, or test pattern")
		}
		if !strings.HasPrefix(group.packagePath, "./") {
			t.Fatalf("security behavioral group %q package path %q is not repository-relative", group.name, group.packagePath)
		}
		if !strings.HasPrefix(group.testPattern, "^") || !strings.HasSuffix(group.testPattern, "$") {
			t.Fatalf("security behavioral group %q test pattern %q is not anchored", group.name, group.testPattern)
		}
		if _, duplicate := groupNames[group.name]; duplicate {
			t.Fatalf("security behavioral group name %q is duplicated", group.name)
		}
		groupNames[group.name] = struct{}{}
		if len(group.scenarioIDs) == 0 {
			t.Fatalf("security behavioral group %q has no assigned scenarios", group.name)
		}

		for _, scenarioID := range group.scenarioIDs {
			if _, expected := expectedIDs[scenarioID]; !expected {
				t.Fatalf("security behavioral group %q assigns unexpected scenario %q", group.name, scenarioID)
			}
			if previous, duplicate := scenarioGroups[scenarioID]; duplicate {
				t.Fatalf("assigned scenario %q is duplicated by groups %q and %q", scenarioID, previous, group.name)
			}
			scenarioGroups[scenarioID] = group.name
		}
	}

	for _, scenarioID := range uciSecurityCorrectnessAssignedScenarioIDs {
		if _, assigned := scenarioGroups[scenarioID]; !assigned {
			t.Fatalf("assigned scenario %q has no behavioral group", scenarioID)
		}
	}

	return scenarioGroups
}

func uciSecurityCorrectnessPlannedScenarioIDs(t *testing.T, root string) map[string]struct{} {
	t.Helper()

	encoded, err := os.ReadFile(filepath.Join(root, uciSecurityCorrectnessScenarioFile))
	if err != nil {
		t.Fatalf("read UCI scenarios.json: %v", err)
	}
	var plan struct {
		Scenarios []struct {
			ID string `json:"id"`
		} `json:"scenarios"`
	}
	if err := json.Unmarshal(encoded, &plan); err != nil {
		t.Fatalf("decode UCI scenarios.json: %v", err)
	}

	ids := make(map[string]struct{}, len(plan.Scenarios))
	for _, scenario := range plan.Scenarios {
		if scenario.ID == "" {
			t.Fatal("scenarios.json contains an empty scenario ID")
		}
		if _, duplicate := ids[scenario.ID]; duplicate {
			t.Fatalf("scenarios.json repeats scenario %q", scenario.ID)
		}
		ids[scenario.ID] = struct{}{}
	}
	return ids
}

func uciSecurityCorrectnessRanBehavioralTest(output []byte) bool {
	for _, line := range bytes.Split(output, []byte{'\n'}) {
		var event struct {
			Action string
			Test   string
		}
		if json.Unmarshal(line, &event) == nil && event.Action == "run" && event.Test != "" {
			return true
		}
	}
	return false
}
