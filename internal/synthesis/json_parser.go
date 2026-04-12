package synthesis

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

// thinkBlockRE matches <think>...</think> and <|channel>thought blocks.
var thinkBlockRE = regexp.MustCompile(`(?s)<think>.*?</think>|<\|channel>thought.*?(?:<\|channel>|$)`)

// jsonObjectRE extracts the first JSON object from a string.
var jsonObjectRE = regexp.MustCompile(`(?s)\{.*\}`)

// ParseEntityExtractionResponse parses the LLM response for entity extraction.
// It handles common LLM output issues: think blocks, markdown fences, and malformed JSON.
func ParseEntityExtractionResponse(raw string) (*ExtractionResult, error) {
	cleaned := cleanLLMOutput(raw)

	// Attempt 1: direct JSON parse
	var result ExtractionResult
	if err := json.Unmarshal([]byte(cleaned), &result); err == nil {
		return validateExtractionResult(&result)
	}

	// Attempt 2: extract JSON object via regex
	match := jsonObjectRE.FindString(cleaned)
	if match == "" {
		return nil, fmt.Errorf("no JSON object found in LLM response")
	}

	if err := json.Unmarshal([]byte(match), &result); err != nil {
		return nil, fmt.Errorf("failed to parse extracted JSON: %w", err)
	}

	return validateExtractionResult(&result)
}

// cleanLLMOutput strips think blocks, markdown fences, and leading/trailing whitespace.
func cleanLLMOutput(raw string) string {
	// Remove think blocks
	cleaned := thinkBlockRE.ReplaceAllString(raw, "")

	// Remove markdown code fences
	cleaned = strings.ReplaceAll(cleaned, "```json", "")
	cleaned = strings.ReplaceAll(cleaned, "```", "")

	return strings.TrimSpace(cleaned)
}

// validateExtractionResult ensures the parsed result has valid content.
func validateExtractionResult(result *ExtractionResult) (*ExtractionResult, error) {
	if len(result.Entities) == 0 {
		return nil, fmt.Errorf("extraction produced zero entities")
	}

	// Filter out entities with empty names
	valid := make([]Entity, 0, len(result.Entities))
	for _, e := range result.Entities {
		name := strings.TrimSpace(e.Name)
		if name == "" {
			continue
		}
		valid = append(valid, Entity{
			Name:        name,
			Type:        normalizeEntityType(strings.TrimSpace(e.Type)),
			Description: strings.TrimSpace(e.Description),
		})
	}

	if len(valid) == 0 {
		return nil, fmt.Errorf("all extracted entities have empty names")
	}

	result.Entities = valid

	// Validate relations reference known entity names
	entityNames := make(map[string]bool, len(valid))
	for _, e := range valid {
		entityNames[strings.ToLower(e.Name)] = true
	}

	validRels := make([]Relation, 0, len(result.Relations))
	for _, r := range result.Relations {
		from := strings.TrimSpace(r.From)
		to := strings.TrimSpace(r.To)
		if from == "" || to == "" {
			continue
		}
		if !entityNames[strings.ToLower(from)] && !entityNames[strings.ToLower(to)] {
			continue // skip relations where NEITHER endpoint is a known entity (at least one must be)
		}
		validRels = append(validRels, Relation{
			From:    from,
			To:      to,
			RelType: normalizeRelationType(strings.TrimSpace(r.RelType)),
		})
	}
	result.Relations = validRels

	return result, nil
}

// normalizeEntityType maps entity type strings to canonical values.
func normalizeEntityType(t string) string {
	switch strings.ToLower(t) {
	case "technology", "tech", "library", "framework", "tool", "database":
		return "technology"
	case "person", "user", "developer":
		return "person"
	case "project", "repo", "repository", "service":
		return "project"
	case "concept", "pattern", "principle", "practice":
		return "concept"
	case "file", "module", "package", "directory":
		return "file"
	default:
		return "concept" // safe default
	}
}

// normalizeRelationType maps relation type strings to the canonical vocabulary.
func normalizeRelationType(r string) string {
	switch strings.ToLower(r) {
	case "uses", "use":
		return "uses"
	case "built_on", "builton", "based_on":
		return "built_on"
	case "part_of", "partof", "belongs_to":
		return "part_of"
	case "depends_on", "dependson", "requires":
		return "depends_on"
	case "related", "related_to":
		return "related"
	case "alternative", "alternative_to":
		return "alternative"
	default:
		return "related" // safe default
	}
}
