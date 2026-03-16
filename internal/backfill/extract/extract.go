// Package extract provides LLM-based observation extraction and XML validation
// for session backfill.
package extract

import (
	"encoding/xml"
	"fmt"
	"regexp"
	"strings"

	"github.com/thebtf/engram/pkg/models"
)

// xmlTagRe matches XML-like tags to prevent prompt boundary injection.
var xmlTagRe = regexp.MustCompile(`</?(?:session_transcript|observations?|metadata|no_observations_found)[^>]*>`)

// SystemPrompt is the frozen poc-v1 extraction prompt for historical sessions.
const SystemPrompt = `You are an expert Principal Staff Engineer responsible for maintaining the permanent architectural memory of a project.
Your job is to read historical coding session transcripts and extract highly valuable, reusable observations.

CRITICAL RULES:
1. HIGH SIGNAL ONLY: 85% of coding is routine (fixing typos, bumping deps, basic CRUD). DO NOT extract routine work. If the session contains no novel architectural decisions, complex debugging arcs, or reusable patterns, output strictly: <no_observations_found/>
2. OUTCOME CLASSIFICATION: You must determine if an approach actually worked. Classify every observation's <outcome> as:
   - active_pattern: The solution worked and was adopted.
   - failed_experiment: The approach was tried, caused errors, and was reverted/abandoned.
   - superseded: An approach was implemented but later replaced by a better one in the same session.
3. REDACTION: You MUST redact all IP addresses, API keys, passwords, customer names, and internal hostnames. Replace them with [REDACTED].
4. LIMIT: Extract a maximum of 2 observations per chunk. Quality over quantity — consolidate related learnings into single, comprehensive narratives.
5. XML EXACTNESS: Output ONLY valid XML in the format provided. No markdown blocks, no conversational preamble, no trailing text.
6. CONCEPTS: Use only <concept> tags inside <concepts>. Do NOT use <code> or any other tag name.`

// UserPromptTemplate is the template for building user prompts.
// Format args: projectPath, gitBranch, durationMin, exchangeCount, chunkInfo, alreadyExtracted, transcript.
const UserPromptTemplate = `Analyze the following historical coding session and extract persistent knowledge.

<metadata>
  <project_path>%s</project_path>
  <git_branch>%s</git_branch>
  <duration_minutes>%d</duration_minutes>
  <total_exchanges>%d</total_exchanges>
  <chunk_info>%s</chunk_info>
</metadata>
%s
<session_transcript>
%s
</session_transcript>

Extract the high-value observations using this exact XML format. Return ONLY the XML, nothing else.

If nothing worth remembering: <no_observations_found/>

Otherwise:
<observations>
  <observation>
    <type>decision|bugfix|feature|refactor|discovery|change</type>
    <outcome>active_pattern|failed_experiment|superseded</outcome>
    <title>Clear, searchable title</title>
    <narrative>
      Detailed explanation. What was the problem? What was the context?
      Why was this specific solution chosen? What failed before this worked?
    </narrative>
    <concepts>
      <concept>how-it-works</concept>
    </concepts>
    <files>
      <file>internal/db/pool.go</file>
    </files>
  </observation>
</observations>`

// ValidTypes contains allowed observation type values.
var ValidTypes = map[string]bool{
	"decision": true, "bugfix": true, "feature": true,
	"refactor": true, "discovery": true, "change": true, "design": true,
}

// ValidOutcomes contains allowed outcome classification values.
var ValidOutcomes = map[string]bool{
	"active_pattern": true, "failed_experiment": true, "superseded": true,
}

// XMLObservation represents a single observation parsed from LLM XML output.
type XMLObservation struct {
	Type      string      `xml:"type"`
	Outcome   string      `xml:"outcome"`
	Title     string      `xml:"title"`
	Narrative string      `xml:"narrative"`
	Concepts  xmlConcepts `xml:"concepts"`
	Files     xmlFiles    `xml:"files"`
}

type xmlObservations struct {
	XMLName      xml.Name         `xml:"observations"`
	Observations []XMLObservation `xml:"observation"`
}

type xmlConcepts struct {
	Concepts []string `xml:"concept"`
}

type xmlFiles struct {
	Files []string `xml:"file"`
}

// ValidationResult holds the result of validating LLM XML output.
type ValidationResult struct {
	ObservationCount int
	ValidCount       int
	Errors           []string
	IsNoObservations bool
	IsMalformedXML   bool
	Observations     []XMLObservation
}

