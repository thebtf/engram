package main

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"
)

func init() {
	uciRunUCI1InstalledScenarioProbes = uci1RunInstalledScenarioProbes
}

type uci1InstalledScenarioProbeDefinition struct {
	name string
	run  uciInstalledAcceptanceScenarioProbe
}

type uci1InstalledScenarioRuntimeRefresh func(context.Context, uciInstalledAcceptanceScenarioRuntime) (uciInstalledAcceptanceScenarioRuntime, error)

func uci1RunInstalledScenarioProbes(ctx context.Context, runtime uciInstalledAcceptanceScenarioRuntime) (map[string]uciInstalledAcceptanceScenarioEvidence, error) {
	return uci1RunInstalledScenarioProbeSequence(ctx, runtime, []uci1InstalledScenarioProbeDefinition{
		{name: "publication", run: uci1ProbePublicationFaults},
		{name: "search", run: uci1ProbeSearchInstalled},
		{name: "semantic-ru", run: uci1ProbeSemanticRUInstalled},
		{name: "graph", run: uci1ProbeGraphInstalled},
		{name: "security", run: uci1ProbeSecurityInstalled},
		{name: "completion", run: uci1ProbeCompletionInstalled},
		{name: "recovery-git", run: uci1ProbeRecoveryGit},
		{name: "scanner", run: uci1ProbeScannerInstalled},
	}, uci1RefreshInstalledScenarioRuntime)
}

func uci1RunInstalledScenarioProbeSequence(
	ctx context.Context,
	runtime uciInstalledAcceptanceScenarioRuntime,
	probes []uci1InstalledScenarioProbeDefinition,
	refresh uci1InstalledScenarioRuntimeRefresh,
) (map[string]uciInstalledAcceptanceScenarioEvidence, error) {
	if refresh == nil {
		return nil, errors.New("installed UCI scenario runtime refresh is unavailable")
	}
	merged := make(map[string]uciInstalledAcceptanceScenarioEvidence)
	for _, probe := range probes {
		refreshed, err := refresh(ctx, runtime)
		if err != nil {
			return nil, fmt.Errorf("refresh before installed UCI %s probe: %w", probe.name, err)
		}
		runtime = refreshed
		probeCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
		evidence, err := probe.run(probeCtx, runtime)
		cancel()
		if err != nil {
			return nil, fmt.Errorf("installed UCI %s probe: %w", probe.name, err)
		}
		if err := uciValidateInstalledAcceptanceScenarioEvidence(evidence); err != nil {
			return nil, fmt.Errorf("validate installed UCI %s probe evidence: %w", probe.name, err)
		}
		for scenarioID, scenarioEvidence := range evidence {
			if scenarioID == "U19" || scenarioID == "U25" {
				return nil, errors.New("installed UCI scenario probe returned historical-only scenario evidence")
			}
			if _, exists := merged[scenarioID]; exists {
				return nil, fmt.Errorf("duplicate installed UCI scenario probe evidence for %s", scenarioID)
			}
			merged[scenarioID] = scenarioEvidence
		}
	}

	// Scanner fixture cleanup may publish a restored View after its last scenario.
	// Refresh those map-backed handoff values before restart consumes them.
	if _, err := refresh(ctx, runtime); err != nil {
		return nil, fmt.Errorf("refresh after installed UCI scenario probes: %w", err)
	}
	return merged, nil
}

func TestUCI1ScenarioProbeRunnerRefreshesFinalScannerPublication(t *testing.T) {
	const client = uciInstalledAcceptanceClientA
	baselineSelection := uciInstalledAcceptanceSelection{contextHandle: "context-a", viewID: "view-before", runID: "run-before"}
	finalSelection := uciInstalledAcceptanceSelection{contextHandle: "context-a", viewID: "view-after", runID: "run-after"}
	baselinePublication := uciInstalledAcceptancePublication{
		sourceID:       "source-a",
		checkoutID:     "checkout-a",
		viewID:         baselineSelection.viewID,
		profileID:      "profile-a",
		runID:          baselineSelection.runID,
		generation:     20,
		freshnessState: "observed_current",
	}
	finalPublication := baselinePublication
	finalPublication.viewID = finalSelection.viewID
	finalPublication.runID = finalSelection.runID
	finalPublication.generation = 38
	runtime := uciInstalledAcceptanceScenarioRuntime{
		Selections:   map[string]uciInstalledAcceptanceSelection{client: baselineSelection},
		Publications: map[string]uciInstalledAcceptancePublication{client: baselinePublication},
	}

	scannerCompleted := false
	refreshCalls := 0
	evidence, err := uci1RunInstalledScenarioProbeSequence(
		context.Background(),
		runtime,
		[]uci1InstalledScenarioProbeDefinition{{
			name: "scanner",
			run: func(context.Context, uciInstalledAcceptanceScenarioRuntime) (map[string]uciInstalledAcceptanceScenarioEvidence, error) {
				scannerCompleted = true
				return map[string]uciInstalledAcceptanceScenarioEvidence{
					"U39": {Code: uciInstalledAcceptanceScenarioCodeObserved, Digest: uciInstalledAcceptanceStringDigest("scanner")},
				}, nil
			},
		}},
		func(_ context.Context, refreshed uciInstalledAcceptanceScenarioRuntime) (uciInstalledAcceptanceScenarioRuntime, error) {
			refreshCalls++
			if scannerCompleted {
				refreshed.Selections[client] = finalSelection
				refreshed.Publications[client] = finalPublication
			}
			return refreshed, nil
		},
	)
	if err != nil {
		t.Fatalf("run scanner probe sequence: %v", err)
	}
	if len(evidence) != 1 {
		t.Fatalf("scanner evidence count = %d, want 1", len(evidence))
	}
	if refreshCalls != 2 {
		t.Fatalf("scenario refresh calls = %d, want refresh before and after scanner", refreshCalls)
	}
	if got := runtime.Selections[client]; got != finalSelection {
		t.Fatalf("restart selection = %#v, want %#v", got, finalSelection)
	}
	if got := runtime.Publications[client]; got != finalPublication {
		t.Fatalf("restart publication = %#v, want %#v", got, finalPublication)
	}
}

func uci1RefreshInstalledScenarioRuntime(ctx context.Context, runtime uciInstalledAcceptanceScenarioRuntime) (uciInstalledAcceptanceScenarioRuntime, error) {
	clients := map[string]*uciInstalledAcceptanceMCPClient{
		uciInstalledAcceptanceClientA:        runtime.ClientA,
		uciInstalledAcceptanceClientB:        runtime.ClientB,
		uciInstalledAcceptanceClientRecorder: runtime.Recorder,
	}
	for name, client := range clients {
		selection, found := runtime.Selections[name]
		if !found || client == nil {
			continue
		}
		statusSnapshot, err := uciInstalledAcceptanceStatusForSelection(ctx, client, selection)
		if err != nil {
			return runtime, fmt.Errorf("refresh installed UCI scenario %s status: %w", name, err)
		}
		selection.runID = statusSnapshot.runID
		publication, err := uciInstalledAcceptanceStatusPublication(statusSnapshot, selection)
		if err != nil {
			return runtime, fmt.Errorf("refresh installed UCI scenario %s publication: %w", name, err)
		}
		selection.viewID = publication.viewID
		runtime.Selections[name] = selection
		runtime.Publications[name] = publication
	}
	return runtime, nil
}
