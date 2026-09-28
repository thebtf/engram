package gorm

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"path"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

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
	// ParserBundle is an explicit request; omitted replays retain their profile.
	ParserBundle *bool
	// DefaultParserBundle comes from the authenticated daemon capability, never
	// the MCP arguments. It applies only when creating a new checkout.
	DefaultParserBundle bool
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
	locator, valid := canonicalUCILocalGitLocator(in.Locator)
	if !valid || (in.SourceID == "" && validateUCIRequiredText("source_label", in.SourceLabel) != nil) ||
		(in.SourceID != "" && (validateUCIUUID("source_id", in.SourceID) != nil || in.SourceLabel != "")) {
		return RegisteredLocalGit{}, uci.NewContextError(uci.ContextMismatch, nil)
	}
	in.Locator = locator
	profileDigest := localGitGoProfileDigest()
	if (in.ParserBundle != nil && *in.ParserBundle) || (in.ParserBundle == nil && in.DefaultParserBundle) {
		profileDigest = uci.TreeSitterSemanticContractDigest()
	}
	var out RegisteredLocalGit
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if in.AuthRealm == uci.NoAuthCodeRealm {
			// One realm-wide lock serializes capacity checks across distinct Source and Checkout identities.
			if err := tx.Exec(`SELECT pg_advisory_xact_lock(hashtextextended('uci-noauth-code-catalog-capacity', 0))`).Error; err != nil {
				return fmt.Errorf("register local git catalog lock: %w", err)
			}
		}
		now := time.Now().UTC()
		sourceID := in.SourceID
		if sourceID == "" {
			if err := tx.Exec(`SELECT pg_advisory_xact_lock(hashtextextended(concat_ws('|', ?::text, ?::text, ?::text, ?::text), 0))`, in.AuthRealm, in.Principal, in.WorkstationID, in.Locator).Error; err != nil {
				return fmt.Errorf("register local git lock: %w", err)
			}
			var matches []UCICheckout
			if err := tx.Table("ci_checkouts AS checkout").Select("checkout.*").
				Joins("JOIN sources AS source ON source.source_id = checkout.source_id").
				Where("source.auth_realm = ? AND source.kind = ? AND source.state = ? AND source.display_name = ? AND checkout.owner_principal = ? AND checkout.workstation_id = ? AND checkout.locator_ref = ? AND checkout.kind = ? AND checkout.state IN ?", in.AuthRealm, UCISourceGit, UCISourceActive, in.SourceLabel, in.Principal, in.WorkstationID, in.Locator, UCICheckoutWorkingTree, []UCICheckoutState{UCICheckoutRegistered, UCICheckoutWatching, UCICheckoutCatchingUp}).Limit(2).Find(&matches).Error; err != nil {
				return fmt.Errorf("register local git replay lookup: %w", err)
			}
			if len(matches) > 1 {
				return errUCIContextAuthorizationDenied
			}
			if len(matches) == 1 {
				if matches[0].RegistrationProfileID == nil {
					return uci.NewContextError(uci.RegistrationProfileUnbound, nil)
				}
				profileID, err := localGitRegistrationProfileForReplay(tx, &matches[0], profileDigest, in.ParserBundle != nil || in.DefaultParserBundle, in.ParserBundle != nil)
				if err != nil {
					return err
				}
				out = RegisteredLocalGit{matches[0].SourceID, matches[0].CheckoutID, matches[0].IncarnationID, profileID}
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
			if existing.OwnerPrincipal != in.Principal || existing.Kind != UCICheckoutWorkingTree {
				return errUCIContextAuthorizationDenied
			}
			if existing.RegistrationProfileID == nil {
				return uci.NewContextError(uci.RegistrationProfileUnbound, nil)
			}
			profileID, err := localGitRegistrationProfileForReplay(tx, &existing, profileDigest, in.ParserBundle != nil || in.DefaultParserBundle, in.ParserBundle != nil)
			if err != nil {
				return err
			}
			out = RegisteredLocalGit{sourceID, existing.CheckoutID, existing.IncarnationID, profileID}
			return nil
		}
		if !errors.Is(result.Error, gorm.ErrRecordNotFound) {
			return fmt.Errorf("register local git lookup: %w", result.Error)
		}
		if in.AuthRealm == uci.NoAuthCodeRealm && in.Principal == uci.NoAuthCodePrincipal {
			var active int64
			if err := tx.Raw(`
				SELECT COUNT(*) FROM (
					SELECT 1 FROM sources AS source
					JOIN ci_checkouts AS checkout ON checkout.source_id = source.source_id
					WHERE source.auth_realm = ? AND checkout.owner_principal = ?
						AND source.state = ? AND checkout.state IN (?, ?, ?)
					LIMIT ?
				) AS active
			`, uci.NoAuthCodeRealm, uci.NoAuthCodePrincipal, UCISourceActive,
				UCICheckoutRegistered, UCICheckoutWatching, UCICheckoutCatchingUp,
				browserCodeCatalogMaxEntries).Scan(&active).Error; err != nil {
				return fmt.Errorf("register local git catalog capacity: %w", err)
			}
			if active >= browserCodeCatalogMaxEntries {
				return uci.ErrNoAuthCodeCatalogFull
			}
		}
		profile := UCIAnalysisProfile{
			ProfileID: uuid.NewString(), ParserBundleDigest: string(profileDigest),
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
		label := localGitCheckoutDisplayLabel(in.Locator, in.WorkstationID)
		if !validBrowserCodeCheckoutDisplayLabel(label) {
			return uci.NewContextError(uci.ContextMismatch, nil)
		}
		if err := tx.Model(&UCICheckout{}).Where("checkout_id = ?", checkout.CheckoutID).Update("display_name", label).Error; err != nil {
			return fmt.Errorf("register local git checkout label: %w", err)
		}
		out = RegisteredLocalGit{sourceID, checkout.CheckoutID, checkout.IncarnationID, profile.ProfileID}
		return nil
	})
	if err != nil {
		return RegisteredLocalGit{}, err
	}
	return out, nil
}

