package gorm

import (
	"context"
	"encoding/json"
	"strings"
)

type UnresolvedProject struct {
	Reference string `json:"reference"`
	Name      string `json:"name"`
	Reason    string `json:"reason"`
}

func (s *ProjectConsolidationStore) ListUnresolved(ctx context.Context) ([]UnresolvedProject, error) {
	if s == nil || s.db == nil || s.vault == nil {
		return nil, consolidationHold("UNKNOWN_REFERENCE")
	}
	var rows []ConsolidationMembership
	if err := s.db.WithContext(ctx).Where("project_id IS NULL AND unresolved_reference <> ''").Order("unresolved_reference, reason, reference").Find(&rows).Error; err != nil {
		return nil, err
	}
	for i := range rows {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if _, _, err := s.unresolvedMembershipMetadata(&rows[i]); err != nil {
			return nil, err
		}
	}
	var projects []Project
	if err := s.db.WithContext(ctx).Find(&projects).Error; err != nil {
		return nil, err
	}
	labels := map[string]string{}
	for _, project := range projects {
		name := project.DisplayName.String
		if name == "" {
			name = project.ID
		}
		labels[s.opaque("selector", project.ID)] = name
	}
	var sourceKeys []json.RawMessage
	var sourceReferences []string
	for _, row := range rows {
		if row.Table == "sources" {
			sourceKeys = append(sourceKeys, json.RawMessage(row.RowKey))
			sourceReferences = append(sourceReferences, row.Reference)
		}
	}
	sourceLabels := make(map[string]string, len(sourceKeys))
	for start := 0; start < len(sourceKeys); {
		end, size := start, 0
		for end < len(sourceKeys) && end-start < consolidationMembershipBatchSize {
			if end > start && size+len(sourceKeys[end]) > consolidationMembershipBatchBytes {
				break
			}
			size += len(sourceKeys[end])
			end++
		}
		keys, err := json.Marshal(sourceKeys[start:end])
		if err != nil {
			return nil, consolidationHold("STALE_REFERENCE")
		}
		var sources []struct {
			Ordinal     int
			DisplayName string
		}
		if err := s.db.WithContext(ctx).Raw("SELECT r.ordinality AS ordinal, s.display_name FROM jsonb_array_elements(?::jsonb) WITH ORDINALITY AS r(value, ordinality) LEFT JOIN sources s ON s.source_id::text = r.value->>'source_id'", string(keys)).Scan(&sources).Error; err != nil {
			return nil, err
		}
		for _, source := range sources {
			if source.Ordinal < 1 || source.Ordinal > end-start {
				return nil, consolidationHold("STALE_REFERENCE")
			}
			sourceLabels[sourceReferences[start+source.Ordinal-1]] = source.DisplayName
		}
		start = end
	}
	result := make([]UnresolvedProject, 0)
	for _, row := range rows {
		name := labels[row.SelectorReference]
		if row.Table == "sources" {
			name = sourceLabels[row.Reference]
		}
		if name == "" {
			name = "Unresolved data owner"
		}
		if len(result) > 0 && result[len(result)-1].Reference == row.UnresolvedReference {
			item := &result[len(result)-1]
			if !strings.Contains(","+item.Reason+",", ","+row.Reason+",") {
				item.Reason += "," + row.Reason
			}
			continue
		}
		result = append(result, UnresolvedProject{Reference: row.UnresolvedReference, Name: name, Reason: row.Reason})
	}
	return result, nil
}
