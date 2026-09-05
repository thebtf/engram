package uci

import (
	"context"
	"reflect"
	"strings"
	"testing"
)

const uciAliasContextRequired = "CONTEXT_REQUIRED"

func TestUCIAliasResolverResolutionVectors(t *testing.T) {
	spaceA := "space-a"
	sourceA := "source-a"
	sourceB := "source-b"
	key := LegacyAliasKey{
		AuthRealm:       "realm-a",
		LegacyDomain:    "workspace",
		Scheme:          "path",
		Value:           "legacy-selector",
		ClientNamespace: "client-a",
	}

	for _, tc := range []struct {
		name    string
		records []LegacyAliasRecord
		want    AliasTarget
		wantErr string
	}{
		{
			name: "resolved space only",
			records: []LegacyAliasRecord{{
				Key: key, State: "resolved", SpaceID: &spaceA,
			}},
			want: AliasTarget{SpaceID: &spaceA},
		},
		{
			name: "resolved source only",
			records: []LegacyAliasRecord{{
				Key: key, State: "resolved", SourceID: &sourceA,
			}},
			want: AliasTarget{SourceID: &sourceA},
		},
		{
			name: "resolved space and source",
			records: []LegacyAliasRecord{{
				Key: key, State: "resolved", SpaceID: &spaceA, SourceID: &sourceA,
			}},
			want: AliasTarget{SpaceID: &spaceA, SourceID: &sourceA},
		},
		{
			name: "ambiguous state requires context",
			records: []LegacyAliasRecord{{
				Key: key, State: "ambiguous", SpaceID: &spaceA, SourceID: &sourceA,
			}},
			wantErr: uciAliasContextRequired,
		},
		{
			name: "unmapped state requires context",
			records: []LegacyAliasRecord{{
				Key: key, State: "unmapped",
			}},
			wantErr: uciAliasContextRequired,
		},
		{
			name: "retired state requires context",
			records: []LegacyAliasRecord{{
				Key: key, State: "retired", SpaceID: &spaceA, SourceID: &sourceA,
			}},
			wantErr: uciAliasContextRequired,
		},
		{
			name:    "zero results require context",
			wantErr: uciAliasContextRequired,
		},
		{
			name: "duplicate exact resolved target is idempotent",
			records: []LegacyAliasRecord{
				{Key: key, State: "resolved", SpaceID: &spaceA, SourceID: &sourceA},
				{Key: key, State: "resolved", SpaceID: &spaceA, SourceID: &sourceA},
			},
			want: AliasTarget{SpaceID: &spaceA, SourceID: &sourceA},
		},
		{
			name: "conflicting targets require context",
			records: []LegacyAliasRecord{
				{Key: key, State: "resolved", SpaceID: &spaceA, SourceID: &sourceA},
				{Key: key, State: "resolved", SpaceID: &spaceA, SourceID: &sourceB},
			},
			wantErr: uciAliasContextRequired,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resolver := NewAliasResolver(uciAliasLookup(key, tc.records))
			got, err := resolver.Resolve(context.Background(), key)
			if tc.wantErr != "" {
				uciRequireNonDisclosingAliasError(t, err, tc.wantErr, tc.records)
				return
			}
			if err != nil {
				t.Fatalf("Resolve() error = %v", err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("Resolve() = %#v, want %#v", got, tc.want)
			}
		})
	}
}

func TestUCIAliasResolverUsesCompleteLegacyKey(t *testing.T) {
	base := LegacyAliasKey{
		AuthRealm:       "realm-a",
		LegacyDomain:    "workspace",
		Scheme:          "path",
		Value:           "same-selector",
		ClientNamespace: "client-a",
	}
	keys := []LegacyAliasKey{
		base,
		{AuthRealm: base.AuthRealm, LegacyDomain: base.LegacyDomain, Scheme: base.Scheme, Value: base.Value, ClientNamespace: "client-b"},
		{AuthRealm: base.AuthRealm, LegacyDomain: "project", Scheme: base.Scheme, Value: base.Value, ClientNamespace: base.ClientNamespace},
		{AuthRealm: base.AuthRealm, LegacyDomain: base.LegacyDomain, Scheme: "uri", Value: base.Value, ClientNamespace: base.ClientNamespace},
	}
	lookup := func(_ context.Context, key LegacyAliasKey) ([]LegacyAliasRecord, error) {
		for i, candidate := range keys {
			if key == candidate {
				sourceID := "source-" + string(rune('a'+i))
				return []LegacyAliasRecord{{Key: candidate, State: "resolved", SourceID: &sourceID}}, nil
			}
		}
		return nil, nil
	}
	resolver := NewAliasResolver(lookup)

	for i, key := range keys {
		t.Run(key.ClientNamespace+"/"+key.LegacyDomain+"/"+key.Scheme, func(t *testing.T) {
			got, err := resolver.Resolve(context.Background(), key)
			if err != nil {
				t.Fatalf("Resolve() error = %v", err)
			}
			wantSourceID := "source-" + string(rune('a'+i))
			if got.SourceID == nil || *got.SourceID != wantSourceID {
				t.Fatalf("Resolve() source = %#v, want %q", got.SourceID, wantSourceID)
			}
		})
	}
}

func TestUCIAliasResolverRejectsMismatchedLookupRecord(t *testing.T) {
	requested := LegacyAliasKey{
		AuthRealm:       "realm-a",
		LegacyDomain:    "workspace",
		Scheme:          "path",
		Value:           "legacy-selector",
		ClientNamespace: "client-a",
	}
	returnedKey := requested
	returnedKey.ClientNamespace = "private-client"
	privateSource := "private-source-id"
	records := []LegacyAliasRecord{{
		Key: returnedKey, State: "resolved", SourceID: &privateSource,
	}}
	resolver := NewAliasResolver(func(context.Context, LegacyAliasKey) ([]LegacyAliasRecord, error) {
		return append([]LegacyAliasRecord(nil), records...), nil
	})

	_, err := resolver.Resolve(context.Background(), requested)
	uciRequireNonDisclosingAliasError(t, err, uciAliasContextRequired, records)
}

func uciAliasLookup(wantKey LegacyAliasKey, records []LegacyAliasRecord) LegacyAliasLookup {
	return func(_ context.Context, gotKey LegacyAliasKey) ([]LegacyAliasRecord, error) {
		if gotKey != wantKey {
			return nil, nil
		}
		return append([]LegacyAliasRecord(nil), records...), nil
	}
}

func uciRequireNonDisclosingAliasError(t *testing.T, err error, want string, records []LegacyAliasRecord) {
	t.Helper()
	if err == nil {
		t.Fatalf("Resolve() error = nil, want %q", want)
	}
	if got := err.Error(); got != want {
		t.Fatalf("Resolve() error = %q, want closed code %q", got, want)
	}
	for _, record := range records {
		for _, id := range []*string{record.SpaceID, record.SourceID} {
			if id != nil && strings.Contains(err.Error(), *id) {
				t.Fatalf("Resolve() disclosed alias target ID %q", *id)
			}
		}
	}
}
