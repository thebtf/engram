package intervention

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"hash"
	"io"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"golang.org/x/text/unicode/norm"

	"github.com/thebtf/engram/internal/privacy"
	"github.com/thebtf/engram/internal/redaction"
	"github.com/thebtf/engram/internal/taskmemory"
)

const (
	policyCompilerVersion      = "intervention-compiler/1"
	policyAlgorithmVersion     = "intervention-algorithm/1"
	policyNormalizationVersion = "intervention-normalization/1"
	policyParameterVersion     = "intervention-parameters/1:"

	policyScopeDerivationDomain      = "engram.intervention-scope/v1"
	policySourceFingerprintDomain    = "engram.intervention-source-memory/v1"
	policySourceProvenanceDomain     = "engram.intervention-source-version/v1"
	policyDescriptorCommitmentDomain = "engram.intervention-descriptor/v1"
	policyIdentityDomain             = "engram.intervention-policy-id/v1"
	policyVersionDomain              = "engram.intervention-policy-version/v1"

	maxPolicySourceBytes = 64 * 1024
	maxPolicyAtoms       = 8
)

// PolicySemanticVersions identifies one deterministic policy generation.
type PolicySemanticVersions struct {
	Compiler      string
	Algorithm     string
	Normalization string
	Parameter     string
}

// Valid reports whether every persisted semantic-version label is canonical.
func (v PolicySemanticVersions) Valid() bool {
	return validPolicyVersionLabel(v.Compiler) &&
		validPolicyVersionLabel(v.Algorithm) &&
		validPolicyVersionLabel(v.Normalization) &&
		validPolicyVersionLabel(v.Parameter)
}

