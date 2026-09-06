package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

const (
	// UCIInstalledReceiptSchemaVersion is the only accepted installed-receipt schema.
	UCIInstalledReceiptSchemaVersion = "engram.uci-installed-receipt/v1"

	uciInstalledReceiptCompletionVerified = "verified_complete"
	uciInstalledReceiptFixtureClass       = "synthetic_redacted"
	uciInstalledReceiptRuntimeClass       = "installed_standard_clients"
	uciInstalledReceiptNotClaimed         = "not_claimed"
	uciInstalledReceiptNotRetained        = "not_retained"
	uciInstalledReceiptNotObserved        = "not_observed"
	uciInstalledReceiptReservedLoopback   = "reserved_loopback"
)

var (
	uciInstalledReceiptClients   = [...]string{uciInstalledAcceptanceClientA, uciInstalledAcceptanceClientB, uciInstalledAcceptanceClientC}
	uciInstalledReceiptABClients = [...]string{uciInstalledAcceptanceClientA, uciInstalledAcceptanceClientB}
	uciInstalledReceiptRoles     = [...]string{"server", "daemon", "parser"}
	uciInstalledReceiptTools     = [...]string{
		"codebase_context",
		"codebase_index",
		"codebase_status",
		"codebase_search",
		"codebase_graph",
		"codebase_read",
	}
)

type uciInstalledReceiptRefusalRequirement struct {
	name      string
	status    string
	errorCode string
}

var uciInstalledReceiptRefusals = [...]uciInstalledReceiptRefusalRequirement{
	{name: "denied", status: "forbidden", errorCode: "PERMISSION_DENIED"},
	{name: "revoked", status: "forbidden", errorCode: "PERMISSION_DENIED"},
	{name: "mismatched", status: "context_required", errorCode: "CONTEXT_MISMATCH"},
	{name: "private", status: "context_required", errorCode: "CONTEXT_MISMATCH"},
}

// UCIInstalledReceipt is a redacted deterministic projection of one completed
// installed-standard-client acceptance result. It is evidence for the
// synthetic fixture only; it deliberately does not claim production behavior
// or retain credentials, private locators, or source bodies.
type UCIInstalledReceipt struct {
	SchemaVersion string                         `json:"schema_version"`
	Completion    string                         `json:"completion"`
	Scope         UCIInstalledReceiptScope       `json:"scope"`
	Candidate     UCIInstalledReceiptCandidate   `json:"candidate"`
	Fixture       UCIInstalledReceiptFixture     `json:"fixture"`
	Runtime       UCIInstalledReceiptRuntime     `json:"runtime"`
	Clients       []UCIInstalledReceiptClient    `json:"clients"`
	Worktrees     UCIInstalledReceiptWorktrees   `json:"worktrees"`
	Bootstrap     UCIInstalledReceiptBootstrap   `json:"bootstrap"`
	Contexts      []UCIInstalledReceiptContext   `json:"contexts"`
	Retrieval     []UCIInstalledReceiptRetrieval `json:"retrieval"`
	Defaults      []UCIInstalledReceiptDefault   `json:"defaults"`
	Refusals      []UCIInstalledReceiptOutcome   `json:"refusals"`
	Recorder      UCIInstalledReceiptRecorder    `json:"recorder"`
	Watcher       UCIInstalledReceiptWatcher     `json:"watcher"`
	Restart       UCIInstalledReceiptRestart     `json:"restart"`
	Cleanup       UCIInstalledReceiptCleanup     `json:"cleanup"`
}

// UCIInstalledReceiptScope records both the bounded evidence claim and every
// intentionally unsupported claim class.
type UCIInstalledReceiptScope struct {
	FixtureClass    string `json:"fixture_class"`
	RuntimeClass    string `json:"runtime_class"`
	Production      string `json:"production"`
	Credentials     string `json:"credentials"`
	SourceBodies    string `json:"source_bodies"`
	PrivateLocators string `json:"private_locators"`
}

// UCIInstalledReceiptCandidate binds the receipt to the exact artifact bytes
// passed through the versioned installation harness.
type UCIInstalledReceiptCandidate struct {
	AcceptanceVersion string                              `json:"acceptance_version"`
	HarnessVersion    string                              `json:"harness_version"`
	Artifacts         []UCIInstalledReceiptArtifactDigest `json:"artifacts"`
}

// UCIInstalledReceiptArtifactDigest records an equality-proven candidate and
// installed digest without retaining either executable's location or bytes.
type UCIInstalledReceiptArtifactDigest struct {
	Role            string `json:"role"`
	CandidateSHA256 string `json:"candidate_sha256"`
	InstalledSHA256 string `json:"installed_sha256"`
}

// UCIInstalledReceiptFixture binds the synthetic fixture by digest only.
type UCIInstalledReceiptFixture struct {
	Class               string `json:"class"`
	ManifestDigest      string `json:"manifest_digest"`
	RelativePathDigest  string `json:"relative_path_digest"`
	SharedSymbolDigest  string `json:"shared_symbol_digest"`
	PrimarySourceDigest string `json:"primary_source_digest"`
	LinkedSourceDigest  string `json:"linked_source_digest"`
	PrimaryCalleeDigest string `json:"primary_callee_digest"`
	LinkedCalleeDigest  string `json:"linked_callee_digest"`
}

// UCIInstalledReceiptRuntime records installed-process and parser facts while
// omitting PIDs, ports, command lines, database settings, and filesystem roots.
type UCIInstalledReceiptRuntime struct {
	StandardMCPOnly   bool                              `json:"standard_mcp_only"`
	ExternalChildren  bool                              `json:"external_children"`
	Loopback          string                            `json:"loopback"`
	ReservedPortCount int                               `json:"reserved_port_count"`
	Parser            UCIInstalledReceiptParserEvidence `json:"parser"`
}

// UCIInstalledReceiptParserEvidence records only installed-parser facts.
type UCIInstalledReceiptParserEvidence struct {
	UsedInstalledArtifact bool   `json:"used_installed_artifact"`
	InvocationCount       int    `json:"invocation_count"`
	RequestDigest         string `json:"request_digest"`
}

// UCIInstalledReceiptClient summarizes one standard-client protocol transcript
// with digests rather than copying method or tool lists into a receipt.
type UCIInstalledReceiptClient struct {
	Name                 string `json:"name"`
	UsedStdio            bool   `json:"used_stdio"`
	MethodCount          int    `json:"method_count"`
	MethodSequenceDigest string `json:"method_sequence_digest"`
	ToolCount            int    `json:"tool_count"`
	ToolSurfaceDigest    string `json:"tool_surface_digest"`
}

// UCIInstalledReceiptWorktrees records the constructed dirty-worktree topology
// with safe identity digests only.
type UCIInstalledReceiptWorktrees struct {
	RegisteredClients      []string `json:"registered_clients"`
	UsedGitArgumentVectors bool     `json:"used_git_argument_vectors"`
	LinkedGitFile          bool     `json:"linked_git_file"`
	SameHead               bool     `json:"same_head"`
	PrimaryDirty           bool     `json:"primary_dirty"`
	LinkedDirty            bool     `json:"linked_dirty"`
	SharedHeadDigest       string   `json:"shared_head_digest"`
	RelativePathDigest     string   `json:"relative_path_digest"`
}

// UCIInstalledReceiptBootstrap records the View-free bootstrap evidence for A
// and B plus the intentionally closed unbound-client result.
type UCIInstalledReceiptBootstrap struct {
	Clients       []UCIInstalledReceiptBootstrapClient `json:"clients"`
	UnboundClient UCIInstalledReceiptOutcome           `json:"unbound_client"`
}

// UCIInstalledReceiptBootstrapClient is one observed bootstrap state.
type UCIInstalledReceiptBootstrapClient struct {
	Name              string `json:"name"`
	InitialViewAbsent bool   `json:"initial_view_absent"`
	IndexStarted      bool   `json:"index_started"`
	StatusState       string `json:"status_state"`
	BarrierState      string `json:"barrier_state"`
}

// UCIInstalledReceiptContext records a selected UCI context by digest.
type UCIInstalledReceiptContext struct {
	Client         string `json:"client"`
	SourceDigest   string `json:"source_digest"`
	CheckoutDigest string `json:"checkout_digest"`
	ViewDigest     string `json:"view_digest"`
}

// UCIInstalledReceiptRetrieval binds the search, graph, and read evidence for
// one selected context without retaining source text or graph payloads.
type UCIInstalledReceiptRetrieval struct {
	Client               string `json:"client"`
	SearchArtifactDigest string `json:"search_artifact_digest"`
	GraphCalleeDigest    string `json:"graph_callee_digest"`
	ReadArtifactDigest   string `json:"read_artifact_digest"`
}

// UCIInstalledReceiptDefault records before/after third-client default View
// identity for an already selected client.
type UCIInstalledReceiptDefault struct {
	Client                      string `json:"client"`
	BeforeThirdClientViewDigest string `json:"before_third_client_view_digest"`
	AfterThirdClientViewDigest  string `json:"after_third_client_view_digest"`
}

