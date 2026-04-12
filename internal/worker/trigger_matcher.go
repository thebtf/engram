package worker

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/thebtf/engram/internal/privacy"
	"github.com/thebtf/engram/internal/vector"
	"github.com/thebtf/engram/pkg/models"
)

const semanticTriggerCandidateLimit = 10
const semanticTriggerResultLimit = 3
const bashTriggerResultLimit = 3
const repeatedReadThreshold = 3
const repeatedReadCandidateLimit = 20
const repeatedReadResultLimit = 3

func (s *Service) matchBashCommandTriggers(ctx context.Context, req MemoryTriggerRequest) ([]MemoryTriggerMatch, error) {
	command := extractTriggerCommand(req.Params)
	if command == "" {
		return []MemoryTriggerMatch{}, nil
	}
	if privacy.ContainsSecrets(command) {
		return []MemoryTriggerMatch{}, nil
	}
	if s.observationStore == nil {
		return []MemoryTriggerMatch{}, nil
	}

	observations, err := s.observationStore.GetObservationsByCommandPrefix(ctx, req.Project, command, bashTriggerResultLimit)
	if err != nil {
		return nil, err
	}
	matches := make([]MemoryTriggerMatch, 0, len(observations))
	for _, observation := range observations {
		if observation == nil {
			continue
		}
		matches = append(matches, MemoryTriggerMatch{
			Kind:          "warning",
			ObservationID: observation.ID,
			Blurb:         semanticTriggerBlurb(observation),
		})
		if len(matches) >= bashTriggerResultLimit {
			break
		}
	}
	return matches, nil
}

func extractTriggerCommand(params map[string]any) string {
	if params == nil {
		return ""
	}
	if command, ok := params["command"].(string); ok {
		return strings.TrimSpace(command)
	}
	return ""
}

func (s *Service) matchReadPathTriggers(ctx context.Context, req MemoryTriggerRequest) ([]MemoryTriggerMatch, error) {
	filePath := extractTriggerFilePath(req.Params)
	if filePath == "" || req.SessionID == "" {
		return []MemoryTriggerMatch{}, nil
	}
	readCount := readSignalCountFromParams(req.Params, filePath)
	if readCount <= 0 {
		readCount = s.readSignalCountForPath(req.SessionID, filePath)
	}
	if readCount < repeatedReadThreshold {
		return []MemoryTriggerMatch{}, nil
	}

	observations, err := s.filePathObservations(ctx, req.Project, filePath, repeatedReadCandidateLimit)
	if err != nil {
		return nil, err
	}
	matches := make([]MemoryTriggerMatch, 0, repeatedReadResultLimit)
	for _, observation := range observations {
		if observation == nil {
			continue
		}
		if observation.Type != models.ObsTypeDecision && !hasPatternConcept(observation.Concepts) {
			continue
		}
		matches = append(matches, MemoryTriggerMatch{
			Kind:          "context",
			ObservationID: observation.ID,
			Blurb:         semanticTriggerBlurb(observation),
		})
		if len(matches) >= repeatedReadResultLimit {
			break
		}
	}
	return matches, nil
}

func (s *Service) readSignalCountForPath(sessionID, filePath string) int {
	if s.retrievalHooks != nil && s.retrievalHooks.readSignalCountForPath != nil {
		return s.retrievalHooks.readSignalCountForPath(sessionID, filePath)
	}
	return readSignalCountFromSessionSignals(sessionID, filePath)
}

func (s *Service) filePathObservations(ctx context.Context, project, filePath string, limit int) ([]*models.Observation, error) {
	if s.retrievalHooks != nil && s.retrievalHooks.filePathObservations != nil {
		return s.retrievalHooks.filePathObservations(ctx, project, filePath, limit)
	}
	if s.observationStore == nil {
		return []*models.Observation{}, nil
	}
	observations, err := s.observationStore.GetObservationsByFile(ctx, filePath, limit)
	if err != nil {
		return nil, err
	}
	if project == "" {
		return observations, nil
	}
	scoped := make([]*models.Observation, 0, len(observations))
	for _, observation := range observations {
		if observation == nil || observation.Project != project {
			continue
		}
		scoped = append(scoped, observation)
		if limit > 0 && len(scoped) >= limit {
			break
		}
	}
	return scoped, nil
}

func hasPatternConcept(concepts []string) bool {
	for _, concept := range concepts {
		if concept == "pattern" {
			return true
		}
	}
	return false
}

func readSignalCountFromSessionSignals(sessionID, filePath string) int {
	if sessionID == "" || filePath == "" {
		return 0
	}
	data, err := os.ReadFile(sessionSignalPath(sessionID))
	if err != nil {
		return 0
	}
	var payload struct {
		ReadCounts map[string]int `json:"read_counts"`
	}
	if err := json.Unmarshal(data, &payload); err != nil {
		return 0
	}
	if payload.ReadCounts == nil {
		return 0
	}
	return payload.ReadCounts[filePath]
}