func validPolicyVersionLabel(value string) bool {
	if value == "" || len(value) > 256 || !utf8.ValidString(value) || strings.TrimSpace(value) != value {
		return false
	}
	for _, r := range value {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}

// SourceMemoryScopeInput is the content-free current access projection used by
// policy persistence and request-time scope revalidation.
type SourceMemoryScopeInput struct {
	CanonicalProject    string
	PrivacyScope        string
	SourceWorkstationID string
	SourceSessions      []string
	OwnerPrincipal      string
	OwnerPrincipalKind  string
	AgentVisibility     string
	Domain              string
}

// SourceMemoryInput is the raw, complete memory-version projection admitted from
// the policy repository boundary.
type SourceMemoryInput struct {
	ID                  int64
	Version             int
	CanonicalProject    string
	Content             string
	Tags                []string
	Status              string
	DeletedAt           *time.Time
	ValidFrom           *time.Time
	ValidUntil          *time.Time
	SupersededBy        *int64
	PrivacyScope        string
	SourceWorkstationID string
	SourceSessions      []string
	OwnerPrincipal      string
	OwnerPrincipalKind  string
	AgentVisibility     string
	Domain              string
}

// SourceMemoryScope is the immutable access-scope projection committed into a
// policy identity. It deliberately excludes mutable lifecycle state.
type SourceMemoryScope struct {
	canonicalProject    string
	privacyScope        string
	sourceWorkstationID string
	sourceSessions      []string
	ownerPrincipal      string
	ownerPrincipalKind  string
	agentVisibility     string
	domain              string
}

// CanonicalProject returns the canonical project UUID committed into the scope.
func (s SourceMemoryScope) CanonicalProject() string {
	return s.canonicalProject
}

// PrivacyScope returns the normalized privacy scope.
func (s SourceMemoryScope) PrivacyScope() string {
	return s.privacyScope
}

// SourceWorkstationID returns the normalized source workstation reference.
func (s SourceMemoryScope) SourceWorkstationID() string {
	return s.sourceWorkstationID
}

// SourceSessions returns a copied sorted unique source-session projection.
func (s SourceMemoryScope) SourceSessions() []string {
	return append([]string(nil), s.sourceSessions...)
}

// OwnerPrincipal returns the normalized source owner principal.
func (s SourceMemoryScope) OwnerPrincipal() string {
	return s.ownerPrincipal
}

// OwnerPrincipalKind returns the normalized source owner kind.
func (s SourceMemoryScope) OwnerPrincipalKind() string {
	return s.ownerPrincipalKind
}

// AgentVisibility returns the normalized source agent visibility.
func (s SourceMemoryScope) AgentVisibility() string {
	return s.agentVisibility
}

// Domain returns the normalized source domain.
func (s SourceMemoryScope) Domain() string {
	return s.domain
}

func (s SourceMemoryScope) valid() bool {
	if !validCanonicalUUID(s.canonicalProject) || !validPrivacyScope(s.privacyScope) ||
		!validPolicyScopeText(s.sourceWorkstationID) ||
		!validPrincipal(s.ownerPrincipal, s.ownerPrincipalKind) ||
		!validAgentVisibility(s.agentVisibility) ||
		!validPolicyScopeText(s.domain) {
		return false
	}
	for index, session := range s.sourceSessions {
		if !validOpaqueReference(session) || (index > 0 && session <= s.sourceSessions[index-1]) {
			return false
		}
	}
	return true
}

func newSourceMemoryScope(input SourceMemoryInput) (SourceMemoryScope, error) {
	return NewSourceMemoryScope(SourceMemoryScopeInput{
		CanonicalProject:    input.CanonicalProject,
		PrivacyScope:        input.PrivacyScope,
		SourceWorkstationID: input.SourceWorkstationID,
		SourceSessions:      input.SourceSessions,
		OwnerPrincipal:      input.OwnerPrincipal,
		OwnerPrincipalKind:  input.OwnerPrincipalKind,
		AgentVisibility:     input.AgentVisibility,
		Domain:              input.Domain,
	})
}

// NewSourceMemoryScope validates and copies the exact access facts used by the
// shared TaskMemory predicate without retaining source content or descriptor data.
func NewSourceMemoryScope(input SourceMemoryScopeInput) (SourceMemoryScope, error) {
	privacyScope := strings.ToLower(strings.TrimSpace(input.PrivacyScope))
	if privacyScope == "" {
		privacyScope = "project"
	}
	if !validPrivacyScope(privacyScope) {
		return SourceMemoryScope{}, ErrInvalidInput
	}
	sessions, err := normalizeSourceSessions(input.SourceSessions)
	if err != nil {
		return SourceMemoryScope{}, err
	}
	scope := SourceMemoryScope{
		canonicalProject:    input.CanonicalProject,
		privacyScope:        privacyScope,
		sourceWorkstationID: strings.TrimSpace(input.SourceWorkstationID),
		sourceSessions:      sessions,
		ownerPrincipal:      strings.TrimSpace(input.OwnerPrincipal),
		ownerPrincipalKind:  strings.ToLower(strings.TrimSpace(input.OwnerPrincipalKind)),
		agentVisibility:     strings.ToLower(strings.TrimSpace(input.AgentVisibility)),
		domain:              strings.TrimSpace(input.Domain),
	}
	if !scope.valid() {
		return SourceMemoryScope{}, ErrInvalidInput
	}
	return scope, nil
}

func normalizeSourceSessions(input []string) ([]string, error) {
	if len(input) == 0 {
		return nil, nil
	}
	result := make([]string, 0, len(input))
	for _, session := range input {
		if !validPolicyScopeText(session) {
			return nil, ErrInvalidInput
		}
		session = strings.TrimSpace(session)
		if session == "" {
			continue
		}
		if !validOpaqueReference(session) {
			return nil, ErrInvalidInput
		}
		result = append(result, session)
	}
	sort.Strings(result)
	if len(result) < 2 {
		return result, nil
	}
	write := 1
	for _, session := range result[1:] {
		if session == result[write-1] {
			continue
		}
		result[write] = session
		write++
	}
	return result[:write], nil
}

func validPrivacyScope(value string) bool {
	switch value {
	case "private", "project", "shared", "global":
		return true
	default:
		return false
	}
}

func validAgentVisibility(value string) bool {
	switch value {
	case "", "private", "shared":
		return true
	default:
		return false
	}
}

func validPolicyScopeText(value string) bool {
	if len(value) > MaxOpaqueReferenceBytes || !utf8.ValidString(value) {
		return false
	}
	for _, r := range value {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}

// SourceMemoryVersion is an immutable, complete source-memory version for one
// deterministic compilation attempt.
type SourceMemoryVersion struct {
	id           int64
	version      int
	content      string
	tags         []string
	status       string
	deletedAt    *time.Time
	validFrom    *time.Time
	validUntil   *time.Time
	supersededBy *int64
	scope        SourceMemoryScope
	fingerprint  Digest
}

// NewSourceMemoryVersion validates and copies one raw source-memory version.
func NewSourceMemoryVersion(input SourceMemoryInput) (SourceMemoryVersion, error) {
	if input.ID <= 0 || input.Version <= 0 || !validCanonicalUUID(input.CanonicalProject) ||
		!utf8.ValidString(input.Content) || !validPolicySourceStatus(input.Status) {
		return SourceMemoryVersion{}, ErrInvalidInput
	}
	for _, tag := range input.Tags {
		if !utf8.ValidString(tag) {
			return SourceMemoryVersion{}, ErrInvalidInput
		}
	}
	if !validPolicySourceTime(input.DeletedAt) || !validPolicySourceTime(input.ValidFrom) || !validPolicySourceTime(input.ValidUntil) ||
		(input.ValidFrom != nil && input.ValidUntil != nil && input.ValidFrom.After(*input.ValidUntil)) ||
		(input.SupersededBy != nil && *input.SupersededBy <= 0) {
		return SourceMemoryVersion{}, ErrInvalidInput
	}
	scope, err := newSourceMemoryScope(input)
	if err != nil {
		return SourceMemoryVersion{}, err
	}
	value := SourceMemoryVersion{
		id:           input.ID,
		version:      input.Version,
		content:      strings.Clone(input.Content),
		tags:         append([]string(nil), input.Tags...),
		status:       strings.ToLower(strings.TrimSpace(input.Status)),
		deletedAt:    clonePolicyTime(input.DeletedAt),
		validFrom:    clonePolicyTime(input.ValidFrom),
		validUntil:   clonePolicyTime(input.ValidUntil),
		supersededBy: clonePolicyInt64(input.SupersededBy),
		scope:        scope,
	}
	value.fingerprint = sourceMemoryFingerprint(value)
	return value, nil
}

func validPolicySourceStatus(value string) bool {
	if !validPolicyScopeText(value) {
		return false
	}
	return strings.TrimSpace(value) == value
}

func validPolicySourceTime(value *time.Time) bool {
	return value == nil || !value.IsZero()
}

func clonePolicyTime(value *time.Time) *time.Time {
	if value == nil {
		return nil
	}
	cloned := value.UTC()
	return &cloned
}

func clonePolicyInt64(value *int64) *int64 {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}

// ID returns the exact source-memory identity.
func (s SourceMemoryVersion) ID() int64 {
	return s.id
}

// Version returns the exact immutable source-memory version.
func (s SourceMemoryVersion) Version() int {
	return s.version
}

// CanonicalProject returns the authoritative source project UUID.
func (s SourceMemoryVersion) CanonicalProject() string {
	return s.scope.canonicalProject
}

// Content returns an independently owned source-content string.
func (s SourceMemoryVersion) Content() string {
	return strings.Clone(s.content)
}

// Tags returns an independently owned source-tag slice.
func (s SourceMemoryVersion) Tags() []string {
	return append([]string(nil), s.tags...)
}

// Status returns the normalized source lifecycle status.
func (s SourceMemoryVersion) Status() string {
	return s.status
}

// DeletedAt returns a copied deletion timestamp when the source is deleted.
func (s SourceMemoryVersion) DeletedAt() *time.Time {
	return clonePolicyTime(s.deletedAt)
}

// ValidFrom returns a copied lower validity bound when present.
func (s SourceMemoryVersion) ValidFrom() *time.Time {
	return clonePolicyTime(s.validFrom)
}

// ValidUntil returns a copied upper validity bound when present.
func (s SourceMemoryVersion) ValidUntil() *time.Time {
	return clonePolicyTime(s.validUntil)
}

// SupersededBy returns a copied successor reference when the source is superseded.
func (s SourceMemoryVersion) SupersededBy() *int64 {
	return clonePolicyInt64(s.supersededBy)
}

// Scope returns an immutable copy of the source access scope.
func (s SourceMemoryVersion) Scope() SourceMemoryScope {
	copied := s.scope
	copied.sourceSessions = append([]string(nil), s.scope.sourceSessions...)
	return copied
}

// SourceFingerprint returns the full deterministic source projection used by a
// repository commit revalidation. It never exposes source content.
func (s SourceMemoryVersion) SourceFingerprint() Digest {
	return s.fingerprint
}

func (s SourceMemoryVersion) valid() bool {
	if s.id <= 0 || s.version <= 0 || !utf8.ValidString(s.content) || !validPolicySourceStatus(s.status) || !s.scope.valid() || s.fingerprint == (Digest{}) {
		return false
	}
	for _, tag := range s.tags {
		if !utf8.ValidString(tag) {
			return false
		}
	}
	if !validPolicySourceTime(s.deletedAt) || !validPolicySourceTime(s.validFrom) || !validPolicySourceTime(s.validUntil) ||
		(s.validFrom != nil && s.validUntil != nil && s.validFrom.After(*s.validUntil)) ||
		(s.supersededBy != nil && *s.supersededBy <= 0) {
		return false
	}
	return s.fingerprint == sourceMemoryFingerprint(s)
}

func sourceMemoryFingerprint(source SourceMemoryVersion) Digest {
	encoder := newSHA256Encoder()
	encoder.text(policySourceFingerprintDomain)
	encoder.int64(source.id)
	encoder.uint32(uint32(source.version))
	encoder.text(source.scope.canonicalProject)
	encoder.text(source.content)
	encoder.uint32(uint32(len(source.tags)))
	for _, tag := range source.tags {
		encoder.text(tag)
	}
	encoder.text(source.status)
	encoder.policyTime(source.deletedAt)
	encoder.policyTime(source.validFrom)
	encoder.policyTime(source.validUntil)
	encoder.boolean(source.supersededBy != nil)
	if source.supersededBy != nil {
		encoder.int64(*source.supersededBy)
	}
	encoder.text(source.scope.privacyScope)
	encoder.text(source.scope.sourceWorkstationID)
	encoder.uint32(uint32(len(source.scope.sourceSessions)))
	for _, session := range source.scope.sourceSessions {
		encoder.text(session)
	}
	encoder.text(source.scope.ownerPrincipal)
	encoder.text(source.scope.ownerPrincipalKind)
	encoder.text(source.scope.agentVisibility)
	encoder.text(source.scope.domain)
	return encoder.sum()
}

func (s SourceMemoryVersion) sourceVersionProvenance() Digest {
	return sourceVersionProvenance(s.id, s.version)
}

func sourceVersionProvenance(memoryID int64, memoryVersion int) Digest {
	encoder := newSHA256Encoder()
	encoder.text(policySourceProvenanceDomain)
	encoder.int64(memoryID)
	encoder.uint32(uint32(memoryVersion))
	return encoder.sum()
}

// PolicyDescriptorStatus is the closed persisted descriptor usability state.
type PolicyDescriptorStatus string

const (
	PolicyDescriptorValid        PolicyDescriptorStatus = "valid"
	PolicyDescriptorInsufficient PolicyDescriptorStatus = "insufficient"
)

func validPolicyDescriptorStatus(status PolicyDescriptorStatus) bool {
	switch status {
	case PolicyDescriptorValid, PolicyDescriptorInsufficient:
		return true
	default:
		return false
	}
}

// PolicyDescriptorOrigin is the closed source of one policy descriptor.
type PolicyDescriptorOrigin string

const (
	PolicyDescriptorOriginDeterministic PolicyDescriptorOrigin = "deterministic"
)

func validPolicyDescriptorOrigin(origin PolicyDescriptorOrigin) bool {
	return origin == PolicyDescriptorOriginDeterministic
}

// PolicyInsufficiencyReason is the closed compiler reason retained in an
// insufficient private descriptor.
type PolicyInsufficiencyReason string

const (
	PolicyInsufficiencySourceOversized  PolicyInsufficiencyReason = "source_oversized"
	PolicyInsufficiencyLineOversized    PolicyInsufficiencyReason = "line_oversized"
	PolicyInsufficiencyContainsSecret   PolicyInsufficiencyReason = "contains_secret"
	PolicyInsufficiencyRedactionMatch   PolicyInsufficiencyReason = "redaction_match"
	PolicyInsufficiencyNoSafeLine       PolicyInsufficiencyReason = "no_safe_line"
	PolicyInsufficiencyControlCharacter PolicyInsufficiencyReason = "control_character"
	PolicyInsufficiencyNonNFC           PolicyInsufficiencyReason = "non_nfc"
	PolicyInsufficiencyNoAtom           PolicyInsufficiencyReason = "no_atom"
)

func validPolicyInsufficiencyReason(reason PolicyInsufficiencyReason) bool {
	switch reason {
	case PolicyInsufficiencySourceOversized,
		PolicyInsufficiencyLineOversized,
		PolicyInsufficiencyContainsSecret,
		PolicyInsufficiencyRedactionMatch,
		PolicyInsufficiencyNoSafeLine,
		PolicyInsufficiencyControlCharacter,
		PolicyInsufficiencyNonNFC,
		PolicyInsufficiencyNoAtom:
		return true
	default:
		return false
	}
}

type canonicalPolicyAtom struct {
	Kind  string `json:"kind"`
	Value string `json:"value"`
}

type canonicalPolicyAction struct {
	Kind       string  `json:"kind"`
	Target     *string `json:"target"`
	TargetKind string  `json:"target_kind"`
}

type canonicalPolicySourceSpan struct {
	EndByte      int    `json:"end_byte"`
	SourceDigest string `json:"source_digest"`
	StartByte    int    `json:"start_byte"`
}

type canonicalPolicyDescriptor struct {
	Action     canonicalPolicyAction     `json:"action"`
	All        []canonicalPolicyAtom     `json:"all"`
	Any        []canonicalPolicyAtom     `json:"any"`
	Negative   []canonicalPolicyAtom     `json:"negative"`
	Origin     PolicyDescriptorOrigin    `json:"origin"`
	SourceSpan canonicalPolicySourceSpan `json:"source_span"`
	Trigger    string                    `json:"trigger"`
}

type canonicalInsufficientPolicyDescriptor struct {
	InsufficiencyReason PolicyInsufficiencyReason `json:"insufficiency_reason"`
	Origin              PolicyDescriptorOrigin    `json:"origin"`
}

// PolicyPersistenceRecord is the complete immutable private policy storage projection.
type PolicyPersistenceRecord struct {
	PolicyID             [32]byte
	PolicyVersion        [32]byte
	ScopeCommitment      [32]byte
	DescriptorCommitment [32]byte
	MemoryID             int64
	MemoryVersion        int
	CanonicalProject     string
	DescriptorJSON       []byte
	DescriptorStatus     PolicyDescriptorStatus
	DescriptorOrigin     PolicyDescriptorOrigin
	CompilerVersion      string
	AlgorithmVersion     string
	NormalizationVersion string
	ParameterVersion     string
	CreatedFromEvent     [32]byte
	CreatedAt            time.Time
}

type policyTrust uint8

const (
	policyTrustUnverified policyTrust = iota
	policyTrustAuthorable
)

// PolicyDefinition is one immutable generated policy. Only compiler-created
// values are authorable at the persistence boundary.
type PolicyDefinition struct {
	record PolicyPersistenceRecord
	source SourceMemoryVersion
	trust  policyTrust
}

// PersistenceRecord returns an independently owned storage projection.
func (p PolicyDefinition) PersistenceRecord() PolicyPersistenceRecord {
	return clonePolicyPersistenceRecord(p.record)
}

// Source returns the exact source memory version for a compiler-created policy.
// Restored persistence rows return the zero SourceMemoryVersion.
func (p PolicyDefinition) Source() SourceMemoryVersion {
	return p.source
}

// CanCommit reports whether a compiler created this policy and it remains a
// structurally valid immutable store input.
func (p PolicyDefinition) CanCommit() bool {
	return p.trust == policyTrustAuthorable && p.source.valid() && p.structurallyValid() &&
		p.record.MemoryID == p.source.id && p.record.MemoryVersion == p.source.version &&
		p.record.CanonicalProject == p.source.scope.canonicalProject
}

func (p PolicyDefinition) exists() bool {
	return p.structurallyValid()
}

func (p PolicyDefinition) structurallyValid() bool {
	return validPolicyPersistenceRecord(p.record)
}

// RestoreUnverifiedPolicy copies and structurally validates a policy row read
// from persistence. It never gives a restored row authoring authority.
func RestoreUnverifiedPolicy(record PolicyPersistenceRecord) (PolicyDefinition, error) {
	record = clonePolicyPersistenceRecord(record)
	record.CreatedAt = record.CreatedAt.UTC()
	canonical, ok := canonicalDescriptorFromJSON(record.DescriptorStatus, record.DescriptorJSON)
	if !ok {
		return PolicyDefinition{}, ErrInvalidInput
	}
	record.DescriptorJSON = canonical
	if !validPolicyPersistenceRecord(record) {
		return PolicyDefinition{}, ErrInvalidInput
	}
	return PolicyDefinition{record: record, trust: policyTrustUnverified}, nil
}

func clonePolicyPersistenceRecord(record PolicyPersistenceRecord) PolicyPersistenceRecord {
	record.DescriptorJSON = append([]byte(nil), record.DescriptorJSON...)
	return record
}

func validPolicyPersistenceRecord(record PolicyPersistenceRecord) bool {
	versions := PolicySemanticVersions{
		Compiler:      record.CompilerVersion,
		Algorithm:     record.AlgorithmVersion,
		Normalization: record.NormalizationVersion,
		Parameter:     record.ParameterVersion,
	}
	if record.PolicyID == ([32]byte{}) || record.PolicyVersion == ([32]byte{}) ||
		record.ScopeCommitment == ([32]byte{}) || record.DescriptorCommitment == ([32]byte{}) ||
		record.CreatedFromEvent == ([32]byte{}) || record.MemoryID <= 0 || record.MemoryVersion <= 0 ||
		!validCanonicalUUID(record.CanonicalProject) || !validPolicyDescriptorStatus(record.DescriptorStatus) ||
		!validPolicyDescriptorOrigin(record.DescriptorOrigin) || !versions.Valid() ||
		!validReceiptTimestamp(record.CreatedAt) {
		return false
	}
	canonical, ok := canonicalDescriptorFromJSON(record.DescriptorStatus, record.DescriptorJSON)
	if !ok || !bytes.Equal(canonical, record.DescriptorJSON) {
		return false
	}
	scope := Digest(record.ScopeCommitment)
	descriptor := descriptorCommitment(record.DescriptorJSON)
	if descriptor != Digest(record.DescriptorCommitment) ||
		sourceVersionProvenance(record.MemoryID, record.MemoryVersion) != Digest(record.CreatedFromEvent) ||
		policyIdentityFromParts(record.MemoryID, record.MemoryVersion, versions, scope, descriptor) != Digest(record.PolicyID) {
		return false
	}
	return policyDefinitionVersion(record) == Digest(record.PolicyVersion)
}

func canonicalDescriptorFromJSON(status PolicyDescriptorStatus, input []byte) ([]byte, bool) {
	switch status {
	case PolicyDescriptorValid:
		var descriptor canonicalPolicyDescriptor
		if !decodeExactJSON(input, &descriptor) || !validCanonicalPolicyDescriptor(descriptor) {
			return nil, false
		}
		canonical, err := canonicalJSON(descriptor)
		return canonical, err == nil
	case PolicyDescriptorInsufficient:
		var descriptor canonicalInsufficientPolicyDescriptor
		if !decodeExactJSON(input, &descriptor) || !validPolicyInsufficientDescriptor(descriptor) {
			return nil, false
		}
		canonical, err := canonicalJSON(descriptor)
		return canonical, err == nil
	default:
		return nil, false
	}
}

func decodeExactJSON(input []byte, target any) bool {
	decoder := json.NewDecoder(bytes.NewReader(input))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return false
	}
	var extra any
	return decoder.Decode(&extra) == io.EOF
}

func validCanonicalPolicyDescriptor(descriptor canonicalPolicyDescriptor) bool {
	if descriptor.Action.Kind != "inspect" || descriptor.Action.TargetKind != "none" || descriptor.Action.Target != nil ||
		descriptor.Origin != PolicyDescriptorOriginDeterministic || descriptor.Trigger != "agent_turn_start" ||
		descriptor.All == nil || descriptor.Negative == nil || len(descriptor.All) != 0 || len(descriptor.Negative) != 0 ||
		len(descriptor.Any) == 0 || len(descriptor.Any) > maxPolicyAtoms ||
		descriptor.SourceSpan.StartByte < 0 || descriptor.SourceSpan.EndByte <= descriptor.SourceSpan.StartByte ||
		!validDigestHex(descriptor.SourceSpan.SourceDigest) {
		return false
	}
	for index, atom := range descriptor.Any {
		if atom.Kind != "keyword" || !validPolicyKeyword(atom.Value) ||
			(index > 0 && atom.Value <= descriptor.Any[index-1].Value) {
			return false
		}
	}
	return true
}

func validPolicyInsufficientDescriptor(descriptor canonicalInsufficientPolicyDescriptor) bool {
	return descriptor.Origin == PolicyDescriptorOriginDeterministic && validPolicyInsufficiencyReason(descriptor.InsufficiencyReason)
}

func validDigestHex(value string) bool {
	if len(value) != hex.EncodedLen(len(Digest{})) {
		return false
	}
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == len(Digest{}) && !bytes.Equal(decoded, make([]byte, len(Digest{})))
}

// PolicyCompilerConfig supplies the immutable compiler dependencies.
type PolicyCompilerConfig struct {
	KeyProvider    KeyProvider
	RedactionRules []redaction.CompiledRule
}

// PolicyCompiler deterministically creates one valid or insufficient policy for
// each source-memory version.
type PolicyCompiler struct {
	keyProvider    KeyProvider
	redactionRules []redaction.CompiledRule
	versions       PolicySemanticVersions
}

// NewPolicyCompiler validates and copies its startup dependencies.
func NewPolicyCompiler(config PolicyCompilerConfig) (*PolicyCompiler, error) {
	if config.KeyProvider == nil {
		return nil, ErrInvalidInput
	}
	fingerprint := redaction.CompiledRulesFingerprint(config.RedactionRules)
	versions := PolicySemanticVersions{
		Compiler:      policyCompilerVersion,
		Algorithm:     policyAlgorithmVersion,
		Normalization: policyNormalizationVersion,
		Parameter:     policyParameterVersion + fingerprint,
	}
	if !versions.Valid() {
		return nil, ErrInvalidInput
	}
	return &PolicyCompiler{
		keyProvider:    config.KeyProvider,
		redactionRules: append([]redaction.CompiledRule(nil), config.RedactionRules...),
		versions:       versions,
	}, nil
}

// Versions returns the fixed semantic-version labels for this compiler instance.
func (c *PolicyCompiler) Versions() PolicySemanticVersions {
	if c == nil {
		return PolicySemanticVersions{}
	}
	return c.versions
}

// Compile produces one immutable valid or insufficient policy without mutating
// its source. Missing compiler dependencies and malformed source values are errors.
func (c *PolicyCompiler) Compile(source SourceMemoryVersion) (PolicyDefinition, error) {
	if c == nil || c.keyProvider == nil || !c.versions.Valid() || !source.valid() {
		return PolicyDefinition{}, ErrInvalidInput
	}
	epoch, current := c.keyProvider.Current()
	if !current {
		return PolicyDefinition{}, ErrInvalidInput
	}
	scopeCommitment, err := epoch.DerivePolicyScope(source.Scope())
	if err != nil {
		return PolicyDefinition{}, ErrInvalidInput
	}

	status, descriptor, err := c.compileDescriptor(source)
	if err != nil {
		return PolicyDefinition{}, err
	}
	return newPolicyDefinition(source, c.versions, scopeCommitment, status, descriptor)
}

func (c *PolicyCompiler) compileDescriptor(source SourceMemoryVersion) (PolicyDescriptorStatus, []byte, error) {
	content := source.content
	if len(content) > maxPolicySourceBytes {
		return insufficientPolicyDescriptor(PolicyInsufficiencySourceOversized)
	}
	if privacy.ContainsSecrets(content) {
		return insufficientPolicyDescriptor(PolicyInsufficiencyContainsSecret)
	}
	_, matched, err := redaction.ScrubCompiled(content, c.redactionRules)
	if err != nil || len(matched) != 0 {
		return insufficientPolicyDescriptor(PolicyInsufficiencyRedactionMatch)
	}

	line, start, end, found := firstNonEmptySourceLine(content)
	if !found {
		return insufficientPolicyDescriptor(PolicyInsufficiencyNoSafeLine)
	}
	if containsPolicyControl(line) {
		return insufficientPolicyDescriptor(PolicyInsufficiencyControlCharacter)
	}
	literal, literalStart, literalEnd := trimPolicyEdgeSpace(line, start, end)
	if literal == "" {
		return insufficientPolicyDescriptor(PolicyInsufficiencyNoSafeLine)
	}
	if len(literal) > MaxPolicyLiteralBytes {
		return insufficientPolicyDescriptor(PolicyInsufficiencyLineOversized)
	}
	if !norm.NFC.IsNormalString(literal) {
		return insufficientPolicyDescriptor(PolicyInsufficiencyNonNFC)
	}
	atoms := policyAtoms(source.tags, literal)
	if len(atoms) == 0 {
		return insufficientPolicyDescriptor(PolicyInsufficiencyNoAtom)
	}

	sourceDigest := sha256.Sum256([]byte(content))
	descriptor := canonicalPolicyDescriptor{
		Action: canonicalPolicyAction{
			Kind:       "inspect",
			Target:     nil,
			TargetKind: "none",
		},
		All:      []canonicalPolicyAtom{},
		Any:      atoms,
		Negative: []canonicalPolicyAtom{},
		Origin:   PolicyDescriptorOriginDeterministic,
		SourceSpan: canonicalPolicySourceSpan{
			EndByte:      literalEnd,
			SourceDigest: hex.EncodeToString(sourceDigest[:]),
			StartByte:    literalStart,
		},
		Trigger: "agent_turn_start",
	}
	encoded, err := canonicalJSON(descriptor)
	if err != nil {
		return "", nil, ErrInvalidInput
	}
	return PolicyDescriptorValid, encoded, nil
}

func insufficientPolicyDescriptor(reason PolicyInsufficiencyReason) (PolicyDescriptorStatus, []byte, error) {
	if !validPolicyInsufficiencyReason(reason) {
		return "", nil, ErrInvalidInput
	}
	encoded, err := canonicalJSON(canonicalInsufficientPolicyDescriptor{
		InsufficiencyReason: reason,
		Origin:              PolicyDescriptorOriginDeterministic,
	})
	if err != nil {
		return "", nil, ErrInvalidInput
	}
	return PolicyDescriptorInsufficient, encoded, nil
}

func firstNonEmptySourceLine(content string) (string, int, int, bool) {
	for start := 0; start < len(content); {
		end := start
		for end < len(content) && content[end] != '\n' && content[end] != '\r' {
			end++
		}
		line := content[start:end]
		trimmed, _, _ := trimPolicyEdgeSpace(line, start, end)
		if trimmed != "" {
			return line, start, end, true
		}
		if end == len(content) {
			break
		}
		if content[end] == '\r' && end+1 < len(content) && content[end+1] == '\n' {
			start = end + 2
		} else {
			start = end + 1
		}
	}
	return "", 0, 0, false
}

func trimPolicyEdgeSpace(line string, start, end int) (string, int, int) {
	left := 0
	for left < len(line) {
		r, size := utf8.DecodeRuneInString(line[left:])
		if !unicode.IsSpace(r) {
			break
		}
		left += size
	}
	right := len(line)
	for right > left {
		r, size := utf8.DecodeLastRuneInString(line[:right])
		if !unicode.IsSpace(r) {
			break
		}
		right -= size
	}
	return line[left:right], start + left, end - (len(line) - right)
}

func containsPolicyControl(value string) bool {
	for _, r := range value {
		if unicode.IsControl(r) {
			return true
		}
	}
	return false
}

func policyAtoms(tags []string, literal string) []canonicalPolicyAtom {
	values := make([]string, 0, maxPolicyAtoms)
	for _, tag := range tags {
		tag = asciiLower(strings.TrimSpace(norm.NFC.String(tag)))
		if validPolicyKeyword(tag) {
			values = append(values, tag)
		}
	}
	if len(values) == 0 {
		values = append(values, fallbackPolicyTokens(literal)...)
	}
	if len(values) == 0 {
		return nil
	}
	sort.Strings(values)
	atoms := make([]canonicalPolicyAtom, 0, maxPolicyAtoms)
	for _, value := range values {
		if len(atoms) > 0 && atoms[len(atoms)-1].Value == value {
			continue
		}
		atoms = append(atoms, canonicalPolicyAtom{Kind: "keyword", Value: value})
		if len(atoms) == maxPolicyAtoms {
			break
		}
	}
	return atoms
}

func fallbackPolicyTokens(literal string) []string {
	lowered := asciiLower(literal)
	values := make([]string, 0, maxPolicyAtoms)
	for start := 0; start < len(lowered); {
		for start < len(lowered) && !isPolicyKeywordByte(lowered[start]) {
			start++
		}
		end := start
		for end < len(lowered) && isPolicyKeywordByte(lowered[end]) {
			end++
		}
		if start < end {
			if token := lowered[start:end]; validPolicyKeyword(token) {
				values = append(values, token)
			}
		}
		start = end + 1
	}
	return values
}

func asciiLower(value string) string {
	for index := range len(value) {
		if value[index] >= 'A' && value[index] <= 'Z' {
			bytes := []byte(value)
			for index := range bytes {
				if bytes[index] >= 'A' && bytes[index] <= 'Z' {
					bytes[index] += 'a' - 'A'
				}
			}
			return string(bytes)
		}
	}
	return value
}

func validPolicyKeyword(value string) bool {
	if len(value) == 0 || len(value) > 64 || !isPolicyKeywordInitial(value[0]) {
		return false
	}
	for index := 1; index < len(value); index++ {
		if !isPolicyKeywordByte(value[index]) {
			return false
		}
	}
	return true
}

func isPolicyKeywordInitial(value byte) bool {
	return value >= 'a' && value <= 'z' || value >= '0' && value <= '9'
}

func isPolicyKeywordByte(value byte) bool {
	return isPolicyKeywordInitial(value) || value == '.' || value == '_' || value == '-'
}

func canonicalJSON(value any) ([]byte, error) {
	var output bytes.Buffer
	encoder := json.NewEncoder(&output)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		return nil, err
	}
	encoded := output.Bytes()
	return append([]byte(nil), encoded[:len(encoded)-1]...), nil
}