// UCIInstalledReceiptOutcome preserves a closed result's status and code only.
// NoContextData is true only when all contextual, content, graph, and exposure
// fields were absent from the observed result.
type UCIInstalledReceiptOutcome struct {
	Case          string `json:"case"`
	Status        string `json:"status"`
	ErrorCode     string `json:"error_code"`
	NoContextData bool   `json:"no_context_data"`
}

// UCIInstalledReceiptRecorder records unavailable, exact-retry, and mismatch
// behavior without preserving the query, JSON-RPC ID, or exposure reference.
type UCIInstalledReceiptRecorder struct {
	InitialUnavailable            UCIInstalledReceiptOutcome `json:"initial_unavailable"`
	HealthAfterInitialUnavailable string                     `json:"health_after_initial_unavailable"`
	FirstExposureDigest           string                     `json:"first_exposure_digest"`
	ExactRetryExposureDigest      string                     `json:"exact_retry_exposure_digest"`
	HealthBeforeMismatch          string                     `json:"health_before_mismatch"`
	HealthAfterMismatch           string                     `json:"health_after_mismatch"`
	Mismatch                      UCIInstalledReceiptOutcome `json:"mismatch"`
}

// UCIInstalledReceiptPublication records a published View by its already
// redacted identifiers and public state facts.
type UCIInstalledReceiptPublication struct {
	SourceDigest   string `json:"source_digest"`
	CheckoutDigest string `json:"checkout_digest"`
	ViewDigest     string `json:"view_digest"`
	RunDigest      string `json:"run_digest"`
	Generation     int64  `json:"generation"`
	FreshnessState string `json:"freshness_state"`
	BarrierState   string `json:"barrier_state"`
}

// UCIInstalledReceiptWatcher records the write/delete isolation sequence.
type UCIInstalledReceiptWatcher struct {
	AfterWriteA  UCIInstalledReceiptPublication `json:"after_write_a"`
	AfterDeleteA UCIInstalledReceiptPublication `json:"after_delete_a"`
	AfterWriteB  UCIInstalledReceiptPublication `json:"after_write_b"`
	AfterDeleteB UCIInstalledReceiptPublication `json:"after_delete_b"`
}

// UCIInstalledReceiptProjectionCounts records the unchanged projection count
// tuple observed before and after a restart.
type UCIInstalledReceiptProjectionCounts struct {
	Embeddings      int64 `json:"embeddings"`
	ChunkEmbeddings int64 `json:"chunk_embeddings"`
	ResolvedEdges   int64 `json:"resolved_edges"`
}

// UCIInstalledReceiptRestart records restart continuity without PID values.
type UCIInstalledReceiptRestart struct {
	ProcessesReplaced         bool                                `json:"processes_replaced"`
	ProjectionCountsUnchanged bool                                `json:"projection_counts_unchanged"`
	ProjectionCounts          UCIInstalledReceiptProjectionCounts `json:"projection_counts"`
	UnchangedInputReembedded  bool                                `json:"unchanged_input_reembedded"`
	Clients                   []UCIInstalledReceiptClient         `json:"clients"`
	BeforeContexts            []UCIInstalledReceiptContext        `json:"before_contexts"`
	Contexts                  []UCIInstalledReceiptContext        `json:"contexts"`
	Retrieval                 []UCIInstalledReceiptRetrieval      `json:"retrieval"`
}

// UCIInstalledReceiptCleanup proves all disposable acceptance state was closed
// and removed before the receipt was assembled.
type UCIInstalledReceiptCleanup struct {
	ProcessTreeClosed     bool `json:"process_tree_closed"`
	InstallRootRemoved    bool `json:"install_root_removed"`
	FixtureRootRemoved    bool `json:"fixture_root_removed"`
	LocalStateRootRemoved bool `json:"local_state_root_removed"`
}

// BuildUCIInstalledReceipt assembles a deterministic redacted receipt from an
// already completed installed acceptance result. It never reruns the harness,
// reads a filesystem root, calls a service, or persists a receipt.
func BuildUCIInstalledReceipt(result uciInstalledAcceptanceResult) (UCIInstalledReceipt, error) {
	if err := validateUCIInstalledAcceptanceResultForReceipt(result); err != nil {
		return UCIInstalledReceipt{}, err
	}

	clients, err := uciInstalledReceiptClientsFor(result.ClientTranscripts, "initial")
	if err != nil {
		return UCIInstalledReceipt{}, err
	}
	restartClients, err := uciInstalledReceiptClientsFor(result.Restart.ClientTranscripts, "restart")
	if err != nil {
		return UCIInstalledReceipt{}, err
	}

	receipt := UCIInstalledReceipt{
		SchemaVersion: UCIInstalledReceiptSchemaVersion,
		Completion:    uciInstalledReceiptCompletionVerified,
		Scope: UCIInstalledReceiptScope{
			FixtureClass:    uciInstalledReceiptFixtureClass,
			RuntimeClass:    uciInstalledReceiptRuntimeClass,
			Production:      uciInstalledReceiptNotClaimed,
			Credentials:     uciInstalledReceiptNotRetained,
			SourceBodies:    uciInstalledReceiptNotRetained,
			PrivateLocators: uciInstalledReceiptNotRetained,
		},
		Candidate: UCIInstalledReceiptCandidate{
			AcceptanceVersion: result.AcceptanceVersion,
			HarnessVersion:    result.InstallHarnessVersion,
			Artifacts:         uciInstalledReceiptArtifacts(result.Artifacts),
		},
		Fixture: uciInstalledReceiptFixtureFromEvidence(result.Fixture),
		Runtime: UCIInstalledReceiptRuntime{
			StandardMCPOnly:   true,
			ExternalChildren:  true,
			Loopback:          uciInstalledReceiptReservedLoopback,
			ReservedPortCount: len(result.ReservedLoopbackPorts),
			Parser: UCIInstalledReceiptParserEvidence{
				UsedInstalledArtifact: result.Parser.UsedInstalledArtifact,
				InvocationCount:       result.Parser.InvocationCount,
				RequestDigest:         result.Parser.RequestDigest,
			},
		},
		Clients:   clients,
		Worktrees: uciInstalledReceiptWorktreesFromResult(result.Worktrees),
		Bootstrap: uciInstalledReceiptBootstrapFromResult(result.Bootstrap),
		Contexts: []UCIInstalledReceiptContext{
			uciInstalledReceiptContext(uciInstalledAcceptanceClientA, result.ClientContexts[uciInstalledAcceptanceClientA]),
			uciInstalledReceiptContext(uciInstalledAcceptanceClientB, result.ClientContexts[uciInstalledAcceptanceClientB]),
		},
		Retrieval: []UCIInstalledReceiptRetrieval{
			uciInstalledReceiptRetrieval(uciInstalledAcceptanceClientA, result.Observations[uciInstalledAcceptanceClientA]),
			uciInstalledReceiptRetrieval(uciInstalledAcceptanceClientB, result.Observations[uciInstalledAcceptanceClientB]),
		},
		Defaults: []UCIInstalledReceiptDefault{
			{
				Client:                      uciInstalledAcceptanceClientA,
				BeforeThirdClientViewDigest: result.Defaults.BeforeThirdClientViewDigests[uciInstalledAcceptanceClientA],
				AfterThirdClientViewDigest:  result.Defaults.AfterThirdClientViewDigests[uciInstalledAcceptanceClientA],
			},
			{
				Client:                      uciInstalledAcceptanceClientB,
				BeforeThirdClientViewDigest: result.Defaults.BeforeThirdClientViewDigests[uciInstalledAcceptanceClientB],
				AfterThirdClientViewDigest:  result.Defaults.AfterThirdClientViewDigests[uciInstalledAcceptanceClientB],
			},
		},
		Refusals: uciInstalledReceiptRefusalsFromResult(result.Refusals),
		Recorder: UCIInstalledReceiptRecorder{
			InitialUnavailable:            uciInstalledReceiptOutcome("initial_unavailable", result.Recorder.InitialUnavailable),
			HealthAfterInitialUnavailable: result.Recorder.HealthAfterInitialUnavailable,
			FirstExposureDigest:           result.Recorder.FirstExposureDigest,
			ExactRetryExposureDigest:      result.Recorder.ExactRetryExposureDigest,
			HealthBeforeMismatch:          result.Recorder.HealthBeforeMismatch,
			HealthAfterMismatch:           result.Recorder.HealthAfterMismatch,
			Mismatch:                      uciInstalledReceiptOutcome("mismatch", result.Recorder.Mismatch),
		},
		Watcher: UCIInstalledReceiptWatcher{
			AfterWriteA:  uciInstalledReceiptPublication(result.Watcher.AfterWriteA),
			AfterDeleteA: uciInstalledReceiptPublication(result.Watcher.AfterDeleteA),
			AfterWriteB:  uciInstalledReceiptPublication(result.Watcher.AfterWriteB),
			AfterDeleteB: uciInstalledReceiptPublication(result.Watcher.AfterDeleteB),
		},
		Restart: UCIInstalledReceiptRestart{
			ProcessesReplaced:         true,
			ProjectionCountsUnchanged: true,
			ProjectionCounts:          uciInstalledReceiptProjectionCounts(result.Restart.BeforeProjectionCounts),
			UnchangedInputReembedded:  result.Restart.UnchangedInputReembedded,
			Clients:                   restartClients,
			BeforeContexts: []UCIInstalledReceiptContext{
				uciInstalledReceiptContext(uciInstalledAcceptanceClientA, result.Restart.BeforeClientContexts[uciInstalledAcceptanceClientA]),
				uciInstalledReceiptContext(uciInstalledAcceptanceClientB, result.Restart.BeforeClientContexts[uciInstalledAcceptanceClientB]),
			},
			Contexts: []UCIInstalledReceiptContext{
				uciInstalledReceiptContext(uciInstalledAcceptanceClientA, result.Restart.ClientContexts[uciInstalledAcceptanceClientA]),
				uciInstalledReceiptContext(uciInstalledAcceptanceClientB, result.Restart.ClientContexts[uciInstalledAcceptanceClientB]),
				uciInstalledReceiptContext(uciInstalledAcceptanceClientC, result.Restart.ClientContexts[uciInstalledAcceptanceClientC]),
			},
			Retrieval: []UCIInstalledReceiptRetrieval{
				uciInstalledReceiptRetrieval(uciInstalledAcceptanceClientA, result.Restart.Observations[uciInstalledAcceptanceClientA]),
				uciInstalledReceiptRetrieval(uciInstalledAcceptanceClientB, result.Restart.Observations[uciInstalledAcceptanceClientB]),
			},
		},
		Cleanup: UCIInstalledReceiptCleanup{
			ProcessTreeClosed:     result.Cleanup.ProcessTreeClosed,
			InstallRootRemoved:    result.Cleanup.InstallRootRemoved,
			FixtureRootRemoved:    result.Cleanup.FixtureRootRemoved,
			LocalStateRootRemoved: result.Cleanup.LocalStateRootRemoved,
		},
	}
	if err := ValidateUCIInstalledReceipt(receipt); err != nil {
		return UCIInstalledReceipt{}, fmt.Errorf("validate assembled UCI installed receipt: %w", err)
	}
	return receipt, nil
}

