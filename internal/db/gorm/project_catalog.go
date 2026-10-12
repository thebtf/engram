package gorm

import (
	"context"
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
	if err := s.validateUnresolvedMembership(ctx, rows); err != nil {
		return nil, err
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
	result := make([]UnresolvedProject, 0)
	for _, row := range rows {
		name := labels[row.SelectorReference]
		if row.Table == "sources" {
			var source struct{ DisplayName string }
			if err := s.db.WithContext(ctx).Raw("SELECT display_name FROM sources WHERE source_id::text = ?::jsonb->>'source_id'", string(row.RowKey)).Scan(&source).Error; err != nil {
				return nil, err
			}
			name = source.DisplayName
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
