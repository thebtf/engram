package loom

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	loom "github.com/thebtf/aimux/loom"
	"github.com/thebtf/engram/internal/module"
	muxcore "github.com/thebtf/mcp-mux/muxcore"
)

// Tool names exposed by the loom module.
const (
	toolLoomSubmit = "loom_submit"
	toolLoomGet    = "loom_get"
	toolLoomList   = "loom_list"
	toolLoomCancel = "loom_cancel"
)

// pre-marshalled JSON schemas for each tool (draft-07 compatible).
// Defined as raw strings to avoid an allocation on every Tools() call.
var (
	schemaLoomSubmit = json.RawMessage(`{
  "type": "object",
  "required": ["worker_type", "prompt", "cli"],
  "additionalProperties": false,
  "properties": {
    "worker_type": {
      "type": "string",
      "enum": ["cli"],
      "description": "Worker type. v4.4.0 only supports 'cli'."
    },
    "prompt": {
      "type": "string",
      "minLength": 1,
      "description": "The full task prompt; passed to the worker on stdin."
    },
    "cli": {
      "type": "string",
      "description": "Allowlisted binary name. v4.4.0 allows: codex, claude, aimux."
    },
    "cwd": {
      "type": "string",
      "description": "Working directory for the subprocess. Optional."
    },
    "env": {
      "type": "object",
      "additionalProperties": {"type": "string"},
      "description": "Environment variables to merge over the daemon's env."
    },
    "model": {
      "type": "string",
      "description": "Model identifier passed to the CLI as --model. Optional."
    },
    "role": {
      "type": "string",
      "description": "Role identifier passed to the CLI as --role. Optional."
    },
    "effort": {
      "type": "string",
      "description": "Effort level passed to the CLI as --effort. Optional."
    },
    "timeout_sec": {
      "type": "integer",
      "minimum": 0,
      "default": 0,
      "description": "Task timeout in seconds. 0 = no timeout."
    },
    "metadata": {
      "type": "object",
      "description": "Free-form metadata stored on the task. Not interpreted."
    }
  }
}`)

	schemaLoomGet = json.RawMessage(`{
  "type": "object",
  "required": ["task_id"],
  "additionalProperties": false,
  "properties": {
    "task_id": {"type": "string", "minLength": 1}
  }
}`)

	schemaLoomList = json.RawMessage(`{
  "type": "object",
  "additionalProperties": false,
  "properties": {
    "statuses": {
      "type": "array",
      "items": {
        "type": "string",
        "enum": [
          "pending",
          "dispatched",
          "running",
          "completed",
          "failed",
          "failed_crash",
          "retrying",
          "cancelled"
        ]
      }
    }
  }
}`)

	schemaLoomCancel = json.RawMessage(`{
  "type": "object",
  "required": ["task_id"],
  "additionalProperties": false,
  "properties": {
    "task_id": {"type": "string", "minLength": 1}
  }
}`)
)

// Tools returns the 4 static tool definitions for the loom module.
// Implements module.ToolProvider.
func (m *Module) Tools() []module.ToolDef {
	return []module.ToolDef{
		{
			Name:        toolLoomSubmit,
			Description: "Submit a background task to the loom engine. Returns a task ID; use loom_get to poll status.",
			InputSchema: schemaLoomSubmit,
		},
		{
			Name:        toolLoomGet,
			Description: "Get the current state of a loom task by ID.",
			InputSchema: schemaLoomGet,
		},
		{
			Name:        toolLoomList,
			Description: "List loom tasks for the current project. Optionally filter by status.",
			InputSchema: schemaLoomList,
		},
		{
			Name:        toolLoomCancel,
			Description: "Cancel a running loom task. Returns cancelled:true if the task was running, cancelled:false if it was already terminal.",
			InputSchema: schemaLoomCancel,
		},
	}
}