func sessionSignalPath(sessionID string) string {
	safe := strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z':
			return r
		case r >= 'A' && r <= 'Z':
			return r
		case r >= '0' && r <= '9':
			return r
		case r == '_' || r == '-':
			return r
		default:
			return '_'
		}
	}, sessionID)
	return filepath.Join(os.TempDir(), "engram-signals-"+safe+".json")
}

func (s *Service) matchMemoryTriggers(ctx context.Context, req MemoryTriggerRequest) ([]MemoryTriggerMatch, error) {
	switch req.Tool {
	case "Edit", "Write":
		return s.matchEditWriteSemanticTriggers(ctx, req)
	case "Bash":
		return s.matchBashCommandTriggers(ctx, req)
	case "Read":
		return s.matchReadPathTriggers(ctx, req)
	default:
		return []MemoryTriggerMatch{}, nil
	}
}

func (s *Service) matchEditWriteSemanticTriggers(ctx context.Context, req MemoryTriggerRequest) ([]MemoryTriggerMatch, error) {
	filePath := extractTriggerFilePath(req.Params)
	if filePath == "" || !s.hasVectorRetrieval() {
		return []MemoryTriggerMatch{}, nil
	}

	where := vector.BuildWhereFilter(vector.DocTypeObservation, req.Project, true)
	query := buildEditWriteTriggerQuery(req.Tool, filePath, req.Params)

	vectorResults, err := s.runVectorQuery(ctx, query, semanticTriggerCandidateLimit, where)
	if err != nil {
		return nil, err
	}
	ids := vector.ExtractObservationIDs(vectorResults, req.Project)
	if len(ids) == 0 {
		return []MemoryTriggerMatch{}, nil
	}

	observations, err := s.fetchObservationsByID(ctx, ids, "", 0)
	if err != nil {
		return nil, err
	}
	if len(observations) == 0 {
		return []MemoryTriggerMatch{}, nil
	}

	byID := make(map[int64]*models.Observation, len(observations))
	for _, observation := range observations {
		if observation != nil {
			byID[observation.ID] = observation
		}
	}

	matches := make([]MemoryTriggerMatch, 0, semanticTriggerResultLimit)
	for _, id := range ids {
		observation, ok := byID[id]
		if !ok || !isSemanticTriggerType(observation.Type) {
			continue
		}
		matches = append(matches, MemoryTriggerMatch{
			Kind:          semanticTriggerKind(observation.Type),
			ObservationID: observation.ID,
			Blurb:         semanticTriggerBlurb(observation),
		})
		if len(matches) >= semanticTriggerResultLimit {
			break
		}
	}

	return matches, nil
}

func extractTriggerFilePath(params map[string]any) string {
	if params == nil {
		return ""
	}
	for _, key := range []string{"file_path", "path", "filePath", "file", "filename"} {
		value, ok := params[key]
		if !ok {
			continue
		}
		if filePath, ok := value.(string); ok && filePath != "" {
			return filePath
		}
	}
	return ""
}

func readSignalCountFromParams(params map[string]any, filePath string) int {
	if params == nil || filePath == "" {
		return 0
	}
	rawReadCounts, ok := params["read_counts"]
	if !ok {
		return 0
	}
	switch counts := rawReadCounts.(type) {
	case map[string]any:
		if value, exists := counts[filePath]; exists {
			if parsed, ok := parseReadCount(value); ok {
				return parsed
			}
		}
	case map[string]int:
		return counts[filePath]
	}
	return 0
}

func parseReadCount(value any) (int, bool) {
	switch v := value.(type) {
	case int:
		return v, true
	case int64:
		return int(v), true
	case float64:
		return int(v), true
	case json.Number:
		parsed, err := v.Int64()
		if err == nil {
			return int(parsed), true
		}
	}
	return 0, false
}

func buildEditWriteTriggerQuery(tool, filePath string, params map[string]any) string {
	_ = params
	return fmt.Sprintf("%s %s", tool, filePath)
}

func isSemanticTriggerType(obsType models.ObservationType) bool {
	switch obsType {
	case models.ObsTypeBugfix, models.ObsTypeGuidance, models.ObsTypePitfall:
		return true
	default:
		return false
	}
}

func semanticTriggerKind(obsType models.ObservationType) string {
	switch obsType {
	case models.ObsTypeGuidance:
		return "context"
	default:
		return "warning"
	}
}

func semanticTriggerBlurb(observation *models.Observation) string {
	if observation == nil {
		return ""
	}
	if observation.Narrative.Valid && observation.Narrative.String != "" {
		return observation.Narrative.String
	}
	if observation.Title.Valid {
		return observation.Title.String
	}
	return string(observation.Type)
}