func localGitCheckoutDisplayLabel(locator, workstationID string) string {
	parsed, _ := url.Parse(locator) // Locator was canonicalized and validated before registration.
	component := func(value string) string {
		var label strings.Builder
		for _, character := range value {
			if !unicode.IsLetter(character) && !unicode.IsNumber(character) && !strings.ContainsRune(" ._-+()", character) {
				character = '-'
			}
			if label.Len()+utf8.RuneLen(character) > 64 {
				break
			}
			label.WriteRune(character)
		}
		return strings.TrimSpace(label.String())
	}
	parent := component(path.Base(path.Dir(parsed.Path)))
	name := component(path.Base(parsed.Path))
	fingerprint := sha256.Sum256([]byte(workstationID))
	return fmt.Sprintf("Worktree · %s › %s · Device %x", parent, name, fingerprint[:4])
}

func localGitGoProfileDigest() uci.IndexDigest {
	profile, _ := uci.GoIndexAdmissionArtifactProfile(uci.GoExtractionProfile{ProfileKey: "go-structure-v1", ParserKey: "go-parser-v1"})
	return profile.ExtractionProfileDigest
}

func localGitRegistrationProfileForReplay(tx *gorm.DB, checkout *UCICheckout, digest uci.IndexDigest, compare, explicit bool) (string, error) {
	if !compare {
		return *checkout.RegistrationProfileID, nil
	}
	var locked UCICheckout
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("checkout_id = ?", checkout.CheckoutID).First(&locked).Error; err != nil {
		return "", fmt.Errorf("register local git checkout lock: %w", err)
	}
	if locked.RegistrationProfileID == nil {
		return "", uci.NewContextError(uci.RegistrationProfileUnbound, nil)
	}
	var profile UCIAnalysisProfile
	if err := tx.Where("profile_id = ?", *locked.RegistrationProfileID).First(&profile).Error; err != nil {
		return "", fmt.Errorf("register local git profile lookup: %w", err)
	}
	if profile.ParserBundleDigest == string(digest) {
		return profile.ProfileID, nil
	}
	if !explicit && profile.ParserBundleDigest == string(localGitGoProfileDigest()) {
		return profile.ProfileID, nil
	}
	if digest != uci.TreeSitterSemanticContractDigest() || profile.ParserBundleDigest == string(localGitGoProfileDigest()) {
		return "", uci.NewContextError(uci.ContextMismatch, nil)
	}
	// Keep the prior profile and its pinned Views immutable; only this checkout
	// selects a new semantic profile for its next complete publication.
	profile.ProfileID = uuid.NewString()
	profile.ParserBundleDigest = string(digest)
	profile.CreatedAt = time.Now().UTC()
	if err := tx.Create(&profile).Error; err != nil {
		return "", fmt.Errorf("register local git semantic profile: %w", err)
	}
	if err := tx.Model(&locked).Update("registration_profile_id", profile.ProfileID).Error; err != nil {
		return "", fmt.Errorf("register local git select semantic profile: %w", err)
	}
	return profile.ProfileID, nil
}

func localGitRegistrationDigest(value string) string {
	sum := sha256.Sum256([]byte(value))
	return "sha256:" + hex.EncodeToString(sum[:])
}

func canonicalUCILocalGitLocator(locator string) (string, bool) {
	if len(locator) < 9 || len(locator) > 4096 || strings.TrimSpace(locator) != locator || strings.ContainsAny(locator, "\r\n\x00") {
		return "", false
	}
	parsed, err := url.Parse(locator)
	if err != nil || parsed.Scheme != "file" || parsed.Opaque != "" || parsed.Host != "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || !strings.HasPrefix(parsed.Path, "/") || strings.HasPrefix(parsed.Path, "//") || strings.Contains(parsed.Path, "\\") || parsed.Path == "/" || parsed.String() != locator {
		return "", false
	}
	for _, component := range strings.Split(parsed.Path, "/") {
		if component == ".." {
			return "", false
		}
	}
	return (&url.URL{Scheme: "file", Path: path.Clean(parsed.Path)}).String(), true
}
