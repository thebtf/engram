package synthesis

// entityExtractionSystemPrompt is the system prompt for entity extraction.
const entityExtractionSystemPrompt = "You are a precise knowledge extraction system. Extract structured entities and relations from technical observations. Output ONLY valid JSON, no markdown, no explanation."

// entityExtractionPromptTemplate is the user prompt template for entity extraction.
// %s is replaced with the formatted observation list.
const entityExtractionPromptTemplate = `Extract entities, relations, and a summary from these observations.
Reply ONLY with valid JSON matching this schema:
{
  "entities": [{"name": "string", "type": "technology|person|project|concept|file", "description": "1 sentence"}],
  "relations": [{"from": "entity name", "to": "entity name", "rel": "uses|built_on|part_of|depends_on|related|alternative"}],
  "summary": "2-3 sentence synthesis of the key theme"
}

Rules:
- Extract 2-8 entities per batch
- Normalize entity names (e.g., "PostgreSQL" not "postgres" or "PG")
- Use only the listed relation types
- Description must be a single concise sentence
- Summary must be a standalone paragraph

Observations:
%s`

// wikiGenerationSystemPrompt is the system prompt for wiki page generation.
const wikiGenerationSystemPrompt = "You are a technical writer creating concise knowledge base articles. Write clear, factual summaries based only on the provided observations. Do not invent information."

// wikiGenerationPromptTemplate is the user prompt template for wiki page generation.
// First %s is the entity name/description, second %s is the formatted observations.
const wikiGenerationPromptTemplate = `Write a 2-4 paragraph wiki summary for the entity described below.

Cover:
1. What it is and its role
2. How it is used in this project
3. Key decisions or patterns related to it
4. Known issues or gotchas (if any)

Write in plain text. No markdown headers, no bullet lists. Just clear paragraphs.

Entity: %s

Source observations:
%s`
