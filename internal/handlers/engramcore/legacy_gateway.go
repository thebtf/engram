package engramcore

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/thebtf/engram/internal/legacyrelay"
	"github.com/thebtf/engram/internal/worker/sessioncompat"
	pb "github.com/thebtf/engram/proto/engram/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/encoding/protojson"
)

// LegacyRelayGateway is the private T3 adapter from the transport-only relay to
// the existing engramcore gRPC pool. It retains no request body or raw project
// authority; child-only credentials are re-read for every call.
type LegacyRelayGateway struct {
	module           *Module
	children         *legacyrelay.ChildRegistry
	capabilities     *legacyrelay.CapabilityRegistry
	mu               sync.Mutex
	known            map[string]string
	childCredentials map[legacyrelay.ChildBinding]map[string]legacyrelay.CredentialRef
}

func NewLegacyRelayGateway(module *Module, children *legacyrelay.ChildRegistry, capabilities *legacyrelay.CapabilityRegistry) (*LegacyRelayGateway, error) {
	if module == nil || children == nil || capabilities == nil {
		return nil, errors.New("legacy relay gateway dependencies are missing")
	}
	gateway := &LegacyRelayGateway{
		module:           module,
		children:         children,
		capabilities:     capabilities,
		known:            make(map[string]string),
		childCredentials: make(map[legacyrelay.ChildBinding]map[string]legacyrelay.CredentialRef),
	}
	children.SetReplacementHandler(gateway.invalidateChild)
	return gateway, nil
}

func (g *LegacyRelayGateway) RegisterIdentity(ctx context.Context, call legacyrelay.IdentityRegistrationCall) (legacyrelay.RegistrationResult, error) {
	config, err := g.configFor(call.Bootstrap().Child(), true)
	if err != nil {
		return legacyrelay.RegistrationResult{}, err
	}
	identity, err := decodeRelayDescriptor(call.Descriptor())
	if err != nil {
		return legacyrelay.RegistrationResult{}, err
	}
	connection, err := g.module.pool.getOrDialGRPC(config.serverURL, config.registrationToken)
	if err != nil {
		return legacyrelay.RegistrationResult{}, fmt.Errorf("relay registration gRPC connection: %w", err)
	}
	response, err := pb.NewEngramServiceClient(connection).RegisterProjectIdentityV3(
		daemonComparisonContextV3(ctx),
		&pb.RegisterProjectIdentityV3Request{ProjectIdentityV3: identity, RelayRevision: legacyrelay.AdapterRevisionHAP01B},
		grpc.WaitForReady(true),
	)
	if err != nil {
		if code := status.Code(err); code == codes.Unauthenticated || code == codes.PermissionDenied {
			g.module.pool.closeTokenHash(hashToken(config.registrationToken))
		}
		return legacyrelay.RegistrationResult{}, fmt.Errorf("relay project registration: %w", err)
	}
	resolution := response.GetProjectResolutionV3()
	canonicalProject := resolution.GetProjectKey()
	if err := validateV3Resolution(resolution, canonicalProject, identity); err != nil {
		return legacyrelay.RegistrationResult{}, err
	}
	projectToken, ok := config.projectTokens[canonicalProject]
	if !ok {
		return legacyrelay.RegistrationResult{}, errors.New("relay project keycard is unavailable")
	}
	credential, err := relayCredentialRef(config.serverURL, canonicalProject, projectToken)
	if err != nil {
		return legacyrelay.RegistrationResult{}, err
	}
	currentConfig, err := g.configFor(call.Bootstrap().Child(), false)
	if err != nil {
		return legacyrelay.RegistrationResult{}, err
	}
	currentToken, ok := currentConfig.projectTokens[canonicalProject]
	if !ok {
		return legacyrelay.RegistrationResult{}, errors.New("relay project keycard changed during registration")
	}
	currentCredential, err := relayCredentialRef(currentConfig.serverURL, canonicalProject, currentToken)
	if err != nil || currentCredential != credential {
		return legacyrelay.RegistrationResult{}, errors.New("relay project keycard changed during registration")
	}
	g.rememberCredential(call.Bootstrap().Child(), credential, hashToken(currentToken))
	project, err := legacyrelay.NewCanonicalProjectRef(canonicalProject)
	if err != nil {
		return legacyrelay.RegistrationResult{}, err
	}
	routes, err := legacyrelay.NewRouteSet(legacyrelay.RouteSessionStartContext, legacyrelay.RouteAmbientCandidates)
	if err != nil {
		return legacyrelay.RegistrationResult{}, err
	}
	return legacyrelay.NewRegistrationResult(project, credential, routes)
}

func (g *LegacyRelayGateway) GetSessionStartContext(ctx context.Context, call legacyrelay.SessionStartCall) (legacyrelay.SessionStartPayload, error) {
	authorization := call.Authorization()
	config, token, err := g.authorizedProjectConfig(authorization)
	if err != nil {
		return legacyrelay.SessionStartPayload{}, err
	}
	identity, err := decodeRelayDescriptor(authorization.Descriptor())
	if err != nil {
		return legacyrelay.SessionStartPayload{}, err
	}
	connection, err := g.module.pool.getOrDialGRPC(config.serverURL, token)
	if err != nil {
		return legacyrelay.SessionStartPayload{}, fmt.Errorf("relay session-start gRPC connection: %w", err)
	}
	response, err := pb.NewEngramServiceClient(connection).GetSessionStartContext(ctx, &pb.GetSessionStartContextRequest{
		ProjectIdentityV3: identity,
		HostSessionRef:    authorization.HostSession().Value(),
		RelayRevision:     legacyrelay.AdapterRevisionHAP01B,
	}, grpc.WaitForReady(true))
	if err != nil {
		return legacyrelay.SessionStartPayload{}, g.classifyCredentialError("relay session-start", err)
	}
	if err := validateV3Resolution(response.GetProjectResolutionV3(), authorization.Project().Value(), identity); err != nil {
		return legacyrelay.SessionStartPayload{}, err
	}
	payload, err := sessioncompat.MarshalResponse(response)
	if err != nil {
		return legacyrelay.SessionStartPayload{}, fmt.Errorf("marshal relay session-start payload: %w", err)
	}
	return legacyrelay.NewSessionStartPayload(payload)
}

