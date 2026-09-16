package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/thebtf/engram/internal/privacy"
)

const (
	uci1HistoricalT099ReceiptPathEnv  = "ENGRAM_UCI_T099_RECEIPT_PATH"
	uci1HistoricalT099CandidateCommit = "b021edc7107e468b4041c27f942955c03e3a4081"
	uci1HistoricalT099CandidateTree   = "0dce843966b22289bfdf36a91a5048e9e5b16837"
	uci1HistoricalMaxReceiptBytes     = 1 << 20
)

type uci1HistoricalFocusedGroup struct {
	scenarioID  string
	packagePath string
	testPattern string
	testNames   []string
}

var uci1HistoricalFocusedGroups = [...]uci1HistoricalFocusedGroup{
	{
		scenarioID:  "U16",
		packagePath: "./internal/uci",
		testPattern: "^(TestUCIVectorProfileCacheReusesOnlyExactCompatibleInput|TestUCISemanticProviderFailuresRemainLexicalAndVisible)$",
		testNames: []string{
			"TestUCIVectorProfileCacheReusesOnlyExactCompatibleInput",
			"TestUCISemanticProviderFailuresRemainLexicalAndVisible",
		},
	},
	{
		scenarioID:  "U19",
		packagePath: "./internal/uci",
		testPattern: "^(TestUCIReconcileChangedCalleeReResolvesUnchangedCallerAtomically|TestUCIGraphReportsAmbiguityUnresolvedSitesAndChangedCalleeInvalidation)$",
		testNames: []string{
			"TestUCIReconcileChangedCalleeReResolvesUnchangedCallerAtomically",
			"TestUCIGraphReportsAmbiguityUnresolvedSitesAndChangedCalleeInvalidation",
		},
	},
	{
		scenarioID:  "U25",
		packagePath: "./internal/uci",
		testPattern: "^TestUCITreeSitterWorkerFramesBuiltParserFacts$",
		testNames: []string{
			"TestUCITreeSitterWorkerFramesBuiltParserFacts",
		},
	},
	{
		scenarioID:  "U25",
		packagePath: "./internal/handlers/codeintel",
		testPattern: "^TestUCIPreparedIndexResolvesTreeSitterModulesAliasesAndCalls$",
		testNames: []string{
			"TestUCIPreparedIndexResolvesTreeSitterModulesAliasesAndCalls",
		},
	},
}

// uci1CollectHistoricalInstalledEvidence revalidates the three historical
// T099 claims whose current risks are covered by deterministic candidate tests.
func uci1CollectHistoricalInstalledEvidence(ctx context.Context, root string) (map[string]uci1ScenarioEvidence, error) {
	if ctx == nil {
		return nil, errors.New("historical UCI evidence requires a context")
	}

	receiptDigest, err := uci1HistoricalT099ReceiptDigest(root)
	if err != nil {
		return nil, err
	}

	testDigests := make(map[string][]string, 3)
	for _, group := range uci1HistoricalFocusedGroups {
		digest, err := uci1HistoricalRunFocusedGroup(ctx, root, group)
		if err != nil {
			return nil, err
		}
		testDigests[group.scenarioID] = append(testDigests[group.scenarioID], digest)
	}

	evidence := make(map[string]uci1ScenarioEvidence, 3)
	for _, scenarioID := range [...]string{"U16", "U19", "U25"} {
		digests := testDigests[scenarioID]
		if len(digests) == 0 {
			return nil, errors.New("historical UCI evidence has no focused test identity")
		}
		evidence[scenarioID] = uci1ScenarioEvidence{
			Mode:   uci1ScenarioModeHistoricalInstalled,
			Code:   uci1ScenarioCodeHistoricalInstalled,
			Digest: uci1HistoricalDigestStrings(append([]string{"uci1-historical-installed-revalidated/v1", scenarioID, receiptDigest}, digests...)...),
		}
	}
	return evidence, nil
}

func uci1HistoricalT099ReceiptDigest(root string) (string, error) {
	receiptPath, err := uci1HistoricalReceiptPath(root)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(receiptPath)
	if err != nil || !info.Mode().IsRegular() || info.Size() <= 0 || info.Size() > uci1HistoricalMaxReceiptBytes {
		return "", errors.New("historical UCI receipt is unavailable")
	}
	raw, err := os.ReadFile(receiptPath)
	if err != nil || len(raw) == 0 {
		return "", errors.New("read historical UCI receipt")
	}

	var record uciRealCorpusRecord
	if err := decodeStrictJSON(raw, &record); err != nil {
		return "", errors.New("historical UCI receipt shape is invalid")
	}
	if err := uci1HistoricalValidateT099Record(record, raw); err != nil {
		return "", err
	}

	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}