func descriptorCommitment(descriptor []byte) Digest {
	encoder := newSHA256Encoder()
	encoder.text(policyDescriptorCommitmentDomain)
	encoder.bytes(descriptor)
	return encoder.sum()
}

func newPolicyDefinition(source SourceMemoryVersion, versions PolicySemanticVersions, scope Digest, status PolicyDescriptorStatus, descriptor []byte) (PolicyDefinition, error) {
	if !source.valid() || !versions.Valid() || scope == (Digest{}) || !validPolicyDescriptorStatus(status) || len(descriptor) == 0 {
		return PolicyDefinition{}, ErrInvalidInput
	}
	descriptorCommitment := descriptorCommitment(descriptor)
	policyID := policyIdentity(source, versions, scope, descriptorCommitment)
	record := PolicyPersistenceRecord{
		PolicyID:             [32]byte(policyID),
		ScopeCommitment:      [32]byte(scope),
		DescriptorCommitment: [32]byte(descriptorCommitment),
		MemoryID:             source.id,
		MemoryVersion:        source.version,
		CanonicalProject:     source.scope.canonicalProject,
		DescriptorJSON:       append([]byte(nil), descriptor...),
		DescriptorStatus:     status,
		DescriptorOrigin:     PolicyDescriptorOriginDeterministic,
		CompilerVersion:      versions.Compiler,
		AlgorithmVersion:     versions.Algorithm,
		NormalizationVersion: versions.Normalization,
		ParameterVersion:     versions.Parameter,
		CreatedFromEvent:     [32]byte(source.sourceVersionProvenance()),
		CreatedAt:            normalizeReceiptTimestamp(time.Now()),
	}
	record.PolicyVersion = [32]byte(policyDefinitionVersion(record))
	definition := PolicyDefinition{record: record, source: source, trust: policyTrustAuthorable}
	if !definition.CanCommit() {
		return PolicyDefinition{}, ErrInvalidInput
	}
	return definition, nil
}

