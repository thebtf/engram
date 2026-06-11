// Package writelint — T033 (engram vNext Milestone F TG5).
// conflict_adapter.go projects Memory→Observation DTO for use with
// pkg/models.DetectConflict, following the 13-row Conflict Adapter Mapping
// Contract in plan.md §Conflict Adapter Mapping Contract (CR-F5 PM contract).
//
// IMPORTANT: This file does NOT modify pkg/models/conflict.go.
// The 14 CorrectionPatterns and all detection functions remain untouched.
// This is a one-way projection adapter only.
//
// IF-WRONG ledger: TD-PHASE-F-CONFLICT-COVERAGE — if any CorrectionPattern
// fails to fire on Memory-projected content where it would fire on native
// Observation content, the gap is logged there and escalated to Option B
// (port DetectConflict to Memory natively).
package writelint

import (
	"database/sql"
	"strings"

	"github.com/thebtf/engram/pkg/models"
)

// ProjectMemoryToObservation converts a Memory to an Observation DTO for
// conflict detection. Implements the 13-row Conflict Adapter Mapping Contract
// from plan.md verbatim. The projection is one-way; no Observation field is
// written back to Memory.
//
// Row summary (plan.md table):
//  1. ID            — direct copy (int64)
//  2. Project       — direct copy (string)
//  3. Scope         — derived from PrivacyScope: private/project→ScopeProject; shared/global→ScopeGlobal
//  4. Type          — derived from EpistemicType: fact→Discovery; experience→Feature; opinion→Decision; observation/default→Change
//  5. Title         — first 100 chars of Content; sql.NullString{Valid:false} if content < 5 chars
//  6. Narrative     — Content; always Valid=true when content non-empty
//  7. Concepts      — direct copy of Tags array
//  8. FilesModified — tags filtered for "file:" prefix, prefix stripped
//  9. FilesRead     — tags filtered for "read:" prefix, prefix stripped
// 10. SourceAgent   — direct copy
// 11. CreatedAtEpoch— CreatedAt.UnixMilli()
// 12. SDKSessionID  — SourceSessions[0] if non-empty, else ""
// 13. IsSuperseded  — Status == "superseded"
func ProjectMemoryToObservation(m *models.Memory) *models.Observation {
	obs := &models.Observation{}

	// Row 1: ID direct copy
	obs.ID = m.ID

	// Row 2: Project direct copy
	obs.Project = m.Project

	// Row 3: Scope from PrivacyScope
	// private/project → ScopeProject; shared/global → ScopeGlobal; default → ScopeProject
	switch m.PrivacyScope {
	case "shared", "global":
		obs.Scope = models.ScopeGlobal
	default:
		obs.Scope = models.ScopeProject
	}

	// Row 4: Type from EpistemicType
	// fact→Discovery; experience→Feature; opinion→Decision; observation/default→Change
	switch m.EpistemicType {
	case "fact":
		obs.Type = models.ObsTypeDiscovery
	case "experience":
		obs.Type = models.ObsTypeFeature
	case "opinion":
		obs.Type = models.ObsTypeDecision
	default:
		obs.Type = models.ObsTypeChange
	}

	// Row 5: Title = first 100 chars of Content
	// sql.NullString{Valid:false} when content shorter than 5 chars
	if len(m.Content) >= 5 {
		title := m.Content
		if len(title) > 100 {
			title = title[:100]
		}
		obs.Title = sql.NullString{String: title, Valid: true}
	} else {
		obs.Title = sql.NullString{Valid: false}
	}

	// Row 6: Narrative = Content (always Valid=true when content non-empty)
	if m.Content != "" {
		obs.Narrative = sql.NullString{String: m.Content, Valid: true}
	}

	// Row 7: Concepts = Tags direct copy
	if len(m.Tags) > 0 {
		concepts := make([]string, len(m.Tags))
		copy(concepts, m.Tags)
		obs.Concepts = models.JSONStringArray(concepts)
	}

	// Row 8: FilesModified from tags with "file:" prefix (strip prefix)
	// Row 9: FilesRead from tags with "read:" prefix (strip prefix)
	var filesModified, filesRead []string
	for _, tag := range m.Tags {
		if stripped, ok := stripPrefix(tag, "file:"); ok {
			filesModified = append(filesModified, stripped)
		} else if stripped, ok := stripPrefix(tag, "read:"); ok {
			filesRead = append(filesRead, stripped)
		}
	}
	if len(filesModified) > 0 {
		obs.FilesModified = models.JSONStringArray(filesModified)
	}
	if len(filesRead) > 0 {
		obs.FilesRead = models.JSONStringArray(filesRead)
	}

	// Row 10: SourceAgent direct copy
	obs.AgentSource = models.AgentSource(m.SourceAgent)

	// Row 11: CreatedAtEpoch from CreatedAt.UnixMilli()
	obs.CreatedAtEpoch = m.CreatedAt.UnixMilli()

	// Row 12: SDKSessionID = SourceSessions[0] if non-empty, else ""
	if len(m.SourceSessions) > 0 {
		obs.SDKSessionID = m.SourceSessions[0]
	}

	// Row 13: IsSuperseded = Status == "superseded"
	obs.IsSuperseded = m.Status == "superseded"

	return obs
}

// stripPrefix checks if s starts with prefix and returns the stripped string.
func stripPrefix(s, prefix string) (string, bool) {
	if strings.HasPrefix(s, prefix) {
		return s[len(prefix):], true
	}
	return "", false
}
