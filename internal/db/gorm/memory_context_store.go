package gorm

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/thebtf/engram/internal/projectidentity"
	"github.com/thebtf/engram/internal/scope"
	"github.com/thebtf/engram/pkg/models"
	gormlib "gorm.io/gorm"
	"gorm.io/gorm/clause"
	"gorm.io/gorm/logger"
)

var (
	ErrMemoryPrincipalRequired   = errors.New("an enrolled canonical keycard principal is required")
	ErrMemoryRequestConflict     = errors.New("request_key already identifies a different memory command")
	ErrMemoryRequestRetired      = errors.New("request_key identifies a retired or missing memory")
	ErrMemoryRequestForbidden    = errors.New("request_key memory is not accessible")
	ErrMemoryEnrollmentForbidden = errors.New("a current persisted browser-admin session is required")
	ErrMemoryCreateDenied        = errors.New("memory create denied")
)

type memoryPrincipal struct {
	PrincipalKind string `gorm:"primaryKey"`
	Principal     string `gorm:"primaryKey"`
	EnrolledBy    int64
	CreatedAt     time.Time
}

func (memoryPrincipal) TableName() string { return "memory_principals" }

type memoryRequestKey struct {
	PrincipalKind string
	Principal     string
	ContextKind   string
	Project       string
	RequestKey    string
	RequestDigest string
	MemoryID      *int64
}

func (memoryRequestKey) TableName() string { return "memory_request_keys" }

// EnrollPrincipal binds an existing canonical pair, not a user or credential.
// Revalidate the authenticated persisted session under locks held through insert.
func (s *MemoryStore) EnrollPrincipal(ctx context.Context, kind, principal string, userID int64, sessionID string) (bool, error) {
	if principal == "" || principal != strings.TrimSpace(principal) || !isValidMemoryOwnerPrincipalKind(kind) {
		return false, ErrMemoryPrincipalRequired
	}
	created := false
	err := s.db.WithContext(ctx).Transaction(func(tx *gormlib.DB) error {
		var user User
		if err := tx.Clauses(clause.Locking{Strength: "SHARE"}).Where("id = ?", userID).Take(&user).Error; err != nil {
			if errors.Is(err, gormlib.ErrRecordNotFound) {
				return ErrMemoryEnrollmentForbidden
			}
			return err
		}
		if user.Disabled || user.Role != DashboardRoleAdmin {
			return ErrMemoryEnrollmentForbidden
		}
		var session AuthSession
		if err := tx.Session(&gormlib.Session{Logger: logger.Discard}).Clauses(clause.Locking{Strength: "SHARE"}).Where("id = ? AND user_id = ?", sessionID, userID).Take(&session).Error; err != nil {
			if errors.Is(err, gormlib.ErrRecordNotFound) {
				return ErrMemoryEnrollmentForbidden
			}
			return err
		}
		if _, err := validateSessionRow(&session); err != nil {
			return ErrMemoryEnrollmentForbidden
		}
		row := memoryPrincipal{PrincipalKind: kind, Principal: principal, EnrolledBy: user.ID}
		result := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&row)
		created = result.RowsAffected == 1
		return result.Error
	})
	return created, err
}

func (s *MemoryStore) PrincipalEnrolled(ctx context.Context, kind, principal string) (bool, error) {
	return memoryPrincipalEnrolled(ctx, s.db, kind, principal)
}

func (s *MemoryStore) CanonicalProjectExists(ctx context.Context, project string) (bool, error) {
	return (&SessionStore{db: s.db}).CanonicalProjectExists(ctx, project)
}

