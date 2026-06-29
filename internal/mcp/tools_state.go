package mcp

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/thebtf/engram/pkg/cognitive"
)

type statePlane interface {
	cognitive.StatePlane
}

type stateToolArgs struct {
	Action                  string `json:"action"`
	Project                 string `json:"project"`
	SessionID               string `json:"session_id"`
	GoalID                  string `json:"goal_id"`
	TaskID                  string `json:"task_id"`
	AllowFilesystemFallback *bool  `json:"allow_filesystem_fallback"`
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
				"session_id": map[string]any{"type": "string", "description": "Session identifier for session/resume reads"},
				"goal_id":    map[string]any{"type": "string", "description": "Optional goal identifier for resume packet binding"},
				"task_id":    map[string]any{"type": "string", "description": "Optional task identifier for resume packet binding"},
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
	if a.Project == "" {
		a.Project = projectFromContext(ctx)
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
		if a.SessionID == "" {
			return "", fmt.Errorf("session_id required")
		}
		if a.AllowFilesystemFallback != nil {
			return "", fmt.Errorf("allow_filesystem_fallback is not supported on the server runtime state path")
		}
		packet, err := s.stateStore.ReadResumePacket(ctx, cognitive.ResumePacketRequest{
			Project:   a.Project,
			SessionID: a.SessionID,
			GoalID:    a.GoalID,
			TaskID:    a.TaskID,
		})
		if err != nil {
			return "", err
		}
		return marshalJSON(packet)
	default:
		return "", fmt.Errorf("action must be one of session, project, resume")
	}
}
