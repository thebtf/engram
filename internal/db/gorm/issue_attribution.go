package gorm

import (
	"context"
	"fmt"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// IssueAttribution is provenance only. Neither a transport nor a reported session
// authenticates a host or grants access to an issue or private session data.
type IssueAttribution struct {
	SessionID     string
	SessionSource string
	Client        string
}

const issueSessionMaxBytes = 256

// NormalizeIssueSession preserves bounded host identifiers, including opaque
// conversation IDs. Unsafe optional metadata remains unknown, never an error or
// an inferred project, keycard, browser session, or daemon identity.
func NormalizeIssueSession(value string) string {
	if value == "" || len(value) > issueSessionMaxBytes || !utf8.ValidString(value) || strings.TrimSpace(value) != value {
		return ""
	}
	for _, r := range value {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			return ""
		}
	}
	if id, err := uuid.Parse(value); err == nil && id == uuid.Nil {
		return ""
	}
	return value
}

// ValidateIssueSessionSelector never turns unsafe selection into a broader
// query. Empty is the existing omitted-filter convention; nonempty IDs are exact.
func ValidateIssueSessionSelector(value string) error {
	if value != "" && NormalizeIssueSession(value) == "" {
		return fmt.Errorf("%w: invalid issue session selector", ErrIssueInvalidInput)
	}
	return nil
}

// NewIssueAttribution accepts host conversation IDs and known public per-call
// client labels. Optional unknown metadata never blocks an authorized write.
// Transport means request provenance, not verified host ownership.
func NewIssueAttribution(transportSession, reportedSession, client string) (IssueAttribution, error) {
	var attribution IssueAttribution
	switch client {
	case "omp", "openclaw", "claude-code", "codex", "cursor", "gemini-cli", "human":
		attribution.Client = client
	}
	if session := NormalizeIssueSession(transportSession); session != "" {
		attribution.SessionID = session
		attribution.SessionSource = "transport"
	} else if session := NormalizeIssueSession(reportedSession); session != "" {
		attribution.SessionID = session
		attribution.SessionSource = "reported"
	}
	return attribution, nil
}

// IssueHistoryEvent records an authored lifecycle action without inventing a
// user comment. Historic closed rows have no inferred verification event.
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

const (
	IssueHistoryDefaultLimit = 50
	IssueHistoryMaxLimit     = 100
)

// IssueHistoryPage is an ID-ordered bounded page; its cursor selects rows only.
type IssueHistoryPage struct {
	Events      []IssueHistoryEvent
	HasMore     bool
	NextAfterID *int64
}

// GetIssueHistoryPage reads lifecycle provenance after the caller has authorized
// the parent issue. Appends can appear on later pages; no snapshot is implied.
func (s *IssueStore) GetIssueHistoryPage(ctx context.Context, id int64, limit int, afterID int64) (IssueHistoryPage, error) {
	if limit < 1 || limit > IssueHistoryMaxLimit || afterID < 0 {
		return IssueHistoryPage{}, fmt.Errorf("%w: invalid issue history pagination", ErrIssueInvalidInput)
	}
	events := make([]IssueHistoryEvent, 0)
	if err := s.db.WithContext(ctx).Where("issue_id = ? AND id > ?", id, afterID).
		Order("id ASC").Limit(limit + 1).Find(&events).Error; err != nil {
		return IssueHistoryPage{}, fmt.Errorf("read issue history: %w", err)
	}
	page := IssueHistoryPage{Events: events}
	if len(events) > limit {
		page.Events = events[:limit]
		page.HasMore = true
		next := events[limit-1].ID
		page.NextAfterID = &next
	}
	return page, nil
}

func addIssueHistoryTx(tx *gorm.DB, id int64, status, authorProject, authorAgent string, attribution IssueAttribution, now time.Time) error {
	return tx.Create(&IssueHistoryEvent{
		IssueID: id, Status: status, AuthorProject: authorProject, AuthorAgent: authorAgent,
		SessionID: attribution.SessionID, SessionSource: attribution.SessionSource,
		Client: attribution.Client, CreatedAt: now,
	}).Error
}

func issueUpdateComment(update IssueUpdate) *IssueComment {
	return &IssueComment{
		AuthorProject: update.AuthorProject, AuthorAgent: update.AuthorAgent, Body: update.Comment,
		AuthorSession: update.Attribution.SessionID, AuthorSessionSource: update.Attribution.SessionSource,
		AuthorClient: update.Attribution.Client,
	}
}
