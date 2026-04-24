package worker

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	"github.com/thebtf/engram/internal/privacy"
	"github.com/thebtf/engram/pkg/models"
)

const repeatedReadThreshold = 3
const repeatedReadCandidateLimit = 20
const repeatedReadResultLimit = 3

func (s *Service) matchBashCommandTriggers(ctx context.Context, req MemoryTriggerRequest) ([]MemoryTriggerMatch, error) {
	_ = ctx
	command := extractTriggerCommand(req.Params)
	if command == "" {
		return []MemoryTriggerMatch{}, nil
	}
	if privacy.ContainsSecrets(command) {
		return []MemoryTriggerMatch{}, nil
	}

	// Observation-backed command trigger matching was removed in v5.
	return []MemoryTriggerMatch{}, nil
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

	// Observation-backed file path lookups were removed in v5.
	return []*models.Observation{}, nil
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

func (s *Service) matchEditWriteSemanticTriggers(_ context.Context, _ MemoryTriggerRequest) ([]MemoryTriggerMatch, error) {
	// Vector search removed in v5 (content_chunks table dropped). No semantic triggers.
	return []MemoryTriggerMatch{}, nil
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
