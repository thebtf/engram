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
	"time"

	"github.com/thebtf/engram/internal/uci"
)

const uci1ScannerProbeRoot = ".uci1-scanner-probe"

type uci1ScannerProbeFixture struct {
	root         string
	agentRoot    string
	agentOwned   bool
	dirty        bool
	trackedFiles map[string][]byte
	headPath     string
	headBackup   string
}

type uci1ScannerMembership struct {
	PathKey     string         `gorm:"column:path_key"`
	DisplayPath string         `gorm:"column:display_path"`
	FileState   string         `gorm:"column:file_state"`
	ArtifactID  sql.NullString `gorm:"column:artifact_id"`
}

// uci1ProbeScannerInstalled observes scanner behavior only through the installed
// codebase tools. Persistent rows are read solely to bind public index actions
// to their current-View membership invariants.
func uci1ProbeScannerInstalled(ctx context.Context, runtime uciInstalledAcceptanceScenarioRuntime) (_ map[string]uciInstalledAcceptanceScenarioEvidence, retErr error) {
	fixture, err := uci1NewScannerProbeFixture(runtime)
	if err != nil {
		return nil, err
	}
	defer func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		retErr = errors.Join(retErr, fixture.reset(cleanupCtx, runtime))
	}()

	evidence := make(map[string]uciInstalledAcceptanceScenarioEvidence)
	for _, scenario := range []struct {
		id  string
		run func(context.Context, uciInstalledAcceptanceScenarioRuntime, *uci1ScannerProbeFixture) (string, bool, error)
	}{
		{id: "U13", run: uci1ObserveScannerNestedIgnores},
		{id: "U14", run: uci1ObserveScannerEscapeAndInaccessible},
		{id: "U15", run: uci1ObserveScannerDeleteAllAndFailedScan},
		{id: "U37", run: uci1ObserveScannerUnicodeCRLFSpans},
		{id: "U39", run: uci1ObserveScannerNestedBoundaries},
	} {
		facts, observed, scenarioErr := scenario.run(ctx, runtime, fixture)
		if resetErr := fixture.reset(ctx, runtime); resetErr != nil {
			return nil, errors.Join(scenarioErr, resetErr)
		}
		if scenarioErr != nil {
			return nil, fmt.Errorf("installed scanner scenario %s: %w", scenario.id, scenarioErr)
		}
		if observed {
			evidence[scenario.id] = uci1ScannerObservedEvidence(facts)
		}
	}
	return evidence, nil
}

func uci1NewScannerProbeFixture(runtime uciInstalledAcceptanceScenarioRuntime) (*uci1ScannerProbeFixture, error) {
	if runtime.ClientA == nil || runtime.Authority == nil || runtime.Authority.store == nil || runtime.Worktrees.primaryRoot == "" || runtime.Worktrees.head == "" {
		return nil, errors.New("installed scanner scenario runtime is incomplete")
	}
	if _, found := runtime.Selections[uciInstalledAcceptanceClientA]; !found {
		return nil, errors.New("installed scanner primary selection is unavailable")
	}
	root := filepath.Join(runtime.Worktrees.primaryRoot, uci1ScannerProbeRoot)
	if _, err := os.Lstat(root); err == nil {
		return nil, errors.New("installed scanner probe root already exists")
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("inspect installed scanner probe root: %w", err)
	}
	return &uci1ScannerProbeFixture{
		root:      root,
		agentRoot: filepath.Join(runtime.Worktrees.primaryRoot, ".agent"),
	}, nil
}

func (fixture *uci1ScannerProbeFixture) physical(relativePath string) string {
	return filepath.Join(fixture.root, filepath.FromSlash(relativePath))
}

func (fixture *uci1ScannerProbeFixture) relative(relativePath string) string {
	return uci1ScannerProbeRoot + "/" + relativePath
}

func (fixture *uci1ScannerProbeFixture) write(relativePath string, body []byte) error {
	path := fixture.physical(relativePath)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	if err := os.WriteFile(path, body, 0o600); err != nil {
		return err
	}
	fixture.dirty = true
	return nil
}

