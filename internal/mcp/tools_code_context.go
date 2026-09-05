package mcp

import (
	"bytes"
	"context"
	cryptorand "crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"unicode/utf8"

	"github.com/thebtf/engram/internal/auth"
	"github.com/thebtf/engram/internal/uci"
)

const codebaseContextMaxHandlesPerClient = 32

// codebaseContextApplication is the narrow UCI application seam exposed to MCP.
// UCI retains resolution defaults and authorization; MCP only owns opaque handles.
type codebaseContextApplication interface {
	Resolve(context.Context, uci.ResolveContextInput) (uci.AuthorizedContext, error)
	List(context.Context, uci.ResolveContextInput) ([]uci.ContextRef, error)
	Project(context.Context, uci.ContextRef) (map[string]string, error)
}

type codebaseContextArgs struct {
	Action            *string `json:"action"`
	ContextHandle     *string `json:"context_handle"`
	SpaceID           *string `json:"space_id"`
	SourceID          *string `json:"source_id"`
	CheckoutID        *string `json:"checkout_id"`
	ViewID            *string `json:"view_id"`
	AnalysisProfileID *string `json:"analysis_profile_id"`
	Generation        *int64  `json:"generation"`
}

type codebaseContextRefKey struct {
	hasSpaceID        bool
	spaceID           string
	sourceID          string
	checkoutID        string
	viewID            string
	analysisProfileID string
	generation        int64
}

type codebaseContextHandleEntry struct {
	key codebaseContextRefKey
	ref uci.ContextRef
}

type codebaseContextClientHandles struct {
	byHandle map[string]codebaseContextHandleEntry
	byRef    map[codebaseContextRefKey]string
	order    []string
}

type codebaseContextPayload struct {
	ContextHandle     string  `json:"context_handle"`
	SpaceID           *string `json:"space_id,omitempty"`
	SourceID          string  `json:"source_id"`
	CheckoutID        string  `json:"checkout_id"`
	ViewID            string  `json:"view_id"`
	AnalysisProfileID string  `json:"analysis_profile_id"`
	Generation        int64   `json:"generation"`
	Source            string  `json:"source"`
	Checkout          string  `json:"checkout"`
	View              string  `json:"view"`
}

func codebaseContextTool() Tool {
	return Tool{
		Name:        "codebase_context",
		Description: "Resolve, list, or select an authorized codebase context. Context handles are client-scoped and opaque.",
		tier:        tierUseful,
		InputSchema: map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"required":             []string{"action"},
			"properties": map[string]any{
				"action": map[string]any{
					"type":        "string",
					"enum":        []string{"resolve", "list", "select"},
					"description": "resolve reuses the caller binding; list returns authorized contexts; select chooses a typed reference or opaque handle",
				},
				"context_handle": map[string]any{
					"type":        "string",
					"description": "Opaque handle returned for this client by a previous codebase_context response",
				},
				"space_id": map[string]any{
					"type":        "string",
					"description": "Optional ContextRef space UUID for action=select",
				},
				"source_id": map[string]any{
					"type":        "string",
					"description": "ContextRef source UUID for action=select",
				},
				"checkout_id": map[string]any{
					"type":        "string",
					"description": "ContextRef checkout UUID for action=select",
				},
				"view_id": map[string]any{
					"type":        "string",
					"description": "ContextRef view UUID for action=select",
				},
				"analysis_profile_id": map[string]any{
					"type":        "string",
					"description": "ContextRef analysis profile UUID for action=select",
				},
				"generation": map[string]any{
					"type":        "integer",
					"minimum":     1,
					"description": "ContextRef generation for action=select",
				},
			},
		},
	}
}

