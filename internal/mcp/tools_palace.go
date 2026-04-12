package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/rs/zerolog/log"
	"github.com/thebtf/engram/internal/palace/aaak"
	"github.com/thebtf/engram/internal/palace/mining"
	"github.com/thebtf/engram/internal/palace/taxonomy"
	"github.com/thebtf/engram/internal/synthesis"
	"github.com/thebtf/engram/pkg/models"
)

// handleWakeUp returns AAAK-compressed identity + essential observations for fast session start.
// recall(action="wake_up", project="...")
func (s *Server) handleWakeUp(ctx context.Context, args json.RawMessage) (string, error) {
	m, _ := parseArgs(args)
	project := coerceString(m["project"], "")

	db := s.observationStore.GetDB()

	// Fetch top observations by importance (always-inject + high importance)
	var observations []struct {
		ID        int64
		Title     string
		Narrative string
		Type      string
	}

	q := db.WithContext(ctx).
		Table("observations").
		Select("id, COALESCE(title, '') as title, COALESCE(narrative, '') as narrative, type").
		Where("is_superseded = 0 AND is_archived = 0").
		Order("importance_score DESC").
		Limit(50)

	if project != "" {
		q = q.Where("project = ?", project)
	}

	if err := q.Find(&observations).Error; err != nil {
		return "", fmt.Errorf("wake_up: query observations: %w", err)
	}

	// Load entity codes for AAAK compression
	codes, _ := aaak.LookupCodes(ctx, db, project)

	meta := aaak.CompressMeta{
		EntityCodes: codes,
		Project:     project,
	}

	// Compress each observation into AAAK format
	var lines []string
	totalOrigTokens := 0
	totalCompTokens := 0

	for _, obs := range observations {
		text := obs.Title + ". " + obs.Narrative
		compressed := aaak.Compress(text, meta)
		if compressed == "" {
			continue
		}
		lines = append(lines, compressed)
		totalOrigTokens += len(text) / 4 // rough token estimate
		totalCompTokens += len(compressed) / 4
	}

	ratio := 0.0
	if totalCompTokens > 0 {
		ratio = float64(totalOrigTokens) / float64(totalCompTokens)
	}

	result := map[string]any{
		"essentials":        lines,
		"observation_count": len(observations),
		"compressed_lines":  len(lines),
		"token_count":       totalCompTokens,
		"compression_ratio": fmt.Sprintf("%.1fx", ratio),
	}

	out, _ := json.MarshalIndent(result, "", "  ")
	return string(out), nil
}

// handleTaxonomy returns the project→type→concept→count tree.
// recall(action="taxonomy", project="...")
func (s *Server) handleTaxonomy(ctx context.Context, args json.RawMessage) (string, error) {
	m, _ := parseArgs(args)
	project := coerceString(m["project"], "")

	db := s.observationStore.GetDB()
	nodes, err := taxonomy.GetTaxonomy(ctx, db, project)
	if err != nil {
		return "", fmt.Errorf("taxonomy: %w", err)
	}

	// Build tree structure
	type conceptEntry struct {
		Concept string `json:"concept"`
		Count   int    `json:"count"`
	}
	type typeEntry struct {
		Type     string         `json:"type"`
		Concepts []conceptEntry `json:"concepts"`
	}
	type projectEntry struct {
		Project string      `json:"project"`
		Types   []typeEntry `json:"types"`
	}

	treeMap := make(map[string]map[string][]conceptEntry)
	for _, n := range nodes {
		if treeMap[n.Project] == nil {
			treeMap[n.Project] = make(map[string][]conceptEntry)
		}
		treeMap[n.Project][n.Type] = append(treeMap[n.Project][n.Type], conceptEntry{n.Concept, n.Count})
	}

	var tree []projectEntry
	for proj, types := range treeMap {
		var typeList []typeEntry
		for t, concepts := range types {
			typeList = append(typeList, typeEntry{t, concepts})
		}
		tree = append(tree, projectEntry{proj, typeList})
	}

	out, _ := json.MarshalIndent(map[string]any{"tree": tree}, "", "  ")
	return string(out), nil
}

// handleTunnels finds concepts shared between two projects.
// recall(action="tunnels", project_a="...", project_b="...")
func (s *Server) handleTunnels(ctx context.Context, args json.RawMessage) (string, error) {
	m, _ := parseArgs(args)
	projectA := coerceString(m["project_a"], "")
	projectB := coerceString(m["project_b"], "")

	if projectA == "" || projectB == "" {
		return "", fmt.Errorf("tunnels: both project_a and project_b are required")
	}

	db := s.observationStore.GetDB()
	tunnels, err := taxonomy.GetTunnels(ctx, db, projectA, projectB)
	if err != nil {
		return "", fmt.Errorf("tunnels: %w", err)
	}

	out, _ := json.MarshalIndent(map[string]any{"tunnels": tunnels}, "", "  ")
	return string(out), nil
}

