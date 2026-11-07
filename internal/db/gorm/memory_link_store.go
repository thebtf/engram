package gorm

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/thebtf/engram/internal/scope"
	"github.com/thebtf/engram/pkg/models"
	gormlib "gorm.io/gorm"
)

type MemoryLink struct {
	ID                  int64      `json:"id"`
	SourceID            int64      `json:"source_id"`
	TargetID            int64      `json:"target_id"`
	Relation            string     `json:"relation"`
	TargetVersion       *int       `json:"target_version"`
	AuthorPrincipalKind string     `json:"-"`
	AuthorPrincipal     string     `json:"-"`
	SourceSessionID     string     `json:"source_session_id"`
	RemovedAt           *time.Time `json:"-"`
}

func (MemoryLink) TableName() string { return "memory_links" }

func (link MemoryLink) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		ID              int64               `json:"id"`
		SourceID        int64               `json:"source_id"`
		TargetID        int64               `json:"target_id"`
		Relation        string              `json:"relation"`
		TargetVersion   *int                `json:"target_version"`
		Author          models.MemoryAuthor `json:"author"`
		SourceSessionID string              `json:"source_session_id"`
	}{link.ID, link.SourceID, link.TargetID, link.Relation, link.TargetVersion, models.MemoryAuthor{PrincipalKind: link.AuthorPrincipalKind, Principal: link.AuthorPrincipal}, link.SourceSessionID})
}

type memoryLinkReceipt struct {
	PrincipalKind string
	Principal     string
	RequestKey    string
	RequestDigest string
	LinkID        int64
}

func (memoryLinkReceipt) TableName() string { return "memory_link_receipts" }

// lockSelfLinkEndpoints follows the table-lock prefix used by revision and
// consolidation before taking ordered memory locks. Both endpoints must be
// active and accessible to this exact keycard.
func lockSelfLinkEndpoints(ctx context.Context, tx *gormlib.DB, owning models.MemoryContext, caller scope.KeycardContext, source, target int64) (*models.Memory, error) {
	if err := tx.Exec(`LOCK TABLE "audit_log", "content_chunks", "memories" IN ROW EXCLUSIVE MODE`).Error; err != nil {
		return nil, err
	}
	ordered := [2]int64{source, target}
	if ordered[0] > ordered[1] {
		ordered[0], ordered[1] = ordered[1], ordered[0]
	}
	var targetMemory *models.Memory
	for i, id := range ordered {
		if i == 1 && id == ordered[0] {
			continue
		}
		m, err := memoryInContext(ctx, tx, id, owning, "", caller, true)
		if err != nil {
			return nil, err
		}
		if m.Status != "active" || !scope.ResolveMemoryManage(caller, m) {
			return nil, gormlib.ErrRecordNotFound
		}
		if id == target {
			targetMemory = m
		}
	}
	return targetMemory, nil
}

func (s *MemoryStore) CreateSelfLink(ctx context.Context, owning models.MemoryContext, caller scope.KeycardContext, source, target int64, relation string, version *int, key, session string) (MemoryLink, bool, error) {
	if source <= 0 || target <= 0 || source == target || (relation != "related_to" && relation != "references") || (version != nil && (*version <= 0 || relation != "references")) || len(key) > 1024 || len(session) > 1024 {
		return MemoryLink{}, false, fmt.Errorf("invalid link")
	}
	if relation == "related_to" && source > target {
		source, target = target, source
	}
	var digest string
	if key != "" {
		payload, err := json.Marshal(struct {
			Source, Target int64
			Relation       string
			Version        *int
		}{source, target, relation, version})
		if err != nil {
			return MemoryLink{}, false, err
		}
		sum := sha256.Sum256(payload)
		digest = hex.EncodeToString(sum[:])
	}
	var result MemoryLink
	created := false
	err := s.db.WithContext(ctx).Transaction(func(tx *gormlib.DB) error {
		// Serialise key reuse even for different endpoints, before endpoint locks.
		if key != "" {
			lockKey, _ := json.Marshal([]string{owning.PrincipalKind, owning.Principal, key})
			if err := tx.Exec("SELECT pg_advisory_xact_lock(hashtextextended(?, 0))", string(lockKey)).Error; err != nil {
				return err
			}
		}
		targetMemory, err := lockSelfLinkEndpoints(ctx, tx, owning, caller, source, target)
		if err != nil {
			return err
		}
		if version != nil && *version != targetMemory.Version {
			var count int64
			if err := tx.Model(&MemoryRevision{}).Where("memory_id = ? AND version = ?", target, *version).Count(&count).Error; err != nil {
				return err
			}
			if count == 0 {
				return gormlib.ErrRecordNotFound
			}
		}
		if key != "" {
			var receipt memoryLinkReceipt
			err := tx.Where("principal_kind = ? AND principal = ? AND request_key = ?", owning.PrincipalKind, owning.Principal, key).Take(&receipt).Error
			if err == nil {
				if receipt.RequestDigest != digest {
					return ErrMemoryRequestConflict
				}
				if err := tx.Where("id = ? AND removed_at IS NULL", receipt.LinkID).Take(&result).Error; errors.Is(err, gormlib.ErrRecordNotFound) {
					return ErrMemoryRequestRetired
				} else {
					return err
				}
			}
			if !errors.Is(err, gormlib.ErrRecordNotFound) {
				return err
			}
		}
		query := tx.Where("source_id = ? AND target_id = ? AND relation = ? AND removed_at IS NULL", source, target, relation)
		if version == nil {
			query = query.Where("target_version IS NULL")
		} else {
			query = query.Where("target_version = ?", *version)
		}
		err = query.Take(&result).Error
		if errors.Is(err, gormlib.ErrRecordNotFound) {
			result = MemoryLink{SourceID: source, TargetID: target, Relation: relation, TargetVersion: version, AuthorPrincipalKind: owning.PrincipalKind, AuthorPrincipal: owning.Principal, SourceSessionID: session}
			if err := tx.Create(&result).Error; err != nil {
				return err
			}
			created = true
		} else if err != nil {
			return err
		}
		if key != "" {
			return tx.Create(&memoryLinkReceipt{PrincipalKind: owning.PrincipalKind, Principal: owning.Principal, RequestKey: key, RequestDigest: digest, LinkID: result.ID}).Error
		}
		return nil
	})
	return result, created, err
}

