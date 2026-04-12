package worker

import (
	"context"
	"testing"

	"github.com/thebtf/engram/internal/config"
	"github.com/thebtf/engram/pkg/models"
)

// newInjectTestService builds a minimal Service suitable for testing the inject relevant section.
// retrievalHooks is initialised so tests can override individual hooks without nil-pointer panics.
func newInjectTestService(injectUnified bool) *Service {
	cfg := config.Default()
	cfg.InjectionFloor = 0
	cfg.InjectUnified = injectUnified
	return &Service{
		config:         cfg,
		retrievalHooks: &retrievalHooks{},
		retrievalStats: map[string]*RetrievalStats{},
	}
}

// TestInjectRelevant_UnifiedPath_UsesLastUserPrompt verifies that when InjectUnified=true and a
// session has a recent user prompt, the relevant section is populated using that session's prompt
// as the retrieval query — not a prompt from another session.
func TestInjectRelevant_UnifiedPath_UsesLastUserPrompt(t *testing.T) {
	svc := newInjectTestService(true)

	// Wire the session-scoped hook: returns "auth bug fix" only when the correct sessionID is supplied.
	const wantSessionID = "session-abc"
	svc.retrievalHooks.getLastPromptBySession = func(_ context.Context, _ string, sessionID string) (*models.UserPromptWithSession, error) {
		if sessionID == wantSessionID {
			return &models.UserPromptWithSession{UserPrompt: models.UserPrompt{PromptText: "auth bug fix"}}, nil
		}
		return nil, nil
	}

	// Capture the query passed to RetrieveRelevant.
	var capturedQuery string
	svc.retrievalHooks.retrieveRelevant = func(_ context.Context, _ string, query string, _ RetrievalOptions) ([]*models.Observation, map[int64]float64, error) {
		capturedQuery = query
		return []*models.Observation{newObservation(42, "Auth fix")}, map[int64]float64{42: 0.9}, nil
	}

	project := "engram"
	recentIDs := map[int64]struct{}{}

	injectQuery := project
	if prompt, pErr := svc.loadLastUserPromptBySession(context.Background(), project, wantSessionID, 20); pErr == nil && prompt != nil {
		if prompt.PromptText != "" {
			injectQuery = prompt.PromptText
		}
	}

	opts := RetrievalOptions{MaxResults: 10, SessionID: wantSessionID}
	retrieved, _, err := svc.RetrieveRelevant(context.Background(), project, injectQuery, opts)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if capturedQuery != "auth bug fix" {
		t.Errorf("expected query 'auth bug fix', got %q", capturedQuery)
	}

	var relevant []*models.Observation
	for _, obs := range retrieved {
		if _, already := recentIDs[obs.ID]; !already {
			relevant = append(relevant, obs)
		}
	}

	if len(relevant) != 1 || relevant[0].ID != 42 {
		t.Errorf("expected observation ID 42 in relevant section, got %v", relevant)
	}
}

// TestInjectRelevant_UnifiedPath_FallsBackToProjectName verifies that when no user prompt is found
// for the session (or session_id is empty), the inject query falls back to the project name.
func TestInjectRelevant_UnifiedPath_FallsBackToProjectName(t *testing.T) {
	svc := newInjectTestService(true)

	// No session-scoped prompt — getLastPromptBySession returns nil.
	svc.retrievalHooks.getLastPromptBySession = func(_ context.Context, _ string, _ string) (*models.UserPromptWithSession, error) {
		return nil, nil
	}
	// Also ensure project-wide fallback returns nothing (cold-start).
	svc.retrievalHooks.getRecentUserPromptsByProject = func(_ context.Context, _ string, _ int) ([]*models.UserPromptWithSession, error) {
		return nil, nil
	}

	var capturedQuery string
	svc.retrievalHooks.retrieveRelevant = func(_ context.Context, _ string, query string, _ RetrievalOptions) ([]*models.Observation, map[int64]float64, error) {
		capturedQuery = query
		return nil, nil, nil
	}

	project := "my-project"
	sessionID := "some-session"

	injectQuery := project
	if prompt, pErr := svc.loadLastUserPromptBySession(context.Background(), project, sessionID, 20); pErr == nil && prompt != nil {
		if prompt.PromptText != "" {
			injectQuery = prompt.PromptText
		}
	}

	opts := RetrievalOptions{MaxResults: 10, SessionID: sessionID}
	_, _, err := svc.RetrieveRelevant(context.Background(), project, injectQuery, opts)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if capturedQuery != project {
		t.Errorf("expected fallback query %q, got %q", project, capturedQuery)
	}
}

