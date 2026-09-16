package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/google/uuid"
	gormdb "github.com/thebtf/engram/internal/db/gorm"
)

type uci1RecoveryPrimaryRestoreInput struct {
	selection       uciInstalledAcceptanceSelection
	root            string
	branch          string
	head            string
	sourcePath      string
	source          []byte
	temporaryBranch string
}

func (input uci1RecoveryPrimaryRestoreInput) withTemporaryBranch(branch string) uci1RecoveryPrimaryRestoreInput {
	input.temporaryBranch = branch
	return input
}

// uci1ProbeRecoveryGit exercises only the installed stdio clients against real
// disposable Git worktrees. It leaves the primary checkout on its original
// branch and bytes, and reconciles those restored bytes through the public MCP
// before returning.
func uci1ProbeRecoveryGit(ctx context.Context, runtime uciInstalledAcceptanceScenarioRuntime) (_ map[string]uciInstalledAcceptanceScenarioEvidence, retErr error) {
	if ctx == nil {
		return nil, errors.New("UCI-1 recovery Git probe requires a context")
	}
	if runtime.Authority == nil || runtime.Authority.store == nil || runtime.Authority.source == nil || runtime.ClientA == nil {
		return nil, errors.New("UCI-1 recovery Git probe runtime is incomplete")
	}
	selection, found := runtime.Selections[uciInstalledAcceptanceClientA]
	baseline, published := runtime.Publications[uciInstalledAcceptanceClientA]
	if !found || !published || selection.contextHandle == "" || baseline.viewID == "" || runtime.Worktrees.primaryRoot == "" {
		return nil, errors.New("UCI-1 recovery Git probe primary baseline is incomplete")
	}

	root := runtime.Worktrees.primaryRoot
	branch, err := uci1RecoveryGitBranch(ctx, root)
	if err != nil {
		return nil, err
	}
	if branch == "" {
		return nil, errors.New("UCI-1 recovery Git probe requires a branch-attached primary fixture")
	}
	head, err := uciReadInstalledAcceptanceGit(ctx, root, "rev-parse", "HEAD")
	if err != nil {
		return nil, err
	}
	sourcePath := filepath.Join(root, filepath.FromSlash(runtime.Request.Fixture.RelativePath))
	originalSource, err := os.ReadFile(sourcePath)
	if err != nil {
		return nil, fmt.Errorf("read UCI-1 primary fixture source: %w", err)
	}
	restore := uci1RecoveryPrimaryRestoreInput{
		selection:  selection,
		root:       root,
		branch:     branch,
		head:       head,
		sourcePath: sourcePath,
		source:     originalSource,
	}

	defer func() {
		_, restoreErr := uci1RecoveryRestorePrimary(ctx, runtime, restore)
		if restoreErr != nil {
			retErr = errors.Join(retErr, fmt.Errorf("restore UCI-1 recovery Git primary fixture: %w", restoreErr))
		}
	}()

	evidence := make(map[string]uciInstalledAcceptanceScenarioEvidence, 3)
	nonce := strings.ReplaceAll(uuid.NewString(), "-", "")

	offlinePublication, err := uci1RecoveryObserveOfflineOverflow(ctx, runtime, selection, root, sourcePath, nonce)
	if err != nil {
		return nil, err
	}
	evidence["U07"] = uci1RecoveryEvidence("U07", offlinePublication, nonce)
	if _, err := uci1RecoveryRestorePrimary(ctx, runtime, restore); err != nil {
		return nil, err
	}

	branchPublication, branchName, err := uci1RecoveryObserveBranch(ctx, runtime, selection, root, sourcePath, nonce)
	if err != nil {
		return nil, err
	}
	evidence["U09"] = uci1RecoveryEvidence("U09", branchPublication, branchName)
	if _, err := uci1RecoveryRestorePrimary(ctx, runtime, restore.withTemporaryBranch(branchName)); err != nil {
		return nil, err
	}

	detachedPublication, unbornPublication, err := uci1RecoveryObserveDetachedAndUnborn(ctx, runtime, selection, root, sourcePath, head, nonce)
	if err != nil {
		return nil, err
	}
	evidence["U10"] = uci1RecoveryEvidence("U10", detachedPublication, unbornPublication.viewID, nonce)
	if _, err := uci1RecoveryRestorePrimary(ctx, runtime, restore); err != nil {
		return nil, err
	}

	// U11 and U12 intentionally remain absent. The installed public MCP only
	// selects already-registered checkouts: it has no relocation, retirement, or
	// replacement operation. In particular, the installed runtime rejects a
	// root that differs from the server-bound LocalRootID before indexing. A
	// direct update of ci_checkouts would substitute the behavior under test,
	// rather than observe it through installed MCP.
	return evidence, nil
}