func policyIdentity(source SourceMemoryVersion, versions PolicySemanticVersions, scope, descriptor Digest) Digest {
	return policyIdentityFromParts(source.id, source.version, versions, scope, descriptor)
}

func policyIdentityFromParts(memoryID int64, memoryVersion int, versions PolicySemanticVersions, scope, descriptor Digest) Digest {
	encoder := newSHA256Encoder()
	encoder.text(policyIdentityDomain)
	encoder.int64(memoryID)
	encoder.uint32(uint32(memoryVersion))
	encoder.digest(scope)
	encoder.digest(descriptor)
	encoder.text(versions.Compiler)
	encoder.text(versions.Algorithm)
	encoder.text(versions.Normalization)
	encoder.text(versions.Parameter)
	return encoder.sum()
}

func policyDefinitionVersion(record PolicyPersistenceRecord) Digest {
	encoder := newSHA256Encoder()
	encoder.text(policyVersionDomain)
	encoder.digest(Digest(record.PolicyID))
	encoder.text(string(record.DescriptorStatus))
	encoder.bytes(record.DescriptorJSON)
	encoder.digest(Digest(record.CreatedFromEvent))
	encoder.text(record.CompilerVersion)
	encoder.text(record.AlgorithmVersion)
	encoder.text(record.NormalizationVersion)
	encoder.text(record.ParameterVersion)
	return encoder.sum()
}

