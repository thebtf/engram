package gorm

import (
	"encoding/json"
	"fmt"
	"strings"

	gormlib "gorm.io/gorm"
)

const (
	liveDocumentMembershipReason      = "document-live-version/v1:"
	liveDocumentMembershipFingerprint = "urn:engram:migration:document-live-version/v1:"
	liveDocumentReferencePrefix       = "urn:engram:migration:document-live-row/v1:"
)

type liveDocumentProof struct {
	Reference, Parent, Table, SourceRowKey, FieldPath, SelectorReference, ProjectID, NativeVersion, SourceSnapshot string
}

func (s *ProjectConsolidationStore) liveDocumentFingerprint(member ConsolidationMembership, parent string) string {
	project := ""
	if member.ProjectID != nil {
		project = *member.ProjectID
	}
	proof := liveDocumentProof{member.Reference, parent, member.Table, string(member.SourceRowKey), string(member.FieldPath), member.SelectorReference, project, member.NativeVersion, member.SourceSnapshot}
	return s.opaque("document-live-version/v1", proof)
}

// membershipEvidence loads all ancestors in one database round trip. The
// caller must hold the membership authority lock while using this snapshot.
func membershipEvidence(tx *gormlib.DB, roots []ConsolidationMembership) (map[string]ConsolidationMembership, error) {
	refs := make([]string, 0, len(roots))
	for _, member := range roots {
		refs = append(refs, member.Reference)
	}
	raw, err := json.Marshal(refs)
	if err != nil {
		return nil, err
	}
	var rows []ConsolidationMembership
	err = tx.Raw(`WITH RECURSIVE ancestry(reference) AS (
		SELECT jsonb_array_elements_text(?::jsonb)
		UNION
		SELECT substring(m.reason FROM ?::integer) FROM project_consolidation_memberships m
		JOIN ancestry a ON a.reference = m.reference
		WHERE left(m.plan_fingerprint, ?) = ? AND left(m.reason, ?) = ?
	) SELECT m.* FROM project_consolidation_memberships m JOIN ancestry a ON a.reference = m.reference`,
		string(raw), len(liveDocumentMembershipReason)+1, len(liveDocumentMembershipFingerprint), liveDocumentMembershipFingerprint,
		len(liveDocumentMembershipReason), liveDocumentMembershipReason).Scan(&rows).Error
	if err != nil {
		return nil, err
	}
	members := make(map[string]ConsolidationMembership, len(rows))
	for _, member := range rows {
		members[member.Reference] = member
	}
	return members, nil
}

func (s *ProjectConsolidationStore) validateMembershipEvidence(tx *gormlib.DB, initial ConsolidationMembership) error {
	members, err := membershipEvidence(tx, []ConsolidationMembership{initial})
	if err != nil {
		return err
	}
	return s.validateLoadedMembershipEvidence(initial, members, make(map[string]bool))
}

// Validated ancestors are shared only within the same locked snapshot.
func (s *ProjectConsolidationStore) validateLoadedMembershipEvidence(initial ConsolidationMembership, members map[string]ConsolidationMembership, validated map[string]bool) error {
	seen := map[string]bool{}
	var chain []string
	member := initial
	for {
		if validated[member.Reference] {
			break
		}
		if seen[member.Reference] {
			return consolidationHold("STALE_MAPPING")
		}
		seen[member.Reference] = true
		chain = append(chain, member.Reference)
		if err := consolidationNormalizeMembership(&member); err != nil {
			return err
		}
		var path []string
		_ = json.Unmarshal(member.FieldPath, &path)
		var nested []string
		if len(path) > 1 {
			nested = path[1:]
		}
		kind := "row"
		liveReference := strings.HasPrefix(member.Reference, liveDocumentReferencePrefix)
		if liveReference {
			kind = "document-live-row/v1"
		}
		ref := s.opaque(kind, struct {
			Table, Column string
			Key           JSONRaw
			Path          []string
		}{member.Table, path[0], member.SourceRowKey, nested})
		if ref != member.Reference || member.PlanFingerprint == "" || member.NativeVersion == "" || member.SourceSnapshot == "" {
			return consolidationHold("STALE_MAPPING")
		}
		if !liveReference {
			if strings.HasPrefix(member.PlanFingerprint, liveDocumentMembershipFingerprint) || strings.HasPrefix(member.Reason, liveDocumentMembershipReason) {
				return consolidationHold("STALE_MAPPING")
			}
			if member.ProjectID == nil {
				if _, _, err := s.unresolvedMembershipMetadata(&member); err != nil {
					return err
				}
			}
			break
		}
		if !strings.HasPrefix(member.PlanFingerprint, liveDocumentMembershipFingerprint) {
			return consolidationHold("STALE_MAPPING")
		}
		parentRef := strings.TrimPrefix(member.Reason, liveDocumentMembershipReason)
		if member.Table != "versioned_documents" || string(member.FieldPath) != `["project"]` || member.ProjectID == nil || parentRef == member.Reason || parentRef == "" || s.liveDocumentFingerprint(member, parentRef) != member.PlanFingerprint {
			return consolidationHold("STALE_MAPPING")
		}
		parent, ok := members[parentRef]
		if !ok {
			return consolidationHold("STALE_MAPPING")
		}
		if parent.Table != member.Table || string(parent.FieldPath) != string(member.FieldPath) || parent.ProjectID == nil || *parent.ProjectID != *member.ProjectID {
			return consolidationHold("STALE_MAPPING")
		}
		member = parent
	}
	for _, ref := range chain {
		validated[ref] = true
	}
	return nil
}

func (s *ProjectConsolidationStore) writeLiveDocumentMembership(tx *gormlib.DB, parent ConsolidationMembership, doc VersionedDocument) error {
	key, err := json.Marshal(map[string]int64{"id": doc.ID})
	if err != nil {
		return err
	}
	member := ConsolidationMembership{Table: "versioned_documents", RowKey: JSONRaw(key), SourceRowKey: JSONRaw(key), FieldPath: JSONRaw(`["project"]`), ProjectID: parent.ProjectID, Reason: liveDocumentMembershipReason + parent.Reference, SelectorReference: s.opaque("selector", doc.Project)}
	member.Reference = s.opaque("document-live-row/v1", struct {
		Table, Column string
		Key           JSONRaw
		Path          []string
	}{member.Table, "project", member.SourceRowKey, nil})
	if err := tx.Raw(`SELECT xmin::text FROM versioned_documents WHERE id = ?`, doc.ID).Scan(&member.NativeVersion).Error; err != nil {
		return err
	}
	if err := tx.Raw(`SELECT txid_current_snapshot()::text`).Scan(&member.SourceSnapshot).Error; err != nil {
		return err
	}
	if member.NativeVersion == "" || member.SourceSnapshot == "" {
		return fmt.Errorf("live document membership evidence unavailable")
	}
	member.PlanFingerprint = s.liveDocumentFingerprint(member, parent.Reference)
	return tx.Create(&member).Error
}