func uci1RecoveryObserveOfflineOverflow(ctx context.Context, runtime uciInstalledAcceptanceScenarioRuntime, selection uciInstalledAcceptanceSelection, root, sourcePath, nonce string) (_ uciInstalledAcceptancePublication, retErr error) {
	checkout := runtime.Authority.checkouts[uciInstalledAcceptanceClientA]
	if checkout == nil || checkout.CheckoutID == "" {
		return uciInstalledAcceptancePublication{}, errors.New("UCI-1 recovery Git primary checkout is unavailable")
	}

	fault, err := uci1RecoveryInstallOverflowFault(ctx, runtime, checkout.CheckoutID)
	if err != nil {
		return uciInstalledAcceptancePublication{}, err
	}
	faultCleared := false
	defer func() {
		if !faultCleared {
			retErr = errors.Join(retErr, fault.clear(context.Background()))
		}
	}()

	originalState, err := uci1RecoveryCheckoutState(ctx, runtime, checkout.CheckoutID)
	if err != nil {
		return uciInstalledAcceptancePublication{}, err
	}
	if originalState == "" {
		return uciInstalledAcceptancePublication{}, errors.New("UCI-1 recovery Git primary checkout state is empty")
	}
	restoredState := false
	defer func() {
		if !restoredState {
			retErr = errors.Join(retErr, uci1RecoverySetCheckoutState(context.Background(), runtime, checkout.CheckoutID, originalState))
		}
	}()

	if err := uci1RecoverySetCheckoutState(ctx, runtime, checkout.CheckoutID, "offline"); err != nil {
		return uciInstalledAcceptancePublication{}, err
	}

	staleName := "UCIRecoveryStale" + nonce
	currentName := "UCIRecoveryCurrent" + nonce
	for index := 0; index < 8; index++ {
		if err := os.WriteFile(sourcePath, []byte(uci1RecoverySource(runtime.Request.Fixture.SharedSymbol, staleName, fmt.Sprintf("offline-%d-%s", index, nonce))), 0o600); err != nil {
			return uciInstalledAcceptancePublication{}, fmt.Errorf("write UCI-1 offline obsolete fixture: %w", err)
		}
	}
	if err := os.WriteFile(sourcePath, []byte(uci1RecoverySource(runtime.Request.Fixture.SharedSymbol, currentName, "current-"+nonce)), 0o600); err != nil {
		return uciInstalledAcceptancePublication{}, fmt.Errorf("write UCI-1 offline current fixture: %w", err)
	}

	statusPayload, statusErr := uciInstalledAcceptanceStatusTool(ctx, runtime.ClientA, map[string]any{"context_handle": selection.contextHandle})
	if statusErr == nil {
		status, decodeErr := uciDecodeInstalledAcceptanceStatus(statusPayload)
		if decodeErr != nil {
			return uciInstalledAcceptancePublication{}, decodeErr
		}
		if status.freshness == nil || status.freshness.state != "offline" {
			return uciInstalledAcceptancePublication{}, errors.New("installed MCP did not expose the injected offline checkout as offline")
		}
	}

	if err := uci1RecoverySetCheckoutState(ctx, runtime, checkout.CheckoutID, originalState); err != nil {
		return uciInstalledAcceptancePublication{}, err
	}
	restoredState = true

	publication, err := uci1RecoveryRunIndex(ctx, runtime.ClientA, selection, root)
	if err != nil {
		return uciInstalledAcceptancePublication{}, err
	}
	if err := uciRequireInstalledAcceptanceWatcherCanary(ctx, runtime.ClientA, selection, publication, currentName, runtime.Request.Fixture.RelativePath, true); err != nil {
		return uciInstalledAcceptancePublication{}, fmt.Errorf("observe UCI-1 current bytes after offline recovery: %w", err)
	}
	if err := uci1RecoveryRequireSymbolAbsent(ctx, runtime.ClientA, selection, publication, staleName, runtime.Request.Fixture.RelativePath); err != nil {
		return uciInstalledAcceptancePublication{}, fmt.Errorf("observe UCI-1 obsolete bytes absent after offline recovery: %w", err)
	}
	if err := fault.clear(ctx); err != nil {
		return uciInstalledAcceptancePublication{}, err
	}
	faultCleared = true
	return publication, nil
}