// EncodeUCIInstalledReceipt validates and encodes the canonical struct/slice
// receipt. Because the schema contains no maps, identical receipts always
// produce byte-identical JSON.
func EncodeUCIInstalledReceipt(receipt UCIInstalledReceipt) ([]byte, error) {
	if err := ValidateUCIInstalledReceipt(receipt); err != nil {
		return nil, err
	}
	encoded, err := json.Marshal(receipt)
	if err != nil {
		return nil, fmt.Errorf("marshal UCI installed receipt: %w", err)
	}
	return encoded, nil
}

// ValidateUCIInstalledReceipt rejects incomplete, unbound, unsafe, or
// false-completion receipts. It validates the DTO without performing I/O.
func ValidateUCIInstalledReceipt(receipt UCIInstalledReceipt) error {
	if receipt.SchemaVersion != UCIInstalledReceiptSchemaVersion {
		return fmt.Errorf("UCI installed receipt schema is not accepted")
	}
	if receipt.Completion != uciInstalledReceiptCompletionVerified {
		return fmt.Errorf("UCI installed receipt does not have verified completion")
	}
	if receipt.Scope != (UCIInstalledReceiptScope{
		FixtureClass:    uciInstalledReceiptFixtureClass,
		RuntimeClass:    uciInstalledReceiptRuntimeClass,
		Production:      uciInstalledReceiptNotClaimed,
		Credentials:     uciInstalledReceiptNotRetained,
		SourceBodies:    uciInstalledReceiptNotRetained,
		PrivateLocators: uciInstalledReceiptNotRetained,
	}) {
		return fmt.Errorf("UCI installed receipt evidence scope is invalid")
	}
	if err := uciValidateInstalledReceiptCandidate(receipt.Candidate); err != nil {
		return err
	}
	if err := uciValidateInstalledReceiptFixture(receipt.Fixture); err != nil {
		return err
	}
	if err := uciValidateInstalledReceiptRuntime(receipt.Runtime); err != nil {
		return err
	}
	if err := uciValidateInstalledReceiptClients(receipt.Clients, "initial"); err != nil {
		return err
	}
	if err := uciValidateInstalledReceiptWorktrees(receipt.Worktrees, receipt.Fixture); err != nil {
		return err
	}
	if err := uciValidateInstalledReceiptBootstrap(receipt.Bootstrap); err != nil {
		return err
	}
	if err := uciValidateInstalledReceiptContexts(receipt.Contexts, uciInstalledReceiptABClients[:], "initial"); err != nil {
		return err
	}
	if err := uciValidateInstalledReceiptRetrieval(receipt.Retrieval, receipt.Fixture, "initial"); err != nil {
		return err
	}
	if err := uciValidateInstalledReceiptDefaults(receipt.Defaults, receipt.Contexts); err != nil {
		return err
	}
	if err := uciValidateInstalledReceiptRefusals(receipt.Refusals); err != nil {
		return err
	}
	if err := uciValidateInstalledReceiptRecorder(receipt.Recorder); err != nil {
		return err
	}
	if err := uciValidateInstalledReceiptWatcher(receipt.Watcher, receipt.Contexts); err != nil {
		return err
	}
	if err := uciValidateInstalledReceiptRestart(receipt.Restart, receipt.Contexts, receipt.Retrieval, receipt.Watcher); err != nil {
		return err
	}
	if !receipt.Cleanup.ProcessTreeClosed || !receipt.Cleanup.InstallRootRemoved || !receipt.Cleanup.FixtureRootRemoved || !receipt.Cleanup.LocalStateRootRemoved {
		return fmt.Errorf("UCI installed receipt cleanup is incomplete")
	}
	return nil
}

func validateUCIInstalledAcceptanceResultForReceipt(result uciInstalledAcceptanceResult) error {
	if result.AcceptanceVersion != uciInstalledAcceptanceVersionV1 || result.InstallHarnessVersion != uciInstallHarnessVersionV1 {
		return fmt.Errorf("installed acceptance version binding is incomplete")
	}
	if result.RawServiceCallCount != 0 || !result.SimultaneousAB {
		return fmt.Errorf("installed acceptance standard-client evidence is incomplete")
	}
	if err := uciInstalledReceiptRequireExactMapKeys(result.Artifacts, uciInstalledReceiptRoles[:], "artifacts"); err != nil {
		return err
	}
	for _, role := range uciInstalledReceiptRoles {
		artifact := result.Artifacts[role]
		if !uciInstalledReceiptValidSHA256(artifact.CandidateSHA256) || artifact.CandidateSHA256 != artifact.InstalledSHA256 {
			return fmt.Errorf("installed acceptance artifact evidence is incomplete")
		}
	}
	if !uciInstalledReceiptValidExternalProcesses(result.Processes) {
		return fmt.Errorf("installed acceptance external process evidence is incomplete")
	}
	if result.LoopbackHost != "127.0.0.1" && result.LoopbackHost != "localhost" && result.LoopbackHost != "::1" {
		return fmt.Errorf("installed acceptance loopback topology is unsafe")
	}
	if len(result.ReservedLoopbackPorts) < 2 || !uciInstalledReceiptValidPorts(result.ReservedLoopbackPorts) {
		return fmt.Errorf("installed acceptance loopback reservation is incomplete")
	}
	if !result.Parser.UsedInstalledArtifact || result.Parser.InvocationCount < 1 || !uciInstalledReceiptValidSHA256(result.Parser.RequestDigest) {
		return fmt.Errorf("installed acceptance installed-parser evidence is incomplete")
	}
	if err := uciValidateInstalledAcceptanceFixtureEvidence(result.Fixture); err != nil {
		return err
	}
	if err := uciInstalledReceiptRequireExactMapKeys(result.ClientTranscripts, uciInstalledReceiptClients[:], "initial client transcripts"); err != nil {
		return err
	}
	for _, client := range uciInstalledReceiptClients {
		if _, err := uciInstalledReceiptClientFact(client, result.ClientTranscripts[client]); err != nil {
			return err
		}
	}
	if err := uciValidateInstalledReceiptWorktreeResult(result.Worktrees, result.Fixture); err != nil {
		return err
	}
	if err := uciValidateInstalledReceiptBootstrapResult(result.Bootstrap); err != nil {
		return err
	}
	if err := uciInstalledReceiptRequireExactMapKeys(result.ClientContexts, uciInstalledReceiptABClients[:], "initial client contexts"); err != nil {
		return err
	}
	if err := uciValidateInstalledReceiptContextPair(result.ClientContexts[uciInstalledAcceptanceClientA], result.ClientContexts[uciInstalledAcceptanceClientB]); err != nil {
		return err
	}
	if err := uciInstalledReceiptRequireExactMapKeys(result.Observations, uciInstalledReceiptABClients[:], "initial observations"); err != nil {
		return err
	}
	if err := uciValidateInstalledReceiptObservationPair(result.Observations, result.Fixture); err != nil {
		return err
	}
	if err := uciInstalledReceiptRequireExactMapKeys(result.Defaults.BeforeThirdClientViewDigests, uciInstalledReceiptABClients[:], "before-third defaults"); err != nil {
		return err
	}
	if err := uciInstalledReceiptRequireExactMapKeys(result.Defaults.AfterThirdClientViewDigests, uciInstalledReceiptABClients[:], "after-third defaults"); err != nil {
		return err
	}
	for _, client := range uciInstalledReceiptABClients {
		context := result.ClientContexts[client]
		if result.Defaults.BeforeThirdClientViewDigests[client] != context.ViewDigest || result.Defaults.AfterThirdClientViewDigests[client] != context.ViewDigest {
			return fmt.Errorf("installed acceptance third-client default evidence is incomplete")
		}
	}
	if err := uciInstalledReceiptRequireExactMapKeys(result.Refusals, uciInstalledReceiptRefusalNames(), "refusal outcomes"); err != nil {
		return err
	}
	for _, requirement := range uciInstalledReceiptRefusals {
		if err := uciValidateInstalledReceiptClosedOutcome(result.Refusals[requirement.name], requirement.status, requirement.errorCode, "refusal"); err != nil {
			return err
		}
	}
	if err := uciValidateInstalledReceiptRecorderResult(result.Recorder); err != nil {
		return err
	}
	if err := uciValidateInstalledReceiptWatcherResult(result.Watcher, result.ClientContexts); err != nil {
		return err
	}
	if err := uciValidateInstalledReceiptRestartResult(result.Restart, result.ClientContexts, result.Observations, result.Watcher); err != nil {
		return err
	}
	if !result.Cleanup.ProcessTreeClosed || !result.Cleanup.InstallRootRemoved || !result.Cleanup.FixtureRootRemoved || !result.Cleanup.LocalStateRootRemoved {
		return fmt.Errorf("installed acceptance cleanup is incomplete")
	}
	return nil
}