// handleMine extracts observations from text content without LLM API.
// store(action="mine", content="...", project="...", source_file="...")
func (s *Server) handleMine(ctx context.Context, args json.RawMessage) (string, error) {
	m, _ := parseArgs(args)
	content := coerceString(m["content"], "")
	project := coerceString(m["project"], "")
	sourceFile := coerceString(m["source_file"], "")

	if content == "" {
		return "", fmt.Errorf("mine: content is required")
	}

	results, err := mining.Mine(content, mining.MineOptions{
		Project:    project,
		SourceFile: sourceFile,
	})
	if err != nil {
		return "", fmt.Errorf("mine: %w", err)
	}

	stored := 0
	skipped := 0
	var conceptsDetected []string

	for _, r := range results {
		// Store each mined result as an observation
		if s.observationStore != nil {
			parsed := &models.ParsedObservation{
				Type:       models.ObservationType(r.Type),
				SourceType: models.SourceBackfill,
				Title:      truncateTitle(r.Text, 80),
				Narrative:  r.Text,
				Concepts:   r.Concepts,
			}

			mineSessionID := "mine-" + r.SourceHash[:12]
			_, _, storeErr := s.observationStore.StoreObservation(ctx, mineSessionID, project, parsed, 0, 0)
			if storeErr != nil {
				log.Warn().Err(storeErr).Msg("mine: failed to store observation")
				skipped++
				continue
			}
			stored++
		}
		conceptsDetected = append(conceptsDetected, r.Concepts...)
	}

	// Deduplicate concepts
	seen := make(map[string]bool)
	var uniqueConcepts []string
	for _, c := range conceptsDetected {
		if !seen[c] {
			seen[c] = true
			uniqueConcepts = append(uniqueConcepts, c)
		}
	}

	result := map[string]any{
		"observations_created": stored,
		"skipped_duplicates":   skipped,
		"total_mined":         len(results),
		"concepts_detected":   uniqueConcepts,
	}
	out, _ := json.MarshalIndent(result, "", "  ")
	return string(out), nil
}

// handleMineDirectory mines all supported files in a directory.
// store(action="mine_directory", path="...", project="...")
func (s *Server) handleMineDirectory(ctx context.Context, args json.RawMessage) (string, error) {
	m, _ := parseArgs(args)
	dirPath := coerceString(m["path"], "")
	project := coerceString(m["project"], "")

	if dirPath == "" {
		return "", fmt.Errorf("mine_directory: path is required")
	}

	// Security: validate path — reject traversal and symlinks
	absPath, err := filepath.Abs(dirPath)
	if err != nil {
		return "", fmt.Errorf("mine_directory: invalid path: %w", err)
	}
	if strings.Contains(dirPath, "..") {
		return "", fmt.Errorf("mine_directory: path traversal (..) not allowed")
	}

	// Verify it's a real directory (not symlink to outside scope)
	info, err := os.Lstat(absPath)
	if err != nil {
		return "", fmt.Errorf("mine_directory: %w", err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("mine_directory: %s is not a directory", absPath)
	}

	supportedExts := map[string]bool{
		".txt": true, ".md": true, ".json": true, ".jsonl": true,
	}

	filesProcessed := 0
	totalCreated := 0
	var errors []string

	entries, err := os.ReadDir(absPath)
	if err != nil {
		return "", fmt.Errorf("mine_directory: read dir: %w", err)
	}

	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		ext := strings.ToLower(filepath.Ext(entry.Name()))
		if !supportedExts[ext] {
			continue
		}

		// Security: reject symlinks
		entryInfo, err := os.Lstat(filepath.Join(absPath, entry.Name()))
		if err != nil || entryInfo.Mode()&os.ModeSymlink != 0 {
			continue
		}

		filePath := filepath.Join(absPath, entry.Name())
		content, err := os.ReadFile(filePath)
		if err != nil {
			errors = append(errors, fmt.Sprintf("%s: %s", entry.Name(), err.Error()))
			continue
		}

		// Mine the file content
		mineArgs, _ := json.Marshal(map[string]string{
			"content":     string(content),
			"project":     project,
			"source_file": entry.Name(),
		})
		result, err := s.handleMine(ctx, mineArgs)
		if err != nil {
			errors = append(errors, fmt.Sprintf("%s: %s", entry.Name(), err.Error()))
			continue
		}

		filesProcessed++

		// Parse result to get created count
		var parsed map[string]any
		if json.Unmarshal([]byte(result), &parsed) == nil {
			if created, ok := parsed["observations_created"].(float64); ok {
				totalCreated += int(created)
			}
		}
	}

	out, _ := json.MarshalIndent(map[string]any{
		"files_processed":      filesProcessed,
		"observations_created": totalCreated,
		"errors":               errors,
	}, "", "  ")
	return string(out), nil
}

