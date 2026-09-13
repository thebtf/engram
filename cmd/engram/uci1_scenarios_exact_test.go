package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"
)

const (
	uci1ExactCandidateTestTimeout     = 2 * time.Minute
	uci1ExactCandidateMatrixTimeout   = 12 * time.Minute
	uci1ExactCandidateTimeoutHeadroom = 30 * time.Second
)

type uci1ExactCandidateCommand struct {
	packagePath string
	testNames   []string
	timeout     time.Duration
}

type uci1ExactCandidateGroup struct {
	name         string
	commands     []uci1ExactCandidateCommand
	scenarioIDs  []string
	evidenceOnly bool
}

var uci1ExactCandidateRequiredScenarioIDs = [...]string{
	"U04", "U05", "U06", "U07", "U09", "U10", "U11", "U12", "U13", "U14", "U15",
	"U17", "U18", "U20", "U21", "U22", "U23", "U24", "U29", "U36", "U37", "U38",
	"U39", "U42", "U45", "U46",
}

var uci1ExactCandidateGroups = [...]uci1ExactCandidateGroup{
	{
		name:        "publication",
		scenarioIDs: []string{"U04", "U05", "U06"},
		commands: []uci1ExactCandidateCommand{{
			packagePath: "./internal/db/gorm",
			testNames: []string{
				"TestUCIPublishIncompletePartsAndEOF",
				"TestUCIReplayFinalizeAfterLostACKAndLaterPublish",
				"TestUCILeaseExpiryTakeoverAndStaleWriter",
			},
			timeout: uci1ExactCandidateTestTimeout,
		}},
	},
	{
		name:        "recovery",
		scenarioIDs: []string{"U07"},
		commands: []uci1ExactCandidateCommand{{
			packagePath: "./internal/uci",
			testNames: []string{
				"TestUCIRestartAndOfflineRecoveryRescansCurrentBytesWithoutReembedding",
			},
			timeout: uci1ExactCandidateTestTimeout,
		}},
	},
	{
		name:        "git",
		scenarioIDs: []string{"U09", "U10", "U11", "U12"},
		commands: []uci1ExactCandidateCommand{{
			packagePath: "./internal/uci",
			testNames: []string{
				"TestUCIRecoveryHandlesBranchDetachedUnbornAndFailedScans",
				"TestUCIIncarnationMoveContinuityAndRecreatedCheckoutLeaveOldViewHistorical",
			},
			timeout: uci1ExactCandidateTestTimeout,
		}},
	},
	{
		name:        "scanner",
		scenarioIDs: []string{"U13", "U14", "U15", "U37", "U39"},
		commands: []uci1ExactCandidateCommand{{
			packagePath: "./internal/uci",
			testNames: []string{
				"TestUCIScannerUsesGitNULCandidateCensusAndNestedIgnores",
				"TestUCIScannerRefusesReparseEscapesAndUnsupportedTypes",
				"TestUCIScannerCensusCannotAuthorizeDeleteAllByAccident",
				"TestUCIScannerPreservesWindowsUnicodeCaseAndCRLFBytes",
				"TestUCIScannerExcludesNestedRepositoriesAndDeclaredSubmodules",
			},
			timeout: uci1ExactCandidateTestTimeout,
		}},
	},
	{
		name:        "search",
		scenarioIDs: []string{"U17", "U18", "U24", "U38"},
		commands: []uci1ExactCandidateCommand{{
			packagePath: "./internal/db/gorm",
			testNames: []string{
				"TestUCIProjectionStoreSemanticMethodsKeepVectorsScopedAndCovered",
				"TestUCIEmbeddingReusesUnchangedInputAcrossCheckouts",
			},
			timeout: uci1ExactCandidateTestTimeout,
		}},
	},
	{
		name:        "graph",
		scenarioIDs: []string{"U20", "U21", "U29"},
		commands: []uci1ExactCandidateCommand{{
			packagePath: "./internal/uci",
			testNames: []string{
				"TestUCIGraphReportsAmbiguityUnresolvedSitesAndChangedCalleeInvalidation",
				"TestUCIGoExtractionReturnsFreshPartialFactsForMalformedSource",
				"TestUCIGraphPathImpactFlowAndCyclesRemainStaticAndViewPinned",
				"TestUCIGraphDistinguishesConclusiveAbsenceFromUnknownOrTruncated",
			},
			timeout: uci1ExactCandidateTestTimeout,
		}},
	},
	{
		name:        "security",
		scenarioIDs: []string{"U22", "U23", "U42", "U46"},
		commands: []uci1ExactCandidateCommand{
			{
				packagePath: "./tests/uci/acceptance",
				testNames: []string{
					"TestUCIAuthorizationMatrix",
				},
				timeout: uci1ExactCandidateTestTimeout,
			},
			{
				packagePath: "./internal/mcp",
				testNames: []string{
					"TestHandleToolsCall_ArgumentsNeverReachErrorLogs",
				},
				timeout: uci1ExactCandidateTestTimeout,
			},
		},
	},
	{
		name:        "context",
		scenarioIDs: []string{"U36"},
		commands: []uci1ExactCandidateCommand{{
			packagePath: "./internal/mcp",
			testNames: []string{
				"TestUCICodebaseContextResolveAmbiguityIsClosedBeforeAuthorizationOrProjection",
			},
			timeout: uci1ExactCandidateTestTimeout,
		}},
	},
	{
		name:         "freshness",
		evidenceOnly: true,
		commands: []uci1ExactCandidateCommand{{
			packagePath: "./internal/mcp",
			testNames: []string{
				"TestUCIFreshnessAfterBarrierTimeoutUsesBoundedStaleResult",
			},
			timeout: uci1ExactCandidateTestTimeout,
		}},
	},
	{
		name:        "completion",
		scenarioIDs: []string{"U45"},
		commands: []uci1ExactCandidateCommand{{
			packagePath: "./internal/mcp",
			testNames: []string{
				"TestUCICompletionAcceptsVerifiedCallbackAndKeepsMismatchClosed",
			},
			timeout: uci1ExactCandidateTestTimeout,
		}},
	},
	{
		name:         "t082-security-correctness",
		evidenceOnly: true,
		commands: []uci1ExactCandidateCommand{{
			packagePath: "./tests/uci/acceptance",
			testNames: []string{
				"TestUCISecurityCorrectness",
			},
			timeout: uci1ExactCandidateMatrixTimeout,
		}},
	},
}

