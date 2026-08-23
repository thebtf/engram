package projectidentity

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
)

var errInvalidComparisonV3 = errors.New("invalid V3 comparison telemetry")

// LegacyComparisonOutcomeV2 is the redacted behavioral result of the legacy
// resolver. It deliberately carries no V2 selector, canonical ID, remote, or
// path because comparison is telemetry rather than a second authority.
type LegacyComparisonOutcomeV2 string

const (
	LegacyComparisonResolvedV2    LegacyComparisonOutcomeV2 = "resolved"
	LegacyComparisonRefusalV2     LegacyComparisonOutcomeV2 = "refusal"
	LegacyComparisonUnavailableV2 LegacyComparisonOutcomeV2 = "unavailable"
)

func (outcome LegacyComparisonOutcomeV2) valid() bool {
	switch outcome {
	case LegacyComparisonResolvedV2, LegacyComparisonRefusalV2, LegacyComparisonUnavailableV2:
		return true
	default:
		return false
	}
}

// ComparisonClassV3 distinguishes agreement, disagreement, a V3 refusal, and
// legacy unavailability. It is derived from the two redacted results and is
// never caller-selected.
type ComparisonClassV3 string

const (
	ComparisonEqualV3       ComparisonClassV3 = "equal"
	ComparisonMismatchV3    ComparisonClassV3 = "mismatch"
	ComparisonRefusalV3     ComparisonClassV3 = "refusal"
	ComparisonUnavailableV3 ComparisonClassV3 = "unavailable"
)

// ComparisonTransportV3 names the observed adapter without retaining a
// transport request or descriptor.
type ComparisonTransportV3 string

const (
	ComparisonTransportGRPCV3     ComparisonTransportV3 = "grpc"
	ComparisonTransportHTTPV3     ComparisonTransportV3 = "http"
	ComparisonTransportHookV3     ComparisonTransportV3 = "hook"
	ComparisonTransportDaemonV3   ComparisonTransportV3 = "daemon"
	ComparisonTransportOpenClawV3 ComparisonTransportV3 = "openclaw"
)

func (transport ComparisonTransportV3) valid() bool {
	switch transport {
	case ComparisonTransportGRPCV3, ComparisonTransportHTTPV3, ComparisonTransportHookV3, ComparisonTransportDaemonV3, ComparisonTransportOpenClawV3:
		return true
	default:
		return false
	}
}

// ComparisonOriginV3 binds a non-authoritative adapter claim to the physical
// request channel and retains an opaque attempt identifier only long enough to
// derive redacted comparison references.
type ComparisonOriginV3 struct {
	transport ComparisonTransportV3
	attemptID string
}

// NewComparisonOriginV3 accepts only labels valid for the physical channel.
// Unknown or cross-channel claims degrade to the physical base transport.
// Unsafe request IDs are replaced before they can contribute to a fingerprint.
func NewComparisonOriginV3(physical ComparisonTransportV3, claim, requestID string) ComparisonOriginV3 {
	base := comparisonPhysicalTransportV3(physical)
	transport := base
	switch base {
	case ComparisonTransportGRPCV3:
		if claim == string(ComparisonTransportDaemonV3) {
			transport = ComparisonTransportDaemonV3
		}
	case ComparisonTransportHTTPV3:
		switch claim {
		case string(ComparisonTransportHookV3):
			transport = ComparisonTransportHookV3
		case string(ComparisonTransportOpenClawV3):
			transport = ComparisonTransportOpenClawV3
		}
	}
	if !validOpaqueReferenceV3(requestID) {
		requestID = uuid.NewString()
	}
	return ComparisonOriginV3{transport: transport, attemptID: requestID}
}

func comparisonPhysicalTransportV3(physical ComparisonTransportV3) ComparisonTransportV3 {
	if physical == ComparisonTransportHTTPV3 {
		return ComparisonTransportHTTPV3
	}
	return ComparisonTransportGRPCV3
}

// Transport returns the channel-validated, non-authoritative adapter label.
func (origin ComparisonOriginV3) Transport() ComparisonTransportV3 { return origin.transport }

// AttemptID returns the safe opaque retry identifier. It is never persisted.
func (origin ComparisonOriginV3) AttemptID() string { return origin.attemptID }

// ComparisonReferencesV3 contains deterministic redacted identifiers for one
// logical V3 operation. It has no raw request, descriptor, or canonical key.
type ComparisonReferencesV3 struct {
	IdempotencyKey      string
	Correlation         CorrelationV3
	EvidenceFingerprint string
}

