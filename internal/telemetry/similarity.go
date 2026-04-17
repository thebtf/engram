// Package telemetry provides measurement tools for engram's belief revision system.
// In v5, vector storage was removed. Similarity telemetry is a no-op.
package telemetry

import (
	"context"
	"encoding/json"
	"time"

	"github.com/rs/zerolog"
	"github.com/thebtf/engram/internal/db/gorm"
)

// SimilaritySnapshot holds the results of a similarity analysis run.
type SimilaritySnapshot struct {
	TotalPairs       int     `json:"total_pairs"`
	HighSimPairs     int     `json:"high_sim_pairs"`      // cosine > 0.85
	VeryHighSimPairs int     `json:"very_high_sim_pairs"` // cosine > 0.90
	HighSimPercent   float64 `json:"high_sim_percent"`
	VeryHighPercent  float64 `json:"very_high_sim_percent"`
	SampleSize       int     `json:"sample_size"`
}

// SimilarityTelemetry measures pairwise similarity overlap among observations.
// Vector-based analysis removed in v5; Run() is a no-op.
type SimilarityTelemetry struct {
	log   zerolog.Logger
	store *gorm.Store
}

// NewSimilarityTelemetry creates a new SimilarityTelemetry instance.
// The vectorClient parameter is accepted for call-site compatibility but ignored.
func NewSimilarityTelemetry(
	store *gorm.Store,
	observationStore *gorm.ObservationStore,
	vectorClient any,
	log zerolog.Logger,
) *SimilarityTelemetry {
	return &SimilarityTelemetry{
		store: store,
		log:   log.With().Str("component", "telemetry.similarity").Logger(),
	}
}

// Run is a no-op in v5 (vector storage removed).
func (st *SimilarityTelemetry) Run(_ context.Context) {
	st.log.Debug().Msg("Similarity telemetry skipped: vector storage removed in v5")
}

// storeSnapshot persists a similarity snapshot to the telemetry_snapshots table.
func (st *SimilarityTelemetry) storeSnapshot(ctx context.Context, project string, snapshot *SimilaritySnapshot) error {
	data, err := json.Marshal(snapshot)
	if err != nil {
		return err
	}

	return st.store.GetDB().WithContext(ctx).Exec(
		`INSERT INTO telemetry_snapshots (snapshot_type, project, data, created_at_epoch) VALUES (?, ?, ?, ?)`,
		"similarity", project, string(data), time.Now().UnixMilli(),
	).Error
}

// GetLatestSnapshot returns the most recent similarity snapshot for a project.
func (st *SimilarityTelemetry) GetLatestSnapshot(ctx context.Context, project string) (*SimilaritySnapshot, error) {
	var dataStr string
	err := st.store.GetDB().WithContext(ctx).
		Raw(`SELECT data FROM telemetry_snapshots WHERE snapshot_type = ? AND project = ? ORDER BY created_at_epoch DESC LIMIT 1`,
			"similarity", project).
		Scan(&dataStr).Error
	if err != nil {
		return nil, err
	}
	if dataStr == "" {
		return nil, nil
	}

	var snapshot SimilaritySnapshot
	if err := json.Unmarshal([]byte(dataStr), &snapshot); err != nil {
		return nil, err
	}
	return &snapshot, nil
}

// GetAllLatestSnapshots returns the most recent snapshot per project.
func (st *SimilarityTelemetry) GetAllLatestSnapshots(ctx context.Context) (map[string]*SimilaritySnapshot, error) {
	type row struct {
		Project string
		Data    string
	}
	var rows []row
	err := st.store.GetDB().WithContext(ctx).
		Raw(`SELECT DISTINCT ON (project) project, data FROM telemetry_snapshots WHERE snapshot_type = ? ORDER BY project, created_at_epoch DESC`,
			"similarity").
		Scan(&rows).Error
	if err != nil {
		return nil, err
	}

	result := make(map[string]*SimilaritySnapshot, len(rows))
	for _, r := range rows {
		var snapshot SimilaritySnapshot
		if err := json.Unmarshal([]byte(r.Data), &snapshot); err != nil {
			continue
		}
		result[r.Project] = &snapshot
	}
	return result, nil
}
