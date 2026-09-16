package gorm

import "time"

type UCISpaceState string

const (
	UCISpaceActive  UCISpaceState = "active"
	UCISpaceRetired UCISpaceState = "retired"
)

type UCISourceKind string

const (
	UCISourceGit         UCISourceKind = "git"
	UCISourceDirectory   UCISourceKind = "directory"
	UCISourceDocumentSet UCISourceKind = "document_set"
)

type UCISourceState string

const (
	UCISourceActive  UCISourceState = "active"
	UCISourceOffline UCISourceState = "offline"
	UCISourceRetired UCISourceState = "retired"
)

type UCIAliasMappingState string

const (
	UCIAliasResolved  UCIAliasMappingState = "resolved"
	UCIAliasAmbiguous UCIAliasMappingState = "ambiguous"
	UCIAliasUnmapped  UCIAliasMappingState = "unmapped"
	UCIAliasRetired   UCIAliasMappingState = "retired"
)

type UCICheckoutKind string

const (
	UCICheckoutWorkingTree  UCICheckoutKind = "working_tree"
	UCICheckoutCommitReader UCICheckoutKind = "commit_reader"
)

type UCICheckoutState string

const (
	UCICheckoutConfigured   UCICheckoutState = "configured"
	UCICheckoutRegistered   UCICheckoutState = "registered"
	UCICheckoutWatching     UCICheckoutState = "watching"
	UCICheckoutCatchingUp   UCICheckoutState = "catching_up"
	UCICheckoutOffline      UCICheckoutState = "offline"
	UCICheckoutUnregistered UCICheckoutState = "unregistered"
)

type UCIViewState string

const (
	UCIViewStaging    UCIViewState = "staging"
	UCIViewPublished  UCIViewState = "published"
	UCIViewSuperseded UCIViewState = "superseded"
	UCIViewRetired    UCIViewState = "retired"
)

type UCISpace struct {
	SpaceID     string        `gorm:"column:space_id;type:uuid;primaryKey"`
	AuthRealm   string        `gorm:"column:auth_realm;type:text;not null"`
	DisplayName string        `gorm:"column:display_name;type:text;not null"`
	State       UCISpaceState `gorm:"column:state;type:text;not null"`
	CreatedAt   time.Time     `gorm:"column:created_at;type:timestamptz;not null"`
	UpdatedAt   time.Time     `gorm:"column:updated_at;type:timestamptz;not null"`
}

func (UCISpace) TableName() string { return "spaces" }

type UCISource struct {
	SourceID    string         `gorm:"column:source_id;type:uuid;primaryKey"`
	AuthRealm   string         `gorm:"column:auth_realm;type:text;not null"`
	Kind        UCISourceKind  `gorm:"column:kind;type:text;not null"`
	DisplayName string         `gorm:"column:display_name;type:text;not null"`
	State       UCISourceState `gorm:"column:state;type:text;not null"`
	CreatedAt   time.Time      `gorm:"column:created_at;type:timestamptz;not null"`
	UpdatedAt   time.Time      `gorm:"column:updated_at;type:timestamptz;not null"`
}

func (UCISource) TableName() string { return "sources" }

type UCISpaceSource struct {
	AuthRealm    string `gorm:"column:auth_realm;type:text;not null"`
	SpaceID      string `gorm:"column:space_id;type:uuid;primaryKey"`
	SourceID     string `gorm:"column:source_id;type:uuid;primaryKey"`
	DisplayOrder int    `gorm:"column:display_order;not null"`
}

func (UCISpaceSource) TableName() string { return "space_sources" }

type UCILegacyContextAlias struct {
	AliasID         string               `gorm:"column:alias_id;type:uuid;primaryKey"`
	AuthRealm       string               `gorm:"column:auth_realm;type:text;not null"`
	LegacyDomain    string               `gorm:"column:legacy_domain;type:text;not null"`
	Scheme          string               `gorm:"column:scheme;type:text;not null"`
	Value           string               `gorm:"column:value;type:text;not null"`
	ClientNamespace string               `gorm:"column:client_namespace;type:text;not null"`
	SpaceID         *string              `gorm:"column:space_id;type:uuid"`
	SourceID        *string              `gorm:"column:source_id;type:uuid"`
	MappingState    UCIAliasMappingState `gorm:"column:mapping_state;type:text;not null"`
	Revision        int64                `gorm:"column:revision;not null"`
	Provenance      string               `gorm:"column:provenance;type:jsonb;not null"`
	CreatedAt       time.Time            `gorm:"column:created_at;type:timestamptz;not null"`
	UpdatedAt       time.Time            `gorm:"column:updated_at;type:timestamptz;not null"`
}

func (UCILegacyContextAlias) TableName() string { return "legacy_context_aliases" }