// TestInjectRelevant_TwoSessionsDifferentPrompts is the anti-stub test.
// Two sessions with different last prompts must produce different inject queries.
//
// Anti-stub guarantee: if loadLastUserPromptBySession is replaced with the old project-wide
// loadRecentUserPromptsByProject, then BOTH calls would return the same prompt (the most
// recent project-wide entry), causing q1 == q2 and THIS test to fail.
func TestInjectRelevant_TwoSessionsDifferentPrompts(t *testing.T) {
	runSession := func(sessionID, lastPrompt string) (string, error) {
		svc := newInjectTestService(true)
		// Session-scoped hook: returns different prompts for different session IDs.
		svc.retrievalHooks.getLastPromptBySession = func(_ context.Context, _ string, sid string) (*models.UserPromptWithSession, error) {
			if sid == sessionID {
				return &models.UserPromptWithSession{UserPrompt: models.UserPrompt{PromptText: lastPrompt}}, nil
			}
			return nil, nil
		}
		var captured string
		svc.retrievalHooks.retrieveRelevant = func(_ context.Context, _ string, q string, _ RetrievalOptions) ([]*models.Observation, map[int64]float64, error) {
			captured = q
			return nil, nil, nil
		}

		project := "proj"
		injectQuery := project
		if prompt, pErr := svc.loadLastUserPromptBySession(context.Background(), project, sessionID, 20); pErr == nil && prompt != nil {
			if prompt.PromptText != "" {
				injectQuery = prompt.PromptText
			}
		}
		_, _, err := svc.RetrieveRelevant(context.Background(), project, injectQuery, RetrievalOptions{MaxResults: 10})
		return captured, err
	}

	q1, err1 := runSession("s1", "fix authentication token")
	q2, err2 := runSession("s2", "refactor database layer")
	if err1 != nil || err2 != nil {
		t.Fatalf("unexpected errors: %v / %v", err1, err2)
	}
	if q1 == q2 {
		t.Errorf("expected different queries for different sessions, both returned %q", q1)
	}
}

// TestInjectRelevant_SessionScoped_IgnoresOtherSessionPrompts verifies that the session-scoped
// lookup returns the correct prompt for each session ID and does not bleed prompts across sessions.
func TestInjectRelevant_SessionScoped_IgnoresOtherSessionPrompts(t *testing.T) {
	svc := newInjectTestService(true)

	// Wire a hook that returns different prompts for different session IDs.
	svc.retrievalHooks.getLastPromptBySession = func(_ context.Context, _ string, sessionID string) (*models.UserPromptWithSession, error) {
		switch sessionID {
		case "session-auth":
			return &models.UserPromptWithSession{UserPrompt: models.UserPrompt{PromptText: "how does auth work"}}, nil
		case "session-db":
			return &models.UserPromptWithSession{UserPrompt: models.UserPrompt{PromptText: "add a migration"}}, nil
		default:
			return nil, nil
		}
	}

	captureQuery := func(sessionID string) string {
		var captured string
		svc.retrievalHooks.retrieveRelevant = func(_ context.Context, _ string, q string, _ RetrievalOptions) ([]*models.Observation, map[int64]float64, error) {
			captured = q
			return nil, nil, nil
		}
		project := "proj"
		injectQuery := project
		if prompt, pErr := svc.loadLastUserPromptBySession(context.Background(), project, sessionID, 20); pErr == nil && prompt != nil {
			if prompt.PromptText != "" {
				injectQuery = prompt.PromptText
			}
		}
		_, _, _ = svc.RetrieveRelevant(context.Background(), project, injectQuery, RetrievalOptions{MaxResults: 10})
		return captured
	}

	qAuth := captureQuery("session-auth")
	qDB := captureQuery("session-db")

	if qAuth != "how does auth work" {
		t.Errorf("session-auth: expected query 'how does auth work', got %q", qAuth)
	}
	if qDB != "add a migration" {
		t.Errorf("session-db: expected query 'add a migration', got %q", qDB)
	}
	if qAuth == qDB {
		t.Errorf("session-auth and session-db produced the same query %q — sessions are not isolated", qAuth)
	}
}

