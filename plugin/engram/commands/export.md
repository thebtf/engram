# Export Observations

Export engram observations in human-readable or machine-readable format.

## Instructions

### 1. Ask Export Parameters

Prompt the user for:

- **Scope**: all projects or specific project (default: current project)
- **Format**: markdown (default), json, jsonl
- **Type filter**: all types or specific type (decision, bugfix, feature, etc.)
- **Output**: console (default) or save to file

If the user provides no arguments, use defaults: current project, markdown, all types, console output.

### 2. Execute Export

Call:
```
Tool: admin(action="export", project="{project}", format="{format}")
```

If type filter specified, pass the type parameter as well.

### 3. Format Output

#### Markdown format (default)
The export tool returns formatted markdown. Display it directly.

If saving to file, write to `{project}-engram-export-{date}.md`.

#### JSON format
Display raw JSON. If saving to file, write to `{project}-engram-export-{date}.json`.

#### JSONL format
Display one observation per line. If saving to file, write to `{project}-engram-export-{date}.jsonl`.

### 4. Large Export Warning

If the export contains more than 100 observations, warn before displaying:

```
Export contains {count} observations ({estimated_lines} lines).
Display in console or save to file?
  [C] Console (may be long)
  [F] Save to file: {suggested_filename}
```

### 5. Report Summary

```
Engram Export:
  Observations: {count}
  Format:       {format}
  Scope:        {project or "all"}
  Type:         {type or "all"}
  Output:       {console or file_path}
```
