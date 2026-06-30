package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/thebtf/engram/pkg/cognitive"
)

type statePlane interface {
	cognitive.StatePlane
}

type stateToolArgs struct {
	Action                  string `json:"action"`
	Project                 string `json:"project"`
	Principal               string `json:"principal"`
	SessionID               string `json:"session_id"`
	GoalID                  string `json:"goal_id"`
	TaskID                  string `json:"task_id"`
	AllowFilesystemFallback *bool  `json:"allow_filesystem_fallback"`
}

type setStateToolArgs struct {
	Action    string          `json:"action"`
	Project   string          `json:"project"`
	SessionID string          `json:"session_id"`
	State     json.RawMessage `json:"state"`
}

func resumeScopesFromFields(project, goalID, taskID string) []cognitive.StateScopeKind {
	scopes := []cognitive.StateScopeKind{cognitive.StateScopeSession}
	if strings.TrimSpace(project) != "" {
		scopes = append(scopes, cognitive.StateScopeProject)
	}
	if strings.TrimSpace(goalID) != "" {
		scopes = append(scopes, cognitive.StateScopeGoal)
	}
	if strings.TrimSpace(taskID) != "" {
		scopes = append(scopes, cognitive.StateScopeTask)
	}
	return scopes
}

func stateTool() Tool {
	return Tool{
		Name:        "get_state",
		Description: "Read Engram-native state-plane payloads. Actions: session, project, resume. Returns bounded native state from the server runtime path.",
		tier:        tierUseful,
		InputSchema: map[string]any{
			"type":     "object",
			"required": []string{"action"},
			"properties": map[string]any{
				"action":     map[string]any{"type": "string", "enum": []string{"session", "project", "resume"}, "description": "State read action"},
				"project":    map[string]any{"type": "string", "description": "Project identifier for project/resume reads"},
				"principal":  map[string]any{"type": "string", "description": "Requesting principal or agent identity for resume packet binding"},
				"session_id": map[string]any{"type": "string", "description": "Session identifier for session/resume reads"},
				"goal_id":    map[string]any{"type": "string", "description": "Optional goal identifier for resume packet binding"},
				"task_id":    map[string]any{"type": "string", "description": "Optional task identifier for resume packet binding"},
			},
			"allOf": []any{
				map[string]any{
					"if": map[string]any{
						"properties": map[string]any{"action": map[string]any{"const": "resume"}},
						"required":   []string{"action"},
					},
					"then": map[string]any{"required": []string{"principal", "session_id"}},
				},
			},
		},
	}
}

func setStateTool() Tool {
	return Tool{
		Name:        "set_state",
		Description: "Write Engram-native state-plane payloads through the agent-owned MCP seam. Actions: session, project. Returns a bounded native acknowledgement.",
		tier:        tierUseful,
		InputSchema: map[string]any{
			"type":     "object",
			"required": []string{"action", "state"},
			"properties": map[string]any{
				"action":     map[string]any{"type": "string", "enum": []string{"session", "project"}, "description": "State write action"},
				"session_id": map[string]any{"type": "string", "description": "Session identifier for session writes"},
				"project":    map[string]any{"type": "string", "description": "Project identifier for project writes"},
				"state": map[string]any{
					"type":        "object",
					"description": "Session writes require focus, execution, and horizons object fields. Project writes require phase, deadline_date, pressure, and updated_by fields; updated_by must be agent.",
					"properties": map[string]any{
						"focus":         map[string]any{"type": "object", "description": "Session focus slot"},
						"execution":     map[string]any{"type": "object", "description": "Session execution slot"},
						"horizons":      map[string]any{"type": "object", "description": "Session horizons slot"},
						"phase":         map[string]any{"type": "string", "description": "Project phase"},
						"deadline_date": map[string]any{"type": []string{"string", "null"}, "description": "Project deadline date as RFC3339 JSON time, or null"},
						"pressure":      map[string]any{"type": "string", "description": "Project pressure"},
						"updated_by":    map[string]any{"type": "string", "enum": []string{"agent"}, "description": "Writer attribution; must be agent"},
					},
				},
			},
			"allOf": []any{
				map[string]any{
					"if": map[string]any{
						"properties": map[string]any{"action": map[string]any{"const": "session"}},
						"required":   []string{"action"},
					},
					"then": map[string]any{
						"required": []string{"session_id"},
						"properties": map[string]any{
							"state": map[string]any{
								"required": []string{"focus", "execution", "horizons"},
							},
						},
					},
				},
				map[string]any{
					"if": map[string]any{
						"properties": map[string]any{"action": map[string]any{"const": "project"}},
						"required":   []string{"action"},
					},
					"then": map[string]any{
						"required": []string{"project"},
						"properties": map[string]any{
							"state": map[string]any{
								"required": []string{"phase", "deadline_date", "pressure", "updated_by"},
							},
						},
					},
				},
			},
		},
	}
}

