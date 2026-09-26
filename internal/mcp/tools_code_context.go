package mcp

import (
	"bytes"
	"context"
	cryptorand "crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"unicode/utf8"

	"github.com/thebtf/engram/internal/auth"
	"github.com/thebtf/engram/internal/uci"
)

const (
	codebaseContextMaxHandlesPerClient = 32
	codebaseContextListLimit           = 64
)

// CodebaseContextApplication is the narrow UCI application seam exposed to
// MCP. UCI retains resolution defaults and authorization; MCP owns only opaque
// handle bytes and their bounded per-client registry.
type CodebaseContextApplication interface {
	Resolve(context.Context, uci.ResolveContextInput) (uci.AuthorizedContext, error)
	Authorize(context.Context, uci.ResolveContextInput) (uci.AuthorizedContext, error)
	List(context.Context, uci.ResolveContextInput) ([]uci.ContextRef, error)
	Project(context.Context, uci.ContextRef) (map[string]string, error)
}

// LocalGitRegistration is installed only by the production composition.
type LocalGitRegistration func(context.Context, uci.ResolveContextInput, string, string, string, *bool) (uci.RegisteredCheckoutSelector, error)

// codebaseContextRegistrationApplication registers client-owned Git worktrees.
type codebaseContextRegistrationApplication interface {
	RegisterLocalGit(context.Context, uci.ResolveContextInput, string, string, string, *bool) (uci.RegisteredCheckoutSelector, error)
}

// codebaseContextIndexApplication authorizes registered checkout selectors for
// selection and explicit checkout-handle use. It stays separate from the
// published-View capability so no-View bootstrap selection never grants query,
// read, or graph access.
type codebaseContextIndexApplication interface {
	ResolveIndexBinding(context.Context, uci.ResolveIndexBindingInput) (uci.AuthorizedIndexBinding, error)
	AuthorizeIndexBinding(context.Context, uci.ResolveIndexBindingInput) (uci.AuthorizedIndexBinding, error)
	BoundSelector(string) (uci.IndexBindingSelector, bool)
	ForgetClient(string)
}

// UCIContextApplication composes the real resolver and safe display directory
// for the production MCP server. It implements no query, graph, or read
// capability; those remain unavailable until their recorder-owned applications
// are composed separately.
type UCIContextApplication struct {
	resolver         *uci.ContextResolver
	directory        uci.ContextDirectory
	registerLocalGit LocalGitRegistration
}

// NewUCIContextApplication creates the production MCP context application.
func NewUCIContextApplication(resolver *uci.ContextResolver, directory uci.ContextDirectory) (*UCIContextApplication, error) {
	if resolver == nil || directory == nil {
		return nil, errors.New("UCI context application is not configured")
	}
	return &UCIContextApplication{resolver: resolver, directory: directory}, nil
}

func (application *UCIContextApplication) SetLocalGitRegistration(register LocalGitRegistration) {
	application.registerLocalGit = register
}

func (application *UCIContextApplication) RegisterLocalGit(ctx context.Context, caller uci.ResolveContextInput, sourceID, label, locator string, parserBundle *bool) (uci.RegisteredCheckoutSelector, error) {
	if application == nil || application.registerLocalGit == nil {
		return uci.RegisteredCheckoutSelector{}, errors.New("local git registration unavailable")
	}
	return application.registerLocalGit(ctx, caller, sourceID, label, locator, parserBundle)
}

func (application *UCIContextApplication) Resolve(ctx context.Context, input uci.ResolveContextInput) (uci.AuthorizedContext, error) {
	if application == nil || application.resolver == nil {
		return uci.AuthorizedContext{}, errors.New("UCI context application is not configured")
	}
	return application.resolver.Resolve(ctx, input)
}

// Authorize validates one explicit ContextRef without selecting it as the
// caller's default context.
func (application *UCIContextApplication) Authorize(ctx context.Context, input uci.ResolveContextInput) (uci.AuthorizedContext, error) {
	if application == nil || application.resolver == nil {
		return uci.AuthorizedContext{}, errors.New("UCI context application is not configured")
	}
	return application.resolver.Authorize(ctx, input)
}

func (application *UCIContextApplication) List(ctx context.Context, input uci.ResolveContextInput) ([]uci.ContextRef, error) {
	if application == nil || application.directory == nil {
		return nil, errors.New("UCI context application is not configured")
	}
	return application.directory.ListAuthorizedContexts(ctx, input.AuthRealm, input.Principal, codebaseContextListLimit)
}

func (application *UCIContextApplication) Project(ctx context.Context, ref uci.ContextRef) (map[string]string, error) {
	if application == nil || application.directory == nil {
		return nil, errors.New("UCI context application is not configured")
	}
	metadata, err := application.directory.LoadContextMetadata(ctx, ref)
	if err != nil {
		return nil, err
	}
	return map[string]string{
		"source":   metadata.Source,
		"checkout": metadata.Checkout,
		"view":     metadata.View,
	}, nil
}

func (application *UCIContextApplication) ResolveIndexBinding(ctx context.Context, input uci.ResolveIndexBindingInput) (uci.AuthorizedIndexBinding, error) {
	if application == nil || application.resolver == nil {
		return uci.AuthorizedIndexBinding{}, errors.New("UCI context application is not configured")
	}
	return application.resolver.ResolveIndexBinding(ctx, input)
}