// PolicyRepository is the sole compiler/reconciler persistence port.
type PolicyRepository interface {
	ListCompileSources(context.Context, PolicySemanticVersions, int) ([]SourceMemoryVersion, error)
	CommitPolicy(context.Context, PolicyDefinition) (PolicyDefinition, bool, error)
}

// CandidatePolicyState is the closed reader result state for one prepared ref.
type CandidatePolicyState string

const (
	CandidatePolicyValid        CandidatePolicyState = "valid"
	CandidatePolicyInsufficient CandidatePolicyState = "insufficient"
	CandidatePolicyMissing      CandidatePolicyState = "missing"
	CandidatePolicySourceStale  CandidatePolicyState = "source_stale"
)

func validCandidatePolicyState(state CandidatePolicyState) bool {
	switch state {
	case CandidatePolicyValid, CandidatePolicyInsufficient, CandidatePolicyMissing, CandidatePolicySourceStale:
		return true
	default:
		return false
	}
}

// CandidatePolicy is the content-free current-policy projection for a prepared
// candidate. It omits policy identity and descriptor/source content.
type CandidatePolicy struct {
	ref                  taskmemory.AuthorizedCandidateRef
	state                CandidatePolicyState
	policyVersion        Digest
	scopeCommitment      Digest
	descriptorCommitment Digest
	currentScope         SourceMemoryScope
}