func (fixture *uci1ScannerProbeFixture) stage(ctx context.Context, runtime uciInstalledAcceptanceScenarioRuntime, paths ...string) error {
	if len(paths) == 0 {
		return nil
	}
	args := append([]string{"add", "-f", "--"}, paths...)
	if err := uciRunInstalledAcceptanceGit(ctx, runtime.Worktrees.primaryRoot, args...); err != nil {
		return err
	}
	fixture.dirty = true
	return nil
}

func (fixture *uci1ScannerProbeFixture) captureTracked(runtime uciInstalledAcceptanceScenarioRuntime) error {
	if fixture.trackedFiles != nil {
		return nil
	}
	paths := []string{
		".engram-project",
		runtime.Request.Fixture.RelativePath,
		uciInstalledAcceptanceParserCanaryRelativePath,
	}
	files := make(map[string][]byte, len(paths))
	for _, relativePath := range paths {
		body, err := os.ReadFile(filepath.Join(runtime.Worktrees.primaryRoot, filepath.FromSlash(relativePath)))
		if err != nil {
			return fmt.Errorf("snapshot installed scanner tracked fixture %q: %w", relativePath, err)
		}
		files[relativePath] = append([]byte(nil), body...)
	}
	fixture.trackedFiles = files
	fixture.dirty = true
	return nil
}

func (fixture *uci1ScannerProbeFixture) restoreHead() error {
	if fixture.headPath == "" {
		return nil
	}
	if err := os.Rename(fixture.headBackup, fixture.headPath); err != nil {
		return fmt.Errorf("restore installed scanner Git HEAD: %w", err)
	}
	fixture.headPath = ""
	fixture.headBackup = ""
	return nil
}

func (fixture *uci1ScannerProbeFixture) reset(ctx context.Context, runtime uciInstalledAcceptanceScenarioRuntime) error {
	if !fixture.dirty {
		return nil
	}

	var cleanup []error
	if err := fixture.restoreHead(); err != nil {
		cleanup = append(cleanup, err)
	}
	if err := os.RemoveAll(fixture.root); err != nil {
		cleanup = append(cleanup, fmt.Errorf("remove installed scanner probe root: %w", err))
	}
	if fixture.agentOwned {
		if err := os.RemoveAll(fixture.agentRoot); err != nil {
			cleanup = append(cleanup, fmt.Errorf("remove installed scanner nested .agent fixture: %w", err))
		}
		fixture.agentOwned = false
	}
	if err := uciRunInstalledAcceptanceGit(ctx, runtime.Worktrees.primaryRoot, "read-tree", runtime.Worktrees.head); err != nil {
		cleanup = append(cleanup, fmt.Errorf("restore installed scanner Git index: %w", err))
	}
	for relativePath, body := range fixture.trackedFiles {
		path := filepath.Join(runtime.Worktrees.primaryRoot, filepath.FromSlash(relativePath))
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			cleanup = append(cleanup, fmt.Errorf("restore installed scanner parent %q: %w", relativePath, err))
			continue
		}
		if err := os.WriteFile(path, body, 0o600); err != nil {
			cleanup = append(cleanup, fmt.Errorf("restore installed scanner tracked fixture %q: %w", relativePath, err))
		}
	}
	fixture.trackedFiles = nil
	fixture.dirty = false
	if err := errors.Join(cleanup...); err != nil {
		return err
	}
	if _, err := uci1ScannerIndex(ctx, runtime); err != nil {
		return fmt.Errorf("restore installed scanner fixture index: %w", err)
	}
	return nil
}

func uci1ScannerObservedEvidence(facts string) uciInstalledAcceptanceScenarioEvidence {
	return uciInstalledAcceptanceScenarioEvidence{
		Code:   uciInstalledAcceptanceScenarioCodeObserved,
		Digest: uciInstalledAcceptanceStringDigest(facts),
	}
}

