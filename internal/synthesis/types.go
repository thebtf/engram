// Package synthesis implements entity extraction and wiki generation
// from observations during the maintenance cycle.
package synthesis

// Entity represents a structured entity extracted from observations.
type Entity struct {
	Name        string `json:"name"`
	Type        string `json:"type"`        // technology, person, project, concept, file
	Description string `json:"description"`
}

// Relation represents a typed relation between two entities.
type Relation struct {
	From    string `json:"from"`
	To      string `json:"to"`
	RelType string `json:"rel"`
}

// ExtractionResult holds the LLM entity extraction output.
type ExtractionResult struct {
	Entities  []Entity  `json:"entities"`
	Relations []Relation `json:"relations"`
	Summary   string    `json:"summary"`
}

// WikiResult holds the LLM wiki generation output.
type WikiResult struct {
	EntityName string `json:"entity_name"`
	Content    string `json:"content"`
}

// EntityMetadata is the JSONB metadata stored on entity-type observations.
type EntityMetadata struct {
	EntityType           string           `json:"entity_type"`
	ObservationCount     int              `json:"observation_count"`
	Relations            []EntityRelation `json:"relations"`
	SourceObservationIDs []int64          `json:"source_observation_ids"`
	LastExtracted        string           `json:"last_extracted"` // ISO 8601
}

// EntityRelation is a lightweight relation stored in entity metadata.
type EntityRelation struct {
	To  string `json:"to"`
	Rel string `json:"rel"`
}

// WikiMetadata is the JSONB metadata stored on wiki-type observations.
type WikiMetadata struct {
	EntityName    string `json:"entity_name"`
	EntityID      int64  `json:"entity_id"`
	SourceCount   int    `json:"source_count"`
	LastGenerated string `json:"last_generated"` // ISO 8601
	WikiFile      string `json:"wiki_file"`
}

// WikiIndexEntry represents one entry in the wiki index.md file.
type WikiIndexEntry struct {
	EntityName  string `json:"entity_name"`
	EntityType  string `json:"entity_type"`
	Slug        string `json:"slug"`
	SourceCount int    `json:"source_count"`
}