func uciValidateInstalledReceiptCandidate(candidate UCIInstalledReceiptCandidate) error {
	if candidate.AcceptanceVersion != uciInstalledAcceptanceVersionV1 || candidate.HarnessVersion != uciInstallHarnessVersionV1 || len(candidate.Artifacts) != len(uciInstalledReceiptRoles) {
		return fmt.Errorf("UCI installed receipt candidate binding is incomplete")
	}
	for index, role := range uciInstalledReceiptRoles {
		artifact := candidate.Artifacts[index]
		if artifact.Role != role || !uciInstalledReceiptValidSHA256(artifact.CandidateSHA256) || artifact.CandidateSHA256 != artifact.InstalledSHA256 {
			return fmt.Errorf("UCI installed receipt artifact binding is incomplete")
		}
	}
	return nil
}

func uciValidateInstalledReceiptFixture(fixture UCIInstalledReceiptFixture) error {
	if fixture.Class != uciInstalledReceiptFixtureClass {
		return fmt.Errorf("UCI installed receipt fixture class is invalid")
	}
	for _, digest := range []string{
		fixture.ManifestDigest,
		fixture.RelativePathDigest,
		fixture.SharedSymbolDigest,
		fixture.PrimarySourceDigest,
		fixture.LinkedSourceDigest,
		fixture.PrimaryCalleeDigest,
		fixture.LinkedCalleeDigest,
	} {
		if !uciInstalledReceiptValidSHA256(digest) {
			return fmt.Errorf("UCI installed receipt fixture digest is missing or unsafe")
		}
	}
	if fixture.ManifestDigest != uciInstalledReceiptFixtureManifestDigest(fixture.Class, fixture.RelativePathDigest, fixture.SharedSymbolDigest, fixture.PrimarySourceDigest, fixture.LinkedSourceDigest, fixture.PrimaryCalleeDigest, fixture.LinkedCalleeDigest) {
		return fmt.Errorf("UCI installed receipt fixture manifest is not bound to its digests")
	}
	if fixture.PrimarySourceDigest == fixture.LinkedSourceDigest || fixture.PrimaryCalleeDigest == fixture.LinkedCalleeDigest {
		return fmt.Errorf("UCI installed receipt fixture does not distinguish dirty worktrees")
	}
	return nil
}

func uciValidateInstalledReceiptRuntime(runtime UCIInstalledReceiptRuntime) error {
	if !runtime.StandardMCPOnly || !runtime.ExternalChildren || runtime.Loopback != uciInstalledReceiptReservedLoopback || runtime.ReservedPortCount < 2 || !runtime.Parser.UsedInstalledArtifact || runtime.Parser.InvocationCount < 1 || !uciInstalledReceiptValidSHA256(runtime.Parser.RequestDigest) {
		return fmt.Errorf("UCI installed receipt runtime proof is incomplete")
	}
	return nil
}

func uciValidateInstalledReceiptClients(clients []UCIInstalledReceiptClient, phase string) error {
	if len(clients) != len(uciInstalledReceiptClients) {
		return fmt.Errorf("UCI installed receipt %s clients are incomplete", phase)
	}
	for index, name := range uciInstalledReceiptClients {
		client := clients[index]
		if client.Name != name || !client.UsedStdio || client.MethodCount < 4 || client.ToolCount < len(uciInstalledReceiptTools) || !uciInstalledReceiptValidSHA256(client.MethodSequenceDigest) || !uciInstalledReceiptValidSHA256(client.ToolSurfaceDigest) {
			return fmt.Errorf("UCI installed receipt %s client transcript is incomplete", phase)
		}
	}
	return nil
}

func uciValidateInstalledReceiptWorktrees(worktrees UCIInstalledReceiptWorktrees, fixture UCIInstalledReceiptFixture) error {
	if len(worktrees.RegisteredClients) != len(uciInstalledReceiptABClients) {
		return fmt.Errorf("UCI installed receipt worktree registrations are incomplete")
	}
	for index, client := range uciInstalledReceiptABClients {
		if worktrees.RegisteredClients[index] != client {
			return fmt.Errorf("UCI installed receipt worktree registration is unstable")
		}
	}
	if !worktrees.UsedGitArgumentVectors || !worktrees.LinkedGitFile || !worktrees.SameHead || !worktrees.PrimaryDirty || !worktrees.LinkedDirty || !uciInstalledReceiptValidSHA256(worktrees.SharedHeadDigest) || worktrees.RelativePathDigest != fixture.RelativePathDigest {
		return fmt.Errorf("UCI installed receipt worktree topology is incomplete")
	}
	return nil
}

func uciValidateInstalledReceiptBootstrap(bootstrap UCIInstalledReceiptBootstrap) error {
	if len(bootstrap.Clients) != len(uciInstalledReceiptABClients) {
		return fmt.Errorf("UCI installed receipt bootstrap clients are incomplete")
	}
	for index, name := range uciInstalledReceiptABClients {
		client := bootstrap.Clients[index]
		if client.Name != name || !client.InitialViewAbsent || !client.IndexStarted || client.StatusState != "observed_current" || client.BarrierState != "satisfied" {
			return fmt.Errorf("UCI installed receipt bootstrap evidence is incomplete")
		}
	}
	return uciValidateReceiptOutcome(bootstrap.UnboundClient, "unbound_client", "context_required", "CONTEXT_REQUIRED")
}

func uciValidateInstalledReceiptContexts(contexts []UCIInstalledReceiptContext, names []string, phase string) error {
	if len(contexts) != len(names) {
		return fmt.Errorf("UCI installed receipt %s contexts are incomplete", phase)
	}
	for index, name := range names {
		context := contexts[index]
		if context.Client != name || !uciInstalledReceiptValidSHA256(context.SourceDigest) || !uciInstalledReceiptValidSHA256(context.CheckoutDigest) || !uciInstalledReceiptValidSHA256(context.ViewDigest) {
			return fmt.Errorf("UCI installed receipt %s context is incomplete", phase)
		}
	}
	if len(names) == len(uciInstalledReceiptABClients) && names[0] == uciInstalledAcceptanceClientA && names[1] == uciInstalledAcceptanceClientB {
		if contexts[0].SourceDigest != contexts[1].SourceDigest || contexts[0].CheckoutDigest == contexts[1].CheckoutDigest || contexts[0].ViewDigest == contexts[1].ViewDigest {
			return fmt.Errorf("UCI installed receipt contexts do not isolate dirty views")
		}
	}
	return nil
}