func uci1ScannerIndex(ctx context.Context, runtime uciInstalledAcceptanceScenarioRuntime) (uciInstalledAcceptancePublication, error) {
	runID, err := uci1ScannerStartIndex(ctx, runtime)
	if err != nil {
		return uciInstalledAcceptancePublication{}, err
	}
	selection := runtime.Selections[uciInstalledAcceptanceClientA]
	selection.runID = runID
	publication, err := uciWaitForInstalledAcceptanceBarrier(ctx, runtime.ClientA, selection)
	if err != nil {
		return uciInstalledAcceptancePublication{}, err
	}
	return uciWaitForInstalledAcceptanceQuiescence(ctx, runtime.ClientA, selection, publication)
}

func uci1ScannerStartIndex(ctx context.Context, runtime uciInstalledAcceptanceScenarioRuntime) (string, error) {
	selection := runtime.Selections[uciInstalledAcceptanceClientA]
	payload, err := runtime.ClientA.Tool(ctx, "codebase_index", map[string]any{
		"context_handle": selection.contextHandle,
		"root":           runtime.Worktrees.primaryRoot,
	})
	if err != nil {
		return "", err
	}
	var response struct {
		Status string `json:"status"`
		RunID  string `json:"run_id"`
	}
	if err := json.Unmarshal(payload, &response); err != nil || response.RunID == "" || (response.Status != "started" && response.Status != "already_running") {
		return "", errors.New("installed scanner codebase_index did not return a live run")
	}
	return response.RunID, nil
}

func uci1ScannerWaitForFailure(ctx context.Context, runtime uciInstalledAcceptanceScenarioRuntime, runID string) (string, bool, error) {
	selection := runtime.Selections[uciInstalledAcceptanceClientA]
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	for {
		payload, err := uciInstalledAcceptanceStatusTool(ctx, runtime.ClientA, map[string]any{"context_handle": selection.contextHandle})
		if err != nil {
			return "", false, err
		}
		status, err := uciDecodeInstalledAcceptanceStatus(payload)
		if err != nil {
			return "", false, err
		}
		if status.runID == runID {
			switch status.status {
			case "error":
				if status.error == "" {
					return "", false, errors.New("installed scanner failed run omitted its error")
				}
				return status.error, true, nil
			case "idle":
				return "", false, nil
			}
		}
		select {
		case <-ctx.Done():
			return "", false, ctx.Err()
		case <-ticker.C:
		}
	}
}

func uci1ScannerCurrentPublication(ctx context.Context, runtime uciInstalledAcceptanceScenarioRuntime) (uciInstalledAcceptancePublication, error) {
	selection := runtime.Selections[uciInstalledAcceptanceClientA]
	payload, err := uciInstalledAcceptanceStatusTool(ctx, runtime.ClientA, map[string]any{"context_handle": selection.contextHandle})
	if err != nil {
		return uciInstalledAcceptancePublication{}, err
	}
	status, err := uciDecodeInstalledAcceptanceStatus(payload)
	if err != nil {
		return uciInstalledAcceptancePublication{}, err
	}
	selection.runID = status.runID
	return uciInstalledAcceptanceStatusPublication(status, selection)
}

func uci1ScannerMemberships(ctx context.Context, runtime uciInstalledAcceptanceScenarioRuntime, publication uciInstalledAcceptancePublication) (map[string]uci1ScannerMembership, error) {
	if runtime.Authority == nil || runtime.Authority.store == nil || publication.sourceID == "" || publication.checkoutID == "" || publication.viewID == "" || publication.generation < 1 {
		return nil, errors.New("installed scanner membership authority is incomplete")
	}
	var rows []uci1ScannerMembership
	query := runtime.Authority.store.GetDB().WithContext(ctx).Raw(`
		SELECT membership.path_key, membership.display_path, membership.file_state, membership.artifact_id::text AS artifact_id
		FROM ci_memberships AS membership
		JOIN ci_views AS view
		  ON view.source_id = membership.source_id
		 AND view.checkout_id = membership.checkout_id
		WHERE view.view_id = ?
		  AND membership.source_id = ?
		  AND membership.checkout_id = ?
		  AND membership.valid_from_generation <= view.generation
		  AND (membership.valid_to_generation IS NULL OR membership.valid_to_generation > view.generation)
		ORDER BY membership.path_key ASC`,
		publication.viewID,
		publication.sourceID,
		publication.checkoutID,
	)
	if query.Error != nil || query.Scan(&rows).Error != nil {
		return nil, errors.New("load installed scanner current-View memberships")
	}
	memberships := make(map[string]uci1ScannerMembership, len(rows))
	for _, row := range rows {
		if row.PathKey == "" || row.DisplayPath == "" || row.PathKey != row.DisplayPath {
			return nil, errors.New("installed scanner membership path is invalid")
		}
		if _, found := memberships[row.PathKey]; found {
			return nil, errors.New("installed scanner current View repeated a membership")
		}
		memberships[row.PathKey] = row
	}
	return memberships, nil
}