func (application *UCIContextApplication) AuthorizeIndexBinding(ctx context.Context, input uci.ResolveIndexBindingInput) (uci.AuthorizedIndexBinding, error) {
	if application == nil || application.resolver == nil {
		return uci.AuthorizedIndexBinding{}, errors.New("UCI context application is not configured")
	}
	return application.resolver.AuthorizeIndexBinding(ctx, input)
}

func (application *UCIContextApplication) BoundSelector(clientSessionID string) (uci.IndexBindingSelector, bool) {
	if application == nil || application.resolver == nil {
		return uci.IndexBindingSelector{}, false
	}
	return application.resolver.BoundSelector(clientSessionID)
}

func (application *UCIContextApplication) ForgetClient(clientSessionID string) {
	if application != nil && application.resolver != nil {
		application.resolver.ForgetClient(clientSessionID)
	}
}

var (
	_ CodebaseContextApplication      = (*UCIContextApplication)(nil)
	_ codebaseContextIndexApplication = (*UCIContextApplication)(nil)
)

type codebaseContextCheckoutArgs struct {
	SourceID          *string `json:"source_id"`
	CheckoutID        *string `json:"checkout_id"`
	IncarnationID     *string `json:"incarnation_id"`
	AnalysisProfileID *string `json:"analysis_profile_id"`
}