func uci1HistoricalReceiptPath(root string) (string, error) {
	configured := strings.TrimSpace(os.Getenv(uci1HistoricalT099ReceiptPathEnv))
	if configured == "" {
		return "", errors.New("historical UCI receipt requires ENGRAM_UCI_T099_RECEIPT_PATH")
	}
	if !filepath.IsAbs(configured) {
		return "", errors.New("historical UCI receipt path must be absolute")
	}

	candidateRoot, err := filepath.Abs(root)
	if err != nil {
		return "", errors.New("resolve UCI candidate root")
	}
	candidateRoot, err = filepath.EvalSymlinks(candidateRoot)
	if err != nil {
		return "", errors.New("resolve UCI candidate root")
	}
	receiptPath, err := filepath.EvalSymlinks(configured)
	if err != nil {
		return "", errors.New("resolve historical UCI receipt")
	}
	if uci1HistoricalPathInside(candidateRoot, receiptPath) {
		return "", errors.New("historical UCI receipt must be outside the candidate")
	}
	return receiptPath, nil
}

func uci1HistoricalPathInside(root, candidate string) bool {
	relative, err := filepath.Rel(root, candidate)
	if err != nil {
		return false
	}
	return relative == "." || (relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) && !filepath.IsAbs(relative))
}

func uci1HistoricalValidateT099Record(record uciRealCorpusRecord, raw []byte) error {
	if err := uciRealCorpusValidateRecordForWrite(record); err != nil {
		return errors.New("historical UCI receipt does not satisfy the accepted v3 shape")
	}
	if record.Candidate.Commit != uci1HistoricalT099CandidateCommit || record.Candidate.Tree != uci1HistoricalT099CandidateTree {
		return errors.New("historical UCI receipt is not bound to the accepted T099 candidate")
	}
	if !record.Installed.StandardMCP || !uciInstalledReceiptValidSHA256(record.Installed.ServerSHA256) || !uciInstalledReceiptValidSHA256(record.Installed.DaemonSHA256) || !uciInstalledReceiptValidSHA256(record.Installed.ParserSHA256) {
		return errors.New("historical UCI receipt installed artifact binding is incomplete")
	}
	if record.Scope.EvidenceRecordSecretsRetained || record.Scope.EvidenceRecordSourceBodiesRetained || record.Scope.EvidenceRecordPrivateLocatorsRetained || record.Scope.ProductionMutation || record.Scope.ReleaseClaim {
		return errors.New("historical UCI receipt scope is not reusable")
	}

	var value any
	if err := json.Unmarshal(raw, &value); err != nil || uciRealCorpusEvidenceValueHasPrivateLocator(value) || privacy.ContainsSecrets(string(raw)) {
		return errors.New("historical UCI receipt redaction boundary is invalid")
	}
	return nil
}

func uci1HistoricalRunFocusedGroup(ctx context.Context, root string, group uci1HistoricalFocusedGroup) (string, error) {
	command := exec.CommandContext(ctx, "go", "test", group.packagePath, "-run", group.testPattern, "-count=1", "-json")
	command.Dir = root
	command.Env = os.Environ()
	stdout, err := command.StdoutPipe()
	if err != nil {
		return "", errors.New("prepare historical UCI focused test")
	}
	command.Stderr = io.Discard
	if err := command.Start(); err != nil {
		return "", errors.New("start historical UCI focused test")
	}

	observed, decodeErr := uci1HistoricalSuccessfulTestEvents(stdout, group.testNames)
	waitErr := command.Wait()
	if ctx.Err() != nil {
		return "", errors.New("historical UCI focused test context ended")
	}
	if decodeErr != nil || waitErr != nil {
		return "", fmt.Errorf("historical UCI focused test group %q failed", group.scenarioID)
	}
	for _, testName := range group.testNames {
		if _, ok := observed[testName]; !ok {
			return "", fmt.Errorf("historical UCI focused test group %q lacked a successful test event", group.scenarioID)
		}
	}

	events := make([]string, 0, len(observed))
	for testName, packageName := range observed {
		events = append(events, packageName+"\x00"+testName)
	}
	sort.Strings(events)
	identity := []string{"go", "test", group.packagePath, "-run", group.testPattern, "-count=1", "-json"}
	identity = append(identity, events...)
	return uci1HistoricalDigestStrings(identity...), nil
}

func uci1HistoricalSuccessfulTestEvents(reader io.Reader, expected []string) (map[string]string, error) {
	expectedNames := make(map[string]struct{}, len(expected))
	for _, name := range expected {
		if name == "" {
			return nil, errors.New("historical UCI focused test identity is invalid")
		}
		expectedNames[name] = struct{}{}
	}

	observed := make(map[string]string, len(expectedNames))
	decoder := json.NewDecoder(reader)
	for {
		var event struct {
			Action  string `json:"Action"`
			Package string `json:"Package"`
			Test    string `json:"Test"`
		}
		err := decoder.Decode(&event)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			_, _ = io.Copy(io.Discard, reader)
			return nil, errors.New("historical UCI focused test emitted invalid JSON")
		}
		if event.Action != "pass" {
			continue
		}
		if _, expected := expectedNames[event.Test]; !expected || event.Package == "" {
			continue
		}
		observed[event.Test] = event.Package
	}
	return observed, nil
}

func uci1HistoricalDigestStrings(values ...string) string {
	digest := sha256.New()
	for _, value := range values {
		_, _ = io.WriteString(digest, value)
		_, _ = digest.Write([]byte{0})
	}
	return hex.EncodeToString(digest.Sum(nil))
}