// NewCandidatePolicy constructs a content-free policy reader result. Current
// valid/insufficient rows must carry the source scope used for HMAC revalidation.
func NewCandidatePolicy(ref taskmemory.AuthorizedCandidateRef, state CandidatePolicyState, policyVersion, scopeCommitment, descriptorCommitment Digest, currentScope SourceMemoryScope) (CandidatePolicy, error) {
	if _, err := taskmemory.NewAuthorizedCandidateRef(ref.ID(), ref.Version(), ref.SourceTier()); err != nil || !validCandidatePolicyState(state) {
		return CandidatePolicy{}, ErrInvalidInput
	}
	commitmentsPresent := policyVersion != (Digest{}) || scopeCommitment != (Digest{}) || descriptorCommitment != (Digest{})
	switch state {
	case CandidatePolicyValid, CandidatePolicyInsufficient:
		if policyVersion == (Digest{}) || scopeCommitment == (Digest{}) || descriptorCommitment == (Digest{}) || !currentScope.valid() {
			return CandidatePolicy{}, ErrInvalidInput
		}
	case CandidatePolicyMissing, CandidatePolicySourceStale:
		if commitmentsPresent || currentScope.valid() {
			return CandidatePolicy{}, ErrInvalidInput
		}
	}
	return CandidatePolicy{
		ref:                  ref,
		state:                state,
		policyVersion:        policyVersion,
		scopeCommitment:      scopeCommitment,
		descriptorCommitment: descriptorCommitment,
		currentScope:         currentScope,
	}, nil
}