// DeriveComparisonReferencesV3 derives retry-stable references from validated
// telemetry inputs. Client instance data influences only hashes and cannot
// select or infer the transport.
func (origin ComparisonOriginV3) DeriveComparisonReferencesV3(clientInstanceID string, intent ResolutionIntentV3) (ComparisonReferencesV3, error) {
	if !origin.transport.valid() || !validOpaqueReferenceV3(origin.attemptID) || !intent.Valid() {
		return ComparisonReferencesV3{}, errInvalidComparisonV3
	}
	if _, err := NewCorrelationV3(clientInstanceID); err != nil {
		return ComparisonReferencesV3{}, errInvalidComparisonV3
	}
	values := []string{origin.attemptID, string(origin.transport), clientInstanceID, string(intent)}
	correlation, err := NewCorrelationV3(comparisonFingerprintV3("correlation", values...))
	if err != nil {
		return ComparisonReferencesV3{}, errInvalidComparisonV3
	}
	return ComparisonReferencesV3{
		IdempotencyKey:      comparisonFingerprintV3("idempotency", values...),
		Correlation:         correlation,
		EvidenceFingerprint: comparisonFingerprintV3("evidence", values...),
	}, nil
}

func comparisonFingerprintV3(domain string, values ...string) string {
	hash := sha256.New()
	_, _ = hash.Write([]byte(domain))
	for _, value := range values {
		_, _ = hash.Write([]byte{0})
		_, _ = hash.Write([]byte(value))
	}
	return "sha256:" + hex.EncodeToString(hash.Sum(nil))
}

// ComparisonScopeV3 records the requested V3 scope even when V3 refuses. It
// is evidence only and is not a resolved or canonical scope authority.
type ComparisonScopeV3 string

const (
	ComparisonRepositoryScopeV3 ComparisonScopeV3 = "repository"
	ComparisonDirectoryScopeV3  ComparisonScopeV3 = "directory"
)

func (scope ComparisonScopeV3) valid() bool {
	return scope == ComparisonRepositoryScopeV3 || scope == ComparisonDirectoryScopeV3
}

// ComparisonFreshnessV3 makes stale versus fresh side-by-side observations
// explicit without refreshing either resolver.
type ComparisonFreshnessV3 string

const (
	ComparisonFreshV3   ComparisonFreshnessV3 = "fresh"
	ComparisonStaleV3   ComparisonFreshnessV3 = "stale"
	ComparisonUnknownV3 ComparisonFreshnessV3 = "unknown"
)

func (freshness ComparisonFreshnessV3) valid() bool {
	switch freshness {
	case ComparisonFreshV3, ComparisonStaleV3, ComparisonUnknownV3:
		return true
	default:
		return false
	}
}

// ComparisonObservationV3 is one complete, explicit comparison boundary. Its
// fields are constrained to redacted taxonomies, opaque references, and SHA-256
// fingerprints; it has no descriptor, V2 selector, canonical key, or target.
type ComparisonObservationV3 struct {
	IdempotencyKey      string
	Correlation         CorrelationV3
	V3Outcome           ResolutionOutcomeV3
	LegacyOutcome       LegacyComparisonOutcomeV2
	ClientInstanceID    string
	Transport           ComparisonTransportV3
	Scope               ComparisonScopeV3
	Freshness           ComparisonFreshnessV3
	EvidenceFingerprint string
}

// NewComparisonObservationV3 validates the redacted comparison boundary before
// it reaches a durable telemetry store.
func NewComparisonObservationV3(idempotencyKey string, correlation CorrelationV3, v3Outcome ResolutionOutcomeV3, legacyOutcome LegacyComparisonOutcomeV2, clientInstanceID string, transport ComparisonTransportV3, scope ComparisonScopeV3, freshness ComparisonFreshnessV3, evidenceFingerprint string) (ComparisonObservationV3, error) {
	observation := ComparisonObservationV3{
		IdempotencyKey:      idempotencyKey,
		Correlation:         correlation,
		V3Outcome:           v3Outcome,
		LegacyOutcome:       legacyOutcome,
		ClientInstanceID:    clientInstanceID,
		Transport:           transport,
		Scope:               scope,
		Freshness:           freshness,
		EvidenceFingerprint: evidenceFingerprint,
	}
	if !observation.Valid() {
		return ComparisonObservationV3{}, errInvalidComparisonV3
	}
	return observation, nil
}

// Classification derives the comparison class from the stored outcomes. A V3
// refusal takes precedence over agreement, and a missing legacy result is
// unavailable rather than a synthetic mismatch.
func (observation ComparisonObservationV3) Classification() ComparisonClassV3 {
	if observation.LegacyOutcome == LegacyComparisonUnavailableV2 {
		return ComparisonUnavailableV3
	}
	if observation.V3Outcome.IsRefusal() {
		return ComparisonRefusalV3
	}
	if observation.V3Outcome.IsSuccess() && observation.LegacyOutcome == LegacyComparisonResolvedV2 {
		return ComparisonEqualV3
	}
	return ComparisonMismatchV3
}