// CreateForExistingProject preserves unbound legacy REST writers, without the
// lower-level legacy writer's first-write project registration side effect.
func (s *MemoryStore) CreateForExistingProject(ctx context.Context, mem *models.Memory) (*models.Memory, error) {
	if err := validateMemoryForCreate(mem); err != nil {
		return nil, err
	}
	if !projectidentity.ValidProjectID(mem.Project) || mem.Context != nil {
		return nil, fmt.Errorf("an existing canonical project is required")
	}
	var created *models.Memory
	err := s.db.WithContext(ctx).Transaction(func(tx *gormlib.DB) error {
		var project Project
		if err := liveKnowledgeProjects(tx).Clauses(clause.Locking{Strength: "SHARE"}).Where("id = ?", mem.Project).Take(&project).Error; err != nil {
			return err
		}
		row := memoryRowForCreate(mem, time.Now().UTC(), false)
		copyPrivacyFields(row, mem)
		copyPrincipalMemoryFields(row, mem)
		if err := tx.Create(row).Error; err != nil {
			return err
		}
		created = memoryRowToModel(row)
		return nil
	})
	return created, err
}

func memoryPrincipalEnrolled(ctx context.Context, db *gormlib.DB, kind, principal string) (bool, error) {
	var found bool
	err := db.WithContext(ctx).Raw(`SELECT EXISTS (SELECT 1 FROM memory_principals WHERE principal_kind = ? AND principal = ?)`, kind, principal).Scan(&found).Error
	return found, err
}

func memoryContextKind(mem *models.Memory) string {
	if mem.Context == nil {
		return models.MemoryContextLegacy
	}
	return mem.Context.Kind
}

func validateMemoryContext(mem *models.Memory) error {
	kind := memoryContextKind(mem)
	if kind == models.MemoryContextLegacy {
		if mem.Context != nil && (mem.Context.Principal != "" || mem.Context.PrincipalKind != "") {
			return fmt.Errorf("legacy context cannot carry an owner")
		}
		return nil
	}
	if kind != models.MemoryContextSelf && kind != models.MemoryContextProject && kind != models.MemoryContextGlobal {
		return fmt.Errorf("invalid owning context")
	}
	if kind == models.MemoryContextProject {
		if !projectidentity.ValidProjectID(mem.Project) {
			return fmt.Errorf("typed project context requires a canonical project")
		}
	} else if mem.Project != "" {
		return fmt.Errorf("self/global context must not carry a project")
	}
	if kind == models.MemoryContextSelf {
		if mem.Context.Principal == "" || mem.Context.Principal != strings.TrimSpace(mem.Context.Principal) || !isValidMemoryOwnerPrincipalKind(mem.Context.PrincipalKind) {
			return ErrMemoryPrincipalRequired
		}
		if mem.Context.Principal != mem.OwnerPrincipal || mem.Context.PrincipalKind != mem.OwnerPrincipalKind {
			return fmt.Errorf("self creator must be the canonical owner")
		}
	} else if mem.Context.Principal != "" || mem.Context.PrincipalKind != "" {
		return fmt.Errorf("only self context carries a principal")
	}
	if mem.OwnerPrincipal == "" || !isValidMemoryOwnerPrincipalKind(mem.OwnerPrincipalKind) {
		return ErrMemoryPrincipalRequired
	}
	return nil
}

func copyMemoryContext(row *Memory, mem *models.Memory) {
	row.ContextKind = memoryContextKind(mem)
	row.ContextPrincipal, row.ContextPrincipalKind = "", ""
	if row.ContextKind == models.MemoryContextSelf {
		row.ContextPrincipal, row.ContextPrincipalKind = mem.Context.Principal, mem.Context.PrincipalKind
	}
}

func restoreMemoryContext(mem *models.Memory) (string, string, string) {
	if mem.Context == nil {
		return models.MemoryContextLegacy, "", ""
	}
	return mem.Context.Kind, mem.Context.Principal, mem.Context.PrincipalKind
}