func uci1RecoveryObserveBranch(ctx context.Context, runtime uciInstalledAcceptanceScenarioRuntime, selection uciInstalledAcceptanceSelection, root, sourcePath, nonce string) (uciInstalledAcceptancePublication, string, error) {
	branch := "uci1-recovery-" + nonce
	if err := uciRunInstalledAcceptanceGit(ctx, root, "checkout", "-b", branch); err != nil {
		return uciInstalledAcceptancePublication{}, "", fmt.Errorf("switch UCI-1 primary checkout to branch: %w", err)
	}

	functionName := "UCIRecoveryBranch" + nonce
	if err := os.WriteFile(sourcePath, []byte(uci1RecoverySource(runtime.Request.Fixture.SharedSymbol, functionName, "branch-"+nonce)), 0o600); err != nil {
		return uciInstalledAcceptancePublication{}, "", fmt.Errorf("write UCI-1 branch fixture: %w", err)
	}
	publication, err := uci1RecoveryRunIndex(ctx, runtime.ClientA, selection, root)
	if err != nil {
		return uciInstalledAcceptancePublication{}, "", err
	}
	if err := uciRequireInstalledAcceptanceWatcherCanary(ctx, runtime.ClientA, selection, publication, functionName, runtime.Request.Fixture.RelativePath, true); err != nil {
		return uciInstalledAcceptancePublication{}, "", fmt.Errorf("observe UCI-1 branch publication: %w", err)
	}
	state, err := uci1RecoveryViewGitState(ctx, runtime, publication.viewID)
	if err != nil {
		return uciInstalledAcceptancePublication{}, "", err
	}
	if !state.headOID.Valid || !state.refLabel.Valid || state.refLabel.String != branch {
		return uciInstalledAcceptancePublication{}, "", errors.New("installed MCP branch publication did not retain the active branch observation")
	}
	return publication, branch, nil
}

func uci1RecoveryObserveDetachedAndUnborn(ctx context.Context, runtime uciInstalledAcceptanceScenarioRuntime, selection uciInstalledAcceptanceSelection, root, sourcePath, head, nonce string) (uciInstalledAcceptancePublication, uciInstalledAcceptancePublication, error) {
	if err := uciRunInstalledAcceptanceGit(ctx, root, "checkout", "--detach", head); err != nil {
		return uciInstalledAcceptancePublication{}, uciInstalledAcceptancePublication{}, fmt.Errorf("detach UCI-1 primary checkout: %w", err)
	}
	functionName := "UCIRecoveryDetached" + nonce
	if err := os.WriteFile(sourcePath, []byte(uci1RecoverySource(runtime.Request.Fixture.SharedSymbol, functionName, "detached-"+nonce)), 0o600); err != nil {
		return uciInstalledAcceptancePublication{}, uciInstalledAcceptancePublication{}, fmt.Errorf("write UCI-1 detached fixture: %w", err)
	}
	detached, err := uci1RecoveryRunIndex(ctx, runtime.ClientA, selection, root)
	if err != nil {
		return uciInstalledAcceptancePublication{}, uciInstalledAcceptancePublication{}, err
	}
	if err := uciRequireInstalledAcceptanceWatcherCanary(ctx, runtime.ClientA, selection, detached, functionName, runtime.Request.Fixture.RelativePath, true); err != nil {
		return uciInstalledAcceptancePublication{}, uciInstalledAcceptancePublication{}, fmt.Errorf("observe UCI-1 detached publication: %w", err)
	}
	detachedState, err := uci1RecoveryViewGitState(ctx, runtime, detached.viewID)
	if err != nil {
		return uciInstalledAcceptancePublication{}, uciInstalledAcceptancePublication{}, err
	}
	if !detachedState.headOID.Valid || detachedState.refLabel.Valid {
		return uciInstalledAcceptancePublication{}, uciInstalledAcceptancePublication{}, errors.New("installed MCP detached publication did not retain detached Git facts")
	}

	unborn, err := uci1RecoveryObserveUnborn(ctx, runtime, nonce)
	if err != nil {
		return uciInstalledAcceptancePublication{}, uciInstalledAcceptancePublication{}, err
	}
	return detached, unborn, nil
}