// Ref returns the original exact prepared candidate reference.
func (p CandidatePolicy) Ref() taskmemory.AuthorizedCandidateRef {
	return p.ref
}

// State returns the closed current-policy state.
func (p CandidatePolicy) State() CandidatePolicyState {
	return p.state
}

// PolicyVersion returns the immutable policy-version commitment, when current.
func (p CandidatePolicy) PolicyVersion() Digest {
	return p.policyVersion
}

// ScopeCommitment returns the immutable scope commitment, when current.
func (p CandidatePolicy) ScopeCommitment() Digest {
	return p.scopeCommitment
}

// DescriptorCommitment returns the immutable descriptor commitment, when current.
func (p CandidatePolicy) DescriptorCommitment() Digest {
	return p.descriptorCommitment
}

// CurrentScope returns the content-free current source scope for a current
// valid/insufficient policy. Missing and stale states have no current scope.
func (p CandidatePolicy) CurrentScope() (SourceMemoryScope, bool) {
	if p.state != CandidatePolicyValid && p.state != CandidatePolicyInsufficient || !p.currentScope.valid() {
		return SourceMemoryScope{}, false
	}
	return p.currentScope, true
}

// PolicyReader reads content-free current policy states for exact authorized candidates.
type PolicyReader interface {
	ReadCandidatePolicies(context.Context, taskmemory.AuthorizedTaskContext, []taskmemory.AuthorizedCandidateRef, PolicySemanticVersions) ([]CandidatePolicy, error)
}