func attachMemoryContext(mem *models.Memory, row *Memory) {
	if row.ContextKind == "" || row.ContextKind == models.MemoryContextLegacy {
		return
	}
	mem.Context = &models.MemoryContext{Kind: row.ContextKind, Principal: row.ContextPrincipal, PrincipalKind: row.ContextPrincipalKind}
	mem.Author = &models.MemoryAuthor{Principal: row.OwnerPrincipal, PrincipalKind: row.OwnerPrincipalKind}
}

func createMemoryContextRowTx(ctx context.Context, tx *gormlib.DB, row *Memory) error {
	switch row.ContextKind {
	case "", models.MemoryContextLegacy:
		if err := ensureLogicalProjectForWrite(ctx, tx, row.Project); err != nil {
			return err
		}
	case models.MemoryContextProject:
		var project Project
		if err := liveKnowledgeProjects(tx.WithContext(ctx)).Clauses(clause.Locking{Strength: "SHARE"}).Where("id = ?", row.Project).Take(&project).Error; err != nil {
			return err
		}
	case models.MemoryContextSelf, models.MemoryContextGlobal:
		found, err := memoryPrincipalEnrolled(ctx, tx, row.OwnerPrincipalKind, row.OwnerPrincipal)
		if err != nil {
			return err
		}
		if !found {
			return ErrMemoryPrincipalRequired
		}
	default:
		return fmt.Errorf("invalid owning context")
	}
	return tx.WithContext(ctx).Create(row).Error
}

// GetInContext applies ownership and ordinary visibility before loading a body.
// Project reads retain the canonical membership/consolidation reader.
func (s *MemoryStore) GetInContext(ctx context.Context, id int64, owning models.MemoryContext, project string, caller scope.KeycardContext) (*models.Memory, error) {
	return memoryInContext(ctx, s.db, id, owning, project, caller, false)
}

func memoryInContext(ctx context.Context, db *gormlib.DB, id int64, owning models.MemoryContext, project string, caller scope.KeycardContext, lock bool) (*models.Memory, error) {
	var q *gormlib.DB
	var err error
	if owning.Kind == models.MemoryContextProject {
		q, err = canonicalProjectRows(ctx, db.Model(&Memory{}), project, "memories", "m")
		if err != nil {
			return nil, err
		}
	} else {
		if owning.Kind != models.MemoryContextSelf && owning.Kind != models.MemoryContextGlobal {
			return nil, ErrMemoryRequestForbidden
		}
		q = db.WithContext(ctx).Model(&Memory{}).Table("memories AS m").Where("m.context_kind = ? AND m.project = ''", owning.Kind)
		if owning.Kind == models.MemoryContextSelf {
			q = q.Where("m.context_principal_kind = ? AND m.context_principal = ?", owning.PrincipalKind, owning.Principal)
		}
	}
	q = memoryReaderPrivacy(q.Where("m.id = ? AND m.deleted_at IS NULL", id), caller, scope.MemoryVisibilityOptions{ApplyPrivacyScope: true})
	if lock {
		q = q.Clauses(clause.Locking{Strength: "UPDATE", Table: clause.Table{Name: "m"}})
	}
	var row Memory
	if err := q.Take(&row).Error; err != nil {
		return nil, err
	}
	memory := memoryRowToModel(&row)
	if owning.Kind == models.MemoryContextProject {
		memory.Project = project
	}
	return memory, nil
}

func memoryCommandDigest(mem *models.Memory) (string, error) {
	row := memoryRowForCreate(mem, time.Time{}, false)
	copyPrivacyFields(row, mem)
	copyPrincipalMemoryFields(row, mem)
	tags := mem.Tags
	if tags == nil {
		tags = []string{}
	}
	sessions := []string(row.SourceSessions)
	if sessions == nil {
		sessions = []string{}
	}
	command := struct {
		Context     *models.MemoryContext
		Project     string
		Content     string
		Tags        []string
		SourceAgent string
		Visibility  string
		Privacy     string
		Domain      string
		Sessions    []string
	}{mem.Context, mem.Project, mem.Content, tags, mem.SourceAgent, row.AgentVisibility, row.PrivacyScope, row.Domain, sessions}
	encoded, err := json.Marshal(command)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), nil
}