func uci1ScannerRequireMembership(memberships map[string]uci1ScannerMembership, path, state string, hasArtifact bool) error {
	membership, found := memberships[path]
	if !found || membership.FileState != state || membership.ArtifactID.Valid != hasArtifact {
		return fmt.Errorf("installed scanner membership %q does not have expected state", path)
	}
	return nil
}

func uci1ScannerSearch(ctx context.Context, runtime uciInstalledAcceptanceScenarioRuntime, publication uciInstalledAcceptancePublication, query, pathPrefix string) (uci.QueryResponse, error) {
	selection := runtime.Selections[uciInstalledAcceptanceClientA]
	payload, err := runtime.ClientA.Tool(ctx, "codebase_search", map[string]any{
		"context_handle": selection.contextHandle,
		"query":          query,
		"path_prefix":    pathPrefix,
		"limit":          10,
	})
	if err != nil {
		return uci.QueryResponse{}, err
	}
	response, err := uciDecodeInstalledAcceptanceQuery(payload)
	if err != nil {
		return uci.QueryResponse{}, err
	}
	if !uciInstalledAcceptanceQueryMatchesPublication(response, publication) {
		return uci.QueryResponse{}, errors.New("installed scanner search left the selected View")
	}
	return response, nil
}

func uci1ScannerRequireSearchAbsent(ctx context.Context, runtime uciInstalledAcceptanceScenarioRuntime, publication uciInstalledAcceptancePublication, query, pathPrefix string) error {
	response, err := uci1ScannerSearch(ctx, runtime, publication, query, pathPrefix)
	if err != nil {
		return err
	}
	if response.Items != nil {
		for _, item := range *response.Items {
			if strings.Contains(item.Ref.EntityKey, query) || strings.Contains(item.Excerpt, query) {
				return errors.New("installed scanner search exposed excluded source")
			}
		}
	}
	return nil
}

func uci1ObserveScannerNestedIgnores(ctx context.Context, runtime uciInstalledAcceptanceScenarioRuntime, fixture *uci1ScannerProbeFixture) (string, bool, error) {
	const (
		directory = "u13"
		tracked   = "u13/tracked-ignored.go"
		kept      = "u13/kept.go"
		ignored   = "u13/ignored.go"
	)
	if err := fixture.write(directory+"/.gitignore", []byte("*.go\n!kept.go\n")); err != nil {
		return "", false, err
	}
	if err := fixture.write(tracked, []byte("package fixture\n\nfunc UCI1ScannerTrackedIgnored() string { return \"tracked\" }\n")); err != nil {
		return "", false, err
	}
	if err := fixture.stage(ctx, runtime, fixture.relative(tracked)); err != nil {
		return "", false, err
	}
	if err := fixture.write(kept, []byte("package fixture\n\nfunc UCI1ScannerNegatedKeep() string { return \"kept\" }\n")); err != nil {
		return "", false, err
	}
	if err := fixture.write(ignored, []byte("package fixture\n\nfunc UCI1ScannerIgnoredMustNotAppear() string { return \"ignored\" }\n")); err != nil {
		return "", false, err
	}

	publication, err := uci1ScannerIndex(ctx, runtime)
	if err != nil {
		return "", false, err
	}
	memberships, err := uci1ScannerMemberships(ctx, runtime, publication)
	if err != nil {
		return "", false, err
	}
	for _, path := range []string{fixture.relative(tracked), fixture.relative(kept)} {
		if err := uci1ScannerRequireMembership(memberships, path, "present", true); err != nil {
			return "", false, err
		}
	}
	if _, found := memberships[fixture.relative(ignored)]; found {
		return "", false, errors.New("installed scanner indexed a nested ignored source")
	}
	if err := uci1ScannerRequireSearchAbsent(ctx, runtime, publication, "UCI1ScannerIgnoredMustNotAppear", fixture.relative(directory)); err != nil {
		return "", false, err
	}
	return strings.Join([]string{publication.viewID, fixture.relative(tracked), fixture.relative(kept)}, "\x00"), true, nil
}