// Valid reports whether the observation is a complete, redacted comparison boundary.
func (observation ComparisonObservationV3) Valid() bool {
	if !validComparisonFingerprintV3(observation.IdempotencyKey) || !validOpaqueReferenceV3(string(observation.Correlation)) || !observation.V3Outcome.Valid() || !observation.LegacyOutcome.valid() || !validOpaqueReferenceV3(observation.ClientInstanceID) || !observation.Transport.valid() || !observation.Scope.valid() || !observation.Freshness.valid() || !validComparisonFingerprintV3(observation.EvidenceFingerprint) {
		return false
	}
	return true
}

func validComparisonFingerprintV3(value string) bool {
	const prefix = "sha256:"
	if !strings.HasPrefix(value, prefix) || len(value) != len(prefix)+64 {
		return false
	}
	for _, character := range value[len(prefix):] {
		if character < '0' || character > '9' && character < 'a' || character > 'f' {
			return false
		}
	}
	return true
}

// ComparisonReceiptV3 is the durable, redacted receipt returned for the first
// write and every idempotent replay. It carries no project authority.
type ComparisonReceiptV3 struct {
	RecordID            string
	IdempotencyKey      string
	Correlation         CorrelationV3
	V3Outcome           ResolutionOutcomeV3
	LegacyOutcome       LegacyComparisonOutcomeV2
	Classification      ComparisonClassV3
	ClientInstanceID    string
	Transport           ComparisonTransportV3
	Scope               ComparisonScopeV3
	Freshness           ComparisonFreshnessV3
	EvidenceFingerprint string
	RecordedAt          time.Time
}

// ComparisonStoreV3 is a telemetry-only persistence boundary. It cannot
// create, bind, merge, redirect, reassign, backfill, or cache a project.
type ComparisonStoreV3 interface {
	RecordComparisonV3(context.Context, ComparisonObservationV3) (ComparisonReceiptV3, error)
}

// RecordComparisonV3 records an already-complete comparison boundary. It does
// not invoke either resolver or transform either result into project authority.
func RecordComparisonV3(ctx context.Context, store ComparisonStoreV3, observation ComparisonObservationV3) (ComparisonReceiptV3, error) {
	if store == nil || !observation.Valid() {
		return ComparisonReceiptV3{}, errInvalidComparisonV3
	}
	return store.RecordComparisonV3(ctx, observation)
}

// LegacyComparisonDescriptorV3 exposes only legacy identifiers that survived
// the same anchor/descriptor validation required by central V3 resolution.
type LegacyComparisonDescriptorV3 struct {
	identifiers []LegacyIdentifierV3
}

// NewLegacyComparisonDescriptorV3 creates a query-only legacy comparison
// input. It deliberately rejects raw or partially valid descriptor evidence.
func NewLegacyComparisonDescriptorV3(anchor AnchorV3, descriptor DescriptorV3) (LegacyComparisonDescriptorV3, error) {
	if !validAnchorV3(anchor) || !validDescriptorV3(descriptor) || descriptor.AnchorProjectID != anchor.ProjectID || descriptor.Name != anchor.Name || descriptor.Scope != anchor.Scope {
		return LegacyComparisonDescriptorV3{}, errInvalidComparisonV3
	}
	return LegacyComparisonDescriptorV3{identifiers: append([]LegacyIdentifierV3(nil), descriptor.LegacyIdentifiers...)}, nil
}

// LegacyIdentifiers returns a copy so comparison readers cannot alter the
// validated descriptor evidence retained by the central workflow.
func (descriptor LegacyComparisonDescriptorV3) LegacyIdentifiers() []LegacyIdentifierV3 {
	return append([]LegacyIdentifierV3(nil), descriptor.identifiers...)
}

// LegacyComparisonObserverV2 reads only bounded compatibility outcomes. It
// must not expose a project key or mutate V2/V3 identity state.
type LegacyComparisonObserverV2 interface {
	ObserveLegacyOutcomeV2(context.Context, LegacyComparisonDescriptorV3) (LegacyComparisonOutcomeV2, error)
}

// ComparisonReaderV3 supplies persisted redacted observations for receipt
// construction. Callers must validate exact correlation coverage themselves.
type ComparisonReaderV3 interface {
	ReadComparisonsByCorrelationV3(context.Context, []CorrelationV3) ([]ComparisonObservationV3, error)
}