type sha256Encoder struct {
	hash hash.Hash
}

func newSHA256Encoder() sha256Encoder {
	return sha256Encoder{hash: sha256.New()}
}

func (e sha256Encoder) sum() Digest {
	var result Digest
	sum := e.hash.Sum(result[:0])
	copy(result[:], sum)
	return result
}

func (e sha256Encoder) bytes(value []byte) {
	var length [4]byte
	binary.BigEndian.PutUint32(length[:], uint32(len(value)))
	_, _ = e.hash.Write(length[:])
	_, _ = e.hash.Write(value)
}

func (e sha256Encoder) text(value string) {
	e.bytes([]byte(value))
}

func (e sha256Encoder) digest(value Digest) {
	e.bytes(value[:])
}

func (e sha256Encoder) uint32(value uint32) {
	var encoded [4]byte
	binary.BigEndian.PutUint32(encoded[:], value)
	_, _ = e.hash.Write(encoded[:])
}

func (e sha256Encoder) int64(value int64) {
	var encoded [8]byte
	binary.BigEndian.PutUint64(encoded[:], uint64(value))
	_, _ = e.hash.Write(encoded[:])
}

func (e sha256Encoder) boolean(value bool) {
	if value {
		e.uint32(1)
		return
	}
	e.uint32(0)
}

func (e sha256Encoder) policyTime(value *time.Time) {
	e.boolean(value != nil)
	if value != nil {
		e.int64(value.UTC().UnixNano())
	}
}