// handleCompressAAK compresses a specific observation to AAAK format.
// admin(action="compress_aaak", observation_id=123)
func (s *Server) handleCompressAAK(ctx context.Context, args json.RawMessage) (string, error) {
	m, _ := parseArgs(args)
	obsID := coerceInt64(m["observation_id"], 0)

	if obsID <= 0 {
		return "", fmt.Errorf("compress_aaak: observation_id is required")
	}

	obs, err := s.observationStore.GetObservationByID(ctx, obsID)
	if err != nil {
		return "", fmt.Errorf("compress_aaak: %w", err)
	}

	db := s.observationStore.GetDB()
	codes, _ := aaak.LookupCodes(ctx, db, obs.Project)

	text := ""
	if obs.Title.Valid {
		text += obs.Title.String + ". "
	}
	if obs.Narrative.Valid {
		text += obs.Narrative.String
	}

	compressed := aaak.Compress(text, aaak.CompressMeta{
		EntityCodes: codes,
		Project:     obs.Project,
		Type:        string(obs.Type),
	})

	origTokens := len(text) / 4
	compTokens := len(compressed) / 4
	ratio := 0.0
	if compTokens > 0 {
		ratio = float64(origTokens) / float64(compTokens)
	}

	decoded, _ := aaak.Decode(compressed)
	entities := []string{}
	if decoded != nil {
		entities = decoded.Entities
	}

	out, _ := json.MarshalIndent(map[string]any{
		"aaak":              compressed,
		"compression_ratio": fmt.Sprintf("%.1fx", ratio),
		"entities":          entities,
		"original_tokens":   origTokens,
		"compressed_tokens": compTokens,
	}, "", "  ")
	return string(out), nil
}

// handleSetAAKCode manually sets an AAAK code for an entity.
// admin(action="set_aaak_code", entity="PostgreSQL", code="POS")
func (s *Server) handleSetAAKCode(ctx context.Context, args json.RawMessage) (string, error) {
	m, _ := parseArgs(args)
	entityName := coerceString(m["entity"], "")
	newCode := coerceString(m["code"], "")

	if entityName == "" || newCode == "" {
		return "", fmt.Errorf("set_aaak_code: entity and code are required")
	}
	if len(newCode) != 3 {
		return "", fmt.Errorf("set_aaak_code: code must be exactly 3 characters")
	}

	db := s.observationStore.GetDB()

	// Find entity observation
	var obsID int64
	var narrative string
	err := db.WithContext(ctx).
		Table("observations").
		Select("id, COALESCE(narrative, '') as narrative").
		Where("LOWER(title) = ? AND type = 'entity' AND is_superseded = 0", strings.ToLower(entityName)).
		Row().Scan(&obsID, &narrative)
	if err != nil || obsID == 0 {
		return "", fmt.Errorf("set_aaak_code: entity %q not found", entityName)
	}

	// Parse narrative as EntityMetadata JSON, update aaak_code, re-marshal.
	var meta synthesis.EntityMetadata
	oldCode := ""
	if err := json.Unmarshal([]byte(narrative), &meta); err != nil {
		// If narrative isn't valid EntityMetadata JSON, create minimal metadata
		meta = synthesis.EntityMetadata{}
	}
	oldCode = meta.AAKCode
	newCode = strings.ToUpper(newCode)
	meta.AAKCode = newCode
	updatedJSON, _ := json.Marshal(meta)
	narrative = string(updatedJSON)

	if err := db.WithContext(ctx).
		Table("observations").
		Where("id = ?", obsID).
		Update("narrative", narrative).Error; err != nil {
		return "", fmt.Errorf("set_aaak_code: update failed: %w", err)
	}

	out, _ := json.MarshalIndent(map[string]any{
		"entity":   entityName,
		"old_code": oldCode,
		"new_code": newCode,
	}, "", "  ")
	return string(out), nil
}

// handleTaxonomyStats returns aggregate taxonomy statistics.
// admin(action="taxonomy_stats")
func (s *Server) handleTaxonomyStats(ctx context.Context, _ json.RawMessage) (string, error) {
	db := s.observationStore.GetDB()
	stats, err := taxonomy.GetStats(ctx, db)
	if err != nil {
		return "", fmt.Errorf("taxonomy_stats: %w", err)
	}

	out, _ := json.MarshalIndent(stats, "", "  ")
	return string(out), nil
}