func uci1ObserveScannerEscapeAndInaccessible(ctx context.Context, runtime uciInstalledAcceptanceScenarioRuntime, fixture *uci1ScannerProbeFixture) (string, bool, error) {
	const (
		directory = "u14"
		escape    = "u14/escape.go"
		blocked   = "u14/blocked.go"
	)
	outside := filepath.Join(runtime.Request.FixtureRoot, "uci1-scanner-outside.go")
	if err := os.WriteFile(outside, []byte("package fixture\n\nfunc UCI1ScannerOutsideSecret() string { return \"outside\" }\n"), 0o600); err != nil {
		return "", false, err
	}
	fixture.dirty = true
	if err := os.MkdirAll(filepath.Dir(fixture.physical(escape)), 0o700); err != nil {
		return "", false, err
	}
	if err := os.Symlink(outside, fixture.physical(escape)); err != nil {
		return "", false, nil
	}
	fixture.dirty = true
	if err := fixture.stage(ctx, runtime, fixture.relative(escape)); err != nil {
		return "", false, nil
	}
	if err := fixture.write(blocked, []byte("package fixture\n\nfunc UCI1ScannerBlockedFile() string { return \"blocked\" }\n")); err != nil {
		return "", false, err
	}
	if err := fixture.stage(ctx, runtime, fixture.relative(blocked)); err != nil {
		return "", false, err
	}

	publication, err := uci1ScannerIndex(ctx, runtime)
	if err != nil {
		return "", false, err
	}
	memberships, err := uci1ScannerMemberships(ctx, runtime, publication)
	if err != nil {
		return "", false, err
	}
	if err := uci1ScannerRequireMembership(memberships, fixture.relative(escape), "excluded", false); err != nil {
		return "", false, err
	}
	if err := uci1ScannerRequireMembership(memberships, fixture.relative(blocked), "present", true); err != nil {
		return "", false, err
	}
	if err := uci1ScannerRequireSearchAbsent(ctx, runtime, publication, "UCI1ScannerOutsideSecret", fixture.relative(directory)); err != nil {
		return "", false, err
	}

	blockedPath := fixture.physical(blocked)
	if err := os.Chmod(blockedPath, 0); err != nil {
		return "", false, nil
	}
	faultCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	runID, startErr := uci1ScannerStartIndex(faultCtx, runtime)
	var failure string
	observedFailure := false
	if startErr == nil {
		failure, observedFailure, err = uci1ScannerWaitForFailure(faultCtx, runtime, runID)
	}
	cancel()
	restoreErr := os.Chmod(blockedPath, 0o600)
	if restoreErr != nil {
		return "", false, fmt.Errorf("restore inaccessible scanner fixture: %w", restoreErr)
	}
	if startErr != nil {
		return "", false, startErr
	}
	if err != nil {
		return "", false, err
	}
	if !observedFailure {
		return "", false, nil
	}
	current, err := uci1ScannerCurrentPublication(ctx, runtime)
	if err != nil {
		return "", false, err
	}
	if current.viewID != publication.viewID {
		return "", false, errors.New("inaccessible scanner run advanced the current View")
	}
	currentMemberships, err := uci1ScannerMemberships(ctx, runtime, current)
	if err != nil {
		return "", false, err
	}
	if err := uci1ScannerRequireMembership(currentMemberships, fixture.relative(blocked), "present", true); err != nil {
		return "", false, errors.New("inaccessible scanner run mass-deleted the retained source")
	}
	return strings.Join([]string{publication.viewID, current.viewID, failure}, "\x00"), true, nil
}