func (g *LegacyRelayGateway) GetAmbientCandidates(ctx context.Context, call legacyrelay.AmbientCandidatesCall) (legacyrelay.AdditionalContext, error) {
	authorization := call.Authorization()
	config, token, err := g.authorizedProjectConfig(authorization)
	if err != nil {
		return legacyrelay.AdditionalContext{}, err
	}
	identity, err := decodeRelayDescriptor(authorization.Descriptor())
	if err != nil {
		return legacyrelay.AdditionalContext{}, err
	}
	connection, err := g.module.pool.getOrDialGRPC(config.serverURL, token)
	if err != nil {
		return legacyrelay.AdditionalContext{}, fmt.Errorf("relay ambient gRPC connection: %w", err)
	}
	response, err := pb.NewEngramServiceClient(connection).GetAmbientCandidates(ctx, &pb.GetAmbientCandidatesRequest{
		ProjectIdentityV3: identity,
		HostSessionRef:    authorization.HostSession().Value(),
		QueryText:         call.QueryText(),
		Limit:             3,
		RelayRevision:     legacyrelay.AdapterRevisionHAP01B,
	}, grpc.WaitForReady(true))
	if err != nil {
		return legacyrelay.AdditionalContext{}, g.classifyCredentialError("relay ambient", err)
	}
	return legacyrelay.NewAdditionalContext(response.GetAdditionalContext())
}

func (g *LegacyRelayGateway) configFor(child legacyrelay.ChildBinding, requireRegistration bool) (relayChildConfig, error) {
	env, ok := g.children.ConfigFor(child.ConfigRef())
	if !ok {
		return relayChildConfig{}, errors.New("relay child configuration is unavailable")
	}
	return parseRelayChildConfig(env, requireRegistration)
}

func (g *LegacyRelayGateway) authorizedProjectConfig(authorization legacyrelay.AuthorizedSession) (relayChildConfig, string, error) {
	config, err := g.configFor(authorization.Child(), false)
	if err != nil {
		return relayChildConfig{}, "", fmt.Errorf("%w: child configuration unavailable", legacyrelay.ErrCredentialInvalid)
	}
	project := authorization.Project().Value()
	token, ok := config.projectTokens[project]
	if !ok {
		return relayChildConfig{}, "", fmt.Errorf("%w: project keycard unavailable", legacyrelay.ErrCredentialInvalid)
	}
	currentCredential, err := relayCredentialRef(config.serverURL, project, token)
	if err != nil || currentCredential != authorization.Credential() {
		return relayChildConfig{}, "", fmt.Errorf("%w: project keycard changed", legacyrelay.ErrCredentialInvalid)
	}
	return config, token, nil
}

func (g *LegacyRelayGateway) classifyCredentialError(operation string, err error) error {
	code := status.Code(err)
	if code == codes.Unauthenticated || code == codes.PermissionDenied {
		return fmt.Errorf("%w: %s authorization refused", legacyrelay.ErrCredentialInvalid, operation)
	}
	return fmt.Errorf("%s: %w", operation, err)
}

func (g *LegacyRelayGateway) rememberCredential(child legacyrelay.ChildBinding, credential legacyrelay.CredentialRef, tokenHash string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.known[credential.Value()] = tokenHash
	credentials := g.childCredentials[child]
	if credentials == nil {
		credentials = make(map[string]legacyrelay.CredentialRef)
		g.childCredentials[child] = credentials
	}
	credentials[credential.Value()] = credential
}

func (g *LegacyRelayGateway) invalidateChild(child legacyrelay.ChildBinding) {
	g.mu.Lock()
	credentials := g.childCredentials[child]
	delete(g.childCredentials, child)
	g.mu.Unlock()
	for _, credential := range credentials {
		g.capabilities.RevokeCredential(credential)
		g.InvalidateCredential(credential)
	}
}

// InvalidateCredential performs only post-lease local cleanup. Relay calls it
// after deleting capability records; child replacement calls it after waiting
// for capability authorization leases to drain.
func (g *LegacyRelayGateway) InvalidateCredential(credential legacyrelay.CredentialRef) {
	g.mu.Lock()
	tokenHash := g.known[credential.Value()]
	delete(g.known, credential.Value())
	for child, credentials := range g.childCredentials {
		delete(credentials, credential.Value())
		if len(credentials) == 0 {
			delete(g.childCredentials, child)
		}
	}
	g.mu.Unlock()
	if tokenHash != "" {
		g.module.pool.closeTokenHash(tokenHash)
	}
}

func decodeRelayDescriptor(descriptor legacyrelay.ProjectIdentityV3Descriptor) (*pb.ProjectIdentityV3, error) {
	var identity pb.ProjectIdentityV3
	if err := (protojson.UnmarshalOptions{DiscardUnknown: false}).Unmarshal(descriptor.RawJSON(), &identity); err != nil {
		return nil, errors.New("relay project descriptor is invalid")
	}
	return &identity, nil
}

var (
	_ legacyrelay.Gateway               = (*LegacyRelayGateway)(nil)
	_ legacyrelay.CredentialInvalidator = (*LegacyRelayGateway)(nil)
)
