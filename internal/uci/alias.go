package uci

import (
	"context"
	"fmt"
	"strings"
)

// LegacyAliasKey identifies one scoped legacy compatibility alias.
type LegacyAliasKey struct {
	AuthRealm       string
	LegacyDomain    string
	Scheme          string
	Value           string
	ClientNamespace string
}

// LegacyAliasRecord is a compatibility mapping returned by the alias catalog.
type LegacyAliasRecord struct {
	Key      LegacyAliasKey
	State    string
	SpaceID  *string
	SourceID *string
}

// AliasTarget contains only the typed scope supplied by a resolved alias.
type AliasTarget struct {
	SpaceID  *string
	SourceID *string
}

// LegacyAliasLookup retrieves compatibility records for one complete legacy key.
type LegacyAliasLookup func(context.Context, LegacyAliasKey) ([]LegacyAliasRecord, error)

// AliasResolver resolves legacy compatibility aliases without selecting a checkout or view.
type AliasResolver struct {
	lookup LegacyAliasLookup
}

// NewAliasResolver creates a fail-closed legacy compatibility resolver.
func NewAliasResolver(lookup LegacyAliasLookup) *AliasResolver {
	return &AliasResolver{lookup: lookup}
}

// Resolve returns one unambiguous typed alias target, or a closed context outcome.
func (resolver *AliasResolver) Resolve(ctx context.Context, key LegacyAliasKey) (AliasTarget, error) {
	if resolver == nil || resolver.lookup == nil || ctx == nil || !key.valid() {
		return AliasTarget{}, newContextError(ContextRequired, nil)
	}

	records, err := resolver.lookup(ctx, key.clone())
	if err != nil {
		return AliasTarget{}, newContextError(ContextRequired, err)
	}

	var resolved *AliasTarget
	for _, record := range records {
		if record.Key != key {
			return AliasTarget{}, newContextError(ContextRequired, fmt.Errorf("legacy alias record key does not match lookup key"))
		}
		if record.State != "resolved" {
			return AliasTarget{}, newContextError(ContextRequired, fmt.Errorf("legacy alias state %q is not resolved", record.State))
		}

		target := AliasTarget{
			SpaceID:  copyAliasOptionalText(record.SpaceID),
			SourceID: copyAliasOptionalText(record.SourceID),
		}
		if target.SpaceID == nil && target.SourceID == nil {
			return AliasTarget{}, newContextError(ContextRequired, fmt.Errorf("resolved legacy alias has no target"))
		}
		if resolved == nil {
			resolved = &target
			continue
		}
		if !resolved.equal(target) {
			return AliasTarget{}, newContextError(ContextRequired, fmt.Errorf("resolved legacy aliases have conflicting targets"))
		}
	}
	if resolved == nil {
		return AliasTarget{}, newContextError(ContextRequired, nil)
	}
	return resolved.clone(), nil
}

func (key LegacyAliasKey) clone() LegacyAliasKey {
	return LegacyAliasKey{
		AuthRealm:       key.AuthRealm,
		LegacyDomain:    key.LegacyDomain,
		Scheme:          key.Scheme,
		Value:           key.Value,
		ClientNamespace: key.ClientNamespace,
	}
}

func (key LegacyAliasKey) valid() bool {
	for _, value := range []string{key.AuthRealm, key.LegacyDomain, key.Scheme, key.Value} {
		if !validAliasText(value) {
			return false
		}
	}
	return key.ClientNamespace == "" || validAliasText(key.ClientNamespace)
}

func (target AliasTarget) clone() AliasTarget {
	return AliasTarget{
		SpaceID:  copyAliasOptionalText(target.SpaceID),
		SourceID: copyAliasOptionalText(target.SourceID),
	}
}

func (target AliasTarget) equal(other AliasTarget) bool {
	return equalAliasOptionalText(target.SpaceID, other.SpaceID) &&
		equalAliasOptionalText(target.SourceID, other.SourceID)
}

func validAliasText(value string) bool {
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

func copyAliasOptionalText(value *string) *string {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func equalAliasOptionalText(left, right *string) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}