func uci1ObserveScannerDeleteAllAndFailedScan(ctx context.Context, runtime uciInstalledAcceptanceScenarioRuntime, fixture *uci1ScannerProbeFixture) (string, bool, error) {
	if err := fixture.captureTracked(runtime); err != nil {
		return "", false, err
	}
	tracked := []string{
		".engram-project",
		runtime.Request.Fixture.RelativePath,
		uciInstalledAcceptanceParserCanaryRelativePath,
	}
	args := append([]string{"rm", "-f", "--"}, tracked...)
	if err := uciRunInstalledAcceptanceGit(ctx, runtime.Worktrees.primaryRoot, args...); err != nil {
		return "", false, err
	}
	fixture.dirty = true
	emptyPublication, err := uci1ScannerIndex(ctx, runtime)
	if err != nil {
		return "", false, err
	}
	emptyMemberships, err := uci1ScannerMemberships(ctx, runtime, emptyPublication)
	if err != nil {
		return "", false, err
	}
	if len(emptyMemberships) != 0 {
		return "", false, errors.New("complete installed delete-all retained memberships")
	}

	if err := uciRunInstalledAcceptanceGit(ctx, runtime.Worktrees.primaryRoot, "read-tree", runtime.Worktrees.head); err != nil {
		return "", false, err
	}
	for relativePath, body := range fixture.trackedFiles {
		path := filepath.Join(runtime.Worktrees.primaryRoot, filepath.FromSlash(relativePath))
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			return "", false, err
		}
		if err := os.WriteFile(path, body, 0o600); err != nil {
			return "", false, err
		}
	}
	restoredPublication, err := uci1ScannerIndex(ctx, runtime)
	if err != nil {
		return "", false, err
	}
	restoredMemberships, err := uci1ScannerMemberships(ctx, runtime, restoredPublication)
	if err != nil {
		return "", false, err
	}
	if len(restoredMemberships) == 0 {
		return "", false, errors.New("installed scanner did not restore nonempty fixture before fault")
	}

	headPath := filepath.Join(runtime.Worktrees.primaryRoot, ".git", "HEAD")
	headBackup := filepath.Join(runtime.Worktrees.primaryRoot, ".git", "uci1-scanner-head.backup")
	if _, err := os.Lstat(headBackup); err == nil {
		return "", false, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", false, err
	}
	if err := os.Rename(headPath, headBackup); err != nil {
		return "", false, nil
	}
	fixture.headPath = headPath
	fixture.headBackup = headBackup
	fixture.dirty = true

	faultCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	runID, startErr := uci1ScannerStartIndex(faultCtx, runtime)
	failure := ""
	observedFailure := false
	if startErr != nil {
		failure = startErr.Error()
		observedFailure = true
	} else {
		failure, observedFailure, err = uci1ScannerWaitForFailure(faultCtx, runtime, runID)
	}
	cancel()
	if restoreErr := fixture.restoreHead(); restoreErr != nil {
		return "", false, restoreErr
	}
	if err != nil {
		return "", false, err
	}
	if !observedFailure {
		return "", false, nil
	}
	current, err := uci1ScannerCurrentPublication(ctx, runtime)
	if err != nil {
		return "", false, err
	}
	if current.viewID != restoredPublication.viewID {
		return "", false, errors.New("failed scanner run advanced a delete-all View")
	}
	currentMemberships, err := uci1ScannerMemberships(ctx, runtime, current)
	if err != nil {
		return "", false, err
	}
	if len(currentMemberships) == 0 {
		return "", false, errors.New("failed scanner run published an empty census")
	}
	return strings.Join([]string{emptyPublication.viewID, restoredPublication.viewID, current.viewID, failure}, "\x00"), true, nil
}

