package mcp

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/thebtf/engram/internal/auth"
	gormdb "github.com/thebtf/engram/internal/db/gorm"
)

func issueAttributionProject(t *testing.T, store *gormdb.Store) string {
	t.Helper()
	project := uniqueIssueToolProject(t, "session-attribution")
	t.Cleanup(func() {
		assert.NoError(t, store.GetDB().Exec(`DELETE FROM issue_comments WHERE issue_id IN (SELECT id FROM issues WHERE source_project = ? AND target_project = ?)`, project, project).Error)
		assert.NoError(t, store.GetDB().Exec(`DELETE FROM issues WHERE source_project = ? AND target_project = ?`, project, project).Error)
	})
	return project
}

func issueAttributionServer(store *gormdb.Store) (*Server, *gormdb.IssueStore) {
	issueStore := gormdb.NewIssueStore(store.GetDB())
	server := NewServer(ServerOptions{})
	server.SetIssueStore(issueStore)
	return server, issueStore
}

func createAttributedIssue(t *testing.T, server *Server, issueStore *gormdb.IssueStore, ctx context.Context, project, title, agent string) int64 {
	t.Helper()
	args := map[string]any{
		"action": "create", "title": title, "body": "User-authored issue body stays verbatim.",
		"target_project": project,
	}
	if agent != "" {
		args["agent_source"] = agent
	}
	_, err := callIssueAction(t, server, ctx, args)
	require.NoError(t, err)
	issues, _, err := issueStore.ListIssuesEx(context.Background(), gormdb.IssueListParams{
		SourceProject: project, TargetProject: project, Limit: 10,
	})
	require.NoError(t, err)
	for _, issue := range issues {
		if issue.Title == title {
			return issue.ID
		}
	}
	t.Fatalf("created issue %q was not persisted for project %s", title, project)
	return 0
}

func TestIssueSessionAttributionCreatePersistsHostUUIDs(t *testing.T) {
	for _, tc := range []struct {
		name  string
		agent string
	}{
		{"explicit_client", "omp"},
		{"unknown_client", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store, issueStore := openIssueToolTestDB(t)
			project := issueAttributionProject(t, store)
			server := NewServer(ServerOptions{})
			server.SetIssueStore(issueStore)
			base := issueClientContext(project, "same-creator-keycard", auth.RoleReadWrite)
			sessions := []string{uuid.NewString(), uuid.NewString()}
			ids := make([]int64, len(sessions))
			for i, session := range sessions {
				ids[i] = createAttributedIssue(t, server, issueStore, ContextWithSession(base, session), project, "creation from "+[]string{"first", "second"}[i]+" session", tc.agent)
			}

			reloaded, persisted := issueAttributionServer(store)
			for i, id := range ids {
				issue, comments, err := persisted.GetIssue(context.Background(), id)
				require.NoError(t, err)
				assert.Equal(t, sessions[i], issue.CreatedBySession, "creation must retain the contextual host UUID, not the shared project or keycard")
				assert.Equal(t, project, issue.SourceProject)
				assert.Equal(t, "same-creator-keycard", issue.CreatorKeycardID)
				assert.Equal(t, tc.agent, issue.SourceAgent, "an omitted client label is unknown, not claude-code")
				assert.Equal(t, "User-authored issue body stays verbatim.", issue.Body)
				assert.Empty(t, comments)
				readback, err := callIssueAction(t, reloaded, base, map[string]any{"action": "get", "id": id})
				require.NoError(t, err)
				assert.Contains(t, readback, sessions[i])
				assert.NotContains(t, readback, sessions[1-i], "a different session on the same client must not replace the creator")
			}
		})
	}
}