// ValidateXML parses and validates the XML output from the LLM.
func ValidateXML(raw string) ValidationResult {
	raw = strings.TrimSpace(raw)

	// Strip markdown code fences if present
	if strings.HasPrefix(raw, "```") {
		lines := strings.Split(raw, "\n")
		start := 1
		end := len(lines) - 1
		if end > start && strings.HasPrefix(lines[end], "```") {
			raw = strings.Join(lines[start:end], "\n")
		}
	}

	result := ValidationResult{}

	// Check for no_observations_found
	if strings.Contains(raw, "<no_observations_found") {
		result.IsNoObservations = true
		return result
	}

	var obs xmlObservations
	if err := xml.Unmarshal([]byte(raw), &obs); err != nil {
		result.IsMalformedXML = true
		result.Errors = append(result.Errors, fmt.Sprintf("XML parse error: %v", err))
		return result
	}

	result.ObservationCount = len(obs.Observations)
	result.Observations = obs.Observations

	for i, o := range obs.Observations {
		valid := true
		prefix := fmt.Sprintf("observation[%d]", i)

		if !ValidTypes[o.Type] {
			result.Errors = append(result.Errors, fmt.Sprintf("%s: invalid type %q", prefix, o.Type))
			valid = false
		}
		if !ValidOutcomes[o.Outcome] {
			result.Errors = append(result.Errors, fmt.Sprintf("%s: invalid outcome %q", prefix, o.Outcome))
			valid = false
		}
		if strings.TrimSpace(o.Title) == "" {
			result.Errors = append(result.Errors, fmt.Sprintf("%s: empty title", prefix))
			valid = false
		}
		if len(strings.TrimSpace(o.Narrative)) < 50 {
			result.Errors = append(result.Errors, fmt.Sprintf("%s: narrative too short (%d chars)", prefix, len(strings.TrimSpace(o.Narrative))))
			valid = false
		}
		if len(o.Concepts.Concepts) == 0 {
			result.Errors = append(result.Errors, fmt.Sprintf("%s: no concepts", prefix))
			valid = false
		}

		if valid {
			result.ValidCount++
		}
	}

	return result
}

// BuildUserPrompt constructs the user prompt for LLM extraction.
// Transcript content is sanitized to prevent XML tag injection (AG06-005).
func BuildUserPrompt(projectPath, gitBranch string, durationMin, exchangeCount int, chunkInfo, alreadyExtracted, transcript string) string {
	// Replace XML tags that could break prompt boundaries with bracketed equivalents
	sanitized := xmlTagRe.ReplaceAllStringFunc(transcript, func(tag string) string {
		return strings.ReplaceAll(strings.ReplaceAll(tag, "<", "["), ">", "]")
	})
	return fmt.Sprintf(UserPromptTemplate, projectPath, gitBranch, durationMin, exchangeCount, chunkInfo, alreadyExtracted, sanitized)
}

// BuildAlreadyExtracted builds the <already_extracted> context block for multi-chunk dedup.
func BuildAlreadyExtracted(titles []string) string {
	if len(titles) == 0 {
		return ""
	}
	var buf strings.Builder
	buf.WriteString("\n<already_extracted>\nDo NOT re-extract these topics (already covered in previous chunks):\n")
	for _, t := range titles {
		buf.WriteString("- " + t + "\n")
	}
	buf.WriteString("</already_extracted>\n")
	return buf.String()
}

// typeToObservationType maps XML type strings to models.ObservationType.
var typeToObservationType = map[string]models.ObservationType{
	"decision":  models.ObsTypeDecision,
	"bugfix":    models.ObsTypeBugfix,
	"feature":   models.ObsTypeFeature,
	"refactor":  models.ObsTypeRefactor,
	"discovery": models.ObsTypeDiscovery,
	"change":    models.ObsTypeChange,
}

// ConvertToObservation converts an XMLObservation to a ParsedObservation.
func ConvertToObservation(xo XMLObservation, project string) *models.ParsedObservation {
	obsType, ok := typeToObservationType[xo.Type]
	if !ok {
		obsType = models.ObsTypeDiscovery
	}

	// Classify memory type from concepts (reuse existing logic)
	memType := models.MemTypeInsight
	if xo.Type == "decision" {
		memType = models.MemTypeDecision
	}

	return &models.ParsedObservation{
		Type:       obsType,
		MemoryType: memType,
		SourceType: models.SourceBackfill,
		Title:      strings.TrimSpace(xo.Title),
		Narrative:  strings.TrimSpace(xo.Narrative),
		Concepts:   xo.Concepts.Concepts,
		FilesRead:  xo.Files.Files,
	}
}