func uciValidateInstalledReceiptRetrieval(retrieval []UCIInstalledReceiptRetrieval, fixture UCIInstalledReceiptFixture, phase string) error {
	if len(retrieval) != len(uciInstalledReceiptABClients) {
		return fmt.Errorf("UCI installed receipt %s retrieval evidence is incomplete", phase)
	}
	for index, client := range uciInstalledReceiptABClients {
		item := retrieval[index]
		if item.Client != client || !uciInstalledReceiptValidSHA256(item.SearchArtifactDigest) || !uciInstalledReceiptValidSHA256(item.GraphCalleeDigest) || !uciInstalledReceiptValidSHA256(item.ReadArtifactDigest) {
			return fmt.Errorf("UCI installed receipt %s retrieval digest is incomplete", phase)
		}
	}
	if retrieval[0].SearchArtifactDigest != fixture.PrimarySourceDigest || retrieval[0].GraphCalleeDigest != fixture.PrimaryCalleeDigest || retrieval[0].ReadArtifactDigest != fixture.PrimarySourceDigest || retrieval[1].SearchArtifactDigest != fixture.LinkedSourceDigest || retrieval[1].GraphCalleeDigest != fixture.LinkedCalleeDigest || retrieval[1].ReadArtifactDigest != fixture.LinkedSourceDigest {
		return fmt.Errorf("UCI installed receipt %s retrieval evidence is not fixture-bound", phase)
	}
	return nil
}

func uciValidateInstalledReceiptDefaults(defaults []UCIInstalledReceiptDefault, contexts []UCIInstalledReceiptContext) error {
	if len(defaults) != len(uciInstalledReceiptABClients) || len(contexts) != len(uciInstalledReceiptABClients) {
		return fmt.Errorf("UCI installed receipt default-isolation evidence is incomplete")
	}
	for index, client := range uciInstalledReceiptABClients {
		value := defaults[index]
		if value.Client != client || value.BeforeThirdClientViewDigest != contexts[index].ViewDigest || value.AfterThirdClientViewDigest != contexts[index].ViewDigest {
			return fmt.Errorf("UCI installed receipt third-client default changed")
		}
	}
	return nil
}

func uciValidateInstalledReceiptRefusals(refusals []UCIInstalledReceiptOutcome) error {
	if len(refusals) != len(uciInstalledReceiptRefusals) {
		return fmt.Errorf("UCI installed receipt refusal evidence is incomplete")
	}
	for index, requirement := range uciInstalledReceiptRefusals {
		if err := uciValidateReceiptOutcome(refusals[index], requirement.name, requirement.status, requirement.errorCode); err != nil {
			return err
		}
	}
	return nil
}

func uciValidateInstalledReceiptRecorder(recorder UCIInstalledReceiptRecorder) error {
	if err := uciValidateReceiptOutcome(recorder.InitialUnavailable, "initial_unavailable", "unavailable", "EXPOSURE_UNAVAILABLE"); err != nil {
		return err
	}
	if recorder.HealthAfterInitialUnavailable != "unavailable" || !uciInstalledReceiptValidSHA256(recorder.FirstExposureDigest) || recorder.FirstExposureDigest != recorder.ExactRetryExposureDigest || recorder.HealthBeforeMismatch != "healthy" || recorder.HealthAfterMismatch != "healthy" {
		return fmt.Errorf("UCI installed receipt recorder retry evidence is incomplete")
	}
	return uciValidateReceiptOutcome(recorder.Mismatch, "mismatch", "unavailable", "IDEMPOTENCY_MISMATCH")
}

func uciValidateInstalledReceiptWatcher(watcher UCIInstalledReceiptWatcher, contexts []UCIInstalledReceiptContext) error {
	if len(contexts) != len(uciInstalledReceiptABClients) {
		return fmt.Errorf("UCI installed receipt watcher contexts are incomplete")
	}
	for _, publication := range []UCIInstalledReceiptPublication{watcher.AfterWriteA, watcher.AfterDeleteA, watcher.AfterWriteB, watcher.AfterDeleteB} {
		if err := uciValidateInstalledReceiptPublication(publication); err != nil {
			return err
		}
	}
	primary, linked := contexts[0], contexts[1]
	if watcher.AfterWriteA.SourceDigest != primary.SourceDigest || watcher.AfterWriteA.CheckoutDigest != primary.CheckoutDigest || watcher.AfterWriteA.ViewDigest == primary.ViewDigest || watcher.AfterDeleteA.SourceDigest != primary.SourceDigest || watcher.AfterDeleteA.CheckoutDigest != primary.CheckoutDigest || watcher.AfterDeleteA.ViewDigest == watcher.AfterWriteA.ViewDigest || watcher.AfterWriteA.Generation < 2 || watcher.AfterDeleteA.Generation <= watcher.AfterWriteA.Generation || watcher.AfterWriteA.BarrierState != "satisfied" || watcher.AfterDeleteA.BarrierState != "satisfied" {
		return fmt.Errorf("UCI installed receipt watcher A write-delete evidence is incomplete")
	}
	if watcher.AfterWriteB.SourceDigest != linked.SourceDigest || watcher.AfterWriteB.CheckoutDigest != linked.CheckoutDigest || watcher.AfterWriteB.ViewDigest != linked.ViewDigest || watcher.AfterDeleteB != watcher.AfterWriteB || watcher.AfterWriteB.Generation < 1 || watcher.AfterWriteB.BarrierState != uciInstalledReceiptNotObserved {
		return fmt.Errorf("UCI installed receipt watcher B isolation evidence is incomplete")
	}
	return nil
}

func uciValidateInstalledReceiptRestart(restart UCIInstalledReceiptRestart, initialContexts []UCIInstalledReceiptContext, initialRetrieval []UCIInstalledReceiptRetrieval, watcher UCIInstalledReceiptWatcher) error {
	if !restart.ProcessesReplaced || !restart.ProjectionCountsUnchanged || restart.UnchangedInputReembedded || restart.ProjectionCounts.Embeddings < 0 || restart.ProjectionCounts.ChunkEmbeddings < 0 || restart.ProjectionCounts.ResolvedEdges < 0 {
		return fmt.Errorf("UCI installed receipt restart process or projection evidence is incomplete")
	}
	if err := uciValidateInstalledReceiptClients(restart.Clients, "restart"); err != nil {
		return err
	}
	if err := uciValidateInstalledReceiptContexts(restart.BeforeContexts, uciInstalledReceiptABClients[:], "restart baseline"); err != nil {
		return err
	}
	if err := uciValidateInstalledReceiptContexts(restart.Contexts, uciInstalledReceiptClients[:], "restart"); err != nil {
		return err
	}
	if len(initialContexts) != len(uciInstalledReceiptABClients) ||
		restart.BeforeContexts[0].SourceDigest != initialContexts[0].SourceDigest ||
		restart.BeforeContexts[0].CheckoutDigest != initialContexts[0].CheckoutDigest ||
		restart.BeforeContexts[0].ViewDigest != watcher.AfterDeleteA.ViewDigest ||
		restart.BeforeContexts[1].SourceDigest != initialContexts[1].SourceDigest ||
		restart.BeforeContexts[1].CheckoutDigest != initialContexts[1].CheckoutDigest ||
		restart.BeforeContexts[1].ViewDigest != watcher.AfterDeleteB.ViewDigest ||
		restart.Contexts[0] != restart.BeforeContexts[0] || restart.Contexts[1] != restart.BeforeContexts[1] ||
		restart.Contexts[2] != (UCIInstalledReceiptContext{Client: uciInstalledAcceptanceClientC, SourceDigest: restart.BeforeContexts[0].SourceDigest, CheckoutDigest: restart.BeforeContexts[0].CheckoutDigest, ViewDigest: restart.BeforeContexts[0].ViewDigest}) {
		return fmt.Errorf("UCI installed receipt restart contexts are not continuous")
	}
	if err := uciValidateInstalledReceiptRetrieval(restart.Retrieval, UCIInstalledReceiptFixture{
		PrimarySourceDigest: initialRetrieval[0].SearchArtifactDigest,
		PrimaryCalleeDigest: initialRetrieval[0].GraphCalleeDigest,
		LinkedSourceDigest:  initialRetrieval[1].SearchArtifactDigest,
		LinkedCalleeDigest:  initialRetrieval[1].GraphCalleeDigest,
	}, "restart"); err != nil {
		return err
	}
	if restart.Retrieval[0] != initialRetrieval[0] || restart.Retrieval[1] != initialRetrieval[1] {
		return fmt.Errorf("UCI installed receipt restart retrieval changed")
	}
	return nil
}

func uciValidateInstalledReceiptPublication(publication UCIInstalledReceiptPublication) error {
	if !uciInstalledReceiptValidSHA256(publication.SourceDigest) || !uciInstalledReceiptValidSHA256(publication.CheckoutDigest) || !uciInstalledReceiptValidSHA256(publication.ViewDigest) || !uciInstalledReceiptValidSHA256(publication.RunDigest) || publication.Generation < 1 || publication.FreshnessState != "observed_current" || (publication.BarrierState != "satisfied" && publication.BarrierState != uciInstalledReceiptNotObserved) {
		return fmt.Errorf("UCI installed receipt publication evidence is incomplete")
	}
	return nil
}

func uciValidateReceiptOutcome(outcome UCIInstalledReceiptOutcome, name, status, errorCode string) error {
	if outcome.Case != name || outcome.Status != status || outcome.ErrorCode != errorCode || !outcome.NoContextData {
		return fmt.Errorf("UCI installed receipt closed outcome is incomplete")
	}
	return nil
}

