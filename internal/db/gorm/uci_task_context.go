package gorm

import (
	"context"

	"github.com/thebtf/engram/internal/projectidentity"
	"github.com/thebtf/engram/internal/taskcontext"
	"github.com/thebtf/engram/internal/uci"
)

// LookupNativeGit selects an existing exact owned checkout. It never registers,
// rewrites a profile or binds a client; normal UCI authorization still follows.
func (store *UCIContextStore) LookupNativeGit(ctx context.Context, caller uci.ResolveContextInput, project, locator string) (uci.RegisteredCheckoutSelector, error) {
	var empty uci.RegisteredCheckoutSelector
	if err := store.requireDB("lookup native git"); err != nil {
		return empty, err
	}
	if err := ctx.Err(); err != nil {
		return empty, err
	}
	if validateUCIContextOwner(caller.AuthRealm, caller.Principal) != nil || validateUCIRequiredText("workstation_id", caller.WorkstationID) != nil {
		return empty, uci.NewContextError(uci.PermissionDenied, nil)
	}
	if !projectidentity.ValidProjectID(project) {
		return empty, taskcontext.Refuse("context_required", "PROJECT_CONTEXT_REQUIRED")
	}
	canonical, valid := canonicalUCILocalGitLocator(locator)
	if !valid || canonical != locator {
		return empty, uci.NewContextError(uci.ContextMismatch, nil)
	}
	type match struct {
		UCICheckout
		ProjectID   *string        `gorm:"column:project_id"`
		SourceState UCISourceState `gorm:"column:source_state"`
		ViewState   *UCIViewState  `gorm:"column:view_state"`
	}
	var matches []match
	err := store.db.WithContext(ctx).Table("ci_checkouts AS checkout").Select("checkout.*, source.project_id, source.state AS source_state, view_row.state AS view_state").
		Joins("JOIN sources AS source ON source.source_id = checkout.source_id").
		Joins("LEFT JOIN ci_views AS view_row ON view_row.view_id = checkout.current_view_id").
		Where("source.auth_realm = ? AND source.kind = ? AND checkout.owner_principal = ? AND checkout.workstation_id = ? AND checkout.locator_ref = ? AND checkout.kind = ?", caller.AuthRealm, UCISourceGit, caller.Principal, caller.WorkstationID, locator, UCICheckoutWorkingTree).
		Limit(2).Find(&matches).Error
	if err != nil {
		return empty, err
	}
	if len(matches) == 0 {
		return empty, uci.NewContextError(uci.ContextRequired, nil)
	}
	if len(matches) != 1 {
		return empty, taskcontext.Refuse("forbidden", "CONTEXT_MISMATCH")
	}
	found := matches[0]
	if found.ProjectID == nil || *found.ProjectID != project {
		return empty, taskcontext.Refuse("context_required", "PROJECT_CONTEXT_MISMATCH")
	}
	if found.SourceState == UCISourceRetired {
		return empty, taskcontext.Refuse("stale", "VIEW_RETIRED")
	}
	if found.SourceState != UCISourceActive {
		return empty, taskcontext.Refuse("unavailable", "SOURCE_UNAVAILABLE")
	}
	if found.State == UCICheckoutOffline {
		return empty, taskcontext.Refuse("stale", "CHECKOUT_OFFLINE")
	}
	if found.State == UCICheckoutCatchingUp {
		return empty, taskcontext.Refuse("stale", "INDEX_CATCHING_UP")
	}
	if !isUCIIndexBindingCheckoutState(found.State) {
		return empty, taskcontext.Refuse("context_required", "CONTEXT_REQUIRED")
	}
	if found.RegistrationProfileID == nil {
		return empty, uci.NewContextError(uci.RegistrationProfileUnbound, nil)
	}
	if found.CurrentViewID == nil {
		return empty, taskcontext.Refuse("not_ready", "OPERATION_NOT_READY")
	}
	if found.ViewState == nil || *found.ViewState != UCIViewPublished {
		return empty, taskcontext.Refuse("stale", "VIEW_RETIRED")
	}
	return uci.RegisteredCheckoutSelector{Scope: uci.IndexScope{SourceID: found.SourceID, CheckoutID: found.CheckoutID, IncarnationID: found.IncarnationID}, ProfileID: *found.RegistrationProfileID}, nil
}