// uci1CollectExactCandidateEvidence records only passing, current-candidate
// behavioral groups. It intentionally neither runs nor calls TestUCI1Scenarios.
func uci1CollectExactCandidateEvidence(ctx context.Context, root string) (map[string]uci1ScenarioEvidence, error) {
	if ctx == nil {
		return nil, errors.New("exact candidate evidence requires a context")
	}
	if strings.TrimSpace(root) == "" {
		return nil, errors.New("exact candidate evidence requires a candidate root")
	}
	if err := uci1ExactCandidateValidateGroups(); err != nil {
		return nil, err
	}

	evidence := make(map[string]uci1ScenarioEvidence, len(uci1ExactCandidateRequiredScenarioIDs))
	for _, group := range uci1ExactCandidateGroups {
		if err := uci1ExactCandidateRunGroup(ctx, root, group); err != nil {
			return nil, err
		}
		digest := uci1ExactCandidateGroupDigest(group)
		for _, scenarioID := range group.scenarioIDs {
			if _, duplicate := evidence[scenarioID]; duplicate {
				return nil, errors.New("exact candidate evidence assigns a scenario more than once")
			}
			evidence[scenarioID] = uci1ScenarioEvidence{
				Mode:   uci1ScenarioModeExactCandidate,
				Code:   uci1ScenarioCodeExactCandidate,
				Digest: digest,
			}
		}
	}
	if len(evidence) != len(uci1ExactCandidateRequiredScenarioIDs) {
		return nil, errors.New("exact candidate evidence does not cover the assigned scenario set")
	}
	for _, scenarioID := range uci1ExactCandidateRequiredScenarioIDs {
		if _, found := evidence[scenarioID]; !found {
			return nil, errors.New("exact candidate evidence misses an assigned scenario")
		}
	}
	return evidence, nil
}

func uci1ExactCandidateValidateGroups() error {
	expected, err := uci1ExactCandidateExpectedScenarios()
	if err != nil {
		return err
	}
	groups := make(map[string]struct{}, len(uci1ExactCandidateGroups))
	covered := make(map[string]struct{}, len(uci1ExactCandidateRequiredScenarioIDs))
	for _, group := range uci1ExactCandidateGroups {
		if err := uci1ExactCandidateValidateGroup(group, expected, groups, covered); err != nil {
			return err
		}
	}
	if len(covered) != len(expected) {
		return errors.New("exact candidate groups do not cover the assigned scenario set")
	}
	for scenarioID := range expected {
		if _, found := covered[scenarioID]; !found {
			return errors.New("exact candidate groups miss an assigned scenario")
		}
	}
	return nil
}

func uci1ExactCandidateExpectedScenarios() (map[string]struct{}, error) {
	expected := make(map[string]struct{}, len(uci1ExactCandidateRequiredScenarioIDs))
	for _, scenarioID := range uci1ExactCandidateRequiredScenarioIDs {
		if _, duplicate := expected[scenarioID]; duplicate {
			return nil, errors.New("exact candidate scenario set is duplicated")
		}
		expected[scenarioID] = struct{}{}
	}
	return expected, nil
}

