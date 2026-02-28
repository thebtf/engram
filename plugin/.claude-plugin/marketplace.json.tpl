{
  "$schema": "https://anthropic.com/claude-code/marketplace.schema.json",
  "name": "engram",
  "version": "{{ .Version }}",
  "description": "Persistent memory for Claude Code — captures observations, stores knowledge across sessions, injects relevant context automatically",
  "owner": {
    "name": "thebtf"
  },
  "plugins": [
    {
      "name": "engram",
      "description": "Persistent memory system with PostgreSQL+pgvector backend and MCP integration",
      "version": "{{ .Version }}",
      "author": {
        "name": "thebtf"
      },
      "source": "./",
      "category": "productivity"
    }
  ]
}