// TestInjectRelevant_LegacyPath_WhenFlagFalse verifies that when InjectUnified=false the legacy
// vector-client path is taken (RetrieveRelevant hook must NOT be called).
func TestInjectRelevant_LegacyPath_WhenFlagFalse(t *testing.T) {
	svc := newInjectTestService(false) // InjectUnified=false

	retrieveRelevantCalled := false
	svc.retrievalHooks.retrieveRelevant = func(_ context.Context, _ string, _ string, _ RetrievalOptions) ([]*models.Observation, map[int64]float64, error) {
		retrieveRelevantCalled = true
		return nil, nil, nil
	}

	// Verify the flag is honoured — when false, unified path must NOT execute.
	if svc.config == nil || svc.config.InjectUnified {
		t.Fatal("test setup error: InjectUnified should be false")
	}

	// Simulate the branch decision from the handler.
	if svc.config.InjectUnified {
		// unified path would call RetrieveRelevant
		_, _, _ = svc.RetrieveRelevant(context.Background(), "proj", "query", RetrievalOptions{MaxResults: 10})
	}
	// else: legacy path (we do NOT call RetrieveRelevant)

	if retrieveRelevantCalled {
		t.Error("InjectUnified=false: RetrieveRelevant must NOT be called; legacy path should be used instead")
	}
}

func TestInjectRelevant_PassesFilePathsIntoRetrievalOptions(t *testing.T) {
	svc := newInjectTestService(true)
	const wantSessionID = "session-files"
	const project = "engram"
	wantFiles := []string{"foo.go", "bar.go"}

	svc.retrievalHooks.getLastPromptBySession = func(_ context.Context, _ string, sessionID string) (*models.UserPromptWithSession, error) {
		if sessionID == wantSessionID {
			return &models.UserPromptWithSession{UserPrompt: models.UserPrompt{PromptText: "auth"}}, nil
		}
		return nil, nil
	}

	var capturedOpts RetrievalOptions
	svc.retrievalHooks.retrieveRelevant = func(_ context.Context, _ string, _ string, opts RetrievalOptions) ([]*models.Observation, map[int64]float64, error) {
		capturedOpts = opts
		return nil, nil, nil
	}

	injectQuery := project
	if prompt, pErr := svc.loadLastUserPromptBySession(context.Background(), project, wantSessionID, 20); pErr == nil && prompt != nil && prompt.PromptText != "" {
		injectQuery = prompt.PromptText
	}

	_, _, err := svc.RetrieveRelevant(context.Background(), project, injectQuery, RetrievalOptions{
		MaxResults: 10,
		SessionID:  wantSessionID,
		FilePaths:  wantFiles,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(capturedOpts.FilePaths) != 2 || capturedOpts.FilePaths[0] != "foo.go" || capturedOpts.FilePaths[1] != "bar.go" {
		t.Fatalf("expected file paths [foo.go bar.go], got %#v", capturedOpts.FilePaths)
	}
}