func (s *Server) handleCodebaseContext(ctx context.Context, raw json.RawMessage) (string, error) {
	if !codeIntelEnabled() || !s.hasCodebaseContextApplication() {
		return "", fmt.Errorf("unknown tool: codebase_context")
	}

	args, err := decodeCodebaseContextArgs(raw)
	if err != nil {
		return "", codebaseContextClosedError(uci.ContextMismatch)
	}
	input, err := codebaseContextCallerInput(ctx)
	if err != nil {
		return "", codebaseContextClosedError(uci.ContextMismatch)
	}

	switch *args.Action {
	case "resolve":
		if args.hasSelector() {
			return "", codebaseContextClosedError(uci.ContextMismatch)
		}
		application, epoch, ok := s.codebaseContextApplicationSnapshot()
		if !ok {
			return "", fmt.Errorf("unknown tool: codebase_context")
		}
		resolved, err := application.Resolve(ctx, input)
		if err != nil {
			return "", codebaseContextApplicationError(err)
		}
		return s.presentCodebaseContext(ctx, application, epoch, input.ClientSessionID, resolved.Ref())
	case "list":
		if args.hasSelector() {
			return "", codebaseContextClosedError(uci.ContextMismatch)
		}
		application, epoch, ok := s.codebaseContextApplicationSnapshot()
		if !ok {
			return "", fmt.Errorf("unknown tool: codebase_context")
		}
		refs, err := application.List(ctx, input)
		if err != nil {
			return "", codebaseContextApplicationError(err)
		}
		payloads := make([]codebaseContextPayload, 0, len(refs))
		for _, ref := range refs {
			payload, err := s.codebaseContextPayload(ctx, application, epoch, input.ClientSessionID, ref)
			if err != nil {
				return "", err
			}
			payloads = append(payloads, payload)
		}
		encoded, err := json.Marshal(struct {
			Contexts []codebaseContextPayload `json:"contexts"`
		}{Contexts: payloads})
		if err != nil {
			return "", codebaseContextClosedError(uci.ContextMismatch)
		}
		return string(encoded), nil
	case "select":
		return s.selectCodebaseContext(ctx, input, args)
	default:
		return "", codebaseContextClosedError(uci.ContextMismatch)
	}
}

func (s *Server) selectCodebaseContext(ctx context.Context, input uci.ResolveContextInput, args codebaseContextArgs) (string, error) {
	ref, handle, err := args.selection()
	if err != nil {
		return "", codebaseContextClosedError(uci.ContextMismatch)
	}

	if handle != "" {
		application, epoch, resolvedRef, found := s.codebaseContextRefForHandle(input.ClientSessionID, handle)
		if !found {
			return "", codebaseContextClosedError(uci.ContextMismatch)
		}
		input.Ref = &resolvedRef
		resolved, err := application.Resolve(ctx, input)
		if err != nil {
			return "", codebaseContextApplicationError(err)
		}
		return s.presentCodebaseContext(ctx, application, epoch, input.ClientSessionID, resolved.Ref())
	}

	application, epoch, ok := s.codebaseContextApplicationSnapshot()
	if !ok {
		return "", fmt.Errorf("unknown tool: codebase_context")
	}
	input.Ref = ref
	resolved, err := application.Resolve(ctx, input)
	if err != nil {
		return "", codebaseContextApplicationError(err)
	}
	return s.presentCodebaseContext(ctx, application, epoch, input.ClientSessionID, resolved.Ref())
}

func (s *Server) presentCodebaseContext(ctx context.Context, application codebaseContextApplication, epoch uint64, clientSessionID string, ref uci.ContextRef) (string, error) {
	payload, err := s.codebaseContextPayload(ctx, application, epoch, clientSessionID, ref)
	if err != nil {
		return "", err
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return "", codebaseContextClosedError(uci.ContextMismatch)
	}
	return string(encoded), nil
}

func (s *Server) codebaseContextPayload(ctx context.Context, application codebaseContextApplication, epoch uint64, clientSessionID string, ref uci.ContextRef) (codebaseContextPayload, error) {
	if !s.codebaseContextEpochCurrent(epoch) {
		return codebaseContextPayload{}, codebaseContextClosedError(uci.ContextMismatch)
	}
	metadata, err := application.Project(ctx, ref)
	if err != nil || !validCodebaseContextMetadata(metadata) {
		return codebaseContextPayload{}, codebaseContextClosedError(uci.ContextMismatch)
	}
	if !s.codebaseContextEpochCurrent(epoch) {
		return codebaseContextPayload{}, codebaseContextClosedError(uci.ContextMismatch)
	}
	handle, ok := s.codebaseContextHandleFor(clientSessionID, ref, epoch)
	if !ok {
		return codebaseContextPayload{}, codebaseContextClosedError(uci.ContextMismatch)
	}
	return codebaseContextPayload{
		ContextHandle:     handle,
		SpaceID:           copyCodebaseContextSpaceID(ref.SpaceID),
		SourceID:          ref.SourceID,
		CheckoutID:        ref.CheckoutID,
		ViewID:            ref.ViewID,
		AnalysisProfileID: ref.AnalysisProfileID,
		Generation:        ref.Generation,
		Source:            metadata["source"],
		Checkout:          metadata["checkout"],
		View:              metadata["view"],
	}, nil
}