func uci1ObserveScannerUnicodeCRLFSpans(ctx context.Context, runtime uciInstalledAcceptanceScenarioRuntime, fixture *uci1ScannerProbeFixture) (string, bool, error) {
	const (
		directory = "u37/Код"
		leftPath  = "u37/Код/Readme.go"
		rightPath = "u37/Код/README.go"
	)
	leftSource := []byte("package fixture\r\n\r\nfunc UCI1ScannerUnicodeReadme() string {\r\n\treturn \"Привет\"\r\n}\r\n")
	if err := fixture.write(leftPath, leftSource); err != nil {
		return "", false, err
	}
	leftPublication, err := uci1ScannerIndex(ctx, runtime)
	if err != nil {
		return "", false, err
	}
	left, err := uci1ScannerReadExactFunction(ctx, runtime, leftPublication, fixture.relative(leftPath), "UCI1ScannerUnicodeReadme", leftSource)
	if err != nil {
		return "", false, err
	}
	if err := os.Remove(fixture.physical(leftPath)); err != nil {
		return "", false, err
	}
	fixture.dirty = true

	rightSource := []byte("package fixture\r\n\r\nfunc UCI1ScannerUnicodeREADME() string {\r\n\treturn \"Здравствуйте\"\r\n}\r\n")
	if err := fixture.write(rightPath, rightSource); err != nil {
		return "", false, err
	}
	rightPublication, err := uci1ScannerIndex(ctx, runtime)
	if err != nil {
		return "", false, err
	}
	right, err := uci1ScannerReadExactFunction(ctx, runtime, rightPublication, fixture.relative(rightPath), "UCI1ScannerUnicodeREADME", rightSource)
	if err != nil {
		return "", false, err
	}
	if left.Path == right.Path || !strings.EqualFold(left.Path, right.Path) || !strings.HasPrefix(left.Path, fixture.relative(directory[:len("u37")])) {
		return "", false, errors.New("installed scanner collapsed Unicode case-distinct paths")
	}
	return strings.Join([]string{
		leftPublication.viewID,
		rightPublication.viewID,
		left.Path,
		fmt.Sprintf("%d:%d", left.Span.ByteStart, left.Span.ByteEnd),
		right.Path,
		fmt.Sprintf("%d:%d", right.Span.ByteStart, right.Span.ByteEnd),
	}, "\x00"), true, nil
}

