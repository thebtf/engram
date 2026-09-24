package gorm

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/thebtf/engram/internal/uci"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// RegisterLocalGitInput is authenticated client evidence, not authority from a path.
// The server supplies realm, principal, workstation, and every new identifier.
type RegisterLocalGitInput struct {
	AuthRealm, Principal, WorkstationID string
	SourceID, SourceLabel, Locator      string
}

type RegisteredLocalGit struct {
	SourceID, CheckoutID, IncarnationID, ProfileID string
}

// RegisterLocalGit registers one working tree. Reusing a Source requires an
// existing checkout owned by this exact principal in the same realm.
func (s *UCIContextStore) RegisterLocalGit(ctx context.Context, in RegisterLocalGitInput) (RegisteredLocalGit, error) {
	if err := s.requireDB("register local git"); err != nil {
		return RegisteredLocalGit{}, err
	}
	if ctx == nil {
		return RegisteredLocalGit{}, uci.NewContextError(uci.ContextMismatch, nil)
	}
	if err := ctx.Err(); err != nil {
		return RegisteredLocalGit{}, err
	}
	if validateUCIContextOwner(in.AuthRealm, in.Principal) != nil || validateUCIRequiredText("workstation_id", in.WorkstationID) != nil {
		return RegisteredLocalGit{}, errUCIContextAuthorizationDenied
	}
	if !validUCILocalGitLocator(in.Locator) || (in.SourceID == "" && validateUCIRequiredText("source_label", in.SourceLabel) != nil) ||
		(in.SourceID != "" && (validateUCIUUID("source_id", in.SourceID) != nil || in.SourceLabel != "")) {
		return RegisteredLocalGit{}, uci.NewContextError(uci.ContextMismatch, nil)
	}
	var out RegisteredLocalGit
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		now := time.Now().UTC()
		sourceID := in.SourceID
		if sourceID == "" {
			if err := tx.Exec(`SELECT pg_advisory_xact_lock(hashtextextended(concat_ws('|', ?::text, ?::text, ?::text, ?::text), 0))`, in.AuthRealm, in.Principal, in.WorkstationID, in.Locator).Error; err != nil {
				return fmt.Errorf("register local git lock: %w", err)
			}
			var matches []UCICheckout
			if err := tx.Table("ci_checkouts AS checkout").Select("checkout.*").
				Joins("JOIN sources AS source ON source.source_id = checkout.source_id").
				Where("source.auth_realm = ? AND source.kind = ? AND source.state = ? AND source.display_name = ? AND checkout.owner_principal = ? AND checkout.workstation_id = ? AND checkout.locator_ref = ? AND checkout.kind = ? AND checkout.state IN ? AND checkout.registration_profile_id IS NOT NULL", in.AuthRealm, UCISourceGit, UCISourceActive, in.SourceLabel, in.Principal, in.WorkstationID, in.Locator, UCICheckoutWorkingTree, []UCICheckoutState{UCICheckoutRegistered, UCICheckoutWatching, UCICheckoutCatchingUp}).Limit(2).Find(&matches).Error; err != nil {
				return fmt.Errorf("register local git replay lookup: %w", err)
			}
			if len(matches) > 1 {
				return errUCIContextAuthorizationDenied
			}
			if len(matches) == 1 {
				out = RegisteredLocalGit{matches[0].SourceID, matches[0].CheckoutID, matches[0].IncarnationID, *matches[0].RegistrationProfileID}
				return nil
			}
			sourceID = uuid.NewString()
			source := UCISource{SourceID: sourceID, AuthRealm: in.AuthRealm, Kind: UCISourceGit, DisplayName: in.SourceLabel, State: UCISourceActive, CreatedAt: now, UpdatedAt: now}
			if err := tx.Create(&source).Error; err != nil {
				return fmt.Errorf("register local git source: %w", err)
			}
		} else {
			var existing UCISource
			if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("source_id = ? AND auth_realm = ? AND kind = ? AND state = ?", sourceID, in.AuthRealm, UCISourceGit, UCISourceActive).First(&existing).Error; err != nil {
				return errUCIContextAuthorizationDenied
			}
			var owned int64
			if err := tx.Model(&UCICheckout{}).Where("source_id = ? AND owner_principal = ? AND state IN ?", sourceID, in.Principal, []UCICheckoutState{UCICheckoutRegistered, UCICheckoutWatching, UCICheckoutCatchingUp}).Count(&owned).Error; err != nil {
				return fmt.Errorf("register local git owner: %w", err)
			}
			if owned == 0 {
				return errUCIContextAuthorizationDenied
			}
		}
		var existing UCICheckout
		result := tx.Where("source_id = ? AND workstation_id = ? AND locator_ref = ? AND state IN ?", sourceID, in.WorkstationID, in.Locator, []UCICheckoutState{UCICheckoutRegistered, UCICheckoutWatching, UCICheckoutCatchingUp}).First(&existing)
		if result.Error == nil {
			if existing.OwnerPrincipal != in.Principal || existing.Kind != UCICheckoutWorkingTree || existing.RegistrationProfileID == nil {
				return errUCIContextAuthorizationDenied
			}
			out = RegisteredLocalGit{sourceID, existing.CheckoutID, existing.IncarnationID, *existing.RegistrationProfileID}
			return nil
		}
		if !errors.Is(result.Error, gorm.ErrRecordNotFound) {
			return fmt.Errorf("register local git lookup: %w", result.Error)
		}
		profile := UCIAnalysisProfile{
			ProfileID: uuid.NewString(), ParserBundleDigest: string(uci.TreeSitterBundleDigest()),
			ResolverRevision: "uci-resolver-v1", ChunkerRevision: "uci-chunker-v1",
			IgnorePolicyDigest: localGitRegistrationDigest("uci-git-ignore-v1"), BuildContextJSON: `{}`,
			SecretPolicyRevision: "uci-secret-policy-v1", CreatedAt: now,
		}
		if err := tx.Create(&profile).Error; err != nil {
			return fmt.Errorf("register local git profile: %w", err)
		}
		checkout := UCICheckout{
			CheckoutID: uuid.NewString(), SourceID: sourceID, WorkstationID: in.WorkstationID,
			IncarnationID: uuid.NewString(), Kind: UCICheckoutWorkingTree, OwnerPrincipal: in.Principal,
			LocatorRef: in.Locator, RegistrationProfileID: &profile.ProfileID, State: UCICheckoutRegistered, CreatedAt: now, UpdatedAt: now,
		}
		if err := tx.Create(&checkout).Error; err != nil {
			return fmt.Errorf("register local git checkout: %w", err)
		}
		out = RegisteredLocalGit{sourceID, checkout.CheckoutID, checkout.IncarnationID, profile.ProfileID}
		return nil
	})
	if err != nil {
		return RegisteredLocalGit{}, err
	}
	return out, nil
}

func localGitRegistrationDigest(value string) string {
	sum := sha256.Sum256([]byte(value))
	return "sha256:" + hex.EncodeToString(sum[:])
}

func validUCILocalGitLocator(locator string) bool {
	if len(locator) < 9 || len(locator) > 4096 || strings.TrimSpace(locator) != locator || strings.ContainsAny(locator, "\r\n\x00") {
		return false
	}
	parsed, err := url.Parse(locator)
	return err == nil && parsed.Scheme == "file" && parsed.Host == "" && parsed.User == nil && parsed.RawQuery == "" && parsed.Fragment == "" && strings.HasPrefix(parsed.Path, "/") && parsed.Path != "/" && parsed.String() == locator
}