func uciInstalledReceiptArtifacts(artifacts map[string]uciInstalledAcceptanceArtifact) []UCIInstalledReceiptArtifactDigest {
	values := make([]UCIInstalledReceiptArtifactDigest, 0, len(uciInstalledReceiptRoles))
	for _, role := range uciInstalledReceiptRoles {
		artifact := artifacts[role]
		values = append(values, UCIInstalledReceiptArtifactDigest{
			Role:            role,
			CandidateSHA256: artifact.CandidateSHA256,
			InstalledSHA256: artifact.InstalledSHA256,
		})
	}
	return values
}

func uciInstalledReceiptFixtureFromEvidence(evidence uciInstalledAcceptanceFixtureEvidence) UCIInstalledReceiptFixture {
	return UCIInstalledReceiptFixture{
		Class:               evidence.Class,
		ManifestDigest:      evidence.ManifestDigest,
		RelativePathDigest:  evidence.RelativePathDigest,
		SharedSymbolDigest:  evidence.SharedSymbolDigest,
		PrimarySourceDigest: evidence.PrimarySourceDigest,
		LinkedSourceDigest:  evidence.LinkedSourceDigest,
		PrimaryCalleeDigest: evidence.PrimaryCalleeDigest,
		LinkedCalleeDigest:  evidence.LinkedCalleeDigest,
	}
}

func uciInstalledReceiptWorktreesFromResult(worktrees uciInstalledAcceptanceWorktrees) UCIInstalledReceiptWorktrees {
	return UCIInstalledReceiptWorktrees{
		RegisteredClients:      []string{uciInstalledAcceptanceClientA, uciInstalledAcceptanceClientB},
		UsedGitArgumentVectors: worktrees.UsedGitArgumentVectors,
		LinkedGitFile:          worktrees.LinkedGitFile,
		SameHead:               worktrees.SameHead,
		PrimaryDirty:           worktrees.PrimaryDirty,
		LinkedDirty:            worktrees.LinkedDirty,
		SharedHeadDigest:       worktrees.SharedHeadDigest,
		RelativePathDigest:     worktrees.RelativePathDigest,
	}
}

func uciInstalledReceiptBootstrapFromResult(bootstrap uciInstalledAcceptanceBootstrap) UCIInstalledReceiptBootstrap {
	return UCIInstalledReceiptBootstrap{
		Clients: []UCIInstalledReceiptBootstrapClient{
			{
				Name:              uciInstalledAcceptanceClientA,
				InitialViewAbsent: bootstrap.InitialViewAbsent[uciInstalledAcceptanceClientA],
				IndexStarted:      bootstrap.IndexStarted[uciInstalledAcceptanceClientA],
				StatusState:       bootstrap.StatusStates[uciInstalledAcceptanceClientA],
				BarrierState:      bootstrap.BarrierStates[uciInstalledAcceptanceClientA],
			},
			{
				Name:              uciInstalledAcceptanceClientB,
				InitialViewAbsent: bootstrap.InitialViewAbsent[uciInstalledAcceptanceClientB],
				IndexStarted:      bootstrap.IndexStarted[uciInstalledAcceptanceClientB],
				StatusState:       bootstrap.StatusStates[uciInstalledAcceptanceClientB],
				BarrierState:      bootstrap.BarrierStates[uciInstalledAcceptanceClientB],
			},
		},
		UnboundClient: uciInstalledReceiptOutcome("unbound_client", bootstrap.UnboundClient),
	}
}

func uciInstalledReceiptContext(client string, context uciInstalledAcceptanceContext) UCIInstalledReceiptContext {
	return UCIInstalledReceiptContext{
		Client:         client,
		SourceDigest:   context.SourceDigest,
		CheckoutDigest: context.CheckoutDigest,
		ViewDigest:     context.ViewDigest,
	}
}

func uciInstalledReceiptRetrieval(client string, observations uciInstalledAcceptanceObservations) UCIInstalledReceiptRetrieval {
	return UCIInstalledReceiptRetrieval{
		Client:               client,
		SearchArtifactDigest: observations.SearchArtifactDigests[0],
		GraphCalleeDigest:    observations.GraphCalleeDigests[0],
		ReadArtifactDigest:   observations.ReadArtifactDigests[0],
	}
}

func uciInstalledReceiptOutcome(name string, outcome uciInstalledAcceptanceClosedOutcome) UCIInstalledReceiptOutcome {
	return UCIInstalledReceiptOutcome{
		Case:          name,
		Status:        outcome.Status,
		ErrorCode:     outcome.ErrorCode,
		NoContextData: true,
	}
}

func uciInstalledReceiptRefusalsFromResult(refusals map[string]uciInstalledAcceptanceClosedOutcome) []UCIInstalledReceiptOutcome {
	values := make([]UCIInstalledReceiptOutcome, 0, len(uciInstalledReceiptRefusals))
	for _, requirement := range uciInstalledReceiptRefusals {
		values = append(values, uciInstalledReceiptOutcome(requirement.name, refusals[requirement.name]))
	}
	return values
}

func uciInstalledReceiptPublication(publication uciInstalledAcceptancePublicationEvidence) UCIInstalledReceiptPublication {
	barrierState := publication.BarrierState
	if barrierState == "" {
		barrierState = uciInstalledReceiptNotObserved
	}
	return UCIInstalledReceiptPublication{
		SourceDigest:   publication.SourceDigest,
		CheckoutDigest: publication.CheckoutDigest,
		ViewDigest:     publication.ViewDigest,
		RunDigest:      publication.RunDigest,
		Generation:     publication.Generation,
		FreshnessState: publication.FreshnessState,
		BarrierState:   barrierState,
	}
}

func uciInstalledReceiptProjectionCounts(counts uciInstalledAcceptanceProjectionCounts) UCIInstalledReceiptProjectionCounts {
	return UCIInstalledReceiptProjectionCounts{
		Embeddings:      counts.Embeddings,
		ChunkEmbeddings: counts.ChunkEmbeddings,
		ResolvedEdges:   counts.ResolvedEdges,
	}
}

func uciInstalledReceiptClientsFor(transcripts map[string]uciInstalledAcceptanceClientTranscript, phase string) ([]UCIInstalledReceiptClient, error) {
	clients := make([]UCIInstalledReceiptClient, 0, len(uciInstalledReceiptClients))
	for _, name := range uciInstalledReceiptClients {
		client, err := uciInstalledReceiptClientFact(name, transcripts[name])
		if err != nil {
			return nil, fmt.Errorf("UCI installed receipt %s client %s: %w", phase, name, err)
		}
		clients = append(clients, client)
	}
	return clients, nil
}

func uciInstalledReceiptClientFact(name string, transcript uciInstalledAcceptanceClientTranscript) (UCIInstalledReceiptClient, error) {
	if !transcript.UsedStdio || !uciInstalledReceiptMethodSubsequence(transcript.Methods) {
		return UCIInstalledReceiptClient{}, fmt.Errorf("standard stdio transcript is incomplete")
	}
	for _, method := range transcript.Methods {
		if method != "initialize" && method != "notifications/initialized" && method != "tools/list" && method != "tools/call" {
			return UCIInstalledReceiptClient{}, fmt.Errorf("standard MCP transcript contains an unsafe method")
		}
	}
	if err := uciInstalledReceiptRequireTools(transcript.Tools); err != nil {
		return UCIInstalledReceiptClient{}, err
	}
	toolDigest, err := uciInstalledReceiptCanonicalToolDigest(transcript.Tools)
	if err != nil {
		return UCIInstalledReceiptClient{}, err
	}
	return UCIInstalledReceiptClient{
		Name:                 name,
		UsedStdio:            true,
		MethodCount:          len(transcript.Methods),
		MethodSequenceDigest: uciInstalledReceiptDigestStrings(append([]string{"uci-installed-method-sequence/v1"}, transcript.Methods...)...),
		ToolCount:            len(transcript.Tools),
		ToolSurfaceDigest:    toolDigest,
	}, nil
}

func uciInstalledReceiptMethodSubsequence(methods []string) bool {
	needed := [...]string{"initialize", "notifications/initialized", "tools/list", "tools/call"}
	next := 0
	for _, method := range methods {
		if next < len(needed) && method == needed[next] {
			next++
		}
	}
	return next == len(needed)
}

func uciInstalledReceiptRequireTools(tools []string) error {
	seen := make(map[string]struct{}, len(tools))
	for _, tool := range tools {
		if !uciInstalledReceiptSafeToolName(tool) {
			return fmt.Errorf("installed MCP tool surface contains an unsafe value")
		}
		if _, duplicate := seen[tool]; duplicate {
			return fmt.Errorf("installed MCP tool surface contains an unstable duplicate")
		}
		seen[tool] = struct{}{}
	}
	for _, required := range uciInstalledReceiptTools {
		if _, found := seen[required]; !found {
			return fmt.Errorf("installed MCP tool surface is incomplete")
		}
	}
	return nil
}