func uci1RecoveryObserveUnborn(ctx context.Context, runtime uciInstalledAcceptanceScenarioRuntime, nonce string) (_ uciInstalledAcceptancePublication, retErr error) {
	if runtime.Installation == nil || runtime.Authority == nil || runtime.Authority.store == nil || runtime.Authority.source == nil || runtime.Authority.token == nil || runtime.Authority.profile == nil {
		return uciInstalledAcceptancePublication{}, errors.New("UCI-1 unborn probe runtime is incomplete")
	}
	root := filepath.Join(runtime.Request.FixtureRoot, "uci1-unborn-"+nonce)
	if err := os.MkdirAll(root, 0o700); err != nil {
		return uciInstalledAcceptancePublication{}, fmt.Errorf("create UCI-1 unborn fixture: %w", err)
	}
	defer func() {
		if removeErr := os.RemoveAll(root); removeErr != nil {
			retErr = errors.Join(retErr, fmt.Errorf("remove UCI-1 unborn fixture: %w", removeErr))
		}
	}()
	if err := uciRunInstalledAcceptanceGit(ctx, root, "init"); err != nil {
		return uciInstalledAcceptancePublication{}, fmt.Errorf("initialize UCI-1 unborn Git fixture: %w", err)
	}
	functionName := "UCIRecoveryUnborn" + nonce
	if err := os.WriteFile(filepath.Join(root, "unborn.go"), []byte("package fixture\n\nfunc "+functionName+"() string { return \"unborn-"+nonce+"\" }\n"), 0o600); err != nil {
		return uciInstalledAcceptancePublication{}, fmt.Errorf("write UCI-1 unborn fixture: %w", err)
	}
	locator, err := uciInstalledAcceptanceFileURI(root)
	if err != nil {
		return uciInstalledAcceptancePublication{}, err
	}
	contexts := gormdb.NewUCIContextStore(runtime.Authority.store.GetDB())
	checkout, err := contexts.RegisterCheckout(ctx, gormdb.RegisterCheckoutInput{
		SourceID:       runtime.Authority.source.SourceID,
		WorkstationID:  runtime.Authority.token.ID,
		Kind:           gormdb.UCICheckoutWorkingTree,
		OwnerPrincipal: runtime.Authority.principal,
		LocatorRef:     locator,
		OwnerInstance:  runtime.Authority.clientInstanceID,
	})
	if err != nil {
		return uciInstalledAcceptancePublication{}, fmt.Errorf("register UCI-1 unborn checkout: %w", err)
	}
	unbornProcess, startErr := runtime.Installation.Start(ctx, uciInstalledHarnessLaunchRequest{
		Role:             "daemon",
		WorkingDirectory: root,
		Environment:      runtime.ClientEnvironment,
		WithStdio:        true,
	})
	if unbornProcess != nil {
		defer func() {
			if closeErr := errors.Join(unbornProcess.closePipes(), unbornProcess.wait()); closeErr != nil {
				retErr = errors.Join(retErr, fmt.Errorf("close UCI-1 unborn client: %w", closeErr))
			}
		}()
	}
	if startErr != nil {
		return uciInstalledAcceptancePublication{}, fmt.Errorf("start UCI-1 unborn client: %w", startErr)
	}
	unbornClient, err := newUCIInstalledAcceptanceMCPClient("client-unborn", unbornProcess)
	if err != nil {
		return uciInstalledAcceptancePublication{}, err
	}
	if err := unbornClient.InitializeAndList(ctx); err != nil {
		return uciInstalledAcceptancePublication{}, err
	}
	selection, err := uci1RecoverySelectCheckout(ctx, unbornClient, runtime.Authority, checkout)
	if err != nil {
		return uciInstalledAcceptancePublication{}, err
	}
	publication, err := uci1RecoveryRunIndex(ctx, unbornClient, selection, root)
	if err != nil {
		return uciInstalledAcceptancePublication{}, err
	}
	if err := uciRequireInstalledAcceptanceWatcherCanary(ctx, unbornClient, selection, publication, functionName, "unborn.go", true); err != nil {
		return uciInstalledAcceptancePublication{}, fmt.Errorf("observe UCI-1 unborn publication: %w", err)
	}
	state, err := uci1RecoveryViewGitState(ctx, runtime, publication.viewID)
	if err != nil {
		return uciInstalledAcceptancePublication{}, err
	}
	if state.headOID.Valid || !state.refLabel.Valid || state.refLabel.String == "" {
		return uciInstalledAcceptancePublication{}, errors.New("installed MCP unborn publication did not retain nullable HEAD and symbolic ref facts")
	}
	return publication, nil
}

