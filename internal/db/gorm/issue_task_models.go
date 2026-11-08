package gorm

import (
	"encoding/json"
	"time"

	"github.com/thebtf/engram/pkg/models"
)

// IssueActor is a non-secret attribution projection, never an authorization grant.
// Historical rows remain unknown; credentials and internal receipt keys are not
// part of this projection.
type IssueActor struct {
	Kind      string  `gorm:"type:text;not null;default:unknown" json:"kind"`
	Reference *string `gorm:"type:text" json:"reference"`
}

// IssueChangeAttribution retains submitted provenance separately from the actor.
type IssueChangeAttribution struct {
	SourceProject *string `gorm:"type:text" json:"source_project"`
	SourceAgent   *string `gorm:"type:text" json:"source_agent"`
	Session       *string `gorm:"type:text" json:"session"`
}

// IssueChange is append-only evidence of a committed material revision. The
// historical issue ID survives explicit card deletion; no cascading foreign key
// may discard this evidence. The application must lock the live issue on insert.
type IssueChange struct {
	IssueID          int64                  `gorm:"primaryKey;autoIncrement:false" json:"-"`
	Revision         int64                  `gorm:"primaryKey;autoIncrement:false" json:"revision,string"`
	PreviousRevision *int64                 `json:"previous_revision,string"`
	Action           string                 `gorm:"type:text;not null" json:"action"`
	ChangedFields    models.JSONStringArray `gorm:"type:jsonb;not null;default:'[]'" json:"changed_fields"`
	Actor            IssueActor             `gorm:"embedded;embeddedPrefix:actor_" json:"actor"`
	Attribution      IssueChangeAttribution `gorm:"embedded;embeddedPrefix:attribution_" json:"attribution"`
	Changes          json.RawMessage        `gorm:"type:jsonb;serializer:json;not null;default:'{}'" json:"changes"`
	CreatedAt        time.Time              `gorm:"not null;default:now()" json:"created_at"`
}

func (IssueChange) TableName() string { return "issue_changes" }

// issueCreateReceipt is private to IssueStore. A NULL IssueID is a permanent
// tombstone produced by the issue foreign key, not an available request key.
// Only the original payload's SHA-256 binding is retained, never its plaintext.
type issueCreateReceipt struct {
	ActorIdentityKey         string    `gorm:"primaryKey;type:text" json:"-"`
	CanonicalSourceProjectID string    `gorm:"primaryKey;type:text" json:"-"`
	RequestKey               string    `gorm:"primaryKey;type:text" json:"-"`
	IssueID                  *int64    `json:"-"`
	PayloadVersion           int16     `gorm:"not null" json:"-"`
	PayloadHash              []byte    `gorm:"type:bytea;not null" json:"-"`
	CreatedAt                time.Time `gorm:"not null;default:now()" json:"-"`
}

func (issueCreateReceipt) TableName() string { return "issue_create_receipts" }