func uciInstalledReceiptCanonicalToolDigest(tools []string) (string, error) {
	canonical := append([]string(nil), tools...)
	sort.Strings(canonical)
	for index := 1; index < len(canonical); index++ {
		if canonical[index-1] == canonical[index] {
			return "", fmt.Errorf("installed MCP tool surface contains an unstable duplicate")
		}
	}
	return uciInstalledReceiptDigestStrings(append([]string{"uci-installed-tool-surface/v1"}, canonical...)...), nil
}

func uciInstalledReceiptSafeToolName(value string) bool {
	if len(value) == 0 || len(value) > 128 {
		return false
	}
	lower := strings.ToLower(value)
	for _, forbidden := range []string{"token", "secret", "password", "credential", "bearer", "postgres", "dsn", "api_key", "apikey", "authorization"} {
		if strings.Contains(lower, forbidden) {
			return false
		}
	}
	for _, runeValue := range value {
		if (runeValue >= 'a' && runeValue <= 'z') || (runeValue >= 'A' && runeValue <= 'Z') || (runeValue >= '0' && runeValue <= '9') || runeValue == '_' || runeValue == '-' || runeValue == '.' || runeValue == '/' {
			continue
		}
		return false
	}
	return true
}

func uciInstalledReceiptDigestStrings(values ...string) string {
	hash := sha256.New()
	for _, value := range values {
		_, _ = fmt.Fprintf(hash, "%d:", len(value))
		_, _ = hash.Write([]byte(value))
		_, _ = hash.Write([]byte{0})
	}
	return hex.EncodeToString(hash.Sum(nil))
}