func uci1RecoveryRestorePrimary(ctx context.Context, runtime uciInstalledAcceptanceScenarioRuntime, input uci1RecoveryPrimaryRestoreInput) (uciInstalledAcceptancePublication, error) {
	selection, root, branch, head := input.selection, input.root, input.branch, input.head
	sourcePath, source, temporaryBranch := input.sourcePath, input.source, input.temporaryBranch
	if err := uciRunInstalledAcceptanceGit(ctx, root, "checkout", branch); err != nil {
		return uciInstalledAcceptancePublication{}, fmt.Errorf("restore UCI-1 primary branch: %w", err)
	}
	if temporaryBranch != "" {
		if err := uciRunInstalledAcceptanceGit(ctx, root, "branch", "-D", temporaryBranch); err != nil {
			return uciInstalledAcceptancePublication{}, fmt.Errorf("remove UCI-1 temporary branch: %w", err)
		}
	}
	if err := os.WriteFile(sourcePath, source, 0o600); err != nil {
		return uciInstalledAcceptancePublication{}, fmt.Errorf("restore UCI-1 primary source: %w", err)
	}
	restoredHead, err := uciReadInstalledAcceptanceGit(ctx, root, "rev-parse", "HEAD")
	if err != nil {
		return uciInstalledAcceptancePublication{}, err
	}
	if restoredHead != head {
		return uciInstalledAcceptancePublication{}, errors.New("UCI-1 primary checkout did not restore its original HEAD")
	}
	publication, err := uci1RecoveryRunIndex(ctx, runtime.ClientA, selection, root)
	if err != nil {
		return uciInstalledAcceptancePublication{}, err
	}
	if err := uciRequireInstalledAcceptanceWatcherCanary(ctx, runtime.ClientA, selection, publication, runtime.Request.Fixture.PrimaryCallee, runtime.Request.Fixture.RelativePath, true); err != nil {
		return uciInstalledAcceptancePublication{}, fmt.Errorf("restore UCI-1 primary publication: %w", err)
	}
	return publication, nil
}

func uci1RecoveryRunIndex(ctx context.Context, client *uciInstalledAcceptanceMCPClient, selection uciInstalledAcceptanceSelection, root string) (uciInstalledAcceptancePublication, error) {
	if client == nil || selection.contextHandle == "" || root == "" {
		return uciInstalledAcceptancePublication{}, errors.New("UCI-1 recovery index target is incomplete")
	}
	payload, err := client.Tool(ctx, "codebase_index", map[string]any{"context_handle": selection.contextHandle, "root": root})
	if err != nil {
		return uciInstalledAcceptancePublication{}, err
	}
	var response struct {
		Status string `json:"status"`
		RunID  string `json:"run_id"`
	}
	if err := json.Unmarshal(payload, &response); err != nil || (response.Status != "started" && response.Status != "already_running") || response.RunID == "" {
		return uciInstalledAcceptancePublication{}, errors.New("installed MCP recovery codebase_index did not return a live run")
	}
	selection.runID = response.RunID
	barrier, err := uciWaitForInstalledAcceptanceBarrier(ctx, client, selection)
	if err != nil {
		return uciInstalledAcceptancePublication{}, err
	}
	return uciWaitForInstalledAcceptanceQuiescence(ctx, client, selection, barrier)
}