func decodeCodebaseContextArgs(raw json.RawMessage) (codebaseContextArgs, error) {
	var args codebaseContextArgs
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&args); err != nil {
		return codebaseContextArgs{}, err
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return codebaseContextArgs{}, errors.New("multiple JSON values")
	}
	if args.Action == nil {
		return codebaseContextArgs{}, errors.New("action is required")
	}
	switch *args.Action {
	case "resolve", "list", "select":
		return args, nil
	default:
		return codebaseContextArgs{}, errors.New("invalid action")
	}
}

func (args codebaseContextArgs) hasSelector() bool {
	return args.ContextHandle != nil || args.SpaceID != nil || args.SourceID != nil || args.CheckoutID != nil || args.ViewID != nil || args.AnalysisProfileID != nil || args.Generation != nil
}

func (args codebaseContextArgs) selection() (*uci.ContextRef, string, error) {
	typed := args.SpaceID != nil || args.SourceID != nil || args.CheckoutID != nil || args.ViewID != nil || args.AnalysisProfileID != nil || args.Generation != nil
	if args.ContextHandle != nil {
		if typed || !validCodebaseContextHandle(*args.ContextHandle) {
			return nil, "", errors.New("ambiguous selector")
		}
		return nil, *args.ContextHandle, nil
	}
	if !typed || args.SourceID == nil || args.CheckoutID == nil || args.ViewID == nil || args.AnalysisProfileID == nil || args.Generation == nil {
		return nil, "", errors.New("incomplete selector")
	}
	ref := uci.ContextRef{
		SpaceID:           copyCodebaseContextSpaceID(args.SpaceID),
		SourceID:          *args.SourceID,
		CheckoutID:        *args.CheckoutID,
		ViewID:            *args.ViewID,
		AnalysisProfileID: *args.AnalysisProfileID,
		Generation:        *args.Generation,
	}
	return &ref, "", nil
}

func codebaseContextCallerInput(ctx context.Context) (uci.ResolveContextInput, error) {
	if ctx == nil {
		return uci.ResolveContextInput{}, errors.New("missing context")
	}
	sessionID := sessionFromContext(ctx)
	identity, ok := auth.IdentityFrom(ctx)
	if !ok || !codebaseContextIdentityText(sessionID) || !codebaseContextIdentityText(identity.Principal) {
		return uci.ResolveContextInput{}, errors.New("invalid caller")
	}
	if identity.PrincipalKind != "" && !auth.IsValidPrincipalKind(identity.PrincipalKind) {
		return uci.ResolveContextInput{}, errors.New("invalid caller")
	}
	principal, _, ok := identity.MemoryOwner()
	if !ok || !codebaseContextIdentityText(principal) {
		return uci.ResolveContextInput{}, errors.New("invalid caller")
	}
	return uci.ResolveContextInput{ClientSessionID: sessionID, Principal: principal}, nil
}

func codebaseContextIdentityText(value string) bool {
	if value == "" || strings.TrimSpace(value) != value {
		return false
	}
	for _, r := range value {
		if r < 0x20 || r == 0x7f {
			return false
		}
	}
	return true
}

func validCodebaseContextHandle(value string) bool {
	return len(value) <= 128 && codebaseContextIdentityText(value)
}

func validCodebaseContextMetadata(metadata map[string]string) bool {
	if metadata == nil {
		return false
	}
	for _, key := range []string{"source", "checkout", "view"} {
		value := metadata[key]
		if len(value) == 0 || len(value) > 256 || !utf8.ValidString(value) || strings.TrimSpace(value) != value {
			return false
		}
		for _, character := range value {
			if character < 0x20 || character == 0x7f {
				return false
			}
		}
	}
	return true
}

func codebaseContextApplicationError(err error) error {
	var contextErr *uci.ContextError
	if errors.As(err, &contextErr) {
		switch contextErr.Code() {
		case uci.ContextRequired, uci.ContextMismatch, uci.PermissionDenied:
			return codebaseContextClosedError(contextErr.Code())
		}
	}
	return codebaseContextClosedError(uci.ContextMismatch)
}

func codebaseContextClosedError(code uci.ContextErrorCode) error {
	return errors.New(string(code))
}

