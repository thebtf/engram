package engramcore

import (
	"bytes"
	"context"
	"encoding/json"
	"io"

	"github.com/thebtf/engram/internal/auditcontext"
	"github.com/thebtf/engram/internal/module"
	"github.com/thebtf/engram/internal/projectidentity"
	muxcore "github.com/thebtf/mcp-mux/muxcore"
)

const (
	gitRevParse     = "rev-parse"
	gitShowTopLevel = "--show-toplevel"
)

type nativeCodebaseRegistrationArgs struct {
	Action        string  `json:"action"`
	SourceLabel   *string `json:"source_label,omitempty"`
	SourceID      *string `json:"source_id,omitempty"`
	ContextHandle *string `json:"context_handle,omitempty"`
	Locator       *string `json:"locator,omitempty"`
	ParserBundle  *bool   `json:"parser_bundle,omitempty"`
}

// Native registration supplies local evidence, never Source or project authority.
// The existing server still authorizes the principal, realm and registered Source.
func (m *Module) prepareNativeCodebaseRegistration(ctx context.Context, project muxcore.ProjectContext, raw json.RawMessage) (json.RawMessage, error) {
	var action struct {
		Action string `json:"action"`
	}
	if json.Unmarshal(raw, &action) != nil || action.Action != "register" {
		return raw, nil
	}
	var args nativeCodebaseRegistrationArgs
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&args) != nil || decoder.Decode(&struct{}{}) != io.EOF {
		return nil, nativeCodebaseRegistrationMismatch()
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil {
		return nil, nativeCodebaseRegistrationMismatch()
	}
	for _, name := range []string{"source_label", "source_id", "context_handle", "locator"} {
		if value, present := fields[name]; present && bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return nil, nativeCodebaseRegistrationMismatch()
		}
	}
	selectors := 0
	for _, value := range []*string{args.SourceLabel, args.SourceID, args.ContextHandle} {
		if value != nil {
			selectors++
			if !validUCIClientIdentifier(*value, 4096) {
				return nil, nativeCodebaseRegistrationMismatch()
			}
		}
	}
	if selectors > 1 {
		return nil, nativeCodebaseRegistrationMismatch()
	}
	if args.Locator != nil && selectors == 1 && args.ContextHandle == nil {
		return raw, nil
	}
	if args.Locator == nil && args.SourceID != nil {
		return nil, &module.ModuleError{Code: "CONTEXT_MISMATCH", Message: "native register with source_id requires an explicit locator; omit source_id to reuse the current server selection"}
	}
	if args.SourceLabel == nil && args.SourceID == nil {
		handle := ""
		if args.ContextHandle != nil {
			handle = *args.ContextHandle
		}
		target, err := NewUCIIndexAdapter(m).ResolveIndexTarget(ctx, project, handle)
		if err != nil {
			return nil, err
		}
		if target.ClientSessionID != auditcontext.UCITransportSession(ctx) || (handle != "" && target.ContextHandle != handle) {
			return nil, nativeCodebaseRegistrationMismatch()
		}
		sourceID := target.BindingClone().Scope.SourceID
		args.SourceID = &sourceID
		args.ContextHandle = nil
	}
	if args.Locator == nil {
		locator, err := nativeCodebaseGitLocator(ctx, project.Cwd)
		if err != nil {
			return nil, err
		}
		args.Locator = &locator
	}
	return json.Marshal(args)
}

func nativeCodebaseRegistrationMismatch() error {
	return &module.ModuleError{Code: "CONTEXT_MISMATCH", Message: "codebase_context register requires one source label, source ID, or owned context handle; omit all three to reuse the current server selection"}
}

func nativeCodebaseGitLocator(ctx context.Context, selected string) (string, error) {
	_, locator, err := nativeCodebaseGitContext(ctx, selected)
	return locator, err
}

func nativeCodebaseGitContext(ctx context.Context, selected string) (context.Context, string, error) {
	discovered, locator, err := projectidentity.NativeGitContext(ctx, selected)
	if err != nil {
		if ctx.Err() != nil {
			return nil, "", ctx.Err()
		}
		return nil, "", &module.ModuleError{Code: "SOURCE_UNAVAILABLE", Message: "the current local Git worktree is unavailable"}
	}
	return discovered, locator, nil
}