func uci1RecoverySelectCheckout(ctx context.Context, client *uciInstalledAcceptanceMCPClient, authority *uciInstalledAcceptanceAuthority, checkout *gormdb.UCICheckout) (uciInstalledAcceptanceSelection, error) {
	if client == nil || authority == nil || authority.source == nil || authority.profile == nil || checkout == nil {
		return uciInstalledAcceptanceSelection{}, errors.New("UCI-1 recovery checkout selection is incomplete")
	}
	payload, err := client.Tool(ctx, "codebase_context", map[string]any{
		"action": "select",
		"checkout": map[string]any{
			"source_id":           authority.source.SourceID,
			"checkout_id":         checkout.CheckoutID,
			"incarnation_id":      checkout.IncarnationID,
			"analysis_profile_id": authority.profile.ProfileID,
		},
	})
	if err != nil {
		return uciInstalledAcceptanceSelection{}, err
	}
	var response struct {
		ContextHandle string `json:"context_handle"`
		BindingKind   string `json:"binding_kind"`
		ViewID        string `json:"view_id"`
		Context       *struct {
			ViewID string `json:"view_id"`
		} `json:"context"`
	}
	if err := json.Unmarshal(payload, &response); err != nil || response.ContextHandle == "" || response.BindingKind != "checkout" {
		return uciInstalledAcceptanceSelection{}, errors.New("installed MCP recovery checkout selection response is invalid")
	}
	if response.Context != nil && response.Context.ViewID != "" {
		response.ViewID = response.Context.ViewID
	}
	return uciInstalledAcceptanceSelection{contextHandle: response.ContextHandle, viewID: response.ViewID}, nil
}

func uci1RecoveryRequireSymbolAbsent(ctx context.Context, client *uciInstalledAcceptanceMCPClient, selection uciInstalledAcceptanceSelection, publication uciInstalledAcceptancePublication, symbol, relativePath string) error {
	payload, err := client.Tool(ctx, "codebase_search", map[string]any{
		"context_handle": selection.contextHandle,
		"query":          symbol,
		"path_prefix":    relativePath,
		"limit":          10,
	})
	if err != nil {
		return err
	}
	response, err := uciDecodeInstalledAcceptanceQuery(payload)
	if err != nil {
		return err
	}
	if !uciInstalledAcceptanceQueryMatchesPublication(response, publication) {
		return errors.New("installed MCP recovery search did not retain the recovered publication")
	}
	if response.Items != nil {
		for _, item := range *response.Items {
			if strings.Contains(item.Ref.EntityKey, symbol) || strings.Contains(item.Excerpt, symbol) {
				return errors.New("installed MCP recovery search retained obsolete source bytes")
			}
		}
	}
	return nil
}

func uci1RecoveryGitBranch(ctx context.Context, root string) (string, error) {
	command := uciReadInstalledAcceptanceGit
	branch, err := command(ctx, root, "symbolic-ref", "--quiet", "--short", "HEAD")
	if err != nil {
		return "", fmt.Errorf("read UCI-1 primary Git branch: %w", err)
	}
	return branch, nil
}

type uci1RecoveryViewState struct {
	headOID  sql.NullString
	refLabel sql.NullString
}

func uci1RecoveryViewGitState(ctx context.Context, runtime uciInstalledAcceptanceScenarioRuntime, viewID string) (uci1RecoveryViewState, error) {
	if runtime.Authority == nil || runtime.Authority.store == nil || viewID == "" {
		return uci1RecoveryViewState{}, errors.New("UCI-1 recovery view state target is incomplete")
	}
	var row struct {
		HeadOID  sql.NullString `gorm:"column:head_oid"`
		RefLabel sql.NullString `gorm:"column:ref_label"`
	}
	result := runtime.Authority.store.GetDB().WithContext(ctx).Raw("SELECT head_oid, ref_label FROM ci_views WHERE view_id = ?", viewID).Scan(&row)
	if result.Error != nil {
		return uci1RecoveryViewState{}, fmt.Errorf("read UCI-1 recovery view Git state: %w", result.Error)
	}
	if result.RowsAffected != 1 {
		return uci1RecoveryViewState{}, errors.New("UCI-1 recovery view Git state is unavailable")
	}
	return uci1RecoveryViewState{headOID: row.HeadOID, refLabel: row.RefLabel}, nil
}

