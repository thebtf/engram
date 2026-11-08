package gorm

import "time"

type issueAttribution struct {
	Session string
	Source  string
	Client  string
}

func selectIssueAttribution(transport, reported, client string) issueAttribution {
	var result issueAttribution
	if transport != "" && ValidIssueSessionSelector(transport) {
		result.Session, result.Source = transport, "transport"
	} else if reported != "" && ValidIssueSessionSelector(reported) {
		result.Session, result.Source = reported, "reported"
	}
	switch client {
	case "omp", "openclaw", "claude-code", "codex", "cursor", "gemini-cli", "human":
		result.Client = client
	}
	return result
}

const issueParticipantSessionPredicate = `(issues.created_by_session = ? OR EXISTS (SELECT 1 FROM issue_comments c WHERE c.issue_id = issues.id AND c.author_session = ?) OR EXISTS (SELECT 1 FROM issue_history_events e WHERE e.issue_id = issues.id AND e.author_session = ?) OR EXISTS (SELECT 1 FROM issue_changes ch WHERE ch.issue_id = issues.id AND ch.attribution_session = ?))`

// IssueHistoryEvent is an ID-ordered lifecycle event; material revision history
// remains in issue_changes. Empty attribution means unknown, not an actor.
type IssueHistoryEvent struct {
	ID            int64     `gorm:"primaryKey;autoIncrement" json:"id"`
	IssueID       int64     `gorm:"not null" json:"issue_id"`
	Status        string    `gorm:"type:text;not null" json:"status"`
	AuthorProject string    `gorm:"type:text;not null" json:"author_project"`
	AuthorAgent   string    `gorm:"type:text;not null" json:"author_agent"`
	SessionID     string    `gorm:"column:author_session;type:text;not null" json:"session_id"`
	SessionSource string    `gorm:"type:text;not null" json:"session_source"`
	Client        string    `gorm:"type:text;not null" json:"client"`
	CreatedAt     time.Time `gorm:"type:timestamptz;not null" json:"created_at"`
}

func (IssueHistoryEvent) TableName() string { return "issue_history_events" }