func uci1ScannerReadExactFunction(ctx context.Context, runtime uciInstalledAcceptanceScenarioRuntime, publication uciInstalledAcceptancePublication, path, functionName string, source []byte) (uci.QueryItem, error) {
	response, err := uci1ScannerSearch(ctx, runtime, publication, functionName, path)
	if err != nil {
		return uci.QueryItem{}, err
	}
	if response.Items == nil {
		return uci.QueryItem{}, errors.New("installed scanner search omitted the Unicode source")
	}
	var item *uci.QueryItem
	for index := range *response.Items {
		candidate := &(*response.Items)[index]
		name, nameOK := uciInstalledAcceptanceGoFunctionName(candidate.Ref.EntityKey)
		if candidate.Path == path && nameOK && name == functionName {
			if item != nil {
				return uci.QueryItem{}, errors.New("installed scanner search returned duplicate Unicode functions")
			}
			item = candidate
		}
	}
	if item == nil || item.Span.ByteStart < 0 || item.Span.ByteEnd <= item.Span.ByteStart || item.Span.ByteEnd > int64(len(source)) {
		return uci.QueryItem{}, errors.New("installed scanner search returned an invalid Unicode byte span")
	}
	spanText := string(source[item.Span.ByteStart:item.Span.ByteEnd])
	contentDigest := strings.TrimPrefix(string(item.ContentDigest), "sha256:")
	if !strings.Contains(spanText, "\r\n") || !uciInstalledAcceptanceIsBareSHA256(contentDigest) {
		return uci.QueryItem{}, errors.New("installed scanner search did not bind the exact CRLF source artifact")
	}

	selection := runtime.Selections[uciInstalledAcceptanceClientA]
	payload, err := runtime.ClientA.Tool(ctx, "codebase_read", map[string]any{
		"context_handle": selection.contextHandle,
		"ref": map[string]any{
			"source_id":  item.Ref.SourceID,
			"view_id":    item.Ref.ViewID,
			"entity_key": item.Ref.EntityKey,
		},
		"span": map[string]any{
			"byte_start": item.Span.ByteStart,
			"byte_end":   item.Span.ByteEnd,
			"line_start": item.Span.LineStart,
			"line_end":   item.Span.LineEnd,
		},
		"content_digest":      string(item.ContentDigest),
		"verify_working_copy": false,
		"max_bytes":           8_192,
	})
	if err != nil {
		return uci.QueryItem{}, err
	}
	read, err := uciDecodeInstalledAcceptanceQuery(payload)
	if err != nil {
		return uci.QueryItem{}, err
	}
	if read.Status != uci.QueryStatusOK || !uciInstalledAcceptanceQueryMatchesPublication(read, publication) || read.Items == nil || len(*read.Items) != 1 {
		return uci.QueryItem{}, errors.New("installed scanner read did not return one exact Unicode source span")
	}
	readItem := (*read.Items)[0]
	if readItem.Ref != item.Ref || readItem.Path != item.Path || readItem.Span != item.Span || readItem.ContentDigest != item.ContentDigest || readItem.Excerpt != spanText {
		return uci.QueryItem{}, errors.New("installed scanner read did not preserve its exact Unicode CRLF citation")
	}
	return *item, nil
}

func uci1ObserveScannerNestedBoundaries(ctx context.Context, runtime uciInstalledAcceptanceScenarioRuntime, fixture *uci1ScannerProbeFixture) (string, bool, error) {
	if _, err := os.Lstat(fixture.agentRoot); err == nil {
		return "", false, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", false, err
	}
	agentPrivate := filepath.Join(fixture.agentRoot, "private.go")
	if err := os.MkdirAll(filepath.Dir(agentPrivate), 0o700); err != nil {
		return "", false, err
	}
	if err := os.WriteFile(agentPrivate, []byte("package private\n\nfunc UCI1ScannerPrivateSentinel() string { return \"private\" }\n"), 0o600); err != nil {
		return "", false, err
	}
	fixture.agentOwned = true
	fixture.dirty = true
	if err := fixture.stage(ctx, runtime, ".agent/private.go"); err != nil {
		return "", false, err
	}
	if err := os.WriteFile(filepath.Join(fixture.agentRoot, ".git"), []byte("gitdir: uci1-private\n"), 0o600); err != nil {
		return "", false, err
	}

	const submodulePath = ".uci1-scanner-submodule"
	if err := uciRunInstalledAcceptanceGit(ctx, runtime.Worktrees.primaryRoot, "update-index", "--add", "--cacheinfo", "160000,"+runtime.Worktrees.head+","+submodulePath); err != nil {
		return "", false, err
	}
	fixture.dirty = true

	publication, err := uci1ScannerIndex(ctx, runtime)
	if err != nil {
		return "", false, err
	}
	memberships, err := uci1ScannerMemberships(ctx, runtime, publication)
	if err != nil {
		return "", false, err
	}
	if err := uci1ScannerRequireMembership(memberships, ".agent/private.go", "excluded", false); err != nil {
		return "", false, err
	}
	if err := uci1ScannerRequireMembership(memberships, submodulePath, "excluded", false); err != nil {
		return "", false, err
	}
	if err := uci1ScannerRequireSearchAbsent(ctx, runtime, publication, "UCI1ScannerPrivateSentinel", ".agent"); err != nil {
		return "", false, err
	}
	return strings.Join([]string{publication.viewID, ".agent/private.go", submodulePath}, "\x00"), true, nil
}
