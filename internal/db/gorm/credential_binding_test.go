package gorm

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/thebtf/engram/internal/config"
	"github.com/thebtf/engram/internal/crypto"
	"github.com/thebtf/engram/pkg/models"
)

func TestResolvedCredentialRetainedWitness(t *testing.T) {
	vault, err := crypto.NewVault(&config.Config{EncryptionKey: strings.Repeat("01", 32)})
	require.NoError(t, err)
	verifier := NewProjectConsolidationStore(nil, vault, nil)
	project := "eab45938-96d6-5199-8f58-94d77a87e6f3"
	credential := &models.Credential{ID: 123, Project: project, Scope: "project"}
	key := JSONRaw(`{"id":123}`)
	selector := verifier.opaque("selector", project)
	member := ConsolidationMembership{
		Reference: verifier.opaque("row", struct {
			Table, Column string
			Key           JSONRaw
			Path          []string
		}{"credentials", "project", key, nil}),
		UnresolvedReference: verifier.opaque("unresolved", selector),
		Table:               "credentials", RowKey: key, SourceRowKey: key, FieldPath: JSONRaw(`["project"]`),
		SelectorReference: selector, ProjectID: &project, PlanFingerprint: "fixture", NativeVersion: "1", SourceSnapshot: "fixture-snapshot",
	}
	require.True(t, resolvedCredentialRetainedWitness(verifier, credential, &member))
	historical := member
	historical.SelectorReference = verifier.opaque("selector", "historical-native-namespace")
	historical.UnresolvedReference = verifier.opaque("unresolved", historical.SelectorReference)
	require.True(t, resolvedCredentialRetainedWitness(verifier, credential, &historical))
	foreign := "3f76516f-83fd-5a54-92f3-9ac0a624bb9c"
	for _, mutate := range []func(*ConsolidationMembership){
		func(m *ConsolidationMembership) { m.Reference += "0" },
		func(m *ConsolidationMembership) { m.UnresolvedReference += "0" },
		func(m *ConsolidationMembership) { m.SelectorReference = verifier.opaque("selector", "foreign") },
		func(m *ConsolidationMembership) { m.RowKey = JSONRaw(`{"id":124}`) },
		func(m *ConsolidationMembership) { m.FieldPath = JSONRaw(`["other"]`) },
		func(m *ConsolidationMembership) { m.ProjectID = nil },
		func(m *ConsolidationMembership) { m.ProjectID = &foreign },
		func(m *ConsolidationMembership) { m.PlanFingerprint = "" },
		func(m *ConsolidationMembership) { m.NativeVersion = "" },
		func(m *ConsolidationMembership) { m.SourceSnapshot = "" },
	} {
		copy := member
		mutate(&copy)
		require.False(t, resolvedCredentialRetainedWitness(verifier, credential, &copy))
	}
	require.False(t, resolvedCredentialRetainedWitness(nil, credential, &member))
}

func TestCredentialListBindingsRetainedAssignment(t *testing.T) {
	db, cleanup := openTestDB(t)
	defer cleanup()
	ctx := context.Background()
	vault, err := crypto.NewVault(&config.Config{EncryptionKey: strings.Repeat("01", 32)})
	require.NoError(t, err)
	verifier := NewProjectConsolidationStore(db, vault, nil)
	store := NewCredentialStore(&Store{DB: db})
	project := "eab45938-96d6-5199-8f58-94d77a87e6f3"
	require.NoError(t, db.Create(&Project{ID: project}).Error)
	ciphertext, err := vault.Encrypt("synthetic-private-value")
	require.NoError(t, err)
	credential, err := store.Create(ctx, &models.Credential{Project: project, Scope: "project", Key: "fixture", EncryptedSecret: ciphertext, EncryptionKeyFingerprint: vault.Fingerprint()})
	require.NoError(t, err)
	keyBytes, err := json.Marshal(map[string]int64{"id": credential.ID})
	require.NoError(t, err)
	key := JSONRaw(keyBytes)
	selector := verifier.opaque("selector", "historical-native-namespace")
	member := ConsolidationMembership{
		Reference: verifier.opaque("row", struct {
			Table, Column string
			Key           JSONRaw
			Path          []string
		}{"credentials", "project", key, nil}),
		UnresolvedReference: verifier.opaque("unresolved", selector), Table: "credentials",
		RowKey: key, SourceRowKey: key, FieldPath: JSONRaw(`["project"]`), SelectorReference: selector,
		ProjectID: &project, PlanFingerprint: "fixture", NativeVersion: "1", SourceSnapshot: "fixture-snapshot",
	}
	require.NoError(t, db.Create(&member).Error)
	bindings, err := store.ListBindings(ctx, []*models.Credential{credential}, vault)
	require.NoError(t, err)
	require.Equal(t, CredentialListBinding{ProjectID: project}, bindings[credential.ID])
	fetched, err := store.Get(ctx, project, "fixture")
	require.NoError(t, err)
	require.Equal(t, credential.ID, fetched.ID)
	require.Equal(t, credential.Version, fetched.Version)
	require.Equal(t, ciphertext, fetched.EncryptedSecret)
	plaintext, err := vault.Decrypt(fetched.EncryptedSecret)
	require.NoError(t, err)
	require.Equal(t, "synthetic-private-value", plaintext)
	_, err = store.Get(ctx, "3f76516f-83fd-5a54-92f3-9ac0a624bb9c", "fixture")
	require.Error(t, err)
	require.NoError(t, db.Model(&ConsolidationMembership{}).Where("reference = ?", member.Reference).Update("project_id", nil).Error)
	bindings, err = store.ListBindings(ctx, []*models.Credential{credential}, vault)
	require.NoError(t, err)
	require.Equal(t, CredentialListBinding{UnresolvedReference: member.UnresolvedReference}, bindings[credential.ID])
	require.NoError(t, db.Model(&ConsolidationMembership{}).Where("reference = ?", member.Reference).Updates(map[string]any{"project_id": project, "source_snapshot": ""}).Error)
	bindings, err = store.ListBindings(ctx, []*models.Credential{credential}, vault)
	require.NoError(t, err)
	require.Equal(t, "CREDENTIAL_BINDING_UNAVAILABLE", bindings[credential.ID].UnavailableReason)
	require.NoError(t, db.Model(&ConsolidationMembership{}).Where("reference = ?", member.Reference).Update("source_snapshot", member.SourceSnapshot).Error)
	conflict := member
	conflict.Reference = "invalid-conflicting-witness"
	conflict.ProjectID = nil
	require.NoError(t, db.Create(&conflict).Error)
	bindings, err = store.ListBindings(ctx, []*models.Credential{credential}, vault)
	require.NoError(t, err)
	require.Equal(t, "CREDENTIAL_BINDING_CONFLICT", bindings[credential.ID].UnavailableReason)
}