// HandleTool dispatches a tool call to the appropriate engine method.
// Implements module.ToolProvider.
//
// Security invariant: the project scope is ALWAYS derived from p.ID (the
// authenticated session's project). Any client-supplied project_id field is
// silently ignored — the caller cannot escalate to another project's tasks.
func (m *Module) HandleTool(ctx context.Context, p muxcore.ProjectContext, name string, args json.RawMessage) (json.RawMessage, error) {
	switch name {
	case toolLoomSubmit:
		return m.handleLoomSubmit(ctx, p, args)
	case toolLoomGet:
		return m.handleLoomGet(p, args)
	case toolLoomList:
		return m.handleLoomList(p, args)
	case toolLoomCancel:
		return m.handleLoomCancel(p, args)
	default:
		return nil, &module.ModuleError{
			Code:    "tool_not_found",
			Message: fmt.Sprintf("unknown tool: %s", name),
		}
	}
}

// ---------------------------------------------------------------------------
// loom_submit
// ---------------------------------------------------------------------------

type submitArgs struct {
	WorkerType string            `json:"worker_type"`
	Prompt     string            `json:"prompt"`
	CLI        string            `json:"cli"`
	CWD        string            `json:"cwd"`
	Env        map[string]string `json:"env"`
	Model      string            `json:"model"`
	Role       string            `json:"role"`
	Effort     string            `json:"effort"`
	TimeoutSec int               `json:"timeout_sec"`
	Metadata   map[string]any    `json:"metadata"`
}

func (m *Module) handleLoomSubmit(ctx context.Context, p muxcore.ProjectContext, raw json.RawMessage) (json.RawMessage, error) {
	var a submitArgs
	if err := json.Unmarshal(raw, &a); err != nil {
		return nil, &module.ModuleError{
			Code:    "tool_input_invalid",
			Message: "loom_submit: invalid arguments: " + err.Error(),
		}
	}

	if strings.TrimSpace(a.Prompt) == "" {
		return nil, &module.ModuleError{
			Code:    "tool_input_invalid",
			Message: "loom_submit: prompt must not be empty",
		}
	}

	// Validate worker_type against v4.4.0 supported set.
	wt := loom.WorkerType(a.WorkerType)
	if wt != loom.WorkerTypeCLI {
		return nil, &module.ModuleError{
			Code:    "tool_input_invalid",
			Message: fmt.Sprintf("loom_submit: unsupported worker_type %q; v4.4.0 supports: cli", a.WorkerType),
			Details: map[string]any{"worker_type": a.WorkerType},
		}
	}

	req := loom.TaskRequest{
		WorkerType: wt,
		ProjectID:  p.ID, // ALWAYS scoped to the authenticated project
		Prompt:     a.Prompt,
		CLI:        a.CLI,
		CWD:        a.CWD,
		Env:        a.Env,
		Model:      a.Model,
		Role:       a.Role,
		Effort:     a.Effort,
		Timeout:    a.TimeoutSec,
		Metadata:   a.Metadata,
	}

	taskID, err := m.engine.Submit(ctx, req)
	if err != nil {
		return nil, &module.ModuleError{
			Code:    "internal_error",
			Message: "loom_submit: engine.Submit failed: " + err.Error(),
		}
	}

	out := map[string]any{
		"task_id": taskID,
		"status":  "dispatched",
	}
	return json.Marshal(out)
}

// ---------------------------------------------------------------------------
// loom_get
// ---------------------------------------------------------------------------

type getArgs struct {
	TaskID string `json:"task_id"`
}