func TestIssueSessionAttributionCommentsSurviveServerReload(t *testing.T) {
	for _, tc := range []struct {
		name  string
		agent string
	}{
		{"explicit_client", "omp"},
		{"unknown_client", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store, issueStore := openIssueToolTestDB(t)
			project := uniqueIssueToolProject(t, "comment-attribution")
			id := createIssueToolFixture(t, store, issueStore, project, "same-comment-keycard", "open")
			base := issueClientContext(project, "same-comment-keycard", auth.RoleReadWrite)
			sessions := []string{uuid.NewString(), uuid.NewString()}
			bodies := []string{"First user comment stays verbatim.", "Second user comment stays verbatim."}

			for i, session := range sessions {
				writer, _ := issueAttributionServer(store)
				args := map[string]any{"action": "comment", "id": id, "body": bodies[i]}
				if tc.agent != "" {
					args["agent_source"] = tc.agent
				}
				_, err := callIssueAction(t, writer, ContextWithSession(base, session), args)
				require.NoError(t, err)

				reloaded, persisted := issueAttributionServer(store)
				issue, comments, err := persisted.GetIssue(context.Background(), id)
				require.NoError(t, err)
				assert.Empty(t, issue.CreatedBySession, "new comments must not backfill a historic issue's unknown creator")
				require.Len(t, comments, i+1)
				actualBodies := make([]string, 0, len(comments))
				for _, comment := range comments {
					actualBodies = append(actualBodies, comment.Body)
					assert.Equal(t, project, comment.AuthorProject)
					assert.Equal(t, tc.agent, comment.AuthorAgent, "session attribution must not overwrite the explicit label or fabricate a missing one")
				}
				assert.ElementsMatch(t, bodies[:i+1], actualBodies)

				readback, err := callIssueAction(t, reloaded, base, map[string]any{"action": "get", "id": id})
				require.NoError(t, err)
				for j := 0; j <= i; j++ {
					assert.Contains(t, readback, bodies[j])
					assert.Contains(t, readback, sessions[j], "get must expose the persisted comment author's actual UUID after server replacement")
				}
				if i == 0 {
					assert.NotContains(t, readback, sessions[1])
				}
			}
		})
	}
}

func TestIssueSessionAttributionMissingSessionRemainsUnknown(t *testing.T) {
	for _, name := range []string{"absent", "explicitly_empty"} {
		t.Run(name, func(t *testing.T) {
			store, issueStore := openIssueToolTestDB(t)
			project := issueAttributionProject(t, store)
			base := issueClientContext(project, "known-authentication-keycard", auth.RoleReadWrite)
			ctx := base
			if name == "explicitly_empty" {
				ctx = ContextWithSession(base, "")
			}
			writer := NewServer(ServerOptions{})
			writer.SetIssueStore(issueStore)
			id := createAttributedIssue(t, writer, issueStore, ctx, project, "unknown host session", "omp")
			_, err := callIssueAction(t, writer, ctx, map[string]any{
				"action": "comment", "id": id, "body": "Comment with no known session.",
			})
			require.NoError(t, err)

			reloaded, persisted := issueAttributionServer(store)
			issue, comments, err := persisted.GetIssue(context.Background(), id)
			require.NoError(t, err)
			assert.Empty(t, issue.CreatedBySession, "missing session must not become the project, keycard, or a daemon-generated UUID")
			assert.Equal(t, "omp", issue.SourceAgent)
			require.Len(t, comments, 1)
			assert.Empty(t, comments[0].AuthorAgent, "missing comment client must not become claude-code")
			assert.Equal(t, "Comment with no known session.", comments[0].Body)
			readerSession := uuid.NewString()
			readback, err := callIssueAction(t, reloaded, ContextWithSession(base, readerSession), map[string]any{"action": "get", "id": id})
			require.NoError(t, err)
			assert.Contains(t, readback, comments[0].Body)
			assert.NotContains(t, readback, readerSession, "a read must not attribute historic unknown events to the reader")
		})
	}
}

