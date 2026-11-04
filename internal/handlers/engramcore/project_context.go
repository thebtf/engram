package engramcore

import (
	"bytes"
	"context"
	"encoding/json"
	"io"

	"github.com/thebtf/engram/internal/config"
	"github.com/thebtf/engram/internal/module"
	"github.com/thebtf/engram/internal/projectidentity"
	muxcore "github.com/thebtf/mcp-mux/muxcore"
)

// Tools registers recovery locally, before any canonical project is resolvable.
func (m *Module) Tools() []module.ToolDef {
	return []module.ToolDef{{
		Name:        "project_context",
		Description: "Choose an existing authorized work project for this actual caller/root. action=choices is read-only and returns current names, full project IDs, repository qualifiers and opaque selection_ref values. action=select requires an exact fresh selection_ref and persists only this caller/root/server binding; it does not change the historical marker, migrate data or grant permissions. Then retry the original operation unchanged. Never replay a mutation whose outcome is unknown.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"action":{"type":"string","enum":["choices","select"]},"selection_ref":{"type":"string","description":"Exact fresh opaque reference from choices; required for select, not a name or project UUID."}},"required":["action"],"additionalProperties":false}`),
	}}
}

// HandleTool is the local ToolProvider entry; recovery is never proxied as a
// backend mutation and never changes the caller's session or original arguments.
func (m *Module) HandleTool(ctx context.Context, project muxcore.ProjectContext, name string, args json.RawMessage) (json.RawMessage, error) {
	if name != "project_context" {
		return nil, &module.ModuleError{Code: "tool_not_found", Message: "unknown local Engram tool"}
	}
	action, selectionRef, err := projectContextArguments(args)
	if err != nil {
		return nil, err
	}
	serverURL, err := m.requireServerURL(project)
	if err != nil {
		return nil, projectIdentityRefusal(err, project.Cwd)
	}
	caller := projectidentity.ProjectContextCaller{
		Cwd: project.Cwd, CallerID: m.v3ClientInstanceID, ServerURL: serverURL, Token: m.envFor(project, config.EnvWorkstationToken),
	}
	var payload any
	if action == "choices" {
		payload, err = projectidentity.ProjectContextChoices(ctx, caller)
	} else {
		payload, err = projectidentity.SelectProjectContext(ctx, caller, selectionRef)
	}
	if err != nil {
		return nil, projectIdentityRefusal(err, project.Cwd)
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	return buildInnerBlock(raw)
}

func projectContextArguments(raw json.RawMessage) (string, string, error) {
	invalid := func() (string, string, error) {
		return "", "", &module.ModuleError{Code: "PROJECT_SELECTION_INVALID", Message: projectidentity.RecoveryGuidance("PROJECT_SELECTION_INVALID")}
	}
	if len(raw) > 4096 {
		return invalid()
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	if token, err := decoder.Token(); err != nil || token != json.Delim('{') {
		return invalid()
	}
	var action, selectionRef string
	var seen uint8
	for decoder.More() {
		key, err := decoder.Token()
		if err != nil {
			return invalid()
		}
		var field uint8
		switch key {
		case "action":
			field = 1
		case "selection_ref":
			field = 2
		default:
			return invalid()
		}
		if seen&field != 0 {
			return invalid()
		}
		seen |= field
		token, err := decoder.Token()
		value, ok := token.(string)
		if err != nil || !ok {
			return invalid()
		}
		if field == 1 {
			action = value
		} else {
			selectionRef = value
		}
	}
	if token, err := decoder.Token(); err != nil || token != json.Delim('}') {
		return invalid()
	}
	if decoder.Decode(&struct{}{}) != io.EOF || action != "choices" && action != "select" || action == "choices" && seen&2 != 0 || action == "select" && selectionRef == "" {
		return invalid()
	}
	return action, selectionRef, nil
}