func uci1RecoveryCheckoutState(ctx context.Context, runtime uciInstalledAcceptanceScenarioRuntime, checkoutID string) (string, error) {
	if runtime.Authority == nil || runtime.Authority.store == nil || checkoutID == "" {
		return "", errors.New("UCI-1 recovery checkout state target is incomplete")
	}
	var row struct {
		State string `gorm:"column:state"`
	}
	result := runtime.Authority.store.GetDB().WithContext(ctx).Raw("SELECT state FROM ci_checkouts WHERE checkout_id = ?", checkoutID).Scan(&row)
	if result.Error != nil {
		return "", result.Error
	}
	if result.RowsAffected != 1 {
		return "", errors.New("UCI-1 recovery checkout state is unavailable")
	}
	return row.State, nil
}

func uci1RecoverySetCheckoutState(ctx context.Context, runtime uciInstalledAcceptanceScenarioRuntime, checkoutID, state string) error {
	if runtime.Authority == nil || runtime.Authority.store == nil || checkoutID == "" || state == "" {
		return errors.New("UCI-1 recovery checkout state mutation is incomplete")
	}
	result := runtime.Authority.store.GetDB().WithContext(ctx).Table("ci_checkouts").Where("checkout_id = ?", checkoutID).Update("state", state)
	if result.Error != nil {
		return fmt.Errorf("set UCI-1 recovery checkout state: %w", result.Error)
	}
	if result.RowsAffected != 1 {
		return errors.New("set UCI-1 recovery checkout state affected no checkout")
	}
	return nil
}

type uci1RecoveryOverflowFault struct {
	db           *sql.DB
	checkoutID   string
	priorRescan  int
	hadOverflow  bool
	hadOffline   bool
	priorOffline uci1RecoveryOfflineRecord
}

type uci1RecoveryOfflineRecord struct {
	fromSequence      int64
	throughSequence   int64
	maxDirtyPaths     int64
	observedDirtyPath int64
}

func uci1RecoveryInstallOverflowFault(ctx context.Context, runtime uciInstalledAcceptanceScenarioRuntime, checkoutID string) (*uci1RecoveryOverflowFault, error) {
	registryPath := filepath.Join(runtime.Request.LocalStateRoot, "daemon-data", "modules", "codeintel", "uci-registry.sqlite")
	db, err := sql.Open("sqlite", registryPath)
	if err != nil {
		return nil, fmt.Errorf("open UCI-1 local recovery registry: %w", err)
	}
	fault := &uci1RecoveryOverflowFault{db: db, checkoutID: checkoutID}
	failed := true
	defer func() {
		if failed {
			_ = db.Close()
		}
	}()
	if _, err := db.ExecContext(ctx, "PRAGMA busy_timeout = 5000"); err != nil {
		return nil, fmt.Errorf("configure UCI-1 local recovery registry: %w", err)
	}
	var dirtySequence, lastReconciled int64
	if err := db.QueryRowContext(ctx, "SELECT dirty_sequence, last_reconciled_sequence, rescan_required FROM uci_local_checkouts WHERE server_checkout_id = ?", checkoutID).Scan(&dirtySequence, &lastReconciled, &fault.priorRescan); err != nil {
		return nil, fmt.Errorf("read UCI-1 local recovery checkout: %w", err)
	}
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM uci_local_rescan_causes WHERE server_checkout_id = ? AND cause = 'overflow'", checkoutID).Scan(&fault.hadOverflow); err != nil {
		return nil, fmt.Errorf("read UCI-1 local overflow cause: %w", err)
	}
	var prior uci1RecoveryOfflineRecord
	err = db.QueryRowContext(ctx, "SELECT from_sequence, through_sequence, max_dirty_paths, observed_dirty_paths FROM uci_local_offline_recovery WHERE server_checkout_id = ?", checkoutID).Scan(&prior.fromSequence, &prior.throughSequence, &prior.maxDirtyPaths, &prior.observedDirtyPath)
	switch {
	case err == nil:
		fault.hadOffline = true
		fault.priorOffline = prior
	case errors.Is(err, sql.ErrNoRows):
	default:
		return nil, fmt.Errorf("read UCI-1 local offline recovery: %w", err)
	}
	sequence := dirtySequence
	if lastReconciled > sequence {
		sequence = lastReconciled
	}
	sequence++
	if _, err := db.ExecContext(ctx, "UPDATE uci_local_checkouts SET dirty_sequence = ?, rescan_required = 1 WHERE server_checkout_id = ?", sequence, checkoutID); err != nil {
		return nil, fmt.Errorf("install UCI-1 local overflow fault: %w", err)
	}
	if _, err := db.ExecContext(ctx, "INSERT INTO uci_local_rescan_causes (server_checkout_id, cause) VALUES (?, 'overflow') ON CONFLICT DO NOTHING", checkoutID); err != nil {
		return nil, fmt.Errorf("record UCI-1 local overflow cause: %w", err)
	}
	if _, err := db.ExecContext(ctx, `
		INSERT INTO uci_local_offline_recovery (
			server_checkout_id, from_sequence, through_sequence, max_dirty_paths, observed_dirty_paths
		) VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(server_checkout_id) DO UPDATE SET
			from_sequence = excluded.from_sequence,
			through_sequence = excluded.through_sequence,
			max_dirty_paths = excluded.max_dirty_paths,
			observed_dirty_paths = excluded.observed_dirty_paths`, checkoutID, sequence, sequence, 1, 2); err != nil {
		return nil, fmt.Errorf("record UCI-1 local offline overflow watermark: %w", err)
	}
	failed = false
	return fault, nil
}