func TestIssueSessionAttributionPreservesHistoricalData(t *testing.T) {
	store, issueStore := openIssueToolTestDB(t)
	project := issueAttributionProject(t, store)
	id, err := issueStore.CreateIssue(context.Background(), &gormdb.Issue{
		Title: "Historic issue", Body: "Historic user input.", SourceProject: project, TargetProject: project,
		SourceAgent: "historic-client-label", CreatedBySession: "historic-session-before-host-uuid", CreatorKeycardID: "historic-keycard",
	})
	require.NoError(t, err)
	_, err = issueStore.AddComment(context.Background(), id, &gormdb.IssueComment{
		AuthorProject: project, AuthorAgent: "historic-comment-client", Body: "Historic comment input.",
	})
	require.NoError(t, err)
	before, history, err := issueStore.GetIssue(context.Background(), id)
	require.NoError(t, err)
	require.Len(t, history, 1)

	writer, _ := issueAttributionServer(store)
	session := uuid.NewString()
	ctx := ContextWithSession(issueClientContext(project, "historic-keycard", auth.RoleReadWrite), session)
	_, err = callIssueAction(t, writer, ctx, map[string]any{
		"action": "comment", "id": id, "agent_source": "omp", "body": "New user input stays verbatim.",
	})
	require.NoError(t, err)

	reloaded, persisted := issueAttributionServer(store)
	after, comments, err := persisted.GetIssue(context.Background(), id)
	require.NoError(t, err)
	before.UpdatedAt = after.UpdatedAt
	assert.Equal(t, before, after, "adding attributed history may update the timestamp, not rewrite the historic issue")
	require.Len(t, comments, 2)
	assert.Contains(t, comments, history[0], "the old comment and its client label must remain byte-for-byte unchanged")
	for _, comment := range comments {
		if comment.ID != history[0].ID {
			assert.Equal(t, "omp", comment.AuthorAgent)
			assert.Equal(t, "New user input stays verbatim.", comment.Body)
		}
	}
	readback, err := callIssueAction(t, reloaded, issueClientContext(project, "historic-keycard", auth.RoleReadWrite), map[string]any{"action": "get", "id": id})
	require.NoError(t, err)
	assert.Contains(t, readback, history[0].Body)
	assert.Contains(t, readback, history[0].AuthorAgent)
	assert.Contains(t, readback, "New user input stays verbatim.")
	assert.Contains(t, readback, session, "only the new history entry has the newly supplied host session")
}

func TestIssueSessionAttributionReportedFallbackAndTransportPrecedence(t *testing.T) {
	for _, tc := range []struct {
		name           string
		emptyTransport bool
		useTransport   bool
	}{
		{name: "reported_fallback"},
		{name: "empty_transport_uses_reported", emptyTransport: true},
		{name: "actual_transport_wins", useTransport: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store, issueStore := openIssueToolTestDB(t)
			project := issueAttributionProject(t, store)
			base := issueClientContext(project, "same-reported-attribution-keycard", auth.RoleReadWrite)
			reportedCreate, reportedComment := uuid.NewString(), uuid.NewString()
			actualCreate, actualComment := uuid.NewString(), uuid.NewString()
			createSession, commentSession := reportedCreate, reportedComment
			createContext, commentContext := base, base
			if tc.emptyTransport {
				createContext = ContextWithSession(base, "")
				commentContext = ContextWithSession(base, "")
			}
			if tc.useTransport {
				createContext = ContextWithSession(base, actualCreate)
				commentContext = ContextWithSession(base, actualComment)
				createSession, commentSession = actualCreate, actualComment
			}
			writer := NewServer(ServerOptions{})
			writer.SetIssueStore(issueStore)
			_, err := callIssueAction(t, writer, createContext, map[string]any{
				"action": "create", "title": "Caller-reported attribution", "target_project": project,
				"body": "Explicit issue input remains unchanged.", "agent_source": "triage-agent",
				"session_id": reportedCreate, "client": "omp",
			})
			require.NoError(t, err)
			issues, _, err := issueStore.ListIssuesEx(context.Background(), gormdb.IssueListParams{
				SourceProject: project, TargetProject: project, Limit: 10,
			})
			require.NoError(t, err)
			require.Len(t, issues, 1)
			id := issues[0].ID

			reloaded, persisted := issueAttributionServer(store)
			issue, comments, err := persisted.GetIssue(context.Background(), id)
			require.NoError(t, err)
			assert.Equal(t, createSession, issue.CreatedBySession)
			assert.Equal(t, "triage-agent", issue.SourceAgent, "client and agent are distinct attribution values")
			assert.Equal(t, "same-reported-attribution-keycard", issue.CreatorKeycardID)
			assert.Equal(t, project, issue.SourceProject)
			assert.Equal(t, "Explicit issue input remains unchanged.", issue.Body)
			assert.Empty(t, comments)
			readback, err := callIssueAction(t, reloaded, base, map[string]any{"action": "get", "id": id})
			require.NoError(t, err)
			assert.Contains(t, readback, createSession)
			assert.Contains(t, readback, "omp")
			assert.Contains(t, readback, "triage-agent")
			if tc.useTransport {
				assert.NotContains(t, readback, reportedCreate, "conflicting reported input must not replace actual transport attribution")
			}

			_, err = callIssueAction(t, reloaded, commentContext, map[string]any{
				"action": "comment", "id": id, "body": "Explicit comment input remains unchanged.",
				"agent_source": "review-agent", "session_id": reportedComment, "client": "openclaw",
			})
			require.NoError(t, err)
			afterReload, stored := issueAttributionServer(store)
			issue, comments, err = stored.GetIssue(context.Background(), id)
			require.NoError(t, err)
			assert.Equal(t, createSession, issue.CreatedBySession, "a later caller must not replace creation attribution")
			assert.Equal(t, "triage-agent", issue.SourceAgent)
			require.Len(t, comments, 1)
			assert.Equal(t, "review-agent", comments[0].AuthorAgent)
			assert.Equal(t, project, comments[0].AuthorProject)
			assert.Equal(t, "Explicit comment input remains unchanged.", comments[0].Body)
			readback, err = callIssueAction(t, afterReload, base, map[string]any{"action": "get", "id": id})
			require.NoError(t, err)
			for _, value := range []string{createSession, commentSession, "omp", "openclaw", "triage-agent", "review-agent", comments[0].Body} {
				assert.Contains(t, readback, value, "both events must retain their own session, client, and agent after reload")
			}
			if tc.useTransport {
				assert.NotContains(t, readback, reportedCreate)
				assert.NotContains(t, readback, reportedComment)
			}
		})
	}
}