type codebaseContextArgs struct {
	Action            *string                      `json:"action"`
	ContextHandle     *string                      `json:"context_handle"`
	SpaceID           *string                      `json:"space_id"`
	SourceID          *string                      `json:"source_id"`
	CheckoutID        *string                      `json:"checkout_id"`
	ViewID            *string                      `json:"view_id"`
	AnalysisProfileID *string                      `json:"analysis_profile_id"`
	Generation        *int64                       `json:"generation"`
	SourceLabel       *string                      `json:"source_label"`
	Locator           *string                      `json:"locator"`
	ParserBundle      *bool                        `json:"parser_bundle"`
	Checkout          *codebaseContextCheckoutArgs `json:"checkout"`
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

type codebaseContextScopeKey struct {
	sourceID      string
	checkoutID    string
	incarnationID string
	profileID     string
}

type codebaseContextSelectorKey struct {
	kind  uint8
	ref   codebaseContextRefKey
	scope codebaseContextScopeKey
}

type codebaseContextHandleEntry struct {
	key      codebaseContextSelectorKey
	selector uci.IndexBindingSelector
	scope    *codebaseContextScopeKey
}

type codebaseContextClientHandles struct {
	byHandle   map[string]codebaseContextHandleEntry
	bySelector map[codebaseContextSelectorKey]string
	byScope    map[codebaseContextScopeKey]uint32
	order      []string
}

type codebaseContextRefPayload struct {
	SpaceID           *string `json:"space_id,omitempty"`
	SourceID          string  `json:"source_id"`
	CheckoutID        string  `json:"checkout_id"`
	ViewID            string  `json:"view_id"`
	AnalysisProfileID string  `json:"analysis_profile_id"`
	Generation        int64   `json:"generation"`
}

type codebaseContextPayload struct {
	ContextHandle     string                     `json:"context_handle"`
	BindingKind       string                     `json:"binding_kind"`
	Context           *codebaseContextRefPayload `json:"context"`
	SpaceID           *string                    `json:"space_id,omitempty"`
	SourceID          string                     `json:"source_id"`
	CheckoutID        string                     `json:"checkout_id"`
	IncarnationID     string                     `json:"incarnation_id,omitempty"`
	ViewID            string                     `json:"view_id,omitempty"`
	AnalysisProfileID string                     `json:"analysis_profile_id"`
	Generation        int64                      `json:"generation,omitempty"`
	Source            string                     `json:"source"`
	Checkout          string                     `json:"checkout"`
	View              string                     `json:"view,omitempty"`
}

type codebaseContextSelection struct {
	ref      *uci.ContextRef
	handle   string
	checkout *uci.RegisteredCheckoutSelector
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
					"enum":        []string{"resolve", "list", "select", "register"},
					"description": "resolve reuses the caller binding; list returns authorized contexts; select chooses a typed reference, registered checkout, or opaque handle",
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
				"source_label":  map[string]any{"type": "string", "description": "Label for a new Git source"},
				"locator":       map[string]any{"type": "string", "description": "Private canonical file URI for the local Git worktree"},
				"parser_bundle": map[string]any{"type": "boolean", "description": "Explicit parser profile request; omitted selects verified installed Tree-sitter on first registration, native Go otherwise, and preserves existing profiles on replay"},
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
				"checkout": map[string]any{
					"type":                 "object",
					"additionalProperties": false,
					"required":             []string{"source_id", "checkout_id", "incarnation_id", "analysis_profile_id"},
					"properties": map[string]any{
						"source_id":           map[string]any{"type": "string"},
						"checkout_id":         map[string]any{"type": "string"},
						"incarnation_id":      map[string]any{"type": "string"},
						"analysis_profile_id": map[string]any{"type": "string"},
					},
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
		return s.resolveCodebaseContext(ctx, input, args)
	case "list":
		return s.listCodebaseContexts(ctx, input, args)
	case "select":
		return s.selectCodebaseContext(ctx, input, args)
	case "register":
		return s.registerCodebaseContext(ctx, input, args)
	default:
		return "", codebaseContextClosedError(uci.ContextMismatch)
	}
}

func (s *Server) registerCodebaseContext(ctx context.Context, input uci.ResolveContextInput, args codebaseContextArgs) (string, error) {
	identity, found := auth.IdentityFrom(ctx)
	if !found || identity.Source != auth.SourceClient || identity.Role != auth.RoleReadWrite || identity.PrincipalKind != auth.PrincipalKindHuman {
		return "", codebaseContextClosedError(uci.PermissionDenied)
	}
	if args.Locator == nil || args.Checkout != nil || args.ContextHandle != nil || args.SpaceID != nil || args.CheckoutID != nil || args.ViewID != nil || args.AnalysisProfileID != nil || args.Generation != nil ||
		(args.SourceID == nil) == (args.SourceLabel == nil) {
		return "", codebaseContextClosedError(uci.ContextMismatch)
	}
	application, _, ok := s.codebaseContextApplicationSnapshot()
	if !ok {
		return "", codebaseContextClosedError(uci.ContextRequired)
	}
	registration, ok := application.(codebaseContextRegistrationApplication)
	if !ok {
		return "", codebaseContextClosedError(uci.ContextRequired)
	}
	var sourceID, label string
	if args.SourceID != nil {
		sourceID = *args.SourceID
	} else {
		label = *args.SourceLabel
	}
	checkout, err := registration.RegisterLocalGit(ctx, input, sourceID, label, *args.Locator, args.ParserBundle)
	if err != nil {
		return "", codebaseContextApplicationError(err)
	}
	return s.selectCodebaseContextSelection(ctx, input, codebaseContextSelection{checkout: &checkout})
}

func (s *Server) resolveCodebaseContext(ctx context.Context, input uci.ResolveContextInput, args codebaseContextArgs) (string, error) {
	if args.hasSelector() {
		return "", codebaseContextClosedError(uci.ContextMismatch)
	}
	application, epoch, ok := s.codebaseContextApplicationSnapshot()
	if !ok {
		return "", fmt.Errorf("unknown tool: codebase_context")
	}
	if indexApplication, ok := application.(codebaseContextIndexApplication); ok {
		if selector, selected := indexApplication.BoundSelector(input.ClientSessionID); selected {
			if _, checkout := selector.Checkout(); checkout {
				binding, err := indexApplication.ResolveIndexBinding(ctx, codebaseContextIndexInput(input, &selector))
				if err != nil {
					return "", codebaseContextApplicationError(err)
				}
				return s.presentCodebaseContextBinding(ctx, application, epoch, input.ClientSessionID, selector, binding.Binding())
			}
		}
	}
	resolved, err := application.Resolve(ctx, input)
	if err != nil {
		return "", codebaseContextApplicationError(err)
	}
	return s.presentCodebaseContext(ctx, application, epoch, input.ClientSessionID, resolved.Ref())
}

func (s *Server) listCodebaseContexts(ctx context.Context, input uci.ResolveContextInput, args codebaseContextArgs) (string, error) {
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
}

func (s *Server) selectCodebaseContext(ctx context.Context, input uci.ResolveContextInput, args codebaseContextArgs) (string, error) {
	if args.ParserBundle != nil {
		return "", codebaseContextClosedError(uci.ContextMismatch)
	}
	selection, err := args.selection()
	if err != nil {
		return "", codebaseContextClosedError(uci.ContextMismatch)
	}
	if selection.handle != "" {
		return s.selectCodebaseContextHandle(ctx, input, selection)
	}
	return s.selectCodebaseContextSelection(ctx, input, selection)
}

func (s *Server) selectCodebaseContextHandle(ctx context.Context, input uci.ResolveContextInput, selection codebaseContextSelection) (string, error) {
	application, epoch, selector, found := s.codebaseContextSelectorForHandle(input.ClientSessionID, selection.handle)
	if !found {
		return "", codebaseContextClosedError(uci.ContextMismatch)
	}
	if checkout, following := selector.Checkout(); following {
		indexApplication, ok := application.(codebaseContextIndexApplication)
		if !ok {
			return "", codebaseContextClosedError(uci.ContextMismatch)
		}
		binding, err := indexApplication.ResolveIndexBinding(ctx, codebaseContextIndexInput(input, &selector))
		if err != nil {
			return "", codebaseContextApplicationError(err)
		}
		if current, ok := selector.Checkout(); !ok || current != checkout {
			return "", codebaseContextClosedError(uci.ContextMismatch)
		}
		return s.presentCodebaseContextBinding(ctx, application, epoch, input.ClientSessionID, selector, binding.Binding())
	}
	ref, pinned := selector.Context()
	if !pinned {
		return "", codebaseContextClosedError(uci.ContextMismatch)
	}
	input.Ref = &ref
	resolved, err := application.Resolve(ctx, input)
	if err != nil {
		return "", codebaseContextApplicationError(err)
	}
	return s.presentCodebaseContext(ctx, application, epoch, input.ClientSessionID, resolved.Ref())
}

func (s *Server) selectCodebaseContextSelection(ctx context.Context, input uci.ResolveContextInput, selection codebaseContextSelection) (string, error) {
	application, epoch, ok := s.codebaseContextApplicationSnapshot()
	if !ok {
		return "", fmt.Errorf("unknown tool: codebase_context")
	}
	if selection.checkout != nil {
		indexApplication, ok := application.(codebaseContextIndexApplication)
		if !ok {
			return "", codebaseContextClosedError(uci.ContextMismatch)
		}
		selector, err := uci.CheckoutIndexBindingSelector(*selection.checkout)
		if err != nil {
			return "", codebaseContextClosedError(uci.ContextMismatch)
		}
		binding, err := indexApplication.ResolveIndexBinding(ctx, codebaseContextIndexInput(input, &selector))
		if err != nil {
			return "", codebaseContextApplicationError(err)
		}
		return s.presentCodebaseContextBinding(ctx, application, epoch, input.ClientSessionID, selector, binding.Binding())
	}
	input.Ref = selection.ref
	resolved, err := application.Resolve(ctx, input)
	if err != nil {
		return "", codebaseContextApplicationError(err)
	}
	return s.presentCodebaseContext(ctx, application, epoch, input.ClientSessionID, resolved.Ref())
}

func codebaseContextIndexInput(input uci.ResolveContextInput, selector *uci.IndexBindingSelector) uci.ResolveIndexBindingInput {
	return uci.ResolveIndexBindingInput{
		ClientSessionID: input.ClientSessionID,
		AuthRealm:       input.AuthRealm,
		Principal:       input.Principal,
		WorkstationID:   input.WorkstationID,
		Selector:        selector,
	}
}

func (s *Server) presentCodebaseContext(ctx context.Context, application CodebaseContextApplication, epoch uint64, clientSessionID string, ref uci.ContextRef) (string, error) {
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

func (s *Server) presentCodebaseContextBinding(ctx context.Context, application CodebaseContextApplication, epoch uint64, clientSessionID string, selector uci.IndexBindingSelector, binding uci.IndexBinding) (string, error) {
	payload, err := s.codebaseContextBindingPayload(ctx, application, epoch, clientSessionID, selector, binding)
	if err != nil {
		return "", err
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return "", codebaseContextClosedError(uci.ContextMismatch)
	}
	return string(encoded), nil
}

func (s *Server) codebaseContextPayload(ctx context.Context, application CodebaseContextApplication, epoch uint64, clientSessionID string, ref uci.ContextRef) (codebaseContextPayload, error) {
	if !s.codebaseContextEpochCurrent(epoch) {
		return codebaseContextPayload{}, codebaseContextClosedError(uci.ContextMismatch)
	}
	metadata, err := application.Project(ctx, ref)
	if err != nil || !validCodebaseContextMetadata(metadata) {
		return codebaseContextPayload{}, codebaseContextClosedError(uci.ContextMismatch)
	}
	selector, err := uci.ContextIndexBindingSelector(ref)
	if err != nil {
		return codebaseContextPayload{}, codebaseContextClosedError(uci.ContextMismatch)
	}
	handle, ok := s.codebaseContextHandleForSelector(clientSessionID, selector, nil, epoch)
	if !ok || !s.codebaseContextEpochCurrent(epoch) {
		return codebaseContextPayload{}, codebaseContextClosedError(uci.ContextMismatch)
	}
	return codebaseContextPayload{
		ContextHandle:     handle,
		BindingKind:       "context",
		Context:           codebaseContextRefPayloadFor(ref),
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

func (s *Server) codebaseContextBindingPayload(ctx context.Context, application CodebaseContextApplication, epoch uint64, clientSessionID string, selector uci.IndexBindingSelector, binding uci.IndexBinding) (codebaseContextPayload, error) {
	binding = binding.Clone()
	if !codebaseContextBindingMatchesSelector(binding, selector) || !s.codebaseContextEpochCurrent(epoch) {
		return codebaseContextPayload{}, codebaseContextClosedError(uci.ContextMismatch)
	}
	handle, ok := s.codebaseContextHandleForSelector(clientSessionID, selector, &binding, epoch)
	if !ok || !s.codebaseContextEpochCurrent(epoch) {
		return codebaseContextPayload{}, codebaseContextClosedError(uci.ContextMismatch)
	}

	payload := codebaseContextPayload{
		ContextHandle:     handle,
		BindingKind:       "checkout",
		SourceID:          binding.Scope.SourceID,
		CheckoutID:        binding.Scope.CheckoutID,
		IncarnationID:     binding.Scope.IncarnationID,
		AnalysisProfileID: binding.ProfileID,
		// The only no-View display facts are server-authorized opaque IDs. They
		// deliberately do not expose locator, root, workstation, or owner data.
		Source:   binding.Scope.SourceID,
		Checkout: binding.Scope.CheckoutID,
	}
	if binding.Context == nil {
		return payload, nil
	}

	ref := cloneCodebaseContextRef(*binding.Context)
	metadata, err := application.Project(ctx, ref)
	if err != nil || !validCodebaseContextMetadata(metadata) {
		return codebaseContextPayload{}, codebaseContextClosedError(uci.ContextMismatch)
	}
	payload.Context = codebaseContextRefPayloadFor(ref)
	payload.SpaceID = copyCodebaseContextSpaceID(ref.SpaceID)
	payload.ViewID = ref.ViewID
	payload.Generation = ref.Generation
	payload.Source = metadata["source"]
	payload.Checkout = metadata["checkout"]
	payload.View = metadata["view"]
	return payload, nil
}

func codebaseContextRefPayloadFor(ref uci.ContextRef) *codebaseContextRefPayload {
	return &codebaseContextRefPayload{
		SpaceID:           copyCodebaseContextSpaceID(ref.SpaceID),
		SourceID:          ref.SourceID,
		CheckoutID:        ref.CheckoutID,
		ViewID:            ref.ViewID,
		AnalysisProfileID: ref.AnalysisProfileID,
		Generation:        ref.Generation,
	}
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
	case "resolve", "list", "select", "register":
		return args, nil
	default:
		return codebaseContextArgs{}, errors.New("invalid action")
	}
}

func (args codebaseContextArgs) hasSelector() bool {
	return args.ContextHandle != nil || args.SpaceID != nil || args.SourceID != nil || args.CheckoutID != nil || args.ViewID != nil || args.AnalysisProfileID != nil || args.Generation != nil || args.Checkout != nil || args.ParserBundle != nil
}

func (args codebaseContextArgs) selection() (codebaseContextSelection, error) {
	typed := args.SpaceID != nil || args.SourceID != nil || args.CheckoutID != nil || args.ViewID != nil || args.AnalysisProfileID != nil || args.Generation != nil
	if args.ContextHandle != nil {
		if typed || args.Checkout != nil || !validCodebaseContextHandle(*args.ContextHandle) {
			return codebaseContextSelection{}, errors.New("ambiguous selector")
		}
		return codebaseContextSelection{handle: *args.ContextHandle}, nil
	}
	if args.Checkout != nil {
		if typed || args.Checkout.SourceID == nil || args.Checkout.CheckoutID == nil || args.Checkout.IncarnationID == nil || args.Checkout.AnalysisProfileID == nil {
			return codebaseContextSelection{}, errors.New("incomplete checkout selector")
		}
		selection := uci.RegisteredCheckoutSelector{
			Scope: uci.IndexScope{
				SourceID:      *args.Checkout.SourceID,
				CheckoutID:    *args.Checkout.CheckoutID,
				IncarnationID: *args.Checkout.IncarnationID,
			},
			ProfileID: *args.Checkout.AnalysisProfileID,
		}
		if _, err := uci.CheckoutIndexBindingSelector(selection); err != nil {
			return codebaseContextSelection{}, err
		}
		return codebaseContextSelection{checkout: &selection}, nil
	}
	if !typed || args.SourceID == nil || args.CheckoutID == nil || args.ViewID == nil || args.AnalysisProfileID == nil || args.Generation == nil {
		return codebaseContextSelection{}, errors.New("incomplete selector")
	}
	ref := uci.ContextRef{
		SpaceID:           copyCodebaseContextSpaceID(args.SpaceID),
		SourceID:          *args.SourceID,
		CheckoutID:        *args.CheckoutID,
		ViewID:            *args.ViewID,
		AnalysisProfileID: *args.AnalysisProfileID,
		Generation:        *args.Generation,
	}
	if _, err := uci.ContextIndexBindingSelector(ref); err != nil {
		return codebaseContextSelection{}, err
	}
	return codebaseContextSelection{ref: &ref}, nil
}

func codebaseContextCallerInput(ctx context.Context) (uci.ResolveContextInput, error) {
	if ctx == nil {
		return uci.ResolveContextInput{}, errors.New("missing context")
	}
	sessionID := sessionFromContext(ctx)
	identity, ok := auth.IdentityFrom(ctx)
	authRealm := string(identity.Source)
	if !ok || !codebaseContextIdentityText(sessionID) || !codebaseContextIdentityText(authRealm) || !codebaseContextIdentityText(identity.Principal) || !codebaseContextIdentityText(identity.WorkstationID()) {
		return uci.ResolveContextInput{}, errors.New("invalid caller")
	}
	if identity.PrincipalKind != "" && !auth.IsValidPrincipalKind(identity.PrincipalKind) {
		return uci.ResolveContextInput{}, errors.New("invalid caller")
	}
	principal, _, ok := identity.MemoryOwner()
	if !ok || !codebaseContextIdentityText(principal) || principal != identity.Principal {
		return uci.ResolveContextInput{}, errors.New("invalid caller")
	}
	return uci.ResolveContextInput{
		ClientSessionID: sessionID,
		AuthRealm:       authRealm,
		Principal:       principal,
		WorkstationID:   identity.WorkstationID(),
	}, nil
}

// resolveCodebaseAuthorizedView resolves the current default only when a
// handle is omitted. Explicit handles are authorization inputs, never selecting
// inputs: a pinned handle authorizes its exact ContextRef, while a checkout
// handle reloads its exact checkout/profile binding and authorizes that binding's
// current ContextRef for this request.
func (s *Server) resolveCodebaseAuthorizedView(ctx context.Context, contextHandle *string) (CodebaseContextApplication, uci.AuthorizedContext, uint64, uci.ContextErrorCode) {
	input, err := codebaseContextCallerInput(ctx)
	if err != nil {
		return nil, uci.AuthorizedContext{}, 0, uci.ContextMismatch
	}

	if contextHandle == nil {
		application, epoch, found := s.codebaseContextApplicationSnapshot()
		if !found {
			return nil, uci.AuthorizedContext{}, 0, uci.ContextRequired
		}
		authorized, err := application.Resolve(ctx, input)
		if err != nil {
			return nil, uci.AuthorizedContext{}, 0, codebaseContextFailureCode(err)
		}
		if !s.codebaseContextEpochCurrent(epoch) {
			return nil, uci.AuthorizedContext{}, 0, uci.ContextMismatch
		}
		return application, authorized, epoch, ""
	}

	application, epoch, selector, found := s.codebaseContextSelectorForHandle(input.ClientSessionID, *contextHandle)
	if !found {
		return nil, uci.AuthorizedContext{}, 0, uci.ContextMismatch
	}
	authorized, binding, contextCode := authorizeCodebaseExplicitSelector(ctx, application, input, selector)
	if contextCode != "" {
		if binding != nil && !s.codebaseContextHandleStillCurrent(input.ClientSessionID, *contextHandle, epoch, selector, binding) {
			return nil, uci.AuthorizedContext{}, 0, uci.ContextMismatch
		}
		return nil, uci.AuthorizedContext{}, 0, contextCode
	}
	if !s.codebaseContextHandleStillCurrent(input.ClientSessionID, *contextHandle, epoch, selector, binding) {
		return nil, uci.AuthorizedContext{}, 0, uci.ContextMismatch
	}
	return application, authorized, epoch, ""
}

func authorizeCodebaseExplicitSelector(ctx context.Context, application CodebaseContextApplication, input uci.ResolveContextInput, selector uci.IndexBindingSelector) (uci.AuthorizedContext, *uci.IndexBinding, uci.ContextErrorCode) {
	if application == nil || selector.Validate() != nil {
		return uci.AuthorizedContext{}, nil, uci.ContextMismatch
	}

	var (
		ref     uci.ContextRef
		binding *uci.IndexBinding
	)
	if pinned, ok := selector.Context(); ok {
		ref = pinned
	} else {
		indexApplication, ok := application.(codebaseContextIndexApplication)
		if !ok {
			return uci.AuthorizedContext{}, nil, uci.ContextMismatch
		}
		authorizedBinding, err := indexApplication.AuthorizeIndexBinding(ctx, codebaseContextIndexInput(input, &selector))
		if err != nil {
			return uci.AuthorizedContext{}, nil, codebaseContextFailureCode(err)
		}
		current := authorizedBinding.Binding()
		if !codebaseContextBindingMatchesSelector(current, selector) {
			return uci.AuthorizedContext{}, nil, uci.ContextMismatch
		}
		binding = &current
		if current.Context == nil {
			return uci.AuthorizedContext{}, binding, uci.ContextRequired
		}
		ref = cloneCodebaseContextRef(*current.Context)
	}

	input.Ref = &ref
	authorized, err := application.Authorize(ctx, input)
	if err != nil {
		return uci.AuthorizedContext{}, binding, codebaseContextFailureCode(err)
	}
	if !codebaseContextRefsEqual(authorized.Ref(), ref) {
		return uci.AuthorizedContext{}, binding, uci.ContextMismatch
	}
	return authorized, binding, ""
}

type uciRequestIdentityContextKey struct{}

type uciRequestIdentity struct {
	requestID     string
	bindingDigest string
}

func contextWithUCIRequestIdentity(ctx context.Context, requestID any, tool string, arguments json.RawMessage) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	encodedID, err := json.Marshal(requestID)
	if err != nil {
		return context.WithValue(ctx, uciRequestIdentityContextKey{}, uciRequestIdentity{})
	}
	encodedBinding, err := json.Marshal(struct {
		Tool      string          `json:"tool"`
		Arguments json.RawMessage `json:"arguments"`
	}{Tool: tool, Arguments: arguments})
	if err != nil {
		return context.WithValue(ctx, uciRequestIdentityContextKey{}, uciRequestIdentity{})
	}
	digest := sha256.Sum256(encodedBinding)
	return context.WithValue(ctx, uciRequestIdentityContextKey{}, uciRequestIdentity{
		requestID:     string(encodedID),
		bindingDigest: "sha256:" + hex.EncodeToString(digest[:]),
	})
}

func uciRequestIdentityFromContext(ctx context.Context) (uciRequestIdentity, bool) {
	if ctx == nil {
		return uciRequestIdentity{}, false
	}
	identity, ok := ctx.Value(uciRequestIdentityContextKey{}).(uciRequestIdentity)
	return identity, ok && codebaseContextIdentityText(identity.requestID) && codebaseContextIdentityText(identity.bindingDigest)
}

func codebaseExposureInput(ctx context.Context, operation uci.ExposureOperation, response uci.QueryResponse) (uci.ExposureInput, error) {
	caller, err := codebaseContextCallerInput(ctx)
	if err != nil {
		return uci.ExposureInput{}, err
	}
	identity, ok := auth.IdentityFrom(ctx)
	if !ok || !codebaseContextIdentityText(identity.WorkstationID()) {
		return uci.ExposureInput{}, errors.New("invalid exposure caller")
	}
	request, ok := uciRequestIdentityFromContext(ctx)
	if !ok {
		return uci.ExposureInput{}, errors.New("missing request identity")
	}
	return uci.ExposureInput{
		AuthRealm:            caller.AuthRealm,
		ClientKeycard:        identity.WorkstationID(),
		ClientSession:        caller.ClientSessionID,
		RequestID:            request.requestID,
		RequestBindingDigest: request.bindingDigest,
		Operation:            operation,
		Response:             response,
	}, nil
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
		if contextErr.Code() == uci.RegistrationProfileUnbound {
			return errors.New("REGISTRATION_PROFILE_UNBOUND: original analysis profile cannot be recovered; register with a new source label for a distinct identity")
		}
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

func (s *Server) codebaseContextApplicationSnapshot() (CodebaseContextApplication, uint64, bool) {
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

func (s *Server) codebaseContextRegistryEpoch() uint64 {
	s.codebaseContextMu.Lock()
	defer s.codebaseContextMu.Unlock()
	return s.codebaseContextEpoch
}

func (s *Server) codebaseContextRegistryEpochCurrent(epoch uint64) bool {
	s.codebaseContextMu.Lock()
	defer s.codebaseContextMu.Unlock()
	return s.codebaseContextEpoch == epoch
}

func (s *Server) codebaseContextSelectorForHandle(clientSessionID, handle string) (CodebaseContextApplication, uint64, uci.IndexBindingSelector, bool) {
	s.codebaseContextMu.Lock()
	defer s.codebaseContextMu.Unlock()
	if s.codebaseContextApplication == nil {
		return nil, 0, uci.IndexBindingSelector{}, false
	}
	client := s.codebaseContextHandles[clientSessionID]
	if client == nil {
		return nil, 0, uci.IndexBindingSelector{}, false
	}
	entry, found := client.byHandle[handle]
	if !found {
		return nil, 0, uci.IndexBindingSelector{}, false
	}
	return s.codebaseContextApplication, s.codebaseContextEpoch, entry.selector.Clone(), true
}

func (s *Server) codebaseContextHandleForSelector(clientSessionID string, selector uci.IndexBindingSelector, binding *uci.IndexBinding, epoch uint64) (string, bool) {
	if err := selector.Validate(); err != nil || (binding != nil && !codebaseContextBindingMatchesSelector(*binding, selector)) {
		return "", false
	}
	key, ok := codebaseContextSelectorKeyFor(selector)
	if !ok {
		return "", false
	}

	s.codebaseContextMu.Lock()
	if s.codebaseContextEpoch != epoch {
		s.codebaseContextMu.Unlock()
		return "", false
	}
	if s.codebaseContextHandles == nil {
		s.codebaseContextHandles = make(map[string]*codebaseContextClientHandles)
	}
	client := s.codebaseContextHandles[clientSessionID]
	if client == nil {
		client = &codebaseContextClientHandles{
			byHandle:   make(map[string]codebaseContextHandleEntry),
			bySelector: make(map[codebaseContextSelectorKey]string),
			byScope:    make(map[codebaseContextScopeKey]uint32),
		}
		s.codebaseContextHandles[clientSessionID] = client
	}
	if handle, found := client.bySelector[key]; found {
		if binding != nil {
			s.codebaseContextSetEntryScope(client, handle, *binding)
		}
		s.codebaseContextMu.Unlock()
		return handle, true
	}

	forgotten := false
	if len(client.order) >= codebaseContextMaxHandlesPerClient {
		oldest := client.order[0]
		client.order = client.order[1:]
		s.codebaseContextRemoveEntry(client, oldest)
		forgotten = true
	}
	handle, err := newCodebaseContextHandle(client.byHandle)
	if err != nil {
		s.codebaseContextMu.Unlock()
		return "", false
	}
	entry := codebaseContextHandleEntry{key: key, selector: selector.Clone()}
	client.byHandle[handle] = entry
	client.bySelector[key] = handle
	client.order = append(client.order, handle)
	if binding != nil {
		s.codebaseContextSetEntryScope(client, handle, *binding)
	}
	application := s.codebaseContextApplication
	s.codebaseContextMu.Unlock()

	// A bounded registry eviction invalidates the same client's resolver default
	// rather than allowing an evicted selection to remain an ambient authority.
	if forgotten {
		if indexApplication, ok := application.(codebaseContextIndexApplication); ok {
			indexApplication.ForgetClient(clientSessionID)
		}
	}
	return handle, true
}

func (s *Server) codebaseContextSetEntryScope(client *codebaseContextClientHandles, handle string, binding uci.IndexBinding) {
	entry, found := client.byHandle[handle]
	if !found || binding.Validate() != nil {
		return
	}
	scope := codebaseContextScopeKeyForBinding(binding)
	if entry.scope != nil && *entry.scope == scope {
		return
	}
	if entry.scope != nil {
		s.codebaseContextRemoveScope(client, *entry.scope)
	}
	entry.scope = &scope
	client.byHandle[handle] = entry
	client.byScope[scope]++
}

func (s *Server) codebaseContextRemoveEntry(client *codebaseContextClientHandles, handle string) {
	entry, found := client.byHandle[handle]
	if !found {
		return
	}
	delete(client.byHandle, handle)
	delete(client.bySelector, entry.key)
	if entry.scope != nil {
		s.codebaseContextRemoveScope(client, *entry.scope)
	}
}

func (s *Server) codebaseContextRemoveScope(client *codebaseContextClientHandles, scope codebaseContextScopeKey) {
	if client.byScope[scope] <= 1 {
		delete(client.byScope, scope)
		return
	}
	client.byScope[scope]--
}

func (s *Server) codebaseContextHandleSelector(clientSessionID, handle string) (uint64, uci.IndexBindingSelector, bool) {
	s.codebaseContextMu.Lock()
	defer s.codebaseContextMu.Unlock()
	client := s.codebaseContextHandles[clientSessionID]
	if client == nil {
		return 0, uci.IndexBindingSelector{}, false
	}
	entry, found := client.byHandle[handle]
	if !found {
		return 0, uci.IndexBindingSelector{}, false
	}
	return s.codebaseContextEpoch, entry.selector.Clone(), true
}

func (s *Server) codebaseContextHandleCurrent(clientSessionID, handle string, epoch uint64, selector uci.IndexBindingSelector, binding uci.IndexBinding) bool {
	return s.codebaseContextHandleStillCurrent(clientSessionID, handle, epoch, selector, &binding)
}

func (s *Server) codebaseContextHandleStillCurrent(clientSessionID, handle string, epoch uint64, selector uci.IndexBindingSelector, binding *uci.IndexBinding) bool {
	if selector.Validate() != nil {
		return false
	}
	if binding != nil {
		if !codebaseContextBindingMatchesSelector(*binding, selector) {
			return false
		}
	} else if _, pinned := selector.Context(); !pinned {
		return false
	}

	s.codebaseContextMu.Lock()
	defer s.codebaseContextMu.Unlock()
	if s.codebaseContextEpoch != epoch {
		return false
	}
	client := s.codebaseContextHandles[clientSessionID]
	if client == nil {
		return false
	}
	entry, found := client.byHandle[handle]
	if !found || !codebaseContextSelectorsEqual(entry.selector, selector) {
		return false
	}
	if binding != nil {
		s.codebaseContextSetEntryScope(client, handle, *binding)
	}
	return true
}

func codebaseContextSelectorsEqual(left, right uci.IndexBindingSelector) bool {
	leftKey, leftOK := codebaseContextSelectorKeyFor(left)
	rightKey, rightOK := codebaseContextSelectorKeyFor(right)
	return leftOK && rightOK && leftKey == rightKey
}

func (s *Server) codebaseContextScopeAdmitted(clientSessionID string, scope codebaseContextScopeKey, epoch uint64) bool {
	s.codebaseContextMu.Lock()
	defer s.codebaseContextMu.Unlock()
	if s.codebaseContextEpoch != epoch {
		return false
	}
	client := s.codebaseContextHandles[clientSessionID]
	return client != nil && client.byScope[scope] > 0
}

func codebaseContextSelectorKeyFor(selector uci.IndexBindingSelector) (codebaseContextSelectorKey, bool) {
	if ref, pinned := selector.Context(); pinned {
		return codebaseContextSelectorKey{kind: 1, ref: codebaseContextKey(ref)}, true
	}
	checkout, following := selector.Checkout()
	if !following {
		return codebaseContextSelectorKey{}, false
	}
	return codebaseContextSelectorKey{
		kind: 2,
		scope: codebaseContextScopeKey{
			sourceID:      checkout.Scope.SourceID,
			checkoutID:    checkout.Scope.CheckoutID,
			incarnationID: checkout.Scope.IncarnationID,
			profileID:     checkout.ProfileID,
		},
	}, true
}

func codebaseContextScopeKeyForBinding(binding uci.IndexBinding) codebaseContextScopeKey {
	return codebaseContextScopeKey{
		sourceID:      binding.Scope.SourceID,
		checkoutID:    binding.Scope.CheckoutID,
		incarnationID: binding.Scope.IncarnationID,
		profileID:     binding.ProfileID,
	}
}

func codebaseContextBindingMatchesSelector(binding uci.IndexBinding, selector uci.IndexBindingSelector) bool {
	if binding.Validate() != nil || selector.Validate() != nil {
		return false
	}
	if ref, pinned := selector.Context(); pinned {
		return binding.Context != nil && codebaseContextRefsEqual(*binding.Context, ref)
	}
	checkout, following := selector.Checkout()
	return following && binding.Scope == checkout.Scope && binding.ProfileID == checkout.ProfileID
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

func codebaseContextRefsEqual(left, right uci.ContextRef) bool {
	return codebaseContextKey(left) == codebaseContextKey(right)
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