func (fault *uci1RecoveryOverflowFault) clear(ctx context.Context) error {
	if fault == nil || fault.db == nil || fault.checkoutID == "" {
		return errors.New("UCI-1 local overflow fault is incomplete")
	}
	defer fault.db.Close()
	var cleanup []error
	if !fault.hadOverflow {
		if _, err := fault.db.ExecContext(ctx, "DELETE FROM uci_local_rescan_causes WHERE server_checkout_id = ? AND cause = 'overflow'", fault.checkoutID); err != nil {
			cleanup = append(cleanup, err)
		}
	}
	if fault.hadOffline {
		prior := fault.priorOffline
		if _, err := fault.db.ExecContext(ctx, `
			INSERT INTO uci_local_offline_recovery (
				server_checkout_id, from_sequence, through_sequence, max_dirty_paths, observed_dirty_paths
			) VALUES (?, ?, ?, ?, ?)
			ON CONFLICT(server_checkout_id) DO UPDATE SET
				from_sequence = excluded.from_sequence,
				through_sequence = excluded.through_sequence,
				max_dirty_paths = excluded.max_dirty_paths,
				observed_dirty_paths = excluded.observed_dirty_paths`, fault.checkoutID, prior.fromSequence, prior.throughSequence, prior.maxDirtyPaths, prior.observedDirtyPath); err != nil {
			cleanup = append(cleanup, err)
		}
	} else if _, err := fault.db.ExecContext(ctx, "DELETE FROM uci_local_offline_recovery WHERE server_checkout_id = ?", fault.checkoutID); err != nil {
		cleanup = append(cleanup, err)
	}
	if _, err := fault.db.ExecContext(ctx, "UPDATE uci_local_checkouts SET rescan_required = ? WHERE server_checkout_id = ?", fault.priorRescan, fault.checkoutID); err != nil {
		cleanup = append(cleanup, err)
	}
	return errors.Join(cleanup...)
}

func uci1RecoverySource(sharedSymbol, callee, marker string) string {
	return "package fixture\n\nfunc " + sharedSymbol + "() string { return " + callee + "() }\nfunc " + callee + "() string { return \"" + marker + "\" }\n"
}

func uci1RecoveryEvidence(scenarioID string, publication uciInstalledAcceptancePublication, values ...string) uciInstalledAcceptanceScenarioEvidence {
	parts := append([]string{scenarioID, publication.sourceID, publication.checkoutID, publication.viewID, publication.runID, fmt.Sprintf("%d", publication.generation)}, values...)
	return uciInstalledAcceptanceScenarioEvidence{
		Code:   uciInstalledAcceptanceScenarioCodeObserved,
		Digest: uciInstalledAcceptanceStringDigest(strings.Join(parts, "\x00")),
	}
}