func TestIssueSessionAttributionLifecycleWithoutUserCommentSurvivesReload(t *testing.T) {
	store, issueStore := openIssueToolTestDB(t)
	project := uniqueIssueToolProject(t, "lifecycle-session-attribution")
	id := createIssueToolFixture(t, store, issueStore, project, "same-lifecycle-keycard", "open")
	_, err := issueStore.AddComment(context.Background(), id, &gormdb.IssueComment{
		AuthorProject: project, AuthorAgent: "historic-user-agent", Body: "Existing user comment must not be rewritten or supplemented.",
	})
	require.NoError(t, err)
	before, userComments, err := issueStore.GetIssue(context.Background(), id)
	require.NoError(t, err)
	require.Len(t, userComments, 1)
	base := issueClientContext(project, "same-lifecycle-keycard", auth.RoleReadWrite)
	sessions := []string{uuid.NewString(), uuid.NewString()}
	reported := []string{uuid.NewString(), uuid.NewString()}
	clients := []string{"omp", "openclaw"}
	agents := []string{"resolution-agent", "verification-agent"}
	for i, action := range []string{"update", "close"} {
		writer, _ := issueAttributionServer(store)
		args := map[string]any{
			"action": action, "id": id, "session_id": reported[i], "client": clients[i], "agent_source": agents[i],
		}
		if action == "update" {
			args["status"] = "resolved"
		}
		_, err := callIssueAction(t, writer, ContextWithSession(base, sessions[i]), args)
		require.NoError(t, err)

		reloaded, persisted := issueAttributionServer(store)
		after, comments, err := persisted.GetIssue(context.Background(), id)
		require.NoError(t, err)
		assert.Equal(t, []string{"resolved", "closed"}[i], after.Status)
		assert.Equal(t, before.CreatedBySession, after.CreatedBySession)
		assert.Equal(t, before.SourceAgent, after.SourceAgent)
		assert.Equal(t, before.CreatorKeycardID, after.CreatorKeycardID)
		assert.Equal(t, before.SourceProject, after.SourceProject)
		assert.Equal(t, before.TargetProject, after.TargetProject)
		assert.Equal(t, before.Title, after.Title)
		assert.Equal(t, before.Body, after.Body)
		assert.Equal(t, before.CreatedAt, after.CreatedAt)
		assert.Equal(t, userComments, comments, "lifecycle attribution belongs to structured history, not fabricated or rewritten user comments")
		readback, err := callIssueAction(t, reloaded, base, map[string]any{"action": "get", "id": id})
		require.NoError(t, err)
		assert.Contains(t, readback, userComments[0].Body)
		assert.Contains(t, readback, userComments[0].AuthorAgent)
		for j := 0; j <= i; j++ {
			assert.Contains(t, readback, sessions[j], "a comment-free lifecycle event must remain attributable after server replacement")
			assert.Contains(t, readback, clients[j])
			assert.Contains(t, readback, agents[j])
			assert.NotContains(t, readback, reported[j], "actual per-call transport session must win for lifecycle events too")
		}
		if i == 0 {
			assert.NotContains(t, readback, sessions[1], "future lifecycle attribution must not appear in current history")
		}
	}
}