func (s *Server) hasCodebaseContextApplication() bool {
	s.codebaseContextMu.Lock()
	defer s.codebaseContextMu.Unlock()
	return s.codebaseContextApplication != nil
}

func (s *Server) codebaseContextApplicationSnapshot() (codebaseContextApplication, uint64, bool) {
	s.codebaseContextMu.Lock()
	defer s.codebaseContextMu.Unlock()
	if s.codebaseContextApplication == nil {
		return nil, 0, false
	}
	return s.codebaseContextApplication, s.codebaseContextEpoch, true
}

func (s *Server) codebaseContextEpochCurrent(epoch uint64) bool {
	s.codebaseContextMu.Lock()
	defer s.codebaseContextMu.Unlock()
	return s.codebaseContextApplication != nil && s.codebaseContextEpoch == epoch
}

func (s *Server) codebaseContextRefForHandle(clientSessionID, handle string) (codebaseContextApplication, uint64, uci.ContextRef, bool) {
	s.codebaseContextMu.Lock()
	defer s.codebaseContextMu.Unlock()
	if s.codebaseContextApplication == nil {
		return nil, 0, uci.ContextRef{}, false
	}
	client, found := s.codebaseContextHandles[clientSessionID]
	if !found {
		return nil, 0, uci.ContextRef{}, false
	}
	entry, found := client.byHandle[handle]
	if !found {
		return nil, 0, uci.ContextRef{}, false
	}
	return s.codebaseContextApplication, s.codebaseContextEpoch, cloneCodebaseContextRef(entry.ref), true
}

func (s *Server) codebaseContextHandleFor(clientSessionID string, ref uci.ContextRef, epoch uint64) (string, bool) {
	s.codebaseContextMu.Lock()
	defer s.codebaseContextMu.Unlock()
	if s.codebaseContextApplication == nil || s.codebaseContextEpoch != epoch {
		return "", false
	}
	if s.codebaseContextHandles == nil {
		s.codebaseContextHandles = make(map[string]*codebaseContextClientHandles)
	}
	client := s.codebaseContextHandles[clientSessionID]
	if client == nil {
		client = &codebaseContextClientHandles{
			byHandle: make(map[string]codebaseContextHandleEntry),
			byRef:    make(map[codebaseContextRefKey]string),
		}
		s.codebaseContextHandles[clientSessionID] = client
	}
	key := codebaseContextKey(ref)
	if handle, found := client.byRef[key]; found {
		return handle, true
	}
	if len(client.order) >= codebaseContextMaxHandlesPerClient {
		oldest := client.order[0]
		client.order = client.order[1:]
		if oldEntry, found := client.byHandle[oldest]; found {
			delete(client.byHandle, oldest)
			delete(client.byRef, oldEntry.key)
		}
	}
	handle, err := newCodebaseContextHandle(client.byHandle)
	if err != nil {
		return "", false
	}
	client.byHandle[handle] = codebaseContextHandleEntry{key: key, ref: cloneCodebaseContextRef(ref)}
	client.byRef[key] = handle
	client.order = append(client.order, handle)
	return handle, true
}

func newCodebaseContextHandle(existing map[string]codebaseContextHandleEntry) (string, error) {
	var bytes [24]byte
	for range 4 {
		if _, err := cryptorand.Read(bytes[:]); err != nil {
			return "", err
		}
		handle := base64.RawURLEncoding.EncodeToString(bytes[:])
		if _, found := existing[handle]; !found {
			return handle, nil
		}
	}
	return "", errors.New("opaque handle collision")
}

func codebaseContextKey(ref uci.ContextRef) codebaseContextRefKey {
	key := codebaseContextRefKey{
		sourceID:          ref.SourceID,
		checkoutID:        ref.CheckoutID,
		viewID:            ref.ViewID,
		analysisProfileID: ref.AnalysisProfileID,
		generation:        ref.Generation,
	}
	if ref.SpaceID != nil {
		key.hasSpaceID = true
		key.spaceID = *ref.SpaceID
	}
	return key
}

func cloneCodebaseContextRef(ref uci.ContextRef) uci.ContextRef {
	copy := ref
	copy.SpaceID = copyCodebaseContextSpaceID(ref.SpaceID)
	return copy
}

func copyCodebaseContextSpaceID(spaceID *string) *string {
	if spaceID == nil {
		return nil
	}
	copy := *spaceID
	return &copy
}