// CreateInContext commits the body and optional retry receipt together. The
// first-write guard runs after retry reconciliation. A false guard result commits
// its audit without creating a body or receipt and returns ErrMemoryCreateDenied
// after commit; a guard or commit error is returned instead.
func (s *MemoryStore) CreateInContext(ctx context.Context, mem *models.Memory, caller scope.KeycardContext, requestKey string, guard func(context.Context, *gormlib.DB) (bool, error)) (*models.Memory, bool, error) {
	if err := validateMemoryForCreate(mem); err != nil {
		return nil, false, err
	}
	if mem.Context == nil || memoryContextKind(mem) == models.MemoryContextLegacy {
		return nil, false, fmt.Errorf("typed memory context is required")
	}
	if caller.WorkstationID == "" || caller.Principal == "" || caller.Principal != mem.OwnerPrincipal || caller.PrincipalKind != mem.OwnerPrincipalKind {
		return nil, false, ErrMemoryPrincipalRequired
	}
	if len(requestKey) > 1024 {
		return nil, false, fmt.Errorf("request_key exceeds 1024 UTF-8 bytes")
	}
	var digest string
	var err error
	if requestKey != "" {
		digest, err = memoryCommandDigest(mem)
		if err != nil {
			return nil, false, err
		}
	}
	var result *models.Memory
	replayed := false
	denied := false
	err = s.db.WithContext(ctx).Transaction(func(tx *gormlib.DB) error {
		if requestKey != "" {
			key := advisoryLockKey("memory-request-key/v1", caller.PrincipalKind, caller.Principal, mem.Context.Kind, mem.Project, requestKey)
			if err := tx.Exec("SELECT pg_advisory_xact_lock(?)", key).Error; err != nil {
				return err
			}
			var receipt memoryRequestKey
			found := tx.Where("principal_kind = ? AND principal = ? AND context_kind = ? AND project = ? AND request_key = ?", caller.PrincipalKind, caller.Principal, mem.Context.Kind, mem.Project, requestKey).Take(&receipt).Error
			if found == nil {
				if receipt.RequestDigest != digest {
					return ErrMemoryRequestConflict
				}
				if receipt.MemoryID == nil {
					return ErrMemoryRequestRetired
				}
				current, err := memoryInContext(ctx, tx, *receipt.MemoryID, *mem.Context, mem.Project, caller, true)
				if errors.Is(err, gormlib.ErrRecordNotFound) {
					return ErrMemoryRequestRetired
				}
				if err != nil {
					return err
				}
				if current.Status != "active" {
					return ErrMemoryRequestRetired
				}
				result, replayed = current, true
				return nil
			}
			if !errors.Is(found, gormlib.ErrRecordNotFound) {
				return found
			}
		}
		if guard != nil {
			allowed, err := guard(ctx, tx)
			if err != nil {
				return err
			}
			if !allowed {
				denied = true
				return nil
			}
		}
		row := memoryRowForCreate(mem, time.Now().UTC(), false)
		copyPrivacyFields(row, mem)
		copyPrincipalMemoryFields(row, mem)
		row.SourceWorkstationID = caller.WorkstationID
		if err := createMemoryContextRowTx(ctx, tx, row); err != nil {
			return err
		}
		if requestKey != "" {
			receipt := memoryRequestKey{PrincipalKind: caller.PrincipalKind, Principal: caller.Principal, ContextKind: mem.Context.Kind, Project: mem.Project, RequestKey: requestKey, RequestDigest: digest, MemoryID: &row.ID}
			if err := tx.Create(&receipt).Error; err != nil {
				return err
			}
		}
		result = memoryRowToModel(row)
		return nil
	}, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err == nil && denied {
		return nil, false, ErrMemoryCreateDenied
	}
	return result, replayed, err
}
