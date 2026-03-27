// Package gorm provides GORM-based database operations for engram.
package gorm

import (
	"context"
	"time"

	"gorm.io/gorm"
)

// AgentObservationStat tracks per-agent injection effectiveness for a single observation.
type AgentObservationStat struct {
	AgentID       string    `gorm:"column:agent_id;primaryKey"`
	ObservationID int64     `gorm:"column:observation_id;primaryKey"`
	Injections    int       `gorm:"column:injections"`
	Successes     int       `gorm:"column:successes"`
	UpdatedAt     time.Time `gorm:"column:updated_at"`
}

// TableName returns the database table name.
func (AgentObservationStat) TableName() string {
	return "agent_observation_stats"
}

// AgentStatsStore handles per-agent observation effectiveness tracking.
type AgentStatsStore struct {
	db *gorm.DB
}

// NewAgentStatsStore creates a new AgentStatsStore.
func NewAgentStatsStore(db *gorm.DB) *AgentStatsStore {
	return &AgentStatsStore{db: db}
}

// UpsertAgentStats increments the injection count and optionally the success count
// for an agent-observation pair. Uses INSERT ... ON CONFLICT DO UPDATE for atomicity.
func (s *AgentStatsStore) UpsertAgentStats(ctx context.Context, agentID string, observationID int64, success bool) error {
	successInc := 0
	if success {
		successInc = 1
	}
	return s.db.WithContext(ctx).Exec(`
		INSERT INTO agent_observation_stats (agent_id, observation_id, injections, successes, updated_at)
		VALUES (?, ?, 1, ?, NOW())
		ON CONFLICT (agent_id, observation_id)
		DO UPDATE SET injections = agent_observation_stats.injections + 1,
		              successes = agent_observation_stats.successes + ?,
		              updated_at = NOW()
	`, agentID, observationID, successInc, successInc).Error
}

// GetAgentStats returns a map of observation_id -> AgentObservationStat for the given
// agent and observation IDs. Returns nil (not an error) when observationIDs is empty.
func (s *AgentStatsStore) GetAgentStats(ctx context.Context, agentID string, observationIDs []int64) (map[int64]AgentObservationStat, error) {
	if len(observationIDs) == 0 {
		return nil, nil
	}
	var stats []AgentObservationStat
	err := s.db.WithContext(ctx).
		Where("agent_id = ? AND observation_id IN ?", agentID, observationIDs).
		Find(&stats).Error
	if err != nil {
		return nil, err
	}
	result := make(map[int64]AgentObservationStat, len(stats))
	for _, stat := range stats {
		result[stat.ObservationID] = stat
	}
	return result, nil
}

// GetAgentEffectiveness returns effectiveness stats for a specific agent and observation.
// Returns (nil, nil) when no record exists (gorm.ErrRecordNotFound is swallowed).
func (s *AgentStatsStore) GetAgentEffectiveness(ctx context.Context, agentID string, observationID int64) (*AgentObservationStat, error) {
	var stat AgentObservationStat
	err := s.db.WithContext(ctx).
		Where("agent_id = ? AND observation_id = ?", agentID, observationID).
		First(&stat).Error
	if err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, nil
		}
		return nil, err
	}
	return &stat, nil
}