type UCIAnalysisProfile struct {
	ProfileID            string    `gorm:"column:profile_id;type:uuid;primaryKey"`
	ParserBundleDigest   string    `gorm:"column:parser_bundle_digest;type:text;not null"`
	ResolverRevision     string    `gorm:"column:resolver_revision;type:text;not null"`
	ChunkerRevision      string    `gorm:"column:chunker_revision;type:text;not null"`
	IgnorePolicyDigest   string    `gorm:"column:ignore_policy_digest;type:text;not null"`
	BuildContextJSON     string    `gorm:"column:build_context_json;type:jsonb;not null"`
	SecretPolicyRevision string    `gorm:"column:secret_policy_revision;type:text;not null"`
	CreatedAt            time.Time `gorm:"column:created_at;type:timestamptz;not null"`
}

func (UCIAnalysisProfile) TableName() string { return "ci_profiles" }

type UCICheckout struct {
	CheckoutID     string           `gorm:"column:checkout_id;type:uuid;primaryKey"`
	SourceID       string           `gorm:"column:source_id;type:uuid;not null"`
	WorkstationID  string           `gorm:"column:workstation_id;type:text;not null"`
	IncarnationID  string           `gorm:"column:incarnation_id;type:uuid;not null"`
	Kind           UCICheckoutKind  `gorm:"column:kind;type:text;not null"`
	OwnerPrincipal string           `gorm:"column:owner_principal;type:text;not null"`
	LocatorRef     string           `gorm:"column:locator_ref;type:text;not null"`
	CurrentViewID  *string          `gorm:"column:current_view_id;type:uuid"`
	State          UCICheckoutState `gorm:"column:state;type:text;not null"`
	LeaseEpoch     int64            `gorm:"column:lease_epoch;not null"`
	OwnerInstance  *string          `gorm:"column:owner_instance;type:text"`
	LeaseExpiresAt *time.Time       `gorm:"column:lease_expires_at;type:timestamptz"`
	CreatedAt      time.Time        `gorm:"column:created_at;type:timestamptz;not null"`
	UpdatedAt      time.Time        `gorm:"column:updated_at;type:timestamptz;not null"`
}

func (UCICheckout) TableName() string { return "ci_checkouts" }

type UCIView struct {
	ViewID         string       `gorm:"column:view_id;type:uuid;primaryKey"`
	CheckoutID     string       `gorm:"column:checkout_id;type:uuid;not null"`
	SourceID       string       `gorm:"column:source_id;type:uuid;not null"`
	IncarnationID  string       `gorm:"column:incarnation_id;type:uuid;not null"`
	Generation     int64        `gorm:"column:generation;not null"`
	ProfileID      string       `gorm:"column:profile_id;type:uuid;not null"`
	HeadOID        *string      `gorm:"column:head_oid;type:text"`
	ObjectFormat   *string      `gorm:"column:object_format;type:text"`
	RefLabel       *string      `gorm:"column:ref_label;type:text"`
	Dirty          bool         `gorm:"column:dirty;not null"`
	ObservedFSSeq  int64        `gorm:"column:observed_fs_seq;not null"`
	ScanStart      time.Time    `gorm:"column:scan_start;type:timestamptz;not null"`
	ScanEnd        time.Time    `gorm:"column:scan_end;type:timestamptz;not null"`
	ManifestDigest string       `gorm:"column:manifest_digest;type:text;not null"`
	State          UCIViewState `gorm:"column:state;type:text;not null"`
	CoverageJSON   string       `gorm:"column:coverage_json;type:jsonb;not null"`
	PublishedAt    *time.Time   `gorm:"column:published_at;type:timestamptz"`
	CreatedAt      time.Time    `gorm:"column:created_at;type:timestamptz;not null"`
	UpdatedAt      time.Time    `gorm:"column:updated_at;type:timestamptz;not null"`
}

func (UCIView) TableName() string { return "ci_views" }

type CreateSpaceInput struct {
	AuthRealm   string
	DisplayName string
}

type CreateSourceInput struct {
	AuthRealm   string
	Kind        UCISourceKind
	DisplayName string
}

type LinkSpaceSourceInput struct {
	SpaceID      string
	SourceID     string
	DisplayOrder int
}

type RegisterCheckoutInput struct {
	SourceID       string
	WorkstationID  string
	Kind           UCICheckoutKind
	OwnerPrincipal string
	LocatorRef     string
	OwnerInstance  string
}

type CreateProfileInput struct {
	ParserBundleDigest   string
	ResolverRevision     string
	ChunkerRevision      string
	IgnorePolicyDigest   string
	BuildContextJSON     string
	SecretPolicyRevision string
}

type CreateViewInput struct {
	CheckoutID     string
	SourceID       string
	IncarnationID  string
	Generation     int64
	ProfileID      string
	HeadOID        *string
	ObjectFormat   *string
	RefLabel       *string
	Dirty          bool
	ObservedFSSeq  int64
	ScanStart      time.Time
	ScanEnd        time.Time
	ManifestDigest string
	CoverageJSON   string
	State          UCIViewState
}

type LegacyContextAliasInput struct {
	AuthRealm       string
	LegacyDomain    string
	Scheme          string
	Value           string
	ClientNamespace string
	SpaceID         *string
	SourceID        *string
	MappingState    UCIAliasMappingState
	Revision        int64
	Provenance      string
}

type LegacyContextAliasKey struct {
	AuthRealm       string
	LegacyDomain    string
	Scheme          string
	Value           string
	ClientNamespace string
}