func (s *Server) handleGetState(ctx context.Context, args json.RawMessage) (string, error) {
	if s.stateStore == nil {
		return "", fmt.Errorf("state store not available")
	}

	var a stateToolArgs
	if err := json.Unmarshal(args, &a); err != nil {
		return "", fmt.Errorf("parse args: %w", err)
	}

	switch a.Action {
	case "session":
		if a.SessionID == "" {
			return "", fmt.Errorf("session_id required")
		}
		state, err := s.stateStore.ReadSessionState(ctx, a.SessionID)
		if err != nil {
			return "", err
		}
		return marshalJSON(map[string]any{
			"source":     cognitive.StatePacketSourceNative,
			"session_id": a.SessionID,
			"state":      state,
		})
	case "project":
		if a.Project == "" {
			a.Project = projectFromContext(ctx)
		}
		if a.Project == "" {
			return "", fmt.Errorf("project required")
		}
		state, err := s.stateStore.ReadProjectState(ctx, a.Project)
		if err != nil {
			return "", err
		}
		return marshalJSON(map[string]any{
			"source":  cognitive.StatePacketSourceNative,
			"project": a.Project,
			"state":   state,
		})
	case "resume":
		a.SessionID = strings.TrimSpace(a.SessionID)
		if a.SessionID == "" {
			return "", fmt.Errorf("session_id required")
		}
		a.Project = strings.TrimSpace(a.Project)
		a.GoalID = strings.TrimSpace(a.GoalID)
		a.TaskID = strings.TrimSpace(a.TaskID)
		a.Principal = strings.TrimSpace(a.Principal)
		if a.Principal == "" {
			return "", fmt.Errorf("principal required")
		}
		if a.AllowFilesystemFallback != nil {
			return "", fmt.Errorf("allow_filesystem_fallback is not supported on the server runtime state path")
		}
		packet, err := s.stateStore.ReadResumePacket(ctx, cognitive.ResumePacketRequest{
			Project:   a.Project,
			Principal: a.Principal,
			SessionID: a.SessionID,
			GoalID:    a.GoalID,
			TaskID:    a.TaskID,
			Scopes:    resumeScopesFromFields(a.Project, a.GoalID, a.TaskID),
		})
		if err != nil {
			return "", err
		}
		return marshalJSON(packet)
	default:
		return "", fmt.Errorf("action must be one of session, project, resume")
	}
}

func (s *Server) handleSetState(ctx context.Context, args json.RawMessage) (string, error) {
	if s.stateStore == nil {
		return "", fmt.Errorf("state store not available")
	}

	var a setStateToolArgs
	if err := json.Unmarshal(args, &a); err != nil {
		return "", fmt.Errorf("parse args: %w", err)
	}

	switch a.Action {
	case "session":
		a.SessionID = strings.TrimSpace(a.SessionID)
		if a.SessionID == "" {
			return "", fmt.Errorf("session_id required")
		}
		state, err := decodeSessionStateForWrite(a.State)
		if err != nil {
			return "", err
		}
		if err := s.stateStore.WriteSessionState(ctx, a.SessionID, state); err != nil {
			return "", err
		}
		return marshalJSON(map[string]any{
			"source":     cognitive.StatePacketSourceNative,
			"action":     "session",
			"session_id": a.SessionID,
			"status":     "updated",
		})
	case "project":
		a.Project = strings.TrimSpace(a.Project)
		if a.Project == "" {
			return "", fmt.Errorf("project required")
		}
		state, err := decodeProjectStateForWrite(a.State)
		if err != nil {
			return "", err
		}
		if err := s.stateStore.WriteProjectState(ctx, a.Project, state); err != nil {
			return "", err
		}
		return marshalJSON(map[string]any{
			"source":  cognitive.StatePacketSourceNative,
			"action":  "project",
			"project": a.Project,
			"status":  "updated",
		})
	default:
		return "", fmt.Errorf("action must be one of session, project")
	}
}

func decodeSessionStateForWrite(raw json.RawMessage) (cognitive.SessionStateSlots, error) {
	fields, err := requireStateObject(raw, "focus", "execution", "horizons")
	if err != nil {
		return cognitive.SessionStateSlots{}, err
	}
	for _, field := range []string{"focus", "execution", "horizons"} {
		if err := requireNestedObject(fields[field], "state."+field); err != nil {
			return cognitive.SessionStateSlots{}, err
		}
	}

	var state cognitive.SessionStateSlots
	if err := json.Unmarshal(raw, &state); err != nil {
		return cognitive.SessionStateSlots{}, fmt.Errorf("state must match session state shape: %w", err)
	}
	return state, nil
}

func decodeProjectStateForWrite(raw json.RawMessage) (cognitive.ProjectStateRecord, error) {
	if _, err := requireStateObject(raw, "phase", "deadline_date", "pressure", "updated_by"); err != nil {
		return cognitive.ProjectStateRecord{}, err
	}

	var state cognitive.ProjectStateRecord
	if err := json.Unmarshal(raw, &state); err != nil {
		return cognitive.ProjectStateRecord{}, fmt.Errorf("state must match project state shape: %w", err)
	}
	if state.UpdatedBy != "agent" {
		return cognitive.ProjectStateRecord{}, fmt.Errorf("state.updated_by must be agent")
	}
	return state, nil
}

func requireStateObject(raw json.RawMessage, requiredFields ...string) (map[string]json.RawMessage, error) {
	if len(raw) == 0 {
		return nil, fmt.Errorf("state required")
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return nil, fmt.Errorf("state must be object: %w", err)
	}
	if fields == nil {
		return nil, fmt.Errorf("state must be object")
	}
	for _, field := range requiredFields {
		if _, ok := fields[field]; !ok {
			return nil, fmt.Errorf("state.%s required", field)
		}
	}
	return fields, nil
}

func requireNestedObject(raw json.RawMessage, name string) error {
	var fields map[string]any
	if err := json.Unmarshal(raw, &fields); err != nil {
		return fmt.Errorf("%s must be object: %w", name, err)
	}
	if fields == nil {
		return fmt.Errorf("%s must be object", name)
	}
	return nil
}