func (m *Module) handleLoomGet(p muxcore.ProjectContext, raw json.RawMessage) (json.RawMessage, error) {
	var a getArgs
	if err := json.Unmarshal(raw, &a); err != nil {
		return nil, &module.ModuleError{
			Code:    "tool_input_invalid",
			Message: "loom_get: invalid arguments: " + err.Error(),
		}
	}
	if strings.TrimSpace(a.TaskID) == "" {
		return nil, &module.ModuleError{
			Code:    "tool_input_invalid",
			Message: "loom_get: task_id is required",
		}
	}

	task, err := m.engine.Get(a.TaskID)
	if err != nil {
		return nil, &module.ModuleError{
			Code:    "not_found",
			Message: fmt.Sprintf("loom_get: task %q not found", a.TaskID),
			Details: map[string]any{"task_id": a.TaskID},
		}
	}
	if task == nil {
		return nil, &module.ModuleError{
			Code:    "not_found",
			Message: fmt.Sprintf("loom_get: task %q not found", a.TaskID),
			Details: map[string]any{"task_id": a.TaskID},
		}
	}

	// Cross-project safety: hide tasks owned by other projects.
	if task.ProjectID != p.ID {
		return nil, &module.ModuleError{
			Code:    "not_found",
			Message: fmt.Sprintf("loom_get: task %q not found", a.TaskID),
			Details: map[string]any{"task_id": a.TaskID},
		}
	}

	return json.Marshal(task)
}

// ---------------------------------------------------------------------------
// loom_list
// ---------------------------------------------------------------------------

type listArgs struct {
	Statuses []string `json:"statuses"`
}

func (m *Module) handleLoomList(p muxcore.ProjectContext, raw json.RawMessage) (json.RawMessage, error) {
	var a listArgs
	if len(raw) > 0 && string(raw) != "null" {
		if err := json.Unmarshal(raw, &a); err != nil {
			return nil, &module.ModuleError{
				Code:    "tool_input_invalid",
				Message: "loom_list: invalid arguments: " + err.Error(),
			}
		}
	}

	// Convert string statuses to loom.TaskStatus values.
	statuses := make([]loom.TaskStatus, 0, len(a.Statuses))
	for _, s := range a.Statuses {
		statuses = append(statuses, loom.TaskStatus(s))
	}

	// Scope to caller's project — no client-supplied project_id accepted.
	tasks, err := m.engine.List(p.ID, statuses...)
	if err != nil {
		return nil, &module.ModuleError{
			Code:    "internal_error",
			Message: "loom_list: engine.List failed: " + err.Error(),
		}
	}

	// Ensure we return an empty array rather than null when there are no tasks.
	if tasks == nil {
		tasks = []*loom.Task{}
	}

	out := map[string]any{"tasks": tasks}
	return json.Marshal(out)
}

// ---------------------------------------------------------------------------
// loom_cancel
// ---------------------------------------------------------------------------

func (m *Module) handleLoomCancel(p muxcore.ProjectContext, raw json.RawMessage) (json.RawMessage, error) {
	var a getArgs
	if err := json.Unmarshal(raw, &a); err != nil {
		return nil, &module.ModuleError{
			Code:    "tool_input_invalid",
			Message: "loom_cancel: invalid arguments: " + err.Error(),
		}
	}
	if strings.TrimSpace(a.TaskID) == "" {
		return nil, &module.ModuleError{
			Code:    "tool_input_invalid",
			Message: "loom_cancel: task_id is required",
		}
	}

	// Verify the task exists and belongs to this project before attempting cancel.
	task, err := m.engine.Get(a.TaskID)
	if err != nil || task == nil {
		return nil, &module.ModuleError{
			Code:    "not_found",
			Message: fmt.Sprintf("loom_cancel: task %q not found", a.TaskID),
			Details: map[string]any{"task_id": a.TaskID},
		}
	}
	if task.ProjectID != p.ID {
		return nil, &module.ModuleError{
			Code:    "not_found",
			Message: fmt.Sprintf("loom_cancel: task %q not found", a.TaskID),
			Details: map[string]any{"task_id": a.TaskID},
		}
	}

	cancelErr := m.engine.Cancel(a.TaskID)
	if cancelErr != nil {
		// Loom's Cancel returns an error when the task is not running (already
		// terminal or pending). Translate to a soft success response.
		out := map[string]any{
			"cancelled": false,
			"reason":    "task already finished",
		}
		return json.Marshal(out)
	}

	out := map[string]any{"cancelled": true}
	return json.Marshal(out)
}