func uciInstalledReceiptValidSHA256(value string) bool {
	if len(value) != sha256.Size*2 || value != strings.ToLower(value) {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func uciInstalledReceiptRequireExactMapKeys[T any](values map[string]T, names []string, label string) error {
	if len(values) != len(names) {
		return fmt.Errorf("UCI installed receipt %s has an unstable key set", label)
	}
	for _, name := range names {
		if _, found := values[name]; !found {
			return fmt.Errorf("UCI installed receipt %s is incomplete", label)
		}
	}
	return nil
}

func uciInstalledReceiptRefusalNames() []string {
	names := make([]string, 0, len(uciInstalledReceiptRefusals))
	for _, requirement := range uciInstalledReceiptRefusals {
		names = append(names, requirement.name)
	}
	return names
}

func uciInstalledReceiptValidExternalProcesses(processes uciInstalledAcceptanceProcesses) bool {
	values := [...]int{processes.ServerPID, processes.DaemonPID, processes.ParserPID}
	for index, value := range values {
		if value <= 0 {
			return false
		}
		for _, earlier := range values[:index] {
			if value == earlier {
				return false
			}
		}
	}
	return true
}

func uciInstalledReceiptValidPorts(ports []int) bool {
	for index, port := range ports {
		if port < 1 || port > 65535 {
			return false
		}
		for _, earlier := range ports[:index] {
			if port == earlier {
				return false
			}
		}
	}
	return true
}

func uciValidateInstalledAcceptanceFixtureEvidence(evidence uciInstalledAcceptanceFixtureEvidence) error {
	if evidence.Class != uciInstalledReceiptFixtureClass {
		return fmt.Errorf("installed acceptance fixture is not synthetic and redacted")
	}
	for _, digest := range []string{
		evidence.ManifestDigest,
		evidence.RelativePathDigest,
		evidence.SharedSymbolDigest,
		evidence.PrimarySourceDigest,
		evidence.LinkedSourceDigest,
		evidence.PrimaryCalleeDigest,
		evidence.LinkedCalleeDigest,
	} {
		if !uciInstalledReceiptValidSHA256(digest) {
			return fmt.Errorf("installed acceptance fixture evidence lacks a digest")
		}
	}
	if evidence.ManifestDigest != uciInstalledReceiptFixtureManifestDigest(evidence.Class, evidence.RelativePathDigest, evidence.SharedSymbolDigest, evidence.PrimarySourceDigest, evidence.LinkedSourceDigest, evidence.PrimaryCalleeDigest, evidence.LinkedCalleeDigest) {
		return fmt.Errorf("installed acceptance fixture manifest is not bound to its digests")
	}
	if evidence.PrimarySourceDigest == evidence.LinkedSourceDigest || evidence.PrimaryCalleeDigest == evidence.LinkedCalleeDigest {
		return fmt.Errorf("installed acceptance fixture evidence does not distinguish dirty inputs")
	}
	return nil
}

func uciValidateInstalledReceiptWorktreeResult(worktrees uciInstalledAcceptanceWorktrees, fixture uciInstalledAcceptanceFixtureEvidence) error {
	if err := uciInstalledReceiptRequireExactMapKeys(worktrees.Registered, uciInstalledReceiptABClients[:], "worktree registrations"); err != nil {
		return err
	}
	for _, client := range uciInstalledReceiptABClients {
		if !worktrees.Registered[client] {
			return fmt.Errorf("installed acceptance worktree registration is incomplete")
		}
	}
	if !worktrees.UsedGitArgumentVectors || !worktrees.LinkedGitFile || !worktrees.SameHead || !worktrees.PrimaryDirty || !worktrees.LinkedDirty || !uciInstalledReceiptValidSHA256(worktrees.SharedHeadDigest) || worktrees.RelativePathDigest != fixture.RelativePathDigest {
		return fmt.Errorf("installed acceptance worktree topology is incomplete")
	}
	return nil
}

func uciValidateInstalledReceiptBootstrapResult(bootstrap uciInstalledAcceptanceBootstrap) error {
	for _, values := range []struct {
		name     string
		mapValue map[string]bool
	}{
		{name: "initial View absence", mapValue: bootstrap.InitialViewAbsent},
		{name: "index starts", mapValue: bootstrap.IndexStarted},
	} {
		if err := uciInstalledReceiptRequireExactMapKeys(values.mapValue, uciInstalledReceiptABClients[:], values.name); err != nil {
			return err
		}
		for _, client := range uciInstalledReceiptABClients {
			if !values.mapValue[client] {
				return fmt.Errorf("installed acceptance bootstrap %s is incomplete", values.name)
			}
		}
	}
	if err := uciInstalledReceiptRequireExactMapKeys(bootstrap.StatusStates, uciInstalledReceiptABClients[:], "bootstrap status states"); err != nil {
		return err
	}
	if err := uciInstalledReceiptRequireExactMapKeys(bootstrap.BarrierStates, uciInstalledReceiptABClients[:], "bootstrap barrier states"); err != nil {
		return err
	}
	for _, client := range uciInstalledReceiptABClients {
		if bootstrap.StatusStates[client] != "observed_current" || bootstrap.BarrierStates[client] != "satisfied" {
			return fmt.Errorf("installed acceptance bootstrap state is incomplete")
		}
	}
	return uciValidateInstalledReceiptClosedOutcome(bootstrap.UnboundClient, "context_required", "CONTEXT_REQUIRED", "unbound client")
}

func uciValidateInstalledReceiptContextPair(primary, linked uciInstalledAcceptanceContext) error {
	for _, context := range []uciInstalledAcceptanceContext{primary, linked} {
		if !uciInstalledReceiptValidSHA256(context.SourceDigest) || !uciInstalledReceiptValidSHA256(context.CheckoutDigest) || !uciInstalledReceiptValidSHA256(context.ViewDigest) {
			return fmt.Errorf("installed acceptance context digest is missing")
		}
	}
	if primary.SourceDigest != linked.SourceDigest || primary.CheckoutDigest == linked.CheckoutDigest || primary.ViewDigest == linked.ViewDigest {
		return fmt.Errorf("installed acceptance contexts are not isolated")
	}
	return nil
}

func uciValidateInstalledReceiptObservationPair(observations map[string]uciInstalledAcceptanceObservations, fixture uciInstalledAcceptanceFixtureEvidence) error {
	for _, item := range []struct {
		client string
		source string
		callee string
	}{
		{client: uciInstalledAcceptanceClientA, source: fixture.PrimarySourceDigest, callee: fixture.PrimaryCalleeDigest},
		{client: uciInstalledAcceptanceClientB, source: fixture.LinkedSourceDigest, callee: fixture.LinkedCalleeDigest},
	} {
		observation := observations[item.client]
		if len(observation.SearchArtifactDigests) != 1 || len(observation.GraphCalleeDigests) != 1 || len(observation.ReadArtifactDigests) != 1 || observation.SearchArtifactDigests[0] != item.source || observation.GraphCalleeDigests[0] != item.callee || observation.ReadArtifactDigests[0] != item.source {
			return fmt.Errorf("installed acceptance search graph read evidence is incomplete")
		}
		for _, digest := range []string{observation.SearchArtifactDigests[0], observation.GraphCalleeDigests[0], observation.ReadArtifactDigests[0]} {
			if !uciInstalledReceiptValidSHA256(digest) {
				return fmt.Errorf("installed acceptance observation digest is missing")
			}
		}
	}
	return nil
}

func uciValidateInstalledReceiptClosedOutcome(outcome uciInstalledAcceptanceClosedOutcome, status, errorCode, label string) error {
	if outcome.Status != status || outcome.ErrorCode != errorCode || outcome.ContextDigest != "" || len(outcome.ContentDigests) != 0 || len(outcome.GraphDigests) != 0 || outcome.ExposureDigest != "" {
		return fmt.Errorf("installed acceptance %s outcome is not closed", label)
	}
	return nil
}

func uciValidateInstalledReceiptRecorderResult(recorder uciInstalledAcceptanceRecorder) error {
	if err := uciValidateInstalledReceiptClosedOutcome(recorder.InitialUnavailable, "unavailable", "EXPOSURE_UNAVAILABLE", "recorder unavailable"); err != nil {
		return err
	}
	if recorder.HealthAfterInitialUnavailable != "unavailable" || !uciInstalledReceiptValidSHA256(recorder.FirstExposureDigest) || recorder.FirstExposureDigest != recorder.ExactRetryExposureDigest || recorder.HealthBeforeMismatch != "healthy" || recorder.HealthAfterMismatch != "healthy" {
		return fmt.Errorf("installed acceptance recorder retry evidence is incomplete")
	}
	return uciValidateInstalledReceiptClosedOutcome(recorder.Mismatch, "unavailable", "IDEMPOTENCY_MISMATCH", "recorder mismatch")
}

func uciValidateInstalledReceiptWatcherResult(watcher uciInstalledAcceptanceWatcher, contexts map[string]uciInstalledAcceptanceContext) error {
	for _, publication := range []uciInstalledAcceptancePublicationEvidence{watcher.AfterWriteA, watcher.AfterDeleteA, watcher.AfterWriteB, watcher.AfterDeleteB} {
		if err := uciValidateInstalledReceiptPublication(uciInstalledReceiptPublication(publication)); err != nil {
			return err
		}
	}
	primary, linked := contexts[uciInstalledAcceptanceClientA], contexts[uciInstalledAcceptanceClientB]
	if watcher.AfterWriteA.SourceDigest != primary.SourceDigest || watcher.AfterWriteA.CheckoutDigest != primary.CheckoutDigest || watcher.AfterWriteA.ViewDigest == primary.ViewDigest || watcher.AfterDeleteA.SourceDigest != primary.SourceDigest || watcher.AfterDeleteA.CheckoutDigest != primary.CheckoutDigest || watcher.AfterDeleteA.ViewDigest == watcher.AfterWriteA.ViewDigest || watcher.AfterWriteA.Generation < 2 || watcher.AfterDeleteA.Generation <= watcher.AfterWriteA.Generation {
		return fmt.Errorf("installed acceptance watcher A evidence is incomplete")
	}
	if watcher.AfterWriteB.SourceDigest != linked.SourceDigest || watcher.AfterWriteB.CheckoutDigest != linked.CheckoutDigest || watcher.AfterWriteB.ViewDigest != linked.ViewDigest || watcher.AfterDeleteB != watcher.AfterWriteB || watcher.AfterWriteB.Generation < 1 {
		return fmt.Errorf("installed acceptance watcher B isolation evidence is incomplete")
	}
	return nil
}

func uciValidateInstalledReceiptRestartResult(restart uciInstalledAcceptanceRestart, contexts map[string]uciInstalledAcceptanceContext, observations map[string]uciInstalledAcceptanceObservations, watcher uciInstalledAcceptanceWatcher) error {
	if !uciInstalledReceiptValidExternalProcesses(restart.BeforeProcesses) || !uciInstalledReceiptValidExternalProcesses(restart.AfterProcesses) || restart.BeforeProcesses.ServerPID == restart.AfterProcesses.ServerPID || restart.BeforeProcesses.DaemonPID == restart.AfterProcesses.DaemonPID || restart.BeforeProcesses.ParserPID == restart.AfterProcesses.ParserPID || restart.UnchangedInputReembedded || restart.BeforeProjectionCounts != restart.AfterProjectionCounts || restart.BeforeProjectionCounts.Embeddings < 0 || restart.BeforeProjectionCounts.ChunkEmbeddings < 0 || restart.BeforeProjectionCounts.ResolvedEdges < 0 {
		return fmt.Errorf("installed acceptance restart process or projection evidence is incomplete")
	}
	if err := uciInstalledReceiptRequireExactMapKeys(restart.ClientTranscripts, uciInstalledReceiptClients[:], "restart client transcripts"); err != nil {
		return err
	}
	for _, client := range uciInstalledReceiptClients {
		if _, err := uciInstalledReceiptClientFact(client, restart.ClientTranscripts[client]); err != nil {
			return err
		}
	}
	if err := uciInstalledReceiptRequireExactMapKeys(restart.BeforeClientContexts, uciInstalledReceiptABClients[:], "restart baseline contexts"); err != nil {
		return err
	}
	if err := uciInstalledReceiptRequireExactMapKeys(restart.ClientContexts, uciInstalledReceiptClients[:], "restart contexts"); err != nil {
		return err
	}
	expectedBefore := map[string]uciInstalledAcceptanceContext{
		uciInstalledAcceptanceClientA: {SourceDigest: watcher.AfterDeleteA.SourceDigest, CheckoutDigest: watcher.AfterDeleteA.CheckoutDigest, ViewDigest: watcher.AfterDeleteA.ViewDigest},
		uciInstalledAcceptanceClientB: {SourceDigest: watcher.AfterDeleteB.SourceDigest, CheckoutDigest: watcher.AfterDeleteB.CheckoutDigest, ViewDigest: watcher.AfterDeleteB.ViewDigest},
	}
	for _, client := range uciInstalledReceiptABClients {
		if restart.BeforeClientContexts[client] != expectedBefore[client] || restart.ClientContexts[client] != restart.BeforeClientContexts[client] {
			return fmt.Errorf("installed acceptance restart context changed")
		}
	}
	if restart.ClientContexts[uciInstalledAcceptanceClientC] != restart.BeforeClientContexts[uciInstalledAcceptanceClientA] {
		return fmt.Errorf("installed acceptance restart third-client context changed")
	}
	if err := uciInstalledReceiptRequireExactMapKeys(restart.BeforeObservations, uciInstalledReceiptABClients[:], "restart baseline observations"); err != nil {
		return err
	}
	if err := uciInstalledReceiptRequireExactMapKeys(restart.Observations, uciInstalledReceiptABClients[:], "restart observations"); err != nil {
		return err
	}
	for _, client := range uciInstalledReceiptABClients {
		if !uciInstalledAcceptanceSameObservations(restart.BeforeObservations[client], observations[client]) || !uciInstalledAcceptanceSameObservations(restart.Observations[client], observations[client]) {
			return fmt.Errorf("installed acceptance restart retrieval changed")
		}
	}
	return nil
}

// uciInstalledAcceptanceFixtureEvidence stores only digest-bound synthetic
// fixture provenance in the result, so later receipt assembly never needs the
// request's roots, DSN, source bodies, or other raw fixture values.
type uciInstalledAcceptanceFixtureEvidence struct {
	Class               string
	ManifestDigest      string
	RelativePathDigest  string
	SharedSymbolDigest  string
	PrimarySourceDigest string
	LinkedSourceDigest  string
	PrimaryCalleeDigest string
	LinkedCalleeDigest  string
}

func uciInstalledAcceptanceFixtureEvidenceFor(fixture uciInstalledAcceptanceFixture) uciInstalledAcceptanceFixtureEvidence {
	evidence := uciInstalledAcceptanceFixtureEvidence{
		Class:               uciInstalledReceiptFixtureClass,
		RelativePathDigest:  uciInstalledAcceptanceStringDigest(fixture.RelativePath),
		SharedSymbolDigest:  uciInstalledAcceptanceStringDigest(fixture.SharedSymbol),
		PrimarySourceDigest: uciInstalledAcceptanceStringDigest(fixture.PrimarySource),
		LinkedSourceDigest:  uciInstalledAcceptanceStringDigest(fixture.LinkedSource),
		PrimaryCalleeDigest: uciInstalledAcceptanceStringDigest(fixture.PrimaryCallee),
		LinkedCalleeDigest:  uciInstalledAcceptanceStringDigest(fixture.LinkedCallee),
	}
	evidence.ManifestDigest = uciInstalledReceiptFixtureManifestDigest(
		evidence.Class,
		evidence.RelativePathDigest,
		evidence.SharedSymbolDigest,
		evidence.PrimarySourceDigest,
		evidence.LinkedSourceDigest,
		evidence.PrimaryCalleeDigest,
		evidence.LinkedCalleeDigest,
	)
	return evidence
}

func uciInstalledReceiptFixtureManifestDigest(class, relativePathDigest, sharedSymbolDigest, primarySourceDigest, linkedSourceDigest, primaryCalleeDigest, linkedCalleeDigest string) string {
	return uciInstalledReceiptDigestStrings(
		"uci-installed-fixture-manifest/v1",
		class,
		relativePathDigest,
		sharedSymbolDigest,
		primarySourceDigest,
		linkedSourceDigest,
		primaryCalleeDigest,
		linkedCalleeDigest,
	)
}
