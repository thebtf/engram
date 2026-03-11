// Package telemetry provides measurement tools for engram's belief revision system.
package telemetry

import (
	"context"
	"encoding/json"
	"time"

	"github.com/rs/zerolog"
	"github.com/thebtf/engram/internal/db/gorm"
	"github.com/thebtf/engram/internal/vector"
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
type SimilarityTelemetry struct {
	log              zerolog.Logger
	store            *gorm.Store
	observationStore *gorm.ObservationStore
	vectorClient     vector.Client
	sampleSize       int
}

// NewSimilarityTelemetry creates a new SimilarityTelemetry instance.
func NewSimilarityTelemetry(
	store *gorm.Store,
	observationStore *gorm.ObservationStore,
	vectorClient vector.Client,
	log zerolog.Logger,
) *SimilarityTelemetry {
	return &SimilarityTelemetry{
		store:            store,
		observationStore: observationStore,
		vectorClient:     vectorClient,
		log:              log.With().Str("component", "telemetry.similarity").Logger(),
		sampleSize:       50,
	}
}

// Run executes the similarity telemetry analysis for all projects.
func (st *SimilarityTelemetry) Run(ctx context.Context) {
	if st.vectorClient == nil || !st.vectorClient.IsConnected() {
		st.log.Warn().Msg("Vector client not available, skipping similarity telemetry")
		return
	}

	projects, err := st.getActiveProjects(ctx)
	if err != nil {
		st.log.Error().Err(err).Msg("Failed to get projects for similarity telemetry")
		return
	}

	for _, project := range projects {
		snapshot, err := st.analyzeProject(ctx, project)
		if err != nil {
			st.log.Error().Err(err).Str("project", project).Msg("Failed to analyze project similarity")
			continue
		}

		if err := st.storeSnapshot(ctx, project, snapshot); err != nil {
			st.log.Error().Err(err).Str("project", project).Msg("Failed to store similarity snapshot")
			continue
		}

		st.log.Info().
			Str("project", project).
			Int("sample_size", snapshot.SampleSize).
			Int("total_pairs", snapshot.TotalPairs).
			Float64("high_sim_pct", snapshot.HighSimPercent).
			Float64("very_high_sim_pct", snapshot.VeryHighPercent).
			Msg("Similarity telemetry completed for project")
	}
}

// analyzeProject samples recent observations and measures pairwise similarity.
func (st *SimilarityTelemetry) analyzeProject(ctx context.Context, project string) (*SimilaritySnapshot, error) {
	observations, err := st.observationStore.GetRecentObservations(ctx, project, st.sampleSize)
	if err != nil {
		return nil, err
	}

	if len(observations) < 2 {
		return &SimilaritySnapshot{SampleSize: len(observations)}, nil
	}

	where := vector.BuildWhereFilter(vector.DocTypeObservation, project, false)

	highSimPairs := 0
	veryHighSimPairs := 0
	totalPairs := 0

	for i, obs := range observations {
		queryText := ""
		if obs.Title.Valid {
			queryText = obs.Title.String
		}
		if obs.Narrative.Valid {
			if queryText != "" {
				queryText += " "
			}
			queryText += obs.Narrative.String
		}
		if queryText == "" {
			continue
		}

		results, err := st.vectorClient.Query(ctx, queryText, st.sampleSize, where)
		if err != nil {
			st.log.Debug().Err(err).Int64("obs_id", obs.ID).Msg("Vector query failed for observation")
			continue
		}

		for _, result := range results {
			resultID := vector.ExtractRowID(result.Metadata)
			if resultID <= obs.ID {
				continue
			}
			// Only count pairs within our sample
			inSample := false
			for j := i + 1; j < len(observations); j++ {
				if observations[j].ID == resultID {
					inSample = true
					break
				}
			}
			if !inSample {
				continue
			}

			totalPairs++
			if result.Similarity > 0.85 {
				highSimPairs++
			}
			if result.Similarity > 0.90 {
				veryHighSimPairs++
			}
		}
	}

	// If vector search returned no pair counts, calculate theoretical maximum
	if totalPairs == 0 {
		n := len(observations)
		totalPairs = n * (n - 1) / 2
	}

	highSimPct := 0.0
	veryHighPct := 0.0
	if totalPairs > 0 {
		highSimPct = float64(highSimPairs) / float64(totalPairs) * 100
		veryHighPct = float64(veryHighSimPairs) / float64(totalPairs) * 100
	}

	return &SimilaritySnapshot{
		TotalPairs:       totalPairs,
		HighSimPairs:     highSimPairs,
		VeryHighSimPairs: veryHighSimPairs,
		HighSimPercent:   highSimPct,
		VeryHighPercent:  veryHighPct,
		SampleSize:       len(observations),
	}, nil
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

// getActiveProjects returns all projects that have recent observations.
func (st *SimilarityTelemetry) getActiveProjects(ctx context.Context) ([]string, error) {
	var projects []string
	err := st.store.GetDB().WithContext(ctx).
		Model(&gorm.Observation{}).
		Distinct("project").
		Where("project != ''").
		Pluck("project", &projects).Error
	return projects, err
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
