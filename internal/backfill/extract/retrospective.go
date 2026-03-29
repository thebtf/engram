package extract

import (
	"encoding/xml"
	"strings"
)

// SessionRetrospective represents the parsed output of the retrospective prompt.
type SessionRetrospective struct {
	Summary             SessionSummary   `xml:"summary"`
	SessionObservations []XMLObservation `xml:"session_observations>observation"`
}

// SessionSummary contains the structured session summary fields.
type SessionSummary struct {
	Request   string `xml:"request"`
	Completed string `xml:"completed"`
	Learned   string `xml:"learned"`
	NextSteps string `xml:"next_steps"`
	Outcome   string `xml:"outcome"` // shipped|partial|blocked|investigation_only
}

// ParseRetrospective extracts a SessionRetrospective from raw LLM XML output.
// Returns nil if the output is empty or unparseable.
func ParseRetrospective(raw string) *SessionRetrospective {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}

	// Find the XML in the response (LLM may include preamble)
	start := strings.Index(raw, "<session_retrospective>")
	end := strings.Index(raw, "</session_retrospective>")
	if start == -1 || end == -1 {
		return nil
	}
	xmlStr := raw[start : end+len("</session_retrospective>")]

	var retro SessionRetrospective
	if err := xml.Unmarshal([]byte(xmlStr), &retro); err != nil {
		return nil
	}

	// Validate outcome
	validOutcomes := map[string]bool{
		"shipped": true, "partial": true, "blocked": true, "investigation_only": true,
	}
	if !validOutcomes[retro.Summary.Outcome] {
		retro.Summary.Outcome = "partial" // default
	}

	return &retro
}