func (s *MemoryStore) ListSelfLinks(ctx context.Context, owning models.MemoryContext, caller scope.KeycardContext, source, after int64, limit int) ([]MemoryLink, *int64, error) {
	rows := make([]MemoryLink, 0)
	var next *int64
	err := s.db.WithContext(ctx).Transaction(func(tx *gormlib.DB) error {
		m, err := memoryInContext(ctx, tx, source, owning, "", caller, false)
		if err != nil {
			return err
		}
		if m.Status != "active" {
			return gormlib.ErrRecordNotFound
		}
		visible := memoryReaderPrivacy(tx.Model(&Memory{}).Table("memories AS m").Select("m.id").Where("m.context_kind = ? AND m.project = '' AND m.context_principal_kind = ? AND m.context_principal = ? AND m.deleted_at IS NULL AND m.status = 'active'", owning.Kind, owning.PrincipalKind, owning.Principal), caller, scope.MemoryVisibilityOptions{ApplyPrivacyScope: true})
		var candidates []struct {
			ID        int64
			ByteBound int64
		}
		// JSON escaping can expand one input byte to six; reserve fixed envelope and numeric-field overhead per row.
		query := tx.Model(&MemoryLink{}).Where("removed_at IS NULL AND id > ? AND (source_id = ? OR (relation = 'related_to' AND target_id = ?))", after, source, source).Where("source_id IN (?) AND target_id IN (?)", visible, visible)
		if err := query.Select("id, 256 + 6 * (octet_length(relation)::bigint + octet_length(author_principal_kind)::bigint + octet_length(author_principal)::bigint + octet_length(source_session_id)::bigint) AS byte_bound").Order("id ASC").Limit(limit + 1).Scan(&candidates).Error; err != nil {
			return err
		}
		ids := make([]int64, 0, limit)
		var size int64
		for _, candidate := range candidates {
			if len(ids) == limit || (len(ids) > 0 && size+candidate.ByteBound > 1<<20) {
				cursor := ids[len(ids)-1]
				next = &cursor
				break
			}
			ids = append(ids, candidate.ID)
			size += candidate.ByteBound
		}
		if len(ids) == 0 {
			return nil
		}
		// The repeatable-read snapshot binds these IDs and the authorization and sizes checked above to the loaded rows.
		return tx.Where("id IN ?", ids).Order("id ASC").Find(&rows).Error
	}, &sql.TxOptions{Isolation: sql.LevelRepeatableRead})
	if err != nil {
		return nil, nil, err
	}
	return rows, next, nil
}

func (s *MemoryStore) RemoveSelfLink(ctx context.Context, owning models.MemoryContext, caller scope.KeycardContext, id int64) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gormlib.DB) error {
		var link MemoryLink
		if err := tx.Where("id = ? AND removed_at IS NULL", id).Take(&link).Error; err != nil {
			return err
		}
		if _, err := lockSelfLinkEndpoints(ctx, tx, owning, caller, link.SourceID, link.TargetID); err != nil {
			return err
		}
		if link.AuthorPrincipalKind != owning.PrincipalKind || link.AuthorPrincipal != owning.Principal {
			return gormlib.ErrRecordNotFound
		}
		result := tx.Model(&MemoryLink{}).Where("id = ? AND removed_at IS NULL", id).Update("removed_at", time.Now().UTC())
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			return gormlib.ErrRecordNotFound
		}
		return nil
	})
}