func uci1ExactCandidateValidateGroup(group uci1ExactCandidateGroup, expected, groups, covered map[string]struct{}) error {
	if group.name == "" || len(group.commands) == 0 {
		return errors.New("exact candidate group is incomplete")
	}
	if _, duplicate := groups[group.name]; duplicate {
		return errors.New("exact candidate group is duplicated")
	}
	groups[group.name] = struct{}{}
	if group.evidenceOnly == (len(group.scenarioIDs) != 0) {
		return errors.New("exact candidate group evidence mode is invalid")
	}
	for _, command := range group.commands {
		if err := uci1ExactCandidateValidateCommand(command); err != nil {
			return err
		}
	}
	for _, scenarioID := range group.scenarioIDs {
		if _, expectedScenario := expected[scenarioID]; !expectedScenario {
			return errors.New("exact candidate group assigns an excluded scenario")
		}
		if _, duplicate := covered[scenarioID]; duplicate {
			return errors.New("exact candidate group assigns a scenario more than once")
		}
		covered[scenarioID] = struct{}{}
	}
	return nil
}

func uci1ExactCandidateValidateCommand(command uci1ExactCandidateCommand) error {
	if !strings.HasPrefix(command.packagePath, "./") || len(command.testNames) == 0 || command.timeout <= 0 {
		return errors.New("exact candidate command is incomplete")
	}
	tests := make(map[string]struct{}, len(command.testNames))
	for _, testName := range command.testNames {
		if testName == "" || testName == "TestUCI1Scenarios" {
			return errors.New("exact candidate command includes an unsafe test")
		}
		if _, duplicate := tests[testName]; duplicate {
			return errors.New("exact candidate command repeats a test")
		}
		tests[testName] = struct{}{}
	}
	return nil
}

func uci1ExactCandidateRunGroup(ctx context.Context, root string, group uci1ExactCandidateGroup) error {
	for _, command := range group.commands {
		if err := uci1ExactCandidateRunCommand(ctx, root, group.name, command); err != nil {
			return err
		}
	}
	return nil
}

func uci1ExactCandidateRunCommand(ctx context.Context, root, groupName string, candidate uci1ExactCandidateCommand) error {
	commandContext, cancel := context.WithTimeout(ctx, candidate.timeout+uci1ExactCandidateTimeoutHeadroom)
	defer cancel()

	arguments := []string{
		"test",
		candidate.packagePath,
		"-run", uci1ExactCandidateTestPattern(candidate.testNames),
		"-count=1",
		"-timeout=" + candidate.timeout.String(),
		"-json",
	}
	command := exec.CommandContext(commandContext, "go", arguments...)
	command.Dir = root
	command.Env = append([]string(nil), os.Environ()...)
	command.Stderr = io.Discard
	stdout, err := command.StdoutPipe()
	if err != nil {
		return fmt.Errorf("start exact candidate group %q: open output stream", groupName)
	}
	if err := command.Start(); err != nil {
		return fmt.Errorf("start exact candidate group %q", groupName)
	}

	required := make(map[string]struct{}, len(candidate.testNames))
	passed := make(map[string]struct{}, len(candidate.testNames))
	for _, testName := range candidate.testNames {
		required[testName] = struct{}{}
	}
	decoder := json.NewDecoder(stdout)
	var decodeErr error
	for {
		var event struct {
			Action string
			Test   string
		}
		err := decoder.Decode(&event)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			decodeErr = errors.New("decode exact candidate test events")
			_, _ = io.Copy(io.Discard, stdout)
			break
		}
		if event.Action == "pass" {
			if _, expected := required[event.Test]; expected {
				passed[event.Test] = struct{}{}
			}
		}
	}
	waitErr := command.Wait()
	if commandContext.Err() != nil {
		return fmt.Errorf("exact candidate group %q exceeded its bounded runtime", groupName)
	}
	if decodeErr != nil {
		return fmt.Errorf("exact candidate group %q produced invalid test events", groupName)
	}
	if waitErr != nil {
		return fmt.Errorf("exact candidate group %q failed", groupName)
	}
	for testName := range required {
		if _, found := passed[testName]; !found {
			return fmt.Errorf("exact candidate group %q did not pass every named test", groupName)
		}
	}
	return nil
}

func uci1ExactCandidateTestPattern(testNames []string) string {
	return "^(" + strings.Join(testNames, "|") + ")$"
}

func uci1ExactCandidateGroupDigest(group uci1ExactCandidateGroup) string {
	values := []string{"uci1-exact-candidate-evidence/v1", group.name}
	for _, command := range group.commands {
		values = append(values,
			"go",
			"test",
			command.packagePath,
			"-run",
			uci1ExactCandidateTestPattern(command.testNames),
			"-count=1",
			"-timeout="+command.timeout.String(),
			"-json",
		)
		values = append(values, command.testNames...)
	}
	return uciInstalledReceiptDigestStrings(values...)
}
